package router

import (
	"context"
	"fmt"
	"log/slog"
	"os/signal"
	"sync/atomic"
	"syscall"

	accountController "github.com/cksidharthan/lavender/pkg/api/v1/account/controller"
	feedsController "github.com/cksidharthan/lavender/pkg/api/v1/feeds/controller"
	healthzController "github.com/cksidharthan/lavender/pkg/api/v1/health/controller"
	"github.com/cksidharthan/lavender/pkg/config"
	"github.com/cksidharthan/lavender/pkg/iconcache"
	"github.com/cksidharthan/lavender/pkg/mfa"
	"github.com/cksidharthan/lavender/pkg/middlewares"
	"github.com/cksidharthan/lavender/pkg/otel"
	lavenderPocketbase "github.com/cksidharthan/lavender/pkg/pocketbase"
	uiHandlers "github.com/cksidharthan/lavender/pkg/ui/handlers"
	"github.com/cksidharthan/lavender/pkg/worker"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	pb "github.com/pocketbase/pocketbase"
)

func RunHTTPServer(logger *slog.Logger, appConfig *config.Config, lavenderOTEL *otel.LavenderOTEL, pbApp *pb.PocketBase) error {
	var isShuttingDown atomic.Bool

	mainAppServerContext, mainAppServerStop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer mainAppServerStop()

	mainChiRouter := chi.NewRouter()
	// tracing goes first, so the request logs sent through the otel log handler are correlated to the trace. The health checks are not traced.
	mainChiRouter.Use(middlewares.Tracing(lavenderOTEL.TraceProvider, "/api/v1/healthz"))
	// the request logger is outermost after that, so it also logs the 500s written by the recoverer.
	mainChiRouter.Use(middlewares.RequestLogger(logger))
	mainChiRouter.Use(middleware.Recoverer)
	mainChiRouter.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "PUT", "POST", "DELETE", "HEAD", "OPTION"},
		AllowedHeaders:   []string{"User-Agent", "Content-Type", "Accept", "Accept-Encoding", "Accept-Language", "Cache-Control", "Connection", "DNT", "Host", "Origin", "Pragma", "Referer"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300, // Maximum value not ignored by any of major browsers
	}))
	apiRouterGroup := chi.NewRouter()
	v1RouterGroup := chi.NewRouter()

	// the pocketbase OnServe hook always has to run, as it starts the pocketbase cron jobs
	// (log cleanup, auto backups), even when the admin ui is disabled.
	pbHandler, err := lavenderPocketbase.NewHandler(pbApp)
	if err != nil {
		return fmt.Errorf("error initializing pocketbase handler: %w", err)
	}

	// pocketbase (REST API, realtime, admin dashboard) is only exposed when the admin ui is enabled,
	// as the fallback for every path the lavender routes do not handle. This has to be set before
	// any Mount call so that the sub routers inherit it.
	if !appConfig.PocketBaseAdminDisabled {
		mainChiRouter.NotFound(pbHandler.ServeHTTP)
	}

	// the workers fetch the articles of the feeds, they stop with the server.
	feedWorkers := worker.New(pbApp, logger, appConfig.FeedWorkers, appConfig.FeedRefreshInterval)
	workersContext, stopWorkers := context.WithCancel(mainAppServerContext)
	feedWorkers.Start(workersContext)
	defer func() {
		stopWorkers()
		feedWorkers.Wait()
	}()

	// healthz Controller
	healthzCtrl, err := healthzController.New(v1RouterGroup, logger, &isShuttingDown, lavenderOTEL.MetricsProvider.Meter("lavender_healthz_metrics"))
	if err != nil {
		return fmt.Errorf("error initializing healthz controller: %w", err)
	}
	healthzCtrl.RegisterRoutes()

	iconCache, err := iconcache.New(appConfig.IconCacheDir)
	if err != nil {
		return fmt.Errorf("error creating the icon cache: %w", err)
	}

	key, err := mfa.LoadKey(appConfig.SecretKey, appConfig.PocketBaseDataDir)
	if err != nil {
		return fmt.Errorf("error loading the secret key: %w", err)
	}
	mfaService := mfa.New(pbApp, key)

	// the settings of the user, and the export and import of everything of the user
	accountController.New(v1RouterGroup, logger, pbApp, uiHandlers.AuthCookieName, iconCache, mfaService).RegisterRoutes()

	// folders and feeds, used by the ui
	feedsController.New(v1RouterGroup, logger, pbApp, uiHandlers.AuthCookieName, feedWorkers).RegisterRoutes()

	// ui (templ + htmx) pages
	uiHandlers.NewAuthHandler(mainChiRouter, logger, pbApp, uiHandlers.Options{
		Icons:          iconCache,
		MFA:            mfaService,
		DefaultRefresh: appConfig.FeedRefreshInterval,
		DataDir:        appConfig.PocketBaseDataDir,
	}).RegisterRoutes()

	// mounts all v1 router groups onto /api routergroup
	apiRouterGroup.Mount("/v1", v1RouterGroup)
	// mount all the subRoutes to the main chi router.
	mainChiRouter.Mount("/api", apiRouterGroup)

	appServer := NewServer(appConfig, mainChiRouter, logger, &isShuttingDown, lavenderOTEL, pbApp)
	err = appServer.Run(mainAppServerContext)
	if err != nil {
		return fmt.Errorf("error running server :%v", err)
	}

	err = appServer.shutdown()
	if err != nil {
		return fmt.Errorf("error shutting down http server :%v", err)
	}

	return nil
}

package router

import (
	"context"
	"fmt"
	"log/slog"
	"os/signal"
	"sync/atomic"
	"syscall"

	healthzController "github.com/cksidharthan/lavender/pkg/api/v1/health/controller"
	"github.com/cksidharthan/lavender/pkg/config"
	"github.com/cksidharthan/lavender/pkg/middlewares"
	"github.com/cksidharthan/lavender/pkg/otel"
	lavenderPocketbase "github.com/cksidharthan/lavender/pkg/pocketbase"
	uiHandlers "github.com/cksidharthan/lavender/pkg/ui/handlers"
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

	// healthz Controller
	healthzCtrl, err := healthzController.New(v1RouterGroup, logger, &isShuttingDown, lavenderOTEL.MetricsProvider.Meter("lavender_healthz_metrics"))
	if err != nil {
		return fmt.Errorf("error initializing healthz controller: %w", err)
	}
	healthzCtrl.RegisterRoutes()

	// ui (templ + htmx) pages
	uiHandlers.NewAuthHandler(mainChiRouter, logger, pbApp).RegisterRoutes()

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

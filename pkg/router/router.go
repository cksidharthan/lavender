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
	"github.com/cksidharthan/lavender/pkg/otel"
	lavenderPocketbase "github.com/cksidharthan/lavender/pkg/pocketbase"
	"github.com/go-chi/chi/v5"
	pb "github.com/pocketbase/pocketbase"
)

func RunHTTPServer(logger *slog.Logger, appConfig *config.Config, lavenderOTEL *otel.LavenderOTEL, pbApp *pb.PocketBase) error {
	var isShuttingDown atomic.Bool

	mainAppServerContext, mainAppServerStop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer mainAppServerStop()

	mainChiRouter := chi.NewRouter()
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

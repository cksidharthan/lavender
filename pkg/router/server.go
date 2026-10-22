package router

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cksidharthan/lavender/pkg/config"
	lavenderLogger "github.com/cksidharthan/lavender/pkg/logger"
	"github.com/cksidharthan/lavender/pkg/otel"
	"github.com/go-chi/chi/v5"
	pb "github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
)

const otelShutdownTimeout = 5 * time.Second

type Server struct {
	appConfig                      *config.Config
	http                           *http.Server
	logger                         *slog.Logger
	shuttingDown                   *atomic.Bool
	stopOngoing                    context.CancelFunc
	proporateWait, shutdownTimeout time.Duration
	lavenderOTEL                   *otel.LavenderOTEL
	pocketbaseApp                  *pb.PocketBase
}

func NewServer(appConfig *config.Config, chiRouter chi.Router, logger *slog.Logger,
	shuttingDown *atomic.Bool, lavenderOTEL *otel.LavenderOTEL, pocketbase *pb.PocketBase) *Server {
	ongoingContext, stopOngoingContext := context.WithCancel(context.Background())

	return &Server{
		appConfig: appConfig,
		http: &http.Server{
			Addr:              fmt.Sprintf(":%d", appConfig.HTTPServerPort),
			Handler:           chiRouter,
			BaseContext:       func(net.Listener) context.Context { return ongoingContext },
			ReadHeaderTimeout: 5 * time.Second,
		},
		logger:       logger,
		shuttingDown: shuttingDown,
		stopOngoing:  stopOngoingContext,
		// TODO: should this be retrieved from appConfig? I see this might be useful if the app runs at scale, but not right now (or maybe never :D)
		proporateWait:   time.Second,
		shutdownTimeout: time.Duration(appConfig.ShutdownWaitSeconds) * time.Second,
		lavenderOTEL:    lavenderOTEL,
		pocketbaseApp:   pocketbase,
	}
}

func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)

	go func() {
		if err := s.http.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	if !s.appConfig.PocketBaseAdminDisabled {
		go func() {
			if err := apis.Serve(s.pocketbaseApp, apis.ServeConfig{
				HttpAddr:        fmt.Sprintf(":%d", s.appConfig.PocketBaseAdminPort),
				ShowStartBanner: false,
			}); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("error starting up pocketbase admin ui")
			}
		}()
	}

	select {
	case err := <-errCh:
		return fmt.Errorf("error in starting http server :%v", err)
	case <-ctx.Done():
	}

	return nil
}

// shutdownOTEL flushes and stops all telemetry providers in parallel under a
// short shared deadline so an unreachable collector cannot stall shutdown.
func (s *Server) shutdownOTEL() {
	ctx, cancel := context.WithTimeout(context.Background(), otelShutdownTimeout)
	defer cancel()

	providers := []struct {
		name     string
		shutdown func(context.Context) error
	}{
		{"tracer", s.lavenderOTEL.TraceProvider.Shutdown},
		{"metrics", s.lavenderOTEL.MetricsProvider.Shutdown},
		{"logger", s.lavenderOTEL.LoggerProvider.Shutdown},
	}

	var wg sync.WaitGroup
	for _, p := range providers {
		wg.Go(func() {
			if err := p.shutdown(ctx); err != nil {
				s.logger.Error(fmt.Sprintf("error shutting down %s provider", p.name), slog.Any(lavenderLogger.ErrorKey, err))
			}
		})
	}
	wg.Wait()
}

func (s *Server) shutdown() error {
	s.shuttingDown.Store(true)
	s.logger.Info("received stop signal. shutting down")

	// wait for sometime so that readiness probws get information that the service is unavailalable
	time.Sleep(s.proporateWait)

	serverShutdownContext, serverShutdownCtxCancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
	defer serverShutdownCtxCancel()

	// drain in-flight requests first so their spans and logs are still exported.
	err := s.http.Shutdown(serverShutdownContext)
	s.stopOngoing()

	s.shutdownOTEL()

	if err != nil {
		return err
	}

	// OnTerminate gracefully stops the pocketbase admin server (bound by apis.Serve)
	// and runs its other cleanup hooks, then the app bootstrap is cleared.
	terminateEvent := new(core.TerminateEvent)
	terminateEvent.App = s.pocketbaseApp
	if err := s.pocketbaseApp.OnTerminate().Trigger(terminateEvent, func(e *core.TerminateEvent) error {
		return e.App.ClearBootstrap()
	}); err != nil {
		return err
	}

	s.logger.Info("server shutdown successfully", slog.String(lavenderLogger.AddressKey, s.http.Addr))

	return nil
}

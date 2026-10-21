package router

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/cksidharthan/lavender/pkg/config"
	lavenderLogger "github.com/cksidharthan/lavender/pkg/logger"
	"github.com/cksidharthan/lavender/pkg/otel"
	"github.com/go-chi/chi/v5"
)

type Server struct {
	http                           *http.Server
	logger                         *slog.Logger
	shuttingDown                   *atomic.Bool
	stopOngoing                    context.CancelFunc
	proporateWait, shutdownTimeout time.Duration
	lavenderOTEL                   *otel.LavenderOTEL
}

func NewServer(appConfig *config.Config, chiRouter chi.Router, logger *slog.Logger, shuttingDown *atomic.Bool, lavenderOTEL *otel.LavenderOTEL) *Server {
	ongoingContext, stopOngoingContext := context.WithCancel(context.Background())

	return &Server{
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
		shutdownTimeout: time.Duration(appConfig.ShutdownWaitSeconds),
		lavenderOTEL:    lavenderOTEL,
	}
}

func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)

	go func() {
		if err := s.http.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("error in starting http server :%v", err)
	case <-ctx.Done():
	}

	return nil
}

func (s *Server) shutdown() error {
	s.shuttingDown.Store(true)
	s.logger.Info("received stop signal. shutting down")

	// wait for sometime so that readiness probws get information that the service is unavailalable
	time.Sleep(s.proporateWait)

	serverShutdownContext, serverShutdownCtxCancel := context.WithTimeout(context.Background(), time.Minute)
	defer serverShutdownCtxCancel()

	if err := s.lavenderOTEL.TraceProvider.Shutdown(serverShutdownContext); err != nil {
		s.logger.ErrorContext(serverShutdownContext, "error shutting down tracer provider", slog.Any(lavenderLogger.ErrorKey, err))
	}

	if err := s.lavenderOTEL.MetricsProvider.Shutdown(serverShutdownContext); err != nil {
		s.logger.ErrorContext(serverShutdownContext, "error shutting down metrics provider", slog.Any(lavenderLogger.ErrorKey, err))
	}

	if err := s.lavenderOTEL.LoggerProvider.Shutdown(serverShutdownContext); err != nil {
		s.logger.ErrorContext(serverShutdownContext, "error shutting down logger provider", slog.Any(lavenderLogger.ErrorKey, err))
	}

	err := s.http.Shutdown(serverShutdownContext)
	s.stopOngoing()
	if err != nil {
		return err
	}

	s.logger.Info("server shutdown successfully", slog.String(lavenderLogger.AddressKey, s.http.Addr))

	return nil
}

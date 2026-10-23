package controller

import (
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const healthzEndpoint = "/healthz"

type HealthzController struct {
	chiRouter            chi.Router
	logger               *slog.Logger
	isShuttingDown       *atomic.Bool
	requestCounterMetric metric.Int64Counter
}

type HealthzResponse struct {
	Status string `json:"status"`
}

func New(chiRouter chi.Router, logger *slog.Logger, isShuttingDown *atomic.Bool, otelMeter metric.Meter) (*HealthzController, error) {
	requestMetricCounter, err := otelMeter.Int64Counter("lavender.healthz.count")
	if err != nil {
		logger.Error("unable to create healthz checks counter")
		return nil, fmt.Errorf("error creating healthz checks counter: %w", err)
	}

	return &HealthzController{
		chiRouter:            chiRouter,
		logger:               logger,
		isShuttingDown:       isShuttingDown,
		requestCounterMetric: requestMetricCounter,
	}, nil
}

func (hc *HealthzController) RegisterRoutes() {
	hc.logger.Info(fmt.Sprintf("registering %s endpoint", healthzEndpoint))
	hc.chiRouter.Get(healthzEndpoint, hc.healthzHandler)
}

func (hc *HealthzController) healthzHandler(w http.ResponseWriter, r *http.Request) {
	if hc.isShuttingDown.Load() {
		hc.requestCounterMetric.Add(r.Context(), 1, metric.WithAttributes(attribute.String("status", "unhealthy")))
		render.Status(r, http.StatusServiceUnavailable)
		render.JSON(w, r, &HealthzResponse{
			Status: "unhealthy",
		})

		return
	}

	hc.requestCounterMetric.Add(r.Context(), 1, metric.WithAttributes(attribute.String("status", "healthy")))
	render.Status(r, http.StatusOK)
	render.JSON(w, r, &HealthzResponse{
		Status: "healthy",
	})
}

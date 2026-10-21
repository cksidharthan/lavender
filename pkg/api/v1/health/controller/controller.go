package controller

import (
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"
)

const healthzEndpoint = "/healthz"

type HealthzController struct {
	chiRouter      chi.Router
	logger         *slog.Logger
	isShuttingDown *atomic.Bool
}

type HealthzResponse struct {
	Status string `json:"status"`
}

func New(chiRouter chi.Router, logger *slog.Logger, isShuttingDown *atomic.Bool) *HealthzController {
	return &HealthzController{
		chiRouter:      chiRouter,
		logger:         logger,
		isShuttingDown: isShuttingDown,
	}
}

func (hc *HealthzController) RegisterRoutes() {
	hc.logger.Info(fmt.Sprintf("registering %s endpoint", healthzEndpoint))
	hc.chiRouter.Get(healthzEndpoint, hc.healthzHandler)
}

func (hc *HealthzController) healthzHandler(w http.ResponseWriter, r *http.Request) {
	if hc.isShuttingDown.Load() {
		render.Status(r, http.StatusServiceUnavailable)

		return
	}
	render.Status(r, http.StatusOK)
	render.JSON(w, r, &HealthzResponse{
		Status: "healthy",
	})
}

package controller

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"
)

type HealthzController struct {
	chiRouter chi.Router
	logger    *slog.Logger
}

func New(chiRouter chi.Router, logger *slog.Logger) *HealthzController {
	return &HealthzController{
		chiRouter: chiRouter,
		logger:    logger,
	}
}

func (hc *HealthzController) RegisterRoutes() {
	hc.chiRouter.Get("/healthz", healthzHandler)
}

type HealthzResponse struct {
	Status string `json:"status"`
}

func healthzHandler(w http.ResponseWriter, r *http.Request) {
	render.Status(r, http.StatusOK)
	render.JSON(w, r, &HealthzResponse{
		Status: "healthy",
	})
}

package router

import (
	"log/slog"
	"net/http"

	healthzController "github.com/cksidharthan/lavender/api/v1/health/controller"
	"github.com/go-chi/chi/v5"
)

func RunServer(logger *slog.Logger) {
	mainChiRouter := chi.NewRouter()

	v1RouterGroup := chi.NewRouter()

	healthzController.New(v1RouterGroup, logger).RegisterRoutes()

	// mount all the subRoutes
	mainChiRouter.Mount("/v1", v1RouterGroup)

	http.ListenAndServe(":8080", mainChiRouter)
}

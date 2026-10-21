package cmd

import (
	"log/slog"

	"github.com/cksidharthan/lavender/router"
)

func Start() {
	appLogger := slog.Default()
	router.RunServer(appLogger)
}

package cmd

import (
	"fmt"

	"github.com/cksidharthan/lavender/pkg/config"
	"github.com/cksidharthan/lavender/pkg/logger"
	"github.com/cksidharthan/lavender/pkg/otel"
	"github.com/cksidharthan/lavender/pkg/pocketbase"
	"github.com/cksidharthan/lavender/pkg/router"
)

func Start() error {
	appConfig, err := config.New()
	if err != nil {
		return fmt.Errorf("error loading application config :%v", err)
	}

	appLogger := logger.New(appConfig)

	lavenderOTEL, err := otel.New(appLogger, appConfig)
	if err != nil {
		return fmt.Errorf("error configuring otel :%v", err)
	}

	pocketbaseApp, err := pocketbase.InitializePocketbase(appConfig, appLogger)
	if err != nil {
		return fmt.Errorf("error initializing pocketbase: %v", err)
	}

	err = router.RunHTTPServer(appLogger, appConfig, lavenderOTEL, pocketbaseApp)
	if err != nil {
		return fmt.Errorf("error running app server :%v", err)
	}

	return nil
}

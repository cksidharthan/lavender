package cmd

import (
	"fmt"

	"github.com/cksidharthan/lavender/pkg/config"
	"github.com/cksidharthan/lavender/pkg/logger"
	"github.com/cksidharthan/lavender/pkg/router"
)

func Start() error {
	appConfig, err := config.New()
	if err != nil {
		return fmt.Errorf("error loading application config :%v", err)
	}

	appLogger := logger.New(appConfig)

	err = router.RunServer(appLogger, appConfig)
	if err != nil {
		return fmt.Errorf("error running app server :%v", err)
	}

	return nil
}

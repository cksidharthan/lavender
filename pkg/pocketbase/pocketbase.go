package pocketbase

import (
	"fmt"
	"log/slog"

	"github.com/cksidharthan/lavender/pkg/config"
	_ "github.com/cksidharthan/lavender/pkg/pocketbase/migrations"
	pb "github.com/pocketbase/pocketbase"
)

func InitializePocketbase(appConfig *config.Config, logger *slog.Logger) (*pb.PocketBase, error) {
	app := pb.NewWithConfig(pb.Config{
		DefaultDataDir:  appConfig.PocketBaseDataDir,
		HideStartBanner: true,
	})

	if err := app.Bootstrap(); err != nil {
		return nil, fmt.Errorf("failed to bootstrap pocketbase: %w", err)
	}

	if err := app.RunAppMigrations(); err != nil {
		return nil, fmt.Errorf("error running pocketbase app migrations: %w", err)
	}

	if err := app.ReloadCachedCollections(); err != nil {
		return nil, fmt.Errorf("failed to reload pocketbase collections: %w", err)
	}

	logger.Info("pocketbase initialized", slog.String("data_dir", appConfig.PocketBaseDataDir))

	return app, nil
}

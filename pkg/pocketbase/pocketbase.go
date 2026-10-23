package pocketbase

import (
	"fmt"
	"log/slog"

	"github.com/cksidharthan/lavender/pkg/config"
	"github.com/cksidharthan/lavender/pkg/pocketbase/db"
	_ "github.com/cksidharthan/lavender/pkg/pocketbase/migrations"
	pb "github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
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

	// The password of an account that has two-factor authentication is not enough, also not through the API of PocketBase.
	// The second step is in the application, so signing in with the password alone is refused here.
	app.OnRecordAuthWithPasswordRequest(db.UsersCollection).BindFunc(func(e *core.RecordAuthWithPasswordRequestEvent) error {
		if e.Record != nil && e.Record.GetBool("totp_enabled") {
			return apis.NewForbiddenError("This account has two-factor authentication, sign in through the application.", nil)
		}

		return e.Next()
	})

	logger.Info("pocketbase initialized", slog.String("data_dir", appConfig.PocketBaseDataDir))

	return app, nil
}

package migrations

import (
	"github.com/cksidharthan/lavender/pkg/pocketbase/db"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// the two factor authentication of a user: the secret of the app, sealed, whether it is on, the hashes of the recovery
// codes and the last step of the app that was used. They are hidden, they are never in an answer of the PocketBase API.
func init() {
	migrations.Register(func(app core.App) error {
		users, err := app.FindCollectionByNameOrId(db.UsersCollection)
		if err != nil {
			return err
		}

		users.Fields.Add(
			&core.TextField{Name: "totp_secret", Hidden: true, Max: 512},
			&core.BoolField{Name: "totp_enabled", Hidden: true},
			&core.TextField{Name: "totp_recovery", Hidden: true, Max: 2048},
			&core.NumberField{Name: "totp_last_step", Hidden: true, OnlyInt: true},
		)

		return app.Save(users)
	}, func(app core.App) error {
		users, err := app.FindCollectionByNameOrId(db.UsersCollection)
		if err != nil {
			return err
		}

		for _, name := range []string{"totp_secret", "totp_enabled", "totp_recovery", "totp_last_step"} {
			users.Fields.RemoveByName(name)
		}

		return app.Save(users)
	})
}

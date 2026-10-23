package migrations

import (
	"github.com/cksidharthan/lavender/pkg/pocketbase/db"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// the refresh interval a user wants for the feeds that have none of their own.
func init() {
	migrations.Register(func(app core.App) error {
		users, err := app.FindCollectionByNameOrId(db.UsersCollection)
		if err != nil {
			return err
		}

		minimum, maximum := 0.0, 1440.0
		// 0 is the default interval of the application
		users.Fields.Add(&core.NumberField{Name: "default_refresh_minutes", OnlyInt: true, Min: &minimum, Max: &maximum})

		return app.Save(users)
	}, func(app core.App) error {
		users, err := app.FindCollectionByNameOrId(db.UsersCollection)
		if err != nil {
			return err
		}

		users.Fields.RemoveByName("default_refresh_minutes")

		return app.Save(users)
	})
}

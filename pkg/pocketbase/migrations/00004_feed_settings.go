package migrations

import (
	"github.com/cksidharthan/lavender/pkg/pocketbase/db"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// the settings of a feed: its own refresh interval, hiding it from the home page and an icon of the user's own.
func init() {
	migrations.Register(func(app core.App) error {
		feeds, err := app.FindCollectionByNameOrId(db.FeedsCollection)
		if err != nil {
			return err
		}

		minimum, maximum := 0.0, 1440.0
		feeds.Fields.Add(
			// 0 is the default interval of the application
			&core.NumberField{Name: "refresh_minutes", OnlyInt: true, Min: &minimum, Max: &maximum},
			&core.BoolField{Name: "hide_from_home"},
			&core.TextField{Name: "icon_override", Max: 512},
		)

		return app.Save(feeds)
	}, func(app core.App) error {
		feeds, err := app.FindCollectionByNameOrId(db.FeedsCollection)
		if err != nil {
			return err
		}

		feeds.Fields.RemoveByName("refresh_minutes")
		feeds.Fields.RemoveByName("hide_from_home")
		feeds.Fields.RemoveByName("icon_override")

		return app.Save(feeds)
	})
}

package migrations

import (
	"github.com/cksidharthan/lavender/pkg/pocketbase/db"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

func init() {
	migrations.Register(func(app core.App) error {
		collection := core.NewBaseCollection(db.FeedsCollection)
		collection.Fields.Add(
			&core.TextField{Name: "feed_url", Required: true, Max: 512},
			&core.TextField{Name: "site_url", Max: 512},
			&core.TextField{Name: "title", Max: 255},
			&core.EditorField{Name: "description"},
			&core.SelectField{
				Name:      "feed_type",
				Required:  true,
				MaxSelect: 1,
				Values:    db.FeedTypes,
			},
			&core.TextField{Name: "icon_url", Max: 512},
			&core.TextField{Name: "language", Max: 16},
			&core.BoolField{Name: "is_active"},
			&core.DateField{Name: "last_fetched_at"},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)

		// Avoid storing the same feed twice across the deployment.
		collection.AddIndex("idx_feeds_feed_url", true, "feed_url", "")

		return app.Save(collection)
	}, func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId(db.FeedsCollection)
		if err != nil {
			return err
		}

		return app.Delete(collection)
	})
}

package migrations

import (
	"github.com/cksidharthan/lavender/pkg/pocketbase/db"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// the audio file of an article, as in the feed of a podcast.
func init() {
	migrations.Register(func(app core.App) error {
		articles, err := app.FindCollectionByNameOrId(db.ArticlesCollection)
		if err != nil {
			return err
		}

		articles.Fields.Add(&core.TextField{Name: "audio_url", Max: 2048})

		return app.Save(articles)
	}, func(app core.App) error {
		articles, err := app.FindCollectionByNameOrId(db.ArticlesCollection)
		if err != nil {
			return err
		}

		articles.Fields.RemoveByName("audio_url")

		return app.Save(articles)
	})
}

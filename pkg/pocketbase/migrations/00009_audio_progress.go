package migrations

import (
	"github.com/cksidharthan/lavender/pkg/pocketbase/db"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

const audioProgressUniqueIndex = "idx_audio_progress_user_article"

// how far a user got in the audio of an article, so a podcast goes on where it was left, on any device.
func init() {
	migrations.Register(func(app core.App) error {
		users, err := app.FindCollectionByNameOrId(db.UsersCollection)
		if err != nil {
			return err
		}
		articles, err := app.FindCollectionByNameOrId(db.ArticlesCollection)
		if err != nil {
			return err
		}

		minimum := 0.0
		progress := core.NewBaseCollection(db.AudioProgressCollection)
		progress.Fields.Add(
			&core.RelationField{Name: "user", CollectionId: users.Id, Required: true, MaxSelect: 1, CascadeDelete: true},
			&core.RelationField{Name: "article", CollectionId: articles.Id, Required: true, MaxSelect: 1, CascadeDelete: true},
			&core.NumberField{Name: "seconds", Min: &minimum},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		progress.AddIndex(audioProgressUniqueIndex, true, "user, article", "")

		return app.Save(progress)
	}, func(app core.App) error {
		progress, err := app.FindCollectionByNameOrId(db.AudioProgressCollection)
		if err != nil {
			return err
		}

		return app.Delete(progress)
	})
}

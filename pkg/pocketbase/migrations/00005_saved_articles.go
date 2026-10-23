package migrations

import (
	"github.com/cksidharthan/lavender/pkg/pocketbase/db"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

const savedUniqueIndex = "idx_saved_articles_user_article_kind"

// the articles a user marked as a favourite or to read later.
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

		saved := core.NewBaseCollection(db.SavedArticlesCollection)
		saved.Fields.Add(
			&core.RelationField{Name: "user", CollectionId: users.Id, Required: true, MaxSelect: 1, CascadeDelete: true},
			&core.RelationField{Name: "article", CollectionId: articles.Id, Required: true, MaxSelect: 1, CascadeDelete: true},
			&core.SelectField{Name: "kind", Required: true, MaxSelect: 1, Values: db.SavedKinds},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		saved.AddIndex(savedUniqueIndex, true, "user, article, kind", "")

		return app.Save(saved)
	}, func(app core.App) error {
		saved, err := app.FindCollectionByNameOrId(db.SavedArticlesCollection)
		if err != nil {
			return err
		}

		return app.Delete(saved)
	})
}

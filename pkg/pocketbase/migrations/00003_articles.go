package migrations

import (
	"github.com/cksidharthan/lavender/pkg/pocketbase/db"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

const (
	articleFeedGUIDIndex = "idx_articles_feed_guid"
	articlePublishedIdx  = "idx_articles_feed_published"
)

// the articles of a feed, and the error of the last fetch of the feed.
func init() {
	migrations.Register(func(app core.App) error {
		feeds, err := app.FindCollectionByNameOrId(db.FeedsCollection)
		if err != nil {
			return err
		}

		feeds.Fields.Add(&core.TextField{Name: "last_error", Max: 512})
		if err := app.Save(feeds); err != nil {
			return err
		}

		articles := core.NewBaseCollection(db.ArticlesCollection)
		articles.Fields.Add(
			&core.RelationField{Name: "feed", CollectionId: feeds.Id, Required: true, MaxSelect: 1, CascadeDelete: true},
			&core.TextField{Name: "guid", Required: true, Max: 1024},
			&core.TextField{Name: "title", Max: 1024},
			&core.TextField{Name: "url", Max: 2048},
			&core.TextField{Name: "summary", Max: 4096},
			&core.EditorField{Name: "content"},
			&core.TextField{Name: "author", Max: 255},
			&core.TextField{Name: "image_url", Max: 2048},
			&core.DateField{Name: "published_at"},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		// an article is stored once per feed, however often the feed is fetched.
		articles.AddIndex(articleFeedGUIDIndex, true, "feed, guid", "")
		articles.AddIndex(articlePublishedIdx, false, "feed, published_at", "")

		return app.Save(articles)
	}, func(app core.App) error {
		articles, err := app.FindCollectionByNameOrId(db.ArticlesCollection)
		if err != nil {
			return err
		}
		if err := app.Delete(articles); err != nil {
			return err
		}

		feeds, err := app.FindCollectionByNameOrId(db.FeedsCollection)
		if err != nil {
			return err
		}
		feeds.Fields.RemoveByName("last_error")

		return app.Save(feeds)
	})
}

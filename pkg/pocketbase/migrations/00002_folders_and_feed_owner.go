package migrations

import (
	"github.com/cksidharthan/lavender/pkg/pocketbase/db"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

const (
	feedURLIndex      = "idx_feeds_feed_url"
	userFeedURLIndex  = "idx_feeds_user_feed_url"
	userFolderIndex   = "idx_folders_user_name"
	feedsUserField    = "user"
	feedsFolderField  = "folder"
	foldersUserField  = "user"
	foldersNameField  = "name"
	foldersNameMaxLen = 64
)

// every user keeps their own folders and feeds: folders are new, feeds get an owner and an optional folder.
func init() {
	migrations.Register(func(app core.App) error {
		users, err := app.FindCollectionByNameOrId(db.UsersCollection)
		if err != nil {
			return err
		}

		folders := core.NewBaseCollection(db.FoldersCollection)
		folders.Fields.Add(
			&core.RelationField{Name: foldersUserField, CollectionId: users.Id, Required: true, MaxSelect: 1, CascadeDelete: true},
			&core.TextField{Name: foldersNameField, Required: true, Max: foldersNameMaxLen},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		folders.AddIndex(userFolderIndex, true, foldersUserField+", "+foldersNameField, "")

		if err := app.Save(folders); err != nil {
			return err
		}

		feeds, err := app.FindCollectionByNameOrId(db.FeedsCollection)
		if err != nil {
			return err
		}

		feeds.Fields.Add(
			&core.RelationField{Name: feedsUserField, CollectionId: users.Id, Required: true, MaxSelect: 1, CascadeDelete: true},
			// not required and no cascade, so deleting a folder moves its feeds out of the folder.
			&core.RelationField{Name: feedsFolderField, CollectionId: folders.Id, MaxSelect: 1},
		)
		// the same feed can be added by different users, but only once per user.
		feeds.RemoveIndex(feedURLIndex)
		feeds.AddIndex(userFeedURLIndex, true, feedsUserField+", feed_url", "")

		return app.Save(feeds)
	}, func(app core.App) error {
		feeds, err := app.FindCollectionByNameOrId(db.FeedsCollection)
		if err != nil {
			return err
		}

		feeds.Fields.RemoveByName(feedsFolderField)
		feeds.Fields.RemoveByName(feedsUserField)
		feeds.RemoveIndex(userFeedURLIndex)
		feeds.AddIndex(feedURLIndex, true, "feed_url", "")

		if err := app.Save(feeds); err != nil {
			return err
		}

		folders, err := app.FindCollectionByNameOrId(db.FoldersCollection)
		if err != nil {
			return err
		}

		return app.Delete(folders)
	})
}

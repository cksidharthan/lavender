// Package view turns stored data into the props of the ui components.
package view

import (
	"github.com/cksidharthan/lavender/pkg/store"
	"github.com/cksidharthan/lavender/pkg/ui/components"
	"github.com/pocketbase/pocketbase/core"
)

// Sidebar builds the sidebar of the user.
func Sidebar(user *core.Record, tree []store.Folder) components.SidebarProps {
	folders := make([]components.Folder, 0, len(tree))
	for _, f := range tree {
		feeds := make([]components.Feed, 0, len(f.Feeds))
		for _, feed := range f.Feeds {
			feeds = append(feeds, components.Feed{ID: feed.ID, Title: feed.Title})
		}

		folders = append(folders, components.Folder{ID: f.ID, Name: f.Name, Feeds: feeds})
	}

	return components.SidebarProps{UserName: DisplayName(user), Folders: folders}
}

// DisplayName is the name of the user, or the email when the user has no name.
func DisplayName(user *core.Record) string {
	if name := user.GetString("name"); name != "" {
		return name
	}

	return user.Email()
}

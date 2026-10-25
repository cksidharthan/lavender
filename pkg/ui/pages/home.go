package pages

import (
	"slices"
	"strconv"

	"github.com/cksidharthan/lavender/pkg/ui/components"
)

// FeedFilter is the choice of feeds above a list of articles. Without Folders there is nothing to choose.
type FeedFilter struct {
	Action   string // the page of the list, the choice is sent to it
	Folders  []components.Folder
	Selected []string // the ids of the feeds that are picked, none for all of them
}

func (f FeedFilter) picked(id string) bool {
	return slices.Contains(f.Selected, id)
}

func (f FeedFilter) feedCount() int {
	n := 0
	for _, folder := range f.Folders {
		n += len(folder.Feeds)
	}

	return n
}

// label is the text of the button that opens the choice.
func (f FeedFilter) label() string {
	switch len(f.Selected) {
	case 0:
		return "All feeds"
	case 1:
		for _, folder := range f.Folders {
			for _, feed := range folder.Feeds {
				if feed.ID == f.Selected[0] {
					return feed.Title
				}
			}
		}
	}

	return strconv.Itoa(len(f.Selected)) + " feeds"
}

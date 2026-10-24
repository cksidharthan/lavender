package components

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/a-h/templ"
)

// FeedSettings are the settings of a feed, as shown in its settings dialog.
type FeedSettings struct {
	RefreshMinutes int // 0 for the default
	HideFromHome   bool
	IconOverride   string
	Paused         bool
}

type Feed struct {
	ID       string
	Title    string
	URL      string // the address of the feed
	IconURL  string // empty when there is none
	Settings FeedSettings
}

// settingsJSON are the settings of the feed for the dialog. The names are the names of the fields of its form, the
// script of the App layout fills the form with them.
// FeedURLJSON are the values of the form of the dialog that edits the address of the feed.
func FeedURLJSON(f Feed) string {
	data, _ := json.Marshal(map[string]any{"id": f.ID, "url": f.URL})

	return string(data)
}

func settingsJSON(f Feed) string {
	refresh := ""
	if f.Settings.RefreshMinutes > 0 {
		refresh = strconv.Itoa(f.Settings.RefreshMinutes)
	}

	data, _ := json.Marshal(map[string]any{
		"id":              f.ID,
		"refresh_minutes": refresh,
		"hide_from_home":  f.Settings.HideFromHome,
		"auto_refresh":    !f.Settings.Paused,
		"icon_override":   f.Settings.IconOverride,
	})

	return string(data)
}

// Folder groups feeds in the sidebar. Feeds that are in no folder are passed as a Folder with an empty ID and Name.
type Folder struct {
	ID    string
	Name  string
	Feeds []Feed
}

type SidebarProps struct {
	UserName string
	Folders  []Folder
	// OOB renders the sidebar as an htmx out of band swap, replacing the sidebar that is on the page.
	OOB bool
}

func sidebarAttrs(p SidebarProps) templ.Attributes {
	if p.OOB {
		return templ.Attributes{"hx-swap-oob": "outerHTML"}
	}

	return nil
}

func initials(name string) string {
	fields := strings.Fields(name)
	switch len(fields) {
	case 0:
		return "?"
	case 1:
		return strings.ToUpper(string([]rune(fields[0])[:1]))
	default:
		return strings.ToUpper(string([]rune(fields[0])[:1]) + string([]rune(fields[len(fields)-1])[:1]))
	}
}

// ContentLink returns the attributes that make a link load its page into the content area next to the
// sidebar instead of navigating the whole page. The href stays, so it still works without javascript.
func ContentLink(href string) templ.Attributes {
	return templ.Attributes{"hx-get": href, "hx-target": "#content", "hx-swap": "innerHTML", "hx-push-url": "true"}
}

// folderMenuID is the id of a folder's menu. Folder names can hold spaces, which are not valid in an id.
func folderMenuID(name string) string {
	return "folder-menu-" + url.QueryEscape(name)
}

// IDVals are the hx-vals that send the id of a record with a request that has no form.
func IDVals(id string) string {
	return `{"id":"` + id + `"}`
}

// feedInitial is the letter that is shown when a feed has no icon, or the icon does not load.
func feedInitial(title string) string {
	for _, r := range title {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return strings.ToUpper(string(r))
		}
	}

	return "#"
}

// Initials are the first letters of the first and the last word of a name, for the avatar of a user.
func Initials(name string) string {
	return initials(name)
}

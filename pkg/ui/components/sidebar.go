package components

import (
	"net/url"
	"strings"

	"github.com/a-h/templ"
)

type Feed struct {
	ID    string
	Title string
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

// idVals are the hx-vals that send the id of a record with a request that has no form.
func idVals(id string) string {
	return `{"id":"` + id + `"}`
}

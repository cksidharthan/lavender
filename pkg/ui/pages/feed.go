package pages

import (
	"strconv"

	"github.com/a-h/templ"
)

// ArticleItem is an article ready to be shown. The texts have no html and the url is safe to link to.
type ArticleItem struct {
	ID       string
	FeedID   string
	FeedName string
	FeedIcon string // the icon of the feed, shown next to the title
	Title    string
	URL      string // empty when the article has no http(s) link
	Author   string
	Date     string // empty when the article has no date
	Summary  string
	Image    string // empty when the article has no picture
	// how the user saved the article
	Favourite bool
	ReadLater bool
}

// ArticlePage is a page of a list of articles, of one feed or of all of them.
type ArticlePage struct {
	Items    []ArticleItem
	ShowFeed bool   // show the name of the feed of each article, for the lists that mix feeds
	HasMore  bool   // there is a next page
	MoreURL  string // where the next page is
}

// ArticleDetail is a whole article for the panel. HTML is sanitized, it is the only field that holds markup.
type ArticleDetail struct {
	ID        string
	Favourite bool
	ReadLater bool
	Title     string
	URL       string // the original, empty when the article has no http(s) link
	Author    string
	Date      string
	Image     string
	Audio     string  // the address of the audio file when the article is a podcast episode, shown with a player
	AudioFrom float64 // seconds the player starts at, 0 for the beginning
	Video     string  // the address of the player when the article is a video, shown instead of the picture
	HTML      string
	FeedName  string
	FeedIcon  string
}

// audioStart is the second the player starts at.
func audioStart(a ArticleDetail) string {
	return strconv.FormatFloat(a.AudioFrom, 'f', 0, 64)
}

// articleAttrs make the card open the article in the panel.
func articleAttrs(a ArticleItem) templ.Attributes {
	return templ.Attributes{
		"hx-get":                "/feeds/" + a.FeedID + "/articles/" + a.ID,
		"hx-target":             "#article-panel-body",
		"hx-swap":               "innerHTML",
		"hx-on::before-request": "openArticlePanel()",
	}
}

// metaParts are the parts of the line below the title: the feed (for the lists that mix feeds), the author and the date.
func metaParts(page ArticlePage, a ArticleItem) []string {
	var parts []string
	if page.ShowFeed && a.FeedName != "" {
		parts = append(parts, a.FeedName)
	}
	if a.Author != "" {
		parts = append(parts, a.Author)
	}
	if a.Date != "" {
		parts = append(parts, a.Date)
	}

	return parts
}

// The ways to save an article, by the name used in the address of the request.
const (
	SaveFavourite = "favourite"
	SaveReadLater = "read-later"
)

// saveButtonID is the id of the button, so the answer to a click can replace the buttons of the article on the page.
func saveButtonID(articleID, kind string, panel bool) string {
	id := "save-" + kind + "-" + articleID
	if panel {
		id = "panel-" + id
	}

	return id
}

func saveButtonAttrs(articleID, kind string, active, panel, oob bool) templ.Attributes {
	label := "Add to favourites"
	switch {
	case kind == SaveFavourite && active:
		label = "Remove from favourites"
	case kind == SaveReadLater && active:
		label = "Remove from read later"
	case kind == SaveReadLater:
		label = "Save to read later"
	}

	attrs := templ.Attributes{
		"id":           saveButtonID(articleID, kind, panel),
		"hx-post":      "/api/v1/articles/" + kind,
		"hx-vals":      `{"id":"` + articleID + `"}`,
		"hx-swap":      "none",
		"aria-pressed": strconv.FormatBool(active),
		"aria-label":   label,
		"title":        label,
	}
	if oob {
		attrs["hx-swap-oob"] = "outerHTML"
	}

	return attrs
}

// saveButtonClass has no classes that fight each other, as the stylesheet and not the order decides which one wins.
func saveButtonClass(kind string, active, panel bool) string {
	color := "text-slate-400 hover:bg-slate-100 hover:text-slate-700"
	if active && kind == SaveFavourite {
		color = "text-amber-500 hover:bg-amber-50"
	} else if active {
		color = "text-brand hover:bg-brand/10"
	}

	if panel {
		border := "border-slate-200 bg-white shadow-xs hover:border-slate-300"
		if active {
			border = "border-slate-200 bg-white shadow-xs hover:border-slate-300"
		}

		return "inline-flex h-9 cursor-pointer items-center gap-1.5 rounded-lg border px-3.5 text-sm font-medium transition " + border + " " + saveButtonPanelColor(kind, active)
	}

	visible := "opacity-0 group-hover:opacity-100 focus-visible:opacity-100"
	if active {
		visible = "opacity-100"
	}

	return "relative z-10 flex size-7 cursor-pointer items-center justify-center rounded-md transition " + visible + " " + color
}

func saveButtonPanelColor(kind string, active bool) string {
	switch {
	case active && kind == SaveFavourite:
		return "text-amber-600"
	case active:
		return "text-brand"
	default:
		return "text-slate-700"
	}
}

func saveButtonLabel(kind string, active bool) string {
	switch {
	case kind == SaveFavourite && active:
		return "Favourite"
	case kind == SaveFavourite:
		return "Favourite"
	case active:
		return "Saved for later"
	default:
		return "Read later"
	}
}

func savePath(kind string) string {
	if kind == SaveFavourite {
		return "M12 3l2.6 5.6 6.1.7-4.5 4.2 1.2 6L12 16.9 6.6 19.5l1.2-6L3.3 9.3l6.1-.7z"
	}

	return "M6 4h12v17l-6-4-6 4z"
}

func saveFill(active bool) string {
	if active {
		return "currentColor"
	}

	return "none"
}

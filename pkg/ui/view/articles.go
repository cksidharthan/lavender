package view

import (
	"html"
	"net/url"
	"strconv"
	"strings"

	"github.com/cksidharthan/lavender/pkg/feedfetch"
	"github.com/cksidharthan/lavender/pkg/store"
	"github.com/cksidharthan/lavender/pkg/ui/components"
	"github.com/cksidharthan/lavender/pkg/ui/pages"
	"github.com/microcosm-cc/bluemonday"
)

const (
	untitled         = "Untitled"
	dateLayout       = "Jan 2, 2006"
	dateTimeLayout   = "Jan 2, 2006 15:04"
	maxSummaryLength = 400
)

// textOnly removes all html. The summaries of a feed hold html written by whoever runs the feed, which must never
// end up on a page as html.
var textOnly = bluemonday.StrictPolicy()

// articleHTML keeps the formatting, links and images of an article and removes everything that can run code. The
// links open in a new tab and send no referrer.
var articleHTML = func() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.RequireNoReferrerOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true)
	p.RequireParseableURLs(true)
	p.AllowURLSchemes("http", "https", "mailto")

	return p
}()

// Articles builds a page of the articles of a feed. Feed content is shown as plain text, with no html, and the
// links are only kept when they are http(s). The feed has the icon as it is shown, see IconPath. The saved state
// tells how the user saved the articles, by article id.
func Articles(feed components.Feed, articles []store.Article, hasMore bool, offset int, saved map[string]store.SavedFlags) pages.ArticlePage {
	items := make([]pages.ArticleItem, 0, len(articles))
	for _, a := range articles {
		items = append(items, articleItem(a, feed, saved[a.ID]))
	}

	return pages.ArticlePage{
		Items:   items,
		HasMore: hasMore,
		MoreURL: "/feeds/" + feed.ID + "/articles?offset=" + strconv.Itoa(offset+len(articles)),
	}
}

// ListArticles builds a page of a list of articles of all the feeds, each with the feed it is of. The next page is at
// moreURL with the offset added.
func ListArticles(articles []store.FeedArticle, hasMore bool, offset int, moreURL string, saved map[string]store.SavedFlags) pages.ArticlePage {
	items := make([]pages.ArticleItem, 0, len(articles))
	for _, a := range articles {
		feed := components.Feed{ID: a.Feed.ID, Title: a.Feed.Title, IconURL: IconPath(a.Feed.ID, a.Feed.IconURL)}
		items = append(items, articleItem(a.Article, feed, saved[a.ID]))
	}

	return pages.ArticlePage{
		Items:    items,
		ShowFeed: true,
		HasMore:  hasMore,
		MoreURL:  moreURL + "&offset=" + strconv.Itoa(offset+len(articles)),
	}
}

func articleItem(a store.Article, feed components.Feed, saved store.SavedFlags) pages.ArticleItem {
	item := pages.ArticleItem{
		ID:        a.ID,
		FeedID:    feed.ID,
		FeedName:  plainText(feed.Title),
		FeedIcon:  feed.IconURL,
		Image:     imageOf(a),
		Title:     plainText(a.Title),
		URL:       webURL(a.URL),
		Author:    plainText(a.Author),
		Summary:   truncate(plainText(firstNonEmpty(a.Summary, a.Content)), maxSummaryLength),
		Favourite: saved.Favourite,
		ReadLater: saved.ReadLater,
	}
	if item.Title == "" {
		item.Title = untitled
	}
	if !a.PublishedAt.IsZero() {
		item.Date = a.PublishedAt.Format(dateTimeLayout)
	}

	return item
}

// imageOf is the picture of the article. Feeds often only give a small thumbnail, so the pictures in the text are
// looked at too, and the biggest one is used.
func imageOf(a store.Article) string {
	return feedfetch.PickImage(a.ImageURL, a.Content+a.Summary)
}

func plainText(s string) string {
	// a space before every tag, so the text of two paragraphs does not run together.
	return strings.Join(strings.Fields(html.UnescapeString(textOnly.Sanitize(strings.ReplaceAll(s, "<", " <")))), " ")
}

func webURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}

	return u.String()
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}

	return ""
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}

	return strings.TrimSpace(string(runes[:max])) + "…"
}

// Article builds the whole article for the panel. The html of the article is sanitized.
func Article(a store.Article, saved store.SavedFlags) pages.ArticleDetail {
	detail := pages.ArticleDetail{
		ID:        a.ID,
		Favourite: saved.Favourite,
		ReadLater: saved.ReadLater,
		Title:     plainText(a.Title),
		URL:       webURL(a.URL),
		Author:    plainText(a.Author),
		Image:     imageOf(a),
		HTML:      articleHTML.Sanitize(firstNonEmpty(a.Content, a.Summary)),
	}
	detail.HTML = tidyBody(detail.HTML, detail.Image)

	// the video of the article itself, as in the feed of a youtube channel, is shown on top instead of the picture.
	if video, ok := youtubeVideo(a.URL); ok {
		detail.Video = video.EmbedURL()
		detail.Image = ""
		detail.HTML = withPlayers(detail.HTML, video.ID)
	} else {
		detail.HTML = withPlayers(detail.HTML)
	}
	if detail.Title == "" {
		detail.Title = untitled
	}
	if !a.PublishedAt.IsZero() {
		detail.Date = a.PublishedAt.Format(dateTimeLayout)
	}

	return detail
}

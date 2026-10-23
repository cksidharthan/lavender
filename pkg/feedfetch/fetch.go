// Package feedfetch downloads a feed and reads its metadata.
package feedfetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/cksidharthan/lavender/pkg/pocketbase/db"
	"github.com/mmcdole/gofeed"
	"golang.org/x/net/html"
)

const (
	requestTimeout = 8 * time.Second
	maxBodyBytes   = 4 << 20
	maxRedirects   = 5
)

var (
	ErrInvalidURL = errors.New("the feed url must be a valid http or https url")
	ErrNotAFeed   = errors.New("the url does not point to an rss or atom feed")
	errPrivateIP  = errors.New("the feed url points to a private address")
)

// Result is a downloaded feed.
type Result struct {
	Meta  Meta
	Items []Item
}

// Item is an article of a feed.
type Item struct {
	GUID        string // stable id of the article, falls back to its url
	Title       string
	URL         string
	Summary     string
	Content     string
	Author      string
	ImageURL    string
	PublishedAt time.Time // zero when the feed has no date
}

// Meta is what is read from the feed document.
type Meta struct {
	Title       string
	SiteURL     string
	Description string
	Type        string // rss or atom
	IconURL     string // the logo of the feed, empty when it has none
}

// ParseURL checks that raw is an absolute http(s) url.
func ParseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, ErrInvalidURL
	}

	return u, nil
}

// NewClient returns an http client that refuses to connect to loopback, private and link local addresses, so a url
// given by a user can not be used to reach services inside the network the server runs in. The check is on the
// resolved address, so it also covers redirects and dns tricks.
func NewClient() *http.Client {
	return &http.Client{
		Timeout: requestTimeout,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{Timeout: requestTimeout, Control: refusePrivate}).DialContext,
		},
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return errors.New("too many redirects")
			}

			return nil
		},
	}
}

var client = NewClient()

func refusePrivate(_, address string, _ syscall.RawConn) error {
	addrPort, err := netip.ParseAddrPort(address)
	if err != nil {
		return err
	}

	if ip := addrPort.Addr().Unmap(); !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return errPrivateIP
	}

	return nil
}

// Fetch downloads the feed at rawURL and reads its metadata.
func Fetch(ctx context.Context, rawURL string) (Result, error) {
	u, err := ParseURL(rawURL)
	if err != nil {
		return Result{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Result{}, ErrInvalidURL
	}
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml;q=0.9, text/xml;q=0.8, */*;q=0.5")
	req.Header.Set("User-Agent", "Lavender feed reader")

	resp, err := client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("error fetching the feed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return Result{}, fmt.Errorf("the feed answered with status %d", resp.StatusCode)
	}

	return Parse(io.LimitReader(resp.Body, maxBodyBytes))
}

// Parse reads a rss, atom or json feed.
func Parse(r io.Reader) (Result, error) {
	feed, err := gofeed.NewParser().Parse(r)
	if err != nil {
		return Result{}, ErrNotAFeed
	}

	result := Result{
		Meta: Meta{
			Title:       strings.TrimSpace(feed.Title),
			SiteURL:     strings.TrimSpace(feed.Link),
			Description: strings.TrimSpace(feed.Description),
			Type:        feedType(feed.FeedType),
		},
		Items: make([]Item, 0, len(feed.Items)),
	}

	if feed.Image != nil {
		result.Meta.IconURL = webURL(feed.Image.URL)
	}

	for _, it := range feed.Items {
		if item, ok := toItem(it); ok {
			result.Items = append(result.Items, item)
		}
	}

	return result, nil
}

// toItem converts a gofeed item. An item without an id and without a link can not be told apart from the
// others and is dropped.
func toItem(it *gofeed.Item) (Item, bool) {
	item := Item{
		GUID:    strings.TrimSpace(it.GUID),
		Title:   strings.TrimSpace(it.Title),
		URL:     strings.TrimSpace(it.Link),
		Summary: strings.TrimSpace(it.Description),
		Content: strings.TrimSpace(it.Content),
	}
	if item.GUID == "" {
		item.GUID = item.URL
	}
	if item.GUID == "" {
		return Item{}, false
	}

	if it.Author != nil {
		item.Author = strings.TrimSpace(it.Author.Name)
	}
	item.ImageURL = itemImage(it)
	switch {
	case it.PublishedParsed != nil:
		item.PublishedAt = it.PublishedParsed.UTC()
	case it.UpdatedParsed != nil:
		item.PublishedAt = it.UpdatedParsed.UTC()
	}

	return item, true
}

// feedType maps the format gofeed detected to a feed type of the feeds collection.
func feedType(format string) string {
	switch format {
	case "rss":
		return db.FeedTypeRSS
	case "atom":
		return db.FeedTypeAtom
	default:
		return db.FeedTypeOther
	}
}

// unknownWidth is the width given to a picture that does not tell its size. Pictures that do tell are mostly
// thumbnails, so an unknown size is taken as a full size one.
const unknownWidth = 800

type candidate struct {
	url   string
	width int
}

// itemImage finds the best picture of an article: the image of the item, image enclosures, the media thumbnails
// and media content of the feed, and the pictures in the text of the article.
func itemImage(it *gofeed.Item) string {
	var candidates []candidate

	if it.Image != nil {
		candidates = addCandidate(candidates, it.Image.URL, 0)
	}
	for _, e := range it.Enclosures {
		if strings.HasPrefix(e.Type, "image/") {
			candidates = addCandidate(candidates, e.URL, 0)
		}
	}
	for _, name := range []string{"content", "thumbnail"} {
		for _, ext := range it.Extensions["media"][name] {
			if medium := ext.Attrs["medium"]; medium != "" && medium != "image" {
				continue
			}
			width, _ := strconv.Atoi(ext.Attrs["width"])
			candidates = addCandidate(candidates, ext.Attrs["url"], width)
		}
	}

	return bestCandidate(append(candidates, markupCandidates(it.Content+it.Description)...))
}

// PickImage returns the best picture of an article from the one that is stored and the pictures in its text.
// Feeds often only give a small thumbnail as the picture, while the text links to the full picture.
func PickImage(stored, markup string) string {
	return bestCandidate(append(addCandidate(nil, stored, 0), markupCandidates(markup)...))
}

func addCandidate(candidates []candidate, rawURL string, width int) []candidate {
	u := webURL(rawURL)
	if u == "" {
		return candidates
	}
	if width == 0 {
		width = widthHint(u)
	}

	return append(candidates, candidate{url: u, width: width})
}

// bestCandidate returns the widest picture, the first one of equal width.
func bestCandidate(candidates []candidate) string {
	best, bestWidth := "", -1
	for _, c := range candidates {
		width := c.width
		if width == 0 {
			width = unknownWidth
		}
		if width > bestWidth {
			best, bestWidth = c.url, width
		}
	}

	return best
}

// widthHint reads the width from the query of a resized picture url, 0 when there is none.
func widthHint(rawURL string) int {
	u, err := url.Parse(rawURL)
	if err != nil {
		return 0
	}

	for _, key := range []string{"width", "w"} {
		if width, err := strconv.Atoi(u.Query().Get(key)); err == nil && width > 0 {
			return width
		}
	}

	return 0
}

var imageExtensions = []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".avif"}

func looksLikeImage(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}

	path := strings.ToLower(u.Path)
	for _, ext := range imageExtensions {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}

	return false
}

// markupCandidates are the img tags in the html, and the links that point to a picture.
func markupCandidates(markup string) []candidate {
	var candidates []candidate

	tokens := html.NewTokenizer(strings.NewReader(markup))
	for {
		switch tokens.Next() {
		case html.ErrorToken:
			return candidates
		case html.StartTagToken, html.SelfClosingTagToken:
			tag := tokens.Token()
			attrs := make(map[string]string, len(tag.Attr))
			for _, a := range tag.Attr {
				attrs[a.Key] = a.Val
			}

			switch tag.Data {
			case "img":
				width, _ := strconv.Atoi(attrs["width"])
				candidates = addCandidate(candidates, attrs["src"], width)
			case "a":
				if looksLikeImage(attrs["href"]) {
					candidates = addCandidate(candidates, attrs["href"], 0)
				}
			}
		}
	}
}

// webURL returns the url when it is an absolute http(s) url, empty otherwise.
func webURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}

	return u.String()
}

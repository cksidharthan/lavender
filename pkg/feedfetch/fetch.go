// Package feedfetch downloads a feed and reads its metadata.
package feedfetch

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
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

// Meta is what is read from the feed document.
type Meta struct {
	Title       string
	SiteURL     string
	Description string
	Type        string // rss or atom
}

// ParseURL checks that raw is an absolute http(s) url.
func ParseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, ErrInvalidURL
	}

	return u, nil
}

// client refuses to connect to loopback, private and link local addresses, so a feed url can not be used to
// reach services inside the network the server runs in. The check is on the resolved address, so it also
// covers redirects and dns tricks.
var client = &http.Client{
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
func Fetch(ctx context.Context, rawURL string) (Meta, error) {
	u, err := ParseURL(rawURL)
	if err != nil {
		return Meta{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Meta{}, ErrInvalidURL
	}
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml;q=0.9, text/xml;q=0.8, */*;q=0.5")
	req.Header.Set("User-Agent", "Lavender feed reader")

	resp, err := client.Do(req)
	if err != nil {
		return Meta{}, fmt.Errorf("error fetching the feed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return Meta{}, fmt.Errorf("the feed answered with status %d", resp.StatusCode)
	}

	return Parse(io.LimitReader(resp.Body, maxBodyBytes))
}

type document struct {
	XMLName xml.Name
	// rss and rdf
	Channel struct {
		Title       string `xml:"title"`
		Link        string `xml:"link"`
		Description string `xml:"description"`
	} `xml:"channel"`
	// atom
	Title    string `xml:"title"`
	Subtitle string `xml:"subtitle"`
	Links    []struct {
		Href string `xml:"href,attr"`
		Rel  string `xml:"rel,attr"`
	} `xml:"link"`
}

// Parse reads the metadata of an rss, rdf or atom document.
func Parse(r io.Reader) (Meta, error) {
	var doc document

	decoder := xml.NewDecoder(r)
	// feeds in other charsets are read as is, only the title and description can be off.
	decoder.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }

	if err := decoder.Decode(&doc); err != nil {
		return Meta{}, ErrNotAFeed
	}

	switch doc.XMLName.Local {
	case "rss", "RDF":
		return Meta{
			Title:       strings.TrimSpace(doc.Channel.Title),
			SiteURL:     strings.TrimSpace(doc.Channel.Link),
			Description: strings.TrimSpace(doc.Channel.Description),
			Type:        "rss",
		}, nil
	case "feed":
		meta := Meta{Title: strings.TrimSpace(doc.Title), Description: strings.TrimSpace(doc.Subtitle), Type: "atom"}
		for _, l := range doc.Links {
			if l.Rel == "" || l.Rel == "alternate" {
				meta.SiteURL = l.Href
				break
			}
		}

		return meta, nil
	default:
		return Meta{}, ErrNotAFeed
	}
}

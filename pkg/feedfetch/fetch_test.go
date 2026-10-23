package feedfetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParse(t *testing.T) {
	rss, err := Parse(strings.NewReader(`<?xml version="1.0"?><rss version="2.0"><channel><title> Go Blog </title><link>https://go.dev/blog</link><description>d</description></channel></rss>`))
	require.NoError(t, err)
	require.Equal(t, Meta{Title: "Go Blog", SiteURL: "https://go.dev/blog", Description: "d", Type: "rss"}, rss)

	atom, err := Parse(strings.NewReader(`<feed xmlns="http://www.w3.org/2005/Atom"><title>A</title><subtitle>s</subtitle><link rel="self" href="https://x/feed"/><link href="https://x"/></feed>`))
	require.NoError(t, err)
	require.Equal(t, Meta{Title: "A", SiteURL: "https://x", Description: "s", Type: "atom"}, atom)

	_, err = Parse(strings.NewReader(`<html><body>nope</body></html>`))
	require.ErrorIs(t, err, ErrNotAFeed)
	_, err = Parse(strings.NewReader(`not xml`))
	require.ErrorIs(t, err, ErrNotAFeed)
}

func TestParseURL(t *testing.T) {
	for _, bad := range []string{"", "feed.xml", "ftp://x/feed", "file:///etc/passwd", "http://"} {
		_, err := ParseURL(bad)
		require.ErrorIs(t, err, ErrInvalidURL, bad)
	}

	_, err := ParseURL(" https://example.com/feed.xml ")
	require.NoError(t, err)
}

func TestFetchRefusesLocalAddresses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<rss><channel><title>t</title></channel></rss>`))
	}))
	defer srv.Close()

	// the test server listens on loopback
	_, err := Fetch(context.Background(), srv.URL)
	require.ErrorIs(t, err, errPrivateIP)
}

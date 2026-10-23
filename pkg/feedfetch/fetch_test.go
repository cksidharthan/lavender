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
	rss, err := Parse(strings.NewReader(`<?xml version="1.0"?><rss version="2.0"><channel><title> Go Blog </title><link>https://go.dev/blog</link><description>d</description>
<item><title>One</title><link>https://go.dev/blog/one</link><guid>one-id</guid><pubDate>Mon, 02 Jan 2006 15:04:05 +0000</pubDate><description>first</description></item>
<item><title>Two</title><link>https://go.dev/blog/two</link></item>
<item><title>No id</title></item>
</channel></rss>`))
	require.NoError(t, err)
	require.Equal(t, Meta{Title: "Go Blog", SiteURL: "https://go.dev/blog", Description: "d", Type: "rss"}, rss.Meta)
	require.Len(t, rss.Items, 2, "an item without guid and link is dropped")
	require.Equal(t, "one-id", rss.Items[0].GUID)
	require.Equal(t, "first", rss.Items[0].Summary)
	require.Equal(t, 2006, rss.Items[0].PublishedAt.Year())
	require.Equal(t, "https://go.dev/blog/two", rss.Items[1].GUID, "the link is the id when there is no guid")
	require.True(t, rss.Items[1].PublishedAt.IsZero())

	atom, err := Parse(strings.NewReader(`<feed xmlns="http://www.w3.org/2005/Atom"><title>A</title><subtitle>s</subtitle><link rel="self" href="https://x/feed"/><link href="https://x"/></feed>`))
	require.NoError(t, err)
	require.Equal(t, Meta{Title: "A", SiteURL: "https://x", Description: "s", Type: "atom"}, atom.Meta)

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

func TestItemImages(t *testing.T) {
	feed := `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom" xmlns:media="http://search.yahoo.com/mrss/">
<title>t</title>
<entry><id>thumb</id><title>a</title><media:thumbnail url="https://img.example.com/t.jpg"/><content type="html">&lt;img src="https://img.example.com/other.jpg"&gt;</content></entry>
<entry><id>content</id><title>b</title><content type="html">&lt;p&gt;hi&lt;/p&gt;&lt;img alt="x" src="https://img.example.com/c.png"&gt;</content></entry>
<entry><id>bad</id><title>c</title><content type="html">&lt;img src="javascript:alert(1)"&gt;&lt;img src="/relative.png"&gt;</content></entry>
<entry><id>none</id><title>d</title></entry>
</feed>`

	result, err := Parse(strings.NewReader(feed))
	require.NoError(t, err)
	require.Len(t, result.Items, 4)
	require.Equal(t, "https://img.example.com/t.jpg", result.Items[0].ImageURL, "the media thumbnail, as there is nothing better")
	require.Equal(t, "https://img.example.com/c.png", result.Items[1].ImageURL, "the first image of the content")
	require.Empty(t, result.Items[2].ImageURL, "only absolute http(s) images")
	require.Empty(t, result.Items[3].ImageURL)
}

func TestTheBiggestPictureWins(t *testing.T) {
	// the shape of a reddit feed: a 140px thumbnail twice, and the full picture as a link in the text
	feed := `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom" xmlns:media="http://search.yahoo.com/mrss/"><title>t</title>
<entry><id>1</id><title>a</title>
<media:thumbnail url="https://preview.redd.it/p.png?width=140&amp;height=128&amp;auto=webp&amp;s=aa"/>
<content type="html">&lt;img src="https://preview.redd.it/p.png?width=140&amp;height=128&amp;auto=webp&amp;s=aa"&gt; &lt;a href="https://preview.redd.it/p.png?width=891&amp;format=png&amp;auto=webp&amp;s=bb"&gt;full&lt;/a&gt; &lt;a href="https://example.com/page"&gt;page&lt;/a&gt;</content></entry>
</feed>`

	result, err := Parse(strings.NewReader(feed))
	require.NoError(t, err)
	require.Equal(t, "https://preview.redd.it/p.png?width=891&format=png&auto=webp&s=bb", result.Items[0].ImageURL)

	require.Equal(t, "https://a.example.com/big.png?w=1200", PickImage("https://a.example.com/small.png?w=140", `<img src="https://a.example.com/big.png?w=1200">`))
	require.Equal(t, "https://a.example.com/stored.png", PickImage("https://a.example.com/stored.png", `<p>no picture</p>`))
	require.Empty(t, PickImage("", `<p>none</p>`))
}

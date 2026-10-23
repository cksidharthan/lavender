package opml

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParse(t *testing.T) {
	feeds, err := Parse(strings.NewReader(`<?xml version="1.0" encoding="UTF-8"?>
<opml version="2.0"><head><title>Feeds</title></head><body>
  <outline text="Tech" title="Tech">
    <outline type="rss" text="Go Blog" xmlUrl="https://go.dev/blog/feed.atom" htmlUrl="https://go.dev/blog"/>
    <outline text="Deeper"><outline type="rss" text="Nested" XMLURL="https://nested.example.com/feed"/></outline>
  </outline>
  <outline type="rss" title="Loose" text="ignored" xmlurl="https://loose.example.com/feed"/>
  <outline text="Empty folder"/>
  <outline type="rss" text="  Spaces " xmlUrl=" https://spaces.example.com/feed "/>
</body></opml>`))
	require.NoError(t, err)
	require.Equal(t, []Feed{
		{Folder: "Tech", Title: "Go Blog", XMLURL: "https://go.dev/blog/feed.atom", HTMLURL: "https://go.dev/blog", Type: "rss"},
		{Folder: "Deeper", Title: "Nested", XMLURL: "https://nested.example.com/feed", Type: "rss"},
		{Title: "Loose", XMLURL: "https://loose.example.com/feed", Type: "rss"},
		{Title: "Spaces", XMLURL: "https://spaces.example.com/feed", Type: "rss"},
	}, feeds)
}

func TestParseRefusesWhatIsNotOPML(t *testing.T) {
	for name, content := range map[string]string{
		"empty":    "",
		"not xml":  "hello",
		"html":     "<html><body>hi</body></html>",
		"broken":   "<opml><body><outline",
		"an rss":   `<rss version="2.0"><channel></channel></rss>`,
		"too deep": "<opml><body>" + strings.Repeat("<outline text='a'>", 40) + strings.Repeat("</outline>", 40) + "</body></opml>",
		"too big":  "<opml><body>" + strings.Repeat("<!-- padding -->", 600000) + "</body></opml>",
		"too many": "<opml><body>" + strings.Repeat(`<outline xmlUrl="https://a.example.com/f"/>`, MaxFeeds+1) + "</body></opml>",
		"no opml":  `<outline xmlUrl="https://a.example.com/f"/>`,
	} {
		_, err := Parse(strings.NewReader(content))
		require.Error(t, err, name)
	}

	feeds, err := Parse(strings.NewReader(`<opml version="2.0"><body></body></opml>`))
	require.NoError(t, err, "a file without feeds is fine")
	require.Empty(t, feeds)
}

func TestWriteAndParseAgain(t *testing.T) {
	feeds := []Feed{
		{Folder: "Tech & News", Title: `Go "Blog" <1>`, XMLURL: "https://go.dev/blog/feed.atom?a=1&b=2", HTMLURL: "https://go.dev/blog", Type: "atom"},
		{Title: "Loose", XMLURL: "https://loose.example.com/feed"},
		{Folder: "Tech & News", Title: "Second", XMLURL: "https://second.example.com/feed"},
	}

	var buf bytes.Buffer
	require.NoError(t, Write(&buf, "Lavender feeds", feeds, time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)))

	out := buf.String()
	require.True(t, strings.HasPrefix(out, `<?xml version="1.0" encoding="UTF-8"?>`))
	require.Contains(t, out, `<opml version="2.0">`)
	require.Contains(t, out, "<title>Lavender feeds</title>")
	require.Contains(t, out, "<dateCreated>Fri, 23 Oct 2026 12:00:00 +0000</dateCreated>")
	require.Contains(t, out, "&amp;", "special characters are escaped")

	parsed, err := Parse(strings.NewReader(out))
	require.NoError(t, err)
	require.Equal(t, []Feed{
		{Folder: "Tech & News", Title: `Go "Blog" <1>`, XMLURL: "https://go.dev/blog/feed.atom?a=1&b=2", HTMLURL: "https://go.dev/blog", Type: "atom"},
		{Folder: "Tech & News", Title: "Second", XMLURL: "https://second.example.com/feed", Type: "rss"},
		{Title: "Loose", XMLURL: "https://loose.example.com/feed", Type: "rss"},
	}, parsed, "the feeds of a folder are together, the loose ones come last")
}

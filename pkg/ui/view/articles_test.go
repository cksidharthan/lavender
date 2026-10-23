package view

import (
	"strings"
	"testing"
	"time"

	"github.com/cksidharthan/lavender/pkg/store"
	"github.com/cksidharthan/lavender/pkg/ui/components"
	"github.com/cksidharthan/lavender/pkg/ui/pages"
	"github.com/stretchr/testify/require"
)

func TestArticles(t *testing.T) {
	page := Articles(components.Feed{ID: "f1", Title: "Blog"}, []store.Article{
		{
			Title:       "<b>Hello</b> &amp; welcome",
			URL:         "https://example.com/a",
			Summary:     `<p>First <script>alert(1)</script>line</p><img src=x onerror=alert(1)>  second`,
			Author:      "Ann",
			PublishedAt: time.Date(2024, 3, 5, 0, 0, 0, 0, time.UTC),
		},
		{Title: "", URL: "javascript:alert(1)", Content: "<p>from content</p>"},
		{Title: "long", Summary: strings.Repeat("x", 1000)},
	}, true, 30, nil)

	require.Equal(t, "/feeds/f1/articles?offset=33", page.MoreURL)
	require.True(t, page.HasMore)
	require.Equal(t, "Hello & welcome", page.Items[0].Title)
	require.Equal(t, "https://example.com/a", page.Items[0].URL)
	require.Equal(t, "Mar 5, 2024", page.Items[0].Date)
	require.NotContains(t, page.Items[0].Summary, "<")
	require.Equal(t, "First line second", page.Items[0].Summary)

	require.Equal(t, "Untitled", page.Items[1].Title)
	require.Empty(t, page.Items[1].URL, "javascript: links are dropped")
	require.Equal(t, "from content", page.Items[1].Summary)
	require.Empty(t, page.Items[1].Date)

	require.Len(t, []rune(page.Items[2].Summary), maxSummaryLength+1)
}

func TestArticleKeepsFormattingAndRemovesCode(t *testing.T) {
	detail := articleOf(store.Article{
		Title: "T",
		Content: `<h2>Head</h2><p>Some <strong>bold</strong> and <a href="https://example.com/x">a link</a>, <a href="javascript:alert(1)">bad</a></p>` +
			`<script>alert(1)</script><img src="https://img.example.com/a.png" onerror="alert(1)"><img src="https://img.example.com/b.png"><iframe src="https://evil.example"></iframe>`,
	})

	require.Contains(t, detail.HTML, "<h2>Head</h2>")
	require.Contains(t, detail.HTML, "<strong>bold</strong>")
	require.Contains(t, detail.HTML, `href="https://example.com/x"`)
	require.Contains(t, detail.HTML, `target="_blank"`)
	require.Contains(t, detail.HTML, `rel="`)
	require.Equal(t, "https://img.example.com/a.png", detail.Image, "the first picture goes on top")
	require.Contains(t, detail.HTML, `src="https://img.example.com/b.png"`)
	for _, bad := range []string{"<script", "javascript:", "onerror", "<iframe"} {
		require.NotContains(t, detail.HTML, bad)
	}
}

func TestArticleDropsThePictureThatIsOnTop(t *testing.T) {
	detail := articleOf(store.Article{
		Title:    "T",
		ImageURL: "https://img.example.com/a.png?x=1&y=2",
		Content:  `<p>text</p><img src="https://img.example.com/a.png?x=1&y=2"><img src="https://img.example.com/b.png">`,
	})

	require.NotContains(t, detail.HTML, "a.png")
	require.Contains(t, detail.HTML, "b.png")
}

func TestArticlePictureFallsBackToTheTextAndLeavesNoEmptyLink(t *testing.T) {
	a := store.Article{Title: "T", Content: `<table><tr><td><a href="https://example.com/p"><img src="https://img.example.com/p.png"></a></td><td>text</td></tr></table>`}

	require.Equal(t, "https://img.example.com/p.png", Articles(components.Feed{ID: "f"}, []store.Article{a}, false, 0, nil).Items[0].Image)

	detail := articleOf(a)
	require.Equal(t, "https://img.example.com/p.png", detail.Image)
	require.NotContains(t, detail.HTML, "p.png")
	require.NotContains(t, detail.HTML, `<a href="https://example.com/p"`)
	require.Contains(t, detail.HTML, "text")
}

func TestArticleDropsSmallerVersionsOfThePictureOnTop(t *testing.T) {
	detail := articleOf(store.Article{
		Title:   "T",
		Content: `<a href="https://preview.example.com/p.png?width=891"><img src="https://preview.example.com/p.png?width=140&height=128"></a><img src="https://preview.example.com/other.png">`,
	})

	require.Equal(t, "https://preview.example.com/p.png?width=891", detail.Image)
	require.NotContains(t, detail.HTML, "p.png")
	require.Contains(t, detail.HTML, "other.png")
}

func TestYoutubeLinksGetAPlayer(t *testing.T) {
	const id = "dQw4w9WgXcQ"

	detail := articleOf(store.Article{
		Title: "T",
		Content: `<p>Watch <a href="https://www.youtube.com/watch?v=` + id + `&t=90s">this</a> and again <a href="https://youtu.be/` + id + `">here</a>.</p>` +
			`<p><a href="https://youtube.com/shorts/abcdefghijk">short</a></p>` +
			`<p><a href="https://youtube.com.evil.example/watch?v=` + id + `">fake host</a> <a href="https://www.youtube.com/watch?v=short">bad id</a> <a href="https://www.youtube.com/@channel">channel</a></p>`,
	})

	require.Equal(t, 2, strings.Count(detail.HTML, "<iframe"), "one player per video")
	require.Contains(t, detail.HTML, `src="https://www.youtube-nocookie.com/embed/`+id+`?start=90"`)
	require.Contains(t, detail.HTML, `src="https://www.youtube-nocookie.com/embed/abcdefghijk"`)
	require.Contains(t, detail.HTML, "sandbox=")
	require.Contains(t, detail.HTML, `referrerpolicy="strict-origin-when-cross-origin"`)
	// the links stay, the player is after the paragraph that has the link
	require.Contains(t, detail.HTML, `>this</a>`)
	require.Less(t, strings.Index(detail.HTML, `>here</a>`), strings.Index(detail.HTML, "<iframe"))
	require.NotContains(t, detail.HTML, "evil.example/embed")
}

func TestYoutubeScriptsAndFramesInTheTextStayOut(t *testing.T) {
	detail := articleOf(store.Article{
		Title:   "T",
		Content: `<iframe src="https://www.youtube.com/embed/dQw4w9WgXcQ"></iframe><iframe src="https://evil.example"></iframe><p>x</p>`,
	})

	require.NotContains(t, detail.HTML, "<iframe", "iframes of the feed are removed, players are only added for links")
}

func TestAnArticleThatIsAVideoShowsItOnTop(t *testing.T) {
	detail := articleOf(store.Article{
		Title:    "A video",
		URL:      "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		ImageURL: "https://i.ytimg.com/vi/dQw4w9WgXcQ/hqdefault.jpg",
		Content:  `<p>Notes, see <a href="https://youtu.be/dQw4w9WgXcQ">the video</a> and <a href="https://youtu.be/abcdefghijk">another</a></p>`,
	})

	require.Equal(t, "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ", detail.Video)
	require.Empty(t, detail.Image, "the player replaces the picture")
	require.Equal(t, 1, strings.Count(detail.HTML, "<iframe"), "the video on top is not shown a second time")
	require.Contains(t, detail.HTML, "abcdefghijk")
}

func TestParagraphsDoNotRunTogetherInTheSummary(t *testing.T) {
	page := Articles(components.Feed{ID: "f"}, []store.Article{{Title: "T", Summary: "<p>One.</p><p>Two.</p>"}}, false, 0, nil)
	require.Equal(t, "One. Two.", page.Items[0].Summary)
}

// articleOf is Article for an article the user did not save.
func articleOf(a store.Article) pages.ArticleDetail {
	return Article(a, store.SavedFlags{})
}

func TestListArticlesKnowTheirFeed(t *testing.T) {
	page := ListArticles([]store.FeedArticle{
		{Article: store.Article{ID: "a1", Title: "One"}, Feed: store.Feed{ID: "f1", Title: "Blog", IconURL: "https://x.example/i.png"}},
		{Article: store.Article{ID: "a2", Title: "Two"}, Feed: store.Feed{ID: "f2", Title: "News"}},
	}, true, 30, "/articles?view=home", map[string]store.SavedFlags{"a2": {Favourite: true}})

	require.True(t, page.ShowFeed)
	require.Equal(t, "/articles?view=home&offset=32", page.MoreURL)
	require.False(t, page.Items[0].Favourite)
	require.True(t, page.Items[1].Favourite)
	require.Equal(t, "f1", page.Items[0].FeedID)
	require.Equal(t, "Blog", page.Items[0].FeedName)
	require.Contains(t, page.Items[0].FeedIcon, "/icons/f1?v=")
	require.Equal(t, "f2", page.Items[1].FeedID)
	require.Empty(t, page.Items[1].FeedIcon)

	feedPage := Articles(components.Feed{ID: "f1"}, []store.Article{{ID: "a"}}, true, 30, nil)
	require.False(t, feedPage.ShowFeed)
	require.Equal(t, "/feeds/f1/articles?offset=31", feedPage.MoreURL)
}

func TestRedditLayoutTableIsFlattenedAndItsThumbnailDropped(t *testing.T) {
	// the shape of an entry of reddit: the thumbnail on another host than the full picture, both in a table
	detail := articleOf(store.Article{
		Title:    "T",
		ImageURL: "https://i.redd.it/abc123.png",
		Content: `<table> <tr><td> <a href="https://www.reddit.com/r/x/comments/1/t/"> <img src="https://preview.redd.it/abc123.png?width=640&crop=smart&auto=webp&s=ff" alt="T"/> </a> </td>` +
			`<td><div class="md"><p>The text of the post.</p></div> submitted by <a href="https://www.reddit.com/user/u">/u/u</a> <span><a href="https://i.redd.it/abc123.png">[link]</a></span></td></tr></table>`,
	})

	require.Equal(t, "https://i.redd.it/abc123.png", detail.Image)
	require.NotContains(t, detail.HTML, "<table", "the layout table is gone")
	require.NotContains(t, detail.HTML, "<td")
	require.NotContains(t, detail.HTML, "preview.redd.it", "the thumbnail is the picture on top")
	require.NotContains(t, detail.HTML, `href="https://www.reddit.com/r/x/comments/1/t/"`, "no empty link left")
	require.Contains(t, detail.HTML, "The text of the post.")
	require.Contains(t, detail.HTML, "[link]", "the other links stay")

	// "submitted by ... [link]" is set apart from the text of the post
	footerStart := strings.Index(detail.HTML, `<div class="post-footer">`)
	require.Positive(t, footerStart)
	require.Greater(t, footerStart, strings.Index(detail.HTML, "The text of the post."))
	require.Contains(t, detail.HTML[footerStart:], "submitted by")
	require.Contains(t, detail.HTML[footerStart:], "[link]")
	require.NotContains(t, detail.HTML[:footerStart], "submitted by")
}

func TestDataTablesAndOtherSitesImagesStay(t *testing.T) {
	detail := articleOf(store.Article{
		Title:    "T",
		ImageURL: "https://a.example.com/photo.png",
		Content: `<table><tr><th>A</th><th>B</th></tr><tr><td>1</td><td>2</td></tr></table>` +
			`<img src="https://b.other.org/photo.png">`,
	})

	require.Contains(t, detail.HTML, "<table", "a table with several rows is data")
	require.Contains(t, detail.HTML, "b.other.org/photo.png", "the same file name on another site is another picture")
}

func TestRedditFooterOfAPostWithoutPictureIsSetApart(t *testing.T) {
	// a post of only text is not in a table, the footer follows the text at the top level
	detail := articleOf(store.Article{
		Title: "T",
		Content: `<div class="md"><p>One.</p> <p>Thank you.</p> </div> &#32; submitted by &#32; <a href="https://www.reddit.com/user/nunbar"> /u/nunbar </a> <br/> ` +
			`<span><a href="https://www.reddit.com/r/x/comments/1/t/">[link]</a></span> &#32; <span><a href="https://www.reddit.com/r/x/comments/1/t/">[comments]</a></span>`,
	})

	footerStart := strings.Index(detail.HTML, `<div class="post-footer">`)
	require.Positive(t, footerStart)
	require.Greater(t, footerStart, strings.Index(detail.HTML, "Thank you."))
	require.Contains(t, detail.HTML[footerStart:], "submitted by")
	require.Contains(t, detail.HTML[footerStart:], "[comments]")
	require.NotContains(t, detail.HTML[:footerStart], "submitted by")
}

func TestTrailingContentThatIsNotTheRedditFooterIsLeftAlone(t *testing.T) {
	detail := articleOf(store.Article{Title: "T", Content: `<p>Text.</p> Thanks for reading, <a href="https://example.com">my site</a>`})

	require.NotContains(t, detail.HTML, "post-footer")
	require.Contains(t, detail.HTML, "Thanks for reading")
}

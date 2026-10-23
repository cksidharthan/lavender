package backup_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	stdpng "image/png"
	"testing"
	"time"

	"github.com/cksidharthan/lavender/pkg/backup"
	"github.com/cksidharthan/lavender/pkg/feedfetch"
	"github.com/cksidharthan/lavender/pkg/iconcache"
	_ "github.com/cksidharthan/lavender/pkg/pocketbase/migrations"
	"github.com/cksidharthan/lavender/pkg/store"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/require"
)

type instance struct {
	app   core.App
	store *store.Store
	icons *iconcache.Cache
	users map[string]string
}

func newInstance(t *testing.T, names ...string) *instance {
	t.Helper()

	app, err := tests.NewTestApp()
	require.NoError(t, err)
	t.Cleanup(app.Cleanup)
	require.NoError(t, app.RunAppMigrations())
	require.NoError(t, app.ReloadCachedCollections())

	icons, err := iconcache.New(t.TempDir())
	require.NoError(t, err)

	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)
	ids := map[string]string{}
	for _, name := range names {
		u := core.NewRecord(users)
		u.SetEmail(name + "@example.com")
		u.SetPassword("password123")
		require.NoError(t, app.Save(u))
		ids[name] = u.Id
	}

	return &instance{app: app, store: store.New(app), icons: icons, users: ids}
}

func png(t *testing.T) []byte {
	t.Helper()

	var buf bytes.Buffer
	require.NoError(t, stdpng.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 16, 16))))

	return buf.Bytes()
}

// fill gives alice a folder, two feeds with articles, saved articles, settings and an icon.
func fill(t *testing.T, in *instance, user string) {
	t.Helper()

	require.NoError(t, in.store.CreateFolder(user, "Tech"))
	tree, err := in.store.Tree(user)
	require.NoError(t, err)
	folderID := tree[0].ID

	day := func(d int) time.Time { return time.Date(2025, 3, d, 10, 0, 0, 0, time.UTC) }

	blog, err := in.store.AddFeed(user, "https://blog.example.com/feed", folderID, feedfetch.Meta{Title: "Blog", Type: "atom", IconURL: "https://blog.example.com/logo.png", SiteURL: "https://blog.example.com"})
	require.NoError(t, err)
	news, err := in.store.AddFeed(user, "https://news.example.com/feed", "", feedfetch.Meta{Title: "News", Type: "rss"})
	require.NoError(t, err)

	_, err = in.store.SaveFetch(blog.ID, feedfetch.Result{Meta: feedfetch.Meta{Type: "atom", SiteURL: "https://blog.example.com"}, Items: []feedfetch.Item{
		{GUID: "b1", Title: "Blog one", URL: "https://blog.example.com/1", Content: "<p>One</p>", Author: "Ann", ImageURL: "https://img.example.com/1.png", PublishedAt: day(1)},
		{GUID: "b2", Title: "Blog two", URL: "https://blog.example.com/2", Summary: "Two", PublishedAt: day(2)},
	}})
	require.NoError(t, err)
	_, err = in.store.SaveFetch(news.ID, feedfetch.Result{Meta: feedfetch.Meta{Type: "rss"}, Items: []feedfetch.Item{{GUID: "n1", Title: "News one", PublishedAt: day(3)}}})
	require.NoError(t, err)

	require.NoError(t, in.store.UpdateFeedSettings(user, blog.ID, store.FeedSettings{RefreshMinutes: 30, HideFromHome: true}))
	require.NoError(t, in.store.UpdateFeedSettings(user, news.ID, store.FeedSettings{Paused: true, IconOverride: "https://cdn.example.com/news.png"}))
	require.NoError(t, in.store.SetDefaultRefreshMinutes(user, 45))

	articles, _, err := in.store.Articles(user, blog.ID, 10, 0)
	require.NoError(t, err)
	byTitle := map[string]string{}
	for _, a := range articles {
		byTitle[a.Title] = a.ID
	}
	_, err = in.store.ToggleSaved(user, byTitle["Blog one"], "favourite")
	require.NoError(t, err)
	_, err = in.store.ToggleSaved(user, byTitle["Blog two"], "read_later")
	require.NoError(t, err)

	// an icon that was downloaded for the blog
	meta := []byte(`{"content_type":"image/png","fetched_at":"` + time.Now().UTC().Format(time.RFC3339Nano) + `"}`)
	require.True(t, in.icons.Restore(keyOf(t, in, "https://blog.example.com/logo.png"), meta, png(t)))
}

// keyOf finds the key of an icon by storing it under the url and reading the files back.
func keyOf(t *testing.T, in *instance, url string) string {
	t.Helper()

	files := in.icons.Files(url)
	if len(files) > 0 {
		return files[0].Name[:64]
	}

	// no icon yet: the key is the sha256 of the url, see iconcache
	return sha(url)
}

func export(t *testing.T, in *instance, user string) []byte {
	t.Helper()

	var buf bytes.Buffer
	_, err := backup.Export(&buf, in.app, user, in.icons)
	require.NoError(t, err)

	return buf.Bytes()
}

func importInto(in *instance, user string, data []byte) (backup.Counts, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return backup.Counts{}, err
	}

	return backup.Import(zr, in.app, user, in.icons)
}

func TestExportAndImportIntoAFreshInstance(t *testing.T) {
	source := newInstance(t, "alice", "bob")
	fill(t, source, source.users["alice"])
	// the data of another user is not in the backup
	require.NoError(t, source.store.CreateFolder(source.users["bob"], "Bobs folder"))
	_, err := source.store.AddFeed(source.users["bob"], "https://bob.example.com/feed", "", feedfetch.Meta{Title: "Bobs feed", Type: "rss"})
	require.NoError(t, err)

	data := export(t, source, source.users["alice"])

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	for _, name := range []string{"manifest.json", "settings.json", "folders.json", "feeds.json", "articles.ndjson", "saved.json"} {
		require.True(t, names[name], name)
	}
	require.NotContains(t, string(data), "Bobs feed", "other users are not in the backup")

	fresh := newInstance(t, "carol")
	counts, err := importInto(fresh, fresh.users["carol"], data)
	require.NoError(t, err)
	require.Equal(t, backup.Counts{Folders: 1, Feeds: 2, Articles: 3, Saved: 2, Icons: 1}, counts)

	carol := fresh.users["carol"]
	stats, err := fresh.store.Stats(carol, 10*time.Minute)
	require.NoError(t, err)
	require.Equal(t, 1, stats.Folders)
	require.Equal(t, 2, stats.Feeds)
	require.Equal(t, 3, stats.Articles)
	require.Equal(t, 1, stats.Images)
	require.Equal(t, 1, stats.Favourites)
	require.Equal(t, 1, stats.ReadLater)
	require.Equal(t, 45, stats.DefaultRefreshMinutes)

	require.Len(t, stats.PerFeed, 2)
	blog, news := stats.PerFeed[0], stats.PerFeed[1]
	require.Equal(t, "Blog", blog.Title)
	require.Equal(t, "Tech", blog.Folder)
	require.Equal(t, 2, blog.Articles)
	require.Equal(t, 30, blog.RefreshMinutes)
	require.True(t, blog.Hidden)
	require.Equal(t, "News", news.Title)
	require.Empty(t, news.Folder)
	require.True(t, news.Paused)
	require.Equal(t, 45, news.RefreshMinutes, "the default of the user")

	found, err := fresh.store.FindFeed(carol, news.ID)
	require.NoError(t, err)
	require.Equal(t, "https://cdn.example.com/news.png", found.Settings.IconOverride)

	// the articles are what they were, also what was saved
	articles, _, err := fresh.store.Articles(carol, blog.ID, 10, 0)
	require.NoError(t, err)
	require.Equal(t, "Blog two", articles[0].Title)
	require.Equal(t, "Blog one", articles[1].Title)
	require.Equal(t, "Ann", articles[1].Author)
	require.Equal(t, "https://img.example.com/1.png", articles[1].ImageURL)
	require.Equal(t, 2025, articles[1].PublishedAt.Year())
	favourites, _, err := fresh.store.SavedArticles(carol, "favourite", 10, 0)
	require.NoError(t, err)
	require.Equal(t, "Blog one", favourites[0].Title)

	// the icon came along
	require.Len(t, fresh.icons.Files("https://blog.example.com/logo.png"), 2)

	// nothing of the others
	bob, err := fresh.store.Tree(carol)
	require.NoError(t, err)
	for _, f := range bob {
		require.NotEqual(t, "Bobs folder", f.Name)
	}
}

func TestImportingTwiceChangesNothing(t *testing.T) {
	source := newInstance(t, "alice")
	fill(t, source, source.users["alice"])
	data := export(t, source, source.users["alice"])

	fresh := newInstance(t, "carol")
	_, err := importInto(fresh, fresh.users["carol"], data)
	require.NoError(t, err)

	counts, err := importInto(fresh, fresh.users["carol"], data)
	require.NoError(t, err)
	require.Equal(t, backup.Counts{}, counts, "all of it is in the account already")

	stats, err := fresh.store.Stats(fresh.users["carol"], 10*time.Minute)
	require.NoError(t, err)
	require.Equal(t, 2, stats.Feeds)
	require.Equal(t, 3, stats.Articles)
	require.Equal(t, 1, stats.Favourites)

	// importing into the account it came from adds nothing either
	counts, err = importInto(source, source.users["alice"], data)
	require.NoError(t, err)
	require.Equal(t, backup.Counts{}, counts)
}

func TestImportAddsToWhatIsThere(t *testing.T) {
	source := newInstance(t, "alice")
	fill(t, source, source.users["alice"])
	data := export(t, source, source.users["alice"])

	fresh := newInstance(t, "carol")
	carol := fresh.users["carol"]
	require.NoError(t, fresh.store.CreateFolder(carol, "Tech"))
	own, err := fresh.store.AddFeed(carol, "https://blog.example.com/feed", "", feedfetch.Meta{Title: "My own name for the blog", Type: "rss"})
	require.NoError(t, err)
	require.NoError(t, fresh.store.SetDefaultRefreshMinutes(carol, 15))

	counts, err := importInto(fresh, carol, data)
	require.NoError(t, err)
	require.Equal(t, 0, counts.Folders, "the folder with that name is used")
	require.Equal(t, 1, counts.Feeds, "only the feed that is not there")
	require.Equal(t, 3, counts.Articles)

	found, err := fresh.store.FindFeed(carol, own.ID)
	require.NoError(t, err)
	require.Equal(t, "My own name for the blog", found.Title, "what is there is not changed")
	articles, _, err := fresh.store.Articles(carol, own.ID, 10, 0)
	require.NoError(t, err)
	require.Len(t, articles, 2, "the articles are added to the feed that is there")

	days, err := fresh.store.DefaultRefreshMinutes(carol)
	require.NoError(t, err)
	require.Equal(t, 15, days, "the setting of the user is kept")
}

func TestInvalidFilesAreRefusedAndImportNothing(t *testing.T) {
	fresh := newInstance(t, "carol")
	carol := fresh.users["carol"]

	zipOf := func(files map[string]string) []byte {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for name, content := range files {
			w, err := zw.Create(name)
			require.NoError(t, err)
			_, err = w.Write([]byte(content))
			require.NoError(t, err)
		}
		require.NoError(t, zw.Close())

		return buf.Bytes()
	}
	valid := map[string]string{
		"manifest.json": `{"app":"lavender","version":1}`, "settings.json": `{}`, "folders.json": `[]`,
		"feeds.json": `[]`, "articles.ndjson": ``, "saved.json": `[]`,
	}
	with := func(name, content string) map[string]string {
		files := map[string]string{}
		for k, v := range valid {
			files[k] = v
		}
		files[name] = content

		return files
	}

	var invalid backup.InvalidError
	for name, data := range map[string][]byte{
		"another app":      zipOf(with("manifest.json", `{"app":"other","version":1}`)),
		"newer version":    zipOf(with("manifest.json", `{"app":"lavender","version":99}`)),
		"no manifest":      zipOf(map[string]string{"feeds.json": `[]`}),
		"bad json":         zipOf(with("feeds.json", `{nope`)),
		"bad article":      zipOf(with("articles.ndjson", "not json\n")),
		"missing articles": zipOf(map[string]string{"manifest.json": valid["manifest.json"], "settings.json": `{}`, "folders.json": `[]`, "feeds.json": `[]`, "saved.json": `[]`}),
	} {
		_, err := importInto(fresh, carol, data)
		require.ErrorAs(t, err, &invalid, name)
	}

	// a file that is not a zip
	_, err := importInto(fresh, carol, []byte("this is not a zip"))
	require.Error(t, err)

	// a broken article in the middle: what came before is not imported either
	good := `{"id":"a1","feed_id":"f1","guid":"g1","title":"fine"}` + "\n"
	files := with("feeds.json", `[{"id":"f1","feed_url":"https://x.example.com/feed","title":"X","feed_type":"rss","is_active":true}]`)
	files["articles.ndjson"] = good + "broken\n"
	_, err = importInto(fresh, carol, zipOf(files))
	require.ErrorAs(t, err, &invalid)
	stats, err := fresh.store.Stats(carol, 10*time.Minute)
	require.NoError(t, err)
	require.Zero(t, stats.Feeds, "all or nothing")
	require.Zero(t, stats.Articles)
}

func TestImportDropsWhatIsNotValid(t *testing.T) {
	fresh := newInstance(t, "carol")
	carol := fresh.users["carol"]

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, content string) {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(content))
		require.NoError(t, err)
	}
	add("manifest.json", `{"app":"lavender","version":1}`)
	add("settings.json", `{"default_refresh_minutes":2}`)
	add("folders.json", `[{"id":"d1","name":"  "},{"id":"d2","name":"Fine"}]`)
	add("feeds.json", `[
		{"id":"f1","folder_id":"d2","feed_url":"https://ok.example.com/feed","title":"Ok","feed_type":"made-up","icon_url":"javascript:alert(1)","icon_override":"ftp://x/i.png","refresh_minutes":2,"is_active":true},
		{"id":"f2","feed_url":"javascript:alert(1)","title":"Bad","feed_type":"rss"},
		{"id":"f3","feed_url":"file:///etc/passwd","title":"Bad","feed_type":"rss"}
	]`)
	add("articles.ndjson", `{"id":"a1","feed_id":"f1","guid":"g1","title":"Ok","url":"javascript:alert(1)","image_url":"ftp://x/i.png"}`+"\n"+
		`{"id":"a2","feed_id":"f2","guid":"g2","title":"Of a feed that was not imported"}`+"\n"+
		`{"id":"a3","feed_id":"f1","guid":"","title":"No id"}`+"\n"+
		`{"id":"a4","feed_id":"unknown","guid":"g4","title":"Unknown feed"}`+"\n")
	add("saved.json", `[{"article_id":"a1","kind":"favourite"},{"article_id":"a1","kind":"starred"},{"article_id":"a2","kind":"favourite"}]`)
	// names that are not files of the icon cache are never used
	add("icons/../../etc/passwd", "x")
	add("icons/not-a-key.img", "x")
	add("icons/"+sha("https://a.example.com/i.png")+".img", "this is not an image")
	add("icons/"+sha("https://a.example.com/i.png")+".json", `{"content_type":"image/png"}`)
	require.NoError(t, zw.Close())

	counts, err := importInto(fresh, carol, buf.Bytes())
	require.NoError(t, err)
	require.Equal(t, backup.Counts{Folders: 1, Feeds: 1, Articles: 1, Saved: 1}, counts)

	stats, err := fresh.store.Stats(carol, 10*time.Minute)
	require.NoError(t, err)
	require.Zero(t, stats.DefaultRefreshMinutes, "an interval that is too short is not taken")
	require.Equal(t, "Ok", stats.PerFeed[0].Title)
	require.Equal(t, "Fine", stats.PerFeed[0].Folder)
	require.Equal(t, 10, stats.PerFeed[0].RefreshMinutes, "the interval of the feed that is too short is not taken")

	articles, _, err := fresh.store.Articles(carol, stats.PerFeed[0].ID, 10, 0)
	require.NoError(t, err)
	require.Empty(t, articles[0].URL)
	require.Empty(t, articles[0].ImageURL)

	found, err := fresh.store.FindFeed(carol, stats.PerFeed[0].ID)
	require.NoError(t, err)
	require.Empty(t, found.Settings.IconOverride)

	icons, _, err := fresh.icons.Stats()
	require.NoError(t, err)
	require.Zero(t, icons, "an icon that is not an image is not restored")
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))

	return hex.EncodeToString(h[:])
}

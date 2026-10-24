package store_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cksidharthan/lavender/pkg/feedfetch"
	_ "github.com/cksidharthan/lavender/pkg/pocketbase/migrations"
	"github.com/cksidharthan/lavender/pkg/store"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/require"
)

func newStore(t *testing.T) (*store.Store, string, string) {
	t.Helper()

	app, err := tests.NewTestApp()
	require.NoError(t, err)
	t.Cleanup(app.Cleanup)
	require.NoError(t, app.RunAppMigrations())
	require.NoError(t, app.ReloadCachedCollections())

	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)

	newUser := func(email string) string {
		u := core.NewRecord(users)
		u.SetEmail(email)
		u.SetPassword("password123")
		require.NoError(t, app.Save(u))

		return u.Id
	}

	return store.New(app), newUser("a@example.com"), newUser("b@example.com")
}

func TestFoldersAndFeeds(t *testing.T) {
	s, alice, bob := newStore(t)

	require.NoError(t, s.CreateFolder(alice, "News"))
	require.NoError(t, s.CreateFolder(alice, "Blogs"))
	require.NoError(t, s.CreateFolder(bob, "News"), "folder names are per user")

	var invalid store.InvalidError
	require.ErrorAs(t, s.CreateFolder(alice, "News"), &invalid)
	require.ErrorAs(t, s.CreateFolder(alice, "  "), &invalid)

	tree, err := s.Tree(alice)
	require.NoError(t, err)
	require.Len(t, tree, 2)
	require.Equal(t, "Blogs", tree[0].Name)
	news := tree[1]

	feed, err := s.AddFeed(alice, "https://go.dev/blog/feed.atom", news.ID, feedfetch.Meta{Title: "Go Blog", Type: "atom"})
	require.NoError(t, err)
	_, err = s.AddFeed(alice, "https://go.dev/blog/feed.atom", "", feedfetch.Meta{Type: "atom"})
	require.ErrorAs(t, err, &invalid, "the same feed twice")
	_, err = s.AddFeed(bob, "https://go.dev/blog/feed.atom", "", feedfetch.Meta{Type: "atom"})
	require.NoError(t, err, "feeds are per user")
	_, err = s.AddFeed(bob, "https://x.dev/feed", news.ID, feedfetch.Meta{Type: "rss"})
	require.ErrorAs(t, err, &invalid, "a folder of another user")

	// nothing of alice can be touched by bob
	require.ErrorIs(t, s.RenameFeed(bob, feed.ID, "mine"), store.ErrNotFound)
	require.ErrorIs(t, s.DeleteFeed(bob, feed.ID), store.ErrNotFound)
	require.ErrorIs(t, s.RenameFolder(bob, news.ID, "mine"), store.ErrNotFound)
	require.ErrorIs(t, s.DeleteFolder(bob, news.ID), store.ErrNotFound)

	require.NoError(t, s.RenameFeed(alice, feed.ID, "The Go Blog"))
	require.NoError(t, s.RenameFolder(alice, news.ID, "Tech"))

	// deleting a folder keeps its feeds, outside the folder
	require.NoError(t, s.DeleteFolder(alice, news.ID))
	tree, err = s.Tree(alice)
	require.NoError(t, err)
	require.Len(t, tree, 2)
	require.Equal(t, "Blogs", tree[0].Name)
	require.Empty(t, tree[1].ID)
	require.Equal(t, "The Go Blog", tree[1].Feeds[0].Title)

	require.NoError(t, s.DeleteFeed(alice, feed.ID))
	_, err = s.FindFeed(alice, feed.ID)
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestArticlesArePagedNewestFirstAndPerUser(t *testing.T) {
	s, alice, bob := newStore(t)

	feed, err := s.AddFeed(alice, "https://example.com/feed", "", feedfetch.Meta{Title: "Blog", Type: "rss"})
	require.NoError(t, err)

	items := make([]feedfetch.Item, 0, 5)
	for i := range 5 {
		items = append(items, feedfetch.Item{GUID: string(rune('a' + i)), Title: string(rune('A' + i)), PublishedAt: time.Date(2024, 1, 1+i, 0, 0, 0, 0, time.UTC)})
	}
	added, err := s.SaveFetch(feed.ID, feedfetch.Result{Meta: feedfetch.Meta{Type: "rss"}, Items: items})
	require.NoError(t, err)
	require.Equal(t, 5, added)

	added, err = s.SaveFetch(feed.ID, feedfetch.Result{Meta: feedfetch.Meta{Type: "rss"}, Items: items})
	require.NoError(t, err)
	require.Zero(t, added, "articles are stored once")

	page, more, err := s.Articles(alice, feed.ID, 2, 0)
	require.NoError(t, err)
	require.True(t, more)
	require.Equal(t, []string{"E", "D"}, []string{page[0].Title, page[1].Title})

	page, more, err = s.Articles(alice, feed.ID, 2, 4)
	require.NoError(t, err)
	require.False(t, more)
	require.Len(t, page, 1)
	require.Equal(t, "A", page[0].Title)

	_, _, err = s.Articles(bob, feed.ID, 2, 0)
	require.ErrorIs(t, err, store.ErrNotFound)

	// deleting the feed deletes its articles
	require.NoError(t, s.DeleteFeed(alice, feed.ID))
}

func TestAllArticlesMixTheFeedsNewestFirstAndOnlyOfTheUser(t *testing.T) {
	s, alice, bob := newStore(t)

	blog, err := s.AddFeed(alice, "https://blog.example.com/feed", "", feedfetch.Meta{Title: "Blog", Type: "rss"})
	require.NoError(t, err)
	news, err := s.AddFeed(alice, "https://news.example.com/feed", "", feedfetch.Meta{Title: "News", Type: "rss"})
	require.NoError(t, err)
	other, err := s.AddFeed(bob, "https://other.example.com/feed", "", feedfetch.Meta{Title: "Other", Type: "rss"})
	require.NoError(t, err)

	day := func(d int) time.Time { return time.Date(2024, 5, d, 12, 0, 0, 0, time.UTC) }
	item := func(guid string, d int) feedfetch.Item {
		return feedfetch.Item{GUID: guid, Title: guid, PublishedAt: day(d)}
	}
	save := func(feedID string, items ...feedfetch.Item) {
		_, err := s.SaveFetch(feedID, feedfetch.Result{Meta: feedfetch.Meta{Type: "rss"}, Items: items})
		require.NoError(t, err)
	}
	save(blog.ID, item("blog-1", 1), item("blog-4", 4))
	save(news.ID, item("news-3", 3), item("news-5", 5))
	save(other.ID, item("bob-6", 6))

	page, more, err := s.AllArticles(alice, 3, 0)
	require.NoError(t, err)
	require.True(t, more)
	require.Equal(t, []string{"news-5", "blog-4", "news-3"}, []string{page[0].Title, page[1].Title, page[2].Title})
	require.Equal(t, "News", page[0].Feed.Title)
	require.Equal(t, news.ID, page[0].Feed.ID)
	require.Equal(t, "Blog", page[1].Feed.Title)

	page, more, err = s.AllArticles(alice, 3, 3)
	require.NoError(t, err)
	require.False(t, more)
	require.Len(t, page, 1)
	require.Equal(t, "blog-1", page[0].Title)

	// an article without a date is dated when it is found, so it is among the newest
	save(blog.ID, feedfetch.Item{GUID: "undated", Title: "undated"})
	page, _, err = s.AllArticles(alice, 1, 0)
	require.NoError(t, err)
	require.Equal(t, "undated", page[0].Title)

	bobPage, _, err := s.AllArticles(bob, 10, 0)
	require.NoError(t, err)
	require.Len(t, bobPage, 1)
	require.Equal(t, "bob-6", bobPage[0].Title)
}

func TestFeedSettings(t *testing.T) {
	s, alice, bob := newStore(t)

	feed, err := s.AddFeed(alice, "https://blog.example.com/feed", "", feedfetch.Meta{Title: "Blog", Type: "rss", IconURL: "https://blog.example.com/logo.png"})
	require.NoError(t, err)
	visible, err := s.AddFeed(alice, "https://news.example.com/feed", "", feedfetch.Meta{Title: "News", Type: "rss"})
	require.NoError(t, err)

	found, err := s.FindFeed(alice, feed.ID)
	require.NoError(t, err)
	require.Equal(t, store.FeedSettings{}, found.Settings, "the defaults")
	require.Equal(t, "https://blog.example.com/logo.png", found.IconURL)

	custom := store.FeedSettings{RefreshMinutes: 30, HideFromHome: true, IconOverride: " https://cdn.example.com/mine.png ", Paused: true}
	require.NoError(t, s.UpdateFeedSettings(alice, feed.ID, custom))

	found, err = s.FindFeed(alice, feed.ID)
	require.NoError(t, err)
	require.Equal(t, store.FeedSettings{RefreshMinutes: 30, HideFromHome: true, IconOverride: "https://cdn.example.com/mine.png", Paused: true}, found.Settings)
	require.Equal(t, "https://cdn.example.com/mine.png", found.IconURL, "the icon of the user wins")
	require.Equal(t, []string{"https://cdn.example.com/mine.png", "https://blog.example.com/logo.png", "https://blog.example.com/favicon.ico"}, found.IconCandidates[:3])

	var invalid store.InvalidError
	for _, bad := range []store.FeedSettings{
		{RefreshMinutes: 1}, {RefreshMinutes: 4}, {RefreshMinutes: 1441}, {RefreshMinutes: -5},
		{IconOverride: "javascript:alert(1)"}, {IconOverride: "not a url"}, {IconOverride: "ftp://x.example/i.png"},
	} {
		require.ErrorAs(t, s.UpdateFeedSettings(alice, feed.ID, bad), &invalid, "%+v", bad)
	}
	require.ErrorIs(t, s.UpdateFeedSettings(bob, feed.ID, store.FeedSettings{}), store.ErrNotFound)

	// a hidden feed is not on the home page, but its articles are in the feed
	for _, id := range []string{feed.ID, visible.ID} {
		_, err := s.SaveFetch(id, feedfetch.Result{Meta: feedfetch.Meta{Type: "rss"}, Items: []feedfetch.Item{{GUID: id, Title: id}}})
		require.NoError(t, err)
	}
	home, _, err := s.AllArticles(alice, 10, 0)
	require.NoError(t, err)
	require.Len(t, home, 1)
	require.Equal(t, visible.ID, home[0].Feed.ID)
	own, _, err := s.Articles(alice, feed.ID, 10, 0)
	require.NoError(t, err)
	require.Len(t, own, 1)

	// back to the defaults
	require.NoError(t, s.UpdateFeedSettings(alice, feed.ID, store.FeedSettings{}))
	home, _, err = s.AllArticles(alice, 10, 0)
	require.NoError(t, err)
	require.Len(t, home, 2)
}

func TestDueFeedsUseTheIntervalOfTheFeed(t *testing.T) {
	s, alice, _ := newStore(t)

	add := func(name string, settings store.FeedSettings) string {
		feed, err := s.AddFeed(alice, "https://"+name+".example.com/feed", "", feedfetch.Meta{Title: name, Type: "rss"})
		require.NoError(t, err)
		require.NoError(t, s.UpdateFeedSettings(alice, feed.ID, settings))
		_, err = s.SaveFetch(feed.ID, feedfetch.Result{Meta: feedfetch.Meta{Type: "rss"}}) // fetched now
		require.NoError(t, err)

		return feed.ID
	}
	standard := add("standard", store.FeedSettings{})
	often := add("often", store.FeedSettings{RefreshMinutes: 5})
	rarely := add("rarely", store.FeedSettings{RefreshMinutes: 120})
	paused := add("paused", store.FeedSettings{Paused: true})
	never, err := s.AddFeed(alice, "https://never.example.com/feed", "", feedfetch.Meta{Title: "never", Type: "rss"})
	require.NoError(t, err)

	due := func(after time.Duration) []string {
		ids, err := s.DueFeeds(time.Now().Add(after), 10*time.Minute, 100)
		require.NoError(t, err)

		return ids
	}

	require.ElementsMatch(t, []string{never.ID}, due(time.Minute), "only the feed that was never fetched")
	require.ElementsMatch(t, []string{never.ID, often}, due(6*time.Minute), "the feed with 5 minutes is due, the default of 10 is not")
	require.ElementsMatch(t, []string{never.ID, often, standard}, due(11*time.Minute))
	require.ElementsMatch(t, []string{never.ID, often, standard, rarely}, due(121*time.Minute), "a paused feed is never due")
	require.NotContains(t, due(1000*time.Minute), paused)
}

func TestSavedArticlesAndRecentArticles(t *testing.T) {
	s, alice, bob := newStore(t)

	feed, err := s.AddFeed(alice, "https://blog.example.com/feed", "", feedfetch.Meta{Title: "Blog", Type: "rss"})
	require.NoError(t, err)
	bobFeed, err := s.AddFeed(bob, "https://other.example.com/feed", "", feedfetch.Meta{Title: "Other", Type: "rss"})
	require.NoError(t, err)

	now := time.Now()
	_, err = s.SaveFetch(feed.ID, feedfetch.Result{Meta: feedfetch.Meta{Type: "rss"}, Items: []feedfetch.Item{
		{GUID: "new", Title: "new", PublishedAt: now.Add(-2 * time.Hour)},
		{GUID: "today", Title: "today", PublishedAt: now.Add(-20 * time.Hour)},
		{GUID: "old", Title: "old", PublishedAt: now.Add(-30 * time.Hour)},
	}})
	require.NoError(t, err)
	_, err = s.SaveFetch(bobFeed.ID, feedfetch.Result{Meta: feedfetch.Meta{Type: "rss"}, Items: []feedfetch.Item{{GUID: "bobs", Title: "bobs", PublishedAt: now}}})
	require.NoError(t, err)

	// today: the last 24 hours, of the feeds of the user
	recent, more, err := s.RecentArticles(alice, now.Add(-24*time.Hour), 10, 0)
	require.NoError(t, err)
	require.False(t, more)
	require.Equal(t, []string{"new", "today"}, []string{recent[0].Title, recent[1].Title})

	all, _, err := s.AllArticles(alice, 10, 0)
	require.NoError(t, err)
	byTitle := map[string]string{}
	for _, a := range all {
		byTitle[a.Title] = a.ID
	}
	require.Len(t, byTitle, 3)

	// saving: a toggle, per kind
	active, err := s.ToggleSaved(alice, byTitle["old"], "favourite")
	require.NoError(t, err)
	require.True(t, active)
	time.Sleep(5 * time.Millisecond) // the order is by the time saved, which has milliseconds
	active, err = s.ToggleSaved(alice, byTitle["new"], "favourite")
	require.NoError(t, err)
	require.True(t, active)
	active, err = s.ToggleSaved(alice, byTitle["new"], "read_later")
	require.NoError(t, err)
	require.True(t, active)

	state, err := s.SavedState(alice, []string{byTitle["old"], byTitle["new"], byTitle["today"]})
	require.NoError(t, err)
	require.Equal(t, store.SavedFlags{Favourite: true}, state[byTitle["old"]])
	require.Equal(t, store.SavedFlags{Favourite: true, ReadLater: true}, state[byTitle["new"]])
	require.NotContains(t, state, byTitle["today"])

	// the last saved first, with their feed
	favourites, more, err := s.SavedArticles(alice, "favourite", 10, 0)
	require.NoError(t, err)
	require.False(t, more)
	require.Equal(t, []string{"new", "old"}, []string{favourites[0].Title, favourites[1].Title})
	require.Equal(t, feed.ID, favourites[0].Feed.ID)
	require.Equal(t, "Blog", favourites[0].Feed.Title)

	page, more, err := s.SavedArticles(alice, "favourite", 1, 0)
	require.NoError(t, err)
	require.True(t, more)
	require.Len(t, page, 1)

	readLater, _, err := s.SavedArticles(alice, "read_later", 10, 0)
	require.NoError(t, err)
	require.Len(t, readLater, 1)

	// taking it back
	active, err = s.ToggleSaved(alice, byTitle["old"], "favourite")
	require.NoError(t, err)
	require.False(t, active)
	favourites, _, err = s.SavedArticles(alice, "favourite", 10, 0)
	require.NoError(t, err)
	require.Len(t, favourites, 1)

	// other users, unknown kinds and unknown articles
	_, err = s.ToggleSaved(bob, byTitle["new"], "favourite")
	require.ErrorIs(t, err, store.ErrNotFound, "the article of another user")
	_, err = s.ToggleSaved(alice, "nope", "favourite")
	require.ErrorIs(t, err, store.ErrNotFound)
	var invalid store.InvalidError
	_, err = s.ToggleSaved(alice, byTitle["new"], "starred")
	require.ErrorAs(t, err, &invalid)
	_, _, err = s.SavedArticles(alice, "starred", 10, 0)
	require.ErrorAs(t, err, &invalid)
	bobFavourites, _, err := s.SavedArticles(bob, "favourite", 10, 0)
	require.NoError(t, err)
	require.Empty(t, bobFavourites)

	// deleting the feed takes its saved articles with it
	require.NoError(t, s.DeleteFeed(alice, feed.ID))
	favourites, _, err = s.SavedArticles(alice, "favourite", 10, 0)
	require.NoError(t, err)
	require.Empty(t, favourites)
}

func TestStatsAndTheDefaultRefreshInterval(t *testing.T) {
	s, alice, bob := newStore(t)

	require.NoError(t, s.CreateFolder(alice, "Tech"))
	tree, err := s.Tree(alice)
	require.NoError(t, err)

	a, err := s.AddFeed(alice, "https://a.example.com/feed", tree[0].ID, feedfetch.Meta{Title: "Alpha", Type: "rss", IconURL: "https://a.example.com/i.png"})
	require.NoError(t, err)
	b, err := s.AddFeed(alice, "https://b.example.com/feed", "", feedfetch.Meta{Title: "beta", Type: "rss"})
	require.NoError(t, err)
	bobsFeed, err := s.AddFeed(bob, "https://c.example.com/feed", "", feedfetch.Meta{Title: "Bobs", Type: "rss"})
	require.NoError(t, err)
	_, err = s.SaveFetch(bobsFeed.ID, feedfetch.Result{Meta: feedfetch.Meta{Type: "rss"}})
	require.NoError(t, err)

	_, err = s.SaveFetch(a.ID, feedfetch.Result{Meta: feedfetch.Meta{Type: "rss"}, Items: []feedfetch.Item{
		{GUID: "1", Title: "1", ImageURL: "https://img.example.com/1.png"}, {GUID: "2", Title: "2"}, {GUID: "3", Title: "3", ImageURL: "https://img.example.com/3.png"},
	}})
	require.NoError(t, err)
	require.NoError(t, s.SaveFetchError(b.ID, errors.New("boom")))
	require.NoError(t, s.UpdateFeedSettings(alice, b.ID, store.FeedSettings{RefreshMinutes: 20, HideFromHome: true, Paused: true}))

	articles, _, err := s.Articles(alice, a.ID, 10, 0)
	require.NoError(t, err)
	_, err = s.ToggleSaved(alice, articles[0].ID, "favourite")
	require.NoError(t, err)
	_, err = s.ToggleSaved(alice, articles[1].ID, "read_later")
	require.NoError(t, err)
	_, err = s.ToggleSaved(alice, articles[2].ID, "read_later")
	require.NoError(t, err)

	stats, err := s.Stats(alice, 10*time.Minute)
	require.NoError(t, err)
	require.Equal(t, 1, stats.Folders)
	require.Equal(t, 2, stats.Feeds)
	require.Equal(t, 1, stats.FeedsWithIcon)
	require.Equal(t, 3, stats.Articles)
	require.Equal(t, 2, stats.Images)
	require.Equal(t, 1, stats.Favourites)
	require.Equal(t, 2, stats.ReadLater)
	require.Zero(t, stats.DefaultRefreshMinutes)

	require.Len(t, stats.PerFeed, 2)
	alpha, beta := stats.PerFeed[0], stats.PerFeed[1]
	require.Equal(t, "Alpha", alpha.Title, "by title, not by case")
	require.Equal(t, "Tech", alpha.Folder)
	require.Equal(t, 3, alpha.Articles)
	require.Equal(t, 2, alpha.Images)
	require.Equal(t, 10, alpha.RefreshMinutes, "the default of the server")
	require.False(t, alpha.LastFetched.IsZero())
	require.Equal(t, "beta", beta.Title)
	require.Zero(t, beta.Articles)
	require.Equal(t, 20, beta.RefreshMinutes, "its own interval")
	require.True(t, beta.Paused)
	require.True(t, beta.Hidden)
	require.Equal(t, "boom", beta.LastError)

	// the interval of the user is the default of the feeds that have none
	require.NoError(t, s.SetDefaultRefreshMinutes(alice, 90))
	minutes, err := s.DefaultRefreshMinutes(alice)
	require.NoError(t, err)
	require.Equal(t, 90, minutes)
	stats, err = s.Stats(alice, 10*time.Minute)
	require.NoError(t, err)
	require.Equal(t, 90, stats.DefaultRefreshMinutes)
	require.Equal(t, 90, stats.PerFeed[0].RefreshMinutes)
	require.Equal(t, 20, stats.PerFeed[1].RefreshMinutes)

	var invalid store.InvalidError
	require.ErrorAs(t, s.SetDefaultRefreshMinutes(alice, 3), &invalid)
	require.ErrorAs(t, s.SetDefaultRefreshMinutes(alice, 5000), &invalid)
	require.NoError(t, s.SetDefaultRefreshMinutes(alice, 0))

	// the scheduler uses the interval of the user for the feeds that have none
	require.NoError(t, s.SetDefaultRefreshMinutes(alice, 120))
	require.NoError(t, s.UpdateFeedSettings(alice, b.ID, store.FeedSettings{}))
	due, err := s.DueFeeds(time.Now().Add(30*time.Minute), 10*time.Minute, 10)
	require.NoError(t, err)
	require.Equal(t, []string{bobsFeed.ID}, due, "only the feed of bob, his interval is the 10 minutes of the server, the one of alice is 2 hours")
	due, err = s.DueFeeds(time.Now().Add(125*time.Minute), 10*time.Minute, 10)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{a.ID, b.ID, bobsFeed.ID}, due, "the user of bob has no interval of their own, the one of the server is 10 minutes")

	bobStats, err := s.Stats(bob, 10*time.Minute)
	require.NoError(t, err)
	require.Equal(t, 1, bobStats.Feeds)
	require.Zero(t, bobStats.Articles)
	require.Zero(t, bobStats.DefaultRefreshMinutes)
}

func TestClearArticles(t *testing.T) {
	s, alice, bob := newStore(t)

	blog, err := s.AddFeed(alice, "https://blog.example.com/feed", "", feedfetch.Meta{Title: "Blog", Type: "rss"})
	require.NoError(t, err)
	news, err := s.AddFeed(alice, "https://news.example.com/feed", "", feedfetch.Meta{Title: "News", Type: "rss"})
	require.NoError(t, err)

	for _, f := range []string{blog.ID, news.ID} {
		_, err := s.SaveFetch(f, feedfetch.Result{Meta: feedfetch.Meta{Type: "rss"}, Items: []feedfetch.Item{{GUID: "1", Title: "one"}, {GUID: "2", Title: "two"}}})
		require.NoError(t, err)
	}
	blogArticles, _, err := s.Articles(alice, blog.ID, 10, 0)
	require.NoError(t, err)
	newsArticles, _, err := s.Articles(alice, news.ID, 10, 0)
	require.NoError(t, err)
	_, err = s.ToggleSaved(alice, blogArticles[0].ID, "favourite")
	require.NoError(t, err)
	_, err = s.ToggleSaved(alice, newsArticles[0].ID, "favourite")
	require.NoError(t, err)

	_, err = s.ClearArticles(bob, blog.ID)
	require.ErrorIs(t, err, store.ErrNotFound, "the feed of another user")

	deleted, err := s.ClearArticles(alice, blog.ID)
	require.NoError(t, err)
	require.Equal(t, 2, deleted)

	left, _, err := s.Articles(alice, blog.ID, 10, 0)
	require.NoError(t, err)
	require.Empty(t, left)
	other, _, err := s.Articles(alice, news.ID, 10, 0)
	require.NoError(t, err)
	require.Len(t, other, 2, "the articles of the other feeds stay")

	favourites, _, err := s.SavedArticles(alice, "favourite", 10, 0)
	require.NoError(t, err)
	require.Len(t, favourites, 1, "what was saved of the cleared feed goes with it, the rest stays")
	require.Equal(t, news.ID, favourites[0].Feed.ID)

	_, err = s.FindFeed(alice, blog.ID)
	require.NoError(t, err, "the feed stays")

	deleted, err = s.ClearArticles(alice, blog.ID)
	require.NoError(t, err)
	require.Zero(t, deleted)

	// the articles come back with a fetch
	_, err = s.SaveFetch(blog.ID, feedfetch.Result{Meta: feedfetch.Meta{Type: "rss"}, Items: []feedfetch.Item{{GUID: "1", Title: "one"}}})
	require.NoError(t, err)
	left, _, err = s.Articles(alice, blog.ID, 10, 0)
	require.NoError(t, err)
	require.Len(t, left, 1)
}

func TestUpdateProfile(t *testing.T) {
	s, alice, bob := newStore(t)
	var invalid store.InvalidError

	// the name needs nothing, the email stays as it is
	require.NoError(t, s.UpdateProfile(alice, "  Alice A  ", "a@example.com", ""))
	require.NoError(t, s.UpdateProfile(alice, "Alice A", "A@Example.com", ""), "the same email in other case is the same email")

	// the email needs the current password
	require.ErrorAs(t, s.UpdateProfile(alice, "Alice", "new@example.com", ""), &invalid)
	require.ErrorAs(t, s.UpdateProfile(alice, "Alice", "new@example.com", "wrong-password"), &invalid)
	require.Contains(t, invalid.Message, "current password")

	require.NoError(t, s.UpdateProfile(alice, "Alice", "new@example.com", "password123"))

	// it is the email of the user now
	for _, bad := range []string{"", "not an email", "Alice <alice@example.com>", "a@", strings.Repeat("a", 300) + "@example.com"} {
		require.ErrorAs(t, s.UpdateProfile(alice, "Alice", bad, "password123"), &invalid, bad)
	}
	require.ErrorAs(t, s.UpdateProfile(alice, strings.Repeat("n", 256), "new@example.com", ""), &invalid)

	// the email of someone else
	require.ErrorAs(t, s.UpdateProfile(alice, "Alice", "b@example.com", "password123"), &invalid)
	require.Contains(t, invalid.Message, "already exists")
	require.ErrorAs(t, s.UpdateProfile(alice, "Alice", "B@EXAMPLE.COM", "password123"), &invalid, "in other case too")

	require.NoError(t, s.UpdateProfile(bob, "Bob", "b@example.com", ""))
}

func TestOPMLExportAndImport(t *testing.T) {
	s, alice, bob := newStore(t)

	require.NoError(t, s.CreateFolder(alice, "Tech"))
	tree, err := s.Tree(alice)
	require.NoError(t, err)
	_, err = s.AddFeed(alice, "https://go.dev/blog/feed.atom", tree[0].ID, feedfetch.Meta{Title: "Go Blog", Type: "atom", SiteURL: "https://go.dev/blog"})
	require.NoError(t, err)
	_, err = s.AddFeed(alice, "https://news.example.com/feed", "", feedfetch.Meta{Title: "News", Type: "rss"})
	require.NoError(t, err)

	exports, err := s.ExportFeeds(alice)
	require.NoError(t, err)
	require.Equal(t, []store.FeedExport{
		{Folder: "Tech", Title: "Go Blog", FeedURL: "https://go.dev/blog/feed.atom", SiteURL: "https://go.dev/blog", Type: "atom"},
		{Title: "News", FeedURL: "https://news.example.com/feed", Type: "rss"},
	}, exports)

	// into the account of another user
	var imports []store.FeedImport
	for _, f := range exports {
		imports = append(imports, store.FeedImport{Folder: f.Folder, Title: f.Title, FeedURL: f.FeedURL, SiteURL: f.SiteURL, Type: f.Type})
	}
	imports = append(imports,
		store.FeedImport{FeedURL: "javascript:alert(1)", Title: "bad"},
		store.FeedImport{FeedURL: "https://notitle.example.com/feed", Folder: "  New folder  "},
		store.FeedImport{FeedURL: "https://go.dev/blog/feed.atom", Title: "twice in the file"},
	)

	result, err := s.ImportFeeds(bob, imports)
	require.NoError(t, err)
	require.Equal(t, store.FeedsImported{Feeds: 3, Folders: 2, Existing: 1, Invalid: 1}, result)

	stats, err := s.Stats(bob, 10*time.Minute)
	require.NoError(t, err)
	require.Equal(t, 3, stats.Feeds)
	require.Equal(t, 2, stats.Folders)
	byTitle := map[string]store.FeedStat{}
	for _, f := range stats.PerFeed {
		byTitle[f.Title] = f
	}
	require.Equal(t, "Tech", byTitle["Go Blog"].Folder)
	require.Equal(t, "New folder", byTitle["notitle.example.com"].Folder, "the title is the site when there is none")
	require.True(t, byTitle["Go Blog"].LastFetched.IsZero(), "fetched by the workers")

	// the same file again: nothing is added
	result, err = s.ImportFeeds(bob, imports)
	require.NoError(t, err)
	require.Equal(t, store.FeedsImported{Existing: 4, Invalid: 1}, result)
}

func TestChangePassword(t *testing.T) {
	s, alice, _ := newStore(t)
	var invalid store.InvalidError

	require.ErrorAs(t, s.ChangePassword(alice, "wrong", "a-new-password"), &invalid)
	require.Contains(t, invalid.Message, "current password")
	require.ErrorAs(t, s.ChangePassword(alice, "password123", "short"), &invalid)
	require.Contains(t, invalid.Message, "at least 8")
	require.ErrorAs(t, s.ChangePassword(alice, "password123", strings.Repeat("x", 80)), &invalid)
	require.ErrorAs(t, s.ChangePassword(alice, "password123", "password123"), &invalid)
	require.Contains(t, invalid.Message, "different")

	require.NoError(t, s.ChangePassword(alice, "password123", "a-new-password"))
	require.ErrorAs(t, s.ChangePassword(alice, "password123", "another-password"), &invalid, "the old password is not the current one any more")
	require.NoError(t, s.ChangePassword(alice, "a-new-password", "password123"))
}

func TestAudioProgressIsPerUserAndFinishedEpisodesAreForgotten(t *testing.T) {
	s, alice, bob := newStore(t)
	feed, err := s.AddFeed(alice, "https://pod.example.com/feed", "", feedfetch.Meta{Title: "Pod", Type: "rss"})
	require.NoError(t, err)
	_, err = s.SaveFetch(feed.ID, feedfetch.Result{Meta: feedfetch.Meta{Type: "rss"}, Items: []feedfetch.Item{{GUID: "1", Title: "1", AudioURL: "https://cdn.example.com/1.mp3"}}})
	require.NoError(t, err)
	articles, _, err := s.Articles(alice, feed.ID, 10, 0)
	require.NoError(t, err)
	require.Equal(t, "https://cdn.example.com/1.mp3", articles[0].AudioURL)
	id := articles[0].ID

	got, err := s.AudioProgress(alice, id)
	require.NoError(t, err)
	require.Zero(t, got)

	require.NoError(t, s.SaveAudioProgress(alice, id, 42))
	require.NoError(t, s.SaveAudioProgress(alice, id, 90))
	got, err = s.AudioProgress(alice, id)
	require.NoError(t, err)
	require.InDelta(t, 90, got, 0.001, "saving again replaces the position")

	got, err = s.AudioProgress(bob, id)
	require.NoError(t, err)
	require.Zero(t, got, "the position of another user")
	require.ErrorIs(t, s.SaveAudioProgress(bob, id, 5), store.ErrNotFound, "an article of another user")
	require.Error(t, s.SaveAudioProgress(alice, id, -1))

	require.NoError(t, s.SaveAudioProgress(alice, id, 0))
	got, err = s.AudioProgress(alice, id)
	require.NoError(t, err)
	require.Zero(t, got)
}

func TestSearchArticlesMatchEveryWordInAnyFieldOfTheFeedsOfTheUser(t *testing.T) {
	s, alice, bob := newStore(t)

	blog, err := s.AddFeed(alice, "https://blog.example.com/feed", "", feedfetch.Meta{Title: "Blog", Type: "rss"})
	require.NoError(t, err)
	news, err := s.AddFeed(alice, "https://news.example.com/feed", "", feedfetch.Meta{Title: "News", Type: "rss"})
	require.NoError(t, err)
	other, err := s.AddFeed(bob, "https://other.example.com/feed", "", feedfetch.Meta{Title: "Other", Type: "rss"})
	require.NoError(t, err)

	day := func(d int) time.Time { return time.Date(2024, 5, d, 12, 0, 0, 0, time.UTC) }
	save := func(feedID string, items ...feedfetch.Item) {
		_, err := s.SaveFetch(feedID, feedfetch.Result{Meta: feedfetch.Meta{Type: "rss"}, Items: items})
		require.NoError(t, err)
	}
	save(blog.ID,
		feedfetch.Item{GUID: "1", Title: "Generics in Go", PublishedAt: day(1)},
		feedfetch.Item{GUID: "2", Title: "Cooking", Content: "<p>A GO-to recipe</p>", PublishedAt: day(2)},
		feedfetch.Item{GUID: "3", Title: "Sale 100% off", Author: "Jane", PublishedAt: day(3)},
	)
	save(news.ID, feedfetch.Item{GUID: "4", Title: "Go 1.25 released", PublishedAt: day(4)})
	save(other.ID, feedfetch.Item{GUID: "5", Title: "Go of bob", PublishedAt: day(5)})

	titles := func(articles []store.FeedArticle) []string {
		out := make([]string, 0, len(articles))
		for _, a := range articles {
			out = append(out, a.Title)
		}

		return out
	}

	// any field, any case, newest first, only the feeds of the user
	found, more, err := s.SearchArticles(alice, "", "go", 10, 0)
	require.NoError(t, err)
	require.False(t, more)
	require.Equal(t, []string{"Go 1.25 released", "Cooking", "Generics in Go"}, titles(found))
	require.Equal(t, "News", found[0].Feed.Title)

	// every word has to match
	found, _, err = s.SearchArticles(alice, "", "generics GO", 10, 0)
	require.NoError(t, err)
	require.Equal(t, []string{"Generics in Go"}, titles(found))

	// the author, and a % is a letter
	found, _, err = s.SearchArticles(alice, "", "jane", 10, 0)
	require.NoError(t, err)
	require.Equal(t, []string{"Sale 100% off"}, titles(found))
	found, _, err = s.SearchArticles(alice, "", "100%", 10, 0)
	require.NoError(t, err)
	require.Equal(t, []string{"Sale 100% off"}, titles(found))
	found, _, err = s.SearchArticles(alice, "", "%", 10, 0)
	require.NoError(t, err)
	require.Equal(t, []string{"Sale 100% off"}, titles(found))

	// one feed, and the feed of someone else
	found, _, err = s.SearchArticles(alice, blog.ID, "go", 10, 0)
	require.NoError(t, err)
	require.Equal(t, []string{"Cooking", "Generics in Go"}, titles(found))
	_, _, err = s.SearchArticles(alice, other.ID, "go", 10, 0)
	require.ErrorIs(t, err, store.ErrNotFound)

	// paged, and an empty query finds nothing
	found, more, err = s.SearchArticles(alice, "", "go", 2, 0)
	require.NoError(t, err)
	require.True(t, more)
	require.Len(t, found, 2)
	found, _, err = s.SearchArticles(alice, "", "   ", 10, 0)
	require.NoError(t, err)
	require.Empty(t, found)
}

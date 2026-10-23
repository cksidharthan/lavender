package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cksidharthan/lavender/pkg/feedfetch"
	_ "github.com/cksidharthan/lavender/pkg/pocketbase/migrations"
	"github.com/cksidharthan/lavender/pkg/store"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/require"
)

func newPool(t *testing.T) (*Pool, core.App, string) {
	t.Helper()

	app, err := tests.NewTestApp()
	require.NoError(t, err)
	t.Cleanup(app.Cleanup)
	require.NoError(t, app.RunAppMigrations())
	require.NoError(t, app.ReloadCachedCollections())

	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)
	u := core.NewRecord(users)
	u.SetEmail("a@example.com")
	u.SetPassword("password123")
	require.NoError(t, app.Save(u))

	return New(app, slog.New(slog.DiscardHandler), 2, time.Hour), app, u.Id
}

func articleCount(t *testing.T, app core.App, feedID string) int {
	t.Helper()

	var n int
	require.NoError(t, app.DB().Select("count(*)").From("articles").Where(dbx.HashExp{"feed": feedID}).Row(&n))

	return n
}

func twoArticles(_ context.Context, _ string) (feedfetch.Result, error) {
	return feedfetch.Result{
		Meta:  feedfetch.Meta{Title: "Blog", SiteURL: "https://example.com", Type: "rss"},
		Items: []feedfetch.Item{{GUID: "1", Title: "One", URL: "https://example.com/1"}, {GUID: "2", Title: "Two", PublishedAt: time.Now()}},
	}, nil
}

func addFeed(t *testing.T, p *Pool, userID, url string) string {
	t.Helper()

	feed, err := p.store.AddFeed(userID, url, "", feedfetch.Meta{Title: "Blog", Type: "rss"})
	require.NoError(t, err)

	return feed.ID
}

func start(t *testing.T, p *Pool) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)
	t.Cleanup(func() { cancel(); p.Wait() })
}

func TestEnqueueFetchesAndStoresTheArticles(t *testing.T) {
	p, app, user := newPool(t)
	p.fetch = twoArticles
	p.check = time.Hour
	id := addFeed(t, p, user, "https://example.com/feed")

	// the scheduler also queues the feed, as it was never fetched, the second enqueue is a no-op
	start(t, p)
	p.Enqueue(id)

	require.Eventually(t, func() bool { return articleCount(t, app, id) == 2 }, 5*time.Second, 10*time.Millisecond)

	// fetching again does not store the articles twice
	require.Eventually(t, func() bool { return p.Enqueue(id) }, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		r, err := app.FindRecordById("feeds", id)
		return err == nil && !r.GetDateTime("last_fetched_at").IsZero()
	}, 5*time.Second, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, 2, articleCount(t, app, id))
}

func TestEnqueueTwiceFetchesOnce(t *testing.T) {
	p, _, user := newPool(t)
	id := addFeed(t, p, user, "https://example.com/feed")

	require.True(t, p.Enqueue(id))
	require.False(t, p.Enqueue(id), "the feed is already queued")
}

func TestFetchErrorIsStoredAndNotRetriedUntilDue(t *testing.T) {
	p, app, user := newPool(t)
	var calls atomic.Int32
	p.fetch = func(context.Context, string) (feedfetch.Result, error) {
		calls.Add(1)
		return feedfetch.Result{}, errors.New("boom")
	}
	p.check = 10 * time.Millisecond
	id := addFeed(t, p, user, "https://example.com/feed")

	start(t, p)

	require.Eventually(t, func() bool {
		r, err := app.FindRecordById("feeds", id)
		return err == nil && r.GetString("last_error") == "boom"
	}, 5*time.Second, 10*time.Millisecond)

	// the interval is an hour, the scheduler checking every 10ms must not fetch the feed again
	time.Sleep(200 * time.Millisecond)
	require.EqualValues(t, 1, calls.Load())
}

func TestSchedulerRefreshesDueFeeds(t *testing.T) {
	p, app, user := newPool(t)
	var mu sync.Mutex
	fetched := map[string]int{}
	p.fetch = func(_ context.Context, url string) (feedfetch.Result, error) {
		mu.Lock()
		fetched[url]++
		mu.Unlock()

		return twoArticles(context.Background(), url)
	}
	p.check = 10 * time.Millisecond
	p.interval = 10 * time.Minute

	fresh := addFeed(t, p, user, "https://example.com/fresh")
	stale := addFeed(t, p, user, "https://example.com/stale")

	// fresh was fetched a minute ago, stale 11 minutes ago
	for id, ago := range map[string]time.Duration{fresh: time.Minute, stale: 11 * time.Minute} {
		r, err := app.FindRecordById("feeds", id)
		require.NoError(t, err)
		r.Set("last_fetched_at", time.Now().Add(-ago).UTC())
		require.NoError(t, app.Save(r))
	}

	start(t, p)

	require.Eventually(t, func() bool { return articleCount(t, app, stale) == 2 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 1, fetched["https://example.com/stale"])
	require.Zero(t, fetched["https://example.com/fresh"])
	require.Zero(t, articleCount(t, app, fresh))
}

func TestDeletedFeedIsSkipped(t *testing.T) {
	p, _, user := newPool(t)
	p.fetch = func(context.Context, string) (feedfetch.Result, error) {
		t.Error("fetch must not be called for a deleted feed")
		return feedfetch.Result{}, nil
	}
	id := addFeed(t, p, user, "https://example.com/feed")
	require.NoError(t, p.store.DeleteFeed(user, id))

	p.process(context.Background(), id, 1)
	_, err := p.store.FeedURL(id)
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestDoneIsClosedWhenTheFeedIsFetched(t *testing.T) {
	p, app, user := newPool(t)
	release := make(chan struct{})
	p.fetch = func(ctx context.Context, url string) (feedfetch.Result, error) {
		<-release
		return twoArticles(ctx, url)
	}
	p.check = time.Hour
	id := addFeed(t, p, user, "https://example.com/feed")

	closed := func(c <-chan struct{}) bool {
		select {
		case <-c:
			return true
		default:
			return false
		}
	}

	require.True(t, closed(p.Done(id)), "a feed that is not queued is done")

	p.Enqueue(id)
	done := p.Done(id)
	require.False(t, closed(done))

	start(t, p)
	close(release)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("done was not closed")
	}
	require.Equal(t, 2, articleCount(t, app, id))
}

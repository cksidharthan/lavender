package controller

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/cksidharthan/lavender/pkg/feedfetch"
	_ "github.com/cksidharthan/lavender/pkg/pocketbase/migrations"
	"github.com/cksidharthan/lavender/pkg/store"
	"github.com/go-chi/chi/v5"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/require"
)

const cookieName = "test_auth"

type fakeQueue struct{ ids []string }

// Done is closed already, the fake queue has no workers.
func (q *fakeQueue) Done(string) <-chan struct{} {
	done := make(chan struct{})
	close(done)

	return done
}

func (q *fakeQueue) Enqueue(id string) bool {
	for _, queued := range q.ids {
		if queued == id {
			return false
		}
	}
	q.ids = append(q.ids, id)

	return true
}

type env struct {
	queue  *fakeQueue
	t      *testing.T
	router chi.Router
	store  *store.Store
	users  map[string]*core.Record
}

func newEnv(t *testing.T) *env {
	t.Helper()

	app, err := tests.NewTestApp()
	require.NoError(t, err)
	t.Cleanup(app.Cleanup)
	require.NoError(t, app.RunAppMigrations())
	require.NoError(t, app.ReloadCachedCollections())

	usersCollection, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)

	users := map[string]*core.Record{}
	for _, name := range []string{"alice", "bob"} {
		u := core.NewRecord(usersCollection)
		u.SetEmail(name + "@example.com")
		u.SetPassword("password123")
		require.NoError(t, app.Save(u))
		users[name] = u
	}

	queue := &fakeQueue{}
	c := New(chi.NewRouter(), slog.New(slog.DiscardHandler), app, cookieName, queue)
	c.fetch = func(_ context.Context, rawURL string) (feedfetch.Result, error) {
		if strings.Contains(rawURL, "broken") {
			return feedfetch.Result{}, feedfetch.ErrNotAFeed
		}

		return feedfetch.Result{Meta: feedfetch.Meta{Title: "Go Blog", Type: "atom"}}, nil
	}
	c.RegisterRoutes()

	return &env{queue: queue, t: t, router: c.chiRouter, store: c.store, users: users}
}

func (e *env) post(user, path string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")

	if u, ok := e.users[user]; ok {
		token, err := u.NewAuthToken()
		require.NoError(e.t, err)
		req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	}

	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)

	return rec
}

func TestRequiresLogin(t *testing.T) {
	e := newEnv(t)

	rec := e.post("", "/folders", url.Values{"name": {"News"}})
	require.Equal(t, "/login", rec.Header().Get("HX-Redirect"))

	req := httptest.NewRequest(http.MethodPost, "/folders", nil)
	rec = httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestFolderAndFeedFlow(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.users["alice"].Id, e.users["bob"].Id

	rec := e.post("alice", "/folders", url.Values{"name": {"News"}})
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `hx-swap-oob="outerHTML"`)
	require.Contains(t, rec.Body.String(), "News")
	require.Contains(t, rec.Body.String(), "Folder created.")

	rec = e.post("alice", "/folders", url.Values{"name": {"News"}})
	require.Contains(t, rec.Body.String(), "already exists")

	tree, err := e.store.Tree(alice)
	require.NoError(t, err)
	require.Len(t, tree, 1)
	folderID := tree[0].ID

	rec = e.post("alice", "/feeds", url.Values{"url": {"https://go.dev/blog/feed.atom"}, "folder": {folderID}})
	require.Contains(t, rec.Body.String(), "Feed added")
	require.Contains(t, rec.Body.String(), "Go Blog")
	require.Len(t, e.queue.ids, 1, "the new feed is queued for the workers")

	queued := len(e.queue.ids)
	rec = e.post("alice", "/feeds", url.Values{"url": {"https://broken.example.com/feed"}})
	require.Contains(t, rec.Body.String(), "Could not add the feed")
	require.NotContains(t, rec.Body.String(), `hx-swap-oob="outerHTML"`)
	require.Len(t, e.queue.ids, queued, "a feed that failed is not queued")

	rec = e.post("alice", "/feeds", url.Values{"url": {"nope"}})
	require.Contains(t, rec.Body.String(), "Could not add the feed")

	tree, err = e.store.Tree(alice)
	require.NoError(t, err)
	feedID := tree[0].Feeds[0].ID

	// bob can not touch the records of alice
	for path, form := range map[string]url.Values{
		"/folders/rename": {"id": {folderID}, "name": {"x"}},
		"/folders/delete": {"id": {folderID}},
		"/feeds/rename":   {"id": {feedID}, "name": {"x"}},
		"/feeds/refresh":  {"id": {feedID}},
		"/feeds/delete":   {"id": {feedID}},
	} {
		rec = e.post("bob", path, form)
		require.Contains(t, rec.Body.String(), "no longer exists", path)
	}
	bobTree, err := e.store.Tree(bob)
	require.NoError(t, err)
	require.Empty(t, bobTree)

	require.Equal(t, []string{feedID}, e.queue.ids)
	e.queue.ids = nil
	rec = e.post("alice", "/feeds/refresh", url.Values{"id": {feedID}})
	require.Contains(t, rec.Body.String(), "Feed refreshed.")
	require.Equal(t, []string{feedID}, e.queue.ids)

	// a failed fetch is reported with its reason
	require.NoError(t, e.store.SaveFetchError(feedID, errors.New("boom")))
	rec = e.post("alice", "/feeds/refresh", url.Values{"id": {feedID}})
	require.Contains(t, rec.Body.String(), "boom")
	_, err = e.store.SaveFetch(feedID, feedfetch.Result{})
	require.NoError(t, err)

	e.queue.ids = nil
	rec = e.post("alice", "/feeds/refresh-all", nil)
	require.Contains(t, rec.Body.String(), "Feed refreshed.")
	require.Equal(t, []string{feedID}, e.queue.ids)
	rec = e.post("bob", "/feeds/refresh-all", nil)
	require.Contains(t, rec.Body.String(), "no feeds to refresh")

	rec = e.post("alice", "/feeds/rename", url.Values{"id": {feedID}, "name": {"The Go Blog"}})
	require.Contains(t, rec.Body.String(), "The Go Blog")

	rec = e.post("alice", "/folders/rename", url.Values{"id": {folderID}, "name": {"Tech"}})
	require.Contains(t, rec.Body.String(), "Tech")

	rec = e.post("alice", "/folders/delete", url.Values{"id": {folderID}})
	require.Contains(t, rec.Body.String(), "Folder deleted.")
	require.Contains(t, rec.Body.String(), "The Go Blog", "the feed stays")

	// saving an article, as a favourite and to read later
	_, err = e.store.SaveFetch(feedID, feedfetch.Result{Meta: feedfetch.Meta{Type: "rss"}, Items: []feedfetch.Item{{GUID: "g1", Title: "An article"}}})
	require.NoError(t, err)
	articles, _, err := e.store.Articles(alice, feedID, 10, 0)
	require.NoError(t, err)
	articleID := articles[0].ID

	rec = e.post("alice", "/articles/favourite", url.Values{"id": {articleID}})
	body := rec.Body.String()
	require.Contains(t, body, `id="save-favourite-`+articleID+`"`, "the button of the card")
	require.Contains(t, body, `id="panel-save-favourite-`+articleID+`"`, "the button of the panel")
	require.Contains(t, body, `hx-swap-oob="outerHTML"`)
	require.Contains(t, body, `aria-pressed="true"`)
	require.Contains(t, body, "Added to favourites.")

	rec = e.post("alice", "/articles/read-later", url.Values{"id": {articleID}})
	require.Contains(t, rec.Body.String(), "Added to read later.")
	state, err := e.store.SavedState(alice, []string{articleID})
	require.NoError(t, err)
	require.Equal(t, store.SavedFlags{Favourite: true, ReadLater: true}, state[articleID])

	rec = e.post("alice", "/articles/favourite", url.Values{"id": {articleID}})
	require.Contains(t, rec.Body.String(), `aria-pressed="false"`)
	require.Contains(t, rec.Body.String(), "Removed from favourites.")

	rec = e.post("bob", "/articles/favourite", url.Values{"id": {articleID}})
	require.Contains(t, rec.Body.String(), "no longer exists")
	rec = e.post("", "/articles/favourite", url.Values{"id": {articleID}})
	require.Equal(t, "/login", rec.Header().Get("HX-Redirect"))

	// the stored posts of a feed can be deleted, the feed stays
	rec = e.post("bob", "/feeds/clear", url.Values{"id": {feedID}})
	require.Contains(t, rec.Body.String(), "no longer exists")
	rec = e.post("alice", "/feeds/clear", url.Values{"id": {feedID}})
	require.Contains(t, rec.Body.String(), "Deleted 1 stored post.")
	require.Equal(t, "reload-feed-page", rec.Header().Get("HX-Trigger"))
	rec = e.post("alice", "/feeds/clear", url.Values{"id": {feedID}})
	require.Contains(t, rec.Body.String(), "There were no stored posts.")
	_, err = e.store.FindFeed(alice, feedID)
	require.NoError(t, err)

	// the settings of a feed
	rec = e.post("alice", "/feeds/settings", url.Values{"id": {feedID}, "refresh_minutes": {"30"}, "hide_from_home": {"on"}, "icon_override": {"https://cdn.example.com/i.png"}})
	require.Contains(t, rec.Body.String(), "Settings saved.")
	require.Equal(t, "reload-feed-page", rec.Header().Get("HX-Trigger"))
	require.Contains(t, rec.Body.String(), `hx-swap-oob="outerHTML"`)
	require.Contains(t, rec.Body.String(), `&#34;refresh_minutes&#34;:&#34;30&#34;`, "the dialog of the feed has its new settings")
	found, err := e.store.FindFeed(alice, feedID)
	require.NoError(t, err)
	require.Equal(t, store.FeedSettings{RefreshMinutes: 30, HideFromHome: true, IconOverride: "https://cdn.example.com/i.png", Paused: true}, found.Settings, "no auto_refresh in the form means paused")

	for _, bad := range []url.Values{
		{"id": {feedID}, "refresh_minutes": {"2"}, "auto_refresh": {"on"}},
		{"id": {feedID}, "refresh_minutes": {"abc"}, "auto_refresh": {"on"}},
		{"id": {feedID}, "icon_override": {"javascript:alert(1)"}, "auto_refresh": {"on"}},
	} {
		rec = e.post("alice", "/feeds/settings", bad)
		require.Contains(t, rec.Body.String(), "Could not save the settings")
	}
	rec = e.post("bob", "/feeds/settings", url.Values{"id": {feedID}, "auto_refresh": {"on"}})
	require.Contains(t, rec.Body.String(), "no longer exists")

	// deleting the feed that is open sends the user home
	req := httptest.NewRequest(http.MethodPost, "/feeds/delete", strings.NewReader(url.Values{"id": {feedID}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Current-URL", "http://localhost:8080/feeds/"+feedID)
	token, err := e.users["alice"].NewAuthToken()
	require.NoError(t, err)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	rec = httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	require.Equal(t, "/", rec.Header().Get("HX-Redirect"))
}

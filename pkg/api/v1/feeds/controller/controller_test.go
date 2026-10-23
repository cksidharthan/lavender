package controller

import (
	"context"
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

type env struct {
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

	c := New(chi.NewRouter(), slog.New(slog.DiscardHandler), app, cookieName)
	c.fetch = func(_ context.Context, rawURL string) (feedfetch.Meta, error) {
		if strings.Contains(rawURL, "broken") {
			return feedfetch.Meta{}, feedfetch.ErrNotAFeed
		}

		return feedfetch.Meta{Title: "Go Blog", Type: "atom"}, nil
	}
	c.RegisterRoutes()

	return &env{t: t, router: c.chiRouter, store: c.store, users: users}
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
	require.Contains(t, rec.Body.String(), "Feed added.")
	require.Contains(t, rec.Body.String(), "Go Blog")

	rec = e.post("alice", "/feeds", url.Values{"url": {"https://broken.example.com/feed"}})
	require.Contains(t, rec.Body.String(), "Could not add the feed")
	require.NotContains(t, rec.Body.String(), `hx-swap-oob="outerHTML"`)

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

	rec = e.post("alice", "/feeds/refresh", url.Values{"id": {feedID}})
	require.Contains(t, rec.Body.String(), "Feed refreshed.")

	rec = e.post("alice", "/feeds/rename", url.Values{"id": {feedID}, "name": {"The Go Blog"}})
	require.Contains(t, rec.Body.String(), "The Go Blog")

	rec = e.post("alice", "/folders/rename", url.Values{"id": {folderID}, "name": {"Tech"}})
	require.Contains(t, rec.Body.String(), "Tech")

	rec = e.post("alice", "/folders/delete", url.Values{"id": {folderID}})
	require.Contains(t, rec.Body.String(), "Folder deleted.")
	require.Contains(t, rec.Body.String(), "The Go Blog", "the feed stays")

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

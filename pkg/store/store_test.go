package store_test

import (
	"testing"

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

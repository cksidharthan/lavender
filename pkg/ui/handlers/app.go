package handlers

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/a-h/templ"
	"github.com/cksidharthan/lavender/pkg/iconcache"
	"github.com/cksidharthan/lavender/pkg/pocketbase/db"
	"github.com/cksidharthan/lavender/pkg/store"
	"github.com/cksidharthan/lavender/pkg/ui/components"
	"github.com/cksidharthan/lavender/pkg/ui/layouts"
	"github.com/cksidharthan/lavender/pkg/ui/pages"
	"github.com/cksidharthan/lavender/pkg/ui/view"
	"github.com/go-chi/chi/v5"
	"github.com/pocketbase/pocketbase/core"
)

// articlesPageSize is how many articles a page of a feed shows.
const articlesPageSize = 30

// renderApp renders the content inside the App layout. When htmx loads the page into the content area only
// the content is sent. A history restore (back button after a refresh) needs the whole page again.
func (h *AuthHandler) renderApp(w http.ResponseWriter, r *http.Request, user *core.Record, title string, content templ.Component) {
	w.Header().Add("Vary", "HX-Request, HX-History-Restore-Request")

	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-History-Restore-Request") != "true" {
		_ = content.Render(r.Context(), w)
		return
	}

	tree, err := h.store.Tree(user.Id)
	if err != nil {
		h.logger.Error("error loading the sidebar", slog.Any("error", err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)

		return
	}

	_ = layouts.App(title, view.Sidebar(user, tree)).Render(templ.WithChildren(r.Context(), content), w)
}

// appPage wraps a handler that needs a signed in user.
func (h *AuthHandler) appPage(fn func(w http.ResponseWriter, r *http.Request, user *core.Record)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := h.currentUser(r)
		if user == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		fn(w, r, user)
	}
}

// list is one of the lists of articles of all the feeds, the first four items of the sidebar.
type list struct {
	name  string // in the address of the next page
	title string
	intro string
	empty string
	// fetch returns a page of the list
	fetch func(h *AuthHandler, userID string, limit, offset int) ([]store.FeedArticle, bool, error)
}

// recentFor is how far back the articles of the today list go.
const recentFor = 24 * time.Hour

var lists = map[string]list{
	"home": {
		name:  "home",
		title: "Home",
		intro: "The newest articles of all your feeds.",
		empty: "No articles yet. They are fetched in the background, check again in a moment.",
		fetch: func(h *AuthHandler, userID string, limit, offset int) ([]store.FeedArticle, bool, error) {
			return h.store.AllArticles(userID, limit, offset)
		},
	},
	"today": {
		name:  "today",
		title: "Today",
		intro: "Articles of the last 24 hours.",
		empty: "Nothing new in the last 24 hours.",
		fetch: func(h *AuthHandler, userID string, limit, offset int) ([]store.FeedArticle, bool, error) {
			return h.store.RecentArticles(userID, time.Now().Add(-recentFor), limit, offset)
		},
	},
	"favourites": {
		name:  "favourites",
		title: "Favourites",
		intro: "The articles you marked with a star.",
		empty: "No favourites yet. Press the star on an article to keep it here.",
		fetch: func(h *AuthHandler, userID string, limit, offset int) ([]store.FeedArticle, bool, error) {
			return h.store.SavedArticles(userID, db.SavedFavourite, limit, offset)
		},
	},
	"read-later": {
		name:  "read-later",
		title: "Read later",
		intro: "The articles you saved for later.",
		empty: "Nothing saved for later. Press the bookmark on an article to keep it here.",
		fetch: func(h *AuthHandler, userID string, limit, offset int) ([]store.FeedArticle, bool, error) {
			return h.store.SavedArticles(userID, db.SavedReadLater, limit, offset)
		},
	},
}

// listPage is the page of a list of articles.
func (h *AuthHandler) listPage(l list) func(w http.ResponseWriter, r *http.Request, user *core.Record) {
	return func(w http.ResponseWriter, r *http.Request, user *core.Record) {
		page, ok := h.listItems(w, r, user, l, 0)
		if !ok {
			return
		}

		empty := l.empty
		if l.name == "home" {
			// nothing to wait for when there are no feeds
			feedIDs, err := h.store.FeedIDs(user.Id)
			if err != nil {
				h.serverError(w, "error finding feeds", err)
				return
			}
			if len(feedIDs) == 0 {
				empty = "You do not follow any feeds yet. Add one with the + in the sidebar, and its newest articles show up here."
			}
		}

		h.renderApp(w, r, user, l.title, pages.List(l.title, l.intro, empty, page))
	}
}

// listFragment answers the next page of a list, as list items to add to the list.
func (h *AuthHandler) listFragment(w http.ResponseWriter, r *http.Request, user *core.Record) {
	l, ok := lists[r.URL.Query().Get("view")]
	if !ok {
		http.NotFound(w, r)
		return
	}

	offset, err := strconv.Atoi(r.URL.Query().Get("offset"))
	if err != nil || offset < 0 {
		offset = 0
	}

	page, ok := h.listItems(w, r, user, l, offset)
	if !ok {
		return
	}

	_ = pages.ArticleItems(page).Render(r.Context(), w)
}

// listItems builds a page of a list. The error is already answered when ok is false.
func (h *AuthHandler) listItems(w http.ResponseWriter, _ *http.Request, user *core.Record, l list, offset int) (pages.ArticlePage, bool) {
	articles, hasMore, err := l.fetch(h, user.Id, articlesPageSize, offset)
	if err != nil {
		h.serverError(w, "error finding articles", err)
		return pages.ArticlePage{}, false
	}

	saved, err := h.store.SavedState(user.Id, articleIDs(articles))
	if err != nil {
		h.serverError(w, "error finding saved articles", err)
		return pages.ArticlePage{}, false
	}

	return view.ListArticles(articles, hasMore, offset, "/articles?view="+l.name, saved), true
}

func storeArticleIDs(articles []store.Article) []string {
	ids := make([]string, 0, len(articles))
	for _, a := range articles {
		ids = append(ids, a.ID)
	}

	return ids
}

func articleIDs(articles []store.FeedArticle) []string {
	ids := make([]string, 0, len(articles))
	for _, a := range articles {
		ids = append(ids, a.ID)
	}

	return ids
}

func (h *AuthHandler) serverError(w http.ResponseWriter, message string, err error) {
	h.logger.Error(message, slog.Any("error", err))
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}

// settingsPage is a tab of the settings. Each reads what it shows, so the tab of the account does not count the
// articles.
func (h *AuthHandler) settingsPage(tab string) func(w http.ResponseWriter, r *http.Request, user *core.Record) {
	return func(w http.ResponseWriter, r *http.Request, user *core.Record) {
		now := time.Now()

		if tab == pages.TabAccount {
			enabled, recoveryLeft, err := h.mfa.Status(user.Id)
			if err != nil {
				h.serverError(w, "error finding two-factor authentication", err)
				return
			}

			page := view.Settings(user, store.Stats{}, h.defaultRefresh, view.Storage{}, now)
			page.TwoFactor = pages.TwoFactorState{Enabled: enabled, RecoveryLeft: recoveryLeft}

			h.renderApp(w, r, user, "Settings", pages.SettingsAccount(page))

			return
		}

		stats, err := h.store.Stats(user.Id, h.defaultRefresh)
		if err != nil {
			h.serverError(w, "error counting what is stored", err)
			return
		}

		if tab == pages.TabFeeds {
			h.renderApp(w, r, user, "Settings", pages.SettingsFeeds(view.Settings(user, stats, h.defaultRefresh, view.Storage{}, now)))
			return
		}

		icons, iconBytes, err := h.icons.Stats()
		if err != nil {
			h.serverError(w, "error counting the icons", err)
			return
		}

		// the size of the database is nice to know, but not worth failing the page for. SQLite keeps the newest changes
		// in the log file next to the database, so it counts too.
		var databaseBytes int64
		for _, name := range []string{"data.db", "data.db-wal"} {
			if info, err := os.Stat(filepath.Join(h.dataDir, name)); err == nil {
				databaseBytes += info.Size()
			}
		}

		storage := view.Storage{Icons: icons, IconBytes: iconBytes, DatabaseBytes: databaseBytes}
		h.renderApp(w, r, user, "Settings", pages.SettingsData(view.Settings(user, stats, h.defaultRefresh, storage, now)))
	}
}

func (h *AuthHandler) feed(w http.ResponseWriter, r *http.Request, user *core.Record) {
	f, ok := h.userFeed(w, r, user)
	if !ok {
		return
	}

	articles, hasMore, err := h.store.Articles(user.Id, f.ID, articlesPageSize, 0)
	if err != nil {
		h.logger.Error("error finding articles", slog.Any("error", err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)

		return
	}

	saved, err := h.store.SavedState(user.Id, storeArticleIDs(articles))
	if err != nil {
		h.serverError(w, "error finding saved articles", err)
		return
	}

	page := view.Articles(f, articles, hasMore, 0, saved)

	h.renderApp(w, r, user, f.Title, pages.Feed(f, page))
}

// articles answers the next page of the articles of a feed, as list items to add to the list.
func (h *AuthHandler) articles(w http.ResponseWriter, r *http.Request, user *core.Record) {
	f, ok := h.userFeed(w, r, user)
	if !ok {
		return
	}

	offset, err := strconv.Atoi(r.URL.Query().Get("offset"))
	if err != nil || offset < 0 {
		offset = 0
	}

	articles, hasMore, err := h.store.Articles(user.Id, f.ID, articlesPageSize, offset)
	if err != nil {
		h.logger.Error("error finding articles", slog.Any("error", err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)

		return
	}

	saved, err := h.store.SavedState(user.Id, storeArticleIDs(articles))
	if err != nil {
		h.serverError(w, "error finding saved articles", err)
		return
	}

	page := view.Articles(f, articles, hasMore, offset, saved)

	_ = pages.ArticleItems(page).Render(r.Context(), w)
}

// userFeed finds the feed of the url for the user, the error is already answered when ok is false.
func (h *AuthHandler) userFeed(w http.ResponseWriter, r *http.Request, user *core.Record) (components.Feed, bool) {
	f, err := h.store.FindFeed(user.Id, chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return components.Feed{}, false
	}
	if err != nil {
		h.logger.Error("error finding feed", slog.Any("error", err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)

		return components.Feed{}, false
	}

	return components.Feed{ID: f.ID, Title: f.Title, IconURL: view.IconPath(f.ID, f.IconURL)}, true
}

// article answers one article of a feed, for the panel next to the list.
func (h *AuthHandler) article(w http.ResponseWriter, r *http.Request, user *core.Record) {
	f, ok := h.userFeed(w, r, user)
	if !ok {
		return
	}

	a, err := h.store.Article(user.Id, f.ID, chi.URLParam(r, "articleID"))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("error finding article", slog.Any("error", err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)

		return
	}

	saved, err := h.store.SavedState(user.Id, []string{a.ID})
	if err != nil {
		h.serverError(w, "error finding saved articles", err)
		return
	}

	detail := view.Article(a, saved[a.ID])
	detail.FeedName, detail.FeedIcon = f.Title, f.IconURL

	_ = pages.Article(detail).Render(r.Context(), w)
}

// icon serves the icon of a feed of the user from the icon cache. The version in the url changes with the icon, so
// the browser can keep it for long.
func (h *AuthHandler) icon(w http.ResponseWriter, r *http.Request) {
	user := h.currentUser(r)
	if user == nil {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}

	f, err := h.store.FindFeed(user.Id, chi.URLParam(r, "id"))
	if err != nil || f.IconURL == "" {
		h.noIcon(w, r)
		return
	}

	icon, err := h.icons.GetFirst(r.Context(), f.IconCandidates...)
	if err != nil {
		if !errors.Is(err, iconcache.ErrUnavailable) && r.Context().Err() == nil {
			h.logger.Error("error getting icon", slog.Any("error", err))
		}

		h.noIcon(w, r)

		return
	}

	header := w.Header()
	header.Set("Content-Type", icon.ContentType)
	header.Set("Cache-Control", "private, max-age=604800")
	// an icon is only ever shown in an img tag, an svg served as a page must not be able to run anything.
	header.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	header.Set("X-Content-Type-Options", "nosniff")

	http.ServeContent(w, r, "", icon.FetchedAt, bytes.NewReader(icon.Data))
}

// noIcon answers that the feed has no icon, the page shows the letter of the feed instead. The browser is told to
// ask again later, not on every page.
func (h *AuthHandler) noIcon(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
}

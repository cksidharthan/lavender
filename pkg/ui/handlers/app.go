package handlers

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/a-h/templ"
	"github.com/cksidharthan/lavender/pkg/store"
	"github.com/cksidharthan/lavender/pkg/ui/components"
	"github.com/cksidharthan/lavender/pkg/ui/layouts"
	"github.com/cksidharthan/lavender/pkg/ui/pages"
	"github.com/cksidharthan/lavender/pkg/ui/view"
	"github.com/go-chi/chi/v5"
	"github.com/pocketbase/pocketbase/core"
)

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

func (h *AuthHandler) settings(w http.ResponseWriter, r *http.Request, user *core.Record) {
	h.renderApp(w, r, user, "Settings", pages.Settings(view.DisplayName(user), user.Email()))
}

func (h *AuthHandler) feed(w http.ResponseWriter, r *http.Request, user *core.Record) {
	f, ok := h.userFeed(w, r, user)
	if !ok {
		return
	}

	h.renderApp(w, r, user, f.Title, pages.Feed(f))
}

func (h *AuthHandler) feedSettings(w http.ResponseWriter, r *http.Request, user *core.Record) {
	f, ok := h.userFeed(w, r, user)
	if !ok {
		return
	}

	h.renderApp(w, r, user, f.Title+" settings", pages.FeedSettings(f))
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

	return components.Feed{ID: f.ID, Title: f.Title}, true
}

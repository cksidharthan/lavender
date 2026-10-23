// Package controller serves the folder and feed routes under /api/v1. They are called by htmx from the
// sidebar and answer with the sidebar and a toast as out of band swaps, so the page does not reload.
package controller

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/cksidharthan/lavender/pkg/feedfetch"
	"github.com/cksidharthan/lavender/pkg/middlewares"
	"github.com/cksidharthan/lavender/pkg/store"
	"github.com/cksidharthan/lavender/pkg/ui/components"
	"github.com/cksidharthan/lavender/pkg/ui/view"
	"github.com/go-chi/chi/v5"
	"github.com/pocketbase/pocketbase/core"
)

const (
	genericError = "Something went wrong, please try again."
	notFound     = "This item no longer exists."
)

type Controller struct {
	chiRouter  chi.Router
	logger     *slog.Logger
	store      *store.Store
	pbApp      core.App
	cookieName string
	fetch      func(ctx context.Context, rawURL string) (feedfetch.Meta, error)
}

func New(chiRouter chi.Router, logger *slog.Logger, pbApp core.App, cookieName string) *Controller {
	return &Controller{
		chiRouter:  chiRouter,
		logger:     logger,
		store:      store.New(pbApp),
		pbApp:      pbApp,
		cookieName: cookieName,
		fetch:      feedfetch.Fetch,
	}
}

func (c *Controller) RegisterRoutes() {
	c.chiRouter.Group(func(r chi.Router) {
		r.Use(middlewares.Authenticated(c.pbApp, c.cookieName))

		r.Post("/folders", c.createFolder)
		r.Post("/folders/rename", c.renameFolder)
		r.Post("/folders/delete", c.deleteFolder)

		r.Post("/feeds", c.addFeed)
		r.Post("/feeds/rename", c.renameFeed)
		r.Post("/feeds/refresh", c.refreshFeed)
		r.Post("/feeds/delete", c.deleteFeed)
	})
}

func (c *Controller) createFolder(w http.ResponseWriter, r *http.Request) {
	user, ok := c.form(w, r)
	if !ok {
		return
	}

	err := c.store.CreateFolder(user.Id, r.PostFormValue("name"))
	c.done(w, r, user, err, "Folder created.", "Could not create the folder")
}

func (c *Controller) renameFolder(w http.ResponseWriter, r *http.Request) {
	user, ok := c.form(w, r)
	if !ok {
		return
	}

	err := c.store.RenameFolder(user.Id, r.PostFormValue("id"), r.PostFormValue("name"))
	c.done(w, r, user, err, "Folder renamed.", "Could not rename the folder")
}

func (c *Controller) deleteFolder(w http.ResponseWriter, r *http.Request) {
	user, ok := c.form(w, r)
	if !ok {
		return
	}

	err := c.store.DeleteFolder(user.Id, r.PostFormValue("id"))
	c.done(w, r, user, err, "Folder deleted.", "Could not delete the folder")
}

func (c *Controller) addFeed(w http.ResponseWriter, r *http.Request) {
	user, ok := c.form(w, r)
	if !ok {
		return
	}

	const errorTitle = "Could not add the feed"

	feedURL := strings.TrimSpace(r.PostFormValue("url"))

	meta, err := c.fetch(r.Context(), feedURL)
	if err != nil {
		c.logger.Info("error fetching feed", slog.String("url", feedURL), slog.Any("error", err))
		c.toast(w, r, components.ToastError, errorTitle, fetchMessage(err))

		return
	}

	_, err = c.store.AddFeed(user.Id, feedURL, r.PostFormValue("folder"), meta)
	c.done(w, r, user, err, "Feed added.", errorTitle)
}

func (c *Controller) renameFeed(w http.ResponseWriter, r *http.Request) {
	user, ok := c.form(w, r)
	if !ok {
		return
	}

	id := r.PostFormValue("id")
	err := c.store.RenameFeed(user.Id, id, r.PostFormValue("name"))
	if err == nil {
		reloadIfViewing(w, r, id)
	}

	c.done(w, r, user, err, "Feed renamed.", "Could not rename the feed")
}

func (c *Controller) refreshFeed(w http.ResponseWriter, r *http.Request) {
	user, ok := c.form(w, r)
	if !ok {
		return
	}

	const errorTitle = "Could not refresh the feed"

	id := r.PostFormValue("id")

	feedURL, err := c.store.FeedURL(user.Id, id)
	if err != nil {
		c.fail(w, r, errorTitle, err)
		return
	}

	meta, err := c.fetch(r.Context(), feedURL)
	if err != nil {
		c.logger.Info("error refreshing feed", slog.String("url", feedURL), slog.Any("error", err))
		c.toast(w, r, components.ToastError, errorTitle, fetchMessage(err))

		return
	}

	if err := c.store.MarkRefreshed(user.Id, id, meta); err != nil {
		c.fail(w, r, errorTitle, err)
		return
	}

	c.toast(w, r, components.ToastSuccess, "", "Feed refreshed.")
}

func (c *Controller) deleteFeed(w http.ResponseWriter, r *http.Request) {
	user, ok := c.form(w, r)
	if !ok {
		return
	}

	id := r.PostFormValue("id")
	err := c.store.DeleteFeed(user.Id, id)
	if err == nil && viewing(r, id) {
		// the feed that is open no longer exists, go home.
		w.Header().Set("HX-Redirect", "/")
	}

	c.done(w, r, user, err, "Feed deleted.", "Could not delete the feed")
}

// form returns the user and parses the form. The error is already answered when ok is false.
func (c *Controller) form(w http.ResponseWriter, r *http.Request) (*core.Record, bool) {
	if err := r.ParseForm(); err != nil {
		c.toast(w, r, components.ToastError, "Invalid request", "The request could not be read.")
		return nil, false
	}

	return middlewares.UserFromContext(r.Context()), true
}

// done answers a change of the sidebar: the new sidebar and a success toast, or an error toast.
func (c *Controller) done(w http.ResponseWriter, r *http.Request, user *core.Record, err error, success, errorTitle string) {
	if err != nil {
		c.fail(w, r, errorTitle, err)
		return
	}

	tree, err := c.store.Tree(user.Id)
	if err != nil {
		c.fail(w, r, errorTitle, err)
		return
	}

	props := view.Sidebar(user, tree)
	props.OOB = true

	_ = components.Sidebar(props).Render(r.Context(), w)
	c.toast(w, r, components.ToastSuccess, "", success)
}

func (c *Controller) fail(w http.ResponseWriter, r *http.Request, title string, err error) {
	var invalid store.InvalidError

	switch {
	case errors.As(err, &invalid):
		c.toast(w, r, components.ToastError, title, invalid.Message)
	case errors.Is(err, store.ErrNotFound):
		c.toast(w, r, components.ToastError, title, notFound)
	default:
		c.logger.Error(title, slog.Any("error", err))
		c.toast(w, r, components.ToastError, title, genericError)
	}
}

// toast answers 200, as htmx does not swap 4xx responses.
func (c *Controller) toast(w http.ResponseWriter, r *http.Request, variant components.ToastVariant, title, message string) {
	_ = components.ToastOOB(components.ToastProps{Variant: variant, Title: title, Message: message}).Render(r.Context(), w)
}

func fetchMessage(err error) string {
	switch {
	case errors.Is(err, feedfetch.ErrInvalidURL), errors.Is(err, feedfetch.ErrNotAFeed):
		return err.Error() + "."
	default:
		return "The feed could not be loaded, check the url."
	}
}

// viewing reports whether the page that made the request shows the feed.
func viewing(r *http.Request, feedID string) bool {
	current, err := url.Parse(r.Header.Get("HX-Current-URL"))
	if err != nil {
		return false
	}

	return current.Path == "/feeds/"+feedID || strings.HasPrefix(current.Path, "/feeds/"+feedID+"/")
}

// reloadIfViewing refreshes the page when it shows the feed, as its title is part of the page.
func reloadIfViewing(w http.ResponseWriter, r *http.Request, feedID string) {
	if viewing(r, feedID) {
		w.Header().Set("HX-Refresh", "true")
	}
}

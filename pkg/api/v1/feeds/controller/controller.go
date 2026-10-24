// Package controller serves the folder and feed routes under /api/v1. They are called by htmx from the
// sidebar and answer with the sidebar and a toast as out of band swaps, so the page does not reload.
package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cksidharthan/lavender/pkg/feedfetch"
	lavenderLogger "github.com/cksidharthan/lavender/pkg/logger"
	"github.com/cksidharthan/lavender/pkg/middlewares"
	lavenderOTEL "github.com/cksidharthan/lavender/pkg/otel"
	"github.com/cksidharthan/lavender/pkg/pocketbase/db"
	"github.com/cksidharthan/lavender/pkg/store"
	"github.com/cksidharthan/lavender/pkg/ui/components"
	"github.com/cksidharthan/lavender/pkg/ui/pages"
	"github.com/cksidharthan/lavender/pkg/ui/view"
	"github.com/go-chi/chi/v5"
	"github.com/pocketbase/pocketbase/core"
	"go.opentelemetry.io/otel/attribute"
)

const (
	genericError = "Something went wrong, please try again."
	notFound     = "This item no longer exists."
)

// Enqueuer queues a feed to have its articles fetched in the background.
type Enqueuer interface {
	Enqueue(feedID string) bool
	// Done is closed when the feed that is queued or being fetched is done.
	Done(feedID string) <-chan struct{}
}

// refreshWait is how long a refresh waits for the workers before it answers, the feed is refreshed anyway.
const refreshWait = 20 * time.Second

type Controller struct {
	chiRouter  chi.Router
	logger     *slog.Logger
	store      *store.Store
	pbApp      core.App
	cookieName string
	fetch      func(ctx context.Context, rawURL string) (feedfetch.Result, error)
	enqueuer   Enqueuer
}

func New(chiRouter chi.Router, logger *slog.Logger, pbApp core.App, cookieName string, enqueuer Enqueuer) *Controller {
	return &Controller{
		chiRouter:  chiRouter,
		logger:     logger,
		store:      store.New(pbApp),
		pbApp:      pbApp,
		cookieName: cookieName,
		fetch:      feedfetch.Fetch,
		enqueuer:   enqueuer,
	}
}

func (c *Controller) RegisterRoutes() {
	c.chiRouter.Group(func(r chi.Router) {
		r.Use(middlewares.Authenticated(c.pbApp, c.cookieName))

		r.Post("/folders", c.createFolder)
		r.Post("/folders/rename", c.renameFolder)
		r.Post("/folders/delete", c.deleteFolder)

		r.Post("/articles/favourite", c.toggleSaved(pages.SaveFavourite, db.SavedFavourite, "favourites"))
		r.Post("/articles/progress", c.audioProgress)
		r.Post("/articles/read-later", c.toggleSaved(pages.SaveReadLater, db.SavedReadLater, "read later"))

		r.Post("/feeds", c.addFeed)
		r.Post("/feeds/rename", c.renameFeed)
		r.Post("/feeds/url", c.updateFeedURL)
		r.Post("/feeds/settings", c.feedSettings)
		r.Post("/feeds/refresh", c.refreshFeed)
		r.Post("/feeds/clear", c.clearFeed)
		r.Post("/feeds/refresh-all", c.refreshAll)
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

	// the feed is read once here, to be sure the url is a feed and to name it. Its articles are fetched by the workers.
	result, err := c.fetch(r.Context(), feedURL)
	if err != nil {
		c.logger.InfoContext(r.Context(), "error fetching feed", slog.String("url", feedURL), slog.Any(lavenderLogger.ErrorKey, err))
		c.toast(w, r, components.ToastError, errorTitle, fetchMessage(err))

		return
	}

	feed, err := c.store.AddFeed(user.Id, feedURL, r.PostFormValue("folder"), result.Meta)
	if err == nil {
		c.enqueuer.Enqueue(feed.ID)

		host := hostOf(feedURL)
		lavenderOTEL.SetAttributes(r.Context(), attribute.String("feed.id", feed.ID), attribute.String("server.address", host))
		c.logger.InfoContext(r.Context(), "feed added", slog.String("feed_id", feed.ID), slog.String("server.address", host),
			slog.String("feed_type", result.Meta.Type), slog.Int("articles", len(result.Items)), slog.Bool("in_folder", r.PostFormValue("folder") != ""))
	}

	c.done(w, r, user, err, "Feed added, its articles are being fetched.", errorTitle)
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
		w.Header().Set("HX-Trigger", "reload-feed-page")
	}

	c.done(w, r, user, err, "Feed renamed.", "Could not rename the feed")
}

func (c *Controller) updateFeedURL(w http.ResponseWriter, r *http.Request) {
	user, ok := c.form(w, r)
	if !ok {
		return
	}

	id := r.PostFormValue("id")
	err := c.store.UpdateFeedURL(user.Id, id, r.PostFormValue("url"))
	if err == nil {
		reloadIfViewing(w, r, id)
		w.Header().Set("HX-Trigger", "reload-feed-page")
	}

	c.done(w, r, user, err, "Feed address updated, its articles are being fetched.", "Could not update the feed address")
}

// feedSettings saves the settings of a feed: its refresh interval, whether it is on the home page and its icon.
func (c *Controller) feedSettings(w http.ResponseWriter, r *http.Request) {
	user, ok := c.form(w, r)
	if !ok {
		return
	}

	const errorTitle = "Could not save the settings"

	settings := store.FeedSettings{
		HideFromHome: r.PostFormValue("hide_from_home") != "",
		IconOverride: r.PostFormValue("icon_override"),
		Paused:       r.PostFormValue("auto_refresh") == "",
	}

	if minutes := strings.TrimSpace(r.PostFormValue("refresh_minutes")); minutes != "" {
		value, err := strconv.Atoi(minutes)
		if err != nil {
			c.fail(w, r, errorTitle, store.InvalidError{Message: "The refresh interval must be a whole number of minutes."})
			return
		}
		settings.RefreshMinutes = value
	}

	err := c.store.UpdateFeedSettings(user.Id, r.PostFormValue("id"), settings)
	if err == nil {
		// the home page and the feed that are open show the settings (hidden feeds, icons), the page reloads them. This
		// is an event, as the form that sent the request is replaced together with the sidebar.
		w.Header().Set("HX-Trigger", "reload-feed-page")
	}

	c.done(w, r, user, err, "Settings saved.", errorTitle)
}

// clearFeed deletes the stored articles of a feed, the feed stays.
func (c *Controller) clearFeed(w http.ResponseWriter, r *http.Request) {
	user, ok := c.form(w, r)
	if !ok {
		return
	}

	deleted, err := c.store.ClearArticles(user.Id, r.PostFormValue("id"))
	if err != nil {
		c.fail(w, r, "Could not delete the posts", err)
		return
	}

	lavenderOTEL.SetAttributes(r.Context(), attribute.String("feed.id", r.PostFormValue("id")), attribute.Int64("articles.deleted", int64(deleted)))
	c.logger.InfoContext(r.Context(), "stored posts of a feed deleted", slog.String("feed_id", r.PostFormValue("id")), slog.Int("deleted", int(deleted)))

	// the page that is open shows the numbers and the posts of the feed
	w.Header().Set("HX-Trigger", "reload-feed-page")

	switch deleted {
	case 0:
		c.toast(w, r, components.ToastInfo, "", "There were no stored posts.")
	case 1:
		c.toast(w, r, components.ToastSuccess, "", "Deleted 1 stored post.")
	default:
		c.toast(w, r, components.ToastSuccess, "", fmt.Sprintf("Deleted %d stored posts.", deleted))
	}
}

// refreshFeed has the workers fetch the feed and answers when they are done, so the page can show the new articles.
func (c *Controller) refreshFeed(w http.ResponseWriter, r *http.Request) {
	user, ok := c.form(w, r)
	if !ok {
		return
	}

	const errorTitle = "Could not refresh the feed"

	feed, err := c.store.FindFeed(user.Id, r.PostFormValue("id"))
	if err != nil {
		c.fail(w, r, errorTitle, err)
		return
	}

	lavenderOTEL.SetAttributes(r.Context(), attribute.String("feed.id", feed.ID))

	queued := c.enqueuer.Enqueue(feed.ID)
	waited := time.Now()
	if !c.wait(r.Context(), feed.ID) {
		c.logger.InfoContext(r.Context(), "feed refresh takes longer than the request waits", slog.String("feed_id", feed.ID), slog.Bool("queued", queued),
			slog.Duration("waited", time.Since(waited)))
		c.toast(w, r, components.ToastInfo, "", "The feed is still being refreshed, its articles will appear shortly.")
		return
	}

	// the workers keep the reason when the fetch failed.
	feed, err = c.store.FindFeed(user.Id, feed.ID)
	if err != nil {
		c.fail(w, r, errorTitle, err)
		return
	}
	if feed.LastError != "" {
		c.toast(w, r, components.ToastError, errorTitle, feed.LastError)
		return
	}

	c.toast(w, r, components.ToastSuccess, "", "Feed refreshed.")
}

// refreshAll has the workers fetch all the feeds of the user and answers when they are done.
func (c *Controller) refreshAll(w http.ResponseWriter, r *http.Request) {
	user, ok := c.form(w, r)
	if !ok {
		return
	}

	const errorTitle = "Could not refresh the feeds"

	ids, err := c.store.FeedIDs(user.Id)
	if err != nil {
		c.fail(w, r, errorTitle, err)
		return
	}
	if len(ids) == 0 {
		c.toast(w, r, components.ToastInfo, "", "There are no feeds to refresh.")
		return
	}

	queued := 0
	for _, id := range ids {
		if c.enqueuer.Enqueue(id) {
			queued++
		}
	}

	waited := time.Now()
	finished := true
	for _, id := range ids {
		finished = c.wait(r.Context(), id) && finished
	}

	lavenderOTEL.SetAttributes(r.Context(), attribute.Int("feeds.count", len(ids)), attribute.Int("feeds.queued", queued), attribute.Bool("feeds.finished", finished))
	c.logger.InfoContext(r.Context(), "all feeds refreshed", slog.Int("feeds", len(ids)), slog.Int("queued", queued), slog.Bool("finished", finished),
		slog.Duration("waited", time.Since(waited)))

	if !finished {
		c.toast(w, r, components.ToastInfo, "", "The feeds are still being refreshed, their articles will appear shortly.")
		return
	}

	message := "Feeds refreshed."
	if len(ids) == 1 {
		message = "Feed refreshed."
	}
	c.toast(w, r, components.ToastSuccess, "", message)
}

// wait waits for the workers to be done with the feed, false when it takes too long or the request is gone.
func (c *Controller) wait(ctx context.Context, feedID string) bool {
	timer := time.NewTimer(refreshWait)
	defer timer.Stop()

	select {
	case <-c.enqueuer.Done(feedID):
		return true
	case <-timer.C:
		return false
	case <-ctx.Done():
		return false
	}
}

func (c *Controller) deleteFeed(w http.ResponseWriter, r *http.Request) {
	user, ok := c.form(w, r)
	if !ok {
		return
	}

	id := r.PostFormValue("id")
	err := c.store.DeleteFeed(user.Id, id)
	if err == nil {
		lavenderOTEL.SetAttributes(r.Context(), attribute.String("feed.id", id))
		c.logger.InfoContext(r.Context(), "feed deleted", slog.String("feed_id", id))

		// the page that is open shows the feed, in its lists or its numbers
		w.Header().Set("HX-Trigger", "reload-feed-page")
		if viewing(r, id) {
			// the feed that is open no longer exists, go home.
			w.Header().Set("HX-Redirect", "/")
		}
	}

	c.done(w, r, user, err, "Feed deleted.", "Could not delete the feed")
}

// toggleSaved saves an article of the user as the kind, or takes it back when it already is. The answer has the buttons
// of the article, as they are now, to replace the ones on the page: the one on the card and the one in the panel.
func (c *Controller) toggleSaved(slug, kind, listName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := c.form(w, r)
		if !ok {
			return
		}

		id := r.PostFormValue("id")

		active, err := c.store.ToggleSaved(user.Id, id, kind)
		if err != nil {
			c.fail(w, r, "Could not save the article", err)
			return
		}

		lavenderOTEL.RecordSavedToggle(r.Context(), kind, active)
		lavenderOTEL.SetAttributes(r.Context(), attribute.String("article.id", id), attribute.Bool("article.saved", active))

		_ = pages.SaveButton(id, slug, active, false, true).Render(r.Context(), w)
		_ = pages.SaveButton(id, slug, active, true, true).Render(r.Context(), w)

		if active {
			c.toast(w, r, components.ToastSuccess, "", "Added to "+listName+".")
		} else {
			c.toast(w, r, components.ToastInfo, "", "Removed from "+listName+".")
		}
	}
}

// audioProgress stores how far the user is in the audio of an article. It is called by the player in the background,
// so it answers with no content and no toast.
func (c *Controller) audioProgress(w http.ResponseWriter, r *http.Request) {
	user, ok := c.form(w, r)
	if !ok {
		return
	}

	seconds, err := strconv.ParseFloat(r.PostFormValue("seconds"), 64)
	if err == nil {
		err = c.store.SaveAudioProgress(user.Id, r.PostFormValue("id"), seconds)
	}

	var invalid store.InvalidError
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
	case errors.As(err, &invalid) || errors.Is(err, strconv.ErrSyntax) || errors.Is(err, strconv.ErrRange):
		http.Error(w, "invalid position", http.StatusBadRequest)
	default:
		c.logError(r, "error saving audio progress", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	}
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

	c.logger.InfoContext(r.Context(), "action completed", slog.String("action", actionOf(r)))

	props := view.Sidebar(user, tree)
	props.OOB = true

	_ = components.Sidebar(props).Render(r.Context(), w)
	c.toast(w, r, components.ToastSuccess, "", success)
}

func (c *Controller) fail(w http.ResponseWriter, r *http.Request, title string, err error) {
	var invalid store.InvalidError

	switch {
	case errors.As(err, &invalid):
		c.logger.InfoContext(r.Context(), "action rejected", slog.String("action", actionOf(r)), slog.String("reason", invalid.Message))
		c.toast(w, r, components.ToastError, title, invalid.Message)
	case errors.Is(err, store.ErrNotFound):
		c.logger.InfoContext(r.Context(), "action rejected", slog.String("action", actionOf(r)), slog.String("reason", "not found"))
		c.toast(w, r, components.ToastError, title, notFound)
	default:
		c.logError(r, title, err)
		c.toast(w, r, components.ToastError, title, genericError)
	}
}

// toast answers 200, as htmx does not swap 4xx responses.
func (c *Controller) toast(w http.ResponseWriter, r *http.Request, variant components.ToastVariant, title, message string) {
	lavenderOTEL.RecordAction(r.Context(), actionOf(r), resultOf(variant))
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

// logError logs the error of a request with its trace id, and marks the trace of the request as failed.
func (c *Controller) logError(r *http.Request, message string, err error) {
	lavenderOTEL.RecordError(r.Context(), err)
	c.logger.ErrorContext(r.Context(), message, slog.Any(lavenderLogger.ErrorKey, err))
}

// actionOf is the route of the request, which names what the user did.
func actionOf(r *http.Request) string {
	if routeCtx := chi.RouteContext(r.Context()); routeCtx != nil && routeCtx.RoutePattern() != "" {
		return routeCtx.RoutePattern()
	}

	return "unknown"
}

func resultOf(variant components.ToastVariant) string {
	switch variant {
	case components.ToastError:
		return "error"
	case components.ToastSuccess:
		return "success"
	default:
		return "info"
	}
}

// hostOf is the host of a url, empty when it can not be read.
func hostOf(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil {
		return u.Hostname()
	}

	return ""
}

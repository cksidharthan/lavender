// Package controller serves the account routes under /api/v1: the settings of the user, and the export and import of
// everything the user has. They are called by htmx from the settings page, and answer with toasts and out of band swaps.
package controller

import (
	"archive/zip"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cksidharthan/lavender/pkg/backup"
	"github.com/cksidharthan/lavender/pkg/iconcache"
	"github.com/cksidharthan/lavender/pkg/mfa"
	"github.com/cksidharthan/lavender/pkg/middlewares"
	"github.com/cksidharthan/lavender/pkg/opml"
	"github.com/cksidharthan/lavender/pkg/store"
	"github.com/cksidharthan/lavender/pkg/ui/components"
	"github.com/cksidharthan/lavender/pkg/ui/pages"
	"github.com/cksidharthan/lavender/pkg/ui/view"
	"github.com/go-chi/chi/v5"
	"github.com/pocketbase/pocketbase/core"
)

const (
	// maxImportBytes is the biggest backup that is taken, and maxImportMemory how much of the upload is kept in memory,
	// the rest of it is kept in a temporary file.
	maxImportBytes  = 512 << 20
	maxImportMemory = 8 << 20
	maxOPMLUpload   = 16 << 20

	genericError = "Something went wrong, please try again."
)

type Controller struct {
	chiRouter  chi.Router
	logger     *slog.Logger
	store      *store.Store
	pbApp      core.App
	icons      *iconcache.Cache
	mfa        *mfa.Service
	cookieName string
}

func New(chiRouter chi.Router, logger *slog.Logger, pbApp core.App, cookieName string, icons *iconcache.Cache, twoFactor *mfa.Service) *Controller {
	return &Controller{chiRouter: chiRouter, logger: logger, store: store.New(pbApp), pbApp: pbApp, icons: icons, mfa: twoFactor, cookieName: cookieName}
}

func (c *Controller) RegisterRoutes() {
	c.chiRouter.Group(func(r chi.Router) {
		r.Use(middlewares.Authenticated(c.pbApp, c.cookieName))

		r.Post("/account/profile", c.saveProfile)
		r.Post("/account/password", c.changePassword)

		r.Get("/account/2fa", c.twoFactorState)
		r.Post("/account/2fa/setup", c.twoFactorSetup)
		r.Post("/account/2fa/cancel", c.twoFactorCancel)
		r.Post("/account/2fa/enable", c.twoFactorEnable)
		r.Post("/account/2fa/recovery", c.twoFactorRecovery)
		r.Post("/account/2fa/disable", c.twoFactorDisable)
		r.Post("/account/settings", c.saveSettings)
		r.Get("/account/export", c.export)
		r.Post("/account/import", c.importBackup)
		r.Get("/account/opml/export", c.exportOPML)
		r.Post("/account/opml/import", c.importOPML)
	})
}

// saveProfile changes the name and the email of the user. The email also needs the current password.
func (c *Controller) saveProfile(w http.ResponseWriter, r *http.Request) {
	user := middlewares.UserFromContext(r.Context())
	if err := r.ParseForm(); err != nil {
		c.toast(w, r, components.ToastError, "Invalid request", "The request could not be read.")
		return
	}

	const errorTitle = "Could not save the profile"

	oldEmail := user.Email()

	if err := c.store.UpdateProfile(user.Id, r.PostFormValue("name"), r.PostFormValue("email"), r.PostFormValue("current_password")); err != nil {
		c.fail(w, r, errorTitle, err)
		return
	}

	// PocketBase ends the sessions of an account when its email changes, so the user is signed in again here, with a
	// new session, instead of being sent to the login page by the next click.
	if updated, err := c.pbApp.FindRecordById("users", user.Id); err == nil && updated.Email() != oldEmail {
		if err := c.startSession(w, r, updated); err != nil {
			c.logger.Error("error starting a new session", slog.Any("error", err))
		}
	}

	// the sidebar has the name, the page the name and the email
	c.answerWithSidebar(w, r, user.Id, components.ToastSuccess, "", "Profile saved.")
}

// changePassword sets a new password. PocketBase ends all the sessions of the account then, the one that asked for the
// change is replaced by a new one, the others are signed out, which is what a password change is for.
func (c *Controller) changePassword(w http.ResponseWriter, r *http.Request) {
	user := middlewares.UserFromContext(r.Context())
	if err := r.ParseForm(); err != nil {
		c.toast(w, r, components.ToastError, "Invalid request", "The request could not be read.")
		return
	}

	const errorTitle = "Could not change the password"

	newPassword := r.PostFormValue("new_password")
	if newPassword != r.PostFormValue("confirm_password") {
		c.toast(w, r, components.ToastError, errorTitle, "The new password and its confirmation are not the same.")
		return
	}

	if err := c.store.ChangePassword(user.Id, r.PostFormValue("current_password"), newPassword); err != nil {
		c.fail(w, r, errorTitle, err)
		return
	}

	if updated, err := c.pbApp.FindRecordById("users", user.Id); err == nil {
		if err := c.startSession(w, r, updated); err != nil {
			c.logger.Error("error starting a new session", slog.Any("error", err))
		}
	}

	// the page is shown again, with empty fields
	w.Header().Set("HX-Trigger", "reload-feed-page")
	c.toast(w, r, components.ToastSuccess, "", "Password changed. You were signed out of your other devices.")
}

// startSession sets the cookie of a new session, which lasts until the browser is closed.
func (c *Controller) startSession(w http.ResponseWriter, r *http.Request, user *core.Record) error {
	token, err := user.NewAuthToken()
	if err != nil {
		return fmt.Errorf("error creating auth token: %w", err)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     c.cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
	})

	return nil
}

// saveSettings saves the default refresh interval of the user, for the feeds that have none of their own.
func (c *Controller) saveSettings(w http.ResponseWriter, r *http.Request) {
	user := middlewares.UserFromContext(r.Context())
	if err := r.ParseForm(); err != nil {
		c.toast(w, r, components.ToastError, "Invalid request", "The request could not be read.")
		return
	}

	const errorTitle = "Could not save the settings"

	minutes := 0
	if value := strings.TrimSpace(r.PostFormValue("default_refresh_minutes")); value != "" {
		var err error
		if minutes, err = strconv.Atoi(value); err != nil {
			c.toast(w, r, components.ToastError, errorTitle, "The refresh interval must be a whole number of minutes.")
			return
		}
	}

	if err := c.store.SetDefaultRefreshMinutes(user.Id, minutes); err != nil {
		c.fail(w, r, errorTitle, err)
		return
	}

	// the page shows the interval of each feed
	w.Header().Set("HX-Trigger", "reload-feed-page")
	c.toast(w, r, components.ToastSuccess, "", "Settings saved.")
}

// export sends the backup of the user as a zip file.
func (c *Controller) export(w http.ResponseWriter, r *http.Request) {
	user := middlewares.UserFromContext(r.Context())

	header := w.Header()
	header.Set("Content-Type", "application/zip")
	header.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="lavender-backup-%s.zip"`, time.Now().Format("2006-01-02-1504")))
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")

	// the zip is written as it is made, so a big backup is not held in memory. When it fails halfway the status is
	// sent already, and the file is cut short, which the zip format shows.
	if _, err := backup.Export(w, c.pbApp, user.Id, c.icons); err != nil {
		c.logger.Error("error exporting backup", slog.Any("error", err))
	}
}

// importBackup adds what is in an uploaded backup to the account of the user.
func (c *Controller) importBackup(w http.ResponseWriter, r *http.Request) {
	user := middlewares.UserFromContext(r.Context())

	const errorTitle = "Could not import the backup"

	r.Body = http.MaxBytesReader(w, r.Body, maxImportBytes)
	if err := r.ParseMultipartForm(maxImportMemory); err != nil {
		message := "The file could not be read."
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			message = fmt.Sprintf("The file is too big, the most that is taken is %d MB.", maxImportBytes>>20)
		}
		c.toast(w, r, components.ToastError, errorTitle, message)

		return
	}
	defer r.MultipartForm.RemoveAll() //nolint:errcheck // temporary files, nothing to do when it fails

	file, header, err := r.FormFile("file")
	if err != nil {
		c.toast(w, r, components.ToastError, errorTitle, "Choose the backup file first.")
		return
	}
	defer file.Close()

	zr, err := zip.NewReader(file, header.Size)
	if err != nil {
		c.toast(w, r, components.ToastError, errorTitle, "The file is not a zip file.")
		return
	}

	counts, err := backup.Import(zr, c.pbApp, user.Id, c.icons)
	if err != nil {
		var invalid backup.InvalidError
		if errors.As(err, &invalid) {
			c.toast(w, r, components.ToastError, errorTitle, invalid.Message)
			return
		}

		c.logger.Error("error importing backup", slog.Any("error", err))
		c.toast(w, r, components.ToastError, errorTitle, genericError)

		return
	}

	// the new feeds and folders are in the sidebar, the page shows the new numbers
	c.answerWithSidebar(w, r, user.Id, components.ToastSuccess, "Backup imported", summary(counts))
}

// exportOPML sends the feeds of the user as an OPML file, to move them to another reader.
func (c *Controller) exportOPML(w http.ResponseWriter, r *http.Request) {
	user := middlewares.UserFromContext(r.Context())

	exports, err := c.store.ExportFeeds(user.Id)
	if err != nil {
		c.logger.Error("error exporting feeds", slog.Any("error", err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)

		return
	}

	feeds := make([]opml.Feed, 0, len(exports))
	for _, f := range exports {
		feeds = append(feeds, opml.Feed{Folder: f.Folder, Title: f.Title, XMLURL: f.FeedURL, HTMLURL: f.SiteURL, Type: f.Type})
	}

	header := w.Header()
	header.Set("Content-Type", "text/x-opml; charset=utf-8")
	header.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="lavender-feeds-%s.opml"`, time.Now().Format("2006-01-02")))
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")

	if err := opml.Write(w, "Lavender feeds", feeds, time.Now()); err != nil {
		c.logger.Error("error writing OPML", slog.Any("error", err))
	}
}

// importOPML adds the feeds of an uploaded OPML file to the account of the user. The workers fetch their articles.
func (c *Controller) importOPML(w http.ResponseWriter, r *http.Request) {
	user := middlewares.UserFromContext(r.Context())

	const errorTitle = "Could not import the feeds"

	r.Body = http.MaxBytesReader(w, r.Body, maxOPMLUpload)
	if err := r.ParseMultipartForm(maxImportMemory); err != nil {
		c.toast(w, r, components.ToastError, errorTitle, "The file could not be read, or it is too big.")
		return
	}
	defer r.MultipartForm.RemoveAll() //nolint:errcheck // temporary files, nothing to do when it fails

	file, _, err := r.FormFile("file")
	if err != nil {
		c.toast(w, r, components.ToastError, errorTitle, "Choose the OPML file first.")
		return
	}
	defer file.Close()

	parsed, err := opml.Parse(file)
	if err != nil {
		c.toast(w, r, components.ToastError, errorTitle, "The file could not be imported: "+err.Error()+".")
		return
	}

	feeds := make([]store.FeedImport, 0, len(parsed))
	for _, f := range parsed {
		feeds = append(feeds, store.FeedImport{Folder: f.Folder, Title: f.Title, FeedURL: f.XMLURL, SiteURL: f.HTMLURL, Type: f.Type})
	}

	result, err := c.store.ImportFeeds(user.Id, feeds)
	if err != nil {
		c.fail(w, r, errorTitle, err)
		return
	}

	c.answerWithSidebar(w, r, user.Id, components.ToastSuccess, "Feeds imported", opmlSummary(len(parsed), result))
}

// opmlSummary tells what the import of an OPML file did.
func opmlSummary(total int, r store.FeedsImported) string {
	if total == 0 {
		return "The file has no feeds in it."
	}

	var parts []string
	switch r.Feeds {
	case 0:
	case 1:
		parts = append(parts, "Added 1 feed")
	default:
		parts = append(parts, fmt.Sprintf("Added %d feeds", r.Feeds))
	}
	if r.Folders > 0 {
		parts[len(parts)-1] += fmt.Sprintf(" and %s", plural(r.Folders, "folder"))
	}
	if r.Existing > 0 {
		parts = append(parts, fmt.Sprintf("%d already in your account", r.Existing))
	}
	if r.Invalid > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped, as their address is not an http or https one", r.Invalid))
	}
	if r.Feeds > 0 {
		parts = append(parts, "their articles are fetched in the background")
	}

	return strings.Join(parts, ", ") + "."
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}

	return fmt.Sprintf("%d %ss", n, unit)
}

// answerWithSidebar answers with the new sidebar and a toast. The page that is open is told to reload, as it shows what
// changed. The headers are set first, they are sent with the first byte of the body.
func (c *Controller) answerWithSidebar(w http.ResponseWriter, r *http.Request, userID string, variant components.ToastVariant, title, message string) {
	w.Header().Set("HX-Trigger", "reload-feed-page")

	user, err := c.pbApp.FindRecordById("users", userID)
	if err == nil {
		var tree []store.Folder
		if tree, err = c.store.Tree(userID); err == nil {
			props := view.Sidebar(user, tree)
			props.OOB = true
			_ = components.Sidebar(props).Render(r.Context(), w)
		}
	}
	if err != nil {
		c.logger.Error("error loading the sidebar", slog.Any("error", err))
	}

	c.toast(w, r, variant, title, message)
}

// summary tells what an import added.
func summary(c backup.Counts) string {
	var parts []string
	for _, item := range []struct {
		n            int
		one, several string
	}{
		{c.Feeds, "feed", "feeds"},
		{c.Folders, "folder", "folders"},
		{c.Articles, "article", "articles"},
		{c.Saved, "saved article", "saved articles"},
		{c.Icons, "icon", "icons"},
	} {
		switch item.n {
		case 0:
		case 1:
			parts = append(parts, "1 "+item.one)
		default:
			parts = append(parts, fmt.Sprintf("%d %s", item.n, item.several))
		}
	}

	if len(parts) == 0 {
		return "Nothing new, all of it is in your account already."
	}

	return "Added " + strings.Join(parts, ", ") + "."
}

func (c *Controller) fail(w http.ResponseWriter, r *http.Request, title string, err error) {
	var invalid store.InvalidError
	if errors.As(err, &invalid) {
		c.toast(w, r, components.ToastError, title, invalid.Message)
		return
	}

	c.logger.Error(title, slog.Any("error", err))
	c.toast(w, r, components.ToastError, title, genericError)
}

// toast answers 200, as htmx does not swap 4xx responses.
func (c *Controller) toast(w http.ResponseWriter, r *http.Request, variant components.ToastVariant, title, message string) {
	_ = components.ToastOOB(components.ToastProps{Variant: variant, Title: title, Message: message}).Render(r.Context(), w)
}

// The routes of two-factor authentication answer with the card of the page, which replaces itself with it. An error is a
// toast and nothing is swapped, so the card keeps what the user typed.

// twoFactorState is the card as it is now.
func (c *Controller) twoFactorState(w http.ResponseWriter, r *http.Request) {
	c.renderTwoFactor(w, r, pages.TwoFactorState{})
}

// twoFactorSetup starts the set up: a new secret, shown as a QR code.
func (c *Controller) twoFactorSetup(w http.ResponseWriter, r *http.Request) {
	user := middlewares.UserFromContext(r.Context())

	setup, err := c.mfa.StartSetup(user.Id)
	if err != nil {
		c.twoFactorFail(w, r, "Could not set up two-factor authentication", err)
		return
	}

	c.renderTwoFactor(w, r, pages.TwoFactorState{Setup: &pages.TwoFactorSetup{QR: setup.QR, Secret: setup.Secret}})
}

// twoFactorCancel throws away the secret of a set up that was not finished.
func (c *Controller) twoFactorCancel(w http.ResponseWriter, r *http.Request) {
	user := middlewares.UserFromContext(r.Context())

	if err := c.mfa.CancelSetup(user.Id); err != nil {
		c.twoFactorFail(w, r, "Could not cancel the set up", err)
		return
	}

	c.renderTwoFactor(w, r, pages.TwoFactorState{})
}

// twoFactorEnable turns it on, when the code of the app is right, and shows the recovery codes.
func (c *Controller) twoFactorEnable(w http.ResponseWriter, r *http.Request) {
	user := middlewares.UserFromContext(r.Context())
	if err := r.ParseForm(); err != nil {
		c.twoFactorFail(w, r, "Invalid request", errors.New("the request could not be read"))
		return
	}

	codes, err := c.mfa.Enable(user.Id, r.PostFormValue("code"))
	if err != nil {
		c.twoFactorFail(w, r, "Could not turn on two-factor authentication", err)
		return
	}

	c.renderTwoFactor(w, r, pages.TwoFactorState{Codes: codes})
	c.toast(w, r, components.ToastSuccess, "", "Two-factor authentication is on.")
}

// twoFactorRecovery makes new recovery codes.
func (c *Controller) twoFactorRecovery(w http.ResponseWriter, r *http.Request) {
	user := middlewares.UserFromContext(r.Context())
	if err := r.ParseForm(); err != nil {
		c.twoFactorFail(w, r, "Invalid request", errors.New("the request could not be read"))
		return
	}

	codes, err := c.mfa.RegenerateRecoveryCodes(user.Id, r.PostFormValue("current_password"), r.PostFormValue("code"))
	if err != nil {
		c.twoFactorFail(w, r, "Could not make new recovery codes", err)
		return
	}

	c.renderTwoFactor(w, r, pages.TwoFactorState{Codes: codes})
}

// twoFactorDisable turns it off.
func (c *Controller) twoFactorDisable(w http.ResponseWriter, r *http.Request) {
	user := middlewares.UserFromContext(r.Context())
	if err := r.ParseForm(); err != nil {
		c.twoFactorFail(w, r, "Invalid request", errors.New("the request could not be read"))
		return
	}

	if err := c.mfa.Disable(user.Id, r.PostFormValue("current_password"), r.PostFormValue("code")); err != nil {
		c.twoFactorFail(w, r, "Could not turn off two-factor authentication", err)
		return
	}

	c.renderTwoFactor(w, r, pages.TwoFactorState{})
	c.toast(w, r, components.ToastInfo, "", "Two-factor authentication is off.")
}

// renderTwoFactor answers with the card. Whether it is on, and the recovery codes that are left, are read from the
// user, the rest is of the state that was given.
func (c *Controller) renderTwoFactor(w http.ResponseWriter, r *http.Request, state pages.TwoFactorState) {
	user := middlewares.UserFromContext(r.Context())

	enabled, left, err := c.mfa.Status(user.Id)
	if err != nil {
		c.logger.Error("error finding two-factor authentication", slog.Any("error", err))
		c.toast(w, r, components.ToastError, "Something went wrong", genericError)

		return
	}
	state.Enabled, state.RecoveryLeft = enabled, left

	// the codes are never cached
	w.Header().Set("Cache-Control", "no-store")
	_ = pages.TwoFactor(state).Render(r.Context(), w)
}

// twoFactorFail answers with a toast, and tells htmx not to swap the card.
func (c *Controller) twoFactorFail(w http.ResponseWriter, r *http.Request, title string, err error) {
	var invalid mfa.InvalidError

	message := genericError
	switch {
	case errors.As(err, &invalid):
		message = invalid.Message
	case errors.Is(err, mfa.ErrInvalid):
		message = "That code is not right."
	case errors.Is(err, mfa.ErrLocked):
		message = "Too many wrong codes. Try again in a few minutes."
	default:
		c.logger.Error(title, slog.Any("error", err))
	}

	w.Header().Set("HX-Reswap", "none")
	c.toast(w, r, components.ToastError, title, message)
}

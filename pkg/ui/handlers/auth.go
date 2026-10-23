package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/cksidharthan/lavender/pkg/iconcache"
	lavenderLogger "github.com/cksidharthan/lavender/pkg/logger"
	"github.com/cksidharthan/lavender/pkg/mfa"
	"github.com/cksidharthan/lavender/pkg/middlewares"
	lavenderOTEL "github.com/cksidharthan/lavender/pkg/otel"
	"github.com/cksidharthan/lavender/pkg/store"
	"github.com/cksidharthan/lavender/pkg/ui/components"
	"github.com/cksidharthan/lavender/pkg/ui/pages"
	"github.com/cksidharthan/lavender/pkg/ui/static"
	"github.com/go-chi/chi/v5"
	"github.com/pocketbase/pocketbase/core"
)

const (
	usersCollection   = "users"
	AuthCookieName    = "lavender_auth"
	rememberMeMaxAge  = 30 * 24 * time.Hour
	minPasswordLength = 8

	loginErrorTitle    = "Login error"
	registerErrorTitle = "Registration error"
)

type AuthHandler struct {
	chiRouter chi.Router
	logger    *slog.Logger
	pbApp     core.App
	store     *store.Store
	icons     *iconcache.Cache
	mfa       *mfa.Service
	// defaultRefresh is the default refresh interval of the application, dataDir the directory of the database
	defaultRefresh time.Duration
	dataDir        string
}

// Options are what the handlers show of the application.
type Options struct {
	Icons *iconcache.Cache
	MFA   *mfa.Service
	// DefaultRefresh is the default refresh interval of the feeds, DataDir the directory of the database.
	DefaultRefresh time.Duration
	DataDir        string
}

func NewAuthHandler(chiRouter chi.Router, logger *slog.Logger, pbApp core.App, opts Options) *AuthHandler {
	return &AuthHandler{
		chiRouter:      chiRouter,
		logger:         logger,
		pbApp:          pbApp,
		store:          store.New(pbApp),
		icons:          opts.Icons,
		mfa:            opts.MFA,
		defaultRefresh: opts.DefaultRefresh,
		dataDir:        opts.DataDir,
	}
}

func (h *AuthHandler) RegisterRoutes() {
	h.chiRouter.Handle("/static/*", http.StripPrefix("/static/", http.FileServerFS(static.FS)))
	h.chiRouter.Get("/", h.appPage(h.listPage(lists["home"])))
	h.chiRouter.Get("/today", h.appPage(h.listPage(lists["today"])))
	h.chiRouter.Get("/favourites", h.appPage(h.listPage(lists["favourites"])))
	h.chiRouter.Get("/read-later", h.appPage(h.listPage(lists["read-later"])))
	h.chiRouter.Get("/articles", h.appPage(h.listFragment))
	h.chiRouter.Get(pages.TabAccount, h.appPage(h.settingsPage(pages.TabAccount)))
	h.chiRouter.Get(pages.TabFeeds, h.appPage(h.settingsPage(pages.TabFeeds)))
	h.chiRouter.Get(pages.TabData, h.appPage(h.settingsPage(pages.TabData)))
	h.chiRouter.Get("/feeds/{id}", h.appPage(h.feed))
	h.chiRouter.Get("/icons/{id}", h.icon)
	h.chiRouter.Get("/feeds/{id}/articles", h.appPage(h.articles))
	h.chiRouter.Get("/feeds/{id}/articles/{articleID}", h.appPage(h.article))
	h.chiRouter.Method(http.MethodGet, "/login", templ.Handler(pages.Login()))
	h.chiRouter.Method(http.MethodGet, "/register", templ.Handler(pages.Register()))
	h.chiRouter.Get("/login/2fa", h.login2FAPage)
	h.chiRouter.Post("/login", h.login)
	h.chiRouter.Post("/login/2fa", h.login2FA)
	h.chiRouter.Post("/register", h.register)
	h.chiRouter.Post("/logout", h.logout)
}

func (h *AuthHandler) login(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		lavenderOTEL.RecordLogin(r.Context(), lavenderOTEL.LoginPassword, "invalid_request")
		h.toastError(w, r, loginErrorTitle, "Invalid request.")
		return
	}

	email := strings.TrimSpace(r.PostFormValue("email"))
	password := r.PostFormValue("password")
	remember := r.PostFormValue("remember") != ""

	record, err := h.pbApp.FindAuthRecordByEmail(usersCollection, email)
	// the same message for unknown email and wrong password, so accounts can't be enumerated.
	if err != nil || !h.validPassword(r.Context(), record, password) {
		lavenderOTEL.RecordLogin(r.Context(), lavenderOTEL.LoginPassword, "invalid_credentials")
		// no email in the log, it could be a password typed in the wrong field. The request log has the client.
		h.logger.WarnContext(r.Context(), "login failed")
		h.toastError(w, r, loginErrorTitle, "Invalid email or password.")
		return
	}

	middlewares.NoteUser(r.Context(), record.Id)

	// the second step, the session starts when the code is right
	if mfa.Enabled(record) {
		lavenderOTEL.RecordLogin(r.Context(), lavenderOTEL.LoginPassword, "two_factor_required")
		h.setChallengeCookie(w, r, h.mfa.NewChallenge(record.Id, remember), int(mfa.ChallengeTTL.Seconds()))
		htmxRedirect(w, "/login/2fa")

		return
	}

	if err := h.startSession(w, r, record, remember); err != nil {
		lavenderOTEL.RecordLogin(r.Context(), lavenderOTEL.LoginPassword, "error")
		h.logError(r, "error starting session", err)
		h.toastError(w, r, loginErrorTitle, "Something went wrong, please try again.")
		return
	}

	lavenderOTEL.RecordLogin(r.Context(), lavenderOTEL.LoginPassword, lavenderOTEL.ResultSuccess)
	h.logger.InfoContext(r.Context(), "user logged in", slog.String(lavenderLogger.UserIDKey, record.Id), slog.Bool("remember", remember))
	htmxRedirect(w, "/")
}

// validPassword checks the password of the user. Hashing is the slowest step of a login, so it has a span and a
// metric of its own.
func (h *AuthHandler) validPassword(ctx context.Context, record *core.Record, password string) bool {
	_, span := lavenderOTEL.StartNewSpanWithName(ctx, "auth.validate_password")
	defer span.End()

	started := time.Now()
	valid := record.ValidatePassword(password)

	result := "invalid"
	if valid {
		result = "valid"
	}
	lavenderOTEL.RecordPasswordValidation(ctx, result, time.Since(started))

	return valid
}

const (
	twoFactorErrorTitle = "Verification error"
	challengeCookieName = "lavender_mfa"
	// the cookie of the second step is only sent to the pages of the sign in
	challengeCookiePath = "/login"
)

// login2FAPage shows the second step of signing in, when the password was right.
func (h *AuthHandler) login2FAPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.challenge(r); !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	_ = pages.Login2FA().Render(r.Context(), w)
}

// login2FA checks the code of the second step, and starts the session when it is right.
func (h *AuthHandler) login2FA(w http.ResponseWriter, r *http.Request) {
	challenge, ok := h.challenge(r)
	if !ok {
		// the password was too long ago, it has to be entered again
		lavenderOTEL.RecordTwoFactor(r.Context(), "expired", "unknown")
		h.setChallengeCookie(w, r, "", -1)
		htmxRedirect(w, "/login")

		return
	}

	if err := r.ParseForm(); err != nil {
		lavenderOTEL.RecordTwoFactor(r.Context(), "invalid_request", "unknown")
		h.toastError(w, r, twoFactorErrorTitle, "Invalid request.")
		return
	}

	middlewares.NoteUser(r.Context(), challenge.UserID)

	usedRecovery, err := h.mfa.VerifyLogin(challenge.UserID, r.PostFormValue("code"))
	switch {
	case errors.Is(err, mfa.ErrLocked):
		lavenderOTEL.RecordTwoFactor(r.Context(), "locked", "unknown")
		h.logger.WarnContext(r.Context(), "two-factor authentication locked after too many wrong codes", slog.String(lavenderLogger.UserIDKey, challenge.UserID))
		h.setChallengeCookie(w, r, "", -1)
		h.toastError(w, r, twoFactorErrorTitle, "Too many wrong codes. Go back to the login and try again in a few minutes.")

		return
	case errors.Is(err, mfa.ErrInvalid):
		lavenderOTEL.RecordTwoFactor(r.Context(), "invalid", "unknown")
		h.toastError(w, r, twoFactorErrorTitle, "That code is not right.")
		return
	case err != nil:
		lavenderOTEL.RecordTwoFactor(r.Context(), "error", "unknown")
		h.logError(r, "error verifying the code", err)
		h.toastError(w, r, twoFactorErrorTitle, "Something went wrong, please try again.")

		return
	}

	record, err := h.pbApp.FindRecordById(usersCollection, challenge.UserID)
	if err == nil {
		err = h.startSession(w, r, record, challenge.Remember)
	}
	if err != nil {
		lavenderOTEL.RecordTwoFactor(r.Context(), "error", "unknown")
		h.logError(r, "error starting session", err)
		h.toastError(w, r, twoFactorErrorTitle, "Something went wrong, please try again.")

		return
	}

	kind := "app"
	if usedRecovery {
		kind = "recovery"
		lavenderOTEL.RecordRecoveryCodeUsed(r.Context())
	}
	lavenderOTEL.RecordTwoFactor(r.Context(), lavenderOTEL.ResultSuccess, kind)
	lavenderOTEL.RecordLogin(r.Context(), lavenderOTEL.LoginTwoFactor, lavenderOTEL.ResultSuccess)
	h.logger.InfoContext(r.Context(), "user logged in", slog.String(lavenderLogger.UserIDKey, record.Id), slog.Bool("remember", challenge.Remember),
		slog.Bool("two_factor", true), slog.Bool("recovery_code", usedRecovery))

	h.setChallengeCookie(w, r, "", -1)
	htmxRedirect(w, "/")
}

// challenge reads the proof of the password that comes with the second step.
func (h *AuthHandler) challenge(r *http.Request) (mfa.Challenge, bool) {
	cookie, err := r.Cookie(challengeCookieName)
	if err != nil {
		return mfa.Challenge{}, false
	}

	return h.mfa.ParseChallenge(cookie.Value)
}

// setChallengeCookie sets the cookie of the second step, maxAge -1 removes it.
func (h *AuthHandler) setChallengeCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     challengeCookieName,
		Value:    value,
		Path:     challengeCookiePath,
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
	})
}

func (h *AuthHandler) register(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		lavenderOTEL.RecordRegistration(r.Context(), "invalid_request")
		h.toastError(w, r, registerErrorTitle, "Invalid request.")
		return
	}

	name := strings.TrimSpace(r.PostFormValue("name"))
	email := strings.TrimSpace(r.PostFormValue("email"))
	password := r.PostFormValue("password")

	if _, err := mail.ParseAddress(email); err != nil {
		lavenderOTEL.RecordRegistration(r.Context(), "invalid_email")
		h.toastError(w, r, registerErrorTitle, "Please enter a valid email address.")
		return
	}
	if len(password) < minPasswordLength {
		lavenderOTEL.RecordRegistration(r.Context(), "weak_password")
		h.toastError(w, r, registerErrorTitle, fmt.Sprintf("Password must be at least %d characters.", minPasswordLength))
		return
	}
	if _, err := h.pbApp.FindAuthRecordByEmail(usersCollection, email); err == nil {
		lavenderOTEL.RecordRegistration(r.Context(), "email_taken")
		h.toastError(w, r, registerErrorTitle, "An account with this email already exists.")
		return
	}

	collection, err := h.pbApp.FindCollectionByNameOrId(usersCollection)
	if err != nil {
		lavenderOTEL.RecordRegistration(r.Context(), "error")
		h.logError(r, "error finding users collection", err)
		h.toastError(w, r, registerErrorTitle, "Something went wrong, please try again.")
		return
	}

	record := core.NewRecord(collection)
	record.SetEmail(email)
	record.SetPassword(password)
	record.Set("name", name)

	if err := h.pbApp.Save(record); err != nil {
		lavenderOTEL.RecordRegistration(r.Context(), "error")
		h.logError(r, "error creating user", err)
		h.toastError(w, r, registerErrorTitle, "Could not create the account, please check your details.")
		return
	}

	// the account exists from here on, even when the session could not be started
	lavenderOTEL.RecordRegistration(r.Context(), lavenderOTEL.ResultSuccess)
	middlewares.NoteUser(r.Context(), record.Id)
	h.logger.InfoContext(r.Context(), "user registered", slog.String(lavenderLogger.UserIDKey, record.Id))

	if err := h.startSession(w, r, record, false); err != nil {
		h.logError(r, "error starting session", err)
		htmxRedirect(w, "/login")
		return
	}

	htmxRedirect(w, "/")
}

func (h *AuthHandler) logout(w http.ResponseWriter, r *http.Request) {
	lavenderOTEL.RecordLogout(r.Context())
	if user := h.currentUser(r); user != nil {
		h.logger.InfoContext(r.Context(), "user logged out", slog.String(lavenderLogger.UserIDKey, user.Id))
	}

	http.SetCookie(w, &http.Cookie{Name: AuthCookieName, Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	htmxRedirect(w, "/login")
}

// currentUser resolves the signed in user from the auth cookie, nil when there is none.
func (h *AuthHandler) currentUser(r *http.Request) *core.Record {
	cookie, err := r.Cookie(AuthCookieName)
	if err != nil {
		return nil
	}

	record, err := h.pbApp.FindAuthRecordByToken(cookie.Value, core.TokenTypeAuth)
	if err != nil {
		return nil
	}

	middlewares.NoteUser(r.Context(), record.Id)

	return record
}

// startSession issues a pocketbase auth token and stores it in an http only cookie.
// Without remember me the cookie is a session cookie.
func (h *AuthHandler) startSession(w http.ResponseWriter, r *http.Request, record *core.Record, remember bool) error {
	token, err := record.NewAuthToken()
	if err != nil {
		return fmt.Errorf("error creating auth token: %w", err)
	}

	cookie := &http.Cookie{
		Name:     AuthCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
	}
	if remember {
		cookie.MaxAge = int(rememberMeMaxAge.Seconds())
	}
	http.SetCookie(w, cookie)

	return nil
}

// toastError sends an error toast to the toast container through an htmx out of band swap. It answers
// 200, as htmx does not swap 4xx responses.
func (h *AuthHandler) toastError(w http.ResponseWriter, r *http.Request, title, msg string) {
	_ = components.ToastOOB(components.ToastProps{Variant: components.ToastError, Title: title, Message: msg}).Render(r.Context(), w)
}

func htmxRedirect(w http.ResponseWriter, to string) {
	w.Header().Set("HX-Redirect", to)
	w.WriteHeader(http.StatusOK)
}

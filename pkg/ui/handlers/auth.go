package handlers

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/cksidharthan/lavender/pkg/ui/components"
	"github.com/cksidharthan/lavender/pkg/ui/pages"
	"github.com/cksidharthan/lavender/pkg/ui/static"
	"github.com/go-chi/chi/v5"
	"github.com/pocketbase/pocketbase/core"
)

const (
	usersCollection   = "users"
	authCookieName    = "lavender_auth"
	rememberMeMaxAge  = 30 * 24 * time.Hour
	minPasswordLength = 8

	loginErrorTitle    = "Login error"
	registerErrorTitle = "Registration error"
)

type AuthHandler struct {
	chiRouter chi.Router
	logger    *slog.Logger
	pbApp     core.App
}

func NewAuthHandler(chiRouter chi.Router, logger *slog.Logger, pbApp core.App) *AuthHandler {
	return &AuthHandler{chiRouter: chiRouter, logger: logger, pbApp: pbApp}
}

func (h *AuthHandler) RegisterRoutes() {
	h.chiRouter.Handle("/static/*", http.StripPrefix("/static/", http.FileServerFS(static.FS)))
	h.chiRouter.Get("/", h.home)
	h.chiRouter.Method(http.MethodGet, "/login", templ.Handler(pages.Login()))
	h.chiRouter.Method(http.MethodGet, "/register", templ.Handler(pages.Register()))
	h.chiRouter.Post("/login", h.login)
	h.chiRouter.Post("/register", h.register)
	h.chiRouter.Post("/logout", h.logout)
}

func (h *AuthHandler) home(w http.ResponseWriter, r *http.Request) {
	record := h.currentUser(r)
	if record == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	_ = pages.Home(record.Email()).Render(r.Context(), w)
}

func (h *AuthHandler) login(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.toastError(w, r, loginErrorTitle, "Invalid request.")
		return
	}

	email := strings.TrimSpace(r.PostFormValue("email"))
	password := r.PostFormValue("password")
	remember := r.PostFormValue("remember") != ""

	record, err := h.pbApp.FindAuthRecordByEmail(usersCollection, email)
	// the same message for unknown email and wrong password, so accounts can't be enumerated.
	if err != nil || !record.ValidatePassword(password) {
		h.toastError(w, r, loginErrorTitle, "Invalid email or password.")
		return
	}

	if err := h.startSession(w, r, record, remember); err != nil {
		h.logger.Error("error starting session", slog.Any("error", err))
		h.toastError(w, r, loginErrorTitle, "Something went wrong, please try again.")
		return
	}

	htmxRedirect(w, "/")
}

func (h *AuthHandler) register(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.toastError(w, r, registerErrorTitle, "Invalid request.")
		return
	}

	name := strings.TrimSpace(r.PostFormValue("name"))
	email := strings.TrimSpace(r.PostFormValue("email"))
	password := r.PostFormValue("password")

	if _, err := mail.ParseAddress(email); err != nil {
		h.toastError(w, r, registerErrorTitle, "Please enter a valid email address.")
		return
	}
	if len(password) < minPasswordLength {
		h.toastError(w, r, registerErrorTitle, fmt.Sprintf("Password must be at least %d characters.", minPasswordLength))
		return
	}
	if _, err := h.pbApp.FindAuthRecordByEmail(usersCollection, email); err == nil {
		h.toastError(w, r, registerErrorTitle, "An account with this email already exists.")
		return
	}

	collection, err := h.pbApp.FindCollectionByNameOrId(usersCollection)
	if err != nil {
		h.logger.Error("error finding users collection", slog.Any("error", err))
		h.toastError(w, r, registerErrorTitle, "Something went wrong, please try again.")
		return
	}

	record := core.NewRecord(collection)
	record.SetEmail(email)
	record.SetPassword(password)
	record.Set("name", name)

	if err := h.pbApp.Save(record); err != nil {
		h.logger.Error("error creating user", slog.Any("error", err))
		h.toastError(w, r, registerErrorTitle, "Could not create the account, please check your details.")
		return
	}

	if err := h.startSession(w, r, record, false); err != nil {
		h.logger.Error("error starting session", slog.Any("error", err))
		htmxRedirect(w, "/login")
		return
	}

	htmxRedirect(w, "/")
}

func (h *AuthHandler) logout(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: authCookieName, Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	htmxRedirect(w, "/login")
}

// currentUser resolves the signed in user from the auth cookie, nil when there is none.
func (h *AuthHandler) currentUser(r *http.Request) *core.Record {
	cookie, err := r.Cookie(authCookieName)
	if err != nil {
		return nil
	}

	record, err := h.pbApp.FindAuthRecordByToken(cookie.Value, core.TokenTypeAuth)
	if err != nil {
		return nil
	}

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
		Name:     authCookieName,
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

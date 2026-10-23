package handlers

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cksidharthan/lavender/pkg/iconcache"
	"github.com/cksidharthan/lavender/pkg/mfa"
	_ "github.com/cksidharthan/lavender/pkg/pocketbase/migrations"
	"github.com/go-chi/chi/v5"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/hotp"
	"github.com/stretchr/testify/require"
)

type loginEnv struct {
	t       *testing.T
	router  chi.Router
	app     core.App
	mfa     *mfa.Service
	userID  string
	secret  string
	codes   []string
	cookies map[string]*http.Cookie
}

func newLoginEnv(t *testing.T, twoFactor bool) *loginEnv {
	t.Helper()

	app, err := tests.NewTestApp()
	require.NoError(t, err)
	t.Cleanup(app.Cleanup)
	require.NoError(t, app.RunAppMigrations())
	require.NoError(t, app.ReloadCachedCollections())

	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)
	u := core.NewRecord(users)
	u.SetEmail("alice@example.com")
	u.SetPassword("password123")
	require.NoError(t, app.Save(u))

	key, err := mfa.LoadKey("", t.TempDir())
	require.NoError(t, err)
	icons, err := iconcache.New(t.TempDir())
	require.NoError(t, err)
	service := mfa.New(app, key)

	router := chi.NewRouter()
	NewAuthHandler(router, slog.New(slog.DiscardHandler), app, Options{Icons: icons, MFA: service, DefaultRefresh: 10 * time.Minute, DataDir: t.TempDir()}).RegisterRoutes()

	env := &loginEnv{t: t, router: router, app: app, mfa: service, userID: u.Id, cookies: map[string]*http.Cookie{}}

	if twoFactor {
		setup, err := service.StartSetup(u.Id)
		require.NoError(t, err)
		env.secret = strings.ReplaceAll(setup.Secret, " ", "")
		env.codes, err = service.Enable(u.Id, env.codeAt(time.Now()))
		require.NoError(t, err)
	}

	return env
}

func (e *loginEnv) codeAt(at time.Time) string {
	code, err := hotp.GenerateCodeCustom(e.secret, uint64(at.Unix()/30), hotp.ValidateOpts{Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	require.NoError(e.t, err)

	return code
}

// send makes a request as the browser does: it sends the cookies it has, and keeps the ones it is given.
func (e *loginEnv) send(method, path string, form url.Values) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}

	req := httptest.NewRequest(method, path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("HX-Request", "true")
	for _, c := range e.cookies {
		// the browser sends a cookie to the paths it is for
		if strings.HasPrefix(path, c.Path) {
			req.AddCookie(c)
		}
	}

	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)

	for _, c := range rec.Result().Cookies() {
		if c.MaxAge < 0 || c.Value == "" {
			delete(e.cookies, c.Name)
		} else {
			e.cookies[c.Name] = c
		}
	}

	return rec
}

func (e *loginEnv) password() *httptest.ResponseRecorder {
	return e.send(http.MethodPost, "/login", url.Values{"email": {"alice@example.com"}, "password": {"password123"}, "remember": {"on"}})
}

func TestLoginWithoutTwoFactorStartsTheSession(t *testing.T) {
	e := newLoginEnv(t, false)

	rec := e.password()
	require.Equal(t, "/", rec.Header().Get("HX-Redirect"))
	require.Contains(t, e.cookies, AuthCookieName)
	require.NotContains(t, e.cookies, challengeCookieName)
}

func TestLoginWithTwoFactorNeedsTheCode(t *testing.T) {
	e := newLoginEnv(t, true)

	// a wrong password does not even get to the second step
	rec := e.send(http.MethodPost, "/login", url.Values{"email": {"alice@example.com"}, "password": {"wrong-password"}})
	require.Empty(t, rec.Header().Get("HX-Redirect"))
	require.Empty(t, e.cookies)

	// the right password is not a session
	rec = e.password()
	require.Equal(t, "/login/2fa", rec.Header().Get("HX-Redirect"))
	require.NotContains(t, e.cookies, AuthCookieName, "no session before the code")
	challenge := e.cookies[challengeCookieName]
	require.NotNil(t, challenge)
	require.True(t, challenge.HttpOnly)
	require.Equal(t, "/login", challenge.Path, "only sent to the pages of the sign in")
	require.Equal(t, int(mfa.ChallengeTTL.Seconds()), challenge.MaxAge)

	// the page of the second step is there with the cookie
	rec = e.send(http.MethodGet, "/login/2fa", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "Two-step verification")

	// a wrong code
	rec = e.send(http.MethodPost, "/login/2fa", url.Values{"code": {"000000"}})
	require.Contains(t, rec.Body.String(), "That code is not right.")
	require.Empty(t, rec.Header().Get("HX-Redirect"))
	require.NotContains(t, e.cookies, AuthCookieName)

	// the right code starts the session, which lasts as "remember me" asked
	rec = e.send(http.MethodPost, "/login/2fa", url.Values{"code": {e.codeAt(time.Now().Add(30 * time.Second))}})
	require.Equal(t, "/", rec.Header().Get("HX-Redirect"))
	session := e.cookies[AuthCookieName]
	require.NotNil(t, session)
	require.Equal(t, int((30 * 24 * time.Hour).Seconds()), session.MaxAge)
	require.NotContains(t, e.cookies, challengeCookieName, "the step is done")

	record, err := e.app.FindAuthRecordByToken(session.Value, core.TokenTypeAuth)
	require.NoError(t, err)
	require.Equal(t, e.userID, record.Id)

	// the page of the second step needs the password first
	rec = e.send(http.MethodGet, "/login/2fa", nil)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/login", rec.Header().Get("Location"))
}

func TestRecoveryCodeSignsInOnce(t *testing.T) {
	e := newLoginEnv(t, true)

	e.password()
	rec := e.send(http.MethodPost, "/login/2fa", url.Values{"code": {e.codes[0]}})
	require.Equal(t, "/", rec.Header().Get("HX-Redirect"))
	require.Contains(t, e.cookies, AuthCookieName)

	delete(e.cookies, AuthCookieName)
	e.password()
	rec = e.send(http.MethodPost, "/login/2fa", url.Values{"code": {e.codes[0]}})
	require.Contains(t, rec.Body.String(), "That code is not right.", "a recovery code works once")
	require.NotContains(t, e.cookies, AuthCookieName)

	rec = e.send(http.MethodPost, "/login/2fa", url.Values{"code": {e.codes[1]}})
	require.Equal(t, "/", rec.Header().Get("HX-Redirect"))
}

func TestTheSecondStepNeedsTheProofOfThePassword(t *testing.T) {
	e := newLoginEnv(t, true)
	code := e.codeAt(time.Now().Add(30 * time.Second))

	// no cookie
	rec := e.send(http.MethodPost, "/login/2fa", url.Values{"code": {code}})
	require.Equal(t, "/login", rec.Header().Get("HX-Redirect"))
	require.NotContains(t, e.cookies, AuthCookieName)

	// a cookie that was made up
	e.cookies[challengeCookieName] = &http.Cookie{Name: challengeCookieName, Value: "dXNlcnwxfHRydWU.AAAA", Path: "/login"}
	rec = e.send(http.MethodPost, "/login/2fa", url.Values{"code": {code}})
	require.Equal(t, "/login", rec.Header().Get("HX-Redirect"))
	require.NotContains(t, e.cookies, AuthCookieName)

	// the cookie of another user's step, signed with another key
	otherKey, err := mfa.LoadKey("", t.TempDir())
	require.NoError(t, err)
	e.cookies[challengeCookieName] = &http.Cookie{Name: challengeCookieName, Value: mfa.New(e.app, otherKey).NewChallenge(e.userID, false), Path: "/login"}
	rec = e.send(http.MethodPost, "/login/2fa", url.Values{"code": {code}})
	require.Equal(t, "/login", rec.Header().Get("HX-Redirect"))
	require.NotContains(t, e.cookies, AuthCookieName)
}

func TestTooManyWrongCodesEndTheStep(t *testing.T) {
	e := newLoginEnv(t, true)

	e.password()
	for range mfa.MaxFailures {
		rec := e.send(http.MethodPost, "/login/2fa", url.Values{"code": {"000000"}})
		require.Contains(t, rec.Body.String(), "That code is not right.")
	}

	rec := e.send(http.MethodPost, "/login/2fa", url.Values{"code": {e.codeAt(time.Now().Add(30 * time.Second))}})
	require.Contains(t, rec.Body.String(), "Too many wrong codes")
	require.NotContains(t, e.cookies, AuthCookieName, "the right code does not work either")
	require.NotContains(t, e.cookies, challengeCookieName, "the step has to be started again")
}

// signedIn gives the environment the cookie of a session, as after signing in.
func (e *loginEnv) signedIn() {
	e.t.Helper()

	user, err := e.app.FindRecordById("users", e.userID)
	require.NoError(e.t, err)
	token, err := user.NewAuthToken()
	require.NoError(e.t, err)
	e.cookies[AuthCookieName] = &http.Cookie{Name: AuthCookieName, Value: token, Path: "/"}
}

func TestSettingsTabs(t *testing.T) {
	e := newLoginEnv(t, false)

	rec := e.send(http.MethodGet, "/settings", nil)
	require.Equal(t, http.StatusSeeOther, rec.Code, "signed out")

	e.signedIn()

	// what each tab has, and what the others have
	tabs := map[string]struct{ has, hasNot []string }{
		"/settings":       {[]string{`id="account-title"`, `id="password-title"`, `id="twofa"`}, []string{"overview-title", "feeds-title", "backup-title", "opml-title", "refresh-title"}},
		"/settings/feeds": {[]string{`id="refresh-title"`, `id="feeds-title"`, `id="opml-title"`}, []string{"account-title", "twofa", "overview-title", "backup-title"}},
		"/settings/data":  {[]string{`id="overview-title"`, `id="backup-title"`}, []string{"account-title", "twofa", "feeds-title", "refresh-title", "opml-title"}},
	}
	for path, want := range tabs {
		// htmx loads the page into the content area: the tabs and the section, not the whole page
		rec = e.send(http.MethodGet, path, nil)
		require.Equal(t, http.StatusOK, rec.Code, path)
		body := rec.Body.String()
		require.NotContains(t, body, "<html", path)
		require.Contains(t, body, `aria-label="Sections of the settings"`, path)
		for _, id := range want.has {
			require.Contains(t, body, id, path)
		}
		for _, id := range want.hasNot {
			require.NotContains(t, body, id, path)
		}

		// only the tab that is open is current
		require.Equal(t, 1, strings.Count(body, `aria-current="page"`), path)
		require.Contains(t, body, `href="`+path+`" hx-get="`+path+`"`, path)

		// the whole page: a refresh, or an address that is typed in, has the sidebar too
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(e.cookies[AuthCookieName])
		full := httptest.NewRecorder()
		e.router.ServeHTTP(full, req)
		require.Equal(t, http.StatusOK, full.Code, path)
		require.Contains(t, full.Body.String(), "<html", path)
		require.Contains(t, full.Body.String(), `id="sidebar"`, path)
		require.Contains(t, full.Body.String(), `aria-label="Sections of the settings"`, path)
	}

	// the old address of the one page is the account tab
	rec = e.send(http.MethodGet, "/settings/unknown", nil)
	require.Equal(t, http.StatusNotFound, rec.Code)
}

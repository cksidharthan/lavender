package controller

import (
	"archive/zip"
	"bytes"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cksidharthan/lavender/pkg/backup"
	"github.com/cksidharthan/lavender/pkg/feedfetch"
	"github.com/cksidharthan/lavender/pkg/iconcache"
	"github.com/cksidharthan/lavender/pkg/mfa"
	_ "github.com/cksidharthan/lavender/pkg/pocketbase/migrations"
	"github.com/cksidharthan/lavender/pkg/store"
	"github.com/go-chi/chi/v5"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/hotp"
	"github.com/stretchr/testify/require"
)

const cookieName = "test_auth"

type env struct {
	mfa    *mfa.Service
	app    core.App
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

	icons, err := iconcache.New(t.TempDir())
	require.NoError(t, err)

	key, err := mfa.LoadKey("", t.TempDir())
	require.NoError(t, err)

	c := New(chi.NewRouter(), slog.New(slog.DiscardHandler), app, cookieName, icons, mfa.New(app, key))
	c.RegisterRoutes()

	return &env{mfa: c.mfa, app: app, t: t, router: c.chiRouter, store: c.store, users: users}
}

func (e *env) do(user string, req *http.Request) *httptest.ResponseRecorder {
	req.Header.Set("HX-Request", "true")
	if u, ok := e.users[user]; ok {
		// the record as it is now, the token is made from what it holds
		fresh, err := e.app.FindRecordById("users", u.Id)
		require.NoError(e.t, err)
		token, err := fresh.NewAuthToken()
		require.NoError(e.t, err)
		req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	}

	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)

	return rec
}

func (e *env) postForm(user, path string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	return e.do(user, req)
}

func (e *env) uploadTo(path, user string, content []byte) *httptest.ResponseRecorder {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "feeds.opml")
	require.NoError(e.t, err)
	_, err = part.Write(content)
	require.NoError(e.t, err)
	require.NoError(e.t, mw.Close())

	req := httptest.NewRequest(http.MethodPost, path, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	return e.do(user, req)
}

func (e *env) upload(user string, content []byte, field string) *httptest.ResponseRecorder {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if content != nil {
		part, err := mw.CreateFormFile(field, "backup.zip")
		require.NoError(e.t, err)
		_, err = part.Write(content)
		require.NoError(e.t, err)
	}
	require.NoError(e.t, mw.Close())

	req := httptest.NewRequest(http.MethodPost, "/account/import", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	return e.do(user, req)
}

func TestRequiresLogin(t *testing.T) {
	e := newEnv(t)

	require.Equal(t, "/login", e.postForm("", "/account/settings", url.Values{}).Header().Get("HX-Redirect"))
	require.Equal(t, "/login", e.upload("", []byte("x"), "file").Header().Get("HX-Redirect"))

	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/account/export", nil))
	require.Equal(t, http.StatusUnauthorized, rec.Code, "the export of someone else is never sent")
}

func TestSettings(t *testing.T) {
	e := newEnv(t)
	alice := e.users["alice"].Id

	rec := e.postForm("alice", "/account/settings", url.Values{"default_refresh_minutes": {"45"}})
	require.Contains(t, rec.Body.String(), "Settings saved.")
	require.Equal(t, "reload-feed-page", rec.Header().Get("HX-Trigger"))
	minutes, err := e.store.DefaultRefreshMinutes(alice)
	require.NoError(t, err)
	require.Equal(t, 45, minutes)

	// empty is the default of the server
	rec = e.postForm("alice", "/account/settings", url.Values{"default_refresh_minutes": {" "}})
	require.Contains(t, rec.Body.String(), "Settings saved.")
	minutes, err = e.store.DefaultRefreshMinutes(alice)
	require.NoError(t, err)
	require.Zero(t, minutes)

	for _, bad := range []string{"2", "1441", "-5", "abc", "1.5"} {
		rec = e.postForm("alice", "/account/settings", url.Values{"default_refresh_minutes": {bad}})
		require.Contains(t, rec.Body.String(), "Could not save the settings", bad)
	}

	// the settings of a user are of that user
	_, err = e.store.DefaultRefreshMinutes(e.users["bob"].Id)
	require.NoError(t, err)
}

func TestExportAndImport(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.users["alice"].Id, e.users["bob"].Id

	require.NoError(t, e.store.CreateFolder(alice, "Tech"))
	feed, err := e.store.AddFeed(alice, "https://blog.example.com/feed", "", feedfetch.Meta{Title: "Blog", Type: "rss"})
	require.NoError(t, err)
	_, err = e.store.SaveFetch(feed.ID, feedfetch.Result{Meta: feedfetch.Meta{Type: "rss"}, Items: []feedfetch.Item{{GUID: "g1", Title: "One"}, {GUID: "g2", Title: "Two"}}})
	require.NoError(t, err)

	rec := e.do("alice", httptest.NewRequest(http.MethodGet, "/account/export", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/zip", rec.Header().Get("Content-Type"))
	require.Regexp(t, `^attachment; filename="lavender-backup-\d{4}-\d{2}-\d{2}-\d{4}\.zip"$`, rec.Header().Get("Content-Disposition"))
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

	data, err := io.ReadAll(rec.Body)
	require.NoError(t, err)
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)
	require.NotEmpty(t, zr.File)

	// bob imports it, into his account
	rec = e.upload("bob", data, "file")
	require.Contains(t, rec.Body.String(), "Backup imported")
	require.Contains(t, rec.Body.String(), "Added 1 feed, 1 folder, 2 articles.")
	require.Contains(t, rec.Body.String(), `hx-swap-oob="outerHTML"`, "the sidebar has the new feeds")
	require.Contains(t, rec.Body.String(), "Blog")
	// the header of the response as it was sent, a header that is set after the body is written is lost
	require.Equal(t, "reload-feed-page", rec.Result().Header.Get("HX-Trigger"))

	stats, err := e.store.Stats(bob, 10*time.Minute)
	require.NoError(t, err)
	require.Equal(t, 1, stats.Feeds)
	require.Equal(t, 2, stats.Articles)

	// again: nothing new
	rec = e.upload("bob", data, "file")
	require.Contains(t, rec.Body.String(), "Nothing new")

	// what is not a backup
	rec = e.upload("bob", []byte("not a zip"), "file")
	require.Contains(t, rec.Body.String(), "Could not import the backup")
	require.Contains(t, rec.Body.String(), "not a zip file")

	rec = e.upload("bob", nil, "file")
	require.Contains(t, rec.Body.String(), "Choose the backup file first.")
	rec = e.upload("bob", data, "other")
	require.Contains(t, rec.Body.String(), "Choose the backup file first.")

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("manifest.json")
	require.NoError(t, err)
	_, err = w.Write([]byte(`{"app":"someone else","version":1}`))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	rec = e.upload("bob", buf.Bytes(), "file")
	require.Contains(t, rec.Body.String(), "This is not a backup of Lavender.")
}

func TestSummary(t *testing.T) {
	require.Equal(t, "Nothing new, all of it is in your account already.", summary(backup.Counts{}))
	require.Equal(t, "Added 1 feed, 2 folders, 1,000 articles.", strings.Replace(summary(backup.Counts{Feeds: 1, Folders: 2, Articles: 1000}), "1000", "1,000", 1))
	require.Equal(t, "Added 1 saved article, 1 icon.", summary(backup.Counts{Saved: 1, Icons: 1}))
}

func TestProfile(t *testing.T) {
	e := newEnv(t)
	alice := e.users["alice"]

	rec := e.postForm("alice", "/account/profile", url.Values{"name": {"Alice A"}, "email": {"alice@example.com"}})
	require.Contains(t, rec.Body.String(), "Profile saved.")
	require.Contains(t, rec.Body.String(), `hx-swap-oob="outerHTML"`, "the sidebar has the new name")
	require.Contains(t, rec.Body.String(), "Alice A")
	require.Equal(t, "reload-feed-page", rec.Result().Header.Get("HX-Trigger"))

	// the email needs the password
	rec = e.postForm("alice", "/account/profile", url.Values{"name": {"Alice A"}, "email": {"new@example.com"}, "current_password": {"nope"}})
	require.Contains(t, rec.Body.String(), "Could not save the profile")
	require.Contains(t, rec.Body.String(), "current password")

	rec = e.postForm("alice", "/account/profile", url.Values{"name": {"Alice A"}, "email": {"new@example.com"}, "current_password": {"password123"}})
	require.Contains(t, rec.Body.String(), "Profile saved.")

	// a changed email ends the sessions of PocketBase, so the answer has a new one
	var session *http.Cookie
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == cookieName {
			session = cookie
		}
	}
	require.NotNil(t, session, "signed in again")
	require.True(t, session.HttpOnly)
	require.Zero(t, session.MaxAge, "until the browser is closed")
	signedIn, err := e.app.FindAuthRecordByToken(session.Value, core.TokenTypeAuth)
	require.NoError(t, err)
	require.Equal(t, alice.Id, signedIn.Id)
	require.Equal(t, "new@example.com", signedIn.Email())

	// without a change of the email the session stays as it is
	rec = e.postForm("alice", "/account/profile", url.Values{"name": {"Alice B"}, "email": {"new@example.com"}})
	require.Contains(t, rec.Body.String(), "Profile saved.")
	require.Empty(t, rec.Result().Cookies())

	rec = e.postForm("alice", "/account/profile", url.Values{"name": {"x"}, "email": {"bob@example.com"}, "current_password": {"password123"}})
	require.Contains(t, rec.Body.String(), "already exists")
	rec = e.postForm("alice", "/account/profile", url.Values{"name": {"x"}, "email": {"nonsense"}})
	require.Contains(t, rec.Body.String(), "valid email address")
	require.Equal(t, "/login", e.postForm("", "/account/profile", url.Values{}).Header().Get("HX-Redirect"))
}

func TestOPML(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.users["alice"].Id, e.users["bob"].Id

	require.NoError(t, e.store.CreateFolder(alice, "Tech"))
	tree, err := e.store.Tree(alice)
	require.NoError(t, err)
	_, err = e.store.AddFeed(alice, "https://go.dev/blog/feed.atom", tree[0].ID, feedfetch.Meta{Title: "Go Blog", Type: "atom"})
	require.NoError(t, err)

	rec := e.do("alice", httptest.NewRequest(http.MethodGet, "/account/opml/export", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "text/x-opml; charset=utf-8", rec.Header().Get("Content-Type"))
	require.Regexp(t, `^attachment; filename="lavender-feeds-\d{4}-\d{2}-\d{2}\.opml"$`, rec.Header().Get("Content-Disposition"))
	file := rec.Body.Bytes()
	require.Contains(t, string(file), `xmlUrl="https://go.dev/blog/feed.atom"`)
	require.Contains(t, string(file), `text="Tech"`)

	rec = httptest.NewRecorder()
	e.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/account/opml/export", nil))
	require.Equal(t, http.StatusUnauthorized, rec.Code, "the feeds of someone else are never sent")

	rec = e.uploadTo("/account/opml/import", "bob", file)
	require.Contains(t, rec.Body.String(), "Feeds imported")
	require.Contains(t, rec.Body.String(), "Added 1 feed and 1 folder")
	require.Contains(t, rec.Body.String(), "fetched in the background")
	require.Contains(t, rec.Body.String(), `hx-swap-oob="outerHTML"`)
	require.Equal(t, "reload-feed-page", rec.Result().Header.Get("HX-Trigger"))

	stats, err := e.store.Stats(bob, 10*time.Minute)
	require.NoError(t, err)
	require.Equal(t, 1, stats.Feeds)

	rec = e.uploadTo("/account/opml/import", "bob", file)
	require.Contains(t, rec.Body.String(), "1 already in your account")

	rec = e.uploadTo("/account/opml/import", "bob", []byte("not opml"))
	require.Contains(t, rec.Body.String(), "Could not import the feeds")
	require.Contains(t, rec.Body.String(), "not an OPML file")

	rec = e.uploadTo("/account/opml/import", "bob", []byte(`<opml version="2.0"><body></body></opml>`))
	require.Contains(t, rec.Body.String(), "no feeds in it")
}

func TestChangePassword(t *testing.T) {
	e := newEnv(t)
	alice := e.users["alice"]

	form := func(current, newPassword, confirm string) url.Values {
		return url.Values{"current_password": {current}, "new_password": {newPassword}, "confirm_password": {confirm}}
	}

	for name, values := range map[string]url.Values{
		"not the same":   form("password123", "a-new-password", "a-new-passwordX"),
		"wrong current":  form("nope", "a-new-password", "a-new-password"),
		"too short":      form("password123", "short", "short"),
		"the same again": form("password123", "password123", "password123"),
		"empty":          form("", "", ""),
	} {
		rec := e.postForm("alice", "/account/password", values)
		require.Contains(t, rec.Body.String(), "Could not change the password", name)
		require.Empty(t, rec.Result().Cookies(), name)
	}
	fresh, err := e.app.FindRecordById("users", alice.Id)
	require.NoError(t, err)
	require.True(t, fresh.ValidatePassword("password123"), "nothing changed")

	rec := e.postForm("alice", "/account/password", form("password123", "a-new-password", "a-new-password"))
	require.Contains(t, rec.Body.String(), "Password changed.")
	require.Equal(t, "reload-feed-page", rec.Result().Header.Get("HX-Trigger"))

	fresh, err = e.app.FindRecordById("users", alice.Id)
	require.NoError(t, err)
	require.True(t, fresh.ValidatePassword("a-new-password"))
	require.False(t, fresh.ValidatePassword("password123"))

	// the session that asked is replaced, the old one is not valid any more
	var session *http.Cookie
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == cookieName {
			session = cookie
		}
	}
	require.NotNil(t, session)
	signedIn, err := e.app.FindAuthRecordByToken(session.Value, core.TokenTypeAuth)
	require.NoError(t, err)
	require.Equal(t, alice.Id, signedIn.Id)

	oldToken, err := alice.NewAuthToken() // made from the record before the change
	require.NoError(t, err)
	_, err = e.app.FindAuthRecordByToken(oldToken, core.TokenTypeAuth)
	require.Error(t, err, "the other sessions are signed out")

	require.Equal(t, "/login", e.postForm("", "/account/password", url.Values{}).Header().Get("HX-Redirect"))
}

func TestTwoFactor(t *testing.T) {
	e := newEnv(t)
	alice := e.users["alice"].Id

	rec := e.do("alice", httptest.NewRequest(http.MethodGet, "/account/2fa", nil))
	require.Contains(t, rec.Body.String(), `id="twofa"`)
	require.Contains(t, rec.Body.String(), ">Off<")
	require.Contains(t, rec.Body.String(), "Set up")

	rec = e.postForm("alice", "/account/2fa/setup", nil)
	body := rec.Body.String()
	require.Contains(t, body, `src="data:image/png;base64,`)
	require.Contains(t, body, "Enter the 6-digit code")
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	secret := regexp.MustCompile(`select-all">([A-Z2-7 ]+)</p>`).FindStringSubmatch(body)
	require.Len(t, secret, 2, "the key to type in is on the page")

	code := func(at time.Time) string {
		c, err := hotp.GenerateCodeCustom(strings.ReplaceAll(secret[1], " ", ""), uint64(at.Unix()/30), hotp.ValidateOpts{Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
		require.NoError(t, err)

		return c
	}

	// a wrong code is a toast, and the card is not swapped
	rec = e.postForm("alice", "/account/2fa/enable", url.Values{"code": {"000000"}})
	require.Equal(t, "none", rec.Header().Get("HX-Reswap"))
	require.Contains(t, rec.Body.String(), "That code is not right.")
	require.NotContains(t, rec.Body.String(), `id="twofa"`)

	rec = e.postForm("alice", "/account/2fa/enable", url.Values{"code": {code(time.Now())}})
	body = rec.Body.String()
	require.Empty(t, rec.Header().Get("HX-Reswap"))
	require.Contains(t, body, "Save your recovery codes")
	require.Contains(t, body, ">On<")
	require.Contains(t, body, "Two-factor authentication is on.")
	codes := regexp.MustCompile(`select-all">([a-z0-9]{5}-[a-z0-9]{5})</li>`).FindAllStringSubmatch(body, -1)
	require.Len(t, codes, 10)

	// the card as it is afterwards
	rec = e.do("alice", httptest.NewRequest(http.MethodGet, "/account/2fa", nil))
	require.Contains(t, rec.Body.String(), "10 recovery codes left")
	require.NotContains(t, rec.Body.String(), codes[0][1], "the codes are shown once")

	// it can not be set up while it is on
	rec = e.postForm("alice", "/account/2fa/setup", nil)
	require.Contains(t, rec.Body.String(), "on already")
	require.Equal(t, "none", rec.Header().Get("HX-Reswap"))

	// new recovery codes, with the password and a recovery code
	rec = e.postForm("alice", "/account/2fa/recovery", url.Values{"current_password": {"wrong"}, "code": {codes[0][1]}})
	require.Contains(t, rec.Body.String(), "password is not right")
	rec = e.postForm("alice", "/account/2fa/recovery", url.Values{"current_password": {"password123"}, "code": {codes[0][1]}})
	require.Contains(t, rec.Body.String(), "Save your recovery codes")
	newCodes := regexp.MustCompile(`select-all">([a-z0-9]{5}-[a-z0-9]{5})</li>`).FindAllStringSubmatch(rec.Body.String(), -1)
	require.Len(t, newCodes, 10)
	require.NotEqual(t, codes[1][1], newCodes[1][1])

	// turning it off
	rec = e.postForm("alice", "/account/2fa/disable", url.Values{"current_password": {"password123"}, "code": {"000000"}})
	require.Equal(t, "none", rec.Header().Get("HX-Reswap"))
	enabled, _, err := e.mfa.Status(alice)
	require.NoError(t, err)
	require.True(t, enabled)

	rec = e.postForm("alice", "/account/2fa/recovery", url.Values{"current_password": {"password123"}, "code": {"zzzzz-zzzzz"}})
	require.Equal(t, "none", rec.Header().Get("HX-Reswap"))

	// the old recovery codes stopped working with the new ones, a new one turns it off
	rec = e.postForm("alice", "/account/2fa/disable", url.Values{"current_password": {"password123"}, "code": {codes[1][1]}})
	require.Equal(t, "none", rec.Header().Get("HX-Reswap"))
	rec = e.postForm("alice", "/account/2fa/disable", url.Values{"current_password": {"password123"}, "code": {newCodes[1][1]}})
	require.Contains(t, rec.Body.String(), "Two-factor authentication is off.")
	require.Contains(t, rec.Body.String(), ">Off<")
	enabled, _, err = e.mfa.Status(alice)
	require.NoError(t, err)
	require.False(t, enabled)

	// of the account of the user that is signed in, nobody else
	rec = e.postForm("", "/account/2fa/setup", nil)
	require.Equal(t, "/login", rec.Header().Get("HX-Redirect"))
}

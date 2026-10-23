package pocketbase_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cksidharthan/lavender/pkg/config"
	lavender "github.com/cksidharthan/lavender/pkg/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/require"
)

// The API of PocketBase signs in with the password alone, so it must refuse the accounts that have a second step.
func TestPasswordApiRefusesAccountsWithTwoFactor(t *testing.T) {
	app, err := lavender.InitializePocketbase(&config.Config{PocketBaseDataDir: t.TempDir()}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)
	for _, email := range []string{"plain@example.com", "secure@example.com"} {
		u := core.NewRecord(users)
		u.SetEmail(email)
		u.SetPassword("password123")
		u.Set("totp_enabled", email == "secure@example.com")
		require.NoError(t, app.Save(u))
	}

	handler, err := lavender.NewHandler(app)
	require.NoError(t, err)

	signIn := func(email string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/collections/users/auth-with-password", strings.NewReader(`{"identity":"`+email+`","password":"password123"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		return rec
	}

	rec := signIn("plain@example.com")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"token"`)

	rec = signIn("secure@example.com")
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), `"token"`)
	require.Contains(t, rec.Body.String(), "two-factor")

	// and the fields of two-factor authentication are not in what the API sends
	for _, field := range []string{"totp_secret", "totp_recovery", "totp_enabled", "totp_last_step"} {
		require.NotContains(t, rec.Body.String(), field)
	}
}

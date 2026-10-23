package middlewares

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestLogger(t *testing.T) {
	newHandler := func(buf *bytes.Buffer, status int) http.Handler {
		logger := slog.New(slog.NewJSONHandler(buf, nil))

		return RequestLogger(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte("body"))
		}))
	}

	t.Run("logs method, path, status and bytes as structured json", func(t *testing.T) {
		var buf bytes.Buffer
		newHandler(&buf, http.StatusOK).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/login", nil))

		var entry map[string]any
		require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
		assert.Equal(t, "INFO", entry["level"])
		assert.Equal(t, "GET", entry["method"])
		assert.Equal(t, "/login", entry["path"])
		assert.EqualValues(t, 200, entry["status"])
		assert.EqualValues(t, 4, entry["bytes"])
	})

	t.Run("uses warn for 4xx and error for 5xx", func(t *testing.T) {
		for status, level := range map[int]string{http.StatusNotFound: "WARN", http.StatusInternalServerError: "ERROR"} {
			var buf bytes.Buffer
			newHandler(&buf, status).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))

			var entry map[string]any
			require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
			assert.Equal(t, level, entry["level"])
		}
	})

	t.Run("skips health checks and static assets", func(t *testing.T) {
		for _, path := range []string{"/api/v1/healthz", "/static/app.css"} {
			var buf bytes.Buffer
			newHandler(&buf, http.StatusOK).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
			assert.Empty(t, buf.String(), path)
		}
	})
}

func TestRequestLoggerFields(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	router := chi.NewRouter()
	router.Use(RequestLogger(logger))
	router.Get("/users/{id}", func(_ http.ResponseWriter, r *http.Request) { NoteUser(r.Context(), "user-1") })

	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/users/42", nil))

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
	assert.Equal(t, "/users/{id}", entry["route"], "the route pattern next to the path")
	assert.Equal(t, "/users/42", entry["path"])
	assert.Equal(t, "user-1", entry["user_id"])
}

func TestRequestLoggerAddsRequestID(t *testing.T) {
	var buf bytes.Buffer
	// the context attributes are what put the request id on the records of the handlers
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	router := chi.NewRouter()
	router.Use(RequestLogger(logger))
	router.Get("/x", func(http.ResponseWriter, *http.Request) {})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-Request-Id", "req-42")
	req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) Mobile/15E148")
	req.Header.Set("HX-Request", "true")
	router.ServeHTTP(rec, req)

	assert.Equal(t, "req-42", rec.Header().Get("X-Request-Id"))

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
	assert.Equal(t, "mobile", entry["device"])
	assert.Equal(t, true, entry["htmx"])
	assert.Equal(t, false, entry["slow"])
	assert.Contains(t, entry, "duration_ms")
	assert.Contains(t, entry, "client_ip")
}

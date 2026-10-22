package middlewares

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

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

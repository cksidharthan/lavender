package middlewares

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	lavenderLogger "github.com/cksidharthan/lavender/pkg/logger"
	"github.com/go-chi/chi/v5/middleware"
)

// RequestLogger logs every http request through the application slog logger, so the access logs
// follow the same json / otel pipeline as the rest of the logs. chi's middleware.Logger writes
// plain text through the stdlib log package instead.
func RequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if skipRequestLog(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			next.ServeHTTP(ww, r)

			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}

			level := slog.LevelInfo
			switch {
			case status >= http.StatusInternalServerError:
				level = slog.LevelError
			case status >= http.StatusBadRequest:
				level = slog.LevelWarn
			}

			logger.Log(r.Context(), level, "http request",
				slog.String(lavenderLogger.MethodKey, r.Method),
				slog.String(lavenderLogger.PathKey, r.URL.Path),
				slog.Int(lavenderLogger.StatusKey, status),
				slog.Int(lavenderLogger.BytesKey, ww.BytesWritten()),
				slog.Duration(lavenderLogger.DurationKey, time.Since(start)),
				slog.String(lavenderLogger.RemoteAddrKey, r.RemoteAddr),
				slog.String(lavenderLogger.UserAgentKey, r.UserAgent()),
			)
		})
	}
}

// skipRequestLog keeps the noisy health checks and static assets out of the logs.
func skipRequestLog(path string) bool {
	return strings.HasSuffix(path, "/healthz") || strings.HasPrefix(path, "/static/")
}

package middlewares

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	lavenderLogger "github.com/cksidharthan/lavender/pkg/logger"
	lavenderOTEL "github.com/cksidharthan/lavender/pkg/otel"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/otel/attribute"
)

// slowRequest is how long a request takes before it is logged as a warning, to find the requests to look at.
const slowRequest = 3 * time.Second

// RequestLogger logs every http request through the application slog logger, so the access logs
// follow the same json / otel pipeline as the rest of the logs. chi's middleware.Logger writes
// plain text through the stdlib log package instead.
//
// It also gives the request an id, taken from the X-Request-Id header or made up, which is sent back in the response
// and is part of every log record of the request, together with the user once that is known.
func RequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if skipRequestLog(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			id := requestID(r)
			ww.Header().Set(requestIDHeader, id)

			ctx := lavenderLogger.NewContext(r.Context())
			ctx = context.WithValue(ctx, userHolderKey{}, &userHolder{})
			lavenderLogger.AddToContext(ctx, slog.String(lavenderLogger.RequestIDKey, id))
			lavenderOTEL.SetAttributes(ctx, attribute.String("http.request.id", id))
			r = r.WithContext(ctx)

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

			duration := time.Since(start)
			slow := duration >= slowRequest
			if level == slog.LevelInfo && slow {
				level = slog.LevelWarn
			}

			logger.Log(r.Context(), level, "http request",
				slog.String(lavenderLogger.MethodKey, r.Method),
				slog.String(lavenderLogger.PathKey, r.URL.Path),
				slog.String(lavenderLogger.RouteKey, routeOf(r)),
				slog.Int(lavenderLogger.StatusKey, status),
				slog.Int(lavenderLogger.BytesKey, ww.BytesWritten()),
				slog.Duration(lavenderLogger.DurationKey, duration),
				slog.Float64("duration_ms", float64(duration.Microseconds())/1000),
				slog.Bool("slow", slow),
				slog.Int64("request_bytes", max(r.ContentLength, 0)),
				slog.String("content_type", ww.Header().Get("Content-Type")),
				slog.String("host", r.Host),
				slog.String("scheme", scheme(r)),
				slog.String("protocol", r.Proto),
				slog.String("client_ip", clientAddress(r)),
				slog.String("forwarded_for", truncate(r.Header.Get("X-Forwarded-For"), 128)),
				slog.String(lavenderLogger.RemoteAddrKey, r.RemoteAddr),
				slog.String(lavenderLogger.UserAgentKey, r.UserAgent()),
				slog.String("device", DeviceClass(r.UserAgent())),
				slog.Bool("htmx", isHTMX(r)),
				slog.String(lavenderLogger.UserIDKey, userID(r)),
			)
		})
	}
}

// skipRequestLog keeps the noisy health checks and static assets out of the logs.
func skipRequestLog(path string) bool {
	return strings.HasSuffix(path, "/healthz") || strings.HasPrefix(path, "/static/")
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}

	return s
}

// userID is the id of the signed in user, empty for the requests of nobody. The authentication runs below the logger
// and sets the user on its own copy of the request, so it is also kept in a holder the logger reads.
func userID(r *http.Request) string {
	if holder, ok := r.Context().Value(userHolderKey{}).(*userHolder); ok {
		return holder.id
	}

	return ""
}

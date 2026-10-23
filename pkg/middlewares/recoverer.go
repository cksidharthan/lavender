package middlewares

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	lavenderLogger "github.com/cksidharthan/lavender/pkg/logger"
	lavenderOTEL "github.com/cksidharthan/lavender/pkg/otel"
)

// Recoverer answers 500 when a handler panics, like chi's Recoverer. The panic is also logged with its stack
// through the application logger, recorded on the trace and counted as a metric. http.ErrAbortHandler is not a
// failure, it is how a handler stops a response, so it is passed on to the server.
func Recoverer(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}

				if rec == http.ErrAbortHandler { //nolint:errorlint // recover returns a value, as it is compared by the server
					panic(rec)
				}

				ctx := r.Context()
				err := fmt.Errorf("panic: %v", rec)

				lavenderOTEL.RecordError(ctx, err)
				lavenderOTEL.RecordHTTPPanic(ctx, routeOf(r))
				logger.ErrorContext(ctx, "panic recovered",
					slog.Any(lavenderLogger.ErrorKey, err),
					slog.String(lavenderLogger.MethodKey, r.Method),
					slog.String(lavenderLogger.PathKey, r.URL.Path),
					slog.String("stack", string(debug.Stack())),
				)

				// a connection that is upgraded (websocket) can not take a status anymore
				if r.Header.Get("Connection") != "Upgrade" {
					w.WriteHeader(http.StatusInternalServerError)
				}
			}()

			next.ServeHTTP(w, r)
		})
	}
}

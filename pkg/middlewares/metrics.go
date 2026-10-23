package middlewares

import (
	"net/http"
	"slices"
	"time"

	lavenderOTEL "github.com/cksidharthan/lavender/pkg/otel"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// unmatchedRoute is the route label of the requests no route handles (pocketbase, and the 404s). The path is not
// used, as it has too many values for a metric.
const unmatchedRoute = "unmatched"

// Metrics records the duration, the response size and the number of active requests of every http request.
// The route label is the route pattern, so it is low cardinality.
//
// Requests whose path exactly matches one of skipPaths (e.g. the health checks) are not recorded.
func Metrics(skipPaths ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if slices.Contains(skipPaths, r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			start := time.Now()
			done := lavenderOTEL.HTTPRequestStarted(r.Context(), r.Method)
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			// deferred, so the request is also recorded when http.ErrAbortHandler travels up to the server
			defer func() {
				done()

				status := ww.Status()
				if status == 0 {
					status = http.StatusOK
				}

				lavenderOTEL.RecordHTTPRequest(r.Context(), lavenderOTEL.HTTPRequest{
					Method:        r.Method,
					Route:         routeOf(r),
					Status:        status,
					RequestBytes:  max(r.ContentLength, 0),
					ResponseBytes: int64(ww.BytesWritten()),
					Duration:      time.Since(start),
					HTMX:          isHTMX(r),
					Device:        DeviceClass(r.UserAgent()),
				})
			}()

			next.ServeHTTP(ww, r)
		})
	}
}

// routeOf is the pattern of the route that handled the request, known once chi has routed it.
func routeOf(r *http.Request) string {
	if routeCtx := chi.RouteContext(r.Context()); routeCtx != nil && routeCtx.RoutePattern() != "" {
		return routeCtx.RoutePattern()
	}

	return unmatchedRoute
}

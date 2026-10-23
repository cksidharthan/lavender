package middlewares

import (
	"net"
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "github.com/cksidharthan/lavender/pkg/middlewares"

// Tracing starts a server span for every http request. An incoming trace context (traceparent
// header) is continued, and the span is put on the request context, so the slog / otel log
// bridge and any child spans are correlated to the request.
//
// Requests whose path exactly matches one of skipPaths (e.g. the health checks) are not traced.
// Register it before RequestLogger so the request logs sent through the otel log handler are correlated to the trace.
func Tracing(tracerProvider trace.TracerProvider, skipPaths ...string) func(http.Handler) http.Handler {
	tracer := tracerProvider.Tracer(tracerName)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if slices.Contains(skipPaths, r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
			ctx, span := tracer.Start(ctx, r.Method+" "+r.URL.Path,
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(
					attribute.String("http.request.method", r.Method),
					attribute.String("url.path", r.URL.Path),
					attribute.String("server.address", r.Host),
					attribute.String("user_agent.original", r.UserAgent()),
					attribute.String("client.address", clientAddress(r)),
					attribute.String("url.scheme", scheme(r)),
					attribute.String("network.protocol.version", r.Proto),
					attribute.Int64("http.request.body.size", max(r.ContentLength, 0)),
				),
			)
			defer span.End()

			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			next.ServeHTTP(ww, r.WithContext(ctx))

			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}

			// the route pattern is only known once chi has routed the request, and keeps the span
			// name low cardinality (/users/{id} instead of /users/42).
			if routeCtx := chi.RouteContext(r.Context()); routeCtx != nil && routeCtx.RoutePattern() != "" {
				span.SetName(r.Method + " " + routeCtx.RoutePattern())
				span.SetAttributes(attribute.String("http.route", routeCtx.RoutePattern()))
			} else {
				// no route handled it (pocketbase, 404s), the path is not a name that can be grouped on
				span.SetName(r.Method + " " + unmatchedRoute)
			}

			span.SetAttributes(
				attribute.Int("http.response.status_code", status),
				attribute.Int("http.response.body.size", ww.BytesWritten()),
			)
			if status >= http.StatusInternalServerError {
				span.SetStatus(codes.Error, http.StatusText(status))
			}
		})
	}
}

// clientAddress is the ip address of the client, without the port.
func clientAddress(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}

	return r.RemoteAddr
}

// scheme is the scheme the client used, the application runs behind a proxy that ends the tls.
func scheme(r *http.Request) string {
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		return "https"
	}

	return "http"
}

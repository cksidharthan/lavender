package middlewares

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestTracing(t *testing.T) {
	setup := func(status int) (*chi.Mux, *tracetest.SpanRecorder) {
		recorder := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))

		router := chi.NewRouter()
		router.Use(Tracing(tp, "/api/v1/healthz"))
		router.Get("/users/{id}", func(w http.ResponseWriter, r *http.Request) {
			assert.True(t, trace.SpanContextFromContext(r.Context()).IsValid(), "span must be on the request context")
			w.WriteHeader(status)
		})
		router.Get("/api/v1/healthz", func(w http.ResponseWriter, _ *http.Request) {})

		return router, recorder
	}

	t.Run("starts a server span named after the route", func(t *testing.T) {
		router, recorder := setup(http.StatusOK)
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/users/42", nil))

		spans := recorder.Ended()
		require.Len(t, spans, 1)
		assert.Equal(t, "GET /users/{id}", spans[0].Name())
		assert.Equal(t, trace.SpanKindServer, spans[0].SpanKind())
		assert.Contains(t, spans[0].Attributes(), attribute.Int("http.response.status_code", 200))
		assert.Contains(t, spans[0].Attributes(), attribute.String("http.route", "/users/{id}"))
	})

	t.Run("marks 5xx responses as errors", func(t *testing.T) {
		router, recorder := setup(http.StatusInternalServerError)
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/users/1", nil))

		spans := recorder.Ended()
		require.Len(t, spans, 1)
		assert.Equal(t, codes.Error, spans[0].Status().Code)
	})

	t.Run("does not trace skipped paths", func(t *testing.T) {
		router, recorder := setup(http.StatusOK)
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/healthz", nil))

		assert.Empty(t, recorder.Ended())
	})
}

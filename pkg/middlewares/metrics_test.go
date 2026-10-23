package middlewares

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// the instruments of the application are bound to the first global meter provider that is set, so it is set once
// for all the tests of the package.
var metricReader = func() *sdkmetric.ManualReader {
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))

	return reader
}()

func collect(t *testing.T) map[string]metricdata.Metrics {
	t.Helper()

	var rm metricdata.ResourceMetrics
	require.NoError(t, metricReader.Collect(context.Background(), &rm))

	metrics := map[string]metricdata.Metrics{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			metrics[m.Name] = m
		}
	}

	return metrics
}

func TestMetrics(t *testing.T) {
	router := chi.NewRouter()
	router.Use(Metrics("/api/v1/healthz"))
	router.Get("/users/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("body"))
	})
	router.Get("/api/v1/healthz", func(http.ResponseWriter, *http.Request) {})

	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/users/42", nil))
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/healthz", nil))
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/nowhere", nil))

	metrics := collect(t)

	duration, ok := metrics["http.server.request.duration"].Data.(metricdata.Histogram[float64])
	require.True(t, ok)
	require.Len(t, duration.DataPoints, 2, "the health check is skipped")

	routes := map[string]uint64{}
	for _, dp := range duration.DataPoints {
		route, _ := dp.Attributes.Value("http.route")
		routes[route.AsString()] = dp.Count

		if route.AsString() == "/users/{id}" {
			status, _ := dp.Attributes.Value("http.response.status_code")
			assert.Equal(t, int64(http.StatusTeapot), status.AsInt64())
			method, _ := dp.Attributes.Value("http.request.method")
			assert.Equal(t, "GET", method.AsString())
		}
	}
	assert.Equal(t, map[string]uint64{"/users/{id}": 1, unmatchedRoute: 1}, routes, "the route pattern is the label, not the path")

	active, ok := metrics["http.server.active_requests"].Data.(metricdata.Sum[int64])
	require.True(t, ok)
	for _, dp := range active.DataPoints {
		assert.Zero(t, dp.Value, "no request is active anymore")
	}

	size, ok := metrics["http.server.response.body.size"].Data.(metricdata.Histogram[int64])
	require.True(t, ok)
	for _, dp := range size.DataPoints {
		if route, _ := dp.Attributes.Value("http.route"); route.AsString() == "/users/{id}" {
			assert.EqualValues(t, 4, dp.Sum)
		}
	}
}

func TestRecoverer(t *testing.T) {
	var logged lockedBuffer
	logger := newTestLogger(&logged)

	router := chi.NewRouter()
	router.Use(Recoverer(logger))
	router.Get("/boom", func(http.ResponseWriter, *http.Request) { panic("boom") })
	router.Get("/abort", func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) })

	t.Run("answers 500, logs the stack and counts the panic", func(t *testing.T) {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.Contains(t, logged.String(), "panic recovered")
		assert.Contains(t, logged.String(), "boom")
		assert.Contains(t, logged.String(), "stack")

		panics, ok := collect(t)["lavender.http.panics"].Data.(metricdata.Sum[int64])
		require.True(t, ok)
		require.Len(t, panics.DataPoints, 1)
		assert.EqualValues(t, 1, panics.DataPoints[0].Value)
		assert.Contains(t, panics.DataPoints[0].Attributes.ToSlice(), attribute.String("http.route", "/boom"))
	})

	t.Run("passes http.ErrAbortHandler on to the server", func(t *testing.T) {
		assert.PanicsWithValue(t, http.ErrAbortHandler, func() {
			router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/abort", nil))
		})
	})
}

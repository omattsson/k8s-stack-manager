package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	noopmetric "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// TestHTTPMetrics verifies the HTTPMetrics middleware records the correct
// OTel metrics. Tests are sequential (not parallel) because they modify the
// process-wide global OTel MeterProvider.
func TestHTTPMetrics(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// setupReader installs a fresh ManualReader-backed MeterProvider as the
	// global, and registers cleanup to restore the no-op provider.
	setupReader := func(t *testing.T) *sdkmetric.ManualReader {
		t.Helper()
		reader := sdkmetric.NewManualReader()
		mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
		otel.SetMeterProvider(mp)
		t.Cleanup(func() {
			_ = mp.Shutdown(context.Background())
			otel.SetMeterProvider(noopmetric.NewMeterProvider())
		})
		return reader
	}

	collect := func(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
		t.Helper()
		var rm metricdata.ResourceMetrics
		require.NoError(t, reader.Collect(context.Background(), &rm))
		return rm
	}

	findCounter := func(rm metricdata.ResourceMetrics, name string) (metricdata.Sum[int64], bool) {
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == name {
					if c, ok := m.Data.(metricdata.Sum[int64]); ok {
						return c, true
					}
				}
			}
		}
		return metricdata.Sum[int64]{}, false
	}

	findHistogram := func(rm metricdata.ResourceMetrics, name string) (metricdata.Histogram[float64], bool) {
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == name {
					if h, ok := m.Data.(metricdata.Histogram[float64]); ok {
						return h, true
					}
				}
			}
		}
		return metricdata.Histogram[float64]{}, false
	}

	t.Run("records request counter with correct labels for matched route", func(t *testing.T) {
		reader := setupReader(t)

		router := gin.New()
		router.Use(HTTPMetrics())
		router.GET("/items/:id", func(c *gin.Context) {
			c.Status(http.StatusOK)
		})

		req := httptest.NewRequest(http.MethodGet, "/items/42", nil)
		router.ServeHTTP(httptest.NewRecorder(), req)

		rm := collect(t, reader)
		counter, ok := findCounter(rm, "http.server.request.total")
		require.True(t, ok, "http.server.request.total metric not found")
		require.Len(t, counter.DataPoints, 1)

		dp := counter.DataPoints[0]
		assert.EqualValues(t, 1, dp.Value)

		method, _ := dp.Attributes.Value(attribute.Key("http.request.method"))
		route, _ := dp.Attributes.Value(attribute.Key("http.route"))
		status, _ := dp.Attributes.Value(attribute.Key("http.response.status_code"))

		assert.Equal(t, "GET", method.AsString())
		assert.Equal(t, "/items/:id", route.AsString())
		assert.EqualValues(t, 200, status.AsInt64())
	})

	t.Run("uses <unmatched> for unregistered routes", func(t *testing.T) {
		reader := setupReader(t)

		router := gin.New()
		router.Use(HTTPMetrics())

		req := httptest.NewRequest(http.MethodGet, "/does-not-exist", nil)
		router.ServeHTTP(httptest.NewRecorder(), req)

		rm := collect(t, reader)
		counter, ok := findCounter(rm, "http.server.request.total")
		require.True(t, ok, "http.server.request.total metric not found")
		require.Len(t, counter.DataPoints, 1)

		route, _ := counter.DataPoints[0].Attributes.Value(attribute.Key("http.route"))
		assert.Equal(t, "<unmatched>", route.AsString())
	})

	t.Run("records duration histogram", func(t *testing.T) {
		reader := setupReader(t)

		router := gin.New()
		router.Use(HTTPMetrics())
		router.GET("/ping", func(c *gin.Context) {
			c.Status(http.StatusNoContent)
		})

		req := httptest.NewRequest(http.MethodGet, "/ping", nil)
		router.ServeHTTP(httptest.NewRecorder(), req)

		rm := collect(t, reader)
		hist, ok := findHistogram(rm, "http.server.request.duration")
		require.True(t, ok, "http.server.request.duration metric not found")
		require.Len(t, hist.DataPoints, 1)
		assert.EqualValues(t, 1, hist.DataPoints[0].Count)
		// Second-based buckets: a fast request lands in the first bucket
		// (<= 5 ms), not in a 0-5 s bucket of the millisecond defaults.
		assert.Equal(t, []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}, hist.DataPoints[0].Bounds)
		assert.EqualValues(t, 1, hist.DataPoints[0].BucketCounts[0], "a fast request is in the <= 5 ms bucket")
	})

	t.Run("is a no-op when global provider is no-op", func(t *testing.T) {
		otel.SetMeterProvider(noopmetric.NewMeterProvider())

		router := gin.New()
		assert.NotPanics(t, func() {
			router.Use(HTTPMetrics())
			router.GET("/ok", func(c *gin.Context) { c.Status(http.StatusOK) })
			req := httptest.NewRequest(http.MethodGet, "/ok", nil)
			router.ServeHTTP(httptest.NewRecorder(), req)
		})
	})
}

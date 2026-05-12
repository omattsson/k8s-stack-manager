package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	noopmetric "go.opentelemetry.io/otel/metric/noop"
)

// HTTPMetrics returns a Gin middleware that records HTTP request metrics.
// Records:
//
//	http.server.request.duration (Float64Histogram, unit: "s")
//	http.server.request.total    (Int64Counter, unit: "{request}")
//
// Labels: http.request.method, http.route, http.response.status_code
// Uses c.FullPath() for route label; unmatched routes use "<unmatched>".
// Returns a fast-path pass-through middleware when the global MeterProvider
// is the no-op provider, so there is zero per-request overhead when metrics
// are disabled.
func HTTPMetrics() gin.HandlerFunc {
	mp := otel.GetMeterProvider()
	if _, ok := mp.(noopmetric.MeterProvider); ok {
		return func(c *gin.Context) { c.Next() }
	}

	meter := mp.Meter("http")

	duration, err := meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of HTTP server requests."),
	)
	if err != nil {
		otel.Handle(err)
	}

	total, err := meter.Int64Counter(
		"http.server.request.total",
		metric.WithUnit("{request}"),
		metric.WithDescription("Total number of HTTP server requests."),
	)
	if err != nil {
		otel.Handle(err)
	}

	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		route := c.FullPath()
		if route == "" {
			route = "<unmatched>"
		}

		attrs := metric.WithAttributes(
			attribute.String("http.request.method", c.Request.Method),
			attribute.String("http.route", route),
			attribute.Int("http.response.status_code", c.Writer.Status()),
		)

		elapsed := time.Since(start).Seconds()
		duration.Record(c.Request.Context(), elapsed, attrs)
		total.Add(c.Request.Context(), 1, attrs)
	}
}

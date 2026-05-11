package middleware

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// HTTPMetrics returns a Gin middleware that records HTTP request metrics.
// Records:
//
//	http.server.request.duration (Float64Histogram, unit: "s")
//	http.server.request.total    (Int64Counter, unit: "{request}")
//
// Labels: http.request.method, http.route, http.response.status_code
// Uses c.FullPath() for route label; unmatched routes use "<unmatched>".
// Is a no-op when the global MeterProvider is a no-op provider.
func HTTPMetrics() gin.HandlerFunc {
	meter := otel.GetMeterProvider().Meter("http")

	duration, _ := meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of HTTP server requests."),
	)

	total, _ := meter.Int64Counter(
		"http.server.request.total",
		metric.WithUnit("{request}"),
		metric.WithDescription("Total number of HTTP server requests."),
	)

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
			attribute.String("http.response.status_code", strconv.Itoa(c.Writer.Status())),
		)

		elapsed := time.Since(start).Seconds()
		duration.Record(c.Request.Context(), elapsed, attrs)
		total.Add(c.Request.Context(), 1, attrs)
	}
}

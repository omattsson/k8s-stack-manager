package ttl

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// reaperMeter is the OTel instrumentation scope for the TTL reaper.
var reaperMeter = otel.Meter("ttl_reaper")

// reaperTracer is the OTel tracer scope for the TTL reaper.
var reaperTracer = otel.Tracer("ttl_reaper")

// reaperMetrics holds pre-created metric instruments for the TTL reaper.
type reaperMetrics struct {
	expiredTotal metric.Int64Counter
	reapDuration metric.Float64Histogram
}

var rMetrics reaperMetrics

func init() {
	var err error

	rMetrics.expiredTotal, err = reaperMeter.Int64Counter(
		"ttl.expired_total",
		metric.WithDescription("Total number of expired instances processed by the TTL reaper"),
		metric.WithUnit("{instance}"),
	)
	if err != nil {
		otel.Handle(err)
	}

	rMetrics.reapDuration, err = reaperMeter.Float64Histogram(
		"ttl.reap_duration",
		metric.WithDescription("Duration of TTL reaper cycles"),
		metric.WithUnit("s"),
	)
	if err != nil {
		otel.Handle(err)
	}
}

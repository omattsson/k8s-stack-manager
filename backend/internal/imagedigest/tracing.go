package imagedigest

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Lookup outcomes: a small fixed set for the metric label and span attribute.
const (
	outcomeSuccess    = "success"
	outcomeError      = "error"       // registry error, timeout, not found
	outcomeCancelled  = "cancelled"   // the build was cancelled during the request
	outcomeNotAllowed = "not_allowed" // host not in the allowlist, no request
	outcomeInvalid    = "invalid"     // invalid repository or tag, no request
	outcomeSkipped    = "skipped"     // lookup cap, build budget or dead host, no request
	outcomeFallback   = "fallback"    // a failed lookup rendered the last deployed digest
)

const (
	attrRegistry = "imagedigest.registry"
	attrOutcome  = "imagedigest.outcome"
)

// telemetry holds the OpenTelemetry instruments of the registry lookups.
// Without OpenTelemetry they are no-ops.
type telemetry struct {
	tracer      trace.Tracer
	lookupTotal metric.Int64Counter
	lookupDur   metric.Float64Histogram
}

// defaultTelemetry uses the global providers (set by the telemetry bootstrap;
// the global delegates pick up a provider set after this init).
var defaultTelemetry = newTelemetry(otel.GetTracerProvider(), otel.GetMeterProvider())

func newTelemetry(tp trace.TracerProvider, mp metric.MeterProvider) *telemetry {
	meter := mp.Meter("imagedigest")
	t := &telemetry{tracer: tp.Tracer("imagedigest")}
	var err error
	t.lookupTotal, err = meter.Int64Counter(
		"imagedigest.lookups_total",
		metric.WithDescription("Image digest lookups of the imageDigest template function, by outcome."),
		metric.WithUnit("{lookup}"),
	)
	if err != nil {
		otel.Handle(err)
	}
	t.lookupDur, err = meter.Float64Histogram(
		"imagedigest.lookup_duration",
		metric.WithDescription("Duration of a registry manifest HEAD request of the imageDigest template function."),
		metric.WithUnit("s"),
		// 10 ms to the 10 s lookup timeout.
		metric.WithExplicitBucketBoundaries(0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10),
	)
	if err != nil {
		otel.Handle(err)
	}
	return t
}

// recordOutcome counts a lookup that made no registry request.
func (t *telemetry) recordOutcome(ctx context.Context, outcome string) {
	t.lookupTotal.Add(ctx, 1, metric.WithAttributes(attribute.String(attrOutcome, outcome)))
}

// startLookupSpan opens a client span around one registry request. The
// caller calls finish exactly once, with an error that is already redacted
// (Resolver.redactErr): the span records it as is.
func (t *telemetry) startLookupSpan(ctx context.Context, registry string) (context.Context, func(outcome string, redacted error)) {
	ctx, span := t.tracer.Start(ctx, "imagedigest.head",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String(attrRegistry, registry)),
	)
	start := time.Now()
	return ctx, func(outcome string, redacted error) {
		span.SetAttributes(attribute.String(attrOutcome, outcome))
		if redacted != nil {
			span.RecordError(redacted)
			span.SetStatus(codes.Error, outcome)
		} else {
			span.SetStatus(codes.Ok, "")
		}
		span.End()
		attrs := metric.WithAttributes(attribute.String(attrOutcome, outcome))
		t.lookupTotal.Add(ctx, 1, attrs)
		t.lookupDur.Record(ctx, time.Since(start).Seconds(), attrs)
	}
}

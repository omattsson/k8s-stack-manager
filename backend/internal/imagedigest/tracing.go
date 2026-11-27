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

// digestTracer and digestMeter are the OpenTelemetry scopes of the registry
// lookups. Without OpenTelemetry they are no-ops.
var (
	digestTracer = otel.Tracer("imagedigest")
	digestMeter  = otel.Meter("imagedigest")
)

// Lookup outcomes: a small fixed set for the metric label and span attribute.
const (
	outcomeSuccess    = "success"
	outcomeError      = "error"       // registry error, timeout, not found
	outcomeNotAllowed = "not_allowed" // host not in the allowlist, no request
	outcomeInvalid    = "invalid"     // invalid repository or tag, no request
	outcomeSkipped    = "skipped"     // lookup cap, build budget or dead host, no request
)

const (
	attrRegistry = "imagedigest.registry"
	attrOutcome  = "imagedigest.outcome"
)

var digestMetrics struct {
	lookupTotal metric.Int64Counter
	lookupDur   metric.Float64Histogram
}

func init() {
	var err error
	digestMetrics.lookupTotal, err = digestMeter.Int64Counter(
		"imagedigest.lookups_total",
		metric.WithDescription("Image digest lookups of the imageDigest template function, by outcome."),
		metric.WithUnit("{lookup}"),
	)
	if err != nil {
		otel.Handle(err)
	}
	digestMetrics.lookupDur, err = digestMeter.Float64Histogram(
		"imagedigest.lookup_duration",
		metric.WithDescription("Duration of a registry manifest HEAD request of the imageDigest template function."),
		metric.WithUnit("s"),
		// 10 ms to the 10 s lookup timeout.
		metric.WithExplicitBucketBoundaries(0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10),
	)
	if err != nil {
		otel.Handle(err)
	}
}

// recordOutcome counts a lookup that made no registry request.
func recordOutcome(ctx context.Context, outcome string) {
	digestMetrics.lookupTotal.Add(ctx, 1, metric.WithAttributes(attribute.String(attrOutcome, outcome)))
}

// startLookupSpan opens a client span around one registry request. The
// caller calls finish exactly once.
func startLookupSpan(ctx context.Context, registry string) (context.Context, func(outcome string, err error)) {
	ctx, span := digestTracer.Start(ctx, "imagedigest.head",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String(attrRegistry, registry)),
	)
	start := time.Now()
	return ctx, func(outcome string, err error) {
		span.SetAttributes(attribute.String(attrOutcome, outcome))
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, outcome)
		} else {
			span.SetStatus(codes.Ok, "")
		}
		span.End()
		attrs := metric.WithAttributes(attribute.String(attrOutcome, outcome))
		digestMetrics.lookupTotal.Add(ctx, 1, attrs)
		digestMetrics.lookupDur.Record(ctx, time.Since(start).Seconds(), attrs)
	}
}

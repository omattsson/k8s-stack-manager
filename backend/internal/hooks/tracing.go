package hooks

import (
	"context"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// hooksTracer and hooksMeter are the OTel instrumentation scopes for hook
// dispatch and action invocation. When OpenTelemetry is disabled these
// resolve to no-op implementations, so callers pay zero runtime cost.
var (
	hooksTracer = otel.Tracer("hooks")
	hooksMeter  = otel.Meter("hooks")
)

// hooksMetrics holds pre-created metric instruments. Instruments are created
// once at init and reused for every dispatch.
type hooksMetrics struct {
	dispatchTotal     metric.Int64Counter
	dispatchDuration  metric.Float64Histogram
	actionTotal       metric.Int64Counter
	actionDuration    metric.Float64Histogram
}

var hmetrics hooksMetrics

// Outcome labels surfaced on metrics + span attributes. A stable, small set
// so dashboards can enumerate them without cardinality worries.
const (
	outcomeSuccess        = "success"
	outcomeDenied         = "denied"          // subscriber returned Allowed:false
	outcomeHTTPError      = "http_error"      // non-2xx from subscriber
	outcomeTransportError = "transport_error" // connection refused, DNS, etc.
	outcomeTimeout        = "timeout"         // ctx deadline exceeded
	outcomeUnknownAction  = "unknown_action"  // action name not registered
	outcomeMarshalError   = "marshal_error"   // envelope serialisation failed
)

// Span attribute + metric label keys. Kept in one place so producers and
// consumers (dashboards, alert rules) can agree on names.
const (
	attrEvent        = "hook.event"
	attrSubscription = "hook.subscription"
	attrAction       = "hook.action"
	attrOutcome      = "hook.outcome"
	attrRequestID    = "hook.request_id"
	attrStatusCode   = "hook.status_code"
)

func init() {
	var err error

	hmetrics.dispatchTotal, err = hooksMeter.Int64Counter(
		"hook.dispatches_total",
		metric.WithDescription("Total number of hook dispatches to subscriber URLs, by event / subscription / outcome"),
		metric.WithUnit("{dispatch}"),
	)
	if err != nil {
		otel.Handle(err)
	}

	hmetrics.dispatchDuration, err = hooksMeter.Float64Histogram(
		"hook.dispatch_duration",
		metric.WithDescription("Per-subscriber dispatch duration (POST + wait for response)"),
		metric.WithUnit("s"),
	)
	if err != nil {
		otel.Handle(err)
	}

	hmetrics.actionTotal, err = hooksMeter.Int64Counter(
		"hook.action_invocations_total",
		metric.WithDescription("Total action-endpoint invocations, by action / outcome"),
		metric.WithUnit("{invocation}"),
	)
	if err != nil {
		otel.Handle(err)
	}

	hmetrics.actionDuration, err = hooksMeter.Float64Histogram(
		"hook.action_invocation_duration",
		metric.WithDescription("Action-endpoint invocation duration (POST + wait for response)"),
		metric.WithUnit("s"),
	)
	if err != nil {
		otel.Handle(err)
	}
}

// startDispatchSpan opens a span around a single subscriber dispatch and
// returns a finish function that records duration + outcome metrics.
// Caller MUST call finish exactly once with the resolved outcome.
func startDispatchSpan(ctx context.Context, event, subscription, requestID string) (context.Context, trace.Span, func(outcome string, err error, statusCode int)) {
	ctx, span := hooksTracer.Start(ctx, "hooks.dispatch",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String(attrEvent, event),
			attribute.String(attrSubscription, subscription),
			attribute.String(attrRequestID, requestID),
		),
	)
	start := time.Now()

	finish := func(outcome string, err error, statusCode int) {
		duration := time.Since(start).Seconds()

		span.SetAttributes(attribute.String(attrOutcome, outcome))
		if statusCode > 0 {
			span.SetAttributes(attribute.Int(attrStatusCode, statusCode))
		}
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		} else if outcome != outcomeSuccess {
			span.SetStatus(codes.Error, outcome)
		} else {
			span.SetStatus(codes.Ok, "")
		}

		attrs := metric.WithAttributes(
			attribute.String(attrEvent, event),
			attribute.String(attrSubscription, subscription),
			attribute.String(attrOutcome, outcome),
		)
		hmetrics.dispatchTotal.Add(ctx, 1, attrs)
		hmetrics.dispatchDuration.Record(ctx, duration, metric.WithAttributes(
			attribute.String(attrEvent, event),
			attribute.String(attrSubscription, subscription),
		))

		span.End()
	}

	return ctx, span, finish
}

// startActionSpan opens a span around an action-endpoint invocation.
// Caller MUST call finish exactly once with the resolved outcome.
func startActionSpan(ctx context.Context, action, requestID string) (context.Context, trace.Span, func(outcome string, err error, statusCode int)) {
	ctx, span := hooksTracer.Start(ctx, "hooks.action",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String(attrAction, action),
			attribute.String(attrRequestID, requestID),
		),
	)
	start := time.Now()

	finish := func(outcome string, err error, statusCode int) {
		duration := time.Since(start).Seconds()

		span.SetAttributes(attribute.String(attrOutcome, outcome))
		if statusCode > 0 {
			span.SetAttributes(attribute.Int(attrStatusCode, statusCode))
		}
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		} else if outcome != outcomeSuccess {
			span.SetStatus(codes.Error, outcome)
		} else {
			span.SetStatus(codes.Ok, "")
		}

		attrs := metric.WithAttributes(
			attribute.String(attrAction, action),
			attribute.String(attrOutcome, outcome),
		)
		hmetrics.actionTotal.Add(ctx, 1, attrs)
		hmetrics.actionDuration.Record(ctx, duration, metric.WithAttributes(
			attribute.String(attrAction, action),
		))

		span.End()
	}

	return ctx, span, finish
}

// injectTraceContext adds W3C traceparent (and baggage, per the global
// propagator) headers to the outbound request so the subscriber can
// stitch its span as a child of ours.
func injectTraceContext(ctx context.Context, req *http.Request) {
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
}

// classifyErr maps a deliver()/Invoke() error into a stable outcome label.
// Used by tests and by the dispatcher to keep labels uniform.
func classifyErr(err error) string {
	if err == nil {
		return outcomeSuccess
	}
	s := err.Error()
	switch {
	case containsAny(s, "context deadline exceeded", "Client.Timeout exceeded"):
		return outcomeTimeout
	case containsAny(s, "hook returned status", "action returned status"):
		return outcomeHTTPError
	default:
		return outcomeTransportError
	}
}

// containsAny is a tiny helper to avoid importing strings in a hot path.
// Returns true when any needle appears in haystack.
func containsAny(haystack string, needles ...string) bool {
	for _, n := range needles {
		if len(n) == 0 {
			continue
		}
		for i := 0; i+len(n) <= len(haystack); i++ {
			if haystack[i:i+len(n)] == n {
				return true
			}
		}
	}
	return false
}

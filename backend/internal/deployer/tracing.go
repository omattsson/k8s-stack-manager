// Package deployer tracing provides OpenTelemetry instrumentation for deployment operations.
package deployer

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// deployerTracer and deployerMeter are the OTel instrumentation scopes for the deployer package.
// When OTel is disabled these resolve to no-op implementations.
var (
	deployerTracer = otel.Tracer("deployer")
	deployerMeter  = otel.Meter("deployer")
)

// deployerMetrics holds pre-created metric instruments for the deployer.
// Instruments are created once at init time and reused for every operation.
type deployerMetrics struct {
	deploymentTotal    metric.Int64Counter
	deploymentDuration metric.Float64Histogram
	deploymentActive   metric.Int64UpDownCounter
}

// metrics is the singleton metrics instance, initialised at package init.
var metrics deployerMetrics

func init() {
	var err error

	metrics.deploymentTotal, err = deployerMeter.Int64Counter(
		"deployment.total",
		metric.WithDescription("Total number of deployment operations"),
		metric.WithUnit("{operation}"),
	)
	if err != nil {
		// OTel errors during metric registration are non-fatal; the instrument
		// will be a no-op.
		otel.Handle(err)
	}

	metrics.deploymentDuration, err = deployerMeter.Float64Histogram(
		"deployment.duration",
		metric.WithDescription("Duration of deployment operations"),
		metric.WithUnit("s"),
	)
	if err != nil {
		otel.Handle(err)
	}

	metrics.deploymentActive, err = deployerMeter.Int64UpDownCounter(
		"deployment.active",
		metric.WithDescription("Number of currently active deployment operations"),
		metric.WithUnit("{operation}"),
	)
	if err != nil {
		otel.Handle(err)
	}
}

// startDeploySpan creates a new span for a deployment operation and returns
// context, span, and a finish function that records duration, status, and
// metrics. The caller MUST call the returned finish function (typically via
// defer) with the error result.
func startDeploySpan(ctx context.Context, spanName string, attrs ...attribute.KeyValue) (context.Context, trace.Span, func(error)) {
	ctx, span := deployerTracer.Start(ctx, spanName,
		trace.WithAttributes(attrs...),
	)
	start := time.Now()

	// Extract the action label from the span name for metrics (e.g. "deployer.deploy" -> "deploy").
	action := spanName
	if len(spanName) > len("deployer.") {
		action = spanName[len("deployer."):]
	}

	actionAttr := attribute.String("action", action)

	metrics.deploymentActive.Add(ctx, 1, metric.WithAttributes(actionAttr))

	finish := func(err error) {
		duration := time.Since(start).Seconds()
		metrics.deploymentActive.Add(ctx, -1, metric.WithAttributes(actionAttr))

		statusAttr := attribute.String("status", "success")
		if err != nil {
			statusAttr = attribute.String("status", "failure")
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		} else {
			span.SetStatus(codes.Ok, "")
		}

		metrics.deploymentTotal.Add(ctx, 1,
			metric.WithAttributes(actionAttr, statusAttr),
		)
		metrics.deploymentDuration.Record(ctx, duration,
			metric.WithAttributes(actionAttr),
		)

		span.End()
	}

	return ctx, span, finish
}

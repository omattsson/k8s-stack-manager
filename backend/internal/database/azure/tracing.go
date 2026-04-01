package azure

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// azureTracer and azureMeter are the OTel instrumentation scopes for Azure Table operations.
// When OTel is disabled these resolve to no-op implementations.
var (
	azureTracer = otel.Tracer("azure_table")
	azureMeter  = otel.Meter("azure_table")
)

// azureTableMetrics holds pre-created metric instruments for Azure Table operations.
type azureTableMetrics struct {
	operationDuration metric.Float64Histogram
	operationCount    metric.Int64Counter
}

var dbMetrics azureTableMetrics

func init() {
	var err error

	dbMetrics.operationDuration, err = azureMeter.Float64Histogram(
		"db.operation.duration",
		metric.WithDescription("Duration of Azure Table Storage operations"),
		metric.WithUnit("s"),
	)
	if err != nil {
		otel.Handle(err)
	}

	dbMetrics.operationCount, err = azureMeter.Int64Counter(
		"db.operation.count",
		metric.WithDescription("Total number of Azure Table Storage operations"),
		metric.WithUnit("{operation}"),
	)
	if err != nil {
		otel.Handle(err)
	}
}

// startDBSpan creates a new span for an Azure Table Storage operation and
// returns the enriched context, the span, and a finish function. The caller
// MUST call the returned finish function (typically via defer) with the error
// result to record duration, status, and metrics.
func startDBSpan(ctx context.Context, operation, table string) (context.Context, trace.Span, func(error)) {
	ctx, span := azureTracer.Start(ctx, "azure_table."+operation,
		trace.WithAttributes(
			attribute.String("db.system", "azure_table"),
			attribute.String("db.collection.name", table),
			attribute.String("db.operation.name", operation),
		),
	)
	start := time.Now()

	opAttr := attribute.String("operation", operation)
	tableAttr := attribute.String("table", table)

	finish := func(err error) {
		duration := time.Since(start).Seconds()

		statusAttr := attribute.String("status", "success")
		if err != nil {
			statusAttr = attribute.String("status", "error")
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		} else {
			span.SetStatus(codes.Ok, "")
		}

		dbMetrics.operationDuration.Record(ctx, duration,
			metric.WithAttributes(opAttr, tableAttr),
		)
		dbMetrics.operationCount.Add(ctx, 1,
			metric.WithAttributes(opAttr, tableAttr, statusAttr),
		)

		span.End()
	}

	return ctx, span, finish
}

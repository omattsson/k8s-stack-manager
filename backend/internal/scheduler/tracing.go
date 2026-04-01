package scheduler

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// schedulerTracer is the OTel tracer scope for the cleanup scheduler.
var schedulerTracer = otel.Tracer("cleanup_scheduler")

// schedulerMeter is the OTel meter scope for the cleanup scheduler.
var schedulerMeter = otel.Meter("cleanup_scheduler")

// schedulerMetrics holds pre-created metric instruments for the cleanup scheduler.
type schedulerMetrics struct {
	executionsTotal metric.Int64Counter
}

var sMetrics schedulerMetrics

func init() {
	var err error

	sMetrics.executionsTotal, err = schedulerMeter.Int64Counter(
		"cleanup.executions_total",
		metric.WithDescription("Total number of cleanup policy executions"),
		metric.WithUnit("{execution}"),
	)
	if err != nil {
		otel.Handle(err)
	}
}

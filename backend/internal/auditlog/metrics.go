package auditlog

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// writeFailed counts audit entries that the database did not store, by action (#789).
var writeFailed metric.Int64Counter

func init() {
	var err error
	writeFailed, err = otel.Meter("auditlog").Int64Counter(
		"audit.write_failed_total",
		metric.WithDescription("Audit entries that could not be stored, by action"),
		metric.WithUnit("{entry}"),
	)
	if err != nil {
		otel.Handle(err)
	}
}

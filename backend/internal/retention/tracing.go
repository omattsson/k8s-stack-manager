package retention

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// retentionMeter is the OTel meter scope of the log retention.
var retentionMeter = otel.Meter("retention")

// deletedRows counts the rows that log-retention deleted, by table.
var deletedRows metric.Int64Counter

func init() {
	var err error
	deletedRows, err = retentionMeter.Int64Counter(
		"log_retention.deleted_rows_total",
		metric.WithDescription("Rows that the log retention deleted, by table"),
		metric.WithUnit("{row}"),
	)
	if err != nil {
		otel.Handle(err)
	}
}

// recordDeleted adds n deleted rows of table to the counter.
func recordDeleted(table string, n int64) {
	if n <= 0 || deletedRows == nil {
		return
	}
	deletedRows.Add(context.Background(), n, metric.WithAttributes(attribute.String("table", table)))
}

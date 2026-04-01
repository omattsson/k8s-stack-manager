package telemetry

import (
	"context"
	"database/sql"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// StartDBMetrics registers asynchronous OTel instruments that expose
// database/sql connection-pool statistics on every collection cycle.
// The caller must pass the *sql.DB obtained from GORM (gormDB.DB()).
func StartDBMetrics(db *sql.DB) error {
	meter := otel.Meter("db.pool")

	maxOpen, err := meter.Int64ObservableGauge("db.pool.max_open",
		metric.WithDescription("Maximum number of open connections to the database"))
	if err != nil {
		return err
	}

	open, err := meter.Int64ObservableGauge("db.pool.open",
		metric.WithDescription("Number of established connections (in-use + idle)"))
	if err != nil {
		return err
	}

	inUse, err := meter.Int64ObservableGauge("db.pool.in_use",
		metric.WithDescription("Number of connections currently in use"))
	if err != nil {
		return err
	}

	idle, err := meter.Int64ObservableGauge("db.pool.idle",
		metric.WithDescription("Number of idle connections"))
	if err != nil {
		return err
	}

	waitCount, err := meter.Int64ObservableCounter("db.pool.wait_count",
		metric.WithDescription("Total number of connections waited for"))
	if err != nil {
		return err
	}

	waitDuration, err := meter.Float64ObservableCounter("db.pool.wait_duration_ms",
		metric.WithDescription("Total time blocked waiting for a new connection, in milliseconds"))
	if err != nil {
		return err
	}

	maxIdleClosed, err := meter.Int64ObservableCounter("db.pool.max_idle_closed",
		metric.WithDescription("Total connections closed due to SetMaxIdleConns"))
	if err != nil {
		return err
	}

	maxLifetimeClosed, err := meter.Int64ObservableCounter("db.pool.max_lifetime_closed",
		metric.WithDescription("Total connections closed due to SetConnMaxLifetime"))
	if err != nil {
		return err
	}

	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		stats := db.Stats()
		o.ObserveInt64(maxOpen, int64(stats.MaxOpenConnections))
		o.ObserveInt64(open, int64(stats.OpenConnections))
		o.ObserveInt64(inUse, int64(stats.InUse))
		o.ObserveInt64(idle, int64(stats.Idle))
		o.ObserveInt64(waitCount, stats.WaitCount)
		o.ObserveFloat64(waitDuration, float64(stats.WaitDuration.Milliseconds()))
		o.ObserveInt64(maxIdleClosed, stats.MaxIdleClosed)
		o.ObserveInt64(maxLifetimeClosed, stats.MaxLifetimeClosed)
		return nil
	}, maxOpen, open, inUse, idle, waitCount, waitDuration, maxIdleClosed, maxLifetimeClosed)
	if err != nil {
		return err
	}

	slog.Info("Registered database/sql connection pool metrics")
	return nil
}

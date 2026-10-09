package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// LeaderMetricName is the OpenTelemetry name of the leader gauge. The
// Prometheus exporter exposes it as stackmanager_leader.
const LeaderMetricName = "stackmanager.leader"

// StartLeaderMetric registers the gauge stackmanager_leader: 1 when this
// replica runs the leader-only workers, 0 otherwise. isLeader is called on
// each collection.
func StartLeaderMetric(isLeader func() bool) error {
	meter := otel.Meter("leader")
	gauge, err := meter.Int64ObservableGauge(
		LeaderMetricName,
		metric.WithDescription("1 when this replica is the leader and runs the background workers, 0 otherwise."),
	)
	if err != nil {
		return err
	}
	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		var v int64
		if isLeader() {
			v = 1
		}
		o.ObserveInt64(gauge, v)
		return nil
	}, gauge)
	return err
}

package cluster

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var clusterMeter = otel.Meter("cluster")

var clusterMetrics struct {
	healthCheckDuration metric.Float64Histogram
	healthTransitions   metric.Int64Counter
	secretRefreshTotal  metric.Int64Counter
	secretRefreshDur    metric.Float64Histogram
}

func init() {
	initClusterMetrics()
}

func initClusterMetrics() {
	var err error

	clusterMetrics.healthCheckDuration, err = clusterMeter.Float64Histogram(
		"cluster.health.check.duration",
		metric.WithDescription("Health check latency for each cluster."),
		metric.WithUnit("s"),
		// Cluster health checks: 10 ms to 30 s. The OpenTelemetry default buckets (0 to 10000) are for
		// milliseconds: in seconds every value fell into the first bucket.
		metric.WithExplicitBucketBoundaries(0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30),
	)
	if err != nil {
		otel.Handle(err)
	}

	clusterMetrics.healthTransitions, err = clusterMeter.Int64Counter(
		"cluster.health.transitions.total",
		metric.WithDescription("Total cluster status transitions."),
		metric.WithUnit("{transition}"),
	)
	if err != nil {
		otel.Handle(err)
	}

	clusterMetrics.secretRefreshTotal, err = clusterMeter.Int64Counter(
		"cluster.secret.refresh.total",
		// One event per refresh cycle (not per namespace), classified
		// success/partial/failure, matching cluster.secret.refresh.duration.
		metric.WithDescription("Total image pull secret refresh cycles by outcome."),
		metric.WithUnit("{cycle}"),
	)
	if err != nil {
		otel.Handle(err)
	}

	clusterMetrics.secretRefreshDur, err = clusterMeter.Float64Histogram(
		"cluster.secret.refresh.duration",
		metric.WithDescription("Duration of image pull secret refresh cycles."),
		metric.WithUnit("s"),
		// Pull secret refresh cycles: 100 ms to 5 min. The OpenTelemetry default buckets (0 to 10000) are for
		// milliseconds: in seconds every value fell into the first bucket.
		metric.WithExplicitBucketBoundaries(0.1, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300),
	)
	if err != nil {
		otel.Handle(err)
	}
}

func recordClusterHealthCheck(clusterName, status string, duration time.Duration) {
	if clusterName == "" {
		return
	}
	clusterMetrics.healthCheckDuration.Record(context.Background(), duration.Seconds(),
		metric.WithAttributes(
			attribute.String("cluster_name", clusterName),
			attribute.String("status", status),
		),
	)
}

func recordClusterTransition(clusterName, fromStatus, toStatus string) {
	if clusterName == "" || fromStatus == "" || toStatus == "" {
		return
	}
	clusterMetrics.healthTransitions.Add(context.Background(), 1,
		metric.WithAttributes(
			attribute.String("cluster_name", clusterName),
			attribute.String("from_status", fromStatus),
			attribute.String("to_status", toStatus),
		),
	)
}

func recordSecretRefreshResult(status string, duration time.Duration) {
	if status == "" {
		return
	}
	clusterMetrics.secretRefreshTotal.Add(context.Background(), 1,
		metric.WithAttributes(
			attribute.String("status", status),
		),
	)
	clusterMetrics.secretRefreshDur.Record(context.Background(), duration.Seconds(),
		metric.WithAttributes(
			attribute.String("status", status),
		),
	)
}

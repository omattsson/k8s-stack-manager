package cluster

import (
	"context"
	"errors"
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// swapClusterMeter installs a manual-reader meter provider and rebinds the
// package cluster metrics to it, returning the reader. Cleanup restores the
// previous meter and provider.
func swapClusterMeter(t *testing.T) sdkmetric.Reader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	prevMeter := clusterMeter
	clusterMeter = mp.Meter("cluster")
	initClusterMetrics()
	t.Cleanup(func() {
		clusterMeter = prevMeter
		otel.SetMeterProvider(prev)
		_ = mp.Shutdown(context.Background())
	})
	return reader
}

func transitionCount(t *testing.T, reader sdkmetric.Reader, from, to string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	var total int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "cluster.health.transitions.total" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			require.True(t, ok)
			for _, dp := range sum.DataPoints {
				f, _ := dp.Attributes.Value(attribute.Key("from_status"))
				to2, _ := dp.Attributes.Value(attribute.Key("to_status"))
				if f.AsString() == from && to2.AsString() == to {
					total += dp.Value
				}
			}
		}
	}
	return total
}

func healthCheckDurationCount(t *testing.T, reader sdkmetric.Reader) int {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	count := 0
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "cluster.health.check.duration" {
				continue
			}
			hist, ok := m.Data.(metricdata.Histogram[float64])
			require.True(t, ok)
			count += len(hist.DataPoints)
		}
	}
	return count
}

func secretRefreshCount(t *testing.T, reader sdkmetric.Reader, status string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	var total int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "cluster.secret.refresh.total" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			require.True(t, ok)
			for _, dp := range sum.DataPoints {
				s, _ := dp.Attributes.Value(attribute.Key("status"))
				if s.AsString() == status {
					total += dp.Value
				}
			}
		}
	}
	return total
}

func newPollerClusterRepo(status string) *mockClusterRepo {
	repo := newMockClusterRepo()
	repo.clusters["c1"] = &models.Cluster{
		ID:             "c1",
		Name:           "Cluster 1",
		KubeconfigPath: "/fake/path",
		HealthStatus:   status,
	}
	return repo
}

func TestHealthPollerPoll_RecordsTransitionAfterPersist(t *testing.T) {
	reader := swapClusterMeter(t)

	repo := newPollerClusterRepo(models.ClusterHealthy)
	poller := NewHealthPoller(HealthPollerConfig{
		ClusterRepo: repo,
		Registry:    healthPollerFailingRegistry(repo),
		Hub:         &mockBroadcastSender{},
	})

	poller.poll()

	assert.Equal(t, int64(1), transitionCount(t, reader, models.ClusterHealthy, models.ClusterUnreachable),
		"a persisted healthy->unreachable change must record one transition")
	assert.GreaterOrEqual(t, healthCheckDurationCount(t, reader), 1, "the health check duration must be recorded")
}

func TestHealthPollerPoll_NoTransitionWhenUpdateFails(t *testing.T) {
	reader := swapClusterMeter(t)

	repo := newPollerClusterRepo(models.ClusterHealthy)
	repo.updateErr = errors.New("db down")
	poller := NewHealthPoller(HealthPollerConfig{
		ClusterRepo: repo,
		Registry:    healthPollerFailingRegistry(repo),
		Hub:         &mockBroadcastSender{},
	})

	poller.poll()

	assert.Equal(t, int64(0), transitionCount(t, reader, models.ClusterHealthy, models.ClusterUnreachable),
		"a failed persist must not record a transition that never took effect")
	assert.GreaterOrEqual(t, healthCheckDurationCount(t, reader), 1, "the health check still runs and is timed")
}

func TestHealthPollerPoll_NoTransitionWhenUnchanged(t *testing.T) {
	reader := swapClusterMeter(t)

	repo := newPollerClusterRepo(models.ClusterHealthy)
	poller := NewHealthPoller(HealthPollerConfig{
		ClusterRepo: repo,
		Registry:    healthPollerTestRegistry(repo), // stays healthy
		Hub:         &mockBroadcastSender{},
	})

	poller.poll()

	assert.Equal(t, int64(0), transitionCount(t, reader, models.ClusterHealthy, models.ClusterUnreachable),
		"an unchanged status must not record a transition")
	assert.GreaterOrEqual(t, healthCheckDurationCount(t, reader), 1, "the health check duration is recorded every cycle")
}

func TestSecretRefresher_CycleMetrics(t *testing.T) {
	t.Run("success on a no-op cycle", func(t *testing.T) {
		reader := swapClusterMeter(t)

		repo := newMockClusterRepo()
		// A cluster with no registry configured is skipped, so the cycle does
		// no work and must be reported as success.
		repo.clusters["c1"] = &models.Cluster{ID: "c1", Name: "Cluster 1"}
		r := NewSecretRefresher(SecretRefresherConfig{
			ClusterRepo:  repo,
			InstanceRepo: &mockInstanceRepo{},
			Registry:     &Registry{},
		})

		r.refresh()

		assert.Equal(t, int64(1), secretRefreshCount(t, reader, "success"))
		assert.Equal(t, int64(0), secretRefreshCount(t, reader, "failure"))
	})

	t.Run("failure when listing clusters fails", func(t *testing.T) {
		reader := swapClusterMeter(t)

		repo := newMockClusterRepo()
		repo.listErr = errors.New("db down")
		r := NewSecretRefresher(SecretRefresherConfig{
			ClusterRepo:  repo,
			InstanceRepo: &mockInstanceRepo{},
			Registry:     &Registry{},
		})

		r.refresh()

		assert.Equal(t, int64(1), secretRefreshCount(t, reader, "failure"))
		assert.Equal(t, int64(0), secretRefreshCount(t, reader, "success"))
	})
}

func TestSecretRefreshStatus(t *testing.T) {
	tests := []struct {
		name              string
		refreshed, failed int
		want              string
	}{
		{"no-op cycle is success", 0, 0, "success"},
		{"all succeeded is success", 3, 0, "success"},
		{"all failed is failure", 0, 2, "failure"},
		{"some failed is partial", 2, 1, "partial"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, secretRefreshStatus(tt.refreshed, tt.failed))
		})
	}
}

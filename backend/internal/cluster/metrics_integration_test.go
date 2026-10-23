package cluster

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/k8s"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
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

// healthCheckSampleCount returns the number of health check duration samples
// with the given status.
func healthCheckSampleCount(t *testing.T, reader sdkmetric.Reader, status string) uint64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	var total uint64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "cluster.health.check.duration" {
				continue
			}
			hist, ok := m.Data.(metricdata.Histogram[float64])
			require.True(t, ok)
			for _, dp := range hist.DataPoints {
				s, _ := dp.Attributes.Value(attribute.Key("status"))
				if s.AsString() == status {
					total += dp.Count
				}
			}
		}
	}
	return total
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

	poller.poll(context.Background())

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

	poller.poll(context.Background())

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

	poller.poll(context.Background())

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

		r.refresh(context.Background())

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

		r.refresh(context.Background())

		assert.Equal(t, int64(1), secretRefreshCount(t, reader, "failure"))
		assert.Equal(t, int64(0), secretRefreshCount(t, reader, "success"))
	})

	t.Run("no result when the cycle is cancelled", func(t *testing.T) {
		reader := swapClusterMeter(t)
		r := NewSecretRefresher(SecretRefresherConfig{
			ClusterRepo:  newMockClusterRepo(),
			InstanceRepo: &mockInstanceRepo{},
			Registry:     &Registry{},
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		r.refresh(ctx)

		assert.Equal(t, int64(0), secretRefreshCount(t, reader, "failure"), "a leader change is not a failed refresh")
		assert.Equal(t, int64(0), secretRefreshCount(t, reader, "success"))
	})
}

// logCapture collects the records of the default slog logger.
type logCapture struct {
	mu      sync.Mutex
	records []slog.Record
}

func (c *logCapture) Enabled(context.Context, slog.Level) bool { return true }
func (c *logCapture) WithAttrs([]slog.Attr) slog.Handler       { return c }
func (c *logCapture) WithGroup(string) slog.Handler            { return c }
func (c *logCapture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, r)
	return nil
}

// withPrefix returns the records whose message starts with prefix.
func (c *logCapture) withPrefix(prefix string) []slog.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []slog.Record
	for _, r := range c.records {
		if strings.HasPrefix(r.Message, prefix) {
			out = append(out, r)
		}
	}
	return out
}

// captureSlog makes the default slog logger write to a capture until the
// test ends. Do not use it in a parallel test.
func captureSlog(t *testing.T) *logCapture {
	t.Helper()
	c := &logCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(c))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return c
}

// TestWorkers_CancelledCycleLogsAtDebug checks that a cycle whose worker
// context ends (leader change, shutdown) logs at debug only, not as a failed
// cluster read. The readiness check still fails, with a debug line. A real
// list error with a live context keeps its error (or warn) line.
func TestWorkers_CancelledCycleLogsAtDebug(t *testing.T) {
	workers := []struct {
		run       func(ctx context.Context, repo *mockClusterRepo)
		name      string
		prefix    string
		liveLevel slog.Level
	}{
		{name: "secret refresher", prefix: "secret refresher:", liveLevel: slog.LevelError, run: func(ctx context.Context, repo *mockClusterRepo) {
			NewSecretRefresher(SecretRefresherConfig{ClusterRepo: repo, InstanceRepo: &mockInstanceRepo{}, Registry: &Registry{}}).refresh(ctx)
		}},
		{name: "health poller", prefix: "health poller:", liveLevel: slog.LevelError, run: func(ctx context.Context, repo *mockClusterRepo) {
			NewHealthPoller(HealthPollerConfig{ClusterRepo: repo, Registry: healthPollerTestRegistry(repo)}).poll(ctx)
		}},
		{name: "quota monitor", prefix: "quota monitor:", liveLevel: slog.LevelError, run: func(ctx context.Context, repo *mockClusterRepo) {
			NewQuotaMonitor(QuotaMonitorConfig{
				ClusterRepo: repo, InstanceRepo: &mockInstanceRepo{},
				QuotaRepo: &mockQuotaRepo{configs: map[string]*models.ResourceQuotaConfig{}}, Notifier: &mockQuotaNotifier{},
			}).check(ctx)
		}},
		{name: "secret monitor", prefix: "secret monitor:", liveLevel: slog.LevelError, run: func(ctx context.Context, repo *mockClusterRepo) {
			NewSecretMonitor(SecretMonitorConfig{ClusterRepo: repo, InstanceRepo: &mockInstanceRepo{}, Notifier: &mockSecretNotifier{}}).scan(ctx)
		}},
		{name: "access reconciler", prefix: "access reconcile:", liveLevel: slog.LevelWarn, run: func(ctx context.Context, repo *mockClusterRepo) {
			NewAccessReconciler(repo, healthPollerTestRegistry(repo), time.Hour).reconcile(ctx)
		}},
		{name: "readiness check", prefix: "health check:", liveLevel: slog.LevelError, run: func(ctx context.Context, repo *mockClusterRepo) {
			assert.Error(t, newTestRegistry(repo).HealthCheck(ctx), "the check still fails")
		}},
	}
	for _, w := range workers {
		for _, cancelled := range []bool{true, false} {
			name := w.name + "/live list error"
			if cancelled {
				name = w.name + "/cancelled"
			}
			t.Run(name, func(t *testing.T) {
				logs := captureSlog(t)
				repo := newMockClusterRepo()
				repo.clusters["c1"] = &models.Cluster{ID: "c1", Name: "dev", KubeconfigPath: "/fake"}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if cancelled {
					cancel()
				} else {
					repo.listErr = errors.New("db down")
				}

				w.run(ctx, repo)

				recs := logs.withPrefix(w.prefix)
				require.NotEmpty(t, recs, "the failure is logged")
				if cancelled {
					for _, r := range recs {
						assert.Equal(t, slog.LevelDebug, r.Level, "%q", r.Message)
					}
					return
				}
				levels := make([]slog.Level, 0, len(recs))
				for _, r := range recs {
					levels = append(levels, r.Level)
				}
				assert.Contains(t, levels, w.liveLevel, "a real list error keeps its level")
			})
		}
	}
}

// TestWorkers_InstanceReadCancel checks the instance read inside a cycle: a
// cancel there logs at debug and records no failure; a real error with a live
// context keeps the error line (and the failure metric of the refresher).
func TestWorkers_InstanceReadCancel(t *testing.T) {
	type testCase struct {
		run         func(ctx context.Context, instances *mockInstanceRepo)
		name        string
		prefix      string
		wantFailure int64
		cancelled   bool
	}
	var tests []testCase
	refresher := func(ctx context.Context, instances *mockInstanceRepo) {
		repo := newMockClusterRepo()
		repo.clusters["c1"] = &models.Cluster{ID: "c1", Name: "dev", RegistryURL: "registry.example.com"}
		NewSecretRefresher(SecretRefresherConfig{ClusterRepo: repo, InstanceRepo: instances, Registry: &Registry{}}).refresh(ctx)
	}
	quota := func(ctx context.Context, instances *mockInstanceRepo) {
		repo := newMockClusterRepo()
		repo.clusters["c1"] = &models.Cluster{ID: "c1", Name: "dev"}
		NewQuotaMonitor(QuotaMonitorConfig{
			ClusterRepo: repo, InstanceRepo: instances,
			QuotaRepo: &mockQuotaRepo{configs: map[string]*models.ResourceQuotaConfig{"c1": {ClusterID: "c1"}}},
			Notifier:  &mockQuotaNotifier{},
		}).check(ctx)
	}
	for _, cancelled := range []bool{true, false} {
		var wantFailure int64
		if !cancelled {
			wantFailure = 1
		}
		tests = append(tests,
			testCase{name: "secret refresher", prefix: "secret refresher:", cancelled: cancelled, wantFailure: wantFailure, run: refresher},
			testCase{name: "quota monitor", prefix: "quota monitor:", cancelled: cancelled, run: quota},
		)
	}
	for _, tt := range tests {
		name := tt.name + "/live error"
		if tt.cancelled {
			name = tt.name + "/cancelled"
		}
		t.Run(name, func(t *testing.T) {
			reader := swapClusterMeter(t)
			logs := captureSlog(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			instances := &mockInstanceRepo{}
			if tt.cancelled {
				instances.onFindByCluster = cancel
			} else {
				instances.findByClusterErr = errors.New("db down")
			}

			tt.run(ctx, instances)

			assert.Equal(t, tt.wantFailure, secretRefreshCount(t, reader, "failure"))
			var errorLines int
			for _, r := range logs.withPrefix(tt.prefix) {
				if r.Level >= slog.LevelError {
					errorLines++
				}
			}
			if tt.cancelled {
				assert.Zero(t, errorLines, "a cancel is not a failed read")
				assert.Zero(t, secretRefreshCount(t, reader, "success"), "a partial cycle records nothing")
			} else {
				assert.Equal(t, 1, errorLines, "a real error keeps its error line")
			}
		})
	}
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

// TestAccessReconciler_EnsureCancel checks that a cancel during
// EnsureAccessBindings logs at debug, while a real error keeps its warn line.
func TestAccessReconciler_EnsureCancel(t *testing.T) {
	for _, cancelled := range []bool{true, false} {
		name := "live error"
		if cancelled {
			name = "cancelled"
		}
		t.Run(name, func(t *testing.T) {
			logs := captureSlog(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			cs := fake.NewSimpleClientset()
			cs.PrependReactor("list", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
				if cancelled {
					cancel()
					return true, nil, context.Canceled
				}
				return true, nil, errors.New("api down")
			})
			client := k8s.NewClientFromInterface(cs)
			client.SetAccessBinding(&k8s.AccessBinding{ClusterRole: "stack-access", ServiceAccount: "backend", ServiceAccountNamespace: "system"})
			repo := newMockClusterRepo()
			repo.clusters["cluster-1"] = &models.Cluster{ID: "cluster-1", Name: "dev", UseInCluster: true}

			NewAccessReconciler(repo, NewRegistryForTest("cluster-1", client, nil), time.Hour).reconcile(ctx)

			recs := logs.withPrefix("access reconcile:")
			require.NotEmpty(t, recs)
			var warns int
			for _, r := range recs {
				if r.Level >= slog.LevelWarn {
					warns++
				}
			}
			if cancelled {
				assert.Zero(t, warns, "a cancel is not a failed reconcile")
			} else {
				assert.Equal(t, 1, warns, "a real error keeps its warn line")
			}
		})
	}
}

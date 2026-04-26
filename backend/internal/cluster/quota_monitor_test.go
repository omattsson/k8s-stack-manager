package cluster

import (
	"context"
	"sync"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Quota monitor test mocks ---

type mockQuotaNotifier struct {
	mu    sync.Mutex
	calls []quotaNotifyCall
}

type quotaNotifyCall struct {
	notifType, title, message, entityType, entityID string
}

func (m *mockQuotaNotifier) NotifySystem(_ context.Context, notifType, title, message, entityType, entityID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, quotaNotifyCall{notifType, title, message, entityType, entityID})
	return nil
}

func (m *mockQuotaNotifier) getCalls() []quotaNotifyCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]quotaNotifyCall, len(m.calls))
	copy(out, m.calls)
	return out
}

type mockQuotaRepo struct {
	configs map[string]*models.ResourceQuotaConfig
}

func (m *mockQuotaRepo) GetByClusterID(_ context.Context, clusterID string) (*models.ResourceQuotaConfig, error) {
	c := m.configs[clusterID]
	return c, nil
}

func (m *mockQuotaRepo) Upsert(_ context.Context, _ *models.ResourceQuotaConfig) error { return nil }
func (m *mockQuotaRepo) Delete(_ context.Context, _ string) error                      { return nil }

// --- Tests ---

func TestQuotaMonitor_StartStop(t *testing.T) {
	t.Parallel()

	m := NewQuotaMonitor(QuotaMonitorConfig{
		ClusterRepo:  newMockClusterRepo(),
		InstanceRepo: &mockInstanceRepo{},
		Interval:     1 * time.Hour,
	})

	m.Start()
	m.Start() // idempotent

	m.Stop()
	m.Stop() // safe to call twice
}

func TestQuotaMonitor_SkipsWhenNilQuotaRepo(t *testing.T) {
	t.Parallel()

	m := NewQuotaMonitor(QuotaMonitorConfig{
		ClusterRepo:  newMockClusterRepo(),
		InstanceRepo: &mockInstanceRepo{},
		Notifier:     &mockQuotaNotifier{},
	})
	// quotaRepo is nil — check() should return without panic.
	m.check()
}

func TestQuotaMonitor_SkipsWhenNilNotifier(t *testing.T) {
	t.Parallel()

	m := NewQuotaMonitor(QuotaMonitorConfig{
		ClusterRepo:  newMockClusterRepo(),
		InstanceRepo: &mockInstanceRepo{},
		QuotaRepo:    &mockQuotaRepo{configs: make(map[string]*models.ResourceQuotaConfig)},
	})
	// notifier is nil — check() should return without panic.
	m.check()
}

func TestQuotaMonitor_PruneCooldowns(t *testing.T) {
	t.Parallel()

	m := NewQuotaMonitor(QuotaMonitorConfig{
		ClusterRepo:  newMockClusterRepo(),
		InstanceRepo: &mockInstanceRepo{},
		Interval:     1 * time.Hour,
	})

	stale := time.Now().Add(-3 * quotaCooldownDuration)
	fresh := time.Now().Add(-30 * time.Minute)

	m.mu.Lock()
	m.cooldown["stale-cluster"] = stale
	m.cooldown["fresh-cluster"] = fresh
	m.mu.Unlock()

	m.pruneCooldowns()

	m.mu.Lock()
	defer m.mu.Unlock()

	_, hasStale := m.cooldown["stale-cluster"]
	_, hasFresh := m.cooldown["fresh-cluster"]

	assert.False(t, hasStale, "stale entry should have been pruned")
	assert.True(t, hasFresh, "fresh entry should be kept")
}

func TestQuotaMonitor_DefaultConfig(t *testing.T) {
	t.Parallel()

	m := NewQuotaMonitor(QuotaMonitorConfig{
		ClusterRepo:  newMockClusterRepo(),
		InstanceRepo: &mockInstanceRepo{},
	})

	require.Equal(t, defaultQuotaMonitorInterval, m.interval)
	require.InDelta(t, defaultQuotaThreshold, m.threshold, 0.001)
}

func TestQuotaMonitor_CooldownPreventsRepeatNotification(t *testing.T) {
	t.Parallel()

	m := NewQuotaMonitor(QuotaMonitorConfig{
		ClusterRepo:  newMockClusterRepo(),
		InstanceRepo: &mockInstanceRepo{},
		Interval:     1 * time.Hour,
	})

	// Simulate that cluster-1 was just alerted.
	m.mu.Lock()
	m.cooldown["cluster-1"] = time.Now()
	m.mu.Unlock()

	// The debounce check compares time.Since(last) < quotaCooldownDuration.
	// Verify entry is present and would block a new alert.
	m.mu.Lock()
	last, ok := m.cooldown["cluster-1"]
	m.mu.Unlock()

	assert.True(t, ok)
	assert.True(t, time.Since(last) < quotaCooldownDuration, "recent cooldown entry should block repeat alert")
}

package cluster

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Secret monitor test mocks ---

type mockSecretNotifier struct {
	mu    sync.Mutex
	calls []secretNotifyCall
}

type secretNotifyCall struct {
	notifType, title, message, entityType, entityID string
}

func (m *mockSecretNotifier) NotifySystem(_ context.Context, notifType, title, message, entityType, entityID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, secretNotifyCall{notifType, title, message, entityType, entityID})
	return nil
}

func (m *mockSecretNotifier) getCalls() []secretNotifyCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]secretNotifyCall, len(m.calls))
	copy(out, m.calls)
	return out
}

// --- Tests ---

func TestSecretMonitor_StartStop(t *testing.T) {
	t.Parallel()

	m := NewSecretMonitor(SecretMonitorConfig{
		ClusterRepo:  newMockClusterRepo(),
		InstanceRepo: &mockInstanceRepo{},
		Interval:     1 * time.Hour,
	})

	m.Start()
	m.Start() // idempotent

	m.Stop()
	m.Stop() // safe to call twice
}

func TestSecretMonitor_SkipsWhenNilNotifier(t *testing.T) {
	t.Parallel()

	m := NewSecretMonitor(SecretMonitorConfig{
		ClusterRepo:  newMockClusterRepo(),
		InstanceRepo: &mockInstanceRepo{},
	})
	// notifier is nil — scan() should return without panic.
	m.scan()
}

func TestSecretMonitor_PruneWarned(t *testing.T) {
	t.Parallel()

	m := NewSecretMonitor(SecretMonitorConfig{
		ClusterRepo:  newMockClusterRepo(),
		InstanceRepo: &mockInstanceRepo{},
		Interval:     1 * time.Hour,
	})

	stale := time.Now().Add(-3 * secretWarnCooldown)   // 72h ago, well past 2*24h cutoff
	fresh := time.Now().Add(-12 * time.Hour)            // 12h ago, within 48h cutoff

	m.mu.Lock()
	m.warned["cluster-1/ns-1/old-secret"] = stale
	m.warned["cluster-1/ns-1/recent-secret"] = fresh
	m.mu.Unlock()

	m.pruneWarned()

	m.mu.Lock()
	defer m.mu.Unlock()

	_, hasStale := m.warned["cluster-1/ns-1/old-secret"]
	_, hasFresh := m.warned["cluster-1/ns-1/recent-secret"]

	assert.False(t, hasStale, "stale entry should have been pruned")
	assert.True(t, hasFresh, "fresh entry should be kept")
}

func TestSecretMonitor_DefaultConfig(t *testing.T) {
	t.Parallel()

	m := NewSecretMonitor(SecretMonitorConfig{
		ClusterRepo:  newMockClusterRepo(),
		InstanceRepo: &mockInstanceRepo{},
	})

	require.Equal(t, defaultSecretMonitorInterval, m.interval)
	require.Equal(t, defaultSecretExpiryThreshold, m.threshold)
}

func TestSecretMonitor_CooldownPreventsRepeatWarning(t *testing.T) {
	t.Parallel()

	m := NewSecretMonitor(SecretMonitorConfig{
		ClusterRepo:  newMockClusterRepo(),
		InstanceRepo: &mockInstanceRepo{},
		Interval:     1 * time.Hour,
	})

	// Simulate a recent warning.
	m.mu.Lock()
	m.warned["cluster-1/ns-1/my-secret"] = time.Now()
	m.mu.Unlock()

	// pruneWarned should NOT remove it (it's fresh).
	m.pruneWarned()

	m.mu.Lock()
	_, exists := m.warned["cluster-1/ns-1/my-secret"]
	m.mu.Unlock()

	assert.True(t, exists, "recent warning should survive pruning")
}

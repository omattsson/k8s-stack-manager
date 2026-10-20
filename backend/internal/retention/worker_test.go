package retention

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"backend/internal/config"

	"github.com/stretchr/testify/assert"
)

// fakeRepo records the calls and returns the configured results.
type fakeRepo struct {
	result map[string]int64
	errs   map[string]error
	calls  map[string]time.Duration
	total  int // all calls, also repeated ones
	mu     sync.Mutex
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{result: map[string]int64{}, errs: map[string]error{}, calls: map[string]time.Duration{}}
}

func (f *fakeRepo) call(table string, age time.Duration) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[table] = age
	f.total++
	return f.result[table], f.errs[table]
}

func (f *fakeRepo) DeleteDeploymentLogsOlderThan(_ context.Context, age time.Duration, _ int) (int64, error) {
	return f.call(TableDeploymentLogs, age)
}

func (f *fakeRepo) DeleteAuditLogsOlderThan(_ context.Context, age time.Duration, _ int) (int64, error) {
	return f.call(TableAuditLogs, age)
}

func (f *fakeRepo) DeleteNotificationsOlderThan(_ context.Context, age time.Duration, _ int) (int64, error) {
	return f.call(TableNotifications, age)
}

func (f *fakeRepo) DeleteDeliveryLogsOlderThan(_ context.Context, age time.Duration, _ int) (int64, error) {
	return f.call(TableDeliveryLogs, age)
}

func (f *fakeRepo) DeleteActionRunsOlderThan(_ context.Context, age time.Duration, _ int) (int64, error) {
	return f.call(TableActionRuns, age)
}

func (f *fakeRepo) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.total
}

func TestWorker_RunOnce(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		cfg         config.RetentionConfig
		errs        map[string]error
		wantCalls   map[string]time.Duration
		wantDeleted map[string]int64
	}{
		{
			name: "every table with a retention",
			cfg:  config.RetentionConfig{DeployLogDays: 180, AuditLogDays: 365, NotificationDays: 90, DeliveryLogDays: 30, ActionRunDays: 60},
			wantCalls: map[string]time.Duration{
				TableDeploymentLogs: 180 * day, TableAuditLogs: 365 * day,
				TableNotifications: 90 * day, TableDeliveryLogs: 30 * day, TableActionRuns: 60 * day,
			},
			wantDeleted: map[string]int64{TableDeploymentLogs: 4, TableAuditLogs: 3, TableNotifications: 2, TableDeliveryLogs: 1, TableActionRuns: 5},
		},
		{
			name:        "action runs only",
			cfg:         config.RetentionConfig{ActionRunDays: 180},
			wantCalls:   map[string]time.Duration{TableActionRuns: 180 * day},
			wantDeleted: map[string]int64{TableActionRuns: 5},
		},
		{
			name:        "0 keeps the rows",
			cfg:         config.RetentionConfig{AuditLogDays: 365},
			wantCalls:   map[string]time.Duration{TableAuditLogs: 365 * day},
			wantDeleted: map[string]int64{TableAuditLogs: 3},
		},
		{
			name: "a failure in one table does not stop the others",
			cfg:  config.RetentionConfig{DeployLogDays: 1, AuditLogDays: 1, NotificationDays: 1, DeliveryLogDays: 1},
			errs: map[string]error{TableDeploymentLogs: errors.New("lock wait timeout")},
			wantCalls: map[string]time.Duration{
				TableDeploymentLogs: day, TableAuditLogs: day, TableNotifications: day, TableDeliveryLogs: day,
			},
			wantDeleted: map[string]int64{TableDeploymentLogs: 4, TableAuditLogs: 3, TableNotifications: 2, TableDeliveryLogs: 1},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := newFakeRepo()
			repo.result = map[string]int64{TableDeploymentLogs: 4, TableAuditLogs: 3, TableNotifications: 2, TableDeliveryLogs: 1, TableActionRuns: 5}
			for k, v := range tt.errs {
				repo.errs[k] = v
			}

			deleted := NewWorker(repo, tt.cfg).RunOnce(context.Background())

			assert.Equal(t, tt.wantCalls, repo.calls)
			assert.Equal(t, tt.wantDeleted, deleted)
		})
	}
}

func TestWorker_RunIsRestartable(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	w := NewWorker(repo, config.RetentionConfig{AuditLogDays: 1, Interval: time.Minute})
	assert.Equal(t, time.Minute, w.interval)

	// Two leader terms: each runs once at the start and stops with ctx.
	for term := 1; term <= 2; term++ {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			w.Run(ctx)
			close(done)
		}()
		want := term
		assert.Eventually(t, func() bool { return repo.callCount() == want }, 5*time.Second, 10*time.Millisecond)
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not stop after the context ended")
		}
	}
}

func TestWorker_CanceledContextStops(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	deleted := NewWorker(repo, config.RetentionConfig{DeployLogDays: 1, AuditLogDays: 1}).RunOnce(ctx)

	assert.Empty(t, deleted)
	assert.Zero(t, repo.callCount())
}

func TestNewWorker_DefaultInterval(t *testing.T) {
	t.Parallel()
	w := NewWorker(newFakeRepo(), config.RetentionConfig{})
	assert.Equal(t, config.DefaultLogRetentionInterval, w.interval)
}

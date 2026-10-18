package scheduler

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/robfig/cron/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Scheduler) entryCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entryMap)
}

func (s *Scheduler) entryID(policyID string) (cron.EntryID, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.entryMap[policyID]
	return id, ok
}

func newTestPolicy(id, schedule string) *models.CleanupPolicy {
	return &models.CleanupPolicy{
		ID: id, Name: id, Schedule: schedule, Condition: "status:stopped",
		Action: "stop", ClusterID: "all", Enabled: true,
	}
}

func TestScheduler_ReloadWhenInactiveDoesNothing(t *testing.T) {
	t.Parallel()

	policyRepo := newMockPolicyRepo()
	require.NoError(t, policyRepo.Create(newTestPolicy("p1", "0 2 * * *")))
	s := NewScheduler(policyRepo, &mockInstanceRepo{}, &mockAuditRepo{}, nil, nil)

	// A replica that is not the leader handles the policy change request.
	require.NoError(t, s.Reload())
	assert.False(t, s.Active())
	assert.Equal(t, 0, s.entryCount(), "an inactive scheduler must not add cron entries")
}

func TestScheduler_RunReloadsPeriodicallyAndRestarts(t *testing.T) {
	t.Parallel()

	policyRepo := newMockPolicyRepo()
	require.NoError(t, policyRepo.Create(newTestPolicy("p1", "0 2 * * *")))
	s := NewScheduler(policyRepo, &mockInstanceRepo{}, &mockAuditRepo{}, nil, nil).
		WithReloadInterval(20 * time.Millisecond)

	for term := 0; term < 2; term++ {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			s.Run(ctx)
		}()
		require.Eventually(t, func() bool { return s.Active() && s.entryCount() == 1 },
			time.Second, 5*time.Millisecond, "term %d", term)

		if term == 0 {
			// A policy change handled by another replica reaches the
			// leader with the periodic reload.
			require.NoError(t, policyRepo.Create(newTestPolicy("p2", "0 3 * * *")))
			require.Eventually(t, func() bool { return s.entryCount() == 2 }, time.Second, 5*time.Millisecond)
			require.NoError(t, policyRepo.Delete("p2"))
			require.Eventually(t, func() bool { return s.entryCount() == 1 }, time.Second, 5*time.Millisecond)
		}

		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Run did not return after cancel")
		}
		assert.False(t, s.Active())
		assert.Equal(t, 0, s.entryCount())
	}
}

func TestScheduler_ReloadKeepsEntriesWhenSchedulesUnchanged(t *testing.T) {
	t.Parallel()

	policyRepo := newMockPolicyRepo()
	p1 := newTestPolicy("p1", "0 2 * * *")
	require.NoError(t, policyRepo.Create(p1))
	s := NewScheduler(policyRepo, &mockInstanceRepo{}, &mockAuditRepo{}, nil, nil)
	require.NoError(t, s.Start())
	defer s.Stop()

	before, ok := s.entryID("p1")
	require.True(t, ok)

	// Same schedule (other fields changed): no new cron entry.
	p1.Condition = "idle_days:3"
	require.NoError(t, s.Reload())
	same, _ := s.entryID("p1")
	assert.Equal(t, before, same)

	// New schedule: new cron entry.
	p1.Schedule = "0 4 * * *"
	require.NoError(t, s.Reload())
	after, _ := s.entryID("p1")
	assert.NotEqual(t, before, after)
}

func TestScheduler_ScheduledRunReadsCurrentPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mutate      func(repo *mockPolicyRepo, p *models.CleanupPolicy)
		cancelled   bool
		name        string
		wantLastRun bool
	}{
		{name: "enabled policy runs", mutate: func(*mockPolicyRepo, *models.CleanupPolicy) {}, wantLastRun: true},
		{name: "disabled after scheduling is skipped", mutate: func(_ *mockPolicyRepo, p *models.CleanupPolicy) { p.Enabled = false }},
		{name: "deleted after scheduling is skipped", mutate: func(r *mockPolicyRepo, p *models.CleanupPolicy) { _ = r.Delete(p.ID) }},
		{name: "ended term is skipped", mutate: func(*mockPolicyRepo, *models.CleanupPolicy) {}, cancelled: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			policyRepo := newMockPolicyRepo()
			p := newTestPolicy("p1", "0 2 * * *")
			require.NoError(t, policyRepo.Create(p))
			s := NewScheduler(policyRepo, &mockInstanceRepo{}, &mockAuditRepo{}, nil, nil)
			tt.mutate(policyRepo, p)

			ctx, cancel := context.WithCancel(context.Background())
			if tt.cancelled {
				cancel()
			}
			defer cancel()
			s.executeScheduledPolicy(ctx, "p1")
			assert.Equal(t, tt.wantLastRun, p.LastRunAt != nil)
		})
	}
}

// countingExecutor counts the actions it receives.
type countingExecutor struct {
	calls atomic.Int32
}

func (e *countingExecutor) StopInstance(context.Context, *models.StackInstance) error {
	e.calls.Add(1)
	return nil
}

func (e *countingExecutor) CleanInstance(context.Context, *models.StackInstance) error {
	e.calls.Add(1)
	return nil
}

func (e *countingExecutor) DeleteInstance(context.Context, *models.StackInstance) error {
	e.calls.Add(1)
	return nil
}

func TestScheduler_OnlyActiveSchedulerRunsCronJobs(t *testing.T) {
	t.Parallel()

	// Two replicas share one policy table. Only the leader runs Run.
	policyRepo := newMockPolicyRepo()
	p := newTestPolicy("p1", "@every 1s")
	p.Condition = "status:stopped"
	require.NoError(t, policyRepo.Create(p))
	instanceRepo := &mockInstanceRepo{instances: []models.StackInstance{
		{ID: "i1", Name: "one", Status: models.StackStatusStopped},
	}}

	leaderExec, followerExec := &countingExecutor{}, &countingExecutor{}
	leader := NewScheduler(policyRepo, instanceRepo, &mockAuditRepo{}, leaderExec, nil)
	follower := NewScheduler(policyRepo, instanceRepo, &mockAuditRepo{}, followerExec, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		leader.Run(ctx)
	}()
	// The follower handles a policy change request.
	require.NoError(t, follower.Reload())

	require.Eventually(t, func() bool { return leaderExec.calls.Load() >= 1 }, 3*time.Second, 10*time.Millisecond)
	cancel()
	<-done
	assert.Equal(t, int32(0), followerExec.calls.Load(), "the follower must not run cron jobs")
}

// cancellingExecutor cancels the run context during its first action and
// returns the context error, like an action that the end of a leadership
// term interrupts.
type cancellingExecutor struct {
	cancel context.CancelFunc
	calls  atomic.Int32
}

func (e *cancellingExecutor) act(ctx context.Context) error {
	e.calls.Add(1)
	e.cancel()
	<-ctx.Done()
	return ctx.Err()
}

func (e *cancellingExecutor) StopInstance(ctx context.Context, _ *models.StackInstance) error {
	return e.act(ctx)
}

func (e *cancellingExecutor) CleanInstance(ctx context.Context, _ *models.StackInstance) error {
	return e.act(ctx)
}

func (e *cancellingExecutor) DeleteInstance(ctx context.Context, _ *models.StackInstance) error {
	return e.act(ctx)
}

// countingCleanupNotifier counts notifications.
type countingCleanupNotifier struct {
	calls atomic.Int32
}

func (n *countingCleanupNotifier) NotifyInstance(context.Context, models.NotificationTarget, string, string, string) error {
	n.calls.Add(1)
	return nil
}

func (n *countingCleanupNotifier) NotifySystemForInstances(context.Context, string, string, string, string, string, []models.NotificationTarget) error {
	n.calls.Add(1)
	return nil
}

func TestScheduler_InterruptedRunStopsWithoutSideEffects(t *testing.T) {
	t.Parallel()

	policyRepo := newMockPolicyRepo()
	p := newTestPolicy("p1", "0 2 * * *")
	require.NoError(t, policyRepo.Create(p))
	instanceRepo := &mockInstanceRepo{instances: []models.StackInstance{
		{ID: "i1", Name: "one", Status: models.StackStatusStopped},
		{ID: "i2", Name: "two", Status: models.StackStatusStopped},
		{ID: "i3", Name: "three", Status: models.StackStatusStopped},
	}}
	auditRepo := &mockAuditRepo{}
	notifier := &countingCleanupNotifier{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exec := &cancellingExecutor{cancel: cancel}
	s := NewScheduler(policyRepo, instanceRepo, auditRepo, exec, notifier)

	s.executeScheduledPolicy(ctx, "p1")

	assert.Equal(t, int32(1), exec.calls.Load(), "no action after the cancel")
	assert.Empty(t, auditRepo.entries, "no audit entries for interrupted or skipped instances")
	assert.Nil(t, p.LastRunAt, "an interrupted run is not recorded")
	assert.Equal(t, int32(0), notifier.calls.Load(), "no summary for an interrupted run")

	// The same run with a cancelled context from the start does nothing.
	results, err := s.executePolicyWithOptions(ctx, p, false)
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, results)
	assert.Equal(t, int32(1), exec.calls.Load())
}

func TestScheduler_CatchUpAtActivation(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	// dailyAt returns a daily cron expression whose last time is d ago.
	dailyAt := func(d time.Duration) string {
		at := now.Add(-d)
		return fmt.Sprintf("%d %d * * *", at.Minute(), at.Hour())
	}
	ago := func(d time.Duration) *time.Time {
		v := now.Add(-d)
		return &v
	}

	tests := []struct {
		lastRunAt   *time.Time
		createdAt   time.Time
		name        string
		schedule    string
		dryRun      bool
		wantRun     bool
		wantActions int32
	}{
		{name: "missed run, never ran", schedule: dailyAt(2 * time.Minute), wantRun: true, wantActions: 1},
		{name: "missed run, last run before it", schedule: dailyAt(2 * time.Minute), lastRunAt: ago(time.Hour), wantRun: true, wantActions: 1},
		{name: "missed run in dry-run mode", schedule: dailyAt(2 * time.Minute), dryRun: true, wantRun: true, wantActions: 0},
		{name: "last run after the scheduled time", schedule: dailyAt(4 * time.Minute), lastRunAt: ago(time.Minute)},
		{name: "created after the scheduled time", schedule: dailyAt(4 * time.Minute), createdAt: now.Add(-time.Minute)},
		{name: "scheduled time outside the window", schedule: dailyAt(10 * time.Minute)},
		{name: "@every has no fixed times", schedule: "@every 1m"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			policyRepo := newMockPolicyRepo()
			p := newTestPolicy("p1", tt.schedule)
			p.LastRunAt = tt.lastRunAt
			p.CreatedAt = tt.createdAt
			p.DryRun = tt.dryRun
			require.NoError(t, policyRepo.Create(p))
			instanceRepo := &mockInstanceRepo{instances: []models.StackInstance{
				{ID: "i1", Name: "one", Status: models.StackStatusStopped},
			}}
			exec := &countingExecutor{}
			s := NewScheduler(policyRepo, instanceRepo, &mockAuditRepo{}, exec, nil)

			require.NoError(t, s.Start())
			s.Stop() // waits for the catch-up run

			ran := p.LastRunAt != nil && (tt.lastRunAt == nil || p.LastRunAt.After(*tt.lastRunAt))
			assert.Equal(t, tt.wantRun, ran)
			assert.Equal(t, tt.wantActions, exec.calls.Load())
		})
	}
}

func TestPreviousScheduledTime(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 9, 12, 3, 30, 0, time.UTC)
	tests := []struct {
		name   string
		spec   string
		want   time.Time
		wantOK bool
	}{
		{name: "every minute", spec: "* * * * *", want: time.Date(2026, 10, 9, 12, 3, 0, 0, time.UTC), wantOK: true},
		{name: "hourly at minute 0", spec: "0 * * * *", want: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC), wantOK: true},
		{name: "daily at 02:00", spec: "0 2 * * *"},
		{name: "every", spec: "@every 1m"},
		{name: "invalid", spec: "not a schedule"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := previousScheduledTime(tt.spec, now)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestPreviousScheduledTime_Boundary(t *testing.T) {
	t.Parallel()

	scheduled := time.Date(2026, 1, 1, 3, 17, 0, 0, time.UTC)
	tests := []struct {
		now    time.Time
		name   string
		wantOK bool
	}{
		{name: "now equals the scheduled time: the cron instance runs it", now: scheduled, wantOK: false},
		{name: "now just after the scheduled time: catch up", now: scheduled.Add(time.Nanosecond), wantOK: true},
		{name: "now at the end of the window", now: scheduled.Add(catchUpWindow - time.Nanosecond), wantOK: true},
		{name: "now after the window", now: scheduled.Add(catchUpWindow), wantOK: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := previousScheduledTime("17 3 1 1 *", tt.now)
			assert.Equal(t, tt.wantOK, ok)
			if ok {
				assert.Equal(t, scheduled, got)
			}
		})
	}
}

func TestScheduler_CatchUpBoundaryWithClock(t *testing.T) {
	t.Parallel()

	scheduled := time.Date(2026, 1, 1, 3, 17, 0, 0, time.UTC)
	tests := []struct {
		now     time.Time
		name    string
		wantRun bool
	}{
		{name: "activation at the scheduled time: no catch-up", now: scheduled},
		{name: "activation just after the scheduled time: catch-up", now: scheduled.Add(time.Nanosecond), wantRun: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			policyRepo := newMockPolicyRepo()
			p := newTestPolicy("p1", "17 3 1 1 *")
			require.NoError(t, policyRepo.Create(p))
			exec := &countingExecutor{}
			s := NewScheduler(policyRepo, &mockInstanceRepo{instances: []models.StackInstance{
				{ID: "i1", Name: "one", Status: models.StackStatusStopped},
			}}, &mockAuditRepo{}, exec, nil)
			s.now = func() time.Time { return tt.now }

			require.NoError(t, s.Start())
			s.Stop()
			assert.Equal(t, tt.wantRun, p.LastRunAt != nil)
			assert.Equal(t, tt.wantRun, exec.calls.Load() == 1)
		})
	}
}

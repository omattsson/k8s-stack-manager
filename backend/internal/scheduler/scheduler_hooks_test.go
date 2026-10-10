package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"backend/internal/hooks"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingHooks records the events the scheduler fires.
type recordingHooks struct {
	mu     sync.Mutex
	events []hooks.EventEnvelope
	names  []string
}

func (h *recordingHooks) Fire(_ context.Context, event string, env hooks.EventEnvelope) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.names = append(h.names, event)
	h.events = append(h.events, env)
	return nil
}

func (h *recordingHooks) snapshot() ([]string, []hooks.EventEnvelope) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.names...), append([]hooks.EventEnvelope(nil), h.events...)
}

// triggerExecutor records the hook trigger of each action and fails the
// instances in failIDs.
type triggerExecutor struct {
	mu       sync.Mutex
	triggers []hooks.Trigger
	failIDs  map[string]bool
}

func (e *triggerExecutor) act(ctx context.Context, inst *models.StackInstance) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	t, _ := hooks.TriggerFromContext(ctx)
	e.triggers = append(e.triggers, t)
	if e.failIDs[inst.ID] {
		return errors.New("cannot delete instance while status is running;\n stop first")
	}
	return nil
}

func (e *triggerExecutor) StopInstance(ctx context.Context, inst *models.StackInstance) error {
	return e.act(ctx, inst)
}

func (e *triggerExecutor) CleanInstance(ctx context.Context, inst *models.StackInstance) error {
	return e.act(ctx, inst)
}

func (e *triggerExecutor) DeleteInstance(ctx context.Context, inst *models.StackInstance) error {
	return e.act(ctx, inst)
}

// systemNotifier records the system and owner notifications.
type systemNotifier struct {
	mu       sync.Mutex
	messages []string
	owners   []string
}

func (n *systemNotifier) NotifyInstance(_ context.Context, _ models.NotificationTarget, notifType, _, _ string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.owners = append(n.owners, notifType)
	return nil
}

func (n *systemNotifier) NotifySystemForInstances(_ context.Context, _, _, message, _, _ string, _ []models.NotificationTarget) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.messages = append(n.messages, message)
	return nil
}

func stoppedInstances(n int) []models.StackInstance {
	out := make([]models.StackInstance, n)
	for i := range out {
		out[i] = models.StackInstance{
			ID: fmt.Sprintf("i%02d", i), Name: fmt.Sprintf("stack-%02d", i), Namespace: fmt.Sprintf("ns-%02d", i),
			OwnerID: "owner", Status: models.StackStatusStopped, ClusterID: "c1",
		}
	}
	return out
}

func TestScheduler_FiresCleanupPolicyExecuted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		instances     []models.StackInstance
		action        string
		dryRun        bool
		manual        bool
		failIDs       map[string]bool
		expectFired   bool
		expectResults []string
		expectFailed  int
		expectActions int
	}{
		{name: "scheduled run with matches", instances: stoppedInstances(2), action: "stop", expectFired: true, expectResults: []string{"success", "success"}, expectActions: 2},
		{name: "manual run with matches", instances: stoppedInstances(1), action: "clean", manual: true, expectFired: true, expectResults: []string{"success"}, expectActions: 1},
		{name: "dry run fires with dry_run and no action", instances: stoppedInstances(2), action: "delete", dryRun: true, expectFired: true, expectResults: []string{"dry_run", "dry_run"}},
		{name: "error result carries the error text", instances: stoppedInstances(2), action: "delete", failIDs: map[string]bool{"i01": true}, expectFired: true, expectResults: []string{"success", "error"}, expectFailed: 1, expectActions: 2},
		{name: "run without matches fires nothing", instances: []models.StackInstance{{ID: "r1", Name: "running", Status: models.StackStatusRunning}}, action: "stop"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			policyRepo := newMockPolicyRepo()
			p := newTestPolicy("pol-1", "0 2 * * *")
			p.Name = "nightly"
			p.Action = tt.action
			p.DryRun = tt.dryRun
			require.NoError(t, policyRepo.Create(p))
			exec := &triggerExecutor{failIDs: tt.failIDs}
			rec := &recordingHooks{}
			s := NewScheduler(policyRepo, &mockInstanceRepo{instances: tt.instances}, &mockAuditRepo{}, exec, nil).WithHooks(rec)

			if tt.manual {
				_, err := s.RunPolicy("pol-1", tt.dryRun)
				require.NoError(t, err)
			} else {
				s.executeScheduledPolicy(context.Background(), "pol-1")
			}

			names, envs := rec.snapshot()
			if !tt.expectFired {
				assert.Empty(t, names)
				return
			}
			require.Equal(t, []string{hooks.EventCleanupPolicyExecuted}, names, "one event per run")
			env := envs[0]
			assert.Equal(t, &hooks.Trigger{Type: hooks.TriggerCleanupPolicy, ID: "pol-1", Name: "nightly"}, env.Trigger)
			assert.Nil(t, env.InstanceRef)
			run := env.CleanupPolicy
			require.NotNil(t, run)
			assert.Equal(t, "pol-1", run.ID)
			assert.Equal(t, "nightly", run.Name)
			assert.Equal(t, tt.action, run.Action)
			assert.Equal(t, "all", run.ClusterID)
			assert.Equal(t, "status:stopped", run.Condition)
			assert.Equal(t, tt.dryRun, run.DryRun)
			wantRun := runScheduled
			if tt.manual {
				wantRun = runManual
			}
			assert.Equal(t, wantRun, run.Run)
			assert.Equal(t, len(tt.expectResults), run.Matched)
			assert.Equal(t, tt.expectFailed, run.Failed)
			var results []string
			for i, inst := range run.Instances {
				results = append(results, inst.Result)
				assert.Equal(t, tt.instances[i].ID, inst.ID)
				assert.Equal(t, tt.instances[i].Name, inst.Name)
				assert.Equal(t, tt.instances[i].Namespace, inst.Namespace)
				assert.Equal(t, "owner", inst.OwnerID)
				if inst.Result == hooks.CleanupResultError {
					assert.Equal(t, "cannot delete instance while status is running; stop first", inst.Error, "error text flattened to one line")
				} else {
					assert.Empty(t, inst.Error)
				}
			}
			assert.Equal(t, tt.expectResults, results)

			exec.mu.Lock()
			assert.Len(t, exec.triggers, tt.expectActions)
			for _, tr := range exec.triggers {
				assert.Equal(t, hooks.Trigger{Type: hooks.TriggerCleanupPolicy, ID: "pol-1", Name: "nightly"}, tr, "actions carry the policy trigger")
			}
			exec.mu.Unlock()
		})
	}
}

func TestScheduler_InterruptedRunFiresNothing(t *testing.T) {
	t.Parallel()
	policyRepo := newMockPolicyRepo()
	require.NoError(t, policyRepo.Create(newTestPolicy("p1", "0 2 * * *")))
	rec := &recordingHooks{}
	s := NewScheduler(policyRepo, &mockInstanceRepo{instances: stoppedInstances(3)}, &mockAuditRepo{}, &triggerExecutor{}, nil).WithHooks(rec)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.executeScheduledPolicy(ctx, "p1")

	names, _ := rec.snapshot()
	assert.Empty(t, names, "an ended leadership term sends no summary")
}

func TestBuildPolicyRun_TruncatesInstances(t *testing.T) {
	t.Parallel()
	results := make([]CleanupResult, maxHookInstances+5)
	for i := range results {
		results[i] = CleanupResult{InstanceID: fmt.Sprint(i), Status: "success"}
	}
	results[len(results)-1].Status = "error"
	results[len(results)-1].Error = strings.Repeat("x", maxHookErrorLen+50)

	run := buildPolicyRun(newTestPolicy("p1", "@daily"), results, false, runScheduled)
	assert.Len(t, run.Instances, maxHookInstances)
	assert.True(t, run.InstancesTruncated)
	assert.Equal(t, maxHookInstances+5, run.Matched)
	assert.Equal(t, maxHookInstances+4, run.Succeeded)
	assert.Equal(t, 1, run.Failed)
	assert.Equal(t, maxHookErrorLen+len("…"), len(hookErrorText(results[len(results)-1].Error)))
}

func TestScheduler_ChannelMessageListsNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		count         int
		action        string
		failIDs       map[string]bool
		expectMessage string
		expectOwners  int
	}{
		{
			name: "few names", count: 2, action: "stop",
			expectMessage: `Policy "p1" matched 2 instance(s), action: stop: stack-00, stack-01`,
			expectOwners:  2,
		},
		{
			name: "more than ten names", count: 12, action: "stop",
			expectMessage: `Policy "p1" matched 12 instance(s), action: stop: stack-00, stack-01, stack-02, stack-03, stack-04, stack-05, stack-06, stack-07, stack-08, stack-09 and 2 more`,
			expectOwners:  12,
		},
		{
			name: "failed count and delete sends no owner notification from the scheduler", count: 2, action: "delete",
			failIDs:       map[string]bool{"i00": true},
			expectMessage: `Policy "p1" matched 1 instance(s), action: delete: stack-01 (1 failed)`,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			policyRepo := newMockPolicyRepo()
			p := newTestPolicy("p1", "0 2 * * *")
			p.Action = tt.action
			require.NoError(t, policyRepo.Create(p))
			notif := &systemNotifier{}
			s := NewScheduler(policyRepo, &mockInstanceRepo{instances: stoppedInstances(tt.count)}, &mockAuditRepo{}, &triggerExecutor{failIDs: tt.failIDs}, notif)

			s.executeScheduledPolicy(context.Background(), "p1")

			notif.mu.Lock()
			defer notif.mu.Unlock()
			require.Len(t, notif.messages, 1)
			assert.Equal(t, tt.expectMessage, notif.messages[0])
			assert.Len(t, notif.owners, tt.expectOwners)
		})
	}
}

// timeoutHooks is a HookFirer that reports hook timeouts per event.
type timeoutHooks struct {
	recordingHooks
	timeouts map[string]time.Duration
}

func (h *timeoutHooks) TotalTimeout(event string) time.Duration { return h.timeouts[event] }

func TestScheduler_ActionTimeout(t *testing.T) {
	t.Parallel()

	long := &timeoutHooks{timeouts: map[string]time.Duration{hooks.EventPreInstanceDelete: 20 * time.Minute}}
	short := &timeoutHooks{timeouts: map[string]time.Duration{hooks.EventPreInstanceDelete: 30 * time.Second}}
	tests := []struct {
		name   string
		hooks  HookFirer
		action string
		expect time.Duration
	}{
		{name: "stop uses the default", hooks: long, action: "stop", expect: 5 * time.Minute},
		{name: "delete without hooks uses the default", action: "delete", expect: 5 * time.Minute},
		{name: "delete with short hooks uses the default", hooks: short, action: "delete", expect: 5 * time.Minute},
		{name: "delete with long hooks waits for them", hooks: long, action: "delete", expect: 21 * time.Minute},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := NewScheduler(newMockPolicyRepo(), &mockInstanceRepo{}, &mockAuditRepo{}, nil, nil).WithHooks(tt.hooks)
			assert.Equal(t, tt.expect, s.actionTimeout(tt.action))
		})
	}
}

func TestHookErrorText_CutsAtRuneBoundary(t *testing.T) {
	t.Parallel()
	msg := strings.Repeat("a", maxHookErrorLen-1) + "ö" + "tail"
	got := hookErrorText(msg)
	assert.True(t, utf8.ValidString(got))
	assert.Equal(t, strings.Repeat("a", maxHookErrorLen-1)+"…", got)
}

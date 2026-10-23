package scheduler

import (
	"context"
	"sync"
	"testing"

	"backend/internal/models"
	"backend/internal/notifier"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingCleanupNotifier records the summary and any instance
// notification. NotifyInstance is not part of CleanupNotifier; a call would
// mean that the scheduler sends a per-instance notification again (#651).
type recordingCleanupNotifier struct {
	mu            sync.Mutex
	instanceTypes []string
	summaryTypes  []string
	summary       []models.NotificationTarget
	summaryCalls  int
}

func (n *recordingCleanupNotifier) NotifyInstance(_ context.Context, _ models.NotificationTarget, notifType, _, _ string, _ ...notifier.InstanceOption) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.instanceTypes = append(n.instanceTypes, notifType)
	return nil
}

func (n *recordingCleanupNotifier) NotifySystemForInstances(_ context.Context, notifType, _, _, _, _ string, instances []models.NotificationTarget) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.summaryCalls++
	n.summaryTypes = append(n.summaryTypes, notifType)
	n.summary = instances
	return nil
}

// TestScheduler_PolicyRunNotifications checks that a policy run sends only
// the summary cleanup.policy.executed: the deploy manager sends the one
// notification of each stop, clean or delete (#651).
func TestScheduler_PolicyRunNotifications(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		action string
		dryRun bool
	}{
		{name: "stop", action: "stop"},
		{name: "clean", action: "clean"},
		{name: "delete", action: "delete"},
		{name: "dry run", action: "stop", dryRun: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			policyRepo := newMockPolicyRepo()
			p := newTestPolicy("p1", "0 2 * * *")
			p.Action = tt.action
			p.DryRun = tt.dryRun
			require.NoError(t, policyRepo.Create(context.Background(), p))
			instanceRepo := &mockInstanceRepo{instances: []models.StackInstance{
				{ID: "i1", Name: "team-a-1", OwnerID: "u1", StackDefinitionID: "d1", ClusterID: "c1", Namespace: "stack-team-a-1-u1", Branch: "main", Status: models.StackStatusStopped},
				{ID: "i2", Name: "running", OwnerID: "u2", Status: models.StackStatusRunning},
			}}
			notif := &recordingCleanupNotifier{}
			s := NewScheduler(policyRepo, instanceRepo, &mockAuditRepo{}, &mockExecutor{}, notif)

			s.executeScheduledPolicy(context.Background(), "p1")

			notif.mu.Lock()
			defer notif.mu.Unlock()
			assert.Empty(t, notif.instanceTypes, "no per-instance notification from the scheduler")
			require.Equal(t, 1, notif.summaryCalls)
			assert.Equal(t, []string{"cleanup.policy.executed"}, notif.summaryTypes)
			require.Len(t, notif.summary, 1, "the summary carries the affected instance for the channel filters")
			assert.Equal(t, "team-a-1", notif.summary[0].InstanceName)
			assert.Equal(t, "d1", notif.summary[0].DefinitionID)
			assert.Equal(t, "c1", notif.summary[0].ClusterID)
			assert.Equal(t, "u1", notif.summary[0].OwnerID)
		})
	}
}

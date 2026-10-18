package scheduler

import (
	"context"
	"sync"
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingCleanupNotifier records the notification targets.
type recordingCleanupNotifier struct {
	mu            sync.Mutex
	followers     map[string][]string
	followerCalls []string
	instanceCalls []models.NotificationTarget
	instanceTypes []string
	summary       []models.NotificationTarget
	summaryCalls  int
}

func (n *recordingCleanupNotifier) NotifyInstance(_ context.Context, target models.NotificationTarget, notifType, _, _ string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.instanceCalls = append(n.instanceCalls, target)
	n.instanceTypes = append(n.instanceTypes, notifType)
	return nil
}

func (n *recordingCleanupNotifier) NotifySystemForInstances(_ context.Context, _, _, _, _, _ string, instances []models.NotificationTarget) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.summaryCalls++
	n.summary = instances
	return nil
}

func (n *recordingCleanupNotifier) FollowerIDs(_ context.Context, instanceID string) []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.followerCalls = append(n.followerCalls, instanceID)
	return n.followers[instanceID]
}

func TestScheduler_PolicyRunNotificationTargets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		action            string
		dryRun            bool
		wantFollowerCalls []string
		wantInstanceCalls int
		wantFollowers     []string // FollowerIDs of the i1 target
	}{
		{name: "delete: the executor notifies owner and followers, not the scheduler", action: "delete",
			wantInstanceCalls: 0},
		{name: "stop lets the notifier read the followers", action: "stop",
			wantInstanceCalls: 1},
		{name: "dry run sends only the summary", action: "delete", dryRun: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			policyRepo := newMockPolicyRepo()
			p := newTestPolicy("p1", "0 2 * * *")
			p.Action = tt.action
			p.DryRun = tt.dryRun
			require.NoError(t, policyRepo.Create(p))
			instanceRepo := &mockInstanceRepo{instances: []models.StackInstance{
				{ID: "i1", Name: "team-a-1", OwnerID: "u1", StackDefinitionID: "d1", ClusterID: "c1", Status: models.StackStatusStopped},
				{ID: "i2", Name: "running", OwnerID: "u2", Status: models.StackStatusRunning},
			}}
			notif := &recordingCleanupNotifier{followers: map[string][]string{"i1": {"f1"}}}
			s := NewScheduler(policyRepo, instanceRepo, &mockAuditRepo{}, &mockExecutor{}, notif)

			s.executeScheduledPolicy(context.Background(), "p1")

			assert.Equal(t, tt.wantFollowerCalls, notif.followerCalls)
			require.Equal(t, 1, notif.summaryCalls)
			require.Len(t, notif.summary, 1, "the summary carries the affected instance for the channel filters")
			assert.Equal(t, "team-a-1", notif.summary[0].InstanceName)
			assert.Equal(t, "d1", notif.summary[0].DefinitionID)
			assert.Equal(t, "c1", notif.summary[0].ClusterID)

			require.Len(t, notif.instanceCalls, tt.wantInstanceCalls)
			if tt.wantInstanceCalls > 0 {
				assert.Equal(t, "cleanup.policy."+tt.action, notif.instanceTypes[0])
				assert.Equal(t, "u1", notif.instanceCalls[0].OwnerID)
				assert.Equal(t, tt.wantFollowers, notif.instanceCalls[0].FollowerIDs)
			}
		})
	}
}

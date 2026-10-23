package deployer

import (
	"context"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInterruptRecovery_AuditSurvivesTheEndOfTheTerm checks that the audit
// entry of an ended operation is written when the leadership term ends right
// after the operation was ended (#656).
func TestInterruptRecovery_AuditSurvivesTheEndOfTheTerm(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	const self, lost = "pod-self", "pod-lost"

	instances := newMockInstanceRepo()
	logs := newMockDeployLogRepo()
	inst := &models.StackInstance{
		ID: "inst-1", Name: "demo", OwnerID: "owner-1", StackDefinitionID: "def-1",
		Status: models.StackStatusDeploying, UpdatedAt: now.Add(-time.Hour),
	}
	require.NoError(t, instances.Create(context.Background(), inst))
	deadline := now.Add(-time.Hour)
	require.NoError(t, logs.Create(context.Background(), &models.DeploymentLog{
		ID: "log-1", StackInstanceID: inst.ID, Action: models.DeployActionDeploy, Status: models.DeployLogRunning,
		StartedAt: deadline.Add(-30 * time.Minute), ReplicaID: lost, DeadlineAt: &deadline,
	}))
	heartbeats := newFakeHeartbeats()
	heartbeats.dbNow = now
	heartbeats.seen[self] = now
	heartbeats.seen[lost] = now.Add(-time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ops := &fakeInterruptOps{logs: logs, instances: instances, heartbeats: heartbeats, beforeInterrupt: cancel}
	audit := &fakeAuditRepo{}
	rec := NewInterruptRecovery(InterruptRecoveryConfig{
		Operations: ops, Heartbeats: heartbeats, Instances: instances,
		AuditLog: audit, Hub: &mockBroadcaster{}, Notifier: &mockNotifier{},
		SelfID: self,
	})
	require.NotNil(t, rec)
	rec.now = func() time.Time { return now }

	closed := rec.RunOnce(ctx)

	require.Error(t, ctx.Err(), "the term ended during the recovery")
	assert.Equal(t, 1, closed)
	require.Len(t, audit.all(), 1, "the interrupted operation is audited")
	assert.Equal(t, auditActionInterrupted, audit.all()[0].Action)
}

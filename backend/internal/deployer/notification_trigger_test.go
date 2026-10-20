package deployer

import (
	"context"
	"testing"
	"time"

	"backend/internal/hooks"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNotification_TriggerPerOperation checks that each finalize passes the
// trigger of its operation to the notifier (m.shutdownCtx has none), with
// the namespace and the branch in the target (issue #642).
func TestNotification_TriggerPerOperation(t *testing.T) {
	t.Parallel()

	devops := hooks.Trigger{Type: hooks.TriggerUser, ID: "devops-1", Name: "dan"}
	policy := hooks.Trigger{Type: hooks.TriggerCleanupPolicy, ID: "p-1", Name: "nightly"}

	tests := []struct {
		name     string
		trigger  *hooks.Trigger // nil: the context has no trigger
		run      func(ctx context.Context, mgr *Manager, inst *models.StackInstance) error
		wantType string
		wantTrig hooks.Trigger
	}{
		{
			name: "deploy by a devops user", trigger: &devops, wantType: "deployment.success", wantTrig: devops,
			run: func(ctx context.Context, mgr *Manager, inst *models.StackInstance) error {
				_, err := mgr.Deploy(ctx, DeployRequest{Instance: inst, Definition: &models.StackDefinition{ID: "def-1"}})
				return err
			},
		},
		{
			name: "deploy without a trigger in the context is a user trigger", wantType: "deployment.success",
			wantTrig: hooks.Trigger{Type: hooks.TriggerUser},
			run: func(ctx context.Context, mgr *Manager, inst *models.StackInstance) error {
				_, err := mgr.Deploy(ctx, DeployRequest{Instance: inst, Definition: &models.StackDefinition{ID: "def-1"}})
				return err
			},
		},
		{
			name: "stop by a cleanup policy", trigger: &policy, wantType: "deployment.stopped", wantTrig: policy,
			run: func(ctx context.Context, mgr *Manager, inst *models.StackInstance) error {
				_, err := mgr.StopWithCharts(ctx, inst, nil)
				return err
			},
		},
		{
			name: "clean by a devops user", trigger: &devops, wantType: "clean.completed", wantTrig: devops,
			run: func(ctx context.Context, mgr *Manager, inst *models.StackInstance) error {
				_, err := mgr.Clean(ctx, inst, nil)
				return err
			},
		},
		{
			name: "delete after clean by a cleanup policy", trigger: &policy, wantType: "instance.deleted", wantTrig: policy,
			run: func(ctx context.Context, mgr *Manager, inst *models.StackInstance) error {
				_, err := mgr.CleanForDelete(ctx, inst, nil)
				return err
			},
		},
		{
			name: "rollback by a devops user", trigger: &devops, wantType: "rollback.completed", wantTrig: devops,
			run: func(ctx context.Context, mgr *Manager, inst *models.StackInstance) error {
				_, err := mgr.Rollback(ctx, RollbackRequest{Instance: inst})
				return err
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			notif := &mockNotifier{}
			instanceRepo := newMockInstanceRepo()
			logRepo := newMockDeployLogRepo()
			mgr := NewManager(ManagerConfig{
				Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", time.Second)},
				InstanceRepo:  instanceRepo,
				DeployLogRepo: logRepo,
				TxRunner: &txRunnerWithBranchOverride{
					instanceRepo: instanceRepo, logRepo: logRepo, boRepo: noopBranchOverrideRepo{},
				},
				Hub:           &mockBroadcaster{},
				MaxConcurrent: 2,
				Notifier:      notif,
			})

			inst := seedInstance(t, instanceRepo, "inst-trig", "web", "owner-1")
			inst.Status = models.StackStatusRunning
			require.NoError(t, instanceRepo.Update(inst))

			ctx := context.Background()
			if tt.trigger != nil {
				ctx = hooks.WithTrigger(ctx, *tt.trigger)
			}
			require.NoError(t, tt.run(ctx, mgr, inst))

			notif.waitForCalls(t, 1)
			call := notif.getCalls()[0]
			assert.Equal(t, tt.wantType, call.Type)
			require.NotNil(t, call.Trigger, "the finalize passes the trigger of the operation")
			assert.Equal(t, tt.wantTrig, *call.Trigger)
			assert.Equal(t, "owner-1", call.Target.OwnerID)
			assert.Equal(t, "ns-inst-trig", call.Target.Namespace)
			assert.Equal(t, "main", call.Target.Branch)
			assert.Empty(t, call.SiteURL, "no k8s client: no site URL")
		})
	}
}

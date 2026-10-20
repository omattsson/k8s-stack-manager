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
// the namespace and the branch in the target (issue #642). Each operation
// sends one notification; a cleanup policy stop or clean has its own type
// (issue #651).
func TestNotification_TriggerPerOperation(t *testing.T) {
	t.Parallel()

	devops := hooks.Trigger{Type: hooks.TriggerUser, ID: "devops-1", Name: "dan"}
	policy := hooks.Trigger{Type: hooks.TriggerCleanupPolicy, ID: "p-1", Name: "nightly"}
	unnamedPolicy := hooks.Trigger{Type: hooks.TriggerCleanupPolicy, ID: "p-2"}
	reaper := hooks.Trigger{Type: hooks.TriggerTTL}
	stop := func(ctx context.Context, mgr *Manager, inst *models.StackInstance) error {
		_, err := mgr.StopWithCharts(ctx, inst, nil)
		return err
	}
	clean := func(ctx context.Context, mgr *Manager, inst *models.StackInstance) error {
		_, err := mgr.Clean(ctx, inst, nil)
		return err
	}

	tests := []struct {
		name      string
		trigger   *hooks.Trigger // nil: the context has no trigger
		run       func(ctx context.Context, mgr *Manager, inst *models.StackInstance) error
		wantType  string
		wantTitle string // empty: not checked
		wantMsg   string // empty: not checked
		wantTrig  hooks.Trigger
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
			name: "stop by a devops user", trigger: &devops, run: stop, wantTrig: devops,
			wantType: "deployment.stopped", wantTitle: "Stack stopped", wantMsg: "Stack web has been stopped",
		},
		{
			name: "stop by a cleanup policy has the policy type", trigger: &policy, run: stop, wantTrig: policy,
			wantType: "cleanup.policy.stop", wantTitle: "Stack stopped by a cleanup policy",
			wantMsg: `Stack web has been stopped by cleanup policy "nightly"`,
		},
		{
			name: "stop by a cleanup policy without a name", trigger: &unnamedPolicy, run: stop, wantTrig: unnamedPolicy,
			wantType: "cleanup.policy.stop", wantMsg: "Stack web has been stopped by a cleanup policy",
		},
		{
			// The reaper also sends stack.expired; that pair stays (#651).
			name: "stop by the TTL reaper keeps deployment.stopped", trigger: &reaper, run: stop, wantTrig: reaper,
			wantType: "deployment.stopped", wantTitle: "Stack stopped", wantMsg: "Stack web has been stopped",
		},
		{
			name: "clean by a devops user", trigger: &devops, run: clean, wantTrig: devops,
			wantType: "clean.completed", wantTitle: "Cleanup completed", wantMsg: "Stack web has been cleaned and returned to draft",
		},
		{
			name: "clean by a cleanup policy has the policy type", trigger: &policy, run: clean, wantTrig: policy,
			wantType: "cleanup.policy.clean", wantTitle: "Stack cleaned by a cleanup policy",
			wantMsg: `Stack web has been cleaned by cleanup policy "nightly" and returned to draft`,
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
			// Shutdown waits for the operation goroutine: no later notification.
			mgr.Shutdown()
			calls := notif.getCalls()
			require.Len(t, calls, 1, "one notification per operation")
			call := calls[0]
			assert.Equal(t, tt.wantType, call.Type)
			if tt.wantTitle != "" {
				assert.Equal(t, tt.wantTitle, call.Title)
			}
			if tt.wantMsg != "" {
				assert.Equal(t, tt.wantMsg, call.Message)
			}
			require.NotNil(t, call.Trigger, "the finalize passes the trigger of the operation")
			assert.Equal(t, tt.wantTrig, *call.Trigger)
			assert.Equal(t, "owner-1", call.Target.OwnerID)
			assert.Equal(t, "ns-inst-trig", call.Target.Namespace)
			assert.Equal(t, "main", call.Target.Branch)
			assert.Empty(t, call.SiteURL, "no k8s client: no site URL")
		})
	}
}

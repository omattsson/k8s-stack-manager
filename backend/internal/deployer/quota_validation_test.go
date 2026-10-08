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

// TestManager_ApplyNamespaceQuotas_InvalidEffectiveQuota checks that an
// invalid merged quota (cluster quota + instance override) fails with a clear
// message before any Kubernetes call (issue 463).
func TestManager_ApplyNamespaceQuotas_InvalidEffectiveQuota(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cluster  *models.ResourceQuotaConfig
		override *models.InstanceQuotaOverride
		wantErr  string
	}{
		{
			name:     "override request above cluster limit",
			cluster:  &models.ResourceQuotaConfig{CPULimit: "2"},
			override: &models.InstanceQuotaOverride{CPURequest: "4"},
			wantErr:  "invalid resource quota: cpu_request 4 (instance override) exceeds the effective cpu_limit 2 (cluster quota)",
		},
		{
			name:    "legacy invalid cluster quantity",
			cluster: &models.ResourceQuotaConfig{MemoryLimit: "abc"},
			wantErr: `invalid resource quota: memory_limit "abc" (cluster quota): invalid quantity`,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			instanceRepo := newMockInstanceRepo()
			inst := &models.StackInstance{ID: "inst-q", Name: "q", Namespace: "stack-q", ClusterID: "cluster-1", Status: models.StackStatusRunning}
			require.NoError(t, instanceRepo.Create(inst))

			mgr := NewManager(ManagerConfig{
				Registry:          &mockClusterResolver{helm: &mockHelmExecutor{}},
				InstanceRepo:      instanceRepo,
				DeployLogRepo:     newMockDeployLogRepo(),
				Hub:               &mockBroadcaster{},
				MaxConcurrent:     2,
				QuotaRepo:         &mockQuotaRepo{quota: tt.cluster},
				QuotaOverrideRepo: &mockQuotaOverrideRepo{override: tt.override},
			})

			err := mgr.applyNamespaceQuotas(context.Background(), inst.ID, inst.Namespace)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidQuota)
			assert.Equal(t, tt.wantErr, err.Error())
			assert.Equal(t, tt.wantErr, sanitizeDeployError(err), "message must reach the user unchanged")
		})
	}
}

// TestManager_Deploy_InvalidEffectiveQuotaFailsBeforeHelm checks that a deploy
// with an invalid merged quota ends in error with the clear message, does not
// fire the pre-deploy hook and installs no chart.
func TestManager_Deploy_InvalidEffectiveQuotaFailsBeforeHelm(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	inst := &models.StackInstance{
		ID: "inst-dq", StackDefinitionID: "def-1", Name: "dq", Namespace: "stack-dq",
		OwnerID: "user-1", Branch: "main", ClusterID: "cluster-1", Status: models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	helmExec := &mockHelmExecutor{}
	rec := newHookRecorder(t)
	mgr := NewManager(ManagerConfig{
		Registry:          &mockClusterResolver{helm: helmExec},
		InstanceRepo:      instanceRepo,
		DeployLogRepo:     logRepo,
		TxRunner:          &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:               &mockBroadcaster{},
		MaxConcurrent:     2,
		Hooks:             rec.dispatcherFor(t, hooks.FailurePolicyFail),
		QuotaRepo:         &mockQuotaRepo{quota: &models.ResourceQuotaConfig{MemoryLimit: "1Gi"}},
		QuotaOverrideRepo: &mockQuotaOverrideRepo{override: &models.InstanceQuotaOverride{MemoryRequest: "2Gi"}},
	})

	logID, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "test-def"},
		Charts: []ChartDeployInfo{{
			ChartConfig: models.ChartConfig{ID: "c1", ChartName: "app", ChartPath: "app"},
			ValuesYAML:  []byte("a: 1\n"),
		}},
	})
	require.NoError(t, err)

	want := "invalid resource quota: memory_request 2Gi (instance override) exceeds the effective memory_limit 1Gi (cluster quota)"
	require.Eventually(t, func() bool {
		l, err := logRepo.FindByID(context.Background(), logID)
		return err == nil && l.Status == models.DeployLogError
	}, 3*time.Second, 20*time.Millisecond)

	l, err := logRepo.FindByID(context.Background(), logID)
	require.NoError(t, err)
	assert.Equal(t, want, l.ErrorMessage)
	final, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusError, final.Status)
	assert.Equal(t, want, final.ErrorMessage)

	assert.NotContains(t, rec.eventNames(), hooks.EventPreDeploy, "the pre-deploy hook must not run for a deploy that will fail")

	helmExec.mu.Lock()
	defer helmExec.mu.Unlock()
	assert.Empty(t, helmExec.installCalls, "no chart may be installed")
}

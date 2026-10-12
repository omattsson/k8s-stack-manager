package deployer

import (
	"context"
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestManager_Deploy_ChartSourceNotAllowed sets the process allowlist, so it
// does not run in parallel (parallel tests wait until the sequential tests
// end).
func TestManager_Deploy_ChartSourceNotAllowed(t *testing.T) {
	models.SetAllowedChartSources([]string{"oci://registry.example.com/charts"})
	t.Cleanup(func() { models.SetAllowedChartSources(nil) })

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	helm := &mockHelmExecutor{}

	inst := &models.StackInstance{
		ID:                "inst-src",
		StackDefinitionID: "def-1",
		Name:              "src-instance",
		Namespace:         "stack-src-user",
		OwnerID:           "user-1",
		Branch:            "main",
		Status:            models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: helm, noK8sClient: true},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
	})

	logID, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "test-def"},
		Charts: []ChartDeployInfo{{ChartConfig: models.ChartConfig{
			ID: "c1", ChartName: "web", RepositoryURL: "https://charts.example.net", DeployOrder: 1,
		}}},
	})
	require.NoError(t, err)
	waitForTerminalStatus(t, instanceRepo, inst.ID)

	helm.mu.Lock()
	calls := len(helm.installCalls)
	helm.mu.Unlock()
	assert.Zero(t, calls, "a chart outside the allowlist must not be installed")

	final, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusError, final.Status)
	finalLog, err := logRepo.FindByID(context.Background(), logID)
	require.NoError(t, err)
	assert.Contains(t, finalLog.Output, "chart source not allowed")
}

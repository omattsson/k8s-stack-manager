package deployer

import (
	"context"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHelmClient_ListReleases_InvalidBinary(t *testing.T) {
	t.Parallel()

	client := NewHelmClient("/nonexistent/helm", "", 1*time.Minute)

	releases, err := client.ListReleases(context.Background(), "test-ns")
	assert.Error(t, err)
	assert.Nil(t, releases)
	assert.Contains(t, err.Error(), "helm list")
}

func TestHelmClient_ListReleases_EmptyOutput(t *testing.T) {
	t.Parallel()

	// Use "true" as the binary to get empty output with success.
	client := NewHelmClient("true", "", 1*time.Minute)

	releases, err := client.ListReleases(context.Background(), "test-ns")
	require.NoError(t, err)
	assert.Empty(t, releases)
}

func TestHelmClient_ListReleases_ParsesOutput(t *testing.T) {
	t.Parallel()

	// Use echo to simulate helm list output with release names.
	// "echo" will output the arguments as a single line.
	echoClient := NewHelmClient("echo", "", 1*time.Minute)
	releases, err := echoClient.ListReleases(context.Background(), "test-ns")
	require.NoError(t, err)
	// echo outputs the arguments as a single line: "--kubeconfig  list -n test-ns -q"
	// or if no kubeconfig: "list -n test-ns -q" which is treated as one release name.
	assert.NotEmpty(t, releases)
}

func TestHelmClient_Install_WithRepoAndVersion(t *testing.T) {
	t.Parallel()

	client := NewHelmClient("/nonexistent/helm", "/path/to/kubeconfig", 1*time.Minute)

	_, err := client.Install(context.Background(), InstallRequest{
		ReleaseName: "test-release",
		ChartPath:   "nginx",
		RepoURL:     "https://charts.example.com",
		Version:     "1.2.3",
		ValuesFile:  "/tmp/values.yaml",
		Namespace:   "test-ns",
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "helm command failed")
}

func TestManager_Shutdown(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		MaxConcurrent: 2,
	})

	// Shutdown should not panic even with no active goroutines.
	mgr.Shutdown()

	// After shutdown, new deploys should be rejected.
	assert.True(t, mgr.shuttingDown.Load())
}

func TestManager_Deploy_ShuttingDown(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		MaxConcurrent: 2,
	})

	mgr.Shutdown()

	_, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance: &models.StackInstance{ID: "inst-1"},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "shutting down")
}

func TestManager_StopWithCharts_ShuttingDown(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		MaxConcurrent: 2,
	})

	mgr.Shutdown()

	_, err := mgr.StopWithCharts(context.Background(), &models.StackInstance{ID: "inst-1"}, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "shutting down")
}

func TestManager_Clean_ShuttingDown(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		MaxConcurrent: 2,
	})

	mgr.Shutdown()

	_, err := mgr.Clean(context.Background(), &models.StackInstance{ID: "inst-1"}, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "shutting down")
}

func TestManager_Deploy_NilRegistry(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	mgr := NewManager(ManagerConfig{
		Registry:      nil,
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		MaxConcurrent: 2,
	})

	_, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance: &models.StackInstance{ID: "inst-1"},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cluster registry is not configured")
}

func TestManager_Deploy_CancelledContext(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		MaxConcurrent: 2,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := mgr.Deploy(ctx, DeployRequest{
		Instance: &models.StackInstance{ID: "inst-1"},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "request cancelled")
}

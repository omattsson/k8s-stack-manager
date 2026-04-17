package deployer

import (
	"context"
	"fmt"
	"testing"
	"time"

	"backend/internal/k8s"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// buildRefreshDBFakeCluster builds a fake clientset preloaded with the
// Deployments the refresh-db flow expects to manipulate, plus a matching
// Redis pod, so scale/wait/exec calls can exercise happy paths without a
// real API server.
func buildRefreshDBFakeCluster(t *testing.T, namespace string) *fake.Clientset {
	t.Helper()

	mkDeploy := func(name string) *appsv1.Deployment {
		replicas := int32(1)
		labels := map[string]string{"app": name}
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:       name,
				Namespace:  namespace,
				Generation: 1,
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Selector: &metav1.LabelSelector{MatchLabels: labels},
			},
			Status: appsv1.DeploymentStatus{
				ObservedGeneration: 1,
				Conditions: []appsv1.DeploymentCondition{{
					Type:   appsv1.DeploymentAvailable,
					Status: corev1.ConditionTrue,
				}},
			},
		}
	}

	return fake.NewSimpleClientset(
		mkDeploy("kvk-core"),
		mkDeploy("kvk-storefront"),
		mkDeploy("kvk-mysql"),
		mkDeploy("kvk-redis"),
	)
}

func TestManager_RefreshDB_CancelledContext(t *testing.T) {
	t.Parallel()

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{k8sClient: k8s.NewClientFromInterface(fake.NewSimpleClientset())},
		InstanceRepo:  newMockInstanceRepo(),
		DeployLogRepo: newMockDeployLogRepo(),
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := mgr.RefreshDB(ctx, runningInstance("inst-refresh-cancel"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "request cancelled")
}

func TestManager_RefreshDB_NilRegistry(t *testing.T) {
	t.Parallel()

	mgr := NewManager(ManagerConfig{
		Registry:      nil,
		InstanceRepo:  newMockInstanceRepo(),
		DeployLogRepo: newMockDeployLogRepo(),
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
	})

	_, err := mgr.RefreshDB(context.Background(), runningInstance("inst-refresh-nilreg"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cluster registry is not configured")
}

// runningInstance returns a minimal StackInstance that passes RefreshDB's
// status gate, so individual tests can focus on a single failure mode.
func runningInstance(id string) *models.StackInstance {
	return &models.StackInstance{ID: id, Status: models.StackStatusRunning}
}

// fullyConfiguredManagerConfig sets the four required RefreshDB fields to
// neutral placeholder values so tests past the config gate.
func fullyConfiguredManagerConfig(base ManagerConfig) ManagerConfig {
	base.RefreshDBScaleTargets = []string{"app-core"}
	base.RefreshDBMysqlRelease = "app-mysql"
	base.RefreshDBRedisRelease = "app-redis"
	base.RefreshDBSyncJobName = "app-sync"
	return base
}

func TestManager_RefreshDB_K8sClientError(t *testing.T) {
	t.Parallel()

	mgr := NewManager(fullyConfiguredManagerConfig(ManagerConfig{
		Registry: &mockClusterResolver{
			k8sErr: fmt.Errorf("no k8s client"),
		},
		InstanceRepo:  newMockInstanceRepo(),
		DeployLogRepo: newMockDeployLogRepo(),
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
	}))

	_, err := mgr.RefreshDB(context.Background(), runningInstance("inst-refresh-k8serr"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "getting cluster k8s client")
}

func TestManager_RefreshDB_NilK8sClient(t *testing.T) {
	t.Parallel()

	mgr := NewManager(fullyConfiguredManagerConfig(ManagerConfig{
		Registry:      &mockClusterResolver{k8sClient: nil},
		InstanceRepo:  newMockInstanceRepo(),
		DeployLogRepo: newMockDeployLogRepo(),
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
	}))

	_, err := mgr.RefreshDB(context.Background(), runningInstance("inst-refresh-nilk8s"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "k8s client is nil")
}

func TestManager_RefreshDB_NotRunning(t *testing.T) {
	t.Parallel()

	mgr := NewManager(fullyConfiguredManagerConfig(ManagerConfig{
		Registry:      &mockClusterResolver{k8sClient: k8s.NewClientFromInterface(fake.NewSimpleClientset())},
		InstanceRepo:  newMockInstanceRepo(),
		DeployLogRepo: newMockDeployLogRepo(),
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
	}))

	// Instance not in Running state — defense-in-depth gate should trip
	// before any cluster-mutating work happens.
	_, err := mgr.RefreshDB(context.Background(), &models.StackInstance{
		ID:     "inst-refresh-draft",
		Status: models.StackStatusDraft,
	})
	assert.ErrorIs(t, err, ErrRefreshDBInstanceNotRunning)
}

func TestManager_RefreshDB_NotConfigured(t *testing.T) {
	t.Parallel()

	// Omit RefreshDB* fields entirely — should refuse to run.
	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{k8sClient: k8s.NewClientFromInterface(fake.NewSimpleClientset())},
		InstanceRepo:  newMockInstanceRepo(),
		DeployLogRepo: newMockDeployLogRepo(),
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
	})

	_, err := mgr.RefreshDB(context.Background(), runningInstance("inst-refresh-unconfigured"))
	assert.ErrorIs(t, err, ErrRefreshDBNotConfigured)
}

func TestManager_RefreshDB_Success(t *testing.T) {
	t.Parallel()

	namespace := "stack-refresh-ok"
	cs := buildRefreshDBFakeCluster(t, namespace)

	// Drive the cleanup Job to Complete from a goroutine (fake clientset has
	// no batch controller). Subsequent MySQL Available wait succeeds against
	// the pre-seeded status.
	go func() {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			j, err := cs.BatchV1().Jobs(namespace).Get(context.Background(), "kvk-mysql-pvc-cleanup", metav1.GetOptions{})
			if err == nil {
				j.Status.Conditions = []batchv1.JobCondition{{
					Type:   batchv1.JobComplete,
					Status: corev1.ConditionTrue,
				}}
				_, _ = cs.BatchV1().Jobs(namespace).UpdateStatus(context.Background(), j, metav1.UpdateOptions{})
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()

	k8sClient := k8s.NewClientFromInterface(cs)

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:        "inst-refresh-ok",
		Name:      "refresh-ok",
		Namespace: namespace,
		Status:    models.StackStatusRunning,
	}
	require.NoError(t, instanceRepo.Create(inst))

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{k8sClient: k8sClient},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           hub,
		MaxConcurrent: 2,

		// Narrow the scale target list so we don't wait on missing deployments.
		RefreshDBScaleTargets: []string{"kvk-core", "kvk-storefront", "not-present"},
		RefreshDBMysqlRelease: "kvk-mysql",
		RefreshDBRedisRelease: "kvk-redis",
		RefreshDBSyncJobName:  "kvk-storefront-sync",
		RefreshDBCleanupImage: "alpine:3.20",
	})

	logID, err := mgr.RefreshDB(context.Background(), inst)
	require.NoError(t, err)
	require.NotEmpty(t, logID)

	// Initial state should be deploying + a refresh-db log running.
	log, err := logRepo.FindByID(context.Background(), logID)
	require.NoError(t, err)
	assert.Equal(t, models.DeployActionRefreshDB, log.Action)
	assert.Equal(t, models.DeployLogRunning, log.Status)

	updated, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusDeploying, updated.Status)

	// Wait for the background orchestrator to complete.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		finalLog, err := logRepo.FindByID(context.Background(), logID)
		if err == nil && finalLog.Status != models.DeployLogRunning {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	finalLog, err := logRepo.FindByID(context.Background(), logID)
	require.NoError(t, err)
	assert.Equal(t, models.DeployLogSuccess, finalLog.Status, "refresh-db should succeed on happy-path fake cluster; output=%s", finalLog.Output)
	assert.NotNil(t, finalLog.CompletedAt)
	assert.Contains(t, finalLog.Output, "refresh-db completed successfully")

	final, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusRunning, final.Status)
	assert.Empty(t, final.ErrorMessage)

	// Verify final scale of app deployments is 1 (back up from 0).
	for _, target := range []string{"kvk-core", "kvk-storefront"} {
		got, err := cs.AppsV1().Deployments(namespace).Get(context.Background(), target, metav1.GetOptions{})
		require.NoError(t, err)
		require.NotNil(t, got.Spec.Replicas, "deployment %s missing replicas", target)
		assert.Equal(t, int32(1), *got.Spec.Replicas, "deployment %s should be scaled back to 1", target)
	}

	// Verify MySQL is back to 1.
	mysql, err := cs.AppsV1().Deployments(namespace).Get(context.Background(), "kvk-mysql", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, mysql.Spec.Replicas)
	assert.Equal(t, int32(1), *mysql.Spec.Replicas)

	// Verify broadcasts were sent.
	assert.Greater(t, hub.messageCount(), 0)
}

func TestManager_RefreshDB_MissingMysqlDeployment(t *testing.T) {
	t.Parallel()

	namespace := "stack-refresh-nomysql"

	// No MySQL deployment in this cluster — step 2's scale will fail.
	cs := fake.NewSimpleClientset()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:        "inst-refresh-nomysql",
		Name:      "refresh-nomysql",
		Namespace: namespace,
		Status:    models.StackStatusRunning,
	}
	require.NoError(t, instanceRepo.Create(inst))

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{k8sClient: k8s.NewClientFromInterface(cs)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           hub,
		MaxConcurrent: 2,

		// Scale targets must be non-empty to satisfy the config gate, but the
		// named deployments don't exist in the cluster so they'll be treated
		// as ErrDeploymentNotFound and skipped. The real failure surfaces in
		// step 2 when MySQL is missing.
		RefreshDBScaleTargets: []string{"app-core"},
		RefreshDBMysqlRelease: "kvk-mysql",
		RefreshDBRedisRelease: "kvk-redis",
		RefreshDBSyncJobName:  "kvk-storefront-sync",
		RefreshDBCleanupImage: "alpine:3.20",
	})

	logID, err := mgr.RefreshDB(context.Background(), inst)
	require.NoError(t, err)

	// Wait for async failure.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		finalLog, err := logRepo.FindByID(context.Background(), logID)
		if err == nil && finalLog.Status != models.DeployLogRunning {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	finalLog, err := logRepo.FindByID(context.Background(), logID)
	require.NoError(t, err)
	assert.Equal(t, models.DeployLogError, finalLog.Status)

	final, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusError, final.Status)
	assert.NotEmpty(t, final.ErrorMessage)
}

func TestManager_refreshDBConfig_Defaults(t *testing.T) {
	t.Parallel()

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{},
		InstanceRepo:  newMockInstanceRepo(),
		DeployLogRepo: newMockDeployLogRepo(),
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 1,
	})

	// With no config wired in, refreshDBConfig should return empty required
	// fields (Manager.RefreshDB will reject the call with ErrRefreshDBNotConfigured
	// before this ever runs in production). Only the cleanup image has a
	// generic fallback.
	targets, mysql, redis, sync, image := mgr.refreshDBConfig()
	assert.Empty(t, targets)
	assert.Empty(t, mysql)
	assert.Empty(t, redis)
	assert.Empty(t, sync)
	assert.Equal(t, "alpine:3.20", image)
}

package cluster

import (
	"context"
	"fmt"
	"testing"
	"time"

	"backend/internal/k8s"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// mockInstanceRepo implements models.StackInstanceRepository for testing.
type mockInstanceRepo struct {
	instances []models.StackInstance
}

func (m *mockInstanceRepo) Create(_ *models.StackInstance) error                   { return nil }
func (m *mockInstanceRepo) FindByID(_ string) (*models.StackInstance, error)       { return nil, fmt.Errorf("not found") }
func (m *mockInstanceRepo) FindByNamespace(_ string) (*models.StackInstance, error) { return nil, nil }
func (m *mockInstanceRepo) Update(_ *models.StackInstance) error                   { return nil }
func (m *mockInstanceRepo) Delete(_ string) error                                  { return nil }
func (m *mockInstanceRepo) List() ([]models.StackInstance, error)                  { return nil, nil }
func (m *mockInstanceRepo) ListPaged(_, _ int) ([]models.StackInstance, int, error) { return nil, 0, nil }
func (m *mockInstanceRepo) ListByOwner(_ string) ([]models.StackInstance, error)   { return nil, nil }
func (m *mockInstanceRepo) FindByName(_ string) ([]models.StackInstance, error)    { return nil, nil }
func (m *mockInstanceRepo) FindByCluster(clusterID string) ([]models.StackInstance, error) {
	var result []models.StackInstance
	for _, inst := range m.instances {
		if inst.ClusterID == clusterID {
			result = append(result, inst)
		}
	}
	return result, nil
}
func (m *mockInstanceRepo) CountByClusterAndOwner(_, _ string) (int, error) { return 0, nil }
func (m *mockInstanceRepo) CountAll() (int, error)                           { return 0, nil }
func (m *mockInstanceRepo) CountByStatus(_ string) (int, error)              { return 0, nil }
func (m *mockInstanceRepo) CountByDefinitionIDs(_ []string) (map[string]int, error) { return nil, nil }
func (m *mockInstanceRepo) CountByOwnerIDs(_ []string) (map[string]int, error)     { return nil, nil }
func (m *mockInstanceRepo) ListIDsByDefinitionIDs(_ []string) (map[string][]string, error) { return nil, nil }
func (m *mockInstanceRepo) ListIDsByOwnerIDs(_ []string) (map[string][]string, error)     { return nil, nil }
func (m *mockInstanceRepo) ExistsByDefinitionAndStatus(_, _ string) (bool, error) { return false, nil }
func (m *mockInstanceRepo) ListExpired() ([]*models.StackInstance, error)    { return nil, nil }

func TestSecretRefresher_RefreshesRunningInstances(t *testing.T) {
	t.Parallel()

	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-ns-1"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-ns-2"}},
	)

	clusterRepo := newMockClusterRepo()
	clusterRepo.clusters["cluster-1"] = &models.Cluster{
		ID:                  "cluster-1",
		Name:                "dev",
		UseInCluster:        true,
		RegistryURL:         "myacr.azurecr.io",
		RegistryUsername:    "user",
		RegistryPassword:    "token123",
		ImagePullSecretName: "acr-pull-secret",
	}

	instanceRepo := &mockInstanceRepo{
		instances: []models.StackInstance{
			{ID: "inst-1", ClusterID: "cluster-1", Namespace: "stack-ns-1", Status: models.StackStatusRunning},
			{ID: "inst-2", ClusterID: "cluster-1", Namespace: "stack-ns-2", Status: models.StackStatusStopped},
		},
	}

	k8sClient := k8s.NewClientFromInterface(cs)
	registry := NewRegistryForTest("cluster-1", k8sClient, nil)

	refresher := NewSecretRefresher(SecretRefresherConfig{
		ClusterRepo:  clusterRepo,
		InstanceRepo: instanceRepo,
		Registry:     registry,
		Interval:     100 * time.Millisecond,
	})

	refresher.refresh()

	got, err := cs.CoreV1().Secrets("stack-ns-1").Get(context.Background(), "acr-pull-secret", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, corev1.SecretTypeDockerConfigJson, got.Type)
	assert.Contains(t, string(got.Data[corev1.DockerConfigJsonKey]), "myacr.azurecr.io")

	_, err = cs.CoreV1().Secrets("stack-ns-2").Get(context.Background(), "acr-pull-secret", metav1.GetOptions{})
	assert.Error(t, err, "stopped instance should not get a pull secret")
}

func TestSecretRefresher_SkipsClusterWithoutRegistry(t *testing.T) {
	t.Parallel()

	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-ns-1"}},
	)

	clusterRepo := newMockClusterRepo()
	clusterRepo.clusters["cluster-1"] = &models.Cluster{
		ID:           "cluster-1",
		Name:         "dev",
		UseInCluster: true,
	}

	instanceRepo := &mockInstanceRepo{
		instances: []models.StackInstance{
			{ID: "inst-1", ClusterID: "cluster-1", Namespace: "stack-ns-1", Status: models.StackStatusRunning},
		},
	}

	k8sClient := k8s.NewClientFromInterface(cs)
	registry := NewRegistryForTest("cluster-1", k8sClient, nil)

	refresher := NewSecretRefresher(SecretRefresherConfig{
		ClusterRepo:  clusterRepo,
		InstanceRepo: instanceRepo,
		Registry:     registry,
	})

	refresher.refresh()

	secrets, err := cs.CoreV1().Secrets("stack-ns-1").List(context.Background(), metav1.ListOptions{})
	assert.NoError(t, err)
	assert.Empty(t, secrets.Items)
}

func TestSecretRefresher_DefaultSecretName(t *testing.T) {
	t.Parallel()

	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-ns-1"}},
	)

	clusterRepo := newMockClusterRepo()
	clusterRepo.clusters["cluster-1"] = &models.Cluster{
		ID:               "cluster-1",
		Name:             "dev",
		UseInCluster:     true,
		RegistryURL:      "myacr.azurecr.io",
		RegistryUsername: "user",
		RegistryPassword: "pass",
		// ImagePullSecretName left empty — should default to "registry-pull-secret"
	}

	instanceRepo := &mockInstanceRepo{
		instances: []models.StackInstance{
			{ID: "inst-1", ClusterID: "cluster-1", Namespace: "stack-ns-1", Status: models.StackStatusRunning},
		},
	}

	k8sClient := k8s.NewClientFromInterface(cs)
	registry := NewRegistryForTest("cluster-1", k8sClient, nil)

	refresher := NewSecretRefresher(SecretRefresherConfig{
		ClusterRepo:  clusterRepo,
		InstanceRepo: instanceRepo,
		Registry:     registry,
	})

	refresher.refresh()

	got, err := cs.CoreV1().Secrets("stack-ns-1").Get(context.Background(), "registry-pull-secret", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, corev1.SecretTypeDockerConfigJson, got.Type)
}

func TestSecretRefresher_StartStop(t *testing.T) {
	t.Parallel()

	refresher := NewSecretRefresher(SecretRefresherConfig{
		ClusterRepo:  newMockClusterRepo(),
		InstanceRepo: &mockInstanceRepo{},
		Interval:     1 * time.Hour,
	})

	refresher.Start()
	refresher.Start() // idempotent

	refresher.Stop()
	refresher.Stop() // safe to call twice
}

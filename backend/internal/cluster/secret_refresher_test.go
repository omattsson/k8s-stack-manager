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
	// onFindByCluster runs before FindByCluster checks ctx (for example a cancel).
	onFindByCluster  func()
	findByClusterErr error
	instances        []models.StackInstance
}

func (m *mockInstanceRepo) Create(ctx context.Context, _ *models.StackInstance) error {
	return ctx.Err()
}
func (m *mockInstanceRepo) FindByID(ctx context.Context, _ string) (*models.StackInstance, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("not found")
}
func (m *mockInstanceRepo) FindByNamespace(ctx context.Context, _ string) (*models.StackInstance, error) {
	return nil, ctx.Err()
}
func (m *mockInstanceRepo) Update(ctx context.Context, _ *models.StackInstance) error {
	return ctx.Err()
}
func (m *mockInstanceRepo) Delete(ctx context.Context, _ string) error { return ctx.Err() }
func (m *mockInstanceRepo) List(ctx context.Context) ([]models.StackInstance, error) {
	return nil, ctx.Err()
}
func (m *mockInstanceRepo) ListPaged(ctx context.Context, _ models.StackInstanceFilter, _, _ int) ([]models.StackInstance, int, error) {
	return nil, 0, ctx.Err()
}
func (m *mockInstanceRepo) ListByOwner(ctx context.Context, _ string) ([]models.StackInstance, error) {
	return nil, ctx.Err()
}
func (m *mockInstanceRepo) FindByName(ctx context.Context, _ string) ([]models.StackInstance, error) {
	return nil, ctx.Err()
}
func (m *mockInstanceRepo) FindByCluster(ctx context.Context, clusterID string) ([]models.StackInstance, error) {
	if m.onFindByCluster != nil {
		m.onFindByCluster()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m.findByClusterErr != nil {
		return nil, m.findByClusterErr
	}
	var result []models.StackInstance
	for _, inst := range m.instances {
		if inst.ClusterID == clusterID {
			result = append(result, inst)
		}
	}
	return result, nil
}
func (m *mockInstanceRepo) CountForInstanceLimit(ctx context.Context, _, _ string, _ bool) (int, error) {
	return 0, ctx.Err()
}
func (m *mockInstanceRepo) CountAll(ctx context.Context) (int, error) { return 0, ctx.Err() }
func (m *mockInstanceRepo) CountByStatus(ctx context.Context, _ string) (int, error) {
	return 0, ctx.Err()
}
func (m *mockInstanceRepo) CountByDefinitionIDs(ctx context.Context, _ []string) (map[string]int, error) {
	return nil, ctx.Err()
}
func (m *mockInstanceRepo) CountByOwnerIDs(ctx context.Context, _ []string) (map[string]int, error) {
	return nil, ctx.Err()
}
func (m *mockInstanceRepo) ListIDsByDefinitionIDs(ctx context.Context, _ []string) (map[string][]string, error) {
	return nil, ctx.Err()
}
func (m *mockInstanceRepo) ListIDsByOwnerIDs(ctx context.Context, _ []string) (map[string][]string, error) {
	return nil, ctx.Err()
}
func (m *mockInstanceRepo) ExistsByDefinitionAndStatus(ctx context.Context, _, _ string) (bool, error) {
	return false, ctx.Err()
}
func (m *mockInstanceRepo) ListExpired(ctx context.Context) ([]*models.StackInstance, error) {
	return nil, ctx.Err()
}
func (*mockInstanceRepo) FindByIDs(ctx context.Context, _ []string) (map[string]*models.StackInstance, error) {
	return nil, ctx.Err()
}

func (m *mockInstanceRepo) ListExpiringSoon(ctx context.Context, _ time.Duration) ([]*models.StackInstance, error) {
	return nil, ctx.Err()
}
func (*mockInstanceRepo) MarkExpiryWarned(ctx context.Context, _ string, _, _ time.Time) (bool, error) {
	return false, ctx.Err()
}
func (m *mockInstanceRepo) ListByStatus(ctx context.Context, _ string, _ int) ([]*models.StackInstance, error) {
	return nil, ctx.Err()
}

func TestSecretRefresher_RefreshesRunningInstances(t *testing.T) {
	t.Parallel()

	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-ns-1"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-ns-2"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-ns-3"}},
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
			{ID: "inst-3", ClusterID: "cluster-1", Namespace: "stack-ns-3", Status: models.StackStatusDeploying},
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

	refresher.refresh(context.Background())

	got, err := cs.CoreV1().Secrets("stack-ns-1").Get(context.Background(), "acr-pull-secret", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, corev1.SecretTypeDockerConfigJson, got.Type)
	assert.Contains(t, string(got.Data[corev1.DockerConfigJsonKey]), "myacr.azurecr.io")

	_, err = cs.CoreV1().Secrets("stack-ns-2").Get(context.Background(), "acr-pull-secret", metav1.GetOptions{})
	assert.Error(t, err, "stopped instance should not get a pull secret")

	got3, err := cs.CoreV1().Secrets("stack-ns-3").Get(context.Background(), "acr-pull-secret", metav1.GetOptions{})
	require.NoError(t, err, "deploying instance should get a pull secret")
	assert.Equal(t, corev1.SecretTypeDockerConfigJson, got3.Type)
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

	refresher.refresh(context.Background())

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

	refresher.refresh(context.Background())

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

func (*mockInstanceRepo) CountByStatuses(ctx context.Context, statuses []string) (int, error) {
	return 0, ctx.Err()
}

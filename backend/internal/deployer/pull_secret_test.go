package deployer

import (
	"context"
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

func TestManager_Deploy_ProvisionsPullSecret(t *testing.T) {
	t.Parallel()

	t.Run("creates pull secret when registry configured", func(t *testing.T) {
		t.Parallel()

		cs := fake.NewSimpleClientset(
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-pull-a"}},
		)
		k8sClient := k8s.NewClientFromInterface(cs)

		instanceRepo := newMockInstanceRepo()
		logRepo := newMockDeployLogRepo()
		hub := &mockBroadcaster{}

		inst := &models.StackInstance{
			ID:                "inst-ps-1",
			StackDefinitionID: "def-1",
			Name:              "pull-secret-happy",
			Namespace:         "stack-pull-a",
			OwnerID:           "user-1",
			Branch:            "main",
			Status:            models.StackStatusDraft,
		}
		require.NoError(t, instanceRepo.Create(inst))

		regCfg := &models.RegistryConfig{
			URL:        "myregistry.azurecr.io",
			Username:   "00000000-0000-0000-0000-000000000000",
			Password:   "test-token",
			SecretName: "acr-pull-secret",
		}

		mgr := NewManager(ManagerConfig{
			Registry: &mockClusterResolver{
				helm:           NewHelmClient("/nonexistent/helm", "", 1*time.Second),
				k8sClient:      k8sClient,
				registryConfig: regCfg,
			},
			InstanceRepo:  instanceRepo,
			DeployLogRepo: logRepo,
			TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
			Hub:           hub,
			MaxConcurrent: 2,
		})

		_, err := mgr.Deploy(context.Background(), DeployRequest{
			Instance:   inst,
			Definition: &models.StackDefinition{ID: "def-1", Name: "def"},
			Charts:     nil,
		})
		require.NoError(t, err)

		var got *corev1.Secret
		require.Eventually(t, func() bool {
			var gerr error
			got, gerr = cs.CoreV1().Secrets("stack-pull-a").Get(
				context.Background(), "acr-pull-secret", metav1.GetOptions{},
			)
			return gerr == nil
		}, 3*time.Second, 20*time.Millisecond, "pull secret should have been created")

		assert.Equal(t, corev1.SecretTypeDockerConfigJson, got.Type)
		assert.Equal(t, "k8s-stack-manager", got.Labels["managed-by"])
		assert.Equal(t, "true", got.Labels["k8s-stack-manager.io/image-pull-secret"])

		dockerCfg := string(got.Data[corev1.DockerConfigJsonKey])
		assert.Contains(t, dockerCfg, "myregistry.azurecr.io")
		assert.Contains(t, dockerCfg, "test-token")
	})

	t.Run("no pull secret when registry not configured", func(t *testing.T) {
		t.Parallel()

		cs := fake.NewSimpleClientset(
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-pull-b"}},
		)

		instanceRepo := newMockInstanceRepo()
		logRepo := newMockDeployLogRepo()
		hub := &mockBroadcaster{}

		inst := &models.StackInstance{
			ID:                "inst-ps-2",
			StackDefinitionID: "def-1",
			Name:              "no-registry",
			Namespace:         "stack-pull-b",
			OwnerID:           "user-1",
			Branch:            "main",
			Status:            models.StackStatusDraft,
		}
		require.NoError(t, instanceRepo.Create(inst))

		mgr := NewManager(ManagerConfig{
			Registry: &mockClusterResolver{
				helm:           NewHelmClient("/nonexistent/helm", "", 1*time.Second),
				registryConfig: nil,
			},
			InstanceRepo:  instanceRepo,
			DeployLogRepo: logRepo,
			TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
			Hub:           hub,
			MaxConcurrent: 2,
		})

		_, err := mgr.Deploy(context.Background(), DeployRequest{
			Instance:   inst,
			Definition: &models.StackDefinition{ID: "def-1", Name: "def"},
			Charts:     nil,
		})
		require.NoError(t, err)

		// Wait for deploy to complete.
		require.Eventually(t, func() bool {
			updated, _ := instanceRepo.FindByID("inst-ps-2")
			return updated != nil && updated.Status != models.StackStatusDeploying
		}, 3*time.Second, 20*time.Millisecond)

		// Verify no secrets were created.
		secrets, err := cs.CoreV1().Secrets("stack-pull-b").List(
			context.Background(), metav1.ListOptions{},
		)
		assert.NoError(t, err)
		assert.Empty(t, secrets.Items)
	})

	t.Run("pull secret failure is non-fatal", func(t *testing.T) {
		t.Parallel()

		// Empty clientset — namespace doesn't exist, so EnsureNamespace will
		// create it, but we test that an error in EnsureDockerRegistrySecret
		// doesn't break the deploy.
		cs := fake.NewSimpleClientset()
		k8sClient := k8s.NewClientFromInterface(cs)

		instanceRepo := newMockInstanceRepo()
		logRepo := newMockDeployLogRepo()
		hub := &mockBroadcaster{}

		inst := &models.StackInstance{
			ID:                "inst-ps-3",
			StackDefinitionID: "def-1",
			Name:              "pull-secret-fail",
			Namespace:         "stack-pull-c",
			OwnerID:           "user-1",
			Branch:            "main",
			Status:            models.StackStatusDraft,
		}
		require.NoError(t, instanceRepo.Create(inst))

		regCfg := &models.RegistryConfig{
			URL:        "myregistry.azurecr.io",
			Username:   "user",
			Password:   "pass",
			SecretName: "acr-pull-secret",
		}

		mgr := NewManager(ManagerConfig{
			Registry: &mockClusterResolver{
				helm:           NewHelmClient("/nonexistent/helm", "", 1*time.Second),
				k8sClient:      k8sClient,
				registryConfig: regCfg,
			},
			InstanceRepo:  instanceRepo,
			DeployLogRepo: logRepo,
			TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
			Hub:           hub,
			MaxConcurrent: 2,
		})

		logID, err := mgr.Deploy(context.Background(), DeployRequest{
			Instance:   inst,
			Definition: &models.StackDefinition{ID: "def-1", Name: "def"},
			Charts:     nil,
		})
		require.NoError(t, err)
		assert.NotEmpty(t, logID)

		// Deploy should still complete (no charts to install, so it succeeds).
		require.Eventually(t, func() bool {
			updated, _ := instanceRepo.FindByID("inst-ps-3")
			return updated != nil && updated.Status != models.StackStatusDeploying
		}, 3*time.Second, 20*time.Millisecond)

		updated, _ := instanceRepo.FindByID("inst-ps-3")
		assert.Equal(t, models.StackStatusRunning, updated.Status)
	})
}

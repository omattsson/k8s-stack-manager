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

// TestManager_Deploy_ReplicatesWildcardTLS exercises the wildcard-secret
// replication path in executeDeploy. Three variants:
//
//  1. Configured + source exists: the Copy runs, the target namespace ends
//     up with an identical secret, the deploy completes normally.
//  2. Configured + source missing: the Copy errors out, but executeDeploy
//     still runs the chart loop and finalizes with success (non-fatal).
//  3. Not configured: no k8s client lookup happens at all (a nil resolver
//     must not panic).
func TestManager_Deploy_ReplicatesWildcardTLS(t *testing.T) {
	t.Parallel()

	srcSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "wildcard-tls", Namespace: "source-ns"},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{"tls.crt": []byte("CERT"), "tls.key": []byte("KEY")},
	}

	t.Run("replicates when configured and source exists", func(t *testing.T) {
		t.Parallel()

		cs := fake.NewSimpleClientset(srcSecret)
		k8sClient := k8s.NewClientFromInterface(cs)

		instanceRepo := newMockInstanceRepo()
		logRepo := newMockDeployLogRepo()
		hub := &mockBroadcaster{}

		inst := &models.StackInstance{
			ID:                "inst-wtls-1",
			StackDefinitionID: "def-1",
			Name:              "wtls-happy",
			Namespace:         "stack-target-a",
			OwnerID:           "user-1",
			Branch:            "main",
			Status:            models.StackStatusDraft,
		}
		require.NoError(t, instanceRepo.Create(inst))

		mgr := NewManager(ManagerConfig{
			Registry: &mockClusterResolver{
				helm:      NewHelmClient("/nonexistent/helm", "", 1*time.Second),
				k8sClient: k8sClient,
			},
			InstanceRepo:               instanceRepo,
			DeployLogRepo:              logRepo,
			TxRunner:                   &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
			Hub:                        hub,
			MaxConcurrent:              2,
			WildcardTLSSourceNamespace: "source-ns",
			WildcardTLSSourceSecret:    "wildcard-tls",
			WildcardTLSTargetSecret:    "wildcard-tls",
		})

		_, err := mgr.Deploy(context.Background(), DeployRequest{
			Instance:   inst,
			Definition: &models.StackDefinition{ID: "def-1", Name: "def"},
			Charts:     nil,
		})
		require.NoError(t, err)
		time.Sleep(300 * time.Millisecond)

		got, err := cs.CoreV1().Secrets("stack-target-a").Get(context.Background(), "wildcard-tls", metav1.GetOptions{})
		require.NoError(t, err, "wildcard secret should have been replicated into target namespace")
		assert.Equal(t, corev1.SecretTypeTLS, got.Type)
		assert.Equal(t, []byte("CERT"), got.Data["tls.crt"])
		assert.Equal(t, "k8s-stack-manager", got.Labels["managed-by"])
		assert.Equal(t, "source-ns", got.Labels["k8s-stack-manager.io/copied-from-namespace"])
	})

	t.Run("source secret missing is non-fatal", func(t *testing.T) {
		t.Parallel()

		cs := fake.NewSimpleClientset() // no source secret
		k8sClient := k8s.NewClientFromInterface(cs)

		instanceRepo := newMockInstanceRepo()
		logRepo := newMockDeployLogRepo()
		hub := &mockBroadcaster{}

		inst := &models.StackInstance{
			ID:                "inst-wtls-2",
			StackDefinitionID: "def-1",
			Name:              "wtls-missing",
			Namespace:         "stack-target-b",
			OwnerID:           "user-1",
			Branch:            "main",
			Status:            models.StackStatusDraft,
		}
		require.NoError(t, instanceRepo.Create(inst))

		mgr := NewManager(ManagerConfig{
			Registry: &mockClusterResolver{
				helm:      NewHelmClient("/nonexistent/helm", "", 1*time.Second),
				k8sClient: k8sClient,
			},
			InstanceRepo:               instanceRepo,
			DeployLogRepo:              logRepo,
			TxRunner:                   &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
			Hub:                        hub,
			MaxConcurrent:              2,
			WildcardTLSSourceNamespace: "source-ns",
			WildcardTLSSourceSecret:    "wildcard-tls",
		})

		logID, err := mgr.Deploy(context.Background(), DeployRequest{
			Instance:   inst,
			Definition: &models.StackDefinition{ID: "def-1", Name: "def"},
			Charts:     nil,
		})
		require.NoError(t, err)
		time.Sleep(300 * time.Millisecond)

		final, err := instanceRepo.FindByID(inst.ID)
		require.NoError(t, err)
		assert.Equal(t, models.StackStatusRunning, final.Status,
			"missing wildcard source is non-fatal; deploy should still succeed")

		finalLog, err := logRepo.FindByID(context.Background(), logID)
		require.NoError(t, err)
		assert.Equal(t, models.DeployLogSuccess, finalLog.Status)
		assert.Contains(t, finalLog.Output, "WARNING: failed to replicate wildcard TLS secret")
	})

	t.Run("replication skipped when not configured", func(t *testing.T) {
		t.Parallel()

		// No k8s client set on the resolver: if the replication path ran,
		// GetK8sClient() would return nil and downstream code would panic.
		// The absence of a panic proves the gate held.
		instanceRepo := newMockInstanceRepo()
		logRepo := newMockDeployLogRepo()

		inst := &models.StackInstance{
			ID:                "inst-wtls-3",
			StackDefinitionID: "def-1",
			Name:              "wtls-off",
			Namespace:         "stack-target-c",
			OwnerID:           "user-1",
			Branch:            "main",
			Status:            models.StackStatusDraft,
		}
		require.NoError(t, instanceRepo.Create(inst))

		mgr := NewManager(ManagerConfig{
			Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
			InstanceRepo:  instanceRepo,
			DeployLogRepo: logRepo,
			TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
			Hub:           &mockBroadcaster{},
			MaxConcurrent: 2,
			// no WildcardTLS* fields set
		})

		_, err := mgr.Deploy(context.Background(), DeployRequest{
			Instance:   inst,
			Definition: &models.StackDefinition{ID: "def-1", Name: "def"},
			Charts:     nil,
		})
		require.NoError(t, err)
		time.Sleep(200 * time.Millisecond)

		final, err := instanceRepo.FindByID(inst.ID)
		require.NoError(t, err)
		assert.Equal(t, models.StackStatusRunning, final.Status)
	})

	t.Run("secret without namespace is skipped with warning", func(t *testing.T) {
		t.Parallel()

		instanceRepo := newMockInstanceRepo()
		logRepo := newMockDeployLogRepo()

		inst := &models.StackInstance{
			ID:                "inst-wtls-4",
			StackDefinitionID: "def-1",
			Name:              "wtls-partial",
			Namespace:         "stack-target-d",
			OwnerID:           "user-1",
			Branch:            "main",
			Status:            models.StackStatusDraft,
		}
		require.NoError(t, instanceRepo.Create(inst))

		mgr := NewManager(ManagerConfig{
			Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
			InstanceRepo:  instanceRepo,
			DeployLogRepo: logRepo,
			TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
			Hub:           &mockBroadcaster{},
			MaxConcurrent: 2,
			// secret set but namespace empty — deliberate misconfiguration
			WildcardTLSSourceSecret: "wildcard-tls",
		})

		_, err := mgr.Deploy(context.Background(), DeployRequest{
			Instance:   inst,
			Definition: &models.StackDefinition{ID: "def-1", Name: "def"},
			Charts:     nil,
		})
		require.NoError(t, err)
		time.Sleep(200 * time.Millisecond)

		final, err := instanceRepo.FindByID(inst.ID)
		require.NoError(t, err)
		assert.Equal(t, models.StackStatusRunning, final.Status,
			"partial config is non-fatal; replication is simply skipped")
	})
}

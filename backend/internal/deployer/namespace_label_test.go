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

// TestDeploy_LabelsNamespaceWithoutOptionalFeatures checks that a deploy puts
// managed-by=k8s-stack-manager on the stack namespace even when no wildcard
// TLS, registry config or namespace RoleBindings are configured. Tools such
// as an External Secrets ClusterExternalSecret select stack namespaces by
// this label.
func TestDeploy_LabelsNamespaceWithoutOptionalFeatures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		namespace string
		existing  *corev1.Namespace
	}{
		{
			name:      "creates the namespace with the label",
			namespace: "stack-label-new",
			existing:  nil,
		},
		{
			name:      "labels a namespace that exists without the label",
			namespace: "stack-label-old",
			existing: &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
				Name:   "stack-label-old",
				Labels: map[string]string{"name": "stack-label-old"},
			}},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cs := fake.NewSimpleClientset()
			if tt.existing != nil {
				cs = fake.NewSimpleClientset(tt.existing)
			}
			k8sClient := k8s.NewClientFromInterface(cs)

			instanceRepo := newMockInstanceRepo()
			logRepo := newMockDeployLogRepo()
			hub := &mockBroadcaster{}

			inst := &models.StackInstance{
				ID:                "inst-" + tt.namespace,
				StackDefinitionID: "def-1",
				Name:              tt.namespace,
				Namespace:         tt.namespace,
				OwnerID:           "user-1",
				Branch:            "main",
				Status:            models.StackStatusDraft,
			}
			require.NoError(t, instanceRepo.Create(inst))

			mgr := NewManager(ManagerConfig{
				Registry: &mockClusterResolver{
					helm:           NewHelmClient("/nonexistent/helm", "", 1*time.Second),
					k8sClient:      k8sClient,
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

			require.Eventually(t, func() bool {
				updated, _ := instanceRepo.FindByID(inst.ID)
				return updated != nil && updated.Status != models.StackStatusDeploying
			}, 3*time.Second, 20*time.Millisecond)

			ns, err := cs.CoreV1().Namespaces().Get(context.Background(), tt.namespace, metav1.GetOptions{})
			require.NoError(t, err)
			assert.Equal(t, "k8s-stack-manager", ns.Labels["managed-by"])
			if tt.existing != nil {
				assert.Equal(t, tt.namespace, ns.Labels["name"], "other labels stay")
			}
		})
	}
}

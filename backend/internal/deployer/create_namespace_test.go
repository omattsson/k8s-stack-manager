package deployer

import (
	"context"
	"errors"
	"testing"

	"backend/internal/k8s"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// TestBuildInstallArgs_CreateNamespace checks that Helm gets
// --create-namespace only when the deployer did not create the namespace.
func TestBuildInstallArgs_CreateNamespace(t *testing.T) {
	t.Parallel()
	h := NewHelmClient("helm", "", 0)
	tests := []struct {
		name  string
		ready bool
		want  bool
	}{
		{name: "namespace not ready: Helm creates it", ready: false, want: true},
		{name: "namespace ready: no create (admission policies)", ready: true, want: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			args, err := h.buildInstallArgs(InstallRequest{ReleaseName: "web", ChartPath: "web", Namespace: "stack-a-u", NamespaceReady: tt.ready})
			require.NoError(t, err)
			assert.Equal(t, tt.want, containsArg(args, "--create-namespace"), "args: %v", args)
		})
	}
}

func containsArg(args []string, a string) bool {
	for _, x := range args {
		if x == a {
			return true
		}
	}
	return false
}

// TestDeploy_NamespaceReadyPassedToHelm checks that a deploy marks the
// install request NamespaceReady when EnsureNamespace worked, and not when it
// failed (then Helm must create the namespace).
func TestDeploy_NamespaceReadyPassedToHelm(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		failCreate bool
		wantReady  bool
	}{
		{name: "namespace created by the deployer", failCreate: false, wantReady: true},
		{name: "namespace create failed", failCreate: true, wantReady: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			instanceRepo := newMockInstanceRepo()
			logRepo := newMockDeployLogRepo()
			inst := &models.StackInstance{
				ID: "inst-ns", StackDefinitionID: "def-1", Name: "ns", Namespace: "stack-ns-user",
				OwnerID: "user-1", Branch: "main", ClusterID: "cluster-1", Status: models.StackStatusDraft,
			}
			require.NoError(t, instanceRepo.Create(inst))

			cs := fake.NewSimpleClientset()
			if tt.failCreate {
				cs.PrependReactor("create", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, errors.New("api server unavailable")
				})
			}
			helm := &mockHelmExecutor{}
			mgr := NewManager(ManagerConfig{
				Registry:      &mockClusterResolver{helm: helm, k8sClient: k8s.NewClientFromInterface(cs)},
				InstanceRepo:  instanceRepo,
				DeployLogRepo: logRepo,
				TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
				Hub:           &mockBroadcaster{},
				MaxConcurrent: 2,
			})
			logID, err := mgr.Deploy(context.Background(), DeployRequest{
				Instance:   inst,
				Definition: &models.StackDefinition{ID: "def-1", Name: "test-def"},
				Charts:     []ChartDeployInfo{{ChartConfig: models.ChartConfig{ChartName: "web", DeployOrder: 1}}},
			})
			require.NoError(t, err)
			waitForLogDone(t, logRepo, logID)

			helm.mu.Lock()
			defer helm.mu.Unlock()
			require.Len(t, helm.installCalls, 1)
			assert.Equal(t, tt.wantReady, helm.installCalls[0].NamespaceReady)
		})
	}
}

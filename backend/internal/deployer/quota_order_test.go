package deployer

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"backend/internal/k8s"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// quotaSeen records what a Helm install saw in the namespace: the
// ResourceQuota and the LimitRange of the stack manager.
type quotaSeen struct {
	cpuLimit   string // hard limits.cpu of the ResourceQuota ("" = no quota)
	limitRange bool
}

// quotaRecordingHelm returns a Helm mock whose installs record the quota
// objects in the namespace at the time of each install.
func quotaRecordingHelm(cs *fake.Clientset) (*mockHelmExecutor, func() []quotaSeen) {
	var mu sync.Mutex
	var seen []quotaSeen
	helm := &mockHelmExecutor{
		installFunc: func(ctx context.Context, req InstallRequest) (string, error) {
			s := quotaSeen{}
			if rq, err := cs.CoreV1().ResourceQuotas(req.Namespace).Get(ctx, "stack-manager-quota", metav1.GetOptions{}); err == nil {
				q := rq.Spec.Hard[corev1.ResourceLimitsCPU]
				s.cpuLimit = q.String()
			}
			if _, err := cs.CoreV1().LimitRanges(req.Namespace).Get(ctx, "stack-manager-limits", metav1.GetOptions{}); err == nil {
				s.limitRange = true
			}
			mu.Lock()
			seen = append(seen, s)
			mu.Unlock()
			return "installed", nil
		},
	}
	return helm, func() []quotaSeen {
		mu.Lock()
		defer mu.Unlock()
		return append([]quotaSeen(nil), seen...)
	}
}

// TestManager_Deploy_AppliesQuotaBeforeFirstInstall checks issue #510: the
// namespace ResourceQuota and LimitRange exist before the first Helm install,
// on the first deploy into a new namespace and on a redeploy.
func TestManager_Deploy_AppliesQuotaBeforeFirstInstall(t *testing.T) {
	t.Parallel()

	clusterQuota := &models.ResourceQuotaConfig{CPURequest: "250m", CPULimit: "2", MemoryRequest: "256Mi", MemoryLimit: "1Gi"}

	tests := []struct {
		quota         *models.ResourceQuotaConfig
		override      *models.InstanceQuotaOverride
		name          string
		wantCPULimit  string
		existingNS    bool
		existingQuota bool // a redeploy: the namespace has an older quota
		noQuotaRepo   bool
		wantLimitRng  bool
	}{
		{name: "first deploy into a new namespace", quota: clusterQuota, wantCPULimit: "2", wantLimitRng: true},
		{name: "redeploy updates the quota before the install", quota: clusterQuota, existingNS: true, existingQuota: true, wantCPULimit: "2", wantLimitRng: true},
		{name: "instance override applies before the install", quota: clusterQuota, override: &models.InstanceQuotaOverride{CPULimit: "1"}, wantCPULimit: "1", wantLimitRng: true},
		{name: "empty quota creates no objects", quota: &models.ResourceQuotaConfig{}},
		{name: "no quota repository", noQuotaRepo: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			instanceRepo := newMockInstanceRepo()
			logRepo := newMockDeployLogRepo()
			inst := &models.StackInstance{
				ID: "inst-qo", StackDefinitionID: "def-1", Name: "qo", Namespace: "stack-qo",
				OwnerID: "user-1", Branch: "main", ClusterID: "cluster-1", Status: models.StackStatusDraft,
			}
			require.NoError(t, instanceRepo.Create(inst))

			var objects []runtime.Object
			if tt.existingNS {
				objects = append(objects, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: inst.Namespace}})
			}
			if tt.existingQuota {
				objects = append(objects, &corev1.ResourceQuota{
					ObjectMeta: metav1.ObjectMeta{Name: "stack-manager-quota", Namespace: inst.Namespace},
					Spec:       corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{}},
				})
			}
			cs := fake.NewSimpleClientset(objects...)
			helm, seen := quotaRecordingHelm(cs)

			cfg := ManagerConfig{
				Registry:      &mockClusterResolver{helm: helm, k8sClient: k8s.NewClientFromInterface(cs)},
				InstanceRepo:  instanceRepo,
				DeployLogRepo: logRepo,
				TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
				Hub:           &mockBroadcaster{},
				MaxConcurrent: 2,
			}
			if !tt.noQuotaRepo {
				cfg.QuotaRepo = &mockQuotaRepo{quota: tt.quota}
				cfg.QuotaOverrideRepo = &mockQuotaOverrideRepo{override: tt.override}
			}
			mgr := NewManager(cfg)

			logID, err := mgr.Deploy(context.Background(), DeployRequest{
				Instance:   inst,
				Definition: &models.StackDefinition{ID: "def-1", Name: "test-def"},
				Charts: []ChartDeployInfo{
					{ChartConfig: models.ChartConfig{ChartName: "web", DeployOrder: 1}},
					{ChartConfig: models.ChartConfig{ChartName: "db", DeployOrder: 2}},
				},
			})
			require.NoError(t, err)
			l := waitForLogDone(t, logRepo, logID)
			assert.Equal(t, models.DeployLogSuccess, l.Status, l.Output)
			assert.NotContains(t, l.Output, "failed to apply resource quotas")

			got := seen()
			require.Len(t, got, 2)
			for i, s := range got {
				assert.Equal(t, tt.wantCPULimit, s.cpuLimit, "install %d: ResourceQuota cpu limit", i)
				assert.Equal(t, tt.wantLimitRng, s.limitRange, "install %d: LimitRange", i)
			}
		})
	}
}

// TestManager_Deploy_QuotaFallbackWithoutNamespace checks the fallback: when
// the namespace is not ready before the installs (the namespace create
// fails), the quota is applied after the installs, as before.
func TestManager_Deploy_QuotaFallbackWithoutNamespace(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	inst := &models.StackInstance{
		ID: "inst-qf", StackDefinitionID: "def-1", Name: "qf", Namespace: "stack-qf",
		OwnerID: "user-1", Branch: "main", ClusterID: "cluster-1", Status: models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	cs := fake.NewSimpleClientset()
	// The namespace create fails; Helm (--create-namespace) creates it.
	cs.PrependReactor("create", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("api server unavailable")
	})
	helm, seen := quotaRecordingHelm(cs)

	mgr := NewManager(ManagerConfig{
		Registry:          &mockClusterResolver{helm: helm, k8sClient: k8s.NewClientFromInterface(cs)},
		InstanceRepo:      instanceRepo,
		DeployLogRepo:     logRepo,
		TxRunner:          &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:               &mockBroadcaster{},
		MaxConcurrent:     2,
		QuotaRepo:         &mockQuotaRepo{quota: &models.ResourceQuotaConfig{CPULimit: "2"}},
		QuotaOverrideRepo: &mockQuotaOverrideRepo{},
	})

	logID, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "test-def"},
		Charts:     []ChartDeployInfo{{ChartConfig: models.ChartConfig{ChartName: "web", DeployOrder: 1}}},
	})
	require.NoError(t, err)
	waitForLogDone(t, logRepo, logID)

	require.Len(t, seen(), 1)
	assert.Empty(t, seen()[0].cpuLimit, "no quota before the install without a ready namespace")
	rq, err := cs.CoreV1().ResourceQuotas(inst.Namespace).Get(context.Background(), "stack-manager-quota", metav1.GetOptions{})
	require.NoError(t, err, "the quota is applied after the install")
	q := rq.Spec.Hard[corev1.ResourceLimitsCPU]
	assert.Equal(t, "2", q.String())
}

// TestRollback_ToTarget_AppliesQuotaBeforeInstall checks that a rollback to a
// target deploy (helm upgrade --install, which can create the namespace)
// creates the namespace and applies the quota before the first install.
func TestRollback_ToTarget_AppliesQuotaBeforeInstall(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	running, _ := json.Marshal(map[string]string{"web": "replicas: 3\n"})
	inst := &models.StackInstance{
		ID: "inst-rbq", StackDefinitionID: "def-1", Name: "rbq", Namespace: "stack-rbq",
		OwnerID: "user-1", Branch: "main", ClusterID: "cluster-1", Status: models.StackStatusError,
		LastDeployedValues: string(running),
	}
	require.NoError(t, instanceRepo.Create(inst))

	cs := fake.NewSimpleClientset() // namespace deleted (for example by a failed clean)
	helm, seen := quotaRecordingHelm(cs)
	mgr := NewManager(ManagerConfig{
		Registry:          &mockClusterResolver{helm: helm, k8sClient: k8s.NewClientFromInterface(cs)},
		InstanceRepo:      instanceRepo,
		DeployLogRepo:     logRepo,
		TxRunner:          &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		MaxConcurrent:     2,
		QuotaRepo:         &mockQuotaRepo{quota: &models.ResourceQuotaConfig{CPULimit: "2", MemoryLimit: "1Gi"}},
		QuotaOverrideRepo: &mockQuotaOverrideRepo{},
	})

	logID, err := mgr.Rollback(context.Background(), RollbackRequest{
		Instance:     inst,
		Charts:       []ChartDeployInfo{{ChartConfig: models.ChartConfig{ChartName: "web", DeployOrder: 1}}},
		TargetLogID:  "dep-a",
		TargetValues: map[string]string{"web": "replicas: 1\n"},
	})
	require.NoError(t, err)
	l := waitForLogDone(t, logRepo, logID)
	assert.Equal(t, models.DeployLogSuccess, l.Status, l.Output)

	ns, err := cs.CoreV1().Namespaces().Get(context.Background(), inst.Namespace, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "k8s-stack-manager", ns.Labels["managed-by"])
	require.Len(t, seen(), 1)
	assert.Equal(t, quotaSeen{cpuLimit: "2", limitRange: true}, seen()[0])
}

// TestManager_Deploy_QuotaRetryAfterFailedApply checks that a failed quota
// apply before the installs stays pending: the fallback after the installs
// applies the quota.
func TestManager_Deploy_QuotaRetryAfterFailedApply(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	inst := &models.StackInstance{
		ID: "inst-qr", StackDefinitionID: "def-1", Name: "qr", Namespace: "stack-qr",
		OwnerID: "user-1", Branch: "main", ClusterID: "cluster-1", Status: models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	cs := fake.NewSimpleClientset()
	var mu sync.Mutex
	creates := 0
	cs.PrependReactor("create", "resourcequotas", func(k8stesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		defer mu.Unlock()
		creates++
		if creates == 1 {
			return true, nil, errors.New("admission webhook unavailable")
		}
		return false, nil, nil // fall through to the fake object tracker
	})
	helm, seen := quotaRecordingHelm(cs)

	mgr := NewManager(ManagerConfig{
		Registry:          &mockClusterResolver{helm: helm, k8sClient: k8s.NewClientFromInterface(cs)},
		InstanceRepo:      instanceRepo,
		DeployLogRepo:     logRepo,
		TxRunner:          &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:               &mockBroadcaster{},
		MaxConcurrent:     2,
		QuotaRepo:         &mockQuotaRepo{quota: &models.ResourceQuotaConfig{CPULimit: "2"}},
		QuotaOverrideRepo: &mockQuotaOverrideRepo{},
	})

	logID, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "test-def"},
		Charts:     []ChartDeployInfo{{ChartConfig: models.ChartConfig{ChartName: "web", DeployOrder: 1}}},
	})
	require.NoError(t, err)
	l := waitForLogDone(t, logRepo, logID)

	require.Len(t, seen(), 1)
	assert.Empty(t, seen()[0].cpuLimit, "the first apply failed")
	assert.Equal(t, 1, strings.Count(l.Output, "WARNING: failed to apply resource quotas"), "one warning for the failed first apply")
	rq, err := cs.CoreV1().ResourceQuotas(inst.Namespace).Get(context.Background(), "stack-manager-quota", metav1.GetOptions{})
	require.NoError(t, err, "the fallback after the installs applies the quota")
	q := rq.Spec.Hard[corev1.ResourceLimitsCPU]
	assert.Equal(t, "2", q.String())
}

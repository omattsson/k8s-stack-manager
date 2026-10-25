package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"backend/internal/cluster"
	"backend/internal/k8s"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

// newQuotaTestRegistry returns a registry for cluster cl-1 (the default) that
// is backed by a fake clientset with the given namespaces.
func newQuotaTestRegistry(namespaces ...string) (*cluster.Registry, *fake.Clientset) {
	objs := make([]runtime.Object, 0, len(namespaces))
	for _, ns := range namespaces {
		objs = append(objs, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	}
	cs := fake.NewSimpleClientset(objs...)
	registry := cluster.NewRegistryForTest("cl-1", k8s.NewClientFromInterface(cs), &mockClusterHelmExecutor{})
	return registry, cs
}

// quotaHard returns the hard limit of resource in the managed ResourceQuota
// of namespace, or "" when the ResourceQuota or the resource is absent.
func quotaHard(t *testing.T, cs kubernetes.Interface, namespace string, resource corev1.ResourceName) string {
	t.Helper()
	rq, err := cs.CoreV1().ResourceQuotas(namespace).Get(context.Background(), "stack-manager-quota", metav1.GetOptions{})
	if err != nil {
		return ""
	}
	q, ok := rq.Spec.Hard[resource]
	if !ok {
		return ""
	}
	return q.String()
}

func seedQuotaInstance(t *testing.T, repo *MockStackInstanceRepository, id, clusterID, namespace, status string) {
	t.Helper()
	require.NoError(t, repo.Create(context.Background(), &models.StackInstance{
		ID:        id,
		Name:      id,
		Namespace: namespace,
		ClusterID: clusterID,
		OwnerID:   "uid-1",
		Status:    status,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}))
}

func TestPropagateQuotasToNamespaces_EffectiveQuota(t *testing.T) {
	t.Parallel()

	registry, cs := newQuotaTestRegistry(
		"stack-override-u", "stack-unassigned-u", "stack-plain-u", "stack-invalid-u", "stack-orphan-u", "kube-system",
	)
	instanceRepo := NewMockStackInstanceRepository()
	overrideRepo := NewMockInstanceQuotaOverrideRepository()
	seedQuotaInstance(t, instanceRepo, "inst-override", "cl-1", "stack-override-u", models.StackStatusRunning)
	seedQuotaInstance(t, instanceRepo, "inst-unassigned", "", "stack-unassigned-u", models.StackStatusRunning)
	seedQuotaInstance(t, instanceRepo, "inst-plain", "cl-1", "stack-plain-u", models.StackStatusRunning)
	seedQuotaInstance(t, instanceRepo, "inst-invalid", "cl-1", "stack-invalid-u", models.StackStatusRunning)
	require.NoError(t, overrideRepo.Upsert(context.Background(), &models.InstanceQuotaOverride{StackInstanceID: "inst-override", CPULimit: "8"}))
	require.NoError(t, overrideRepo.Upsert(context.Background(), &models.InstanceQuotaOverride{StackInstanceID: "inst-unassigned", MemoryLimit: "4Gi"}))
	// cpu_request 6 is above the cluster cpu_limit 4: not valid.
	require.NoError(t, overrideRepo.Upsert(context.Background(), &models.InstanceQuotaOverride{StackInstanceID: "inst-invalid", CPURequest: "6"}))

	quotaRepo := NewMockResourceQuotaRepository()
	require.NoError(t, quotaRepo.Upsert(context.Background(), &models.ResourceQuotaConfig{ClusterID: "cl-1", CPULimit: "4", MemoryLimit: "2Gi"}))
	h := NewClusterHandlerWithQuotas(NewMockClusterRepository(), registry, instanceRepo, quotaRepo).
		WithInstanceQuotaOverrides(overrideRepo)

	h.propagateQuotasToNamespaces(context.Background(), &models.Cluster{ID: "cl-1", IsDefault: true})

	tests := []struct {
		name      string
		namespace string
		wantCPU   string
		wantMem   string
	}{
		{name: "instance override wins over the cluster quota", namespace: "stack-override-u", wantCPU: "8", wantMem: "2Gi"},
		{name: "instance without a cluster on the default cluster gets its override", namespace: "stack-unassigned-u", wantCPU: "4", wantMem: "4Gi"},
		{name: "instance without an override gets the cluster quota", namespace: "stack-plain-u", wantCPU: "4", wantMem: "2Gi"},
		{name: "namespace without an instance gets the cluster quota", namespace: "stack-orphan-u", wantCPU: "4", wantMem: "2Gi"},
		{name: "invalid effective quota skips the namespace", namespace: "stack-invalid-u"},
		{name: "non-stack namespace is not changed", namespace: "kube-system"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.wantCPU, quotaHard(t, cs, tt.namespace, corev1.ResourceLimitsCPU))
			assert.Equal(t, tt.wantMem, quotaHard(t, cs, tt.namespace, corev1.ResourceLimitsMemory))
		})
	}
}

func TestPropagateQuotasToNamespaces_OverrideLookupFails(t *testing.T) {
	t.Parallel()

	registry, cs := newQuotaTestRegistry("stack-override-u", "stack-orphan-u")
	instanceRepo := NewMockStackInstanceRepository()
	overrideRepo := NewMockInstanceQuotaOverrideRepository()
	quotaRepo := NewMockResourceQuotaRepository()
	seedQuotaInstance(t, instanceRepo, "inst-override", "cl-1", "stack-override-u", models.StackStatusRunning)
	require.NoError(t, quotaRepo.Upsert(context.Background(), &models.ResourceQuotaConfig{ClusterID: "cl-1", CPULimit: "4"}))
	overrideRepo.SetError(assert.AnError)

	h := NewClusterHandlerWithQuotas(NewMockClusterRepository(), registry, instanceRepo, quotaRepo).
		WithInstanceQuotaOverrides(overrideRepo)

	h.propagateQuotasToNamespaces(context.Background(), &models.Cluster{ID: "cl-1"})

	// The instance namespace is skipped, so no override is lost.
	assert.Empty(t, quotaHard(t, cs, "stack-override-u", corev1.ResourceLimitsCPU))
	// A namespace without an instance does not read an override.
	assert.Equal(t, "4", quotaHard(t, cs, "stack-orphan-u", corev1.ResourceLimitsCPU))
}

func TestKeyedMutex(t *testing.T) {
	t.Parallel()

	var k keyedMutex
	unlockA := k.lock("a")
	// Another key does not wait.
	k.lock("b")()

	got := make(chan struct{})
	go func() {
		k.lock("a")()
		close(got)
	}()
	select {
	case <-got:
		t.Fatal("second lock of the same key did not wait")
	case <-time.After(50 * time.Millisecond):
	}
	unlockA()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("second lock did not get the key after the unlock")
	}

	k.mu.Lock()
	defer k.mu.Unlock()
	assert.Empty(t, k.locks, "unused keys are removed")
}

func TestInstanceQuotaOverride_ApplyEffectiveQuota(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		namespace    string
		createNS     bool
		clusterQuota *models.ResourceQuotaConfig
		override     *models.InstanceQuotaOverride
		wantErr      bool
		wantCPU      string
	}{
		{
			name:         "override merged with the cluster quota",
			namespace:    "stack-a-u",
			createNS:     true,
			clusterQuota: &models.ResourceQuotaConfig{ClusterID: "cl-1", CPULimit: "4"},
			override:     &models.InstanceQuotaOverride{StackInstanceID: "inst-1", CPULimit: "8"},
			wantCPU:      "8",
		},
		{
			name:         "no override gives the cluster quota",
			namespace:    "stack-a-u",
			createNS:     true,
			clusterQuota: &models.ResourceQuotaConfig{ClusterID: "cl-1", CPULimit: "4"},
			wantCPU:      "4",
		},
		{
			name:      "override without a cluster quota",
			namespace: "stack-a-u",
			createNS:  true,
			override:  &models.InstanceQuotaOverride{StackInstanceID: "inst-1", CPULimit: "2"},
			wantCPU:   "2",
		},
		{
			name:         "missing namespace writes nothing",
			namespace:    "stack-a-u",
			clusterQuota: &models.ResourceQuotaConfig{ClusterID: "cl-1", CPULimit: "4"},
		},
		{
			name:      "empty quota writes nothing",
			namespace: "stack-a-u",
			createNS:  true,
		},
		{
			name:         "invalid effective quota is an error",
			namespace:    "stack-a-u",
			createNS:     true,
			clusterQuota: &models.ResourceQuotaConfig{ClusterID: "cl-1", CPULimit: "4"},
			override:     &models.InstanceQuotaOverride{StackInstanceID: "inst-1", CPURequest: "6"},
			wantErr:      true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var registry *cluster.Registry
			var cs *fake.Clientset
			if tt.createNS {
				registry, cs = newQuotaTestRegistry(tt.namespace)
			} else {
				registry, cs = newQuotaTestRegistry()
			}
			instanceRepo := NewMockStackInstanceRepository()
			overrideRepo := NewMockInstanceQuotaOverrideRepository()
			quotaRepo := NewMockResourceQuotaRepository()
			seedQuotaInstance(t, instanceRepo, "inst-1", "cl-1", tt.namespace, models.StackStatusRunning)
			if tt.clusterQuota != nil {
				require.NoError(t, quotaRepo.Upsert(context.Background(), tt.clusterQuota))
			}
			if tt.override != nil {
				require.NoError(t, overrideRepo.Upsert(context.Background(), tt.override))
			}
			h := NewInstanceQuotaOverrideHandler(overrideRepo, instanceRepo).WithClusterQuotas(quotaRepo, registry)
			inst, err := instanceRepo.FindByID(context.Background(), "inst-1")
			require.NoError(t, err)

			err = h.applyEffectiveQuota(context.Background(), inst)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.wantCPU, quotaHard(t, cs, tt.namespace, corev1.ResourceLimitsCPU))
		})
	}
}

func TestShouldApplyQuota(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    string
		namespace string
		want      bool
	}{
		{name: "running instance", status: models.StackStatusRunning, namespace: "stack-a-u", want: true},
		{name: "stopped instance", status: models.StackStatusStopped, namespace: "stack-a-u", want: true},
		{name: "draft instance", status: models.StackStatusDraft, namespace: "stack-a-u"},
		{name: "cleaning instance", status: models.StackStatusCleaning, namespace: "stack-a-u"},
		{name: "instance without a namespace", status: models.StackStatusRunning},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, shouldApplyQuota(&models.StackInstance{Namespace: tt.namespace, Status: tt.status}))
		})
	}
}

func TestInstanceQuotaOverride_SetAndDeleteApplyAtOnce(t *testing.T) {
	t.Parallel()

	registry, cs := newQuotaTestRegistry("stack-a-u")
	instanceRepo := NewMockStackInstanceRepository()
	overrideRepo := NewMockInstanceQuotaOverrideRepository()
	quotaRepo := NewMockResourceQuotaRepository()
	seedQuotaInstance(t, instanceRepo, "inst-1", "cl-1", "stack-a-u", models.StackStatusRunning)
	require.NoError(t, quotaRepo.Upsert(context.Background(), &models.ResourceQuotaConfig{ClusterID: "cl-1", CPULimit: "4"}))

	done := make(chan struct{}, 2)
	h := NewInstanceQuotaOverrideHandler(overrideRepo, instanceRepo).WithClusterQuotas(quotaRepo, registry)
	h.applyDone = func() { done <- struct{}{} }

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(injectAuthContext("admin-user", "admin"))
	r.PUT("/api/v1/stack-instances/:id/quota-overrides", h.SetQuotaOverride)
	r.DELETE("/api/v1/stack-instances/:id/quota-overrides", h.DeleteQuotaOverride)

	wait := func() {
		t.Helper()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("background quota write did not finish")
		}
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/stack-instances/inst-1/quota-overrides", bytes.NewBufferString(`{"cpu_limit":"8"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	wait()
	assert.Equal(t, "8", quotaHard(t, cs, "stack-a-u", corev1.ResourceLimitsCPU))

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/v1/stack-instances/inst-1/quota-overrides", nil))
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	wait()
	assert.Equal(t, "4", quotaHard(t, cs, "stack-a-u", corev1.ResourceLimitsCPU))
}

// bgQuotaCtxKey marks the request context in TestInstanceQuotaOverride_BackgroundApplyContext.
type bgQuotaCtxKey struct{}

// ctxQuotaRepo records the context state of each GetByClusterID call, read at
// the call: the background write cancels its context when it ends.
type ctxQuotaRepo struct {
	*MockResourceQuotaRepository
	mu    sync.Mutex
	calls []ctxCallState
}

type ctxCallState struct {
	err   error
	value any
	left  time.Duration // -1: no deadline
}

func (r *ctxQuotaRepo) GetByClusterID(ctx context.Context, clusterID string) (*models.ResourceQuotaConfig, error) {
	left := time.Duration(-1)
	if d, ok := ctx.Deadline(); ok {
		left = time.Until(d)
	}
	r.mu.Lock()
	r.calls = append(r.calls, ctxCallState{err: ctx.Err(), value: ctx.Value(bgQuotaCtxKey{}), left: left})
	r.mu.Unlock()
	return r.MockResourceQuotaRepository.GetByClusterID(ctx, clusterID)
}

// TestInstanceQuotaOverride_BackgroundApplyContext checks the context of the
// background quota write after an override change (#674): it keeps the
// request values (request ID, trace), the end of the request does not end
// it, and it has the quotaApplyTimeout deadline.
func TestInstanceQuotaOverride_BackgroundApplyContext(t *testing.T) {
	t.Parallel()

	registry, _ := newQuotaTestRegistry("stack-a-u")
	instanceRepo := NewMockStackInstanceRepository()
	overrideRepo := NewMockInstanceQuotaOverrideRepository()
	quotaRepo := &ctxQuotaRepo{MockResourceQuotaRepository: NewMockResourceQuotaRepository()}
	seedQuotaInstance(t, instanceRepo, "inst-1", "cl-1", "stack-a-u", models.StackStatusRunning)
	require.NoError(t, quotaRepo.Upsert(context.Background(), &models.ResourceQuotaConfig{ClusterID: "cl-1", CPULimit: "4"}))

	done := make(chan struct{}, 1)
	h := NewInstanceQuotaOverrideHandler(overrideRepo, instanceRepo).WithClusterQuotas(quotaRepo, registry)
	h.applyDone = func() { done <- struct{}{} }

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(injectAuthContext("admin-user", "admin"))
	r.PUT("/api/v1/stack-instances/:id/quota-overrides", h.SetQuotaOverride)

	reqCtx, endRequest := context.WithCancel(context.WithValue(context.Background(), bgQuotaCtxKey{}, "req-1"))
	defer endRequest()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/stack-instances/inst-1/quota-overrides",
		bytes.NewBufferString(`{"cpu_limit":"8"}`)).WithContext(reqCtx)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	endRequest() // the request ends with the response
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("background quota write did not finish")
	}
	quotaRepo.mu.Lock()
	defer quotaRepo.mu.Unlock()
	require.NotEmpty(t, quotaRepo.calls, "the background write reads the cluster quota")
	bg := quotaRepo.calls[len(quotaRepo.calls)-1]
	assert.NoError(t, bg.err, "the end of the request does not end the background write")
	assert.Equal(t, "req-1", bg.value, "the background write keeps the request values")
	assert.Greater(t, bg.left, quotaApplyTimeout-5*time.Second, "the background write has the quotaApplyTimeout deadline")
	assert.LessOrEqual(t, bg.left, quotaApplyTimeout)
}

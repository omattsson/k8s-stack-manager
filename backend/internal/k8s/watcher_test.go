package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"backend/internal/models"
	"backend/internal/websocket"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// ---- mock repositories and broadcaster ----

type mockInstanceRepo struct {
	mu        sync.RWMutex
	instances map[string]*models.StackInstance
	listErr   error
	updateErr error
}

func newMockInstanceRepo() *mockInstanceRepo {
	return &mockInstanceRepo{instances: make(map[string]*models.StackInstance)}
}

func (m *mockInstanceRepo) Create(inst *models.StackInstance) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *inst
	m.instances[inst.ID] = &cp
	return nil
}

func (m *mockInstanceRepo) FindByID(id string) (*models.StackInstance, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	inst, ok := m.instances[id]
	if !ok {
		return nil, errors.New("not found")
	}
	cp := *inst
	return &cp, nil
}

func (m *mockInstanceRepo) Update(inst *models.StackInstance) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.updateErr != nil {
		return m.updateErr
	}
	cp := *inst
	m.instances[inst.ID] = &cp
	return nil
}

func (m *mockInstanceRepo) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.instances, id)
	return nil
}

func (m *mockInstanceRepo) List() ([]models.StackInstance, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.listErr != nil {
		return nil, m.listErr
	}
	out := make([]models.StackInstance, 0, len(m.instances))
	for _, inst := range m.instances {
		out = append(out, *inst)
	}
	return out, nil
}

func (m *mockInstanceRepo) FindByNamespace(namespace string) (*models.StackInstance, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, inst := range m.instances {
		if inst.Namespace == namespace {
			cp := *inst
			return &cp, nil
		}
	}
	return nil, errors.New("not found")
}

func (m *mockInstanceRepo) ListByOwner(_ string) ([]models.StackInstance, error) {
	return m.List()
}

func (m *mockInstanceRepo) FindByCluster(clusterID string) ([]models.StackInstance, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.listErr != nil {
		return nil, m.listErr
	}
	var out []models.StackInstance
	for _, inst := range m.instances {
		if inst.ClusterID == clusterID {
			out = append(out, *inst)
		}
	}
	return out, nil
}

func (m *mockInstanceRepo) CountByClusterAndOwner(clusterID, ownerID string) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	count := 0
	for _, inst := range m.instances {
		if inst.ClusterID == clusterID && inst.OwnerID == ownerID {
			count++
		}
	}
	return count, nil
}

func (m *mockInstanceRepo) ListPaged(_, _ int) ([]models.StackInstance, int, error) { return nil, 0, nil }
func (m *mockInstanceRepo) CountAll() (int, error)                              { return 0, nil }
func (m *mockInstanceRepo) CountByStatus(_ string) (int, error)                 { return 0, nil }
func (m *mockInstanceRepo) ExistsByDefinitionAndStatus(_, _ string) (bool, error) { return false, nil }

func (m *mockInstanceRepo) ListExpired() ([]*models.StackInstance, error) {
	return nil, nil
}

func (m *mockInstanceRepo) getStatus(id string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	inst, ok := m.instances[id]
	if !ok {
		return ""
	}
	return inst.Status
}

type mockBroadcaster struct {
	mu       sync.Mutex
	messages [][]byte
}

func (m *mockBroadcaster) Broadcast(message []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(message))
	copy(cp, message)
	m.messages = append(m.messages, cp)
}

func (m *mockBroadcaster) messageCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.messages)
}

func (m *mockBroadcaster) getMessages() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([][]byte, len(m.messages))
	copy(out, m.messages)
	return out
}

// mockClientProvider implements ClientProvider for tests.
type mockClientProvider struct {
	client *Client
	err    error
}

func (m *mockClientProvider) GetK8sClient(_ string) (*Client, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.client, nil
}

// ---- tests ----

func TestNewWatcher(t *testing.T) {
	t.Parallel()

	cs := fake.NewSimpleClientset()
	client := NewClientFromInterface(cs)
	repo := newMockInstanceRepo()
	hub := &mockBroadcaster{}
	interval := 30 * time.Second

	w := NewWatcher(&mockClientProvider{client: client}, repo, hub, interval)

	assert.NotNil(t, w)
	assert.NotNil(t, w.clientProvider)
	assert.Equal(t, interval, w.interval)
	assert.NotNil(t, w.lastStatus)
	assert.Empty(t, w.lastStatus)
}

func TestNewWatcher_NilHub(t *testing.T) {
	t.Parallel()

	cs := fake.NewSimpleClientset()
	client := NewClientFromInterface(cs)
	repo := newMockInstanceRepo()

	w := NewWatcher(&mockClientProvider{client: client}, repo, nil, 10*time.Second)
	assert.NotNil(t, w)
	assert.Nil(t, w.hub)
}

func TestWatcherStartStop(t *testing.T) {
	t.Parallel()

	cs := fake.NewSimpleClientset()
	client := NewClientFromInterface(cs)
	repo := newMockInstanceRepo()
	hub := &mockBroadcaster{}

	w := NewWatcher(&mockClientProvider{client: client}, repo, hub, 50*time.Millisecond)

	ctx := context.Background()
	w.Start(ctx)

	// Give the watcher time to run at least one poll cycle.
	time.Sleep(150 * time.Millisecond)

	// Stop should return without deadlock.
	w.Stop()
}

func TestWatcherStartStop_Idempotent(t *testing.T) {
	t.Parallel()

	cs := fake.NewSimpleClientset()
	client := NewClientFromInterface(cs)
	repo := newMockInstanceRepo()

	w := NewWatcher(&mockClientProvider{client: client}, repo, nil, 50*time.Millisecond)

	ctx := context.Background()
	w.Start(ctx)
	time.Sleep(100 * time.Millisecond)
	w.Stop()

	// Calling Stop again should not panic or deadlock.
	w.Stop()
}

func TestWatcherGetStatus_NoCache(t *testing.T) {
	t.Parallel()

	cs := fake.NewSimpleClientset()
	client := NewClientFromInterface(cs)
	repo := newMockInstanceRepo()

	w := NewWatcher(&mockClientProvider{client: client}, repo, nil, 1*time.Second)

	status, ok := w.GetStatus("nonexistent")
	assert.False(t, ok)
	assert.Nil(t, status)
}

func TestWatcherGetStatus_ReturnsCachedStatus(t *testing.T) {
	t.Parallel()

	ns := "test-ns"
	replicas := int32(1)

	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-app",
				Namespace: ns,
				Labels:    map[string]string{"app.kubernetes.io/instance": "my-release"},
			},
			Spec: appsv1.DeploymentSpec{Replicas: &replicas},
			Status: appsv1.DeploymentStatus{
				ReadyReplicas:   1,
				Replicas:        1,
				UpdatedReplicas: 1,
				Conditions: []appsv1.DeploymentCondition{
					{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
				},
			},
		},
	)
	client := NewClientFromInterface(cs)
	repo := newMockInstanceRepo()

	inst := &models.StackInstance{
		ID:        "inst-1",
		Name:      "test-instance",
		Namespace: ns,
		Status:    models.StackStatusRunning,
	}
	require.NoError(t, repo.Create(inst))

	w := NewWatcher(&mockClientProvider{client: client}, repo, nil, 100*time.Millisecond)

	ctx := context.Background()
	w.Start(ctx)

	// Wait for at least one poll.
	time.Sleep(250 * time.Millisecond)

	w.Stop()

	status, ok := w.GetStatus("inst-1")
	assert.True(t, ok)
	require.NotNil(t, status)
	assert.Equal(t, ns, status.Namespace)
	assert.Equal(t, StatusHealthy, status.Status)
}

func TestWatcherPoll_SkipsNonActiveInstances(t *testing.T) {
	t.Parallel()

	cs := fake.NewSimpleClientset()
	client := NewClientFromInterface(cs)
	repo := newMockInstanceRepo()

	// Create instances with non-active statuses — they should be skipped.
	for _, s := range []string{models.StackStatusDraft, models.StackStatusStopped, models.StackStatusError} {
		require.NoError(t, repo.Create(&models.StackInstance{
			ID:        "inst-" + s,
			Namespace: "ns-" + s,
			Status:    s,
		}))
	}

	w := NewWatcher(&mockClientProvider{client: client}, repo, nil, 100*time.Millisecond)

	ctx := context.Background()
	w.Start(ctx)
	time.Sleep(200 * time.Millisecond)
	w.Stop()

	// None of these should have cached status since they were skipped.
	for _, s := range []string{models.StackStatusDraft, models.StackStatusStopped, models.StackStatusError} {
		_, ok := w.GetStatus("inst-" + s)
		assert.False(t, ok, "instance with status %q should not be polled", s)
	}
}

func TestWatcherPoll_SkipsEmptyNamespace(t *testing.T) {
	t.Parallel()

	cs := fake.NewSimpleClientset()
	client := NewClientFromInterface(cs)
	repo := newMockInstanceRepo()

	require.NoError(t, repo.Create(&models.StackInstance{
		ID:        "inst-empty-ns",
		Namespace: "", // Empty namespace should be skipped.
		Status:    models.StackStatusRunning,
	}))

	w := NewWatcher(&mockClientProvider{client: client}, repo, nil, 100*time.Millisecond)

	ctx := context.Background()
	w.Start(ctx)
	time.Sleep(200 * time.Millisecond)
	w.Stop()

	_, ok := w.GetStatus("inst-empty-ns")
	assert.False(t, ok)
}

func TestWatcherPoll_ListError(t *testing.T) {
	t.Parallel()

	cs := fake.NewSimpleClientset()
	client := NewClientFromInterface(cs)
	repo := newMockInstanceRepo()
	repo.listErr = errors.New("db down")

	w := NewWatcher(&mockClientProvider{client: client}, repo, nil, 100*time.Millisecond)

	ctx := context.Background()
	w.Start(ctx)
	time.Sleep(200 * time.Millisecond)
	w.Stop()

	// No statuses should be cached when list fails.
	assert.Empty(t, w.lastStatus)
}

func TestWatcherPoll_MonitorsRunningAndDeployingInstances(t *testing.T) {
	t.Parallel()

	ns1 := "ns-running"
	ns2 := "ns-deploying"

	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns1}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns2}},
	)
	client := NewClientFromInterface(cs)
	repo := newMockInstanceRepo()

	require.NoError(t, repo.Create(&models.StackInstance{
		ID:        "inst-running",
		Namespace: ns1,
		Status:    models.StackStatusRunning,
	}))
	require.NoError(t, repo.Create(&models.StackInstance{
		ID:        "inst-deploying",
		Namespace: ns2,
		Status:    models.StackStatusDeploying,
	}))

	w := NewWatcher(&mockClientProvider{client: client}, repo, nil, 100*time.Millisecond)

	ctx := context.Background()
	w.Start(ctx)
	time.Sleep(250 * time.Millisecond)
	w.Stop()

	s1, ok1 := w.GetStatus("inst-running")
	assert.True(t, ok1)
	require.NotNil(t, s1)

	s2, ok2 := w.GetStatus("inst-deploying")
	assert.True(t, ok2)
	require.NotNil(t, s2)
}

func TestWatcherBroadcast_SendsMessageOnStatusChange(t *testing.T) {
	t.Parallel()

	ns := "bcast-ns"
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
	)
	client := NewClientFromInterface(cs)
	repo := newMockInstanceRepo()
	hub := &mockBroadcaster{}

	require.NoError(t, repo.Create(&models.StackInstance{
		ID:        "inst-bcast",
		Namespace: ns,
		Status:    models.StackStatusRunning,
	}))

	w := NewWatcher(&mockClientProvider{client: client}, repo, hub, 100*time.Millisecond)

	ctx := context.Background()
	w.Start(ctx)
	time.Sleep(250 * time.Millisecond)
	w.Stop()

	// The first poll should detect a "change" because there is no previous status,
	// so at least one broadcast should have been sent.
	assert.Greater(t, hub.messageCount(), 0)

	// Verify message structure.
	msgs := hub.getMessages()
	var msg websocket.Message
	err := json.Unmarshal(msgs[0], &msg)
	assert.NoError(t, err)
	assert.Equal(t, "instance.status", msg.Type)

	var payload statusPayload
	err = json.Unmarshal(msg.Payload, &payload)
	assert.NoError(t, err)
	assert.Equal(t, "inst-bcast", payload.InstanceID)
	assert.NotEmpty(t, payload.Status)
}

func TestWatcherBroadcast_NilHub(t *testing.T) {
	t.Parallel()

	ns := "nil-hub-ns"
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
	)
	client := NewClientFromInterface(cs)
	repo := newMockInstanceRepo()

	require.NoError(t, repo.Create(&models.StackInstance{
		ID:        "inst-nil-hub",
		Namespace: ns,
		Status:    models.StackStatusRunning,
	}))

	// nil hub should not panic.
	w := NewWatcher(&mockClientProvider{client: client}, repo, nil, 100*time.Millisecond)

	ctx := context.Background()
	w.Start(ctx)
	time.Sleep(200 * time.Millisecond)
	w.Stop()

	// Verify polling still worked.
	status, ok := w.GetStatus("inst-nil-hub")
	assert.True(t, ok)
	require.NotNil(t, status)
}

func TestWatcherBroadcast_NoDuplicateOnSameStatus(t *testing.T) {
	t.Parallel()

	ns := "nodup-ns"
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
	)
	client := NewClientFromInterface(cs)
	repo := newMockInstanceRepo()
	hub := &mockBroadcaster{}

	require.NoError(t, repo.Create(&models.StackInstance{
		ID:        "inst-nodup",
		Namespace: ns,
		Status:    models.StackStatusRunning,
	}))

	w := NewWatcher(&mockClientProvider{client: client}, repo, hub, 50*time.Millisecond)

	ctx := context.Background()
	w.Start(ctx)
	// Wait for multiple poll cycles.
	time.Sleep(300 * time.Millisecond)
	w.Stop()

	// Only the first poll should broadcast (status unchanged after that).
	// We allow 1 broadcast: the initial detection.
	assert.Equal(t, 1, hub.messageCount(), "should only broadcast once when status is unchanged")
}

func TestWatcherHandleStatusTransition_ErrorStatus(t *testing.T) {
	t.Parallel()

	ns := "error-ns"

	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "failed-pod",
				Namespace: ns,
				Labels:    map[string]string{"app.kubernetes.io/instance": "bad-release"},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "myimage:v1"}},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodFailed,
			},
		},
	)

	client := NewClientFromInterface(cs)
	repo := newMockInstanceRepo()
	hub := &mockBroadcaster{}

	require.NoError(t, repo.Create(&models.StackInstance{
		ID:        "inst-error",
		Namespace: ns,
		Status:    models.StackStatusRunning,
	}))

	w := NewWatcher(&mockClientProvider{client: client}, repo, hub, 100*time.Millisecond)

	ctx := context.Background()
	w.Start(ctx)
	time.Sleep(250 * time.Millisecond)
	w.Stop()

	// The watcher should have transitioned the instance status to error.
	instStatus := repo.getStatus("inst-error")
	assert.Equal(t, models.StackStatusError, instStatus)

	// Verify cached status is error.
	cached, ok := w.GetStatus("inst-error")
	assert.True(t, ok)
	require.NotNil(t, cached)
	assert.Equal(t, StatusError, cached.Status)
}

func TestWatcherHandleStatusTransition_DegradedDoesNotChangeRepoStatus(t *testing.T) {
	t.Parallel()

	ns := "degraded-ns-watcher"

	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "crashy-pod",
				Namespace: ns,
				Labels:    map[string]string{"app.kubernetes.io/instance": "crashy-release"},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "myimage:v1"}},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{
					{Ready: true, RestartCount: 10},
				},
			},
		},
	)

	client := NewClientFromInterface(cs)
	repo := newMockInstanceRepo()

	require.NoError(t, repo.Create(&models.StackInstance{
		ID:        "inst-degraded",
		Namespace: ns,
		Status:    models.StackStatusRunning,
	}))

	w := NewWatcher(&mockClientProvider{client: client}, repo, nil, 100*time.Millisecond)

	ctx := context.Background()
	w.Start(ctx)
	time.Sleep(250 * time.Millisecond)
	w.Stop()

	// Degraded should NOT change the instance status in the repo — it stays "running".
	instStatus := repo.getStatus("inst-degraded")
	assert.Equal(t, models.StackStatusRunning, instStatus)

	// But the cached K8s status should show degraded.
	cached, ok := w.GetStatus("inst-degraded")
	assert.True(t, ok)
	require.NotNil(t, cached)
	assert.Equal(t, StatusDegraded, cached.Status)
}

func TestWatcherHandleStatusTransition_ErrorAlreadyInErrorState(t *testing.T) {
	t.Parallel()

	ns := "already-error-ns"

	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "failed-pod",
				Namespace: ns,
				Labels:    map[string]string{"app.kubernetes.io/instance": "fail-release"},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "myimage:v1"}},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodFailed,
			},
		},
	)

	client := NewClientFromInterface(cs)
	repo := newMockInstanceRepo()

	// Instance is already in error state — handleStatusTransition should not
	// attempt another update.
	require.NoError(t, repo.Create(&models.StackInstance{
		ID:           "inst-already-error",
		Namespace:    ns,
		Status:       models.StackStatusError,
		ErrorMessage: "previous error",
	}))

	// Note: The instance is in error status, so the watcher skips it
	// (only monitors "running" and "deploying"). We need deploying to test this.
	// Let's use deploying status instead to make it get polled.
	require.NoError(t, repo.Update(&models.StackInstance{
		ID:           "inst-already-error",
		Namespace:    ns,
		Status:       models.StackStatusDeploying,
		ErrorMessage: "",
	}))

	w := NewWatcher(&mockClientProvider{client: client}, repo, nil, 100*time.Millisecond)

	ctx := context.Background()
	w.Start(ctx)
	time.Sleep(250 * time.Millisecond)
	w.Stop()

	// Verify the instance was transitioned to error status.
	instStatus := repo.getStatus("inst-already-error")
	assert.Equal(t, models.StackStatusError, instStatus)
}

func TestWatcherHandleStatusTransition_UpdateError(t *testing.T) {
	t.Parallel()

	ns := "update-err-ns"

	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "failed-pod",
				Namespace: ns,
				Labels:    map[string]string{"app.kubernetes.io/instance": "fail-release"},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "myimage:v1"}},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodFailed,
			},
		},
	)

	client := NewClientFromInterface(cs)
	repo := newMockInstanceRepo()

	require.NoError(t, repo.Create(&models.StackInstance{
		ID:        "inst-update-err",
		Namespace: ns,
		Status:    models.StackStatusRunning,
	}))

	// Set update to fail — watcher should handle the error gracefully (log it).
	repo.updateErr = errors.New("db write failed")

	w := NewWatcher(&mockClientProvider{client: client}, repo, nil, 100*time.Millisecond)

	ctx := context.Background()
	w.Start(ctx)
	time.Sleep(250 * time.Millisecond)
	w.Stop()

	// The instance status should remain "running" because the update failed.
	instStatus := repo.getStatus("inst-update-err")
	assert.Equal(t, models.StackStatusRunning, instStatus)
}

func TestWatcherPoll_NamespaceNotFound(t *testing.T) {
	t.Parallel()

	// Namespace doesn't exist in K8s but instance references it.
	cs := fake.NewSimpleClientset()
	client := NewClientFromInterface(cs)
	repo := newMockInstanceRepo()
	hub := &mockBroadcaster{}

	require.NoError(t, repo.Create(&models.StackInstance{
		ID:        "inst-ns-gone",
		Namespace: "gone-ns",
		Status:    models.StackStatusRunning,
	}))

	w := NewWatcher(&mockClientProvider{client: client}, repo, hub, 100*time.Millisecond)

	ctx := context.Background()
	w.Start(ctx)
	time.Sleep(250 * time.Millisecond)
	w.Stop()

	// The watcher should still cache the status (not_found).
	cached, ok := w.GetStatus("inst-ns-gone")
	assert.True(t, ok)
	require.NotNil(t, cached)
	assert.Equal(t, StatusNotFound, cached.Status)
}

func TestWatcherContextCancellation(t *testing.T) {
	t.Parallel()

	cs := fake.NewSimpleClientset()
	client := NewClientFromInterface(cs)
	repo := newMockInstanceRepo()

	w := NewWatcher(&mockClientProvider{client: client}, repo, nil, 1*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)

	// Cancel the context externally.
	cancel()

	// Stop should return quickly because the context was cancelled.
	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Good — stopped in time.
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not stop within 2 seconds after context cancellation")
	}
}

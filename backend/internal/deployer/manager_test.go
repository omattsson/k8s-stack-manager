package deployer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/database"
	"backend/internal/k8s"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// ---- mock repositories ----

type mockInstanceRepo struct {
	mu    sync.RWMutex
	items map[string]*models.StackInstance
	err   error
}

func newMockInstanceRepo() *mockInstanceRepo {
	return &mockInstanceRepo{items: make(map[string]*models.StackInstance)}
}

func (m *mockInstanceRepo) Create(inst *models.StackInstance) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	cp := *inst
	m.items[inst.ID] = &cp
	return nil
}

func (m *mockInstanceRepo) FindByID(id string) (*models.StackInstance, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	inst, ok := m.items[id]
	if !ok {
		return nil, errors.New("not found")
	}
	cp := *inst
	return &cp, nil
}

func (m *mockInstanceRepo) Update(inst *models.StackInstance) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	cp := *inst
	m.items[inst.ID] = &cp
	return nil
}

func (m *mockInstanceRepo) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, id)
	return nil
}

func (m *mockInstanceRepo) List() ([]models.StackInstance, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	out := make([]models.StackInstance, 0, len(m.items))
	for _, inst := range m.items {
		out = append(out, *inst)
	}
	return out, nil
}

func (m *mockInstanceRepo) FindByNamespace(namespace string) (*models.StackInstance, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, inst := range m.items {
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
	if m.err != nil {
		return nil, m.err
	}
	var out []models.StackInstance
	for _, inst := range m.items {
		if inst.ClusterID == clusterID {
			out = append(out, *inst)
		}
	}
	return out, nil
}

func (m *mockInstanceRepo) CountByClusterAndOwner(clusterID, ownerID string) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return 0, m.err
	}
	count := 0
	for _, inst := range m.items {
		if inst.ClusterID == clusterID && inst.OwnerID == ownerID {
			count++
		}
	}
	return count, nil
}

func (m *mockInstanceRepo) ListPaged(_, _ int) ([]models.StackInstance, int, error) {
	return nil, 0, nil
}
func (m *mockInstanceRepo) CountAll() (int, error)                                  { return 0, nil }
func (m *mockInstanceRepo) CountByStatus(_ string) (int, error)                     { return 0, nil }
func (m *mockInstanceRepo) ExistsByDefinitionAndStatus(_, _ string) (bool, error)   { return false, nil }
func (m *mockInstanceRepo) CountByDefinitionIDs(_ []string) (map[string]int, error) { return nil, nil }
func (m *mockInstanceRepo) CountByOwnerIDs(_ []string) (map[string]int, error)      { return nil, nil }
func (m *mockInstanceRepo) ListIDsByDefinitionIDs(_ []string) (map[string][]string, error) {
	return nil, nil
}
func (m *mockInstanceRepo) ListIDsByOwnerIDs(_ []string) (map[string][]string, error) {
	return nil, nil
}

func (m *mockInstanceRepo) ListExpired() ([]*models.StackInstance, error) {
	return nil, nil
}

func (m *mockInstanceRepo) setError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

// ---- deployment log repo mock ----

type mockDeployLogRepo struct {
	mu    sync.RWMutex
	items map[string]*models.DeploymentLog
	err   error
}

func newMockDeployLogRepo() *mockDeployLogRepo {
	return &mockDeployLogRepo{items: make(map[string]*models.DeploymentLog)}
}

func (m *mockDeployLogRepo) Create(_ context.Context, log *models.DeploymentLog) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	cp := *log
	m.items[log.ID] = &cp
	return nil
}

func (m *mockDeployLogRepo) FindByID(_ context.Context, id string) (*models.DeploymentLog, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	log, ok := m.items[id]
	if !ok {
		return nil, errors.New("not found")
	}
	cp := *log
	return &cp, nil
}

func (m *mockDeployLogRepo) Update(_ context.Context, log *models.DeploymentLog) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	cp := *log
	m.items[log.ID] = &cp
	return nil
}

func (m *mockDeployLogRepo) ListByInstance(_ context.Context, instanceID string) ([]models.DeploymentLog, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []models.DeploymentLog
	for _, log := range m.items {
		if log.StackInstanceID == instanceID {
			out = append(out, *log)
		}
	}
	return out, nil
}

func (m *mockDeployLogRepo) ListByInstancePaginated(_ context.Context, filters models.DeploymentLogFilters) (*models.DeploymentLogResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []models.DeploymentLog
	for _, log := range m.items {
		if log.StackInstanceID == filters.InstanceID {
			out = append(out, *log)
		}
	}
	return &models.DeploymentLogResult{Data: out, Total: int64(len(out))}, nil
}

func (m *mockDeployLogRepo) GetLatestByInstance(_ context.Context, instanceID string) (*models.DeploymentLog, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var latest *models.DeploymentLog
	for _, log := range m.items {
		if log.StackInstanceID == instanceID {
			if latest == nil || log.StartedAt.After(latest.StartedAt) {
				cp := *log
				latest = &cp
			}
		}
	}
	if latest == nil {
		return nil, errors.New("not found")
	}
	return latest, nil
}

func (m *mockDeployLogRepo) SummarizeByInstance(_ context.Context, instanceID string) (*models.DeployLogSummary, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	summary := &models.DeployLogSummary{InstanceID: instanceID}
	for _, log := range m.items {
		if log.StackInstanceID != instanceID || log.Action != models.DeployActionDeploy {
			continue
		}
		summary.DeployCount++
		switch log.Status {
		case models.DeployLogSuccess:
			summary.SuccessCount++
		case models.DeployLogError:
			summary.ErrorCount++
		}
	}
	return summary, nil
}

func (m *mockDeployLogRepo) SummarizeBatch(ctx context.Context, instanceIDs []string) (map[string]*models.DeployLogSummary, error) {
	result := make(map[string]*models.DeployLogSummary, len(instanceIDs))
	for _, id := range instanceIDs {
		summary, err := m.SummarizeByInstance(ctx, id)
		if err != nil {
			return nil, err
		}
		result[id] = summary
	}
	return result, nil
}

func (m *mockDeployLogRepo) CountByAction(_ context.Context, _ string) (int, error) {
	return 0, nil
}

// ---- txRunner mock ----

// mockTxRunner implements database.TxRunner by calling fn with TxRepos
// that delegate to the provided mock repositories.
type mockTxRunner struct {
	instanceRepo models.StackInstanceRepository
	logRepo      models.DeploymentLogRepository
}

func (m *mockTxRunner) RunInTx(fn func(repos database.TxRepos) error) error {
	return fn(database.TxRepos{
		StackInstance: m.instanceRepo,
		DeploymentLog: m.logRepo,
	})
}

// ---- broadcaster mock ----

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

// ---- tests ----

// mockClusterResolver implements ClusterResolver for tests.
type mockClusterResolver struct {
	helm       HelmExecutor
	k8sClient  *k8s.Client
	resolveErr error
	helmErr    error
	k8sErr     error
}

func (m *mockClusterResolver) ResolveClusterID(clusterID string) (string, error) {
	if m.resolveErr != nil {
		return "", m.resolveErr
	}
	if clusterID == "" {
		return "default", nil
	}
	return clusterID, nil
}

func (m *mockClusterResolver) GetHelmExecutor(_ string) (HelmExecutor, error) {
	if m.helmErr != nil {
		return nil, m.helmErr
	}
	return m.helm, nil
}

func (m *mockClusterResolver) GetK8sClient(_ string) (*k8s.Client, error) {
	if m.k8sErr != nil {
		return nil, m.k8sErr
	}
	return m.k8sClient, nil
}

func TestNewManager(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		maxConcurrent int
		wantCap       int
	}{
		{
			name:          "explicit concurrency",
			maxConcurrent: 5,
			wantCap:       5,
		},
		{
			name:          "zero defaults to 3",
			maxConcurrent: 0,
			wantCap:       3,
		},
		{
			name:          "negative defaults to 3",
			maxConcurrent: -1,
			wantCap:       3,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mgr := NewManager(ManagerConfig{
				Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
				InstanceRepo:  newMockInstanceRepo(),
				DeployLogRepo: newMockDeployLogRepo(),
				Hub:           &mockBroadcaster{},
				MaxConcurrent: tt.maxConcurrent,
			})
			assert.NotNil(t, mgr)
			assert.Equal(t, tt.wantCap, cap(mgr.semaphore))
		})
	}
}

func TestManager_Deploy_CreatesLogAndUpdatesStatus(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:                "inst-1",
		StackDefinitionID: "def-1",
		Name:              "test-instance",
		Namespace:         "stack-test-user",
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
		Hub:           hub,
		MaxConcurrent: 2,
	})

	req := DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "test-def"},
		Charts:     []ChartDeployInfo{}, // No charts = quick finish.
	}

	logID, err := mgr.Deploy(context.Background(), req)
	assert.NoError(t, err)
	assert.NotEmpty(t, logID)

	// Verify deployment log was created.
	log, err := logRepo.FindByID(context.Background(), logID)
	assert.NoError(t, err)
	assert.Equal(t, models.DeployActionDeploy, log.Action)
	assert.Equal(t, models.DeployLogRunning, log.Status)
	assert.Equal(t, inst.ID, log.StackInstanceID)

	// Verify instance status was updated to deploying.
	updated, err := instanceRepo.FindByID(inst.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.StackStatusDeploying, updated.Status)

	// Give goroutine time to finish (no charts = quick).
	time.Sleep(200 * time.Millisecond)

	// Verify final status after async completion.
	final, err := instanceRepo.FindByID(inst.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.StackStatusRunning, final.Status)

	// Verify log was updated to success.
	finalLog, err := logRepo.FindByID(context.Background(), logID)
	assert.NoError(t, err)
	assert.Equal(t, models.DeployLogSuccess, finalLog.Status)
	assert.NotNil(t, finalLog.CompletedAt)

	// Verify broadcasts were sent.
	assert.Greater(t, hub.messageCount(), 0)
}

func TestManager_Deploy_WithCharts_Fails(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:                "inst-2",
		StackDefinitionID: "def-1",
		Name:              "fail-instance",
		Namespace:         "stack-fail-user",
		OwnerID:           "user-1",
		Branch:            "main",
		Status:            models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	// Use a nonexistent binary so helm install fails.
	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           hub,
		MaxConcurrent: 2,
	})

	req := DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "test-def"},
		Charts: []ChartDeployInfo{
			{
				ChartConfig: models.ChartConfig{
					ID:            "c1",
					ChartName:     "nginx",
					RepositoryURL: "oci://example.com/charts/nginx",
					DeployOrder:   1,
				},
			},
		},
	}

	logID, err := mgr.Deploy(context.Background(), req)
	assert.NoError(t, err)
	assert.NotEmpty(t, logID)

	// Wait for async completion.
	time.Sleep(500 * time.Millisecond)

	// Verify instance status is error.
	final, err := instanceRepo.FindByID(inst.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.StackStatusError, final.Status)
	assert.NotEmpty(t, final.ErrorMessage)

	// Verify log was updated to error.
	finalLog, err := logRepo.FindByID(context.Background(), logID)
	assert.NoError(t, err)
	assert.Equal(t, models.DeployLogError, finalLog.Status)
	assert.NotEmpty(t, finalLog.ErrorMessage)
}

func TestManager_Stop_CreatesLogAndUpdatesStatus(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:        "inst-3",
		Name:      "running-instance",
		Namespace: "stack-test-user",
		Status:    models.StackStatusRunning,
	}
	require.NoError(t, instanceRepo.Create(inst))

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           hub,
		MaxConcurrent: 2,
	})

	logID, err := mgr.StopWithCharts(context.Background(), inst, nil)
	assert.NoError(t, err)
	assert.NotEmpty(t, logID)

	// Verify deployment log was created with stop action. The async goroutine
	// may complete before we read, so accept either intermediate or final state.
	log, err := logRepo.FindByID(context.Background(), logID)
	assert.NoError(t, err)
	assert.Equal(t, models.DeployActionStop, log.Action)
	assert.Contains(t, []string{models.DeployLogRunning, models.DeployLogSuccess}, log.Status)
	assert.Equal(t, inst.ID, log.StackInstanceID)

	// Wait for async completion.
	time.Sleep(200 * time.Millisecond)

	// Verify final status (StopWithCharts without charts finalizes to stopped).
	final, err := instanceRepo.FindByID(inst.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.StackStatusStopped, final.Status)
}

func TestManager_StopWithCharts_NoCharts(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:        "inst-4",
		Name:      "running-instance",
		Namespace: "stack-test-user",
		Status:    models.StackStatusRunning,
	}
	require.NoError(t, instanceRepo.Create(inst))

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           hub,
		MaxConcurrent: 2,
	})

	logID, err := mgr.StopWithCharts(context.Background(), inst, []ChartDeployInfo{})
	assert.NoError(t, err)
	assert.NotEmpty(t, logID)

	// Wait for async completion.
	time.Sleep(200 * time.Millisecond)

	// Verify instance is stopped.
	final, err := instanceRepo.FindByID(inst.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.StackStatusStopped, final.Status)
}

func TestManager_Deploy_LogRepoError(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	logRepo.err = errors.New("db error")

	inst := &models.StackInstance{
		ID:     "inst-5",
		Name:   "test",
		Status: models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
	})

	_, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1"},
		Charts:     []ChartDeployInfo{},
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "creating deployment log")
}

func TestManager_Deploy_InstanceRepoUpdateError(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	inst := &models.StackInstance{
		ID:     "inst-6",
		Name:   "test",
		Status: models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	// Set error after creation so log creation succeeds but instance update fails.
	instanceRepo.setError(errors.New("update fail"))

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
	})

	_, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1"},
		Charts:     []ChartDeployInfo{},
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "updating instance status")
}

func TestManager_Stop_LogRepoError(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	logRepo.err = errors.New("db error")

	inst := &models.StackInstance{
		ID:     "inst-7",
		Name:   "test",
		Status: models.StackStatusRunning,
	}
	require.NoError(t, instanceRepo.Create(inst))

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
	})

	_, err := mgr.StopWithCharts(context.Background(), inst, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "creating deployment log")
}

func TestManager_Deploy_NilHub(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	inst := &models.StackInstance{
		ID:     "inst-8",
		Name:   "test",
		Status: models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	// Hub is nil — should not panic.
	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           nil,
		MaxConcurrent: 2,
	})

	logID, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1"},
		Charts:     []ChartDeployInfo{},
	})

	assert.NoError(t, err)
	assert.NotEmpty(t, logID)

	// Wait for async completion — verify no panic.
	time.Sleep(200 * time.Millisecond)

	final, err := instanceRepo.FindByID(inst.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.StackStatusRunning, final.Status)
}

func TestManager_Deploy_ChartsSortedByDeployOrder(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	inst := &models.StackInstance{
		ID:        "inst-9",
		Name:      "sort-test",
		Namespace: "stack-sort-test",
		Status:    models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	// Use nonexistent binary — first chart will fail, but we can
	// verify from the log output which chart was attempted first.
	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
	})

	req := DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1"},
		Charts: []ChartDeployInfo{
			{
				ChartConfig: models.ChartConfig{
					ChartName:     "second-chart",
					RepositoryURL: "oci://example.com/second",
					DeployOrder:   2,
				},
			},
			{
				ChartConfig: models.ChartConfig{
					ChartName:     "first-chart",
					RepositoryURL: "oci://example.com/first",
					DeployOrder:   1,
				},
			},
		},
	}

	logID, err := mgr.Deploy(context.Background(), req)
	assert.NoError(t, err)

	// Wait for async completion.
	time.Sleep(500 * time.Millisecond)

	// The error should reference the first chart (deploy order 1) since it fails first.
	finalLog, err := logRepo.FindByID(context.Background(), logID)
	assert.NoError(t, err)
	assert.Equal(t, models.DeployLogError, finalLog.Status)
	assert.Contains(t, finalLog.ErrorMessage, "first-chart")
}

func TestManager_StopWithCharts_ExecutesUninstall(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:        "inst-stop-charts",
		Name:      "running-with-charts",
		Namespace: "stack-stop-charts",
		Status:    models.StackStatusRunning,
	}
	require.NoError(t, instanceRepo.Create(inst))

	// Use a nonexistent binary — uninstall will fail, but we exercise the full path.
	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           hub,
		MaxConcurrent: 2,
	})

	charts := []ChartDeployInfo{
		{
			ChartConfig: models.ChartConfig{
				ID:          "c1",
				ChartName:   "redis",
				DeployOrder: 1,
			},
		},
		{
			ChartConfig: models.ChartConfig{
				ID:          "c2",
				ChartName:   "nginx",
				DeployOrder: 2,
			},
		},
	}

	logID, err := mgr.StopWithCharts(context.Background(), inst, charts)
	assert.NoError(t, err)
	assert.NotEmpty(t, logID)

	// Wait for async completion.
	time.Sleep(500 * time.Millisecond)

	// Verify final status is error (because helm binary is nonexistent).
	final, err := instanceRepo.FindByID(inst.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.StackStatusError, final.Status)
	assert.NotEmpty(t, final.ErrorMessage)

	// Verify the error references the chart that was attempted first (reverse order = nginx first).
	finalLog, err := logRepo.FindByID(context.Background(), logID)
	assert.NoError(t, err)
	assert.Equal(t, models.DeployLogError, finalLog.Status)
	assert.Contains(t, finalLog.ErrorMessage, "nginx")
	assert.NotNil(t, finalLog.CompletedAt)

	// Verify broadcasts were sent (at least initial "stopping" + final "error").
	assert.Greater(t, hub.messageCount(), 0)
}

func TestManager_StopWithCharts_Success_NoCharts(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:        "inst-stop-ok",
		Name:      "stop-ok",
		Namespace: "stack-stop-ok",
		Status:    models.StackStatusRunning,
	}
	require.NoError(t, instanceRepo.Create(inst))

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           hub,
		MaxConcurrent: 2,
	})

	logID, err := mgr.StopWithCharts(context.Background(), inst, []ChartDeployInfo{})
	assert.NoError(t, err)
	assert.NotEmpty(t, logID)

	// Wait for async.
	time.Sleep(200 * time.Millisecond)

	// No charts to uninstall means success.
	final, err := instanceRepo.FindByID(inst.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.StackStatusStopped, final.Status)
	assert.Empty(t, final.ErrorMessage)

	finalLog, err := logRepo.FindByID(context.Background(), logID)
	assert.NoError(t, err)
	assert.Equal(t, models.DeployLogSuccess, finalLog.Status)
	assert.NotNil(t, finalLog.CompletedAt)
}

func TestManager_FinalizeStop_Success(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:        "inst-fin-ok",
		Name:      "finalize-ok",
		Namespace: "stack-fin-ok",
		Status:    models.StackStatusStopping,
	}
	require.NoError(t, instanceRepo.Create(inst))

	deployLog := &models.DeploymentLog{
		ID:              "log-fin-ok",
		StackInstanceID: inst.ID,
		Action:          models.DeployActionStop,
		Status:          models.DeployLogRunning,
		StartedAt:       time.Now().UTC(),
	}
	require.NoError(t, logRepo.Create(context.Background(), deployLog))

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           hub,
		MaxConcurrent: 2,
	})

	// Call finalizeStop directly for the success path.
	mgr.finalizeStop(inst.ID, deployLog, "all charts uninstalled", nil)

	final, err := instanceRepo.FindByID(inst.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.StackStatusStopped, final.Status)
	assert.Empty(t, final.ErrorMessage)

	finalLog, err := logRepo.FindByID(context.Background(), deployLog.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.DeployLogSuccess, finalLog.Status)
	assert.Equal(t, "all charts uninstalled", finalLog.Output)
	assert.NotNil(t, finalLog.CompletedAt)

	// Should have broadcast the stopped status.
	assert.Greater(t, hub.messageCount(), 0)
}

func TestManager_FinalizeStop_Error(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:        "inst-fin-err",
		Name:      "finalize-err",
		Namespace: "stack-fin-err",
		Status:    models.StackStatusStopping,
	}
	require.NoError(t, instanceRepo.Create(inst))

	deployLog := &models.DeploymentLog{
		ID:              "log-fin-err",
		StackInstanceID: inst.ID,
		Action:          models.DeployActionStop,
		Status:          models.DeployLogRunning,
		StartedAt:       time.Now().UTC(),
	}
	require.NoError(t, logRepo.Create(context.Background(), deployLog))

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           hub,
		MaxConcurrent: 2,
	})

	stopErr := fmt.Errorf("uninstalling chart %q: helm command failed: exit status 1", "nginx")
	mgr.finalizeStop(inst.ID, deployLog, "partial output", stopErr)

	final, err := instanceRepo.FindByID(inst.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.StackStatusError, final.Status)
	// Error message is sanitized — no raw Helm output.
	assert.Equal(t, `uninstalling chart "nginx": operation failed`, final.ErrorMessage)

	finalLog, err := logRepo.FindByID(context.Background(), deployLog.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.DeployLogError, finalLog.Status)
	assert.Equal(t, `uninstalling chart "nginx": operation failed`, finalLog.ErrorMessage)
	assert.Equal(t, "partial output", finalLog.Output)
	assert.NotNil(t, finalLog.CompletedAt)

	assert.Greater(t, hub.messageCount(), 0)
}

func TestManager_FinalizeStop_InstanceNotFound(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           nil,
		MaxConcurrent: 2,
	})

	// Should not panic when instance is not found.
	orphanLog := &models.DeploymentLog{ID: "some-log-id", StackInstanceID: "nonexistent-id"}
	mgr.finalizeStop("nonexistent-id", orphanLog, "output", nil)
}

func TestManager_FinalizeDeploy_InstanceNotFound(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           nil,
		MaxConcurrent: 2,
	})

	// Should not panic when instance is not found.
	orphanLog := &models.DeploymentLog{ID: "some-log-id", StackInstanceID: "nonexistent-id"}
	mgr.finalizeDeploy("nonexistent-id", orphanLog, "output", nil, "")
}

func TestManager_BroadcastStatusWithError_NilHub(t *testing.T) {
	t.Parallel()

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  newMockInstanceRepo(),
		DeployLogRepo: newMockDeployLogRepo(),
		Hub:           nil,
		MaxConcurrent: 2,
	})

	// Should not panic with nil hub.
	assert.NotPanics(t, func() {
		mgr.broadcastStatusWithError("inst-1", models.StackStatusError, "log-1", "some error")
	})
}

func TestManager_BroadcastStatus_NilHub(t *testing.T) {
	t.Parallel()

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  newMockInstanceRepo(),
		DeployLogRepo: newMockDeployLogRepo(),
		Hub:           nil,
		MaxConcurrent: 2,
	})

	// Should not panic with nil hub.
	assert.NotPanics(t, func() {
		mgr.broadcastStatus("inst-1", models.StackStatusRunning, "log-1")
	})
}

func TestManager_BroadcastLog_NilHub(t *testing.T) {
	t.Parallel()

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  newMockInstanceRepo(),
		DeployLogRepo: newMockDeployLogRepo(),
		Hub:           nil,
		MaxConcurrent: 2,
	})

	// Should not panic with nil hub.
	assert.NotPanics(t, func() {
		mgr.broadcastLog("inst-1", "log-1", "some log line")
	})
}

func TestManager_BroadcastLog_WithHub(t *testing.T) {
	t.Parallel()

	hub := &mockBroadcaster{}
	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  newMockInstanceRepo(),
		DeployLogRepo: newMockDeployLogRepo(),
		Hub:           hub,
		MaxConcurrent: 2,
	})

	mgr.broadcastLog("inst-1", "log-1", "deploying chart nginx...")

	assert.Equal(t, 1, hub.messageCount())
}

func TestManager_BroadcastStatusWithError_WithHub(t *testing.T) {
	t.Parallel()

	hub := &mockBroadcaster{}
	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  newMockInstanceRepo(),
		DeployLogRepo: newMockDeployLogRepo(),
		Hub:           hub,
		MaxConcurrent: 2,
	})

	mgr.broadcastStatusWithError("inst-1", models.StackStatusError, "log-1", "chart failed")

	assert.Equal(t, 1, hub.messageCount())
}

func TestManager_StopWithCharts_ReversesDeployOrder(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:        "inst-reverse-order",
		Name:      "reverse-order",
		Namespace: "stack-reverse-order",
		Status:    models.StackStatusRunning,
	}
	require.NoError(t, instanceRepo.Create(inst))

	// Use nonexistent binary so we can check which chart fails first.
	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           hub,
		MaxConcurrent: 2,
	})

	charts := []ChartDeployInfo{
		{
			ChartConfig: models.ChartConfig{
				ChartName:   "first-chart",
				DeployOrder: 1,
			},
		},
		{
			ChartConfig: models.ChartConfig{
				ChartName:   "second-chart",
				DeployOrder: 2,
			},
		},
		{
			ChartConfig: models.ChartConfig{
				ChartName:   "third-chart",
				DeployOrder: 3,
			},
		},
	}

	logID, err := mgr.StopWithCharts(context.Background(), inst, charts)
	assert.NoError(t, err)

	// Wait for async.
	time.Sleep(500 * time.Millisecond)

	// The error should reference the third chart (highest deploy order),
	// which should be uninstalled first (reverse order).
	finalLog, err := logRepo.FindByID(context.Background(), logID)
	assert.NoError(t, err)
	assert.Equal(t, models.DeployLogError, finalLog.Status)
	assert.Contains(t, finalLog.ErrorMessage, "third-chart")
}

func TestManager_Stop_InstanceRepoUpdateError(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	inst := &models.StackInstance{
		ID:     "inst-stop-update-err",
		Name:   "test",
		Status: models.StackStatusRunning,
	}
	require.NoError(t, instanceRepo.Create(inst))

	// Set error after creation so log creation succeeds but instance update fails.
	instanceRepo.setError(errors.New("update fail"))

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
	})

	_, err := mgr.StopWithCharts(context.Background(), inst, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "updating instance status")
}

func TestManager_StopWithCharts_LogRepoError(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	logRepo.err = errors.New("db error")

	inst := &models.StackInstance{
		ID:     "inst-swc-log-err",
		Name:   "test",
		Status: models.StackStatusRunning,
	}
	require.NoError(t, instanceRepo.Create(inst))

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
	})

	_, err := mgr.StopWithCharts(context.Background(), inst, []ChartDeployInfo{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "creating deployment log")
}

func TestManager_StopWithCharts_InstanceUpdateError(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	inst := &models.StackInstance{
		ID:     "inst-swc-upd-err",
		Name:   "test",
		Status: models.StackStatusRunning,
	}
	require.NoError(t, instanceRepo.Create(inst))

	instanceRepo.setError(errors.New("update fail"))

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
	})

	_, err := mgr.StopWithCharts(context.Background(), inst, []ChartDeployInfo{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "updating instance status")
}

func TestSanitizeDeployError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "deploying chart error",
			err:  fmt.Errorf("deploying chart %q: helm command failed: exit status 1\nsome sensitive output", "nginx"),
			want: `deploying chart "nginx": operation failed`,
		},
		{
			name: "uninstalling chart error",
			err:  fmt.Errorf("uninstalling chart %q: helm command failed: exit status 1", "redis"),
			want: `uninstalling chart "redis": operation failed`,
		},
		{
			name: "creating temp directory error",
			err:  fmt.Errorf("creating temp directory: permission denied"),
			want: "creating temp directory: operation failed",
		},
		{
			name: "unknown error format",
			err:  errors.New("something totally unexpected with secrets"),
			want: "deployment operation failed",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := sanitizeDeployError(tt.err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestTruncateString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{
			name:   "no truncation needed",
			input:  "short",
			maxLen: 10,
			want:   "short",
		},
		{
			name:   "exact length",
			input:  "exact",
			maxLen: 5,
			want:   "exact",
		},
		{
			name:   "truncated with ellipsis",
			input:  "this is a long string",
			maxLen: 10,
			want:   "this is...",
		},
		{
			name:   "very small max",
			input:  "abcdef",
			maxLen: 3,
			want:   "abc",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := truncateString(tt.input, tt.maxLen)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestManager_FinalizeDeploy_OutputTruncation(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	inst := &models.StackInstance{
		ID:        "inst-trunc",
		Name:      "truncate-test",
		Namespace: "stack-trunc",
		Status:    models.StackStatusDeploying,
	}
	require.NoError(t, instanceRepo.Create(inst))

	deployLog := &models.DeploymentLog{
		ID:              "log-trunc",
		StackInstanceID: inst.ID,
		Action:          models.DeployActionDeploy,
		Status:          models.DeployLogRunning,
		StartedAt:       time.Now().UTC(),
	}
	require.NoError(t, logRepo.Create(context.Background(), deployLog))

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           nil,
		MaxConcurrent: 2,
	})

	// Create output larger than maxOutputLen (64KB).
	largeOutput := strings.Repeat("x", maxOutputLen+1000)
	mgr.finalizeDeploy(inst.ID, deployLog, largeOutput, nil, "")

	finalLog, err := logRepo.FindByID(context.Background(), deployLog.ID)
	assert.NoError(t, err)
	assert.LessOrEqual(t, len(finalLog.Output), maxOutputLen)
}

// ---- mock helm executor ----

type mockHelmExecutor struct {
	mu             sync.Mutex
	installFunc    func(ctx context.Context, req InstallRequest) (string, error)
	uninstallFunc  func(ctx context.Context, req UninstallRequest) (string, error)
	installCalls   []InstallRequest
	uninstallCalls []UninstallRequest
	timeout        time.Duration
}

func (m *mockHelmExecutor) Install(ctx context.Context, req InstallRequest) (string, error) {
	m.mu.Lock()
	m.installCalls = append(m.installCalls, req)
	m.mu.Unlock()
	if m.installFunc != nil {
		return m.installFunc(ctx, req)
	}
	return "installed " + req.ReleaseName, nil
}

func (m *mockHelmExecutor) Uninstall(ctx context.Context, req UninstallRequest) (string, error) {
	m.mu.Lock()
	m.uninstallCalls = append(m.uninstallCalls, req)
	m.mu.Unlock()
	if m.uninstallFunc != nil {
		return m.uninstallFunc(ctx, req)
	}
	return "uninstalled " + req.ReleaseName, nil
}

func (m *mockHelmExecutor) Status(_ context.Context, releaseName, _ string) (*ReleaseStatus, error) {
	return &ReleaseStatus{Name: releaseName, Info: releaseInfo{Status: "deployed"}}, nil
}

func (m *mockHelmExecutor) ListReleases(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

func (m *mockHelmExecutor) Timeout() time.Duration {
	if m.timeout > 0 {
		return m.timeout
	}
	return 30 * time.Second
}

func (m *mockHelmExecutor) getUninstallCalls() []UninstallRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]UninstallRequest, len(m.uninstallCalls))
	copy(cp, m.uninstallCalls)
	return cp
}

// ---- rollback tests ----

func TestManager_Deploy_PartialRollback(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:                "inst-rollback-1",
		StackDefinitionID: "def-1",
		Name:              "partial-rollback",
		Namespace:         "stack-rollback-test",
		OwnerID:           "user-1",
		Branch:            "main",
		Status:            models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	helmMock := &mockHelmExecutor{
		installFunc: func(_ context.Context, req InstallRequest) (string, error) {
			// Charts 1 and 2 succeed, chart 3 fails.
			if req.ReleaseName == "chart-c" {
				return "install failed", fmt.Errorf("helm command failed: exit status 1")
			}
			return "installed " + req.ReleaseName, nil
		},
	}

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: helmMock},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           hub,
		MaxConcurrent: 2,
	})

	req := DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "test-def"},
		Charts: []ChartDeployInfo{
			{ChartConfig: models.ChartConfig{ChartName: "chart-a", DeployOrder: 1}},
			{ChartConfig: models.ChartConfig{ChartName: "chart-b", DeployOrder: 2}},
			{ChartConfig: models.ChartConfig{ChartName: "chart-c", DeployOrder: 3}},
		},
	}

	logID, err := mgr.Deploy(context.Background(), req)
	assert.NoError(t, err)
	assert.NotEmpty(t, logID)

	// Wait for async completion.
	time.Sleep(500 * time.Millisecond)

	// Verify instance status is error with original failure message.
	final, err := instanceRepo.FindByID(inst.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.StackStatusError, final.Status)
	assert.Contains(t, final.ErrorMessage, "chart-c")

	// Verify deployment log contains rollback output.
	finalLog, err := logRepo.FindByID(context.Background(), logID)
	assert.NoError(t, err)
	assert.Equal(t, models.DeployLogError, finalLog.Status)
	assert.Contains(t, finalLog.Output, "Rollback: chart-b")
	assert.Contains(t, finalLog.Output, "Rollback: chart-a")

	// Verify uninstall was called for chart-b and chart-a (reverse order),
	// but NOT for chart-c (the one that failed).
	uninstalls := helmMock.getUninstallCalls()
	require.Len(t, uninstalls, 2)
	assert.Equal(t, "chart-b", uninstalls[0].ReleaseName)
	assert.Equal(t, "chart-a", uninstalls[1].ReleaseName)
}

func TestManager_Deploy_PartialRollback_RollbackFails(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:                "inst-rollback-2",
		StackDefinitionID: "def-1",
		Name:              "rollback-fails",
		Namespace:         "stack-rollback-fail",
		OwnerID:           "user-1",
		Branch:            "main",
		Status:            models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	helmMock := &mockHelmExecutor{
		installFunc: func(_ context.Context, req InstallRequest) (string, error) {
			// Chart 1 succeeds, chart 2 fails.
			if req.ReleaseName == "chart-b" {
				return "install failed", fmt.Errorf("helm command failed: exit status 1")
			}
			return "installed " + req.ReleaseName, nil
		},
		uninstallFunc: func(_ context.Context, req UninstallRequest) (string, error) {
			// Rollback also fails.
			return "uninstall failed", fmt.Errorf("helm uninstall failed: exit status 1")
		},
	}

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: helmMock},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           hub,
		MaxConcurrent: 2,
	})

	req := DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "test-def"},
		Charts: []ChartDeployInfo{
			{ChartConfig: models.ChartConfig{ChartName: "chart-a", DeployOrder: 1}},
			{ChartConfig: models.ChartConfig{ChartName: "chart-b", DeployOrder: 2}},
			{ChartConfig: models.ChartConfig{ChartName: "chart-c", DeployOrder: 3}},
		},
	}

	logID, err := mgr.Deploy(context.Background(), req)
	assert.NoError(t, err)
	assert.NotEmpty(t, logID)

	// Wait for async completion.
	time.Sleep(500 * time.Millisecond)

	// Verify instance status is error with the ORIGINAL deploy failure (not rollback failure).
	final, err := instanceRepo.FindByID(inst.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.StackStatusError, final.Status)
	assert.Contains(t, final.ErrorMessage, "chart-b")

	// Verify deployment log contains both deploy error and rollback error.
	finalLog, err := logRepo.FindByID(context.Background(), logID)
	assert.NoError(t, err)
	assert.Equal(t, models.DeployLogError, finalLog.Status)
	assert.Contains(t, finalLog.Output, "ERROR:")
	assert.Contains(t, finalLog.Output, "ROLLBACK ERROR:")

	// Verify uninstall was attempted for chart-a despite failure.
	uninstalls := helmMock.getUninstallCalls()
	require.Len(t, uninstalls, 1)
	assert.Equal(t, "chart-a", uninstalls[0].ReleaseName)
}

// ---- Clean tests ----

func TestManager_Clean_Success(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:        "inst-clean-1",
		Name:      "clean-test",
		Namespace: "stack-clean-test",
		Status:    models.StackStatusRunning,
	}
	require.NoError(t, instanceRepo.Create(inst))

	helmMock := &mockHelmExecutor{}

	// Create a real k8s.Client with a fake clientset that has the namespace.
	fakeCS := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "stack-clean-test"},
	})
	k8sClient := k8s.NewClientFromInterface(fakeCS)

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: helmMock, k8sClient: k8sClient},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           hub,
		MaxConcurrent: 2,
	})

	charts := []models.ChartConfig{
		{ChartName: "redis", DeployOrder: 1},
		{ChartName: "nginx", DeployOrder: 2},
	}

	logID, err := mgr.Clean(context.Background(), inst, charts)
	assert.NoError(t, err)
	assert.NotEmpty(t, logID)

	// Verify deployment log was created with clean action.
	log, err := logRepo.FindByID(context.Background(), logID)
	assert.NoError(t, err)
	assert.Equal(t, models.DeployActionClean, log.Action)
	assert.Equal(t, models.DeployLogRunning, log.Status)

	// Verify instance status was updated to cleaning.
	updated, err := instanceRepo.FindByID(inst.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.StackStatusCleaning, updated.Status)

	// Wait for async completion.
	time.Sleep(500 * time.Millisecond)

	// Verify final status is draft.
	final, err := instanceRepo.FindByID(inst.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.StackStatusDraft, final.Status)
	assert.Empty(t, final.ErrorMessage)
	assert.Nil(t, final.LastDeployedAt)

	// Verify log was updated to success.
	finalLog, err := logRepo.FindByID(context.Background(), logID)
	assert.NoError(t, err)
	assert.Equal(t, models.DeployLogSuccess, finalLog.Status)
	assert.NotNil(t, finalLog.CompletedAt)
	assert.Contains(t, finalLog.Output, "Namespace stack-clean-test deleted")

	// Verify uninstall was called for each chart in reverse order.
	uninstalls := helmMock.getUninstallCalls()
	require.Len(t, uninstalls, 2)
	assert.Equal(t, "nginx", uninstalls[0].ReleaseName)
	assert.Equal(t, "redis", uninstalls[1].ReleaseName)

	// Verify broadcasts were sent.
	assert.Greater(t, hub.messageCount(), 0)

	// We used a real k8s.Client with a fake clientset — namespace was deleted.
}

func TestManager_Clean_Success_NoK8sClient(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:        "inst-clean-nok8s",
		Name:      "clean-nok8s",
		Namespace: "stack-clean-nok8s",
		Status:    models.StackStatusStopped,
	}
	require.NoError(t, instanceRepo.Create(inst))

	helmMock := &mockHelmExecutor{}

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: helmMock},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           hub,
		MaxConcurrent: 2,
	})

	charts := []models.ChartConfig{
		{ChartName: "app", DeployOrder: 1},
	}

	logID, err := mgr.Clean(context.Background(), inst, charts)
	assert.NoError(t, err)
	assert.NotEmpty(t, logID)

	// Wait for async completion.
	time.Sleep(500 * time.Millisecond)

	// Verify final status is draft (namespace delete skipped).
	final, err := instanceRepo.FindByID(inst.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.StackStatusDraft, final.Status)
	assert.Empty(t, final.ErrorMessage)

	// Verify uninstall was called.
	uninstalls := helmMock.getUninstallCalls()
	require.Len(t, uninstalls, 1)
	assert.Equal(t, "app", uninstalls[0].ReleaseName)

	// Verify log does NOT contain namespace deletion.
	finalLog, err := logRepo.FindByID(context.Background(), logID)
	assert.NoError(t, err)
	assert.Equal(t, models.DeployLogSuccess, finalLog.Status)
	assert.NotContains(t, finalLog.Output, "Namespace")
}

func TestManager_Clean_HelmFails(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:        "inst-clean-fail",
		Name:      "clean-fail",
		Namespace: "stack-clean-fail",
		Status:    models.StackStatusError,
	}
	require.NoError(t, instanceRepo.Create(inst))

	helmMock := &mockHelmExecutor{
		uninstallFunc: func(_ context.Context, _ UninstallRequest) (string, error) {
			return "uninstall failed", fmt.Errorf("helm command failed: exit status 1")
		},
	}

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: helmMock},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           hub,
		MaxConcurrent: 2,
	})

	charts := []models.ChartConfig{
		{ChartName: "nginx", DeployOrder: 1},
	}

	logID, err := mgr.Clean(context.Background(), inst, charts)
	assert.NoError(t, err)
	assert.NotEmpty(t, logID)

	// Wait for async completion.
	time.Sleep(500 * time.Millisecond)

	// Best-effort: uninstall failures are warnings, not errors.
	// With no K8s client, namespace deletion is skipped, so the clean succeeds.
	final, err := instanceRepo.FindByID(inst.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.StackStatusDraft, final.Status)
	assert.Empty(t, final.ErrorMessage)

	// Verify log was updated to success with warning in output.
	finalLog, err := logRepo.FindByID(context.Background(), logID)
	assert.NoError(t, err)
	assert.Equal(t, models.DeployLogSuccess, finalLog.Status)
	assert.Empty(t, finalLog.ErrorMessage)
	assert.NotNil(t, finalLog.CompletedAt)
	assert.Contains(t, finalLog.Output, "WARNING:")
	assert.Contains(t, finalLog.Output, "1 of 1 charts failed to uninstall")

	// Verify broadcasts were sent.
	assert.Greater(t, hub.messageCount(), 0)
}

func TestManager_Clean_ReleasesAlreadyGone(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	// Simulate a stopped instance whose releases were already removed by stop.
	inst := &models.StackInstance{
		ID:        "inst-clean-stopped",
		Name:      "clean-stopped",
		Namespace: "stack-clean-stopped",
		Status:    models.StackStatusStopped,
	}
	require.NoError(t, instanceRepo.Create(inst))

	// Helm returns the "release: not found" message that the real CLI produces.
	helmMock := &mockHelmExecutor{
		uninstallFunc: func(_ context.Context, req UninstallRequest) (string, error) {
			msg := fmt.Sprintf("Error: uninstall: Release not loaded: %s: release: not found", req.ReleaseName)
			return msg, fmt.Errorf("helm command failed: exit status 1")
		},
	}

	fakeClient := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "stack-clean-stopped"},
	})
	k8sClient := k8s.NewClientFromInterface(fakeClient)

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: helmMock, k8sClient: k8sClient},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           hub,
		MaxConcurrent: 2,
	})

	charts := []models.ChartConfig{
		{ChartName: "traefik", DeployOrder: 1},
		{ChartName: "azurite-storage", DeployOrder: 2},
		{ChartName: "frontend-app", DeployOrder: 3},
	}

	logID, err := mgr.Clean(context.Background(), inst, charts)
	assert.NoError(t, err)
	assert.NotEmpty(t, logID)

	// Wait for async completion.
	time.Sleep(500 * time.Millisecond)

	// Should succeed — "not found" releases are treated as already removed.
	final, err := instanceRepo.FindByID(inst.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.StackStatusDraft, final.Status)
	assert.Empty(t, final.ErrorMessage)

	finalLog, err := logRepo.FindByID(context.Background(), logID)
	assert.NoError(t, err)
	assert.Equal(t, models.DeployLogSuccess, finalLog.Status)
	assert.Contains(t, finalLog.Output, "already removed")
	assert.Contains(t, finalLog.Output, "Namespace stack-clean-stopped deleted")
}

func TestManager_Deploy_FirstChartFails_NoRollback(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:                "inst-rollback-3",
		StackDefinitionID: "def-1",
		Name:              "first-chart-fails",
		Namespace:         "stack-no-rollback",
		OwnerID:           "user-1",
		Branch:            "main",
		Status:            models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	helmMock := &mockHelmExecutor{
		installFunc: func(_ context.Context, _ InstallRequest) (string, error) {
			return "install failed", fmt.Errorf("helm command failed: exit status 1")
		},
	}

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: helmMock},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           hub,
		MaxConcurrent: 2,
	})

	req := DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "test-def"},
		Charts: []ChartDeployInfo{
			{ChartConfig: models.ChartConfig{ChartName: "chart-a", DeployOrder: 1}},
			{ChartConfig: models.ChartConfig{ChartName: "chart-b", DeployOrder: 2}},
		},
	}

	logID, err := mgr.Deploy(context.Background(), req)
	assert.NoError(t, err)
	assert.NotEmpty(t, logID)

	// Wait for async completion.
	time.Sleep(500 * time.Millisecond)

	// Verify instance is in error state.
	final, err := instanceRepo.FindByID(inst.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.StackStatusError, final.Status)
	assert.Contains(t, final.ErrorMessage, "chart-a")

	// Verify NO uninstall calls were made (nothing to roll back).
	uninstalls := helmMock.getUninstallCalls()
	assert.Empty(t, uninstalls)

	// Verify log does not contain rollback output.
	finalLog, err := logRepo.FindByID(context.Background(), logID)
	assert.NoError(t, err)
	assert.NotContains(t, finalLog.Output, "Rollback:")
}

// ---- mergeQuotaOverride tests ----

func TestMergeQuotaOverride(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cluster  *models.ResourceQuotaConfig
		override *models.InstanceQuotaOverride
		want     *models.ResourceQuotaConfig
	}{
		{
			name: "all fields overridden",
			cluster: &models.ResourceQuotaConfig{
				CPURequest:    "100m",
				CPULimit:      "500m",
				MemoryRequest: "128Mi",
				MemoryLimit:   "512Mi",
				StorageLimit:  "1Gi",
				PodLimit:      10,
			},
			override: &models.InstanceQuotaOverride{
				CPURequest:    "200m",
				CPULimit:      "1000m",
				MemoryRequest: "256Mi",
				MemoryLimit:   "1Gi",
				StorageLimit:  "5Gi",
				PodLimit:      intPtr(20),
			},
			want: &models.ResourceQuotaConfig{
				CPURequest:    "200m",
				CPULimit:      "1000m",
				MemoryRequest: "256Mi",
				MemoryLimit:   "1Gi",
				StorageLimit:  "5Gi",
				PodLimit:      20,
			},
		},
		{
			name: "no fields overridden",
			cluster: &models.ResourceQuotaConfig{
				CPURequest:    "100m",
				CPULimit:      "500m",
				MemoryRequest: "128Mi",
				MemoryLimit:   "512Mi",
				StorageLimit:  "1Gi",
				PodLimit:      10,
			},
			override: &models.InstanceQuotaOverride{},
			want: &models.ResourceQuotaConfig{
				CPURequest:    "100m",
				CPULimit:      "500m",
				MemoryRequest: "128Mi",
				MemoryLimit:   "512Mi",
				StorageLimit:  "1Gi",
				PodLimit:      10,
			},
		},
		{
			name: "partial override - only CPU",
			cluster: &models.ResourceQuotaConfig{
				CPURequest:    "100m",
				CPULimit:      "500m",
				MemoryRequest: "128Mi",
				MemoryLimit:   "512Mi",
				StorageLimit:  "1Gi",
				PodLimit:      10,
			},
			override: &models.InstanceQuotaOverride{
				CPURequest: "250m",
				CPULimit:   "750m",
			},
			want: &models.ResourceQuotaConfig{
				CPURequest:    "250m",
				CPULimit:      "750m",
				MemoryRequest: "128Mi",
				MemoryLimit:   "512Mi",
				StorageLimit:  "1Gi",
				PodLimit:      10,
			},
		},
		{
			name: "override PodLimit to zero",
			cluster: &models.ResourceQuotaConfig{
				PodLimit: 10,
			},
			override: &models.InstanceQuotaOverride{
				PodLimit: intPtr(0),
			},
			want: &models.ResourceQuotaConfig{
				PodLimit: 0,
			},
		},
		{
			name: "nil PodLimit keeps cluster value",
			cluster: &models.ResourceQuotaConfig{
				PodLimit: 10,
			},
			override: &models.InstanceQuotaOverride{
				PodLimit: nil,
			},
			want: &models.ResourceQuotaConfig{
				PodLimit: 10,
			},
		},
		{
			name:    "empty cluster with overrides",
			cluster: &models.ResourceQuotaConfig{},
			override: &models.InstanceQuotaOverride{
				CPURequest:   "500m",
				MemoryLimit:  "2Gi",
				StorageLimit: "10Gi",
				PodLimit:     intPtr(50),
			},
			want: &models.ResourceQuotaConfig{
				CPURequest:   "500m",
				MemoryLimit:  "2Gi",
				StorageLimit: "10Gi",
				PodLimit:     50,
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := mergeQuotaOverride(tt.cluster, tt.override)
			assert.Equal(t, tt.want.CPURequest, got.CPURequest)
			assert.Equal(t, tt.want.CPULimit, got.CPULimit)
			assert.Equal(t, tt.want.MemoryRequest, got.MemoryRequest)
			assert.Equal(t, tt.want.MemoryLimit, got.MemoryLimit)
			assert.Equal(t, tt.want.StorageLimit, got.StorageLimit)
			assert.Equal(t, tt.want.PodLimit, got.PodLimit)
		})
	}
}

func intPtr(n int) *int {
	return &n
}

// ---- applyNamespaceQuotas tests ----

// mockQuotaRepo implements models.ResourceQuotaRepository for tests.
type mockQuotaRepo struct {
	quota *models.ResourceQuotaConfig
	err   error
}

func (m *mockQuotaRepo) GetByClusterID(_ context.Context, _ string) (*models.ResourceQuotaConfig, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.quota, nil
}

func (m *mockQuotaRepo) Upsert(_ context.Context, _ *models.ResourceQuotaConfig) error {
	return nil
}

func (m *mockQuotaRepo) Delete(_ context.Context, _ string) error {
	return nil
}

// mockQuotaOverrideRepo implements models.InstanceQuotaOverrideRepository for tests.
type mockQuotaOverrideRepo struct {
	override *models.InstanceQuotaOverride
	err      error
}

func (m *mockQuotaOverrideRepo) GetByInstanceID(_ context.Context, _ string) (*models.InstanceQuotaOverride, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.override, nil
}

func (m *mockQuotaOverrideRepo) Upsert(_ context.Context, _ *models.InstanceQuotaOverride) error {
	return nil
}

func (m *mockQuotaOverrideRepo) Delete(_ context.Context, _ string) error {
	return nil
}

func TestManager_ApplyNamespaceQuotas_SkipsWhenEmpty(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	inst := &models.StackInstance{
		ID:        "inst-quota-empty",
		Name:      "quota-empty",
		Namespace: "stack-quota-empty",
		ClusterID: "cluster-1",
		Status:    models.StackStatusRunning,
	}
	require.NoError(t, instanceRepo.Create(inst))

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: &mockHelmExecutor{}},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: newMockDeployLogRepo(),
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
		QuotaRepo:     &mockQuotaRepo{quota: &models.ResourceQuotaConfig{}},
	})

	err := mgr.applyNamespaceQuotas(context.Background(), inst.ID, inst.Namespace)
	assert.NoError(t, err)
}

func TestManager_ApplyNamespaceQuotas_InstanceNotFound(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: &mockHelmExecutor{}},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: newMockDeployLogRepo(),
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
		QuotaRepo:     &mockQuotaRepo{quota: &models.ResourceQuotaConfig{CPULimit: "1"}},
	})

	err := mgr.applyNamespaceQuotas(context.Background(), "nonexistent", "ns")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "finding instance")
}

func TestManager_ApplyNamespaceQuotas_ResolveClusterError(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	inst := &models.StackInstance{
		ID:        "inst-quota-resolve-err",
		Name:      "quota-resolve-err",
		Namespace: "stack-quota-resolve-err",
		ClusterID: "bad-cluster",
		Status:    models.StackStatusRunning,
	}
	require.NoError(t, instanceRepo.Create(inst))

	mgr := NewManager(ManagerConfig{
		Registry: &mockClusterResolver{
			helm:       &mockHelmExecutor{},
			resolveErr: fmt.Errorf("cluster not found"),
		},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: newMockDeployLogRepo(),
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
		QuotaRepo:     &mockQuotaRepo{quota: &models.ResourceQuotaConfig{CPULimit: "1"}},
	})

	err := mgr.applyNamespaceQuotas(context.Background(), inst.ID, inst.Namespace)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "resolving cluster")
}

func TestManager_ApplyNamespaceQuotas_FallsBackOnQuotaRepoError(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	inst := &models.StackInstance{
		ID:        "inst-quota-repo-err",
		Name:      "quota-repo-err",
		Namespace: "stack-quota-repo-err",
		ClusterID: "cluster-1",
		Status:    models.StackStatusRunning,
	}
	require.NoError(t, instanceRepo.Create(inst))

	// When quota repo errors, it falls back to empty config (skips)
	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: &mockHelmExecutor{}},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: newMockDeployLogRepo(),
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
		QuotaRepo:     &mockQuotaRepo{err: fmt.Errorf("quota repo error")},
	})

	// When GetByClusterID errors, it falls back to empty quota config, which is skipped.
	err := mgr.applyNamespaceQuotas(context.Background(), inst.ID, inst.Namespace)
	assert.NoError(t, err)
}

func TestManager_ApplyNamespaceQuotas_WithOverride(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	inst := &models.StackInstance{
		ID:        "inst-quota-override",
		Name:      "quota-override",
		Namespace: "stack-quota-override",
		ClusterID: "cluster-1",
		Status:    models.StackStatusRunning,
	}
	require.NoError(t, instanceRepo.Create(inst))

	fakeCS := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: inst.Namespace},
	})
	k8sClient := k8s.NewClientFromInterface(fakeCS)

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: &mockHelmExecutor{}, k8sClient: k8sClient},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: newMockDeployLogRepo(),
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
		QuotaRepo: &mockQuotaRepo{
			quota: &models.ResourceQuotaConfig{
				CPURequest:    "100m",
				CPULimit:      "500m",
				MemoryRequest: "128Mi",
				MemoryLimit:   "512Mi",
			},
		},
		QuotaOverrideRepo: &mockQuotaOverrideRepo{
			override: &models.InstanceQuotaOverride{
				CPULimit:    "1000m",
				MemoryLimit: "1Gi",
			},
		},
	})

	err := mgr.applyNamespaceQuotas(context.Background(), inst.ID, inst.Namespace)
	assert.NoError(t, err)
}

func TestManager_ApplyNamespaceQuotas_K8sClientError(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	inst := &models.StackInstance{
		ID:        "inst-quota-k8s-err",
		Name:      "quota-k8s-err",
		Namespace: "stack-quota-k8s-err",
		ClusterID: "cluster-1",
		Status:    models.StackStatusRunning,
	}
	require.NoError(t, instanceRepo.Create(inst))

	mgr := NewManager(ManagerConfig{
		Registry: &mockClusterResolver{
			helm:   &mockHelmExecutor{},
			k8sErr: fmt.Errorf("k8s client unavailable"),
		},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: newMockDeployLogRepo(),
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
		QuotaRepo: &mockQuotaRepo{
			quota: &models.ResourceQuotaConfig{
				CPULimit: "500m",
			},
		},
	})

	err := mgr.applyNamespaceQuotas(context.Background(), inst.ID, inst.Namespace)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "getting k8s client")
}

func TestManager_ApplyNamespaceQuotas_OverrideRepoError_UsesClusterDefaults(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	inst := &models.StackInstance{
		ID:        "inst-quota-ov-err",
		Name:      "quota-ov-err",
		Namespace: "stack-quota-ov-err",
		ClusterID: "cluster-1",
		Status:    models.StackStatusRunning,
	}
	require.NoError(t, instanceRepo.Create(inst))

	fakeCS := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: inst.Namespace},
	})
	k8sClient := k8s.NewClientFromInterface(fakeCS)

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: &mockHelmExecutor{}, k8sClient: k8sClient},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: newMockDeployLogRepo(),
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
		QuotaRepo: &mockQuotaRepo{
			quota: &models.ResourceQuotaConfig{
				CPURequest:    "100m",
				CPULimit:      "500m",
				MemoryRequest: "128Mi",
				MemoryLimit:   "256Mi",
			},
		},
		QuotaOverrideRepo: &mockQuotaOverrideRepo{
			err: fmt.Errorf("override repo error"),
		},
	})

	// When override repo errors, cluster defaults are still applied.
	err := mgr.applyNamespaceQuotas(context.Background(), inst.ID, inst.Namespace)
	assert.NoError(t, err)
}

// ---- finalizeClean error path tests ----

func TestManager_FinalizeClean_InstanceNotFound(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: &mockHelmExecutor{}},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           nil,
		MaxConcurrent: 2,
	})

	// Should not panic when instance is not found.
	orphanLog := &models.DeploymentLog{ID: "log-clean-orphan", StackInstanceID: "nonexistent-id"}
	assert.NotPanics(t, func() {
		mgr.finalizeClean("nonexistent-id", orphanLog, "some output", nil)
	})
}

func TestManager_FinalizeClean_Success(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:        "inst-fin-clean-ok",
		Name:      "finalize-clean-ok",
		Namespace: "stack-fin-clean-ok",
		Status:    models.StackStatusCleaning,
	}
	require.NoError(t, instanceRepo.Create(inst))

	deployLog := &models.DeploymentLog{
		ID:              "log-fin-clean-ok",
		StackInstanceID: inst.ID,
		Action:          models.DeployActionClean,
		Status:          models.DeployLogRunning,
		StartedAt:       time.Now().UTC(),
	}
	require.NoError(t, logRepo.Create(context.Background(), deployLog))

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: &mockHelmExecutor{}},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           hub,
		MaxConcurrent: 2,
	})

	mgr.finalizeClean(inst.ID, deployLog, "namespace deleted", nil)

	final, err := instanceRepo.FindByID(inst.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.StackStatusDraft, final.Status)
	assert.Empty(t, final.ErrorMessage)
	assert.Nil(t, final.LastDeployedAt)

	finalLog, err := logRepo.FindByID(context.Background(), deployLog.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.DeployLogSuccess, finalLog.Status)
	assert.Equal(t, "namespace deleted", finalLog.Output)
	assert.NotNil(t, finalLog.CompletedAt)

	assert.Greater(t, hub.messageCount(), 0)
}

func TestManager_FinalizeClean_Error(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:        "inst-fin-clean-err",
		Name:      "finalize-clean-err",
		Namespace: "stack-fin-clean-err",
		Status:    models.StackStatusCleaning,
	}
	require.NoError(t, instanceRepo.Create(inst))

	deployLog := &models.DeploymentLog{
		ID:              "log-fin-clean-err",
		StackInstanceID: inst.ID,
		Action:          models.DeployActionClean,
		Status:          models.DeployLogRunning,
		StartedAt:       time.Now().UTC(),
	}
	require.NoError(t, logRepo.Create(context.Background(), deployLog))

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: &mockHelmExecutor{}},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           hub,
		MaxConcurrent: 2,
	})

	cleanErr := fmt.Errorf("deleting namespace %q: context deadline exceeded", "stack-fin-clean-err")
	mgr.finalizeClean(inst.ID, deployLog, "partial output", cleanErr)

	final, err := instanceRepo.FindByID(inst.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.StackStatusError, final.Status)
	assert.Equal(t, `deleting namespace "stack-fin-clean-err": operation failed`, final.ErrorMessage)

	finalLog, err := logRepo.FindByID(context.Background(), deployLog.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.DeployLogError, finalLog.Status)
	assert.Equal(t, `deleting namespace "stack-fin-clean-err": operation failed`, finalLog.ErrorMessage)
	assert.Equal(t, "partial output", finalLog.Output)
	assert.NotNil(t, finalLog.CompletedAt)

	assert.Greater(t, hub.messageCount(), 0)
}

// ---- executeDeploy improvements: OCI chart ref, values file ----

func TestManager_ExecuteDeploy_OCIChartRef(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:                "inst-oci",
		StackDefinitionID: "def-1",
		Name:              "oci-test",
		Namespace:         "stack-oci-test",
		OwnerID:           "user-1",
		Branch:            "main",
		Status:            models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	helmMock := &mockHelmExecutor{}

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: helmMock},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           hub,
		MaxConcurrent: 2,
	})

	req := DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "test-def"},
		Charts: []ChartDeployInfo{
			{
				ChartConfig: models.ChartConfig{
					ChartName:     "myapp",
					ChartPath:     "myapp",
					RepositoryURL: "oci://registry.example.com/charts",
					ChartVersion:  "1.2.3",
					DeployOrder:   1,
				},
				ValuesYAML: []byte("key: value\n"),
			},
		},
	}

	logID, err := mgr.Deploy(context.Background(), req)
	assert.NoError(t, err)
	assert.NotEmpty(t, logID)

	// Wait for async completion.
	time.Sleep(300 * time.Millisecond)

	// Verify the install was called with the OCI chart ref (repo+path combined).
	helmMock.mu.Lock()
	require.Len(t, helmMock.installCalls, 1)
	call := helmMock.installCalls[0]
	helmMock.mu.Unlock()

	assert.Equal(t, "oci://registry.example.com/charts/myapp", call.ChartPath)
	assert.Empty(t, call.RepoURL, "OCI charts should not use --repo")
	assert.Equal(t, "1.2.3", call.Version)
	assert.Equal(t, "myapp", call.ReleaseName)
	assert.NotEmpty(t, call.ValuesFile, "values file should be set")
	assert.Equal(t, "stack-oci-test", call.Namespace)
}

func TestManager_ExecuteDeploy_HTTPRepoRef(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:                "inst-http-repo",
		StackDefinitionID: "def-1",
		Name:              "http-repo-test",
		Namespace:         "stack-http-repo-test",
		OwnerID:           "user-1",
		Branch:            "main",
		Status:            models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	helmMock := &mockHelmExecutor{}

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: helmMock},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           hub,
		MaxConcurrent: 2,
	})

	req := DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "test-def"},
		Charts: []ChartDeployInfo{
			{
				ChartConfig: models.ChartConfig{
					ChartName:     "nginx-ingress",
					ChartPath:     "nginx-ingress",
					RepositoryURL: "https://charts.example.com/stable",
					ChartVersion:  "4.0.0",
					DeployOrder:   1,
				},
			},
		},
	}

	logID, err := mgr.Deploy(context.Background(), req)
	assert.NoError(t, err)
	assert.NotEmpty(t, logID)

	// Wait for async completion.
	time.Sleep(300 * time.Millisecond)

	helmMock.mu.Lock()
	require.Len(t, helmMock.installCalls, 1)
	call := helmMock.installCalls[0]
	helmMock.mu.Unlock()

	assert.Equal(t, "nginx-ingress", call.ChartPath)
	assert.Equal(t, "https://charts.example.com/stable", call.RepoURL, "HTTP repos should pass --repo separately")
	assert.Equal(t, "4.0.0", call.Version)
}

func TestManager_ExecuteDeploy_ValuesFileWritten(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:                "inst-values-file",
		StackDefinitionID: "def-1",
		Name:              "values-file-test",
		Namespace:         "stack-values-file-test",
		OwnerID:           "user-1",
		Branch:            "main",
		Status:            models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	helmMock := &mockHelmExecutor{}

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: helmMock},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           hub,
		MaxConcurrent: 2,
	})

	req := DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "test-def"},
		Charts: []ChartDeployInfo{
			{
				ChartConfig: models.ChartConfig{
					ChartName:   "chart-with-values",
					DeployOrder: 1,
				},
				ValuesYAML: []byte("replicas: 3\nimage: nginx:latest\n"),
			},
			{
				ChartConfig: models.ChartConfig{
					ChartName:   "chart-no-values",
					DeployOrder: 2,
				},
				ValuesYAML: nil,
			},
		},
	}

	logID, err := mgr.Deploy(context.Background(), req)
	assert.NoError(t, err)
	assert.NotEmpty(t, logID)

	// Wait for async completion.
	time.Sleep(300 * time.Millisecond)

	helmMock.mu.Lock()
	require.Len(t, helmMock.installCalls, 2)
	firstCall := helmMock.installCalls[0]
	secondCall := helmMock.installCalls[1]
	helmMock.mu.Unlock()

	assert.NotEmpty(t, firstCall.ValuesFile, "chart with values should have a values file")
	assert.Contains(t, firstCall.ValuesFile, "chart-with-values-values.yaml")
	assert.Empty(t, secondCall.ValuesFile, "chart without values should not have a values file")
}

// ---- Deploy/Stop/Clean synchronous error paths (resolve, helm executor) ----

func TestManager_Deploy_ResolveClusterError(t *testing.T) {
	t.Parallel()

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{resolveErr: fmt.Errorf("cluster not found")},
		InstanceRepo:  newMockInstanceRepo(),
		DeployLogRepo: newMockDeployLogRepo(),
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
	})

	_, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance:   &models.StackInstance{ID: "inst-resolve-err"},
		Definition: &models.StackDefinition{ID: "def-1"},
		Charts:     []ChartDeployInfo{},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "resolving cluster")
}

func TestManager_Deploy_HelmExecutorError(t *testing.T) {
	t.Parallel()

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helmErr: fmt.Errorf("no executor")},
		InstanceRepo:  newMockInstanceRepo(),
		DeployLogRepo: newMockDeployLogRepo(),
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
	})

	_, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance:   &models.StackInstance{ID: "inst-helm-err"},
		Definition: &models.StackDefinition{ID: "def-1"},
		Charts:     []ChartDeployInfo{},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "getting cluster clients")
}

func TestManager_Deploy_NilHelmExecutor(t *testing.T) {
	t.Parallel()

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: nil},
		InstanceRepo:  newMockInstanceRepo(),
		DeployLogRepo: newMockDeployLogRepo(),
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
	})

	_, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance:   &models.StackInstance{ID: "inst-nil-helm"},
		Definition: &models.StackDefinition{ID: "def-1"},
		Charts:     []ChartDeployInfo{},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "helm executor is nil")
}

func TestSanitizeDeployError_DeleteNamespace(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf("deleting namespace %q: context deadline exceeded", "stack-test")
	got := sanitizeDeployError(err)
	assert.Equal(t, `deleting namespace "stack-test": operation failed`, got)
}

func TestManager_Clean_CancelledContext(t *testing.T) {
	t.Parallel()

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: &mockHelmExecutor{}},
		InstanceRepo:  newMockInstanceRepo(),
		DeployLogRepo: newMockDeployLogRepo(),
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := mgr.Clean(ctx, &models.StackInstance{ID: "inst-clean-cancel"}, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "request cancelled")
}

func TestManager_Clean_NilRegistry(t *testing.T) {
	t.Parallel()

	mgr := NewManager(ManagerConfig{
		Registry:      nil,
		InstanceRepo:  newMockInstanceRepo(),
		DeployLogRepo: newMockDeployLogRepo(),
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
	})

	_, err := mgr.Clean(context.Background(), &models.StackInstance{ID: "inst-clean-nil-reg"}, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cluster registry is not configured")
}

func TestManager_Clean_K8sClientError(t *testing.T) {
	t.Parallel()

	mgr := NewManager(ManagerConfig{
		Registry: &mockClusterResolver{
			helm:   &mockHelmExecutor{},
			k8sErr: fmt.Errorf("no k8s client"),
		},
		InstanceRepo:  newMockInstanceRepo(),
		DeployLogRepo: newMockDeployLogRepo(),
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
	})

	_, err := mgr.Clean(context.Background(), &models.StackInstance{ID: "inst-clean-k8s-err"}, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "getting cluster k8s client")
}

func TestManager_StopWithCharts_CancelledContext(t *testing.T) {
	t.Parallel()

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: &mockHelmExecutor{}},
		InstanceRepo:  newMockInstanceRepo(),
		DeployLogRepo: newMockDeployLogRepo(),
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := mgr.StopWithCharts(ctx, &models.StackInstance{ID: "inst-stop-cancel"}, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "request cancelled")
}

func TestManager_StopWithCharts_NilHelmExecutor(t *testing.T) {
	t.Parallel()

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: nil},
		InstanceRepo:  newMockInstanceRepo(),
		DeployLogRepo: newMockDeployLogRepo(),
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
	})

	_, err := mgr.StopWithCharts(context.Background(), &models.StackInstance{ID: "inst-stop-nil-helm"}, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "helm executor is nil")
}

func TestManager_Clean_NilHelmExecutor(t *testing.T) {
	t.Parallel()

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: nil},
		InstanceRepo:  newMockInstanceRepo(),
		DeployLogRepo: newMockDeployLogRepo(),
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
	})

	_, err := mgr.Clean(context.Background(), &models.StackInstance{ID: "inst-clean-nil-helm"}, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "helm executor is nil")
}

func TestManager_FinalizeDeploy_LastDeployedValues(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	inst := &models.StackInstance{
		ID:        "inst-ldv",
		Name:      "ldv-test",
		Namespace: "stack-ldv",
		Status:    models.StackStatusDeploying,
	}
	require.NoError(t, instanceRepo.Create(inst))

	deployLog := &models.DeploymentLog{
		ID:              "log-ldv",
		StackInstanceID: inst.ID,
		Action:          models.DeployActionDeploy,
		Status:          models.DeployLogRunning,
		StartedAt:       time.Now().UTC(),
	}
	require.NoError(t, logRepo.Create(context.Background(), deployLog))

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           nil,
		MaxConcurrent: 2,
	})

	lastValues := `{"mychart":"key: value\n"}`
	mgr.finalizeDeploy(inst.ID, deployLog, "deploy output", nil, lastValues)

	updated, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, lastValues, updated.LastDeployedValues)
	assert.Equal(t, models.StackStatusRunning, updated.Status)
}

func TestManager_FinalizeDeploy_LastDeployedValuesNotSetOnError(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	inst := &models.StackInstance{
		ID:        "inst-ldv-err",
		Name:      "ldv-err-test",
		Namespace: "stack-ldv-err",
		Status:    models.StackStatusDeploying,
	}
	require.NoError(t, instanceRepo.Create(inst))

	deployLog := &models.DeploymentLog{
		ID:              "log-ldv-err",
		StackInstanceID: inst.ID,
		Action:          models.DeployActionDeploy,
		Status:          models.DeployLogRunning,
		StartedAt:       time.Now().UTC(),
	}
	require.NoError(t, logRepo.Create(context.Background(), deployLog))

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           nil,
		MaxConcurrent: 2,
	})

	lastValues := `{"mychart":"key: value\n"}`
	mgr.finalizeDeploy(inst.ID, deployLog, "deploy output", fmt.Errorf("helm install failed"), lastValues)

	updated, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Empty(t, updated.LastDeployedValues)
	assert.Equal(t, models.StackStatusError, updated.Status)
}

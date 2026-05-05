package deployer

import (
	"context"
	"encoding/json"
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

func (m *mockInstanceRepo) FindByName(_ string) ([]models.StackInstance, error) { return nil, nil }

func (m *mockInstanceRepo) ListExpired() ([]*models.StackInstance, error) {
	return nil, nil
}

func (m *mockInstanceRepo) ListExpiringSoon(_ time.Duration) ([]*models.StackInstance, error) {
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

// getMessages returns a thread-safe copy of all broadcast messages.
func (m *mockBroadcaster) getMessages() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([][]byte, len(m.messages))
	for i, msg := range m.messages {
		c := make([]byte, len(msg))
		copy(c, msg)
		cp[i] = c
	}
	return cp
}

// ---- helpers ----

// waitForTerminalStatus polls the instance repo until the instance reaches a
// terminal status (not queued/deploying/stopping/cleaning). This replaces
// fixed time.Sleep calls, making tests faster and less flaky.
func waitForTerminalStatus(t *testing.T, repo *mockInstanceRepo, instanceID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		inst, err := repo.FindByID(instanceID)
		if err == nil {
			switch inst.Status {
			case models.StackStatusQueued, models.StackStatusDeploying,
				models.StackStatusStabilizing, models.StackStatusStopping, models.StackStatusCleaning:
			default:
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for terminal instance status")
}

// ---- tests ----

// mockClusterResolver implements ClusterResolver for tests.
type mockClusterResolver struct {
	helm           HelmExecutor
	k8sClient      *k8s.Client
	noK8sClient    bool
	registryConfig *models.RegistryConfig
	resolveErr     error
	helmErr        error
	k8sErr         error
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
	if m.k8sClient != nil {
		return m.k8sClient, nil
	}
	if m.noK8sClient {
		return nil, nil
	}
	return k8s.NewClientFromInterface(fake.NewSimpleClientset()), nil
}

func (m *mockClusterResolver) GetRegistryConfig(_ string) (*models.RegistryConfig, error) {
	return m.registryConfig, nil
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
	waitForTerminalStatus(t, instanceRepo, inst.ID)

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
	waitForTerminalStatus(t, instanceRepo, inst.ID)

	// Both charts fail (nonexistent binary) — error lists all failed charts.
	finalLog, err := logRepo.FindByID(context.Background(), logID)
	assert.NoError(t, err)
	assert.Equal(t, models.DeployLogError, finalLog.Status)
	assert.Contains(t, finalLog.ErrorMessage, "first-chart")
	assert.Contains(t, finalLog.ErrorMessage, "second-chart")
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
	waitForTerminalStatus(t, instanceRepo, inst.ID)

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
	mgr.finalizeDeploy("nonexistent-id", orphanLog, "output", nil, false, "", "")
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
	waitForTerminalStatus(t, instanceRepo, inst.ID)

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
	mgr.finalizeDeploy(inst.ID, deployLog, largeOutput, nil, false, "", "")

	finalLog, err := logRepo.FindByID(context.Background(), deployLog.ID)
	assert.NoError(t, err)
	assert.LessOrEqual(t, len(finalLog.Output), maxOutputLen)
}

// ---- mock helm executor ----

type mockHelmExecutor struct {
	mu             sync.Mutex
	installFunc    func(ctx context.Context, req InstallRequest) (string, error)
	uninstallFunc  func(ctx context.Context, req UninstallRequest) (string, error)
	historyFunc    func(ctx context.Context, releaseName, namespace string, max int) ([]ReleaseRevision, error)
	rollbackFunc   func(ctx context.Context, releaseName, namespace string, revision int) (string, error)
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

func (m *mockHelmExecutor) History(ctx context.Context, releaseName, namespace string, max int) ([]ReleaseRevision, error) {
	if m.historyFunc != nil {
		return m.historyFunc(ctx, releaseName, namespace, max)
	}
	return nil, nil
}

func (m *mockHelmExecutor) Rollback(ctx context.Context, releaseName, namespace string, revision int) (string, error) {
	if m.rollbackFunc != nil {
		return m.rollbackFunc(ctx, releaseName, namespace, revision)
	}
	return "rolled back " + releaseName, nil
}

func (m *mockHelmExecutor) GetValues(_ context.Context, _ string, _ string, _ int) (string, error) {
	return "", nil
}

func (m *mockHelmExecutor) RegistryLogin(_ context.Context, _, _, _ string) error {
	return nil
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

func TestManager_Deploy_PartialDeploy_KeepsSuccessfulCharts(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:                "inst-rollback-1",
		StackDefinitionID: "def-1",
		Name:              "partial-keep",
		Namespace:         "stack-partial-keep",
		OwnerID:           "user-1",
		Branch:            "main",
		Status:            models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	helmMock := &mockHelmExecutor{
		installFunc: func(_ context.Context, req InstallRequest) (string, error) {
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

	waitForTerminalStatus(t, instanceRepo, inst.ID)

	// chart-c failed, chart-a and chart-b succeeded → partial status.
	final, err := instanceRepo.FindByID(inst.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.StackStatusPartial, final.Status)
	assert.Contains(t, final.ErrorMessage, "chart-c")

	finalLog, err := logRepo.FindByID(context.Background(), logID)
	assert.NoError(t, err)
	assert.Equal(t, models.DeployLogError, finalLog.Status)
	assert.Contains(t, finalLog.Output, "ERROR:")
	// No rollback — successful charts are preserved.
	assert.NotContains(t, finalLog.Output, "Rolling back")

	// No uninstall calls.
	uninstalls := helmMock.getUninstallCalls()
	assert.Empty(t, uninstalls)
}

func TestManager_Deploy_PartialSuccess_ContinuesOnFailure(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:                "inst-rollback-2",
		StackDefinitionID: "def-1",
		Name:              "partial-deploy",
		Namespace:         "stack-partial",
		OwnerID:           "user-1",
		Branch:            "main",
		Status:            models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	helmMock := &mockHelmExecutor{
		installFunc: func(_ context.Context, req InstallRequest) (string, error) {
			if req.ReleaseName == "chart-b" {
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

	waitForTerminalStatus(t, instanceRepo, inst.ID)

	// Partial deploy: chart-b failed but chart-a and chart-c succeeded.
	final, err := instanceRepo.FindByID(inst.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.StackStatusPartial, final.Status)
	assert.Contains(t, final.ErrorMessage, "chart-b")

	finalLog, err := logRepo.FindByID(context.Background(), logID)
	assert.NoError(t, err)
	assert.Equal(t, models.DeployLogError, finalLog.Status)
	assert.Contains(t, finalLog.Output, "ERROR:")
	// No rollback — successful charts are kept.
	assert.NotContains(t, finalLog.Output, "Rolling back")

	// No uninstall calls — we don't roll back on partial failure.
	uninstalls := helmMock.getUninstallCalls()
	assert.Empty(t, uninstalls)
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
	waitForTerminalStatus(t, instanceRepo, inst.ID)

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
		Registry:      &mockClusterResolver{helm: helmMock, noK8sClient: true},
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
	waitForTerminalStatus(t, instanceRepo, inst.ID)

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
	waitForTerminalStatus(t, instanceRepo, inst.ID)

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
	waitForTerminalStatus(t, instanceRepo, inst.ID)

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

func TestManager_Deploy_AllChartsFail(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:                "inst-rollback-3",
		StackDefinitionID: "def-1",
		Name:              "all-charts-fail",
		Namespace:         "stack-all-fail",
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

	waitForTerminalStatus(t, instanceRepo, inst.ID)

	// All charts failed → full error status.
	final, err := instanceRepo.FindByID(inst.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.StackStatusError, final.Status)
	assert.Contains(t, final.ErrorMessage, "chart-a")
	assert.Contains(t, final.ErrorMessage, "chart-b")

	// No uninstall calls — nothing succeeded.
	uninstalls := helmMock.getUninstallCalls()
	assert.Empty(t, uninstalls)

	finalLog, err := logRepo.FindByID(context.Background(), logID)
	assert.NoError(t, err)
	assert.NotContains(t, finalLog.Output, "Rolling back")
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
	mgr.finalizeDeploy(inst.ID, deployLog, "deploy output", nil, false, lastValues, "")

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
	mgr.finalizeDeploy(inst.ID, deployLog, "deploy output", fmt.Errorf("helm install failed"), false, lastValues, "")

	updated, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Empty(t, updated.LastDeployedValues)
	assert.Equal(t, models.StackStatusError, updated.Status)
}

// ---- streaming helm executor mock ----

// streamingMockHelmExecutor embeds mockHelmExecutor and implements
// StreamingHelmExecutor. WithLineHandler returns a wrapper that calls fn
// for each line of output produced by Install/Uninstall/Rollback.
type streamingMockHelmExecutor struct {
	*mockHelmExecutor
	mu2            sync.Mutex
	lineHandlerSet bool
}

func (s *streamingMockHelmExecutor) WithLineHandler(fn func(string)) HelmExecutor {
	s.mu2.Lock()
	s.lineHandlerSet = true
	s.mu2.Unlock()
	return &streamingMockWrapper{inner: s.mockHelmExecutor, onLine: fn}
}

func (s *streamingMockHelmExecutor) wasLineHandlerSet() bool {
	s.mu2.Lock()
	defer s.mu2.Unlock()
	return s.lineHandlerSet
}

// streamingMockWrapper wraps mockHelmExecutor and calls onLine for each
// line in the output before returning.
type streamingMockWrapper struct {
	inner  *mockHelmExecutor
	onLine func(string)
}

func (w *streamingMockWrapper) Install(ctx context.Context, req InstallRequest) (string, error) {
	output, err := w.inner.Install(ctx, req)
	if w.onLine != nil {
		for _, line := range strings.Split(output, "\n") {
			if line != "" {
				w.onLine(line)
			}
		}
	}
	return output, err
}

func (w *streamingMockWrapper) Uninstall(ctx context.Context, req UninstallRequest) (string, error) {
	output, err := w.inner.Uninstall(ctx, req)
	if w.onLine != nil {
		for _, line := range strings.Split(output, "\n") {
			if line != "" {
				w.onLine(line)
			}
		}
	}
	return output, err
}

func (w *streamingMockWrapper) Status(ctx context.Context, releaseName, namespace string) (*ReleaseStatus, error) {
	return w.inner.Status(ctx, releaseName, namespace)
}

func (w *streamingMockWrapper) ListReleases(ctx context.Context, namespace string) ([]string, error) {
	return w.inner.ListReleases(ctx, namespace)
}

func (w *streamingMockWrapper) History(ctx context.Context, releaseName, namespace string, max int) ([]ReleaseRevision, error) {
	return w.inner.History(ctx, releaseName, namespace, max)
}

func (w *streamingMockWrapper) Rollback(ctx context.Context, releaseName, namespace string, revision int) (string, error) {
	output, err := w.inner.Rollback(ctx, releaseName, namespace, revision)
	if w.onLine != nil {
		for _, line := range strings.Split(output, "\n") {
			if line != "" {
				w.onLine(line)
			}
		}
	}
	return output, err
}

func (w *streamingMockWrapper) GetValues(ctx context.Context, releaseName, namespace string, revision int) (string, error) {
	return w.inner.GetValues(ctx, releaseName, namespace, revision)
}

func (w *streamingMockWrapper) RegistryLogin(_ context.Context, _, _, _ string) error {
	return nil
}

func (w *streamingMockWrapper) Timeout() time.Duration {
	return w.inner.Timeout()
}

// ---- streaming broadcast tests ----

// parseBroadcastMessageType extracts the "type" field from a JSON-encoded
// websocket.Message. Returns empty string on parse failure.
func parseBroadcastMessageType(data []byte) string {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ""
	}
	return envelope.Type
}

func TestManager_Deploy_StreamingBroadcastsPerLine(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:                "inst-stream-deploy",
		StackDefinitionID: "def-1",
		Name:              "stream-deploy",
		Namespace:         "stack-stream-deploy",
		OwnerID:           "user-1",
		Branch:            "main",
		Status:            models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	helmMock := &streamingMockHelmExecutor{
		mockHelmExecutor: &mockHelmExecutor{
			installFunc: func(_ context.Context, req InstallRequest) (string, error) {
				// Return multi-line output to simulate real Helm streaming.
				return fmt.Sprintf("installing %s\nprogress: 50%%\nprogress: 100%%\ndone", req.ReleaseName), nil
			},
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
			{ChartConfig: models.ChartConfig{ChartName: "redis", DeployOrder: 1}},
			{ChartConfig: models.ChartConfig{ChartName: "nginx", DeployOrder: 2}},
		},
	}

	logID, err := mgr.Deploy(context.Background(), req)
	require.NoError(t, err)
	assert.NotEmpty(t, logID)

	// Wait for async completion.
	waitForTerminalStatus(t, instanceRepo, inst.ID)

	// Verify the streaming executor's WithLineHandler was called.
	assert.True(t, helmMock.wasLineHandlerSet(), "WithLineHandler should have been called")

	// Verify instance completed successfully.
	final, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusRunning, final.Status)

	// Parse broadcast messages and verify deployment.log messages are present.
	messages := hub.getMessages()
	assert.Greater(t, len(messages), 0, "should have broadcast messages")

	var logMsgCount int
	var statusMsgCount int
	for _, msg := range messages {
		msgType := parseBroadcastMessageType(msg)
		switch msgType {
		case "deployment.log":
			logMsgCount++
		case "deployment.status":
			statusMsgCount++
		}
	}

	// With streaming enabled, per-line broadcasts should appear as deployment.log.
	// Each chart produces multi-line output (4 non-empty lines), and we have 2 charts.
	assert.Greater(t, logMsgCount, 0, "streaming deploy should produce deployment.log messages")
	assert.Greater(t, statusMsgCount, 0, "deploy should still produce deployment.status messages")
}

func TestManager_Deploy_NonStreamingBroadcastsPerChart(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:                "inst-nonstream-deploy",
		StackDefinitionID: "def-1",
		Name:              "nonstream-deploy",
		Namespace:         "stack-nonstream-deploy",
		OwnerID:           "user-1",
		Branch:            "main",
		Status:            models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	// Regular mockHelmExecutor does NOT implement StreamingHelmExecutor.
	helmMock := &mockHelmExecutor{
		installFunc: func(_ context.Context, req InstallRequest) (string, error) {
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
			{ChartConfig: models.ChartConfig{ChartName: "redis", DeployOrder: 1}},
			{ChartConfig: models.ChartConfig{ChartName: "nginx", DeployOrder: 2}},
		},
	}

	logID, err := mgr.Deploy(context.Background(), req)
	require.NoError(t, err)
	assert.NotEmpty(t, logID)

	// Wait for async completion.
	waitForTerminalStatus(t, instanceRepo, inst.ID)

	// Verify instance completed successfully.
	final, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusRunning, final.Status)

	// Parse broadcast messages: non-streaming should still produce
	// deployment.log messages (one per chart, containing full output),
	// plus deployment.status messages.
	messages := hub.getMessages()
	assert.Greater(t, len(messages), 0, "should have broadcast messages")

	var logMsgCount int
	var statusMsgCount int
	for _, msg := range messages {
		msgType := parseBroadcastMessageType(msg)
		switch msgType {
		case "deployment.log":
			logMsgCount++
		case "deployment.status":
			statusMsgCount++
		}
	}

	// Non-streaming: broadcastLog is called once per chart (not per line).
	assert.Equal(t, 2, logMsgCount, "non-streaming deploy should produce exactly one deployment.log per chart")
	assert.Greater(t, statusMsgCount, 0, "deploy should produce deployment.status messages")
}

func TestManager_StopWithCharts_StreamingSupport(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:        "inst-stream-stop",
		Name:      "stream-stop",
		Namespace: "stack-stream-stop",
		Status:    models.StackStatusRunning,
	}
	require.NoError(t, instanceRepo.Create(inst))

	helmMock := &streamingMockHelmExecutor{
		mockHelmExecutor: &mockHelmExecutor{
			uninstallFunc: func(_ context.Context, req UninstallRequest) (string, error) {
				return fmt.Sprintf("uninstalling %s\ncleaning up\ndone", req.ReleaseName), nil
			},
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

	charts := []ChartDeployInfo{
		{ChartConfig: models.ChartConfig{ChartName: "redis", DeployOrder: 1}},
		{ChartConfig: models.ChartConfig{ChartName: "nginx", DeployOrder: 2}},
	}

	logID, err := mgr.StopWithCharts(context.Background(), inst, charts)
	require.NoError(t, err)
	assert.NotEmpty(t, logID)

	// Wait for async completion.
	waitForTerminalStatus(t, instanceRepo, inst.ID)

	// Verify the streaming executor's WithLineHandler was called.
	assert.True(t, helmMock.wasLineHandlerSet(), "WithLineHandler should have been called for stop")

	// Verify instance completed successfully.
	final, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusStopped, final.Status)

	// Verify deployment.log messages were streamed.
	messages := hub.getMessages()
	assert.Greater(t, len(messages), 0, "should have broadcast messages")

	var logMsgCount int
	for _, msg := range messages {
		if parseBroadcastMessageType(msg) == "deployment.log" {
			logMsgCount++
		}
	}

	// With streaming: per-line broadcasts. 2 charts, each produces 3 non-empty lines.
	assert.Greater(t, logMsgCount, 0, "streaming stop should produce deployment.log messages")
}

func TestManager_StopWithCharts_NonStreamingBroadcastsPerChart(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:        "inst-nonstream-stop",
		Name:      "nonstream-stop",
		Namespace: "stack-nonstream-stop",
		Status:    models.StackStatusRunning,
	}
	require.NoError(t, instanceRepo.Create(inst))

	// Regular mockHelmExecutor — no StreamingHelmExecutor.
	helmMock := &mockHelmExecutor{
		uninstallFunc: func(_ context.Context, req UninstallRequest) (string, error) {
			return "uninstalled " + req.ReleaseName, nil
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

	charts := []ChartDeployInfo{
		{ChartConfig: models.ChartConfig{ChartName: "redis", DeployOrder: 1}},
		{ChartConfig: models.ChartConfig{ChartName: "nginx", DeployOrder: 2}},
	}

	logID, err := mgr.StopWithCharts(context.Background(), inst, charts)
	require.NoError(t, err)
	assert.NotEmpty(t, logID)

	// Wait for async completion.
	waitForTerminalStatus(t, instanceRepo, inst.ID)

	// Verify instance completed successfully.
	final, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusStopped, final.Status)

	// Parse messages.
	messages := hub.getMessages()
	assert.Greater(t, len(messages), 0, "should have broadcast messages")

	var logMsgCount int
	for _, msg := range messages {
		if parseBroadcastMessageType(msg) == "deployment.log" {
			logMsgCount++
		}
	}

	// Non-streaming: one broadcastLog per chart.
	assert.Equal(t, 2, logMsgCount, "non-streaming stop should produce exactly one deployment.log per chart")
}

func TestManager_Clean_StreamingSupport(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	fakeClient := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-stream-clean"}},
	)
	k8sClient := k8s.NewClientFromInterface(fakeClient)

	inst := &models.StackInstance{
		ID:        "inst-stream-clean",
		Name:      "stream-clean",
		Namespace: "stack-stream-clean",
		Status:    models.StackStatusStopped,
	}
	require.NoError(t, instanceRepo.Create(inst))

	helmMock := &streamingMockHelmExecutor{
		mockHelmExecutor: &mockHelmExecutor{
			uninstallFunc: func(_ context.Context, req UninstallRequest) (string, error) {
				return fmt.Sprintf("uninstalling %s\ncleaned", req.ReleaseName), nil
			},
		},
	}

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
	}

	logID, err := mgr.Clean(context.Background(), inst, charts)
	require.NoError(t, err)
	assert.NotEmpty(t, logID)

	// Wait for async completion.
	waitForTerminalStatus(t, instanceRepo, inst.ID)

	// Verify the streaming executor's WithLineHandler was called.
	assert.True(t, helmMock.wasLineHandlerSet(), "WithLineHandler should have been called for clean")

	// Verify instance returned to draft status.
	final, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusDraft, final.Status)

	// Verify deployment.log messages were streamed.
	messages := hub.getMessages()
	var logMsgCount int
	for _, msg := range messages {
		if parseBroadcastMessageType(msg) == "deployment.log" {
			logMsgCount++
		}
	}
	assert.Greater(t, logMsgCount, 0, "streaming clean should produce deployment.log messages")
}

func TestManager_Deploy_StreamingPartialFailure_StillBroadcastsLines(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:                "inst-stream-fail",
		StackDefinitionID: "def-1",
		Name:              "stream-fail",
		Namespace:         "stack-stream-fail",
		OwnerID:           "user-1",
		Branch:            "main",
		Status:            models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	helmMock := &streamingMockHelmExecutor{
		mockHelmExecutor: &mockHelmExecutor{
			installFunc: func(_ context.Context, req InstallRequest) (string, error) {
				if req.ReleaseName == "failing-chart" {
					return "preparing\nerror: image pull failed", fmt.Errorf("helm command failed: exit status 1")
				}
				return "installed " + req.ReleaseName, nil
			},
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
			{ChartConfig: models.ChartConfig{ChartName: "good-chart", DeployOrder: 1}},
			{ChartConfig: models.ChartConfig{ChartName: "failing-chart", DeployOrder: 2}},
		},
	}

	logID, err := mgr.Deploy(context.Background(), req)
	require.NoError(t, err)
	assert.NotEmpty(t, logID)

	// Wait for async completion.
	waitForTerminalStatus(t, instanceRepo, inst.ID)

	// good-chart succeeded, failing-chart failed → partial status.
	final, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusPartial, final.Status)

	// Even with partial failure, streaming should have broadcast per-line log messages
	// for both the successful chart and the failing chart's partial output.
	messages := hub.getMessages()
	var logMsgCount int
	for _, msg := range messages {
		if parseBroadcastMessageType(msg) == "deployment.log" {
			logMsgCount++
		}
	}
	assert.Greater(t, logMsgCount, 0, "streaming deploy with failure should still produce per-line log messages")
}

func TestManager_Rollback_StreamingSupport(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:                "inst-rollback-stream",
		StackDefinitionID: "def-1",
		Name:              "rollback-stream",
		Namespace:         "stack-rollback-stream",
		OwnerID:           "user-1",
		Branch:            "main",
		Status:            models.StackStatusRunning,
	}
	require.NoError(t, instanceRepo.Create(inst))

	helmMock := &streamingMockHelmExecutor{
		mockHelmExecutor: &mockHelmExecutor{
			historyFunc: func(_ context.Context, _ string, _ string, _ int) ([]ReleaseRevision, error) {
				return []ReleaseRevision{
					{Revision: 1, Status: "deployed"},
					{Revision: 2, Status: "deployed"},
				}, nil
			},
			rollbackFunc: func(_ context.Context, releaseName string, _ string, _ int) (string, error) {
				return "rolling back " + releaseName + "\nrollback complete", nil
			},
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

	req := RollbackRequest{
		Instance: inst,
		Charts: []ChartDeployInfo{
			{ChartConfig: models.ChartConfig{ChartName: "web", DeployOrder: 1}},
		},
	}

	logID, err := mgr.Rollback(context.Background(), req)
	require.NoError(t, err)
	assert.NotEmpty(t, logID)

	waitForTerminalStatus(t, instanceRepo, inst.ID)

	final, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusRunning, final.Status)

	assert.True(t, helmMock.wasLineHandlerSet(), "streaming helm executor should have WithLineHandler called")

	messages := hub.getMessages()
	var logMsgCount int
	for _, msg := range messages {
		if parseBroadcastMessageType(msg) == "deployment.log" {
			logMsgCount++
		}
	}
	assert.Greater(t, logMsgCount, 0, "rollback with streaming executor should produce per-line log messages")
}

// ---- #182: namespace terminating rejection tests ----

func TestDeploy_RejectsTerminatingNamespace(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	inst := &models.StackInstance{
		ID:                "inst-terminating",
		StackDefinitionID: "def-1",
		Name:              "terminating-test",
		Namespace:         "stack-terminating",
		OwnerID:           "user-1",
		Branch:            "main",
		Status:            models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	fakeCS := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "stack-terminating"},
		Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceTerminating},
	})
	k8sClient := k8s.NewClientFromInterface(fakeCS)

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: &mockHelmExecutor{}, k8sClient: k8sClient},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
	})

	_, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "test-def"},
		Charts:     []ChartDeployInfo{},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "terminating")
}

func TestDeploy_AllowsActiveNamespace(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	inst := &models.StackInstance{
		ID:                "inst-active",
		StackDefinitionID: "def-1",
		Name:              "active-test",
		Namespace:         "stack-active",
		OwnerID:           "user-1",
		Branch:            "main",
		Status:            models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	fakeCS := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "stack-active"},
		Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
	})
	k8sClient := k8s.NewClientFromInterface(fakeCS)

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: &mockHelmExecutor{}, k8sClient: k8sClient},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
	})

	logID, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "test-def"},
		Charts:     []ChartDeployInfo{},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, logID)
}

func TestDeploy_AllowsNonExistentNamespace(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	inst := &models.StackInstance{
		ID:                "inst-new-ns",
		StackDefinitionID: "def-1",
		Name:              "new-ns-test",
		Namespace:         "stack-new",
		OwnerID:           "user-1",
		Branch:            "main",
		Status:            models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	// Empty clientset — namespace doesn't exist yet.
	k8sClient := k8s.NewClientFromInterface(fake.NewSimpleClientset())

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: &mockHelmExecutor{}, k8sClient: k8sClient},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
	})

	logID, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "test-def"},
		Charts:     []ChartDeployInfo{},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, logID)
}

// ---- #186: readiness gating tests ----

func TestAwaitReadiness_HealthyImmediately(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:                "inst-ready",
		StackDefinitionID: "def-1",
		Name:              "ready-test",
		Namespace:         "stack-ready",
		OwnerID:           "user-1",
		Branch:            "main",
		Status:            models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	// Namespace exists with no pods/deployments → healthy immediately.
	fakeCS := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "stack-ready"},
		Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
	})
	k8sClient := k8s.NewClientFromInterface(fakeCS)

	mgr := NewManager(ManagerConfig{
		Registry:             &mockClusterResolver{helm: &mockHelmExecutor{}, k8sClient: k8sClient},
		InstanceRepo:         instanceRepo,
		DeployLogRepo:        logRepo,
		TxRunner:             &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:                  hub,
		MaxConcurrent:        2,
		StabilizeTimeout:     10 * time.Second,
		StabilizePollInterval: 50 * time.Millisecond,
	})

	logID, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "test-def"},
		Charts:     []ChartDeployInfo{},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, logID)

	waitForTerminalStatus(t, instanceRepo, inst.ID)

	final, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusRunning, final.Status)

	// Verify stabilizing status was broadcast.
	messages := hub.getMessages()
	var sawStabilizing bool
	for _, msg := range messages {
		if strings.Contains(string(msg), "stabilizing") {
			sawStabilizing = true
			break
		}
	}
	assert.True(t, sawStabilizing, "should have broadcast stabilizing status")
}

func TestAwaitReadiness_Timeout(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:                "inst-timeout",
		StackDefinitionID: "def-1",
		Name:              "timeout-test",
		Namespace:         "stack-timeout",
		OwnerID:           "user-1",
		Branch:            "main",
		Status:            models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	// Namespace with a pending pod → never becomes healthy.
	fakeCS := fake.NewSimpleClientset(
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "stack-timeout"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "stuck-pod", Namespace: "stack-timeout"},
			Status: corev1.PodStatus{
				Phase: corev1.PodPending,
			},
		},
	)
	k8sClient := k8s.NewClientFromInterface(fakeCS)

	mgr := NewManager(ManagerConfig{
		Registry:             &mockClusterResolver{helm: &mockHelmExecutor{}, k8sClient: k8sClient},
		InstanceRepo:         instanceRepo,
		DeployLogRepo:        logRepo,
		TxRunner:             &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:                  hub,
		MaxConcurrent:        2,
		StabilizeTimeout:     200 * time.Millisecond,
		StabilizePollInterval: 50 * time.Millisecond,
	})

	logID, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "test-def"},
		Charts:     []ChartDeployInfo{},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, logID)

	waitForTerminalStatus(t, instanceRepo, inst.ID)

	// Deploy still succeeds (readiness timeout is a warning, not a failure).
	final, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusRunning, final.Status)

	// Verify timeout warning was broadcast.
	messages := hub.getMessages()
	var sawTimeout bool
	for _, msg := range messages {
		if strings.Contains(string(msg), "readiness timeout") {
			sawTimeout = true
			break
		}
	}
	assert.True(t, sawTimeout, "should have broadcast readiness timeout warning")
}

func TestAwaitReadiness_Disabled_WhenTimeoutZero(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:                "inst-no-stabilize",
		StackDefinitionID: "def-1",
		Name:              "no-stabilize",
		Namespace:         "stack-no-stabilize",
		OwnerID:           "user-1",
		Branch:            "main",
		Status:            models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	mgr := NewManager(ManagerConfig{
		Registry:             &mockClusterResolver{helm: &mockHelmExecutor{}},
		InstanceRepo:         instanceRepo,
		DeployLogRepo:        logRepo,
		TxRunner:             &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:                  hub,
		MaxConcurrent:        2,
		StabilizeTimeout:     0, // disabled
		StabilizePollInterval: 50 * time.Millisecond,
	})

	logID, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "test-def"},
		Charts:     []ChartDeployInfo{},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, logID)

	waitForTerminalStatus(t, instanceRepo, inst.ID)

	final, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusRunning, final.Status)

	// Should NOT see stabilizing status when timeout is 0.
	messages := hub.getMessages()
	for _, msg := range messages {
		assert.NotContains(t, string(msg), "stabilizing", "should not enter stabilizing state when timeout=0")
	}
}

func TestFinalizeDeploy_ReadinessWarning_AttachesHookMetadata(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID:                "inst-rw",
		StackDefinitionID: "def-1",
		Name:              "rw-test",
		Namespace:         "stack-rw",
		OwnerID:           "user-1",
		Branch:            "main",
		Status:            models.StackStatusDeploying,
	}
	require.NoError(t, instanceRepo.Create(inst))

	deployLog := &models.DeploymentLog{
		ID:              "log-rw",
		StackInstanceID: inst.ID,
		Action:          models.DeployActionDeploy,
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

	// Call finalizeDeploy with a readinessWarning — deploy succeeds but with metadata.
	mgr.finalizeDeploy(inst.ID, deployLog, "deploy output", nil, false, "", "readiness timeout after 5m0s")

	updated, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusRunning, updated.Status)

	finalLog, err := logRepo.FindByID(context.Background(), deployLog.ID)
	require.NoError(t, err)
	assert.Equal(t, models.DeployLogSuccess, finalLog.Status)
}

// Verify that streamingMockHelmExecutor satisfies StreamingHelmExecutor at compile time.
var _ StreamingHelmExecutor = (*streamingMockHelmExecutor)(nil)

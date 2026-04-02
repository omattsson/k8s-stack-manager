package deployer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- mock definition repo ----

type mockDefinitionRepo struct {
	mu    sync.RWMutex
	items map[string]*models.StackDefinition
	err   error
}

func newMockDefinitionRepo() *mockDefinitionRepo {
	return &mockDefinitionRepo{items: make(map[string]*models.StackDefinition)}
}

func (m *mockDefinitionRepo) Create(d *models.StackDefinition) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[d.ID] = d
	return nil
}

func (m *mockDefinitionRepo) FindByID(id string) (*models.StackDefinition, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	d, ok := m.items[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return d, nil
}

func (m *mockDefinitionRepo) Update(d *models.StackDefinition) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[d.ID] = d
	return nil
}

func (m *mockDefinitionRepo) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, id)
	return nil
}

func (m *mockDefinitionRepo) List() ([]models.StackDefinition, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []models.StackDefinition
	for _, d := range m.items {
		out = append(out, *d)
	}
	return out, nil
}

func (m *mockDefinitionRepo) ListByOwner(_ string) ([]models.StackDefinition, error) {
	return m.List()
}

func (m *mockDefinitionRepo) ListByTemplate(_ string) ([]models.StackDefinition, error) {
	return m.List()
}

func (m *mockDefinitionRepo) Count() (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return int64(len(m.items)), nil
}

// ---- mock chart config repo ----

type mockChartConfigRepo struct {
	mu    sync.RWMutex
	items map[string][]models.ChartConfig
	err   error
}

func newMockChartConfigRepo() *mockChartConfigRepo {
	return &mockChartConfigRepo{items: make(map[string][]models.ChartConfig)}
}

func (m *mockChartConfigRepo) Create(c *models.ChartConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[c.StackDefinitionID] = append(m.items[c.StackDefinitionID], *c)
	return nil
}

func (m *mockChartConfigRepo) FindByID(id string) (*models.ChartConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, charts := range m.items {
		for _, c := range charts {
			if c.ID == id {
				return &c, nil
			}
		}
	}
	return nil, errors.New("not found")
}

func (m *mockChartConfigRepo) Update(c *models.ChartConfig) error {
	return nil
}

func (m *mockChartConfigRepo) Delete(id string) error {
	return nil
}

func (m *mockChartConfigRepo) ListByDefinition(defID string) ([]models.ChartConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	charts, ok := m.items[defID]
	if !ok {
		return nil, nil
	}
	return charts, nil
}

// ---- CleanupExecutor tests ----

func TestNewCleanupExecutor(t *testing.T) {
	t.Parallel()

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  newMockInstanceRepo(),
		DeployLogRepo: newMockDeployLogRepo(),
		MaxConcurrent: 1,
	})
	defRepo := newMockDefinitionRepo()
	ccRepo := newMockChartConfigRepo()
	instRepo := newMockInstanceRepo()

	exec := NewCleanupExecutor(mgr, defRepo, ccRepo, instRepo)
	assert.NotNil(t, exec)
	assert.Same(t, mgr, exec.manager)
}

func TestCleanupExecutor_DeleteInstance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "draft status allows delete",
			status:  models.StackStatusDraft,
			wantErr: false,
		},
		{
			name:    "stopped status allows delete",
			status:  models.StackStatusStopped,
			wantErr: false,
		},
		{
			name:    "error status allows delete",
			status:  models.StackStatusError,
			wantErr: false,
		},
		{
			name:    "running status blocks delete",
			status:  models.StackStatusRunning,
			wantErr: true,
			errMsg:  "cannot delete instance",
		},
		{
			name:    "deploying status blocks delete",
			status:  models.StackStatusDeploying,
			wantErr: true,
			errMsg:  "cannot delete instance",
		},
		{
			name:    "stopping status blocks delete",
			status:  models.StackStatusStopping,
			wantErr: true,
			errMsg:  "cannot delete instance",
		},
		{
			name:    "cleaning status blocks delete",
			status:  models.StackStatusCleaning,
			wantErr: true,
			errMsg:  "cannot delete instance",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			instRepo := newMockInstanceRepo()
			inst := &models.StackInstance{
				ID:     "inst-del-" + tt.status,
				Name:   "test",
				Status: tt.status,
			}
			require.NoError(t, instRepo.Create(inst))

			exec := &CleanupExecutor{instanceRepo: instRepo}

			err := exec.DeleteInstance(context.Background(), inst)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
				// Verify instance was NOT deleted.
				_, findErr := instRepo.FindByID(inst.ID)
				assert.NoError(t, findErr)
			} else {
				assert.NoError(t, err)
				// Verify instance was deleted.
				_, findErr := instRepo.FindByID(inst.ID)
				assert.Error(t, findErr)
			}
		})
	}
}

func TestCleanupExecutor_StopInstance_DefinitionNotFound(t *testing.T) {
	t.Parallel()

	defRepo := newMockDefinitionRepo()
	ccRepo := newMockChartConfigRepo()

	exec := &CleanupExecutor{
		definitionRepo:  defRepo,
		chartConfigRepo: ccRepo,
	}

	inst := &models.StackInstance{
		ID:                "inst-1",
		StackDefinitionID: "nonexistent-def",
		Status:            models.StackStatusRunning,
	}

	err := exec.StopInstance(context.Background(), inst)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "finding definition")
}

func TestCleanupExecutor_StopInstance_NoCharts(t *testing.T) {
	t.Parallel()

	defRepo := newMockDefinitionRepo()
	defRepo.items["def-1"] = &models.StackDefinition{ID: "def-1", Name: "test-def"}

	ccRepo := newMockChartConfigRepo()
	// No charts added for def-1.

	exec := &CleanupExecutor{
		definitionRepo:  defRepo,
		chartConfigRepo: ccRepo,
	}

	inst := &models.StackInstance{
		ID:                "inst-1",
		StackDefinitionID: "def-1",
		Status:            models.StackStatusRunning,
	}

	err := exec.StopInstance(context.Background(), inst)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no charts configured")
}

func TestCleanupExecutor_CleanInstance_DefinitionNotFound(t *testing.T) {
	t.Parallel()

	defRepo := newMockDefinitionRepo()
	ccRepo := newMockChartConfigRepo()

	exec := &CleanupExecutor{
		definitionRepo:  defRepo,
		chartConfigRepo: ccRepo,
	}

	inst := &models.StackInstance{
		ID:                "inst-1",
		StackDefinitionID: "nonexistent-def",
		Status:            models.StackStatusStopped,
	}

	err := exec.CleanInstance(context.Background(), inst)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "finding definition")
}

func TestCleanupExecutor_CleanInstance_ChartListError(t *testing.T) {
	t.Parallel()

	defRepo := newMockDefinitionRepo()
	defRepo.items["def-1"] = &models.StackDefinition{ID: "def-1", Name: "test-def"}

	ccRepo := newMockChartConfigRepo()
	ccRepo.err = errors.New("db connection lost")

	exec := &CleanupExecutor{
		definitionRepo:  defRepo,
		chartConfigRepo: ccRepo,
	}

	inst := &models.StackInstance{
		ID:                "inst-1",
		StackDefinitionID: "def-1",
		Status:            models.StackStatusStopped,
	}

	err := exec.CleanInstance(context.Background(), inst)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "listing charts")
}

func TestCleanupExecutor_ResolveCharts_NoCharts(t *testing.T) {
	t.Parallel()

	defRepo := newMockDefinitionRepo()
	defRepo.items["def-1"] = &models.StackDefinition{ID: "def-1", Name: "test-def"}

	ccRepo := newMockChartConfigRepo()
	// No charts for def-1.

	exec := &CleanupExecutor{
		definitionRepo:  defRepo,
		chartConfigRepo: ccRepo,
	}

	inst := &models.StackInstance{
		ID:                "inst-1",
		StackDefinitionID: "def-1",
	}

	err := exec.CleanInstance(context.Background(), inst)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no charts configured")
}

func TestCleanupExecutor_StopInstance_WithCharts(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	inst := &models.StackInstance{
		ID:                "inst-stop-1",
		StackDefinitionID: "def-1",
		Name:              "test",
		Namespace:         "stack-test",
		Status:            models.StackStatusRunning,
	}
	require.NoError(t, instanceRepo.Create(inst))

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		MaxConcurrent: 1,
	})

	defRepo := newMockDefinitionRepo()
	defRepo.items["def-1"] = &models.StackDefinition{ID: "def-1", Name: "test-def"}

	ccRepo := newMockChartConfigRepo()
	ccRepo.items["def-1"] = []models.ChartConfig{
		{ID: "cc-1", StackDefinitionID: "def-1", ChartName: "nginx", DeployOrder: 1},
	}

	exec := NewCleanupExecutor(mgr, defRepo, ccRepo, instanceRepo)

	err := exec.StopInstance(context.Background(), inst)
	// Only testing that the sync initiation succeeds; the async helm call
	// will fail (nonexistent binary) but that is not what we assert here.
	assert.NoError(t, err)
}

func TestCleanupExecutor_CleanInstance_WithCharts(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	inst := &models.StackInstance{
		ID:                "inst-clean-1",
		StackDefinitionID: "def-1",
		Name:              "test",
		Namespace:         "stack-test",
		Status:            models.StackStatusStopped,
	}
	require.NoError(t, instanceRepo.Create(inst))

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		MaxConcurrent: 1,
	})

	defRepo := newMockDefinitionRepo()
	defRepo.items["def-1"] = &models.StackDefinition{ID: "def-1", Name: "test-def"}

	ccRepo := newMockChartConfigRepo()
	ccRepo.items["def-1"] = []models.ChartConfig{
		{ID: "cc-1", StackDefinitionID: "def-1", ChartName: "nginx", DeployOrder: 1},
	}

	exec := NewCleanupExecutor(mgr, defRepo, ccRepo, instanceRepo)

	err := exec.CleanInstance(context.Background(), inst)
	// Only testing that the sync initiation succeeds; the async helm call
	// will fail (nonexistent binary) but that is not what we assert here.
	assert.NoError(t, err)
}

// ---- ExpiryStopper tests ----

func TestNewExpiryStopper(t *testing.T) {
	t.Parallel()

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  newMockInstanceRepo(),
		DeployLogRepo: newMockDeployLogRepo(),
		MaxConcurrent: 1,
	})
	defRepo := newMockDefinitionRepo()
	ccRepo := newMockChartConfigRepo()

	stopper := NewExpiryStopper(mgr, defRepo, ccRepo)
	assert.NotNil(t, stopper)
	assert.Same(t, mgr, stopper.manager)
}

func TestExpiryStopper_StopInstance_DefinitionNotFound(t *testing.T) {
	t.Parallel()

	defRepo := newMockDefinitionRepo()
	ccRepo := newMockChartConfigRepo()

	stopper := &ExpiryStopper{
		definitionRepo:  defRepo,
		chartConfigRepo: ccRepo,
	}

	inst := &models.StackInstance{
		ID:                "inst-1",
		StackDefinitionID: "nonexistent-def",
		Status:            models.StackStatusRunning,
	}

	err := stopper.StopInstance(context.Background(), inst)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "finding definition")
}

func TestExpiryStopper_StopInstance_NoCharts(t *testing.T) {
	t.Parallel()

	defRepo := newMockDefinitionRepo()
	defRepo.items["def-1"] = &models.StackDefinition{ID: "def-1", Name: "test-def"}

	ccRepo := newMockChartConfigRepo()
	// No charts for def-1.

	stopper := &ExpiryStopper{
		definitionRepo:  defRepo,
		chartConfigRepo: ccRepo,
	}

	inst := &models.StackInstance{
		ID:                "inst-1",
		StackDefinitionID: "def-1",
		Status:            models.StackStatusRunning,
	}

	err := stopper.StopInstance(context.Background(), inst)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no charts configured")
}

func TestExpiryStopper_StopInstance_WithCharts(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	inst := &models.StackInstance{
		ID:                "inst-expire-1",
		StackDefinitionID: "def-1",
		Name:              "test",
		Namespace:         "stack-test",
		Status:            models.StackStatusRunning,
	}
	require.NoError(t, instanceRepo.Create(inst))

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		MaxConcurrent: 1,
	})

	defRepo := newMockDefinitionRepo()
	defRepo.items["def-1"] = &models.StackDefinition{ID: "def-1", Name: "test-def"}

	ccRepo := newMockChartConfigRepo()
	ccRepo.items["def-1"] = []models.ChartConfig{
		{ID: "cc-1", StackDefinitionID: "def-1", ChartName: "nginx", DeployOrder: 1},
	}

	stopper := NewExpiryStopper(mgr, defRepo, ccRepo)

	err := stopper.StopInstance(context.Background(), inst)
	// Only testing that the sync initiation succeeds; the async helm call
	// will fail (nonexistent binary) but that is not what we assert here.
	assert.NoError(t, err)
}

func TestExpiryStopper_StopInstance_ChartListError(t *testing.T) {
	t.Parallel()

	defRepo := newMockDefinitionRepo()
	defRepo.items["def-1"] = &models.StackDefinition{ID: "def-1", Name: "test-def"}

	ccRepo := newMockChartConfigRepo()
	ccRepo.err = errors.New("db connection lost")

	stopper := &ExpiryStopper{
		definitionRepo:  defRepo,
		chartConfigRepo: ccRepo,
	}

	inst := &models.StackInstance{
		ID:                "inst-1",
		StackDefinitionID: "def-1",
		Status:            models.StackStatusRunning,
	}

	err := stopper.StopInstance(context.Background(), inst)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "listing charts")
}


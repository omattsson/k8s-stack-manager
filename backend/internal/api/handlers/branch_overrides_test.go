package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- Mock ChartBranchOverrideRepository ----

type MockChartBranchOverrideRepository struct {
	mu    sync.RWMutex
	items map[string]*models.ChartBranchOverride // key = instanceID + ":" + chartConfigID
	err   error
}

func NewMockChartBranchOverrideRepository() *MockChartBranchOverrideRepository {
	return &MockChartBranchOverrideRepository{
		items: make(map[string]*models.ChartBranchOverride),
	}
}

func (m *MockChartBranchOverrideRepository) key(instanceID, chartConfigID string) string {
	return instanceID + ":" + chartConfigID
}

func (m *MockChartBranchOverrideRepository) List(instanceID string) ([]*models.ChartBranchOverride, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	var out []*models.ChartBranchOverride
	for _, o := range m.items {
		if o.StackInstanceID == instanceID {
			cp := *o
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *MockChartBranchOverrideRepository) Get(instanceID, chartConfigID string) (*models.ChartBranchOverride, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	o, ok := m.items[m.key(instanceID, chartConfigID)]
	if !ok {
		return nil, dberrors.NewDatabaseError("get", dberrors.ErrNotFound)
	}
	cp := *o
	return &cp, nil
}

func (m *MockChartBranchOverrideRepository) Set(override *models.ChartBranchOverride) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	cp := *override
	m.items[m.key(cp.StackInstanceID, cp.ChartConfigID)] = &cp
	return nil
}

func (m *MockChartBranchOverrideRepository) Delete(instanceID, chartConfigID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	k := m.key(instanceID, chartConfigID)
	if _, ok := m.items[k]; !ok {
		return dberrors.NewDatabaseError("delete", dberrors.ErrNotFound)
	}
	delete(m.items, k)
	return nil
}

func (m *MockChartBranchOverrideRepository) DeleteByInstance(instanceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	for k, o := range m.items {
		if o.StackInstanceID == instanceID {
			delete(m.items, k)
		}
	}
	return nil
}

func (m *MockChartBranchOverrideRepository) SetError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

// ---- Test router setup ----

// newBranchTestChartRepo returns chart configs chart-1..chart-5 of definition
// def-1 and chart-other of definition def-2.
func newBranchTestChartRepo() *MockChartConfigRepository {
	repo := NewMockChartConfigRepository()
	for i := 1; i <= 5; i++ {
		id := "chart-" + string(rune('0'+i))
		_ = repo.Create(&models.ChartConfig{ID: id, StackDefinitionID: "def-1", ChartName: "app-" + id})
	}
	_ = repo.Create(&models.ChartConfig{ID: "chart-other", StackDefinitionID: "def-2", ChartName: "other"})
	return repo
}

func setupBranchOverrideRouter(
	instanceRepo *MockStackInstanceRepository,
	overrideRepo *MockChartBranchOverrideRepository,
	callerID, callerRole string,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(injectAuthContext(callerID, callerRole))

	h := NewBranchOverrideHandler(overrideRepo, instanceRepo, newBranchTestChartRepo())

	insts := r.Group("/api/v1/stack-instances")
	{
		insts.GET("/:id/branches", h.ListBranchOverrides)
		insts.GET("/:id/branches/:chartId", h.GetBranchOverride)
		insts.PUT("/:id/branches/:chartId", h.SetBranchOverride)
		insts.DELETE("/:id/branches/:chartId", h.DeleteBranchOverride)
	}
	return r
}

func seedBranchOverride(t *testing.T, repo *MockChartBranchOverrideRepository, id, instanceID, chartID, branch string) *models.ChartBranchOverride {
	t.Helper()
	o := &models.ChartBranchOverride{
		ID:              id,
		StackInstanceID: instanceID,
		ChartConfigID:   chartID,
		Branch:          branch,
		UpdatedAt:       time.Now().UTC(),
	}
	require.NoError(t, repo.Set(o))
	return o
}

// ---- ListBranchOverrides ----

func TestListBranchOverrides(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		instanceID string
		seedInst   bool
		seedCount  int
		wantStatus int
		wantLen    int
	}{
		{
			name:       "returns overrides list",
			instanceID: "inst-1",
			seedInst:   true,
			seedCount:  2,
			wantStatus: http.StatusOK,
			wantLen:    2,
		},
		{
			name:       "returns empty list when no overrides",
			instanceID: "inst-1",
			seedInst:   true,
			seedCount:  0,
			wantStatus: http.StatusOK,
			wantLen:    0,
		},
		{
			name:       "returns 404 when instance not found",
			instanceID: "nonexistent",
			seedInst:   false,
			seedCount:  0,
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			instRepo := NewMockStackInstanceRepository()
			overrideRepo := NewMockChartBranchOverrideRepository()

			if tt.seedInst {
				seedInstance(t, instRepo, tt.instanceID, "my-stack", "def-1", "uid-1", models.StackStatusDraft)
			}
			for i := 0; i < tt.seedCount; i++ {
				seedBranchOverride(t, overrideRepo,
					"bo-"+string(rune('1'+i)),
					tt.instanceID,
					"chart-"+string(rune('1'+i)),
					"feature/branch-"+string(rune('1'+i)),
				)
			}

			router := setupBranchOverrideRouter(instRepo, overrideRepo, "uid-1", "user")
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/"+tt.instanceID+"/branches", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.wantStatus == http.StatusOK {
				var overrides []*models.ChartBranchOverride
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &overrides))
				assert.Len(t, overrides, tt.wantLen)
			}
		})
	}
}

// ---- GetBranchOverride ----

func TestGetBranchOverride(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		instanceID string
		chartID    string
		callerID   string
		callerRole string
		seedInst   bool
		seed       bool
		repoErr    error
		wantStatus int
		wantError  string
	}{
		{name: "owner gets the override", instanceID: "inst-1", chartID: "chart-1", callerID: "uid-1", callerRole: "user",
			seedInst: true, seed: true, wantStatus: http.StatusOK},
		{name: "admin gets the override", instanceID: "inst-1", chartID: "chart-1", callerID: "uid-admin", callerRole: "admin",
			seedInst: true, seed: true, wantStatus: http.StatusOK},
		{name: "devops gets the override", instanceID: "inst-1", chartID: "chart-1", callerID: "uid-devops", callerRole: "devops",
			seedInst: true, seed: true, wantStatus: http.StatusOK},
		{name: "other user is forbidden", instanceID: "inst-1", chartID: "chart-1", callerID: "uid-other", callerRole: "user",
			seedInst: true, seed: true, wantStatus: http.StatusForbidden},
		{name: "no override gives 404", instanceID: "inst-1", chartID: "chart-2", callerID: "uid-1", callerRole: "user",
			seedInst: true, wantStatus: http.StatusNotFound, wantError: "Branch override not found"},
		{name: "unknown chart gives 404", instanceID: "inst-1", chartID: "chart-other", callerID: "uid-1", callerRole: "user",
			seedInst: true, wantStatus: http.StatusNotFound, wantError: "Chart not found in this stack definition"},
		{name: "unknown instance gives 404", instanceID: "missing", chartID: "chart-1", callerID: "uid-1", callerRole: "user",
			wantStatus: http.StatusNotFound},
		{name: "repository error gives 500", instanceID: "inst-1", chartID: "chart-1", callerID: "uid-1", callerRole: "user",
			seedInst: true, repoErr: errors.New("connection refused"), wantStatus: http.StatusInternalServerError, wantError: "Internal server error"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			instRepo := NewMockStackInstanceRepository()
			overrideRepo := NewMockChartBranchOverrideRepository()
			if tt.seedInst {
				seedInstance(t, instRepo, tt.instanceID, "my-stack", "def-1", "uid-1", models.StackStatusDraft)
			}
			if tt.seed {
				seedBranchOverride(t, overrideRepo, "bo-1", tt.instanceID, tt.chartID, "feature/x")
			}
			if tt.repoErr != nil {
				overrideRepo.SetError(tt.repoErr)
			}

			router := setupBranchOverrideRouter(instRepo, overrideRepo, tt.callerID, tt.callerRole)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/"+tt.instanceID+"/branches/"+tt.chartID, nil)
			router.ServeHTTP(w, req)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantStatus == http.StatusOK {
				var got models.ChartBranchOverride
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
				assert.Equal(t, "bo-1", got.ID)
				assert.Equal(t, tt.chartID, got.ChartConfigID)
				assert.Equal(t, "feature/x", got.Branch)
				return
			}
			if tt.wantError != "" {
				var body map[string]string
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
				assert.Equal(t, tt.wantError, body["error"])
			}
		})
	}
}

// ---- SetBranchOverride ----

func TestSetBranchOverride(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		instanceID string
		chartID    string
		body       interface{}
		seedInst   bool
		seedExist  bool
		repoErr    error
		wantStatus int
	}{
		{
			name:       "creates new override",
			instanceID: "inst-1",
			chartID:    "chart-1",
			body:       map[string]string{"branch": "feature/new"},
			seedInst:   true,
			wantStatus: http.StatusOK,
		},
		{
			name:       "updates existing override",
			instanceID: "inst-1",
			chartID:    "chart-1",
			body:       map[string]string{"branch": "feature/updated"},
			seedInst:   true,
			seedExist:  true,
			wantStatus: http.StatusOK,
		},
		{
			name:       "returns 404 for missing instance",
			instanceID: "nonexistent",
			chartID:    "chart-1",
			body:       map[string]string{"branch": "feature/x"},
			seedInst:   false,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "returns 400 for missing branch",
			instanceID: "inst-1",
			chartID:    "chart-1",
			body:       map[string]string{"branch": ""},
			seedInst:   true,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "returns 400 for invalid JSON",
			instanceID: "inst-1",
			chartID:    "chart-1",
			body:       "not json",
			seedInst:   true,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "returns 500 for repo error",
			instanceID: "inst-1",
			chartID:    "chart-1",
			body:       map[string]string{"branch": "feature/x"},
			seedInst:   true,
			repoErr:    errors.New("internal db failure"),
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			instRepo := NewMockStackInstanceRepository()
			overrideRepo := NewMockChartBranchOverrideRepository()

			if tt.seedInst {
				seedInstance(t, instRepo, tt.instanceID, "my-stack", "def-1", "uid-1", models.StackStatusDraft)
			}
			if tt.seedExist {
				seedBranchOverride(t, overrideRepo, "existing-id", tt.instanceID, tt.chartID, "old-branch")
			}
			if tt.repoErr != nil {
				overrideRepo.SetError(tt.repoErr)
			}

			router := setupBranchOverrideRouter(instRepo, overrideRepo, "uid-1", "user")
			w := httptest.NewRecorder()

			var bodyBytes []byte
			switch v := tt.body.(type) {
			case string:
				bodyBytes = []byte(v)
			default:
				bodyBytes, _ = json.Marshal(v)
			}

			req, _ := http.NewRequest(http.MethodPut, "/api/v1/stack-instances/"+tt.instanceID+"/branches/"+tt.chartID, bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.wantStatus == http.StatusOK {
				var result models.ChartBranchOverride
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
				assert.Equal(t, tt.instanceID, result.StackInstanceID)
				assert.Equal(t, tt.chartID, result.ChartConfigID)
				assert.NotEmpty(t, result.ID)

				if tt.seedExist {
					assert.Equal(t, "existing-id", result.ID)
				}
			}
		})
	}
}

// ---- DeleteBranchOverride ----

func TestDeleteBranchOverride(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		instanceID string
		chartID    string
		seedInst   bool
		seedExist  bool
		wantStatus int
	}{
		{
			name:       "deletes existing override",
			instanceID: "inst-1",
			chartID:    "chart-1",
			seedInst:   true,
			seedExist:  true,
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "returns 404 for missing instance",
			instanceID: "nonexistent",
			chartID:    "chart-1",
			seedInst:   false,
			seedExist:  false,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "returns 404 for missing override",
			instanceID: "inst-1",
			chartID:    "chart-999",
			seedInst:   true,
			seedExist:  false,
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			instRepo := NewMockStackInstanceRepository()
			overrideRepo := NewMockChartBranchOverrideRepository()

			if tt.seedInst {
				seedInstance(t, instRepo, tt.instanceID, "my-stack", "def-1", "uid-1", models.StackStatusDraft)
			}
			if tt.seedExist {
				seedBranchOverride(t, overrideRepo, "bo-1", tt.instanceID, tt.chartID, "feature/old")
			}

			router := setupBranchOverrideRouter(instRepo, overrideRepo, "uid-1", "user")
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodDelete, "/api/v1/stack-instances/"+tt.instanceID+"/branches/"+tt.chartID, nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
		})
	}
}

// ---- ListBranchOverrides repo error on list ----

func TestListBranchOverrides_RepoError(t *testing.T) {
	t.Parallel()

	instRepo := NewMockStackInstanceRepository()
	overrideRepo := NewMockChartBranchOverrideRepository()

	seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusDraft)
	// Set error after instance exists so FindByID succeeds but List fails.
	overrideRepo.SetError(errors.New("storage unavailable"))

	router := setupBranchOverrideRouter(instRepo, overrideRepo, "uid-1", "user")
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/inst-1/branches", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ---- DeleteBranchOverride repo error ----

func TestDeleteBranchOverride_RepoError(t *testing.T) {
	t.Parallel()

	instRepo := NewMockStackInstanceRepository()
	overrideRepo := NewMockChartBranchOverrideRepository()

	seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusDraft)
	seedBranchOverride(t, overrideRepo, "bo-1", "inst-1", "chart-1", "feature/old")
	overrideRepo.SetError(errors.New("storage error"))

	router := setupBranchOverrideRouter(instRepo, overrideRepo, "uid-1", "user")
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodDelete, "/api/v1/stack-instances/inst-1/branches/chart-1", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ---- Authorization / Owner checks ----

func TestSetBranchOverride_OwnerAuthorization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		callerID   string
		callerRole string
		ownerID    string
		wantStatus int
	}{
		{
			name:       "owner can set override",
			callerID:   "uid-1",
			callerRole: "user",
			ownerID:    "uid-1",
			wantStatus: http.StatusOK,
		},
		{
			name:       "non-owner gets 403",
			callerID:   "uid-other",
			callerRole: "user",
			ownerID:    "uid-1",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "admin can set override on any instance",
			callerID:   "uid-admin",
			callerRole: "admin",
			ownerID:    "uid-1",
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			instRepo := NewMockStackInstanceRepository()
			overrideRepo := NewMockChartBranchOverrideRepository()

			seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", tt.ownerID, models.StackStatusDraft)

			router := setupBranchOverrideRouter(instRepo, overrideRepo, tt.callerID, tt.callerRole)
			w := httptest.NewRecorder()
			body, _ := json.Marshal(map[string]string{"branch": "feature/test"})
			req, _ := http.NewRequest(http.MethodPut, "/api/v1/stack-instances/inst-1/branches/chart-1", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
		})
	}
}

func TestDeleteBranchOverride_OwnerAuthorization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		callerID   string
		callerRole string
		ownerID    string
		wantStatus int
	}{
		{
			name:       "owner can delete override",
			callerID:   "uid-1",
			callerRole: "user",
			ownerID:    "uid-1",
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "non-owner gets 403",
			callerID:   "uid-other",
			callerRole: "user",
			ownerID:    "uid-1",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "admin can delete override on any instance",
			callerID:   "uid-admin",
			callerRole: "admin",
			ownerID:    "uid-1",
			wantStatus: http.StatusNoContent,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			instRepo := NewMockStackInstanceRepository()
			overrideRepo := NewMockChartBranchOverrideRepository()

			seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", tt.ownerID, models.StackStatusDraft)
			seedBranchOverride(t, overrideRepo, "bo-1", "inst-1", "chart-1", "feature/old")

			router := setupBranchOverrideRouter(instRepo, overrideRepo, tt.callerID, tt.callerRole)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodDelete, "/api/v1/stack-instances/inst-1/branches/chart-1", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
		})
	}
}

// TestBranchOverride_ChartValidation checks that :chartId must be a chart of
// the instance's definition (#445), and that DELETE still removes a stale row
// whose chart ID is not a chart config.
func TestBranchOverride_ChartValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		method     string
		chartID    string
		seedRow    bool
		wantStatus int
		wantError  string
	}{
		{name: "PUT unknown chart ID", method: http.MethodPut, chartID: "my-chart-name", wantStatus: http.StatusNotFound, wantError: msgChartNotInDefinition},
		{name: "PUT chart of another definition", method: http.MethodPut, chartID: "chart-other", wantStatus: http.StatusNotFound, wantError: msgChartNotInDefinition},
		{name: "PUT chart of the definition", method: http.MethodPut, chartID: "chart-1", wantStatus: http.StatusOK},
		{name: "DELETE unknown chart without override", method: http.MethodDelete, chartID: "my-chart-name", wantStatus: http.StatusNotFound, wantError: msgChartNotInDefinition},
		{name: "DELETE known chart without override", method: http.MethodDelete, chartID: "chart-2", wantStatus: http.StatusNotFound, wantError: "Branch override not found"},
		{name: "DELETE stale row with unknown chart ID", method: http.MethodDelete, chartID: "my-chart-name", seedRow: true, wantStatus: http.StatusNoContent},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			instRepo := NewMockStackInstanceRepository()
			overrideRepo := NewMockChartBranchOverrideRepository()
			seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusDraft)
			if tt.seedRow {
				seedBranchOverride(t, overrideRepo, "bo-1", "inst-1", tt.chartID, "feature/old")
			}

			router := setupBranchOverrideRouter(instRepo, overrideRepo, "uid-1", "user")
			body := bytes.NewReader([]byte(`{"branch":"feature/x"}`))
			req, _ := http.NewRequest(tt.method, "/api/v1/stack-instances/inst-1/branches/"+tt.chartID, body)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantError != "" {
				var resp map[string]string
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, tt.wantError, resp["error"])
			}
			if tt.method == http.MethodPut && tt.wantStatus == http.StatusNotFound {
				_, err := overrideRepo.Get("inst-1", tt.chartID)
				assert.Error(t, err, "no row may be stored for an invalid chart")
			}
		})
	}
}

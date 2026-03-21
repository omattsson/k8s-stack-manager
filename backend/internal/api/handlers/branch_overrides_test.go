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

func setupBranchOverrideRouter(
	instanceRepo *MockStackInstanceRepository,
	overrideRepo *MockChartBranchOverrideRepository,
	callerID, callerRole string,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(injectAuthContext(callerID, callerRole))

	h := NewBranchOverrideHandler(overrideRepo, instanceRepo)

	insts := r.Group("/api/v1/stack-instances")
	{
		insts.GET("/:id/branches", h.ListBranchOverrides)
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

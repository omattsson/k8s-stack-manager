package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockSharedValuesRepo is an in-memory mock for models.SharedValuesRepository.
type mockSharedValuesRepo struct {
	items map[string]*models.SharedValues
}

func newMockSharedValuesRepo() *mockSharedValuesRepo {
	return &mockSharedValuesRepo{items: make(map[string]*models.SharedValues)}
}

func (m *mockSharedValuesRepo) Create(ctx context.Context, sv *models.SharedValues) error {
	if err := ctxDone(ctx, "create"); err != nil {
		return err
	}
	if _, exists := m.items[sv.ID]; exists {
		return dberrors.NewDatabaseError("create", dberrors.ErrDuplicateKey)
	}
	now := time.Now().UTC()
	sv.CreatedAt = now
	sv.UpdatedAt = now
	m.items[sv.ID] = sv
	return nil
}

func (m *mockSharedValuesRepo) FindByID(ctx context.Context, id string) (*models.SharedValues, error) {
	if err := ctxDone(ctx, "find_by_id"); err != nil {
		return nil, err
	}
	sv, ok := m.items[id]
	if !ok {
		return nil, dberrors.NewDatabaseError("find_by_id", dberrors.ErrNotFound)
	}
	return sv, nil
}

func (m *mockSharedValuesRepo) FindByClusterAndID(ctx context.Context, clusterID, id string) (*models.SharedValues, error) {
	if err := ctxDone(ctx, "find_by_cluster_and_id"); err != nil {
		return nil, err
	}
	sv, ok := m.items[id]
	if !ok || sv.ClusterID != clusterID {
		return nil, dberrors.NewDatabaseError("find_by_cluster_and_id", dberrors.ErrNotFound)
	}
	return sv, nil
}

func (m *mockSharedValuesRepo) Update(ctx context.Context, sv *models.SharedValues) error {
	if err := ctxDone(ctx, "update"); err != nil {
		return err
	}
	if _, ok := m.items[sv.ID]; !ok {
		return dberrors.NewDatabaseError("update", dberrors.ErrNotFound)
	}
	sv.UpdatedAt = time.Now().UTC()
	m.items[sv.ID] = sv
	return nil
}

func (m *mockSharedValuesRepo) Delete(ctx context.Context, id string) error {
	if err := ctxDone(ctx, "delete"); err != nil {
		return err
	}
	if _, ok := m.items[id]; !ok {
		return dberrors.NewDatabaseError("delete", dberrors.ErrNotFound)
	}
	delete(m.items, id)
	return nil
}

func (m *mockSharedValuesRepo) ListByCluster(ctx context.Context, clusterID string) ([]models.SharedValues, error) {
	if err := ctxDone(ctx, "list_by_cluster"); err != nil {
		return nil, err
	}
	var result []models.SharedValues
	for _, sv := range m.items {
		if sv.ClusterID == clusterID {
			result = append(result, *sv)
		}
	}
	return result, nil
}

// mockClusterRepoForSV is a minimal mock for models.ClusterRepository used by SharedValuesHandler tests.
type mockClusterRepoForSV struct {
	clusters map[string]*models.Cluster
}

func newMockClusterRepoForSV() *mockClusterRepoForSV {
	return &mockClusterRepoForSV{clusters: map[string]*models.Cluster{
		"cluster-1": {ID: "cluster-1", Name: "test-cluster", APIServerURL: "https://k8s.example.com", KubeconfigPath: "/tmp/kc"},
	}}
}

func (m *mockClusterRepoForSV) Create(ctx context.Context, c *models.Cluster) error {
	return ctxDone(ctx, "create")
}
func (m *mockClusterRepoForSV) Update(ctx context.Context, c *models.Cluster) error {
	return ctxDone(ctx, "update")
}
func (m *mockClusterRepoForSV) UpdateHealthStatus(ctx context.Context, _, _ string) error {
	return ctxDone(ctx, "update_health_status")
}
func (m *mockClusterRepoForSV) Delete(ctx context.Context, id string) error {
	return ctxDone(ctx, "delete")
}
func (m *mockClusterRepoForSV) List(ctx context.Context) ([]models.Cluster, error) {
	if err := ctxDone(ctx, "list"); err != nil {
		return nil, err
	}
	var r []models.Cluster
	for _, c := range m.clusters {
		r = append(r, *c)
	}
	return r, nil
}
func (m *mockClusterRepoForSV) FindDefault(ctx context.Context) (*models.Cluster, error) {
	if err := ctxDone(ctx, "find_default"); err != nil {
		return nil, err
	}
	return nil, dberrors.NewDatabaseError("find_default", dberrors.ErrNotFound)
}
func (m *mockClusterRepoForSV) SetDefault(ctx context.Context, id string) error {
	return ctxDone(ctx, "set_default")
}
func (m *mockClusterRepoForSV) FindByID(ctx context.Context, id string) (*models.Cluster, error) {
	if err := ctxDone(ctx, "find_by_id"); err != nil {
		return nil, err
	}
	c, ok := m.clusters[id]
	if !ok {
		return nil, dberrors.NewDatabaseError("find_by_id", dberrors.ErrNotFound)
	}
	return c, nil
}

func setupSharedValuesRouter() (*gin.Engine, *mockSharedValuesRepo, *mockClusterRepoForSV) {
	gin.SetMode(gin.TestMode)
	svRepo := newMockSharedValuesRepo()
	clusterRepo := newMockClusterRepoForSV()
	handler := NewSharedValuesHandler(svRepo, clusterRepo)

	router := gin.New()
	clusters := router.Group("/api/v1/clusters")
	{
		sv := clusters.Group("/:id/shared-values")
		{
			sv.GET("", handler.ListSharedValues)
			sv.POST("", handler.CreateSharedValues)
			sv.PUT("/:valueId", handler.UpdateSharedValues)
			sv.DELETE("/:valueId", handler.DeleteSharedValues)
		}
	}
	return router, svRepo, clusterRepo
}

func TestListSharedValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		clusterID  string
		seed       []*models.SharedValues
		wantStatus int
		wantLen    int
	}{
		{
			name:      "success - returns values sorted by priority",
			clusterID: "cluster-1",
			seed: []*models.SharedValues{
				{ID: "sv-1", ClusterID: "cluster-1", Name: "env", Values: "env: prod", Priority: 10},
				{ID: "sv-2", ClusterID: "cluster-1", Name: "base", Values: "env: base", Priority: 1},
			},
			wantStatus: http.StatusOK,
			wantLen:    2,
		},
		{
			name:       "cluster not found",
			clusterID:  "nonexistent",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "empty list",
			clusterID:  "cluster-1",
			wantStatus: http.StatusOK,
			wantLen:    0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			router, svRepo, _ := setupSharedValuesRouter()
			for _, sv := range tt.seed {
				svRepo.items[sv.ID] = sv
			}

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("GET", "/api/v1/clusters/"+tt.clusterID+"/shared-values", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)

			if tt.wantStatus == http.StatusOK {
				var result []models.SharedValues
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
				assert.Len(t, result, tt.wantLen)
			}
		})
	}
}

func TestCreateSharedValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		clusterID  string
		body       interface{}
		wantStatus int
	}{
		{
			name:      "success",
			clusterID: "cluster-1",
			body: map[string]interface{}{
				"name":     "env-vars",
				"values":   "env: production",
				"priority": 10,
			},
			wantStatus: http.StatusCreated,
		},
		{
			name:       "invalid JSON",
			clusterID:  "cluster-1",
			body:       "not json",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "cluster not found",
			clusterID:  "nonexistent",
			body:       map[string]interface{}{"name": "x", "values": "a: 1"},
			wantStatus: http.StatusNotFound,
		},
		{
			name:      "missing name",
			clusterID: "cluster-1",
			body: map[string]interface{}{
				"values":   "env: production",
				"priority": 0,
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:      "invalid YAML values",
			clusterID: "cluster-1",
			body: map[string]interface{}{
				"name":   "bad-yaml",
				"values": "{{invalid: [yaml",
			},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			router, _, _ := setupSharedValuesRouter()

			var bodyBytes []byte
			switch v := tt.body.(type) {
			case string:
				bodyBytes = []byte(v)
			default:
				bodyBytes, _ = json.Marshal(v)
			}

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("POST", "/api/v1/clusters/"+tt.clusterID+"/shared-values", bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)

			if tt.wantStatus == http.StatusCreated {
				var result models.SharedValues
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
				assert.NotEmpty(t, result.ID)
				assert.Equal(t, "cluster-1", result.ClusterID)
			}
		})
	}
}

func TestUpdateSharedValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		clusterID  string
		valueID    string
		seed       *models.SharedValues
		body       interface{}
		wantStatus int
	}{
		{
			name:       "success",
			clusterID:  "cluster-1",
			valueID:    "sv-1",
			seed:       &models.SharedValues{ID: "sv-1", ClusterID: "cluster-1", Name: "old", Values: "a: 1", Priority: 0},
			body:       map[string]interface{}{"name": "updated", "values": "a: 2", "priority": 5},
			wantStatus: http.StatusOK,
		},
		{
			name:       "not found",
			clusterID:  "cluster-1",
			valueID:    "nonexistent",
			body:       map[string]interface{}{"name": "x", "values": "a: 1"},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "cluster mismatch",
			clusterID:  "cluster-1",
			valueID:    "sv-other",
			seed:       &models.SharedValues{ID: "sv-other", ClusterID: "other-cluster", Name: "x", Values: "a: 1"},
			body:       map[string]interface{}{"name": "x", "values": "a: 1"},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "invalid JSON body",
			clusterID:  "cluster-1",
			valueID:    "sv-1",
			seed:       &models.SharedValues{ID: "sv-1", ClusterID: "cluster-1", Name: "old", Values: "a: 1"},
			body:       "not json",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing name validation error",
			clusterID:  "cluster-1",
			valueID:    "sv-1",
			seed:       &models.SharedValues{ID: "sv-1", ClusterID: "cluster-1", Name: "old", Values: "a: 1"},
			body:       map[string]interface{}{"name": "", "values": "a: 1"},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "cluster not found",
			clusterID:  "nonexistent",
			valueID:    "sv-1",
			body:       map[string]interface{}{"name": "x", "values": "a: 1"},
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			router, svRepo, _ := setupSharedValuesRouter()
			if tt.seed != nil {
				svRepo.items[tt.seed.ID] = tt.seed
			}

			var bodyBytes []byte
			switch v := tt.body.(type) {
			case string:
				bodyBytes = []byte(v)
			default:
				bodyBytes, _ = json.Marshal(v)
			}

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("PUT", "/api/v1/clusters/"+tt.clusterID+"/shared-values/"+tt.valueID, bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)

			if tt.wantStatus == http.StatusOK {
				var result models.SharedValues
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
				assert.Equal(t, "updated", result.Name)
				assert.Equal(t, "cluster-1", result.ClusterID)
			}
		})
	}
}

func TestDeleteSharedValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		clusterID  string
		valueID    string
		seed       *models.SharedValues
		wantStatus int
	}{
		{
			name:       "success",
			clusterID:  "cluster-1",
			valueID:    "sv-1",
			seed:       &models.SharedValues{ID: "sv-1", ClusterID: "cluster-1", Name: "env", Values: "a: 1"},
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "not found",
			clusterID:  "cluster-1",
			valueID:    "nonexistent",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "cluster mismatch",
			clusterID:  "cluster-1",
			valueID:    "sv-other",
			seed:       &models.SharedValues{ID: "sv-other", ClusterID: "other-cluster", Name: "x", Values: "a: 1"},
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			router, svRepo, _ := setupSharedValuesRouter()
			if tt.seed != nil {
				svRepo.items[tt.seed.ID] = tt.seed
			}

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("DELETE", "/api/v1/clusters/"+tt.clusterID+"/shared-values/"+tt.valueID, nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
		})
	}
}

func (*mockClusterRepoForSV) CountAll(ctx context.Context) (int, error) {
	return 0, ctxDone(ctx, "count_all")
}

func (*mockClusterRepoForSV) NamesByIDs(ctx context.Context, _ []string) (map[string]string, error) {
	if err := ctxDone(ctx, "names_by_ids"); err != nil {
		return nil, err
	}
	return map[string]string{}, nil
}
func (*mockClusterRepoForSV) CountByHealthStatus(ctx context.Context, status string) (int, error) {
	return 0, ctxDone(ctx, "count_by_health_status")
}

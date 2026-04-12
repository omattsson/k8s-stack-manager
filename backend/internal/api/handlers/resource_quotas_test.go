package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"backend/internal/api/middleware"
	"backend/internal/cluster"
	"backend/internal/database"
	"backend/internal/k8s"
	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// ---- MockResourceQuotaRepository ----

type MockResourceQuotaRepository struct {
	mu      sync.RWMutex
	configs map[string]*models.ResourceQuotaConfig // keyed by ClusterID
	err     error
}

func NewMockResourceQuotaRepository() *MockResourceQuotaRepository {
	return &MockResourceQuotaRepository{configs: make(map[string]*models.ResourceQuotaConfig)}
}

func (m *MockResourceQuotaRepository) GetByClusterID(_ context.Context, clusterID string) (*models.ResourceQuotaConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	cfg, ok := m.configs[clusterID]
	if !ok {
		return nil, dberrors.NewDatabaseError("GetByClusterID", dberrors.ErrNotFound)
	}
	cp := *cfg
	return &cp, nil
}

func (m *MockResourceQuotaRepository) Upsert(_ context.Context, config *models.ResourceQuotaConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if config.ID == "" {
		config.ID = fmt.Sprintf("quota-%d", len(m.configs)+1)
	}
	m.configs[config.ClusterID] = config
	return nil
}

func (m *MockResourceQuotaRepository) Delete(_ context.Context, clusterID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if _, ok := m.configs[clusterID]; !ok {
		return dberrors.NewDatabaseError("Delete", dberrors.ErrNotFound)
	}
	delete(m.configs, clusterID)
	return nil
}

func (m *MockResourceQuotaRepository) SetError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

// setupQuotaRouter creates a gin engine wired to ClusterHandler quota routes.
func setupQuotaRouter(
	clusterRepo *MockClusterRepository,
	instanceRepo *MockStackInstanceRepository,
	quotaRepo *MockResourceQuotaRepository,
	registry *cluster.Registry,
	callerRole string,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(injectAuthContext("test-user-id", callerRole))

	h := NewClusterHandlerWithQuotas(clusterRepo, registry, instanceRepo, quotaRepo)
	adminMW := middleware.RequireAdmin()
	devopsMW := middleware.RequireDevOps()
	clusters := r.Group("/api/v1/clusters")
	{
		clusters.GET("/:id/quotas", h.GetQuotas)
		clusters.PUT("/:id/quotas", adminMW, h.UpdateQuotas)
		clusters.DELETE("/:id/quotas", adminMW, h.DeleteQuotas)
		clusters.GET("/:id/utilization", devopsMW, h.GetUtilization)
	}
	return r
}

func TestGetQuotas(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		clusterID  string
		seed       bool
		wantStatus int
	}{
		{
			name:       "returns quota config when found",
			clusterID:  "cluster-1",
			seed:       true,
			wantStatus: http.StatusOK,
		},
		{
			name:       "returns 404 when no quota config",
			clusterID:  "cluster-1",
			seed:       false,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "returns 404 when cluster not found",
			clusterID:  "nonexistent",
			seed:       false,
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			clusterRepo := NewMockClusterRepository()
			instanceRepo := NewMockStackInstanceRepository()
			quotaRepo := NewMockResourceQuotaRepository()

			seedCluster(clusterRepo, "cluster-1", "test-cluster")

			if tt.seed {
				_ = quotaRepo.Upsert(context.Background(), &models.ResourceQuotaConfig{
					ClusterID:     "cluster-1",
					CPURequest:    "500m",
					CPULimit:      "2000m",
					MemoryRequest: "256Mi",
					MemoryLimit:   "1Gi",
					PodLimit:      20,
				})
			}

			r := setupQuotaRouter(clusterRepo, instanceRepo, quotaRepo, nil, "admin")
			w := httptest.NewRecorder()
			req, _ := http.NewRequest("GET", "/api/v1/clusters/"+tt.clusterID+"/quotas", nil)
			r.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)

			if tt.wantStatus == http.StatusOK {
				var resp models.ResourceQuotaConfig
				err := json.Unmarshal(w.Body.Bytes(), &resp)
				require.NoError(t, err)
				assert.Equal(t, "500m", resp.CPURequest)
				assert.Equal(t, "2000m", resp.CPULimit)
				assert.Equal(t, 20, resp.PodLimit)
			}
		})
	}
}

func TestUpdateQuotas(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		clusterID  string
		body       interface{}
		role       string
		wantStatus int
	}{
		{
			name:      "creates new quota config",
			clusterID: "cluster-1",
			body: UpdateQuotaRequest{
				CPURequest:    "500m",
				CPULimit:      "2000m",
				MemoryRequest: "256Mi",
				MemoryLimit:   "1Gi",
				PodLimit:      10,
			},
			role:       "admin",
			wantStatus: http.StatusOK,
		},
		{
			name:      "updates existing quota config",
			clusterID: "cluster-1",
			body: UpdateQuotaRequest{
				CPURequest: "1000m",
				CPULimit:   "4000m",
			},
			role:       "admin",
			wantStatus: http.StatusOK,
		},
		{
			name:      "rejects negative pod limit",
			clusterID: "cluster-1",
			body: UpdateQuotaRequest{
				PodLimit: -5,
			},
			role:       "admin",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "rejects non-admin users",
			clusterID:  "cluster-1",
			body:       UpdateQuotaRequest{CPULimit: "1"},
			role:       "developer",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "returns 404 for unknown cluster",
			clusterID:  "nonexistent",
			body:       UpdateQuotaRequest{CPULimit: "1"},
			role:       "admin",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "rejects invalid JSON",
			clusterID:  "cluster-1",
			body:       "not json",
			role:       "admin",
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			clusterRepo := NewMockClusterRepository()
			instanceRepo := NewMockStackInstanceRepository()
			quotaRepo := NewMockResourceQuotaRepository()

			seedCluster(clusterRepo, "cluster-1", "test-cluster")

			r := setupQuotaRouter(clusterRepo, instanceRepo, quotaRepo, nil, tt.role)
			w := httptest.NewRecorder()

			var bodyBytes []byte
			if s, ok := tt.body.(string); ok {
				bodyBytes = []byte(s)
			} else {
				bodyBytes, _ = json.Marshal(tt.body)
			}

			req, _ := http.NewRequest("PUT", "/api/v1/clusters/"+tt.clusterID+"/quotas", bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)

			if tt.wantStatus == http.StatusOK {
				var resp models.ResourceQuotaConfig
				err := json.Unmarshal(w.Body.Bytes(), &resp)
				require.NoError(t, err)
				assert.Equal(t, "cluster-1", resp.ClusterID)
			}
		})
	}
}

func TestDeleteQuotas(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		clusterID  string
		seed       bool
		role       string
		wantStatus int
	}{
		{
			name:       "deletes existing quota config",
			clusterID:  "cluster-1",
			seed:       true,
			role:       "admin",
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "returns 404 when no quota config",
			clusterID:  "cluster-1",
			seed:       false,
			role:       "admin",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "rejects non-admin users",
			clusterID:  "cluster-1",
			seed:       true,
			role:       "developer",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "returns 404 for unknown cluster",
			clusterID:  "nonexistent",
			seed:       false,
			role:       "admin",
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			clusterRepo := NewMockClusterRepository()
			instanceRepo := NewMockStackInstanceRepository()
			quotaRepo := NewMockResourceQuotaRepository()

			seedCluster(clusterRepo, "cluster-1", "test-cluster")

			if tt.seed {
				_ = quotaRepo.Upsert(context.Background(), &models.ResourceQuotaConfig{
					ClusterID:  "cluster-1",
					CPURequest: "500m",
				})
			}

			r := setupQuotaRouter(clusterRepo, instanceRepo, quotaRepo, nil, tt.role)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest("DELETE", "/api/v1/clusters/"+tt.clusterID+"/quotas", nil)
			r.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
		})
	}
}

func TestGetUtilization(t *testing.T) {
	t.Parallel()

	t.Run("returns utilization for cluster with namespaces", func(t *testing.T) {
		t.Parallel()

		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		quotaRepo := NewMockResourceQuotaRepository()

		seedCluster(clusterRepo, "cluster-1", "test-cluster")

		// Create a fake K8s client with a stack namespace and resource quota.
		fakeClient := fake.NewSimpleClientset(
			&corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: "stack-test-user"},
			},
			&corev1.ResourceQuota{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "stack-manager-quota",
					Namespace: "stack-test-user",
				},
				Status: corev1.ResourceQuotaStatus{
					Hard: corev1.ResourceList{
						corev1.ResourceRequestsCPU: resource.MustParse("2000m"),
						corev1.ResourcePods:        resource.MustParse("10"),
					},
					Used: corev1.ResourceList{
						corev1.ResourceRequestsCPU: resource.MustParse("500m"),
						corev1.ResourcePods:        resource.MustParse("3"),
					},
				},
			},
		)

		k8sClient := k8s.NewClientFromInterface(fakeClient)
		helmExec := &mockClusterHelmExecutor{}
		registry := cluster.NewRegistryForTest("cluster-1", k8sClient, helmExec)

		r := setupQuotaRouter(clusterRepo, instanceRepo, quotaRepo, registry, "devops")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/api/v1/clusters/cluster-1/utilization", nil)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var resp ClusterUtilization
		err := json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)
		assert.Equal(t, "cluster-1", resp.ClusterID)
		require.Len(t, resp.Namespaces, 1)
		assert.Equal(t, "stack-test-user", resp.Namespaces[0].Namespace)
		assert.Equal(t, "500m", resp.Namespaces[0].CPUUsed)
		assert.Equal(t, 3, resp.Namespaces[0].PodCount)
	})

	t.Run("returns 404 for unknown cluster", func(t *testing.T) {
		t.Parallel()

		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		quotaRepo := NewMockResourceQuotaRepository()

		r := setupQuotaRouter(clusterRepo, instanceRepo, quotaRepo, nil, "devops")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/api/v1/clusters/nonexistent/utilization", nil)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("returns 500 when registry is nil", func(t *testing.T) {
		t.Parallel()

		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		quotaRepo := NewMockResourceQuotaRepository()
		seedCluster(clusterRepo, "cluster-1", "test-cluster")

		r := setupQuotaRouter(clusterRepo, instanceRepo, quotaRepo, nil, "devops")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/api/v1/clusters/cluster-1/utilization", nil)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})
}

func TestPerUserInstanceLimit(t *testing.T) {
	t.Parallel()

	t.Run("blocks creation when limit reached", func(t *testing.T) {
		t.Parallel()

		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		defRepo := NewMockStackDefinitionRepository()

		// Create cluster with max 2 instances per user.
		cl := &models.Cluster{
			ID:                  "cluster-1",
			Name:                "test-cluster",
			APIServerURL:        "https://k8s.example.com",
			KubeconfigPath:      "/path/to/kubeconfig",
			MaxInstancesPerUser: 2,
		}
		_ = clusterRepo.Create(cl)

		// Create 2 existing instances for the user on this cluster.
		_ = instanceRepo.Create(&models.StackInstance{
			ID:                "inst-1",
			Name:              "inst-one",
			StackDefinitionID: "def-1",
			OwnerID:           "test-user-id",
			ClusterID:         "cluster-1",
			Namespace:         "stack-inst-one-testuser",
			Status:            models.StackStatusRunning,
		})
		_ = instanceRepo.Create(&models.StackInstance{
			ID:                "inst-2",
			Name:              "inst-two",
			StackDefinitionID: "def-1",
			OwnerID:           "test-user-id",
			ClusterID:         "cluster-1",
			Namespace:         "stack-inst-two-testuser",
			Status:            models.StackStatusRunning,
		})

		_ = defRepo.Create(&models.StackDefinition{
			ID:      "def-1",
			Name:    "test-def",
			OwnerID: "test-user-id",
		})

		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set("userID", "test-user-id")
			c.Set("username", "testuser")
			c.Set("role", "developer")
			c.Next()
		})

		h := &InstanceHandler{
			instanceRepo:   instanceRepo,
			definitionRepo: defRepo,
			clusterRepo:    clusterRepo,
			txRunner: &mockHandlerTxRunner{repos: database.TxRepos{
				StackInstance: instanceRepo,
			}},
		}
		r.POST("/api/v1/stack-instances", h.CreateInstance)

		body, _ := json.Marshal(map[string]interface{}{
			"stack_definition_id": "def-1",
			"name":                "inst-three",
			"cluster_id":          "cluster-1",
		})
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/api/v1/stack-instances", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusConflict, w.Code)
		var resp map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		assert.Contains(t, resp["error"], "Maximum instances per user reached")
		assert.Contains(t, resp["error"], "limit: 2")
	})

	t.Run("allows creation when under limit", func(t *testing.T) {
		t.Parallel()

		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		defRepo := NewMockStackDefinitionRepository()

		cl := &models.Cluster{
			ID:                  "cluster-1",
			Name:                "test-cluster",
			APIServerURL:        "https://k8s.example.com",
			KubeconfigPath:      "/path/to/kubeconfig",
			MaxInstancesPerUser: 5,
		}
		_ = clusterRepo.Create(cl)

		// Only 1 existing instance.
		_ = instanceRepo.Create(&models.StackInstance{
			ID:                "inst-1",
			Name:              "inst-one",
			StackDefinitionID: "def-1",
			OwnerID:           "test-user-id",
			ClusterID:         "cluster-1",
			Namespace:         "stack-inst-one-testuser",
			Status:            models.StackStatusRunning,
		})

		_ = defRepo.Create(&models.StackDefinition{
			ID:      "def-1",
			Name:    "test-def",
			OwnerID: "test-user-id",
		})

		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set("userID", "test-user-id")
			c.Set("username", "testuser")
			c.Set("role", "developer")
			c.Next()
		})

		h := &InstanceHandler{
			instanceRepo:   instanceRepo,
			definitionRepo: defRepo,
			clusterRepo:    clusterRepo,
			txRunner: &mockHandlerTxRunner{repos: database.TxRepos{
				StackInstance: instanceRepo,
			}},
		}
		r.POST("/api/v1/stack-instances", h.CreateInstance)

		body, _ := json.Marshal(map[string]interface{}{
			"stack_definition_id": "def-1",
			"name":                "inst-two",
			"cluster_id":          "cluster-1",
		})
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/api/v1/stack-instances", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusCreated, w.Code)
	})

	t.Run("skips limit check when max is 0 (unlimited)", func(t *testing.T) {
		t.Parallel()

		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		defRepo := NewMockStackDefinitionRepository()

		cl := &models.Cluster{
			ID:                  "cluster-1",
			Name:                "test-cluster",
			APIServerURL:        "https://k8s.example.com",
			KubeconfigPath:      "/path/to/kubeconfig",
			MaxInstancesPerUser: 0, // unlimited
		}
		_ = clusterRepo.Create(cl)

		_ = defRepo.Create(&models.StackDefinition{
			ID:      "def-1",
			Name:    "test-def",
			OwnerID: "test-user-id",
		})

		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set("userID", "test-user-id")
			c.Set("username", "testuser")
			c.Set("role", "developer")
			c.Next()
		})

		h := &InstanceHandler{
			instanceRepo:   instanceRepo,
			definitionRepo: defRepo,
			clusterRepo:    clusterRepo,
		}
		r.POST("/api/v1/stack-instances", h.CreateInstance)

		body, _ := json.Marshal(map[string]interface{}{
			"stack_definition_id": "def-1",
			"name":                "inst-one",
			"cluster_id":          "cluster-1",
		})
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/api/v1/stack-instances", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusCreated, w.Code)
	})
}

// mockValuesGen is a stub for tests that don't exercise values generation.
type mockValuesGen struct{}

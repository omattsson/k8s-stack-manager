package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/cluster"
	"backend/internal/deployer"
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

// mockClusterHelmExecutor implements deployer.HelmExecutor for cluster tests.
type mockClusterHelmExecutor struct{}

// strPtr returns a pointer to s — helper for building UpdateClusterRequest in tests.
func strPtr(s string) *string { return &s }

func (m *mockClusterHelmExecutor) Install(_ context.Context, _ deployer.InstallRequest) (string, error) {
	return "", nil
}
func (m *mockClusterHelmExecutor) Uninstall(_ context.Context, _ deployer.UninstallRequest) (string, error) {
	return "", nil
}
func (m *mockClusterHelmExecutor) Status(_ context.Context, name, _ string) (*deployer.ReleaseStatus, error) {
	return &deployer.ReleaseStatus{Name: name}, nil
}
func (m *mockClusterHelmExecutor) ListReleases(_ context.Context, _ string) ([]string, error) {
	return []string{}, nil
}
func (m *mockClusterHelmExecutor) History(_ context.Context, _ string, _ string, _ int) ([]deployer.ReleaseRevision, error) {
	return nil, nil
}
func (m *mockClusterHelmExecutor) Rollback(_ context.Context, _ string, _ string, _ int) (string, error) {
	return "", nil
}
func (m *mockClusterHelmExecutor) GetValues(_ context.Context, _ string, _ string, _ int) (string, error) {
	return "", nil
}
func (m *mockClusterHelmExecutor) Timeout() time.Duration { return 30 * time.Second }

// setupClusterRouter creates a gin engine wired to ClusterHandler routes.
func setupClusterRouter(
	clusterRepo *MockClusterRepository,
	instanceRepo *MockStackInstanceRepository,
	registry *cluster.Registry,
	callerRole string,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(injectAuthContext("test-user-id", callerRole))

	h := NewClusterHandler(clusterRepo, registry, instanceRepo)
	adminMW := middleware.RequireAdmin()
	devopsMW := middleware.RequireDevOps()
	clusters := r.Group("/api/v1/clusters")
	{
		clusters.GET("", h.ListClusters)
		clusters.GET("/:id", h.GetCluster)
		clusters.POST("", adminMW, h.CreateCluster)
		clusters.PUT("/:id", adminMW, h.UpdateCluster)
		clusters.DELETE("/:id", adminMW, h.DeleteCluster)
		clusters.POST("/:id/test", adminMW, h.TestClusterConnection)
		clusters.POST("/:id/default", adminMW, h.SetDefaultCluster)

		clusters.GET("/:id/health/summary", devopsMW, h.GetClusterHealthSummary)
		clusters.GET("/:id/health/nodes", devopsMW, h.GetClusterNodes)
		clusters.GET("/:id/namespaces", devopsMW, h.GetClusterNamespaces)
	}
	return r
}

// seedCluster is a helper to add a cluster to the mock repository.
func seedCluster(repo *MockClusterRepository, id, name string) *models.Cluster {
	cl := &models.Cluster{
		ID:             id,
		Name:           name,
		APIServerURL:   "https://k8s.example.com",
		KubeconfigPath: "/path/to/kubeconfig",
		Region:         "eastus",
		HealthStatus:   models.ClusterHealthy,
	}
	_ = repo.Create(cl)
	return cl
}

func TestListClusters(t *testing.T) {
	t.Parallel()

	t.Run("returns empty list", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "user")

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var result []models.Cluster
		err := json.Unmarshal(w.Body.Bytes(), &result)
		require.NoError(t, err)
		assert.Empty(t, result)
	})

	t.Run("returns clusters without kubeconfig", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()

		cl := &models.Cluster{
			ID:             "cl-1",
			Name:           "prod",
			APIServerURL:   "https://k8s.example.com",
			KubeconfigData: "secret-kubeconfig",
			KubeconfigPath: "/tmp/kubeconfig",
			Region:         "eastus",
			HealthStatus:   models.ClusterHealthy,
		}
		_ = clusterRepo.Create(cl)

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		// Verify kubeconfig fields are not in the JSON response.
		body := w.Body.String()
		assert.NotContains(t, body, "secret-kubeconfig")
		assert.NotContains(t, body, "/tmp/kubeconfig")
		assert.NotContains(t, body, "kubeconfig_data")
		assert.NotContains(t, body, "kubeconfig_path")
	})
}

func TestCreateCluster(t *testing.T) {
	t.Parallel()

	t.Run("creates cluster successfully", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")

		payload := CreateClusterRequest{
			Name:           "production",
			APIServerURL:   "https://k8s.prod.example.com",
			KubeconfigData: "base64-kubeconfig-data",
			Region:         "westus",
			MaxNamespaces:  50,
		}
		body, _ := json.Marshal(payload)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/clusters", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusCreated, w.Code)

		var result models.Cluster
		err := json.Unmarshal(w.Body.Bytes(), &result)
		require.NoError(t, err)
		assert.Equal(t, "production", result.Name)
		assert.Equal(t, "westus", result.Region)

		// Verify kubeconfig is NOT in the response.
		respBody := w.Body.String()
		assert.NotContains(t, respBody, "base64-kubeconfig-data")
		assert.NotContains(t, respBody, "kubeconfig_data")
	})

	t.Run("returns 400 for missing name", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")

		payload := map[string]string{
			"api_server_url":  "https://k8s.example.com",
			"kubeconfig_data": "data",
		}
		body, _ := json.Marshal(payload)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/clusters", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("returns 400 for missing kubeconfig", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")

		payload := map[string]string{
			"name":           "test",
			"api_server_url": "https://k8s.example.com",
		}
		body, _ := json.Marshal(payload)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/clusters", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("non-admin gets 403", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "user")

		payload := CreateClusterRequest{
			Name:           "prod",
			APIServerURL:   "https://k8s.example.com",
			KubeconfigData: "data",
		}
		body, _ := json.Marshal(payload)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/clusters", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusForbidden, w.Code)
	})
}

func TestGetCluster(t *testing.T) {
	t.Parallel()

	t.Run("returns cluster by ID", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		seedCluster(clusterRepo, "cl-1", "production")

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/clusters/cl-1", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var result models.Cluster
		err := json.Unmarshal(w.Body.Bytes(), &result)
		require.NoError(t, err)
		assert.Equal(t, "production", result.Name)
	})

	t.Run("returns 404 for missing cluster", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "user")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/clusters/nonexistent", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

func TestUpdateCluster(t *testing.T) {
	t.Parallel()

	t.Run("updates cluster metadata", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		seedCluster(clusterRepo, "cl-1", "prod")

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")

		payload := UpdateClusterRequest{
			Name:   strPtr("production-v2"),
			Region: strPtr("westus2"),
		}
		body, _ := json.Marshal(payload)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPut, "/api/v1/clusters/cl-1", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var result models.Cluster
		err := json.Unmarshal(w.Body.Bytes(), &result)
		require.NoError(t, err)
		assert.Equal(t, "production-v2", result.Name)
		assert.Equal(t, "westus2", result.Region)
	})

	t.Run("returns 404 for missing cluster", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")

		payload := UpdateClusterRequest{Name: strPtr("new-name")}
		body, _ := json.Marshal(payload)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPut, "/api/v1/clusters/nonexistent", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("invalidates client on kubeconfig update", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		seedCluster(clusterRepo, "cl-1", "prod")

		clientset := fake.NewSimpleClientset()
		k8sClient := k8s.NewClientFromInterface(clientset)
		helmExec := &mockClusterHelmExecutor{}
		reg := cluster.NewRegistryForTest("cl-1", k8sClient, helmExec)

		router := setupClusterRouter(clusterRepo, instanceRepo, reg, "admin")

		payload := UpdateClusterRequest{
			KubeconfigData: strPtr("new-kubeconfig"),
		}
		body, _ := json.Marshal(payload)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPut, "/api/v1/clusters/cl-1", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		// Response should not contain kubeconfig data.
		assert.NotContains(t, w.Body.String(), "new-kubeconfig")
	})

	t.Run("non-admin gets 403", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		seedCluster(clusterRepo, "cl-1", "prod")

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "user")

		payload := UpdateClusterRequest{Name: strPtr("new-name")}
		body, _ := json.Marshal(payload)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPut, "/api/v1/clusters/cl-1", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusForbidden, w.Code)
	})
}

func TestDeleteCluster(t *testing.T) {
	t.Parallel()

	t.Run("deletes cluster with no instances", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		seedCluster(clusterRepo, "cl-1", "prod")

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/clusters/cl-1", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNoContent, w.Code)

		// Verify it's deleted.
		_, err := clusterRepo.FindByID("cl-1")
		assert.Error(t, err)
	})

	t.Run("returns 409 when instances exist", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		seedCluster(clusterRepo, "cl-1", "prod")

		// Add an instance referencing this cluster.
		_ = instanceRepo.Create(&models.StackInstance{
			ID:        "inst-1",
			Name:      "myapp",
			ClusterID: "cl-1",
			OwnerID:   "user1",
			Status:    models.StackStatusStopped,
		})

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/clusters/cl-1", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusConflict, w.Code)

		// Verify cluster still exists.
		_, err := clusterRepo.FindByID("cl-1")
		assert.NoError(t, err)
	})

	t.Run("returns 404 for missing cluster", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/clusters/nonexistent", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("non-admin gets 403", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		seedCluster(clusterRepo, "cl-1", "prod")

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "user")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/clusters/cl-1", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusForbidden, w.Code)
	})
}

func TestTestClusterConnection(t *testing.T) {
	t.Parallel()

	t.Run("successful connectivity test", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		seedCluster(clusterRepo, "cl-1", "prod")

		clientset := fake.NewSimpleClientset()
		k8sClient := k8s.NewClientFromInterface(clientset)
		helmExec := &mockClusterHelmExecutor{}
		reg := cluster.NewRegistryForTest("cl-1", k8sClient, helmExec)

		router := setupClusterRouter(clusterRepo, instanceRepo, reg, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/clusters/cl-1/test", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var result map[string]string
		err := json.Unmarshal(w.Body.Bytes(), &result)
		require.NoError(t, err)
		assert.Equal(t, "Connection successful", result["message"])
	})

	t.Run("returns 404 for missing cluster", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()

		clientset := fake.NewSimpleClientset()
		k8sClient := k8s.NewClientFromInterface(clientset)
		helmExec := &mockClusterHelmExecutor{}
		reg := cluster.NewRegistryForTest("cl-1", k8sClient, helmExec)

		router := setupClusterRouter(clusterRepo, instanceRepo, reg, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/clusters/nonexistent/test", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("returns 503 when registry is nil", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		seedCluster(clusterRepo, "cl-1", "prod")

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/clusters/cl-1/test", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	})

	t.Run("non-admin gets 403", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		seedCluster(clusterRepo, "cl-1", "prod")

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "user")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/clusters/cl-1/test", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusForbidden, w.Code)
	})
}

func TestListClusters_RepoError(t *testing.T) {
	t.Parallel()

	t.Run("returns 500 on repo error", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		clusterRepo.SetError(fmt.Errorf("database connection lost"))

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "user")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})
}

func TestCreateCluster_ErrorPaths(t *testing.T) {
	t.Parallel()

	t.Run("returns 409 on duplicate cluster", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		clusterRepo.SetError(dberrors.NewDatabaseError("Create", dberrors.ErrDuplicateKey))

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")

		payload := CreateClusterRequest{
			Name:           "prod",
			APIServerURL:   "https://k8s.example.com",
			KubeconfigData: "data",
		}
		body, _ := json.Marshal(payload)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/clusters", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusConflict, w.Code)
	})

	t.Run("returns 500 on repo error", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		clusterRepo.SetError(fmt.Errorf("database unavailable"))

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")

		payload := CreateClusterRequest{
			Name:           "prod",
			APIServerURL:   "https://k8s.example.com",
			KubeconfigData: "data",
		}
		body, _ := json.Marshal(payload)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/clusters", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})

	t.Run("returns 400 for missing api_server_url", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")

		payload := map[string]string{
			"name":            "prod",
			"kubeconfig_data": "data",
		}
		body, _ := json.Marshal(payload)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/clusters", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestUpdateCluster_ErrorPaths(t *testing.T) {
	t.Parallel()

	t.Run("returns 400 on invalid JSON", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		seedCluster(clusterRepo, "cl-1", "prod")

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPut, "/api/v1/clusters/cl-1", bytes.NewReader([]byte("{invalid")))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("returns 500 on repo update error", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		seedCluster(clusterRepo, "cl-1", "prod")

		// Set error after seed — FindByID uses fetchErr (nil), Update uses err.
		clusterRepo.SetError(fmt.Errorf("database write failed"))

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")

		payload := UpdateClusterRequest{Name: strPtr("new-name")}
		body, _ := json.Marshal(payload)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPut, "/api/v1/clusters/cl-1", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})

	t.Run("kubeconfig not in response even after update", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		seedCluster(clusterRepo, "cl-1", "prod")

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")

		payload := UpdateClusterRequest{
			KubeconfigData: strPtr("super-secret-kubeconfig-value"),
		}
		body, _ := json.Marshal(payload)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPut, "/api/v1/clusters/cl-1", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.NotContains(t, w.Body.String(), "super-secret-kubeconfig-value")
		assert.NotContains(t, w.Body.String(), "kubeconfig_data")
		assert.NotContains(t, w.Body.String(), "kubeconfig_path")
	})
}

func TestDeleteCluster_ErrorPaths(t *testing.T) {
	t.Parallel()

	t.Run("returns 500 on instance check error", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		seedCluster(clusterRepo, "cl-1", "prod")

		// instanceRepo.FindByCluster uses m.err.
		instanceRepo.SetError(fmt.Errorf("instance repo unavailable"))

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/clusters/cl-1", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)

		// Cluster should still exist.
		_, err := clusterRepo.FindByID("cl-1")
		assert.NoError(t, err)
	})

	t.Run("returns 500 on repo delete error", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		seedCluster(clusterRepo, "cl-1", "prod")

		// Set general error on clusterRepo — Delete uses m.err, FindByID uses m.fetchErr.
		clusterRepo.SetError(fmt.Errorf("database write failed"))

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/clusters/cl-1", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})

	t.Run("invalidates client on successful delete", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		seedCluster(clusterRepo, "cl-1", "prod")

		clientset := fake.NewSimpleClientset()
		k8sClient := k8s.NewClientFromInterface(clientset)
		helmExec := &mockClusterHelmExecutor{}
		reg := cluster.NewRegistryForTest("cl-1", k8sClient, helmExec)

		router := setupClusterRouter(clusterRepo, instanceRepo, reg, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/clusters/cl-1", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNoContent, w.Code)

		// Verify cluster was deleted from repository.
		_, err := clusterRepo.FindByID("cl-1")
		assert.Error(t, err)
	})
}

func TestGetCluster_Security(t *testing.T) {
	t.Parallel()

	t.Run("kubeconfig fields never in response", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()

		cl := &models.Cluster{
			ID:             "cl-sec",
			Name:           "secret-cluster",
			APIServerURL:   "https://k8s.example.com",
			KubeconfigData: "super-secret-admin-kubeconfig",
			KubeconfigPath: "/root/.kube/admin-config",
			Region:         "eastus",
			HealthStatus:   models.ClusterHealthy,
		}
		_ = clusterRepo.Create(cl)

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/clusters/cl-sec", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		body := w.Body.String()
		assert.NotContains(t, body, "super-secret-admin-kubeconfig")
		assert.NotContains(t, body, "/root/.kube/admin-config")
		assert.NotContains(t, body, "kubeconfig_data")
		assert.NotContains(t, body, "kubeconfig_path")
		// Ensure normal fields are present.
		assert.Contains(t, body, "secret-cluster")
		assert.Contains(t, body, "eastus")
	})
}

func TestListClusters_FieldRestriction(t *testing.T) {
	t.Parallel()

	seedRepo := func() *MockClusterRepository {
		repo := NewMockClusterRepository()
		_ = repo.Create(&models.Cluster{
			ID:           "cl-1",
			Name:         "production",
			APIServerURL: "https://k8s.prod.example.com",
			Region:       "eastus",
			HealthStatus: models.ClusterHealthy,
			IsDefault:    true,
		})
		return repo
	}

	tests := []struct {
		name            string
		role            string
		expectFullField bool
	}{
		{"admin gets full details", "admin", true},
		{"devops gets full details", "devops", true},
		{"user gets summary only", "user", false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			clusterRepo := seedRepo()
			instanceRepo := NewMockStackInstanceRepository()
			router := setupClusterRouter(clusterRepo, instanceRepo, nil, tt.role)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.Code)

			if tt.expectFullField {
				var clusters []map[string]interface{}
				err := json.Unmarshal(w.Body.Bytes(), &clusters)
				require.NoError(t, err)
				require.Len(t, clusters, 1)
				assert.Equal(t, "cl-1", clusters[0]["id"])
				assert.Equal(t, "production", clusters[0]["name"])
				// Check fields exist that would be stripped for non-privileged
				assert.Contains(t, clusters[0], "api_server_url")
				assert.Contains(t, clusters[0], "health_status")
				assert.Contains(t, clusters[0], "region")
			} else {
				var summaries []ClusterSummary
				err := json.Unmarshal(w.Body.Bytes(), &summaries)
				require.NoError(t, err)
				require.Len(t, summaries, 1)
				assert.Equal(t, "cl-1", summaries[0].ID)
				assert.Equal(t, "production", summaries[0].Name)
				assert.True(t, summaries[0].IsDefault)
			}
		})
	}
}

func TestGetCluster_FieldRestriction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		role            string
		expectFullField bool
	}{
		{"admin gets full details", "admin", true},
		{"devops gets full details", "devops", true},
		{"user gets summary only", "user", false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			clusterRepo := NewMockClusterRepository()
			instanceRepo := NewMockStackInstanceRepository()
			_ = clusterRepo.Create(&models.Cluster{
				ID:           "cl-1",
				Name:         "production",
				APIServerURL: "https://k8s.prod.example.com",
				Region:       "eastus",
				HealthStatus: models.ClusterHealthy,
				IsDefault:    true,
			})
			router := setupClusterRouter(clusterRepo, instanceRepo, nil, tt.role)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/api/v1/clusters/cl-1", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.Code)

			if tt.expectFullField {
				var cl map[string]interface{}
				err := json.Unmarshal(w.Body.Bytes(), &cl)
				require.NoError(t, err)
				assert.Equal(t, "cl-1", cl["id"])
				assert.Equal(t, "production", cl["name"])
				// Check fields exist that would be stripped for non-privileged
				assert.Contains(t, cl, "api_server_url")
				assert.Contains(t, cl, "health_status")
				assert.Contains(t, cl, "region")
			} else {
				var summary ClusterSummary
				err := json.Unmarshal(w.Body.Bytes(), &summary)
				require.NoError(t, err)
				assert.Equal(t, "cl-1", summary.ID)
				assert.Equal(t, "production", summary.Name)
				assert.True(t, summary.IsDefault)
			}
		})
	}
}

func TestTestClusterConnection_ErrorPaths(t *testing.T) {
	t.Parallel()

	t.Run("returns 502 when registry fails to build client", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		seedCluster(clusterRepo, "cl-1", "prod")

		// Create a registry whose cluster repo doesn't have cl-1,
		// so GetK8sClient will fail to build.
		emptyClusterRepo := NewMockClusterRepository()
		reg := cluster.NewRegistry(cluster.RegistryConfig{
			ClusterRepo: emptyClusterRepo,
			HelmBinary:  "helm",
			HelmTimeout: 5 * time.Minute,
		})

		router := setupClusterRouter(clusterRepo, instanceRepo, reg, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/clusters/cl-1/test", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadGateway, w.Code)
		assert.Contains(t, w.Body.String(), "Failed to connect to cluster")
	})
}

func TestSetDefaultCluster(t *testing.T) {
	t.Parallel()

	t.Run("sets default cluster", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		seedCluster(clusterRepo, "cl-1", "prod")

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/clusters/cl-1/default", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var result map[string]string
		err := json.Unmarshal(w.Body.Bytes(), &result)
		require.NoError(t, err)
		assert.Equal(t, "Default cluster updated", result["message"])

		// Verify the default was set in the repo.
		def, err := clusterRepo.FindDefault()
		require.NoError(t, err)
		assert.Equal(t, "cl-1", def.ID)
	})

	t.Run("returns 404 for missing cluster", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/clusters/nonexistent/default", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("non-admin gets 403", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		seedCluster(clusterRepo, "cl-1", "prod")

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "user")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/clusters/cl-1/default", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusForbidden, w.Code)
	})
}

// --- Cluster Health Dashboard Handler Tests ---

// makeTestNode creates a corev1.Node for handler tests.
func makeTestNode(name string, ready bool) *corev1.Node {
	readyStatus := corev1.ConditionFalse
	if ready {
		readyStatus = corev1.ConditionTrue
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: readyStatus},
			},
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4"),
				corev1.ResourceMemory: resource.MustParse("16Gi"),
				corev1.ResourcePods:   resource.MustParse("110"),
			},
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4"),
				corev1.ResourceMemory: resource.MustParse("16Gi"),
				corev1.ResourcePods:   resource.MustParse("110"),
			},
		},
	}
}

func TestGetClusterHealthSummary(t *testing.T) {
	t.Parallel()

	t.Run("returns summary for valid cluster", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		seedCluster(clusterRepo, "cl-1", "prod")

		fakeCS := fake.NewSimpleClientset(
			makeTestNode("node-1", true),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-app1"}},
		)
		k8sClient := k8s.NewClientFromInterface(fakeCS)
		registry := cluster.NewRegistryForTest("cl-1", k8sClient, &mockClusterHelmExecutor{})

		router := setupClusterRouter(clusterRepo, instanceRepo, registry, "devops")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/clusters/cl-1/health/summary", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var result k8s.ClusterSummary
		err := json.Unmarshal(w.Body.Bytes(), &result)
		require.NoError(t, err)
		assert.Equal(t, 1, result.NodeCount)
		assert.Equal(t, 1, result.ReadyNodeCount)
		assert.Equal(t, 1, result.NamespaceCount)
	})

	t.Run("returns 404 for unknown cluster", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/clusters/missing/health/summary", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("returns 500 when registry is nil", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		seedCluster(clusterRepo, "cl-1", "prod")

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/clusters/cl-1/health/summary", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})

	t.Run("regular user gets 403", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		seedCluster(clusterRepo, "cl-1", "prod")

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "user")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/clusters/cl-1/health/summary", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusForbidden, w.Code)
	})
}

func TestGetClusterNodes(t *testing.T) {
	t.Parallel()

	t.Run("returns node statuses", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		seedCluster(clusterRepo, "cl-1", "prod")

		fakeCS := fake.NewSimpleClientset(
			makeTestNode("node-1", true),
			makeTestNode("node-2", false),
		)
		k8sClient := k8s.NewClientFromInterface(fakeCS)
		registry := cluster.NewRegistryForTest("cl-1", k8sClient, &mockClusterHelmExecutor{})

		router := setupClusterRouter(clusterRepo, instanceRepo, registry, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/clusters/cl-1/health/nodes", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var result []k8s.NodeStatus
		err := json.Unmarshal(w.Body.Bytes(), &result)
		require.NoError(t, err)
		assert.Len(t, result, 2)
	})

	t.Run("returns 404 for unknown cluster", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/clusters/missing/health/nodes", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("returns 500 when registry is nil", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		seedCluster(clusterRepo, "cl-1", "prod")

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/clusters/cl-1/health/nodes", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})

	t.Run("regular user gets 403", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		seedCluster(clusterRepo, "cl-1", "prod")

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "user")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/clusters/cl-1/health/nodes", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusForbidden, w.Code)
	})
}

func TestGetClusterNamespaces(t *testing.T) {
	t.Parallel()

	t.Run("returns stack namespaces", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		seedCluster(clusterRepo, "cl-1", "prod")

		fakeCS := fake.NewSimpleClientset(
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-web"}},
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-api"}},
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}},
		)
		k8sClient := k8s.NewClientFromInterface(fakeCS)
		registry := cluster.NewRegistryForTest("cl-1", k8sClient, &mockClusterHelmExecutor{})

		router := setupClusterRouter(clusterRepo, instanceRepo, registry, "devops")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/clusters/cl-1/namespaces", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var result []k8s.NamespaceInfo
		err := json.Unmarshal(w.Body.Bytes(), &result)
		require.NoError(t, err)
		assert.Len(t, result, 2) // only stack-* namespaces
	})

	t.Run("returns 404 for unknown cluster", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/clusters/missing/namespaces", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("returns 500 when registry is nil", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		seedCluster(clusterRepo, "cl-1", "prod")

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/clusters/cl-1/namespaces", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})

	t.Run("regular user gets 403", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		instanceRepo := NewMockStackInstanceRepository()
		seedCluster(clusterRepo, "cl-1", "prod")

		router := setupClusterRouter(clusterRepo, instanceRepo, nil, "user")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/clusters/cl-1/namespaces", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusForbidden, w.Code)
	})
}

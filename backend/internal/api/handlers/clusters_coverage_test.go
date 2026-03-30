package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/internal/api/middleware"
	"backend/internal/cluster"
	"backend/internal/k8s"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// setupClusterRouterWithQuotas creates a gin engine with all cluster routes
// including quota and utilization endpoints.
func setupClusterRouterWithQuotas(
	clusterRepo *MockClusterRepository,
	instanceRepo *MockStackInstanceRepository,
	registry *cluster.Registry,
	quotaRepo models.ResourceQuotaRepository,
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

		clusters.GET("/:id/quotas", h.GetQuotas)
		clusters.PUT("/:id/quotas", adminMW, h.UpdateQuotas)
		clusters.DELETE("/:id/quotas", adminMW, h.DeleteQuotas)
		clusters.GET("/:id/utilization", devopsMW, h.GetUtilization)
	}
	return r
}

// ---- CreateCluster additional coverage ----

func TestCreateCluster_ValidationError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		body           interface{}
		expectedStatus int
		expectedError  string
	}{
		{
			name:           "invalid JSON",
			body:           "{bad json",
			expectedStatus: http.StatusBadRequest,
			expectedError:  msgInvalidRequestFormat,
		},
		{
			name: "missing required fields",
			body: CreateClusterRequest{
				Description: "no name or url",
			},
			expectedStatus: http.StatusBadRequest,
			expectedError:  msgInvalidRequestFormat,
		},
		{
			name: "no kubeconfig",
			body: CreateClusterRequest{
				Name:         "my-cluster",
				APIServerURL: "https://k8s.example.com",
			},
			expectedStatus: http.StatusBadRequest,
			expectedError:  "kubeconfig",
		},
		{
			name: "both kubeconfig data and path",
			body: CreateClusterRequest{
				Name:           "my-cluster",
				APIServerURL:   "https://k8s.example.com",
				KubeconfigData: "data",
				KubeconfigPath: "/path",
			},
			expectedStatus: http.StatusBadRequest,
			expectedError:  "only one of kubeconfig",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			clusterRepo := NewMockClusterRepository()
			instanceRepo := NewMockStackInstanceRepository()
			r := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")

			var bodyBytes []byte
			switch v := tt.body.(type) {
			case string:
				bodyBytes = []byte(v)
			default:
				bodyBytes, _ = json.Marshal(v)
			}
			w := httptest.NewRecorder()
			req, _ := http.NewRequest("POST", "/api/v1/clusters", bytes.NewBuffer(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
			var resp map[string]string
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Contains(t, resp["error"], tt.expectedError)
		})
	}
}

func TestCreateCluster_SetDefaultError(t *testing.T) {
	t.Parallel()

	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	// Pre-seed a default cluster so auto-default doesn't trigger.
	existing := &models.Cluster{
		ID: "existing-1", Name: "existing", KubeconfigPath: "/path",
		IsDefault: true, HealthStatus: models.ClusterHealthy,
	}
	_ = clusterRepo.Create(existing)
	// Set it as default.
	_ = clusterRepo.SetDefault("existing-1")

	r := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")

	body, _ := json.Marshal(CreateClusterRequest{
		Name:           "new-cluster",
		APIServerURL:   "https://k8s.example.com",
		KubeconfigPath: "/new/path",
		IsDefault:      true,
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/clusters", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	// Should succeed — either 201 with is_default: true, or with a warning header.
	assert.Equal(t, http.StatusCreated, w.Code)
}

func TestCreateCluster_AutoDefault(t *testing.T) {
	t.Parallel()

	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	// No existing clusters → should auto-set as default.
	r := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")

	body, _ := json.Marshal(CreateClusterRequest{
		Name:           "first-cluster",
		APIServerURL:   "https://k8s.example.com",
		KubeconfigPath: "/path",
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/clusters", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)
	var resp models.Cluster
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.IsDefault)
}

// ---- UpdateCluster additional coverage ----

func TestUpdateCluster_FieldUpdates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		update         UpdateClusterRequest
		expectedStatus int
		check          func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name: "update kubeconfig data clears path",
			update: UpdateClusterRequest{
				KubeconfigData: strPtr("new-kubeconfig-data"),
			},
			expectedStatus: http.StatusOK,
			check: func(t *testing.T, w *httptest.ResponseRecorder) {
				t.Helper()
				var resp models.Cluster
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				// Kubeconfig should be cleared from response.
				assert.Empty(t, resp.KubeconfigData)
				assert.Empty(t, resp.KubeconfigPath)
			},
		},
		{
			name: "update kubeconfig path clears data",
			update: UpdateClusterRequest{
				KubeconfigPath: strPtr("/new/path"),
			},
			expectedStatus: http.StatusOK,
			check: func(t *testing.T, w *httptest.ResponseRecorder) {
				t.Helper()
				var resp models.Cluster
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Empty(t, resp.KubeconfigData)
				assert.Empty(t, resp.KubeconfigPath)
			},
		},
		{
			name: "update region and limits",
			update: UpdateClusterRequest{
				Region:              strPtr("westus"),
				MaxNamespaces:       intPtr(100),
				MaxInstancesPerUser: intPtr(10),
			},
			expectedStatus: http.StatusOK,
			check: func(t *testing.T, w *httptest.ResponseRecorder) {
				t.Helper()
				var resp models.Cluster
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, "westus", resp.Region)
				assert.Equal(t, 100, resp.MaxNamespaces)
				assert.Equal(t, 10, resp.MaxInstancesPerUser)
			},
		},
		{
			name: "cannot unset default",
			update: UpdateClusterRequest{
				IsDefault: boolPtr(false),
			},
			expectedStatus: http.StatusBadRequest,
			check: func(t *testing.T, w *httptest.ResponseRecorder) {
				t.Helper()
				var resp map[string]interface{}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Contains(t, resp["error"], "Cannot unset default")
			},
		},
		{
			name: "set as default via update",
			update: UpdateClusterRequest{
				IsDefault: boolPtr(true),
			},
			expectedStatus: http.StatusOK,
			check: func(t *testing.T, w *httptest.ResponseRecorder) {
				t.Helper()
				var resp models.Cluster
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.True(t, resp.IsDefault)
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			clusterRepo := NewMockClusterRepository()
			instanceRepo := NewMockStackInstanceRepository()
			cl := seedCluster(clusterRepo, "cl-1", "test-cluster")

			// Mark as default for the "cannot unset" and "set as default" tests.
			if tt.update.IsDefault != nil {
				_ = clusterRepo.SetDefault(cl.ID)
				// Update the stored cluster to reflect default status since the mock
				// SetDefault only records the ID, not the IsDefault flag on the model.
				stored, _ := clusterRepo.FindByID(cl.ID)
				stored.IsDefault = true
				_ = clusterRepo.Update(stored)
			}

			r := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")
			body, _ := json.Marshal(tt.update)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest("PUT", "/api/v1/clusters/cl-1", bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
			if tt.check != nil {
				tt.check(t, w)
			}
		})
	}
}

func TestUpdateCluster_NotFound(t *testing.T) {
	t.Parallel()
	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	r := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")

	body, _ := json.Marshal(UpdateClusterRequest{Name: strPtr("updated")})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PUT", "/api/v1/clusters/nonexistent", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestUpdateCluster_InvalidJSON(t *testing.T) {
	t.Parallel()
	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	seedCluster(clusterRepo, "cl-1", "test-cluster")
	r := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PUT", "/api/v1/clusters/cl-1", bytes.NewBufferString("{invalid"))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ---- TestClusterConnection additional coverage ----

func TestTestClusterConnection_RegistryNil(t *testing.T) {
	t.Parallel()
	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	seedCluster(clusterRepo, "cl-1", "test-cluster")
	r := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/clusters/cl-1/test", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestTestClusterConnection_ClusterNotFound(t *testing.T) {
	t.Parallel()
	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	r := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/clusters/nonexistent/test", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestTestClusterConnection_Success(t *testing.T) {
	t.Parallel()
	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	seedCluster(clusterRepo, "cl-1", "test-cluster")

	fakeCS := fake.NewSimpleClientset()
	k8sClient := k8s.NewClientFromInterface(fakeCS)
	registry := cluster.NewRegistryForTest("cl-1", k8sClient, &mockClusterHelmExecutor{})

	r := setupClusterRouter(clusterRepo, instanceRepo, registry, "admin")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/clusters/cl-1/test", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "success", resp["status"])
}

// ---- GetClusterHealthSummary additional coverage ----

func TestGetClusterHealthSummary_ClusterNotFound(t *testing.T) {
	t.Parallel()
	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	r := setupClusterRouter(clusterRepo, instanceRepo, nil, "devops")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/clusters/nonexistent/health/summary", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestGetClusterHealthSummary_RegistryNil(t *testing.T) {
	t.Parallel()
	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	seedCluster(clusterRepo, "cl-1", "test-cluster")
	r := setupClusterRouter(clusterRepo, instanceRepo, nil, "devops")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/clusters/cl-1/health/summary", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestGetClusterHealthSummary_Success(t *testing.T) {
	t.Parallel()
	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	seedCluster(clusterRepo, "cl-1", "test-cluster")

	fakeCS := fake.NewSimpleClientset(
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
			Status: corev1.NodeStatus{
				Conditions: []corev1.NodeCondition{
					{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
				},
			},
		},
	)
	k8sClient := k8s.NewClientFromInterface(fakeCS)
	registry := cluster.NewRegistryForTest("cl-1", k8sClient, &mockClusterHelmExecutor{})
	r := setupClusterRouter(clusterRepo, instanceRepo, registry, "devops")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/clusters/cl-1/health/summary", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// ---- GetClusterNodes additional coverage ----

func TestGetClusterNodes_ClusterNotFound(t *testing.T) {
	t.Parallel()
	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	r := setupClusterRouter(clusterRepo, instanceRepo, nil, "devops")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/clusters/nonexistent/health/nodes", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestGetClusterNodes_RegistryNil(t *testing.T) {
	t.Parallel()
	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	seedCluster(clusterRepo, "cl-1", "test-cluster")
	r := setupClusterRouter(clusterRepo, instanceRepo, nil, "devops")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/clusters/cl-1/health/nodes", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestGetClusterNodes_Success(t *testing.T) {
	t.Parallel()
	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	seedCluster(clusterRepo, "cl-1", "test-cluster")

	fakeCS := fake.NewSimpleClientset(
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
			Status: corev1.NodeStatus{
				Conditions: []corev1.NodeCondition{
					{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
				},
			},
		},
	)
	k8sClient := k8s.NewClientFromInterface(fakeCS)
	registry := cluster.NewRegistryForTest("cl-1", k8sClient, &mockClusterHelmExecutor{})
	r := setupClusterRouter(clusterRepo, instanceRepo, registry, "devops")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/clusters/cl-1/health/nodes", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// ---- GetClusterNamespaces additional coverage ----

func TestGetClusterNamespaces_ClusterNotFound(t *testing.T) {
	t.Parallel()
	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	r := setupClusterRouter(clusterRepo, instanceRepo, nil, "devops")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/clusters/nonexistent/namespaces", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestGetClusterNamespaces_RegistryNil(t *testing.T) {
	t.Parallel()
	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	seedCluster(clusterRepo, "cl-1", "test-cluster")
	r := setupClusterRouter(clusterRepo, instanceRepo, nil, "devops")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/clusters/cl-1/namespaces", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestGetClusterNamespaces_Success(t *testing.T) {
	t.Parallel()
	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	seedCluster(clusterRepo, "cl-1", "test-cluster")

	fakeCS := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-myapp-user1"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}},
	)
	k8sClient := k8s.NewClientFromInterface(fakeCS)
	registry := cluster.NewRegistryForTest("cl-1", k8sClient, &mockClusterHelmExecutor{})
	r := setupClusterRouter(clusterRepo, instanceRepo, registry, "devops")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/clusters/cl-1/namespaces", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// ---- UpdateQuotas additional coverage ----

func TestUpdateQuotas_ClusterNotFound(t *testing.T) {
	t.Parallel()
	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	quotaRepo := NewMockResourceQuotaRepository()
	r := setupClusterRouterWithQuotas(clusterRepo, instanceRepo, nil, quotaRepo, "admin")

	body, _ := json.Marshal(map[string]string{"cpu_limit": "4"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PUT", "/api/v1/clusters/nonexistent/quotas", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestUpdateQuotas_QuotaRepoNil(t *testing.T) {
	t.Parallel()
	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	seedCluster(clusterRepo, "cl-1", "test-cluster")
	// Use setup without quota repo
	r := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")
	// Add quota routes manually without quotaRepo set.
	// Since the existing setupClusterRouter doesn't have quota routes,
	// use setupClusterRouterWithQuotas with nil quotaRepo.
	r = setupClusterRouterWithQuotas(clusterRepo, instanceRepo, nil, nil, "admin")
	seedCluster(clusterRepo, "cl-2", "test-cluster-2") // re-seed after re-creating repo

	body, _ := json.Marshal(map[string]string{"cpu_limit": "4"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PUT", "/api/v1/clusters/cl-2/quotas", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestUpdateQuotas_InvalidJSON(t *testing.T) {
	t.Parallel()
	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	quotaRepo := NewMockResourceQuotaRepository()
	seedCluster(clusterRepo, "cl-1", "test-cluster")
	r := setupClusterRouterWithQuotas(clusterRepo, instanceRepo, nil, quotaRepo, "admin")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PUT", "/api/v1/clusters/cl-1/quotas", bytes.NewBufferString("{bad"))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestUpdateQuotas_Success(t *testing.T) {
	t.Parallel()
	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	quotaRepo := NewMockResourceQuotaRepository()
	seedCluster(clusterRepo, "cl-1", "test-cluster")
	r := setupClusterRouterWithQuotas(clusterRepo, instanceRepo, nil, quotaRepo, "admin")

	body, _ := json.Marshal(map[string]string{"cpu_limit": "4", "memory_limit": "8Gi"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PUT", "/api/v1/clusters/cl-1/quotas", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// ---- DeleteQuotas additional coverage ----

func TestDeleteQuotas_ClusterNotFound(t *testing.T) {
	t.Parallel()
	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	quotaRepo := NewMockResourceQuotaRepository()
	r := setupClusterRouterWithQuotas(clusterRepo, instanceRepo, nil, quotaRepo, "admin")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("DELETE", "/api/v1/clusters/nonexistent/quotas", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestDeleteQuotas_QuotaRepoNil(t *testing.T) {
	t.Parallel()
	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	seedCluster(clusterRepo, "cl-1", "test-cluster")
	r := setupClusterRouterWithQuotas(clusterRepo, instanceRepo, nil, nil, "admin")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("DELETE", "/api/v1/clusters/cl-1/quotas", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ---- GetUtilization additional coverage ----

func TestGetUtilization_ClusterNotFound(t *testing.T) {
	t.Parallel()
	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	quotaRepo := NewMockResourceQuotaRepository()
	r := setupClusterRouterWithQuotas(clusterRepo, instanceRepo, nil, quotaRepo, "devops")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/clusters/nonexistent/utilization", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestGetUtilization_RegistryNil(t *testing.T) {
	t.Parallel()
	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	quotaRepo := NewMockResourceQuotaRepository()
	seedCluster(clusterRepo, "cl-1", "test-cluster")
	r := setupClusterRouterWithQuotas(clusterRepo, instanceRepo, nil, quotaRepo, "devops")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/clusters/cl-1/utilization", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestGetUtilization_Success(t *testing.T) {
	t.Parallel()
	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	quotaRepo := NewMockResourceQuotaRepository()
	seedCluster(clusterRepo, "cl-1", "test-cluster")

	fakeCS := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-app1-user1"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-app2-user2"}},
	)
	k8sClient := k8s.NewClientFromInterface(fakeCS)
	registry := cluster.NewRegistryForTest("cl-1", k8sClient, &mockClusterHelmExecutor{})
	r := setupClusterRouterWithQuotas(clusterRepo, instanceRepo, registry, quotaRepo, "devops")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/clusters/cl-1/utilization", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp ClusterUtilization
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "cl-1", resp.ClusterID)
	assert.Len(t, resp.Namespaces, 2)
}

// ---- propagateQuotasToNamespaces coverage ----

func TestPropagateQuotasToNamespaces_WithNamespaces(t *testing.T) {
	t.Parallel()

	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	quotaRepo := NewMockResourceQuotaRepository()

	fakeCS := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-app1-user1"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-app2-user2"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}},
	)
	k8sClient := k8s.NewClientFromInterface(fakeCS)
	registry := cluster.NewRegistryForTest("cl-1", k8sClient, &mockClusterHelmExecutor{})

	h := NewClusterHandlerWithQuotas(clusterRepo, registry, instanceRepo, quotaRepo)

	config := &models.ResourceQuotaConfig{
		ClusterID: "cl-1",
		CPULimit:  "4",
	}

	// Should not panic; exercises the namespace iteration and quota application.
	h.propagateQuotasToNamespaces("cl-1", config)
}

func TestPropagateQuotasToNamespaces_NoNamespaces(t *testing.T) {
	t.Parallel()

	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	quotaRepo := NewMockResourceQuotaRepository()

	// No stack namespaces — only kube-system.
	fakeCS := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}},
	)
	k8sClient := k8s.NewClientFromInterface(fakeCS)
	registry := cluster.NewRegistryForTest("cl-1", k8sClient, &mockClusterHelmExecutor{})

	h := NewClusterHandlerWithQuotas(clusterRepo, registry, instanceRepo, quotaRepo)

	config := &models.ResourceQuotaConfig{
		ClusterID: "cl-1",
		CPULimit:  "4",
	}

	// Should not panic; no stack- namespaces to iterate.
	h.propagateQuotasToNamespaces("cl-1", config)
}

// ---- SetDefaultCluster additional coverage ----

func TestSetDefaultCluster_NotFound(t *testing.T) {
	t.Parallel()
	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	r := setupClusterRouter(clusterRepo, instanceRepo, nil, "admin")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/clusters/nonexistent/default", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestSetDefaultCluster_WithRegistry(t *testing.T) {
	t.Parallel()
	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	seedCluster(clusterRepo, "cl-1", "test-cluster")

	fakeCS := fake.NewSimpleClientset()
	k8sClient := k8s.NewClientFromInterface(fakeCS)
	registry := cluster.NewRegistryForTest("cl-1", k8sClient, &mockClusterHelmExecutor{})
	r := setupClusterRouter(clusterRepo, instanceRepo, registry, "admin")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/clusters/cl-1/default", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// ---- UpdateQuotas validation error ----

func TestUpdateQuotas_ValidationError(t *testing.T) {
	t.Parallel()
	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	quotaRepo := NewMockResourceQuotaRepository()
	seedCluster(clusterRepo, "cl-1", "test-cluster")
	r := setupClusterRouterWithQuotas(clusterRepo, instanceRepo, nil, quotaRepo, "admin")

	// pod_limit < 0 triggers validation error
	body, _ := json.Marshal(UpdateQuotaRequest{PodLimit: -1})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PUT", "/api/v1/clusters/cl-1/quotas", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ---- GetQuotas additional coverage ----

func TestGetQuotas_ClusterNotFound(t *testing.T) {
	t.Parallel()
	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	quotaRepo := NewMockResourceQuotaRepository()
	r := setupClusterRouterWithQuotas(clusterRepo, instanceRepo, nil, quotaRepo, "devops")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/clusters/nonexistent/quotas", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestGetQuotas_QuotaRepoNil(t *testing.T) {
	t.Parallel()
	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	seedCluster(clusterRepo, "cl-1", "test-cluster")
	r := setupClusterRouterWithQuotas(clusterRepo, instanceRepo, nil, nil, "devops")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/clusters/cl-1/quotas", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestGetQuotas_Success(t *testing.T) {
	t.Parallel()
	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	quotaRepo := NewMockResourceQuotaRepository()
	seedCluster(clusterRepo, "cl-1", "test-cluster")

	// Pre-seed a quota config
	quotaRepo.Upsert(nil, &models.ResourceQuotaConfig{
		ClusterID: "cl-1",
		CPULimit:  "4",
	})

	r := setupClusterRouterWithQuotas(clusterRepo, instanceRepo, nil, quotaRepo, "devops")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/clusters/cl-1/quotas", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestGetQuotas_NotConfigured(t *testing.T) {
	t.Parallel()
	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	quotaRepo := NewMockResourceQuotaRepository()
	seedCluster(clusterRepo, "cl-1", "test-cluster")
	r := setupClusterRouterWithQuotas(clusterRepo, instanceRepo, nil, quotaRepo, "devops")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/clusters/cl-1/quotas", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// ---- DeleteQuotas success ----

func TestDeleteQuotas_Success(t *testing.T) {
	t.Parallel()
	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	quotaRepo := NewMockResourceQuotaRepository()
	seedCluster(clusterRepo, "cl-1", "test-cluster")

	quotaRepo.Upsert(nil, &models.ResourceQuotaConfig{
		ClusterID: "cl-1",
		CPULimit:  "4",
	})

	r := setupClusterRouterWithQuotas(clusterRepo, instanceRepo, nil, quotaRepo, "admin")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("DELETE", "/api/v1/clusters/cl-1/quotas", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)
}

// ---- UpdateQuotas with registry triggers propagation ----

func TestUpdateQuotas_WithRegistryTriggersPropagation(t *testing.T) {
	t.Parallel()
	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	quotaRepo := NewMockResourceQuotaRepository()
	seedCluster(clusterRepo, "cl-1", "test-cluster")

	fakeCS := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-app1-user1"}},
	)
	k8sClient := k8s.NewClientFromInterface(fakeCS)
	registry := cluster.NewRegistryForTest("cl-1", k8sClient, &mockClusterHelmExecutor{})
	r := setupClusterRouterWithQuotas(clusterRepo, instanceRepo, registry, quotaRepo, "admin")

	body, _ := json.Marshal(map[string]interface{}{"cpu_limit": "4", "memory_limit": "8Gi", "pod_limit": 10})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PUT", "/api/v1/clusters/cl-1/quotas", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// helper
func boolPtr(b bool) *bool { return &b }

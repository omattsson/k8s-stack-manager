package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/deployer"
	"backend/internal/k8s"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// mockAdminHelmExecutor implements deployer.HelmExecutor for admin tests.
type mockAdminHelmExecutor struct {
	listReleasesFunc func(ctx context.Context, namespace string) ([]string, error)
	uninstallFunc    func(ctx context.Context, req deployer.UninstallRequest) (string, error)
	uninstallCalls   []deployer.UninstallRequest
}

func (m *mockAdminHelmExecutor) Install(_ context.Context, _ deployer.InstallRequest) (string, error) {
	return "", nil
}

func (m *mockAdminHelmExecutor) Uninstall(ctx context.Context, req deployer.UninstallRequest) (string, error) {
	m.uninstallCalls = append(m.uninstallCalls, req)
	if m.uninstallFunc != nil {
		return m.uninstallFunc(ctx, req)
	}
	return "uninstalled " + req.ReleaseName, nil
}

func (m *mockAdminHelmExecutor) Status(_ context.Context, name, _ string) (*deployer.ReleaseStatus, error) {
	return &deployer.ReleaseStatus{Name: name}, nil
}

func (m *mockAdminHelmExecutor) ListReleases(ctx context.Context, namespace string) ([]string, error) {
	if m.listReleasesFunc != nil {
		return m.listReleasesFunc(ctx, namespace)
	}
	return []string{}, nil
}

func (m *mockAdminHelmExecutor) Timeout() time.Duration {
	return 30 * time.Second
}

// setupAdminRouter creates a gin engine wired to AdminHandler routes with admin auth.
func setupAdminRouter(
	k8sClient *k8s.Client,
	helmExec deployer.HelmExecutor,
	instanceRepo *MockStackInstanceRepository,
	callerRole string,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(injectAuthContext("admin-user-id", callerRole))
	h := NewAdminHandler(k8sClient, helmExec, instanceRepo)
	adminMW := middleware.RequireAdmin()
	admin := r.Group("/api/v1/admin")
	admin.Use(adminMW)
	{
		admin.GET("/orphaned-namespaces", h.ListOrphanedNamespaces)
		admin.DELETE("/orphaned-namespaces/:namespace", h.DeleteOrphanedNamespace)
	}
	return r
}

func TestListOrphanedNamespaces(t *testing.T) {
	t.Parallel()

	t.Run("returns empty list when no stack namespaces exist", func(t *testing.T) {
		t.Parallel()
		clientset := fake.NewSimpleClientset()
		k8sClient := k8s.NewClientFromInterface(clientset)
		instanceRepo := NewMockStackInstanceRepository()
		helmExec := &mockAdminHelmExecutor{}

		router := setupAdminRouter(k8sClient, helmExec, instanceRepo, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/admin/orphaned-namespaces", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var result []OrphanedNamespaceResponse
		err := json.Unmarshal(w.Body.Bytes(), &result)
		require.NoError(t, err)
		assert.Empty(t, result)
	})

	t.Run("identifies orphaned namespaces", func(t *testing.T) {
		t.Parallel()
		clientset := fake.NewSimpleClientset()
		ctx := context.Background()

		// Create two stack namespaces: one with a matching instance, one without.
		_, err := clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "stack-myapp-alice"},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		_, err = clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "stack-orphan-bob"},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		// Also create a non-stack namespace that should be ignored.
		_, err = clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "default"},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		k8sClient := k8s.NewClientFromInterface(clientset)
		instanceRepo := NewMockStackInstanceRepository()
		// Add an instance matching the first namespace.
		_ = instanceRepo.Create(&models.StackInstance{
			ID:        "inst-1",
			Name:      "myapp",
			Namespace: "stack-myapp-alice",
			OwnerID:   "alice",
			Status:    models.StackStatusRunning,
		})

		helmExec := &mockAdminHelmExecutor{
			listReleasesFunc: func(_ context.Context, ns string) ([]string, error) {
				if ns == "stack-orphan-bob" {
					return []string{"nginx", "redis"}, nil
				}
				return []string{}, nil
			},
		}

		router := setupAdminRouter(k8sClient, helmExec, instanceRepo, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/admin/orphaned-namespaces", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var result []OrphanedNamespaceResponse
		err = json.Unmarshal(w.Body.Bytes(), &result)
		require.NoError(t, err)
		require.Len(t, result, 1)
		assert.Equal(t, "stack-orphan-bob", result[0].Name)
		assert.Equal(t, []string{"nginx", "redis"}, result[0].HelmReleases)
		assert.NotNil(t, result[0].ResourceCounts)
	})

	t.Run("non-admin gets 403", func(t *testing.T) {
		t.Parallel()
		clientset := fake.NewSimpleClientset()
		k8sClient := k8s.NewClientFromInterface(clientset)
		instanceRepo := NewMockStackInstanceRepository()
		helmExec := &mockAdminHelmExecutor{}

		router := setupAdminRouter(k8sClient, helmExec, instanceRepo, "user")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/admin/orphaned-namespaces", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("returns 503 when k8s client is nil", func(t *testing.T) {
		t.Parallel()
		instanceRepo := NewMockStackInstanceRepository()
		helmExec := &mockAdminHelmExecutor{}

		router := setupAdminRouter(nil, helmExec, instanceRepo, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/admin/orphaned-namespaces", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	})
}

func TestDeleteOrphanedNamespace(t *testing.T) {
	t.Parallel()

	t.Run("deletes orphaned namespace and uninstalls releases", func(t *testing.T) {
		t.Parallel()
		clientset := fake.NewSimpleClientset()
		ctx := context.Background()

		_, err := clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "stack-orphan-bob"},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		k8sClient := k8s.NewClientFromInterface(clientset)
		instanceRepo := NewMockStackInstanceRepository()
		helmExec := &mockAdminHelmExecutor{
			listReleasesFunc: func(_ context.Context, _ string) ([]string, error) {
				return []string{"nginx", "redis"}, nil
			},
		}

		router := setupAdminRouter(k8sClient, helmExec, instanceRepo, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/admin/orphaned-namespaces/stack-orphan-bob", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		// Verify both releases were uninstalled.
		assert.Len(t, helmExec.uninstallCalls, 2)
		assert.Equal(t, "nginx", helmExec.uninstallCalls[0].ReleaseName)
		assert.Equal(t, "redis", helmExec.uninstallCalls[1].ReleaseName)

		// Verify namespace was deleted.
		_, err = clientset.CoreV1().Namespaces().Get(ctx, "stack-orphan-bob", metav1.GetOptions{})
		assert.Error(t, err) // Should be not found.
	})

	t.Run("rejects non-stack namespace", func(t *testing.T) {
		t.Parallel()
		clientset := fake.NewSimpleClientset()
		k8sClient := k8s.NewClientFromInterface(clientset)
		instanceRepo := NewMockStackInstanceRepository()
		helmExec := &mockAdminHelmExecutor{}

		router := setupAdminRouter(k8sClient, helmExec, instanceRepo, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/admin/orphaned-namespaces/default", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("rejects namespace with matching instance", func(t *testing.T) {
		t.Parallel()
		clientset := fake.NewSimpleClientset()
		k8sClient := k8s.NewClientFromInterface(clientset)
		instanceRepo := NewMockStackInstanceRepository()
		_ = instanceRepo.Create(&models.StackInstance{
			ID:        "inst-1",
			Name:      "myapp",
			Namespace: "stack-myapp-alice",
			OwnerID:   "alice",
			Status:    models.StackStatusRunning,
		})
		helmExec := &mockAdminHelmExecutor{}

		router := setupAdminRouter(k8sClient, helmExec, instanceRepo, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/admin/orphaned-namespaces/stack-myapp-alice", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusConflict, w.Code)
	})

	t.Run("non-admin gets 403", func(t *testing.T) {
		t.Parallel()
		clientset := fake.NewSimpleClientset()
		k8sClient := k8s.NewClientFromInterface(clientset)
		instanceRepo := NewMockStackInstanceRepository()
		helmExec := &mockAdminHelmExecutor{}

		router := setupAdminRouter(k8sClient, helmExec, instanceRepo, "devops")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/admin/orphaned-namespaces/stack-orphan-bob", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusForbidden, w.Code)
	})
}

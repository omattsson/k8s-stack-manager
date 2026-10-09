package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/cluster"
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

func (m *mockAdminHelmExecutor) History(_ context.Context, _ string, _ string, _ int) ([]deployer.ReleaseRevision, error) {
	return nil, nil
}

func (m *mockAdminHelmExecutor) Rollback(_ context.Context, _ string, _ string, _ int) (string, error) {
	return "", nil
}

func (m *mockAdminHelmExecutor) GetValues(_ context.Context, _ string, _ string, _ int) (string, error) {
	return "", nil
}

func (m *mockAdminHelmExecutor) RegistryLogin(_ context.Context, _, _, _ string) error {
	return nil
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
	var reg *cluster.Registry
	if k8sClient != nil {
		reg = cluster.NewRegistryForTest("default-cluster", k8sClient, helmExec)
	}
	h := NewAdminHandler(reg, instanceRepo)
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
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/admin/orphaned-namespaces?details=true", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var result []OrphanedNamespaceResponse
		err = json.Unmarshal(w.Body.Bytes(), &result)
		require.NoError(t, err)
		require.Len(t, result, 1)
		assert.Equal(t, "stack-orphan-bob", result[0].Name)
		assert.Equal(t, []string{"nginx", "redis"}, result[0].HelmReleases)
		assert.NotNil(t, result[0].ResourceCounts)
		assert.False(t, result[0].Managed)
	})

	t.Run("reports the managed-by label", func(t *testing.T) {
		t.Parallel()
		clientset := fake.NewSimpleClientset()
		ctx := context.Background()
		for _, meta := range []metav1.ObjectMeta{managedNamespaceMeta("stack-ours-a"), {Name: "stack-theirs-a"}} {
			_, err := clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: meta}, metav1.CreateOptions{})
			require.NoError(t, err)
		}

		router := setupAdminRouter(k8s.NewClientFromInterface(clientset), &mockAdminHelmExecutor{}, NewMockStackInstanceRepository(), "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/admin/orphaned-namespaces", nil)
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		var result []OrphanedNamespaceResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
		managed := map[string]bool{}
		for _, r := range result {
			managed[r.Name] = r.Managed
		}
		assert.Equal(t, map[string]bool{"stack-ours-a": true, "stack-theirs-a": false}, managed)
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

	t.Run("without details omits resource counts and releases", func(t *testing.T) {
		t.Parallel()
		clientset := fake.NewSimpleClientset()
		ctx := context.Background()

		_, err := clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "stack-orphan-test"},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		k8sClient := k8s.NewClientFromInterface(clientset)
		instanceRepo := NewMockStackInstanceRepository()
		helmExec := &mockAdminHelmExecutor{}

		router := setupAdminRouter(k8sClient, helmExec, instanceRepo, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/admin/orphaned-namespaces", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var result []OrphanedNamespaceResponse
		err = json.Unmarshal(w.Body.Bytes(), &result)
		require.NoError(t, err)
		require.Len(t, result, 1)
		assert.Nil(t, result[0].ResourceCounts)
		assert.Equal(t, []string{}, result[0].HelmReleases)
	})

	t.Run("details with nil helm executor returns empty releases", func(t *testing.T) {
		t.Parallel()
		clientset := fake.NewSimpleClientset()
		ctx := context.Background()

		_, err := clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: managedNamespaceMeta("stack-orphan-nohelm"),
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		k8sClient := k8s.NewClientFromInterface(clientset)
		instanceRepo := NewMockStackInstanceRepository()

		// Pass nil helm executor
		router := setupAdminRouter(k8sClient, nil, instanceRepo, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/admin/orphaned-namespaces?details=true", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var result []OrphanedNamespaceResponse
		err = json.Unmarshal(w.Body.Bytes(), &result)
		require.NoError(t, err)
		require.Len(t, result, 1)
		assert.Equal(t, []string{}, result[0].HelmReleases)
	})

	t.Run("non-not-found error from FindByNamespace is logged and skipped", func(t *testing.T) {
		t.Parallel()
		clientset := fake.NewSimpleClientset()
		ctx := context.Background()

		_, err := clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "stack-errns-user"},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		k8sClient := k8s.NewClientFromInterface(clientset)
		instanceRepo := NewMockStackInstanceRepository()
		// Inject a generic error for FindByNamespace
		instanceRepo.fetchErr = errors.New("connection reset")
		helmExec := &mockAdminHelmExecutor{}

		router := setupAdminRouter(k8sClient, helmExec, instanceRepo, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/admin/orphaned-namespaces", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var result []OrphanedNamespaceResponse
		err = json.Unmarshal(w.Body.Bytes(), &result)
		require.NoError(t, err)
		// Namespace with non-not-found error should be skipped, not included
		assert.Empty(t, result)
	})

	t.Run("details with listReleases error returns empty releases", func(t *testing.T) {
		t.Parallel()
		clientset := fake.NewSimpleClientset()
		ctx := context.Background()

		_, err := clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "stack-orphan-relerr"},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		k8sClient := k8s.NewClientFromInterface(clientset)
		instanceRepo := NewMockStackInstanceRepository()
		helmExec := &mockAdminHelmExecutor{
			listReleasesFunc: func(_ context.Context, _ string) ([]string, error) {
				return nil, errors.New("helm error")
			},
		}

		router := setupAdminRouter(k8sClient, helmExec, instanceRepo, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/admin/orphaned-namespaces?details=true", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var result []OrphanedNamespaceResponse
		err = json.Unmarshal(w.Body.Bytes(), &result)
		require.NoError(t, err)
		require.Len(t, result, 1)
		assert.Equal(t, []string{}, result[0].HelmReleases)
	})
}

// managedNamespaceMeta returns namespace metadata with the label that the
// deployer sets on the namespaces it creates.
func managedNamespaceMeta(name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:   name,
		Labels: map[string]string{k8s.ManagedByLabelKey: k8s.ManagedByLabelValue},
	}
}

func TestDeleteOrphanedNamespaceManagedGuard(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		namespace  string
		labelled   bool
		create     bool
		query      string
		wantStatus int
		wantGone   bool
	}{
		{name: "managed namespace without confirm", namespace: "stack-managed-a", labelled: true, create: true, wantStatus: http.StatusOK, wantGone: true},
		{name: "unmanaged namespace without confirm", namespace: "stack-foreign-a", create: true, wantStatus: http.StatusConflict},
		{name: "unmanaged namespace with wrong confirm", namespace: "stack-foreign-b", create: true, query: "?confirm=stack-foreign", wantStatus: http.StatusConflict},
		{name: "unmanaged namespace with full-name confirm", namespace: "stack-foreign-c", create: true, query: "?confirm=stack-foreign-c", wantStatus: http.StatusOK, wantGone: true},
		{name: "missing namespace", namespace: "stack-missing-a", wantStatus: http.StatusNotFound},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			clientset := fake.NewSimpleClientset()
			ctx := context.Background()
			if tt.create {
				meta := metav1.ObjectMeta{Name: tt.namespace}
				if tt.labelled {
					meta = managedNamespaceMeta(tt.namespace)
				}
				_, err := clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: meta}, metav1.CreateOptions{})
				require.NoError(t, err)
			}
			helmExec := &mockAdminHelmExecutor{}
			router := setupAdminRouter(k8s.NewClientFromInterface(clientset), helmExec, NewMockStackInstanceRepository(), "admin")

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodDelete, "/api/v1/admin/orphaned-namespaces/"+tt.namespace+tt.query, nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			_, getErr := clientset.CoreV1().Namespaces().Get(ctx, tt.namespace, metav1.GetOptions{})
			if tt.wantGone {
				assert.Error(t, getErr)
			} else if tt.create {
				assert.NoError(t, getErr, "namespace must not be deleted")
				assert.Empty(t, helmExec.uninstallCalls, "no release may be uninstalled")
			}
		})
	}
}

func TestDeleteOrphanedNamespace(t *testing.T) {
	t.Parallel()

	t.Run("deletes orphaned namespace and uninstalls releases", func(t *testing.T) {
		t.Parallel()
		clientset := fake.NewSimpleClientset()
		ctx := context.Background()

		_, err := clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: managedNamespaceMeta("stack-orphan-bob"),
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

	t.Run("returns 503 when registry is nil", func(t *testing.T) {
		t.Parallel()
		instanceRepo := NewMockStackInstanceRepository()

		router := setupAdminRouter(nil, nil, instanceRepo, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/admin/orphaned-namespaces/stack-orphan-bob", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	})

	t.Run("rejects invalid RFC1123 namespace", func(t *testing.T) {
		t.Parallel()
		clientset := fake.NewSimpleClientset()
		k8sClient := k8s.NewClientFromInterface(clientset)
		instanceRepo := NewMockStackInstanceRepository()
		helmExec := &mockAdminHelmExecutor{}

		router := setupAdminRouter(k8sClient, helmExec, instanceRepo, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/admin/orphaned-namespaces/stack-INVALID_UPPER", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("returns 500 on unexpected DB error during orphan check", func(t *testing.T) {
		t.Parallel()
		clientset := fake.NewSimpleClientset()
		k8sClient := k8s.NewClientFromInterface(clientset)
		instanceRepo := NewMockStackInstanceRepository()
		instanceRepo.fetchErr = errors.New("db connection lost")
		helmExec := &mockAdminHelmExecutor{}

		router := setupAdminRouter(k8sClient, helmExec, instanceRepo, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/admin/orphaned-namespaces/stack-orphan-bob", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})

	t.Run("continues deletion when helm listReleases fails", func(t *testing.T) {
		t.Parallel()
		clientset := fake.NewSimpleClientset()
		ctx := context.Background()

		_, err := clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: managedNamespaceMeta("stack-orphan-helmerr"),
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		k8sClient := k8s.NewClientFromInterface(clientset)
		instanceRepo := NewMockStackInstanceRepository()
		helmExec := &mockAdminHelmExecutor{
			listReleasesFunc: func(_ context.Context, _ string) ([]string, error) {
				return nil, errors.New("helm list failed")
			},
		}

		router := setupAdminRouter(k8sClient, helmExec, instanceRepo, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/admin/orphaned-namespaces/stack-orphan-helmerr", nil)
		router.ServeHTTP(w, req)

		// Should still succeed — listReleases error is best-effort
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("continues deletion when helm uninstall fails", func(t *testing.T) {
		t.Parallel()
		clientset := fake.NewSimpleClientset()
		ctx := context.Background()

		_, err := clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: managedNamespaceMeta("stack-orphan-unierr"),
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		k8sClient := k8s.NewClientFromInterface(clientset)
		instanceRepo := NewMockStackInstanceRepository()
		helmExec := &mockAdminHelmExecutor{
			listReleasesFunc: func(_ context.Context, _ string) ([]string, error) {
				return []string{"failing-release"}, nil
			},
			uninstallFunc: func(_ context.Context, _ deployer.UninstallRequest) (string, error) {
				return "", errors.New("uninstall failed")
			},
		}

		router := setupAdminRouter(k8sClient, helmExec, instanceRepo, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/admin/orphaned-namespaces/stack-orphan-unierr", nil)
		router.ServeHTTP(w, req)

		// Should still succeed — uninstall error is best-effort
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("deletes namespace without helm executor", func(t *testing.T) {
		t.Parallel()
		clientset := fake.NewSimpleClientset()
		ctx := context.Background()

		_, err := clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: managedNamespaceMeta("stack-orphan-nohelm"),
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		k8sClient := k8s.NewClientFromInterface(clientset)
		instanceRepo := NewMockStackInstanceRepository()

		router := setupAdminRouter(k8sClient, nil, instanceRepo, "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/admin/orphaned-namespaces/stack-orphan-nohelm", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})
}

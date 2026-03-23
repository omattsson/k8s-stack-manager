package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/cluster"
	"backend/internal/config"
	"backend/internal/helm"
	"backend/internal/k8s"
	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// ---- truncate tests ----

func TestTruncate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{
			name:   "string shorter than maxLen is unchanged",
			input:  "hello",
			maxLen: 10,
			want:   "hello",
		},
		{
			name:   "string equal to maxLen is unchanged",
			input:  "hello",
			maxLen: 5,
			want:   "hello",
		},
		{
			name:   "string longer than maxLen is truncated",
			input:  "hello world",
			maxLen: 5,
			want:   "hello",
		},
		{
			name:   "empty string remains empty",
			input:  "",
			maxLen: 5,
			want:   "",
		},
		{
			name:   "maxLen zero returns empty",
			input:  "hello",
			maxLen: 0,
			want:   "",
		},
		{
			name:   "multi-byte UTF-8 characters are preserved",
			input:  "\u00e4\u00f6\u00fc\u00df\u00e9", // five runes
			maxLen: 3,
			want:   "\u00e4\u00f6\u00fc",
		},
		{
			name:   "emoji runes are not split",
			input:  "\U0001f600\U0001f601\U0001f602\U0001f603",
			maxLen: 2,
			want:   "\U0001f600\U0001f601",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := truncate(tt.input, tt.maxLen)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ---- EnsureAdminUser tests ----

func TestEnsureAdminUser(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		adminUsername   string
		adminPassword   string
		seedAdmin       bool
		expectUserCount int
	}{
		{
			name:            "creates admin when not present",
			adminUsername:   "admin",
			adminPassword:   "secretpass",
			seedAdmin:       false,
			expectUserCount: 1,
		},
		{
			name:            "skips creation when admin already exists",
			adminUsername:   "admin",
			adminPassword:   "secretpass",
			seedAdmin:       true,
			expectUserCount: 1,
		},
		{
			name:            "no-op when admin username is empty",
			adminUsername:   "",
			adminPassword:   "secretpass",
			seedAdmin:       false,
			expectUserCount: 0,
		},
		{
			name:            "no-op when admin password is empty",
			adminUsername:   "admin",
			adminPassword:   "",
			seedAdmin:       false,
			expectUserCount: 0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repo := NewMockUserRepository()
			if tt.seedAdmin {
				seedUser(t, repo, "existing-admin-id", tt.adminUsername, "oldpass", "admin")
			}

			cfg := &config.AuthConfig{
				JWTSecret:     "test-secret",
				JWTExpiration: time.Hour,
				AdminUsername: tt.adminUsername,
				AdminPassword: tt.adminPassword,
			}
			h := NewAuthHandler(repo, cfg)
			h.EnsureAdminUser()

			users, err := repo.List()
			require.NoError(t, err)
			assert.Len(t, users, tt.expectUserCount)

			if tt.expectUserCount > 0 && tt.adminUsername != "" {
				found, findErr := repo.FindByUsername(tt.adminUsername)
				require.NoError(t, findErr)
				assert.Equal(t, "admin", found.Role)
			}
		})
	}
}

// ---- ExportAllValues tests ----

// setupExportRouter creates a test gin engine with the ExportAllValues route.
func setupExportRouter(
	instanceRepo *MockStackInstanceRepository,
	overrideRepo *MockValueOverrideRepository,
	defRepo *MockStackDefinitionRepository,
	ccRepo *MockChartConfigRepository,
	tmplChartRepo *MockTemplateChartConfigRepository,
	callerID, callerUsername string,
) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if callerID != "" {
			c.Set("userID", callerID)
		}
		if callerUsername != "" {
			c.Set("username", callerUsername)
		}
		c.Next()
	})

	valuesGen := helm.NewValuesGenerator()
	userRepo := NewMockUserRepository()
	tmplRepo := NewMockStackTemplateRepository()
	h := NewInstanceHandler(instanceRepo, overrideRepo, nil, defRepo, ccRepo, tmplRepo, tmplChartRepo, valuesGen, userRepo, 0)

	r.GET("/api/v1/stack-instances/:id/values", h.ExportAllValues)
	return r
}

func TestExportAllValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		instanceID string
		setup      func(*MockStackInstanceRepository, *MockStackDefinitionRepository, *MockChartConfigRepository, *MockTemplateChartConfigRepository)
		wantStatus int
		checkFn    func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:       "success — returns zip with chart values",
			instanceID: "i1",
			setup: func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository, _ *MockTemplateChartConfigRepository) {
				seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusRunning)
				seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
				require.NoError(t, ccRepo.Create(&models.ChartConfig{
					ID:                "c1",
					StackDefinitionID: "d1",
					ChartName:         "nginx",
					DefaultValues:     "replicas: 1",
					CreatedAt:         time.Now().UTC(),
				}))
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				assert.Equal(t, "application/zip", w.Header().Get("Content-Type"))
				assert.Contains(t, w.Header().Get("Content-Disposition"), "stack-a-values.zip")
				assert.NotEmpty(t, w.Body.Bytes())
			},
		},
		{
			name:       "instance not found returns 404",
			instanceID: "missing",
			setup:      func(_ *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository, _ *MockTemplateChartConfigRepository) {},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "definition not found returns 404",
			instanceID: "i1",
			setup: func(instRepo *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository, _ *MockTemplateChartConfigRepository) {
				seedInstance(t, instRepo, "i1", "stack-a", "missing-def", "uid-1", models.StackStatusRunning)
				// No definition seeded.
			},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "chart config repo error returns 500",
			instanceID: "i1",
			setup: func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository, _ *MockTemplateChartConfigRepository) {
				seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusRunning)
				seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
				ccRepo.SetError(errInternal)
			},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "no charts produces valid zip",
			instanceID: "i1",
			setup: func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, _ *MockChartConfigRepository, _ *MockTemplateChartConfigRepository) {
				seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusRunning)
				seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
				// No charts.
			},
			wantStatus: http.StatusOK,
		},
		{
			name:       "with template locked values",
			instanceID: "i1",
			setup: func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository, tmplChartRepo *MockTemplateChartConfigRepository) {
				seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusRunning)
				def := seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
				def.SourceTemplateID = "tmpl-1"
				require.NoError(t, defRepo.Update(def))
				require.NoError(t, ccRepo.Create(&models.ChartConfig{
					ID:                "c1",
					StackDefinitionID: "d1",
					ChartName:         "nginx",
					DefaultValues:     "replicas: 1",
					CreatedAt:         time.Now().UTC(),
				}))
				require.NoError(t, tmplChartRepo.Create(&models.TemplateChartConfig{
					ID:               "tc1",
					StackTemplateID:  "tmpl-1",
					ChartName:        "nginx",
					LockedValues:     "image: locked",
				}))
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				assert.Equal(t, "application/zip", w.Header().Get("Content-Type"))
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			instRepo := NewMockStackInstanceRepository()
			defRepo := NewMockStackDefinitionRepository()
			ccRepo := NewMockChartConfigRepository()
			tmplChartRepo := NewMockTemplateChartConfigRepository()
			tt.setup(instRepo, defRepo, ccRepo, tmplChartRepo)

			router := setupExportRouter(
				instRepo, NewMockValueOverrideRepository(),
				defRepo, ccRepo, tmplChartRepo,
				"uid-1", "alice",
			)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/"+tt.instanceID+"/values", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.checkFn != nil {
				tt.checkFn(t, w)
			}
		})
	}
}

// ---- DeployInstance additional error paths ----

func TestDeployInstance_ErrorPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		instanceID string
		setup      func(*MockStackInstanceRepository, *MockStackDefinitionRepository, *MockChartConfigRepository, *MockValueOverrideRepository)
		wantStatus int
		checkFn    func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:       "definition not found returns 404",
			instanceID: "i1",
			setup: func(instRepo *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository, _ *MockValueOverrideRepository) {
				seedInstance(t, instRepo, "i1", "stack-a", "missing-def", "uid-1", models.StackStatusDraft)
				// No definition seeded.
			},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "queued status returns 409",
			instanceID: "i1",
			setup: func(instRepo *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository, _ *MockValueOverrideRepository) {
				seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusQueued)
			},
			wantStatus: http.StatusConflict,
		},
		{
			name:       "stopping status returns 409",
			instanceID: "i1",
			setup: func(instRepo *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository, _ *MockValueOverrideRepository) {
				seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusStopping)
			},
			wantStatus: http.StatusConflict,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp map[string]string
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Contains(t, resp["error"], "stopping")
			},
		},
		{
			name:       "empty namespace returns 400",
			instanceID: "i1",
			setup: func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository, _ *MockValueOverrideRepository) {
				inst := &models.StackInstance{
					ID:                "i1",
					StackDefinitionID: "d1",
					Name:              "stack-a",
					Namespace:         "", // empty
					OwnerID:           "uid-1",
					Branch:            "master",
					Status:            models.StackStatusDraft,
					CreatedAt:         time.Now().UTC(),
					UpdatedAt:         time.Now().UTC(),
				}
				require.NoError(t, instRepo.Create(inst))
				seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
				require.NoError(t, ccRepo.Create(&models.ChartConfig{
					ID:                "c1",
					StackDefinitionID: "d1",
					ChartName:         "nginx",
					RepositoryURL:     "oci://example.com/charts/nginx",
					DeployOrder:       1,
				}))
			},
			wantStatus: http.StatusBadRequest,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp map[string]string
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Contains(t, resp["error"], "namespace")
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			instRepo := NewMockStackInstanceRepository()
			defRepo := NewMockStackDefinitionRepository()
			ccRepo := NewMockChartConfigRepository()
			overrideRepo := NewMockValueOverrideRepository()
			logRepo := NewMockDeploymentLogRepository()
			tt.setup(instRepo, defRepo, ccRepo, overrideRepo)

			mgr := newTestManager(instRepo, logRepo)

			router := setupDeployRouter(
				instRepo, overrideRepo, defRepo, ccRepo,
				NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
				mgr, nil, nil, logRepo,
				"uid-1", "alice", "user",
			)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/"+tt.instanceID+"/deploy", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.checkFn != nil {
				tt.checkFn(t, w)
			}
		})
	}
}

// ---- GetInstanceStatus additional tests ----

func TestGetInstanceStatus_ErrorPaths(t *testing.T) {
	t.Parallel()

	t.Run("registry with valid client for missing namespace returns 200", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		inst := seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusRunning)
		inst.ClusterID = "test-cluster"
		require.NoError(t, instRepo.Update(inst))

		// Registry has the cluster; namespace does not exist but GetNamespaceStatus
		// returns a "not_found" status rather than an error.
		cs := fake.NewSimpleClientset()
		k8sClient := k8s.NewClientFromInterface(cs)
		registry := cluster.NewRegistryForTest("test-cluster", k8sClient, nil)

		router := setupDeployRouter(
			instRepo, NewMockValueOverrideRepository(),
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, registry, NewMockDeploymentLogRepository(),
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/i1/status", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "not_found", resp["status"])
	})

	t.Run("registry fallback with valid client returns 200", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		inst := seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusRunning)
		inst.ClusterID = "test-cluster"
		require.NoError(t, instRepo.Update(inst))

		cs := fake.NewSimpleClientset()
		k8sClient := k8s.NewClientFromInterface(cs)
		registry := cluster.NewRegistryForTest("test-cluster", k8sClient, nil)

		router := setupDeployRouter(
			instRepo, NewMockValueOverrideRepository(),
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, registry, NewMockDeploymentLogRepository(),
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/i1/status", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("instance fetch error returns error status", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		instRepo.SetFetchError(errInternal)

		router := setupDeployRouter(
			instRepo, NewMockValueOverrideRepository(),
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, nil, NewMockDeploymentLogRepository(),
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/i1/status", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})

	t.Run("direct k8s query with no namespace returns status", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		inst := seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusRunning)
		inst.ClusterID = "test-cluster"
		require.NoError(t, instRepo.Update(inst))

		// Fake clientset with no namespace pre-created.
		cs := fake.NewSimpleClientset()
		k8sClient := k8s.NewClientFromInterface(cs)
		registry := cluster.NewRegistryForTest("test-cluster", k8sClient, nil)

		router := setupDeployRouter(
			instRepo, NewMockValueOverrideRepository(),
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, registry, NewMockDeploymentLogRepository(),
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/i1/status", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})
}

// ---- DeleteInstance additional tests ----

func TestDeleteInstance_WithBranchOverrides(t *testing.T) {
	t.Parallel()

	t.Run("deletes branch overrides then instance", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		branchRepo := NewMockChartBranchOverrideRepository()
		seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)
		require.NoError(t, branchRepo.Set(&models.ChartBranchOverride{
			ID:              "bo1",
			StackInstanceID: "i1",
			ChartConfigID:   "c1",
			Branch:          "develop",
		}))

		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set("userID", "uid-1")
			c.Set("username", "alice")
			c.Set("role", "user")
			c.Next()
		})
		valuesGen := helm.NewValuesGenerator()
		userRepo := NewMockUserRepository()
		h := NewInstanceHandler(instRepo, NewMockValueOverrideRepository(), branchRepo,
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			valuesGen, userRepo, 0)
		r.DELETE("/api/v1/stack-instances/:id", h.DeleteInstance)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/stack-instances/i1", nil)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNoContent, w.Code)

		// Verify branch overrides were deleted.
		overrides, err := branchRepo.List("i1")
		require.NoError(t, err)
		assert.Empty(t, overrides)
	})

	t.Run("branch override delete error returns 500", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		branchRepo := NewMockChartBranchOverrideRepository()
		seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)
		branchRepo.SetError(errInternal)

		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set("userID", "uid-1")
			c.Next()
		})
		valuesGen := helm.NewValuesGenerator()
		userRepo := NewMockUserRepository()
		h := NewInstanceHandler(instRepo, NewMockValueOverrideRepository(), branchRepo,
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			valuesGen, userRepo, 0)
		r.DELETE("/api/v1/stack-instances/:id", h.DeleteInstance)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/stack-instances/i1", nil)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})

	t.Run("repository error on delete returns proper status", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		instRepo.SetError(dberrors.NewDatabaseError("delete", dberrors.ErrNotFound))

		r := gin.New()
		valuesGen := helm.NewValuesGenerator()
		h := NewInstanceHandler(instRepo, NewMockValueOverrideRepository(), nil,
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			valuesGen, NewMockUserRepository(), 0)
		r.DELETE("/api/v1/stack-instances/:id", h.DeleteInstance)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/stack-instances/nonexistent", nil)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/cluster"
	"backend/internal/deployer"
	"backend/internal/helm"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupQuickDeployRouter creates a test gin engine for the QuickDeploy endpoint.
func setupQuickDeployRouter(
	templateRepo *MockStackTemplateRepository,
	templateChartRepo *MockTemplateChartConfigRepository,
	definitionRepo *MockStackDefinitionRepository,
	chartConfigRepo *MockChartConfigRepository,
	instanceRepo *MockStackInstanceRepository,
	branchOverrideRepo *MockChartBranchOverrideRepository,
	overrideRepo *MockValueOverrideRepository,
	auditRepo *MockAuditLogRepository,
	deployMgr *deployer.Manager,
	registry *cluster.Registry,
	callerID, callerUsername, callerRole string,
	defaultTTL int,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if callerID != "" {
			c.Set("userID", callerID)
		}
		if callerUsername != "" {
			c.Set("username", callerUsername)
		}
		if callerRole != "" {
			c.Set("role", callerRole)
		}
		c.Next()
	})

	valuesGen := helm.NewValuesGenerator()
	userRepo := NewMockUserRepository()

	h := NewQuickDeployHandler(
		templateRepo, templateChartRepo, definitionRepo, chartConfigRepo,
		instanceRepo, branchOverrideRepo, overrideRepo, valuesGen,
		deployMgr, userRepo, nil, auditRepo,
		&MockBroadcastSender{}, registry, nil,
		defaultTTL,
	)

	tpl := r.Group("/api/v1/templates")
	{
		tpl.POST("/:id/quick-deploy", h.QuickDeploy)
	}
	return r
}

func TestQuickDeploy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		templateID string
		body       quickDeployRequest
		setup      func(
			*MockStackTemplateRepository,
			*MockTemplateChartConfigRepository,
			*MockChartConfigRepository,
			*MockStackInstanceRepository,
			*MockChartBranchOverrideRepository,
		)
		noManager  bool
		defaultTTL int
		wantStatus int
		checkFn    func(*testing.T, *httptest.ResponseRecorder, *MockStackInstanceRepository, *MockChartBranchOverrideRepository, *MockAuditLogRepository)
	}{
		{
			name:       "success — published template creates instance and deploys",
			templateID: "t1",
			body:       quickDeployRequest{InstanceName: "my-instance"},
			setup: func(
				tmplRepo *MockStackTemplateRepository,
				tmplChartRepo *MockTemplateChartConfigRepository,
				ccRepo *MockChartConfigRepository,
				_ *MockStackInstanceRepository,
				_ *MockChartBranchOverrideRepository,
			) {
				seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)
				require.NoError(t, tmplChartRepo.Create(&models.TemplateChartConfig{
					ID:              "tc1",
					StackTemplateID: "t1",
					ChartName:       "nginx",
					RepositoryURL:   "oci://example.com/charts/nginx",
					DeployOrder:     1,
				}))
			},
			wantStatus: http.StatusAccepted,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder, instRepo *MockStackInstanceRepository, _ *MockChartBranchOverrideRepository, auditRepo *MockAuditLogRepository) {
				var resp quickDeployResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.NotEmpty(t, resp.Instance.ID)
				assert.NotEmpty(t, resp.Definition.ID)
				assert.NotEmpty(t, resp.LogID)
				assert.Equal(t, "my-instance", resp.Instance.Name)
				assert.Equal(t, models.StackStatusDeploying, resp.Instance.Status)
				assert.NotEmpty(t, resp.Instance.Namespace)

				// Verify audit log was created.
				auditRepo.mu.RLock()
				defer auditRepo.mu.RUnlock()
				assert.Len(t, auditRepo.entries, 1)
				assert.Equal(t, "quick_deploy", auditRepo.entries[0].Action)
			},
		},
		{
			name:       "template not found returns 404",
			templateID: "nonexistent",
			body:       quickDeployRequest{InstanceName: "my-instance"},
			setup: func(_ *MockStackTemplateRepository, _ *MockTemplateChartConfigRepository, _ *MockChartConfigRepository, _ *MockStackInstanceRepository, _ *MockChartBranchOverrideRepository) {
			},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "template not published returns 400",
			templateID: "t1",
			body:       quickDeployRequest{InstanceName: "my-instance"},
			setup: func(
				tmplRepo *MockStackTemplateRepository,
				_ *MockTemplateChartConfigRepository,
				_ *MockChartConfigRepository,
				_ *MockStackInstanceRepository,
				_ *MockChartBranchOverrideRepository,
			) {
				seedTemplate(t, tmplRepo, "t1", "Draft Template", "owner-1", false)
			},
			wantStatus: http.StatusBadRequest,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder, _ *MockStackInstanceRepository, _ *MockChartBranchOverrideRepository, _ *MockAuditLogRepository) {
				var body map[string]string
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
				assert.Contains(t, body["error"], "not published")
			},
		},
		{
			name:       "missing instance_name returns 400",
			templateID: "t1",
			body:       quickDeployRequest{},
			setup: func(
				tmplRepo *MockStackTemplateRepository,
				_ *MockTemplateChartConfigRepository,
				_ *MockChartConfigRepository,
				_ *MockStackInstanceRepository,
				_ *MockChartBranchOverrideRepository,
			) {
				seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)
			},
			wantStatus: http.StatusBadRequest,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder, _ *MockStackInstanceRepository, _ *MockChartBranchOverrideRepository, _ *MockAuditLogRepository) {
				var body map[string]string
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
				assert.Contains(t, body["error"], "instance_name")
			},
		},
		{
			name:       "with custom branch",
			templateID: "t1",
			body:       quickDeployRequest{InstanceName: "branch-test", Branch: "feature/xyz"},
			setup: func(
				tmplRepo *MockStackTemplateRepository,
				tmplChartRepo *MockTemplateChartConfigRepository,
				_ *MockChartConfigRepository,
				_ *MockStackInstanceRepository,
				_ *MockChartBranchOverrideRepository,
			) {
				seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)
				require.NoError(t, tmplChartRepo.Create(&models.TemplateChartConfig{
					ID:              "tc1",
					StackTemplateID: "t1",
					ChartName:       "app",
					RepositoryURL:   "oci://example.com/charts/app",
					DeployOrder:     1,
				}))
			},
			wantStatus: http.StatusAccepted,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder, _ *MockStackInstanceRepository, _ *MockChartBranchOverrideRepository, _ *MockAuditLogRepository) {
				var resp quickDeployResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, "feature/xyz", resp.Instance.Branch)
			},
		},
		{
			name:       "with branch overrides — creates override entries",
			templateID: "t1",
			body: quickDeployRequest{
				InstanceName: "override-test",
				BranchOverrides: map[string]string{
					"cc1": "feature/other",
					"cc2": "main",
				},
			},
			setup: func(
				tmplRepo *MockStackTemplateRepository,
				tmplChartRepo *MockTemplateChartConfigRepository,
				_ *MockChartConfigRepository,
				_ *MockStackInstanceRepository,
				_ *MockChartBranchOverrideRepository,
			) {
				seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)
				require.NoError(t, tmplChartRepo.Create(&models.TemplateChartConfig{
					ID:              "tc1",
					StackTemplateID: "t1",
					ChartName:       "svc-a",
					RepositoryURL:   "oci://example.com/charts/a",
					DeployOrder:     1,
				}))
			},
			wantStatus: http.StatusAccepted,
			checkFn: func(t *testing.T, _ *httptest.ResponseRecorder, _ *MockStackInstanceRepository, boRepo *MockChartBranchOverrideRepository, _ *MockAuditLogRepository) {
				boRepo.mu.RLock()
				defer boRepo.mu.RUnlock()
				assert.Len(t, boRepo.items, 2)
			},
		},
		{
			name:       "with TTL — sets expiry",
			templateID: "t1",
			body:       quickDeployRequest{InstanceName: "ttl-test", TTLMinutes: 120},
			setup: func(
				tmplRepo *MockStackTemplateRepository,
				tmplChartRepo *MockTemplateChartConfigRepository,
				_ *MockChartConfigRepository,
				_ *MockStackInstanceRepository,
				_ *MockChartBranchOverrideRepository,
			) {
				seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)
				require.NoError(t, tmplChartRepo.Create(&models.TemplateChartConfig{
					ID:              "tc1",
					StackTemplateID: "t1",
					ChartName:       "app",
					RepositoryURL:   "oci://example.com/charts/app",
					DeployOrder:     1,
				}))
			},
			wantStatus: http.StatusAccepted,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder, _ *MockStackInstanceRepository, _ *MockChartBranchOverrideRepository, _ *MockAuditLogRepository) {
				var resp quickDeployResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 120, resp.Instance.TTLMinutes)
				assert.NotNil(t, resp.Instance.ExpiresAt)
			},
		},
		{
			name:       "with default TTL — uses server default",
			templateID: "t1",
			body:       quickDeployRequest{InstanceName: "default-ttl"},
			defaultTTL: 480,
			setup: func(
				tmplRepo *MockStackTemplateRepository,
				tmplChartRepo *MockTemplateChartConfigRepository,
				_ *MockChartConfigRepository,
				_ *MockStackInstanceRepository,
				_ *MockChartBranchOverrideRepository,
			) {
				seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)
				require.NoError(t, tmplChartRepo.Create(&models.TemplateChartConfig{
					ID:              "tc1",
					StackTemplateID: "t1",
					ChartName:       "app",
					RepositoryURL:   "oci://example.com/charts/app",
					DeployOrder:     1,
				}))
			},
			wantStatus: http.StatusAccepted,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder, _ *MockStackInstanceRepository, _ *MockChartBranchOverrideRepository, _ *MockAuditLogRepository) {
				var resp quickDeployResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 480, resp.Instance.TTLMinutes)
				assert.NotNil(t, resp.Instance.ExpiresAt)
			},
		},
		{
			name:       "deploy failure — returns 202 with instance in error state",
			templateID: "t1",
			body:       quickDeployRequest{InstanceName: "deploy-fail"},
			noManager:  true,
			setup: func(
				tmplRepo *MockStackTemplateRepository,
				tmplChartRepo *MockTemplateChartConfigRepository,
				_ *MockChartConfigRepository,
				_ *MockStackInstanceRepository,
				_ *MockChartBranchOverrideRepository,
			) {
				seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)
				require.NoError(t, tmplChartRepo.Create(&models.TemplateChartConfig{
					ID:              "tc1",
					StackTemplateID: "t1",
					ChartName:       "app",
					RepositoryURL:   "oci://example.com/charts/app",
					DeployOrder:     1,
				}))
			},
			wantStatus: http.StatusAccepted,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder, instRepo *MockStackInstanceRepository, _ *MockChartBranchOverrideRepository, _ *MockAuditLogRepository) {
				var resp quickDeployResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, models.StackStatusError, resp.Instance.Status)
				assert.NotEmpty(t, resp.Instance.ErrorMessage)
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmplRepo := NewMockStackTemplateRepository()
			tmplChartRepo := NewMockTemplateChartConfigRepository()
			defRepo := NewMockStackDefinitionRepository()
			ccRepo := NewMockChartConfigRepository()
			instRepo := NewMockStackInstanceRepository()
			boRepo := NewMockChartBranchOverrideRepository()
			ovRepo := NewMockValueOverrideRepository()
			auditRepo := NewMockAuditLogRepository()

			tt.setup(tmplRepo, tmplChartRepo, ccRepo, instRepo, boRepo)

			var mgr *deployer.Manager
			if !tt.noManager {
				logRepo := NewMockDeploymentLogRepository()
				mgr = newTestManager(instRepo, logRepo)
			}

			// Use the test registry from newTestManager for cluster resolution.
			var registry *cluster.Registry
			if mgr != nil {
				registry = cluster.NewRegistryForTest("test-cluster", nil, &noopHelmExecutor{})
			}

			router := setupQuickDeployRouter(
				tmplRepo, tmplChartRepo, defRepo, ccRepo, instRepo, boRepo, ovRepo, auditRepo,
				mgr, registry,
				"uid-1", "alice", "user",
				tt.defaultTTL,
			)

			body, _ := json.Marshal(tt.body)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/templates/"+tt.templateID+"/quick-deploy", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.checkFn != nil {
				tt.checkFn(t, w, instRepo, boRepo, auditRepo)
			}
		})
	}
}

func TestQuickDeploy_UsesTemplateDefaultBranch(t *testing.T) {
	t.Parallel()

	tmplRepo := NewMockStackTemplateRepository()
	tmplChartRepo := NewMockTemplateChartConfigRepository()
	defRepo := NewMockStackDefinitionRepository()
	ccRepo := NewMockChartConfigRepository()
	instRepo := NewMockStackInstanceRepository()
	boRepo := NewMockChartBranchOverrideRepository()
	ovRepo := NewMockValueOverrideRepository()
	auditRepo := NewMockAuditLogRepository()

	// Template with custom default branch.
	tmpl := &models.StackTemplate{
		ID:            "t1",
		Name:          "Template with main",
		IsPublished:   true,
		DefaultBranch: "main",
		OwnerID:       "owner-1",
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	require.NoError(t, tmplRepo.Create(tmpl))

	require.NoError(t, tmplChartRepo.Create(&models.TemplateChartConfig{
		ID:              "tc1",
		StackTemplateID: "t1",
		ChartName:       "svc",
		RepositoryURL:   "oci://example.com/charts/svc",
		DeployOrder:     1,
	}))

	logRepo := NewMockDeploymentLogRepository()
	mgr := newTestManager(instRepo, logRepo)
	registry := cluster.NewRegistryForTest("test-cluster", nil, &noopHelmExecutor{})

	router := setupQuickDeployRouter(
		tmplRepo, tmplChartRepo, defRepo, ccRepo, instRepo, boRepo, ovRepo, auditRepo,
		mgr, registry,
		"uid-1", "alice", "user",
		0,
	)

	body, _ := json.Marshal(quickDeployRequest{InstanceName: "branch-default-test"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/templates/t1/quick-deploy", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusAccepted, w.Code)
	var resp quickDeployResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "main", resp.Instance.Branch)
}

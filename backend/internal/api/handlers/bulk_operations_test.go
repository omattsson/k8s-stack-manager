package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// setupBulkRouter creates a test gin engine with bulk operation routes.
func setupBulkRouter(
	instanceRepo *MockStackInstanceRepository,
	overrideRepo *MockValueOverrideRepository,
	defRepo *MockStackDefinitionRepository,
	ccRepo *MockChartConfigRepository,
	tmplRepo *MockStackTemplateRepository,
	tmplChartRepo *MockTemplateChartConfigRepository,
	deployManager *deployer.Manager,
	deployLogRepo models.DeploymentLogRepository,
	callerID, callerUsername, callerRole string,
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

	h := NewInstanceHandlerWithDeployer(
		instanceRepo, overrideRepo, nil, defRepo, ccRepo,
		tmplRepo, tmplChartRepo, valuesGen, userRepo,
		deployManager, nil, nil, deployLogRepo, nil,
		0,
	)

	bulk := r.Group("/api/v1/stack-instances/bulk")
	{
		bulk.POST("/deploy", h.BulkDeploy)
		bulk.POST("/stop", h.BulkStop)
		bulk.POST("/clean", h.BulkClean)
		bulk.POST("/delete", h.BulkDelete)
	}
	return r
}

// newBulkTestManager creates a Manager for bulk operation tests.
func newBulkTestManager(instRepo models.StackInstanceRepository, logRepo models.DeploymentLogRepository) *deployer.Manager {
	testRegistry := cluster.NewRegistryForTest("test-cluster", nil, &noopHelmExecutor{})
	return deployer.NewManager(deployer.ManagerConfig{
		Registry:      testRegistry,
		InstanceRepo:  instRepo,
		DeployLogRepo: logRepo,
		Hub:           &MockBroadcastSender{},
		MaxConcurrent: 2,
	})
}

func TestBulkDeploy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       interface{}
		callerID   string
		callerRole string
		setup      func(*MockStackInstanceRepository, *MockStackDefinitionRepository, *MockChartConfigRepository)
		noManager  bool
		wantStatus int
		checkFn    func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:       "happy path — two draft instances return 200 with both succeeded",
			body:       BulkOperationRequest{InstanceIDs: []string{"i1", "i2"}},
			callerID:   "uid-1",
			callerRole: "devops",
			setup: func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)
				seedInstance(t, instRepo, "i2", "stack-b", "d1", "uid-2", models.StackStatusDraft)
				seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
				require.NoError(t, ccRepo.Create(&models.ChartConfig{
					ID:                "c1",
					StackDefinitionID: "d1",
					ChartName:         "nginx",
					RepositoryURL:     "oci://example.com/charts/nginx",
					DeployOrder:       1,
				}))
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp BulkOperationResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 2, resp.Total)
				assert.Equal(t, 2, resp.Succeeded)
				assert.Equal(t, 0, resp.Failed)
				assert.Len(t, resp.Results, 2)
				for _, r := range resp.Results {
					assert.Equal(t, "success", r.Status)
					assert.NotEmpty(t, r.LogID)
				}
			},
		},
		{
			name:       "one not found — partial success",
			body:       BulkOperationRequest{InstanceIDs: []string{"i1", "nonexistent"}},
			callerID:   "uid-1",
			callerRole: "admin",
			setup: func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)
				seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
				require.NoError(t, ccRepo.Create(&models.ChartConfig{
					ID:                "c1",
					StackDefinitionID: "d1",
					ChartName:         "nginx",
					RepositoryURL:     "oci://example.com/charts/nginx",
					DeployOrder:       1,
				}))
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp BulkOperationResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 2, resp.Total)
				assert.Equal(t, 1, resp.Succeeded)
				assert.Equal(t, 1, resp.Failed)
				// Check the failed result.
				for _, r := range resp.Results {
					if r.InstanceID == "nonexistent" {
						assert.Equal(t, "error", r.Status)
						assert.Equal(t, "not found", r.Error)
					}
				}
			},
		},
		{
			name:       "empty instance_ids returns 400",
			body:       BulkOperationRequest{InstanceIDs: []string{}},
			callerID:   "uid-1",
			callerRole: "admin",
			setup:      func(_ *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository) {},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing instance_ids returns 400",
			body:       map[string]string{"foo": "bar"},
			callerID:   "uid-1",
			callerRole: "admin",
			setup:      func(_ *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository) {},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "too many instances returns 400",
			body: BulkOperationRequest{InstanceIDs: func() []string {
				ids := make([]string, 51)
				for i := range ids {
					ids[i] = "id"
				}
				return ids
			}()},
			callerID:   "uid-1",
			callerRole: "admin",
			setup:      func(_ *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository) {},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "regular user cannot deploy others instances — forbidden in results",
			body:       BulkOperationRequest{InstanceIDs: []string{"i1"}},
			callerID:   "uid-other",
			callerRole: "user",
			setup: func(instRepo *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp BulkOperationResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 1, resp.Failed)
				assert.Equal(t, "forbidden", resp.Results[0].Error)
			},
		},
		{
			name:       "deploying instance returns error in results",
			body:       BulkOperationRequest{InstanceIDs: []string{"i1"}},
			callerID:   "uid-1",
			callerRole: "admin",
			setup: func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, _ *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDeploying)
				seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp BulkOperationResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 1, resp.Failed)
				assert.Contains(t, resp.Results[0].Error, "cannot deploy")
			},
		},
		{
			name:       "no deploy manager returns error in results",
			body:       BulkOperationRequest{InstanceIDs: []string{"i1"}},
			callerID:   "uid-1",
			callerRole: "admin",
			noManager:  true,
			setup: func(instRepo *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp BulkOperationResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 1, resp.Failed)
				assert.Contains(t, resp.Results[0].Error, "deployment service not configured")
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
			logRepo := NewMockDeploymentLogRepository()
			tt.setup(instRepo, defRepo, ccRepo)

			var manager *deployer.Manager
			if !tt.noManager {
				manager = newBulkTestManager(instRepo, logRepo)
			}

			router := setupBulkRouter(
				instRepo,
				NewMockValueOverrideRepository(),
				defRepo, ccRepo,
				NewMockStackTemplateRepository(),
				NewMockTemplateChartConfigRepository(),
				manager, logRepo,
				tt.callerID, "testuser", tt.callerRole,
			)

			body, _ := json.Marshal(tt.body)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/bulk/deploy", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.checkFn != nil {
				tt.checkFn(t, w)
			}
		})
	}
}

func TestBulkStop(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       interface{}
		callerID   string
		callerRole string
		setup      func(*MockStackInstanceRepository, *MockStackDefinitionRepository, *MockChartConfigRepository)
		wantStatus int
		checkFn    func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:       "happy path — running instance returns success",
			body:       BulkOperationRequest{InstanceIDs: []string{"i1"}},
			callerID:   "uid-1",
			callerRole: "devops",
			setup: func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusRunning)
				seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
				require.NoError(t, ccRepo.Create(&models.ChartConfig{
					ID:                "c1",
					StackDefinitionID: "d1",
					ChartName:         "nginx",
					RepositoryURL:     "oci://example.com/charts/nginx",
					DeployOrder:       1,
				}))
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp BulkOperationResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 1, resp.Succeeded)
				assert.Equal(t, "success", resp.Results[0].Status)
			},
		},
		{
			name:       "draft instance cannot be stopped",
			body:       BulkOperationRequest{InstanceIDs: []string{"i1"}},
			callerID:   "uid-1",
			callerRole: "admin",
			setup: func(instRepo *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp BulkOperationResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 1, resp.Failed)
				assert.Contains(t, resp.Results[0].Error, "cannot stop")
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
			logRepo := NewMockDeploymentLogRepository()
			tt.setup(instRepo, defRepo, ccRepo)

			manager := newBulkTestManager(instRepo, logRepo)

			router := setupBulkRouter(
				instRepo,
				NewMockValueOverrideRepository(),
				defRepo, ccRepo,
				NewMockStackTemplateRepository(),
				NewMockTemplateChartConfigRepository(),
				manager, logRepo,
				tt.callerID, "testuser", tt.callerRole,
			)

			body, _ := json.Marshal(tt.body)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/bulk/stop", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.checkFn != nil {
				tt.checkFn(t, w)
			}
		})
	}
}

func TestBulkClean(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       interface{}
		callerID   string
		callerRole string
		setup      func(*MockStackInstanceRepository, *MockStackDefinitionRepository, *MockChartConfigRepository)
		wantStatus int
		checkFn    func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:       "happy path — stopped instance returns success",
			body:       BulkOperationRequest{InstanceIDs: []string{"i1"}},
			callerID:   "uid-1",
			callerRole: "devops",
			setup: func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusStopped)
				seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
				require.NoError(t, ccRepo.Create(&models.ChartConfig{
					ID:                "c1",
					StackDefinitionID: "d1",
					ChartName:         "nginx",
					RepositoryURL:     "oci://example.com/charts/nginx",
					DeployOrder:       1,
				}))
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp BulkOperationResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 1, resp.Succeeded)
				assert.Equal(t, "success", resp.Results[0].Status)
			},
		},
		{
			name:       "draft instance cannot be cleaned",
			body:       BulkOperationRequest{InstanceIDs: []string{"i1"}},
			callerID:   "uid-1",
			callerRole: "admin",
			setup: func(instRepo *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp BulkOperationResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 1, resp.Failed)
				assert.Contains(t, resp.Results[0].Error, "cannot clean")
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
			logRepo := NewMockDeploymentLogRepository()
			tt.setup(instRepo, defRepo, ccRepo)

			manager := newBulkTestManager(instRepo, logRepo)

			router := setupBulkRouter(
				instRepo,
				NewMockValueOverrideRepository(),
				defRepo, ccRepo,
				NewMockStackTemplateRepository(),
				NewMockTemplateChartConfigRepository(),
				manager, logRepo,
				tt.callerID, "testuser", tt.callerRole,
			)

			body, _ := json.Marshal(tt.body)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/bulk/clean", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.checkFn != nil {
				tt.checkFn(t, w)
			}
		})
	}
}

func TestBulkDelete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       interface{}
		callerID   string
		callerRole string
		setup      func(*MockStackInstanceRepository)
		wantStatus int
		checkFn    func(*testing.T, *httptest.ResponseRecorder, *MockStackInstanceRepository)
	}{
		{
			name:       "happy path — deletes multiple instances",
			body:       BulkOperationRequest{InstanceIDs: []string{"i1", "i2"}},
			callerID:   "uid-1",
			callerRole: "admin",
			setup: func(instRepo *MockStackInstanceRepository) {
				seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)
				seedInstance(t, instRepo, "i2", "stack-b", "d1", "uid-2", models.StackStatusStopped)
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder, instRepo *MockStackInstanceRepository) {
				var resp BulkOperationResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 2, resp.Total)
				assert.Equal(t, 2, resp.Succeeded)
				assert.Equal(t, 0, resp.Failed)
				// Verify instances are actually deleted.
				_, err := instRepo.FindByID("i1")
				assert.Error(t, err)
				_, err = instRepo.FindByID("i2")
				assert.Error(t, err)
			},
		},
		{
			name:       "regular user can delete own instance",
			body:       BulkOperationRequest{InstanceIDs: []string{"i1"}},
			callerID:   "uid-1",
			callerRole: "user",
			setup: func(instRepo *MockStackInstanceRepository) {
				seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder, instRepo *MockStackInstanceRepository) {
				var resp BulkOperationResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 1, resp.Succeeded)
			},
		},
		{
			name:       "regular user cannot delete others instance",
			body:       BulkOperationRequest{InstanceIDs: []string{"i1"}},
			callerID:   "uid-other",
			callerRole: "user",
			setup: func(instRepo *MockStackInstanceRepository) {
				seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder, instRepo *MockStackInstanceRepository) {
				var resp BulkOperationResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 1, resp.Failed)
				assert.Equal(t, "forbidden", resp.Results[0].Error)
				// Instance should still exist.
				_, err := instRepo.FindByID("i1")
				assert.NoError(t, err)
			},
		},
		{
			name:       "mixed results — one exists one does not",
			body:       BulkOperationRequest{InstanceIDs: []string{"i1", "missing"}},
			callerID:   "uid-1",
			callerRole: "admin",
			setup: func(instRepo *MockStackInstanceRepository) {
				seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder, _ *MockStackInstanceRepository) {
				var resp BulkOperationResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 1, resp.Succeeded)
				assert.Equal(t, 1, resp.Failed)
			},
		},
		{
			name:       "empty body returns 400",
			body:       map[string]string{},
			callerID:   "uid-1",
			callerRole: "admin",
			setup:      func(_ *MockStackInstanceRepository) {},
			wantStatus: http.StatusBadRequest,
			checkFn:    nil,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			instRepo := NewMockStackInstanceRepository()
			tt.setup(instRepo)

			router := setupBulkRouter(
				instRepo,
				NewMockValueOverrideRepository(),
				NewMockStackDefinitionRepository(),
				NewMockChartConfigRepository(),
				NewMockStackTemplateRepository(),
				NewMockTemplateChartConfigRepository(),
				nil, nil,
				tt.callerID, "testuser", tt.callerRole,
			)

			body, _ := json.Marshal(tt.body)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/bulk/delete", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.checkFn != nil {
				tt.checkFn(t, w, instRepo)
			}
		})
	}
}

func TestBulkOperationMaxInstances(t *testing.T) {
	t.Parallel()

	// Verify that exactly MaxBulkInstances is allowed but MaxBulkInstances+1 is rejected.
	ids := make([]string, MaxBulkInstances)
	for i := range ids {
		ids[i] = "id"
	}
	body, _ := json.Marshal(BulkOperationRequest{InstanceIDs: ids})

	instRepo := NewMockStackInstanceRepository()
	router := setupBulkRouter(
		instRepo,
		NewMockValueOverrideRepository(),
		NewMockStackDefinitionRepository(),
		NewMockChartConfigRepository(),
		NewMockStackTemplateRepository(),
		NewMockTemplateChartConfigRepository(),
		nil, nil,
		"uid-1", "testuser", "admin",
	)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/bulk/delete", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	// Should be accepted (200 OK with results, not 400).
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestBulkOperationInvalidJSON(t *testing.T) {
	t.Parallel()

	router := setupBulkRouter(
		NewMockStackInstanceRepository(),
		NewMockValueOverrideRepository(),
		NewMockStackDefinitionRepository(),
		NewMockChartConfigRepository(),
		NewMockStackTemplateRepository(),
		NewMockTemplateChartConfigRepository(),
		nil, nil,
		"uid-1", "testuser", "admin",
	)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/bulk/delete", strings.NewReader("{invalid json"))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// Verify that instance_name is populated in results even on operation error.
func TestBulkOperationReturnsInstanceName(t *testing.T) {
	t.Parallel()

	instRepo := NewMockStackInstanceRepository()
	seedInstance(t, instRepo, "i1", "my-stack", "d1", "uid-1", models.StackStatusDraft)

	router := setupBulkRouter(
		instRepo,
		NewMockValueOverrideRepository(),
		NewMockStackDefinitionRepository(),
		NewMockChartConfigRepository(),
		NewMockStackTemplateRepository(),
		NewMockTemplateChartConfigRepository(),
		nil, nil,
		"uid-1", "testuser", "admin",
	)

	body, _ := json.Marshal(BulkOperationRequest{InstanceIDs: []string{"i1"}})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/bulk/deploy", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp BulkOperationResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "my-stack", resp.Results[0].InstanceName)
}

// Ensure unused imports don't cause issues.
var _ = time.Now

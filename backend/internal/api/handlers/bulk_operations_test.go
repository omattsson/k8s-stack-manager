package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/cluster"
	"backend/internal/database"
	"backend/internal/deployer"
	"backend/internal/helm"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupBulkRouter creates a test gin engine with bulk operation routes.
func setupBulkRouter(
	t *testing.T,
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
	t.Helper()
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

	boRepo := NewMockChartBranchOverrideRepository()
	h, err := NewInstanceHandlerWithDeployer(
		instanceRepo, overrideRepo, boRepo, defRepo, ccRepo,
		tmplRepo, tmplChartRepo, valuesGen, userRepo,
		deployManager, nil, nil, deployLogRepo, nil,
		0,
		&mockHandlerTxRunner{repos: database.TxRepos{
			StackInstance:  instanceRepo,
			BranchOverride: boRepo,
		}},
	)
	require.NoError(t, err)

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

			router := setupBulkRouter(t,
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

// TestBulkDeploy_RecordsCaller checks that each deploy log of a bulk deploy
// stores the user who started it (per-user analytics), also for an instance
// of another owner.
func TestBulkDeploy_RecordsCaller(t *testing.T) {
	t.Parallel()

	instRepo := NewMockStackInstanceRepository()
	defRepo := NewMockStackDefinitionRepository()
	ccRepo := NewMockChartConfigRepository()
	logRepo := NewMockDeploymentLogRepository()
	seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)
	seedInstance(t, instRepo, "i2", "stack-b", "d1", "uid-2", models.StackStatusDraft)
	seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
	require.NoError(t, ccRepo.Create(&models.ChartConfig{
		ID: "c1", StackDefinitionID: "d1", ChartName: "nginx", RepositoryURL: "oci://example.com/charts/nginx", DeployOrder: 1,
	}))

	router := setupBulkRouter(t, instRepo, NewMockValueOverrideRepository(), defRepo, ccRepo,
		NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
		newBulkTestManager(instRepo, logRepo), logRepo, "uid-ops", "ops", "devops")

	body, _ := json.Marshal(BulkOperationRequest{InstanceIDs: []string{"i1", "i2"}})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/bulk/deploy", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp BulkOperationResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, 2, resp.Succeeded)
	for _, r := range resp.Results {
		log, err := logRepo.FindByID(context.Background(), r.LogID)
		require.NoError(t, err)
		assert.Equal(t, "uid-ops", log.UserID, "instance %s", r.InstanceID)
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

			router := setupBulkRouter(t,
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

			router := setupBulkRouter(t,
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

	// seedDefWithChart seeds definition d1 with one chart.
	seedDefWithChart := func(t *testing.T, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository) {
		t.Helper()
		seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
		require.NoError(t, ccRepo.Create(&models.ChartConfig{
			ID:                "c1",
			StackDefinitionID: "d1",
			ChartName:         "nginx",
			RepositoryURL:     "oci://example.com/charts/nginx",
			DeployOrder:       1,
		}))
	}

	tests := []struct {
		name        string
		body        interface{}
		callerID    string
		callerRole  string
		withManager bool
		setup       func(*testing.T, *MockStackInstanceRepository, *MockStackDefinitionRepository, *MockChartConfigRepository)
		wantStatus  int
		checkFn     func(*testing.T, *httptest.ResponseRecorder, *MockStackInstanceRepository)
	}{
		{
			name:       "happy path — deletes multiple draft instances",
			body:       BulkOperationRequest{InstanceIDs: []string{"i1", "i2"}},
			callerID:   "uid-1",
			callerRole: "admin",
			setup: func(t *testing.T, instRepo *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)
				seedInstance(t, instRepo, "i2", "stack-b", "d1", "uid-2", models.StackStatusDraft)
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder, instRepo *MockStackInstanceRepository) {
				var resp BulkOperationResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 2, resp.Total)
				assert.Equal(t, 2, resp.Succeeded)
				assert.Equal(t, 0, resp.Failed)
				// A draft has nothing to clean: no clean log.
				assert.Empty(t, resp.Results[0].LogID)
				// Verify instances are actually deleted.
				_, err := instRepo.FindByID("i1")
				assert.Error(t, err)
				_, err = instRepo.FindByID("i2")
				assert.Error(t, err)
			},
		},
		{
			name:        "stopped instance is cleaned first, then deleted",
			body:        BulkOperationRequest{InstanceIDs: []string{"i1"}},
			callerID:    "uid-1",
			callerRole:  "admin",
			withManager: true,
			setup: func(t *testing.T, instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusStopped)
				seedDefWithChart(t, defRepo, ccRepo)
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder, instRepo *MockStackInstanceRepository) {
				var resp BulkOperationResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				require.Len(t, resp.Results, 1)
				assert.Equal(t, "success", resp.Results[0].Status)
				assert.NotEmpty(t, resp.Results[0].LogID, "the result must carry the clean log")
				// The deploy manager deletes the row when the clean completes.
				assert.Eventually(t, func() bool {
					_, err := instRepo.FindByID("i1")
					return err != nil
				}, 5*time.Second, 20*time.Millisecond)
			},
		},
		{
			name:        "running, partial and error instances are cleaned first",
			body:        BulkOperationRequest{InstanceIDs: []string{"i1", "i2", "i3"}},
			callerID:    "uid-1",
			callerRole:  "admin",
			withManager: true,
			setup: func(t *testing.T, instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusRunning)
				seedInstance(t, instRepo, "i2", "stack-b", "d1", "uid-1", models.StackStatusPartial)
				seedInstance(t, instRepo, "i3", "stack-c", "d1", "uid-1", models.StackStatusError)
				seedDefWithChart(t, defRepo, ccRepo)
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder, instRepo *MockStackInstanceRepository) {
				var resp BulkOperationResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 3, resp.Succeeded)
				for _, r := range resp.Results {
					assert.NotEmpty(t, r.LogID, r.InstanceID)
				}
				assert.Eventually(t, func() bool {
					for _, id := range []string{"i1", "i2", "i3"} {
						if _, err := instRepo.FindByID(id); err == nil {
							return false
						}
					}
					return true
				}, 5*time.Second, 20*time.Millisecond)
			},
		},
		{
			name:        "operation in progress gives a per-item error",
			body:        BulkOperationRequest{InstanceIDs: []string{"i1", "i2"}},
			callerID:    "uid-1",
			callerRole:  "admin",
			withManager: true,
			setup: func(t *testing.T, instRepo *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDeploying)
				seedInstance(t, instRepo, "i2", "stack-b", "d1", "uid-1", models.StackStatusDraft)
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder, instRepo *MockStackInstanceRepository) {
				var resp BulkOperationResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 1, resp.Succeeded)
				assert.Equal(t, 1, resp.Failed)
				assert.Equal(t, "error", resp.Results[0].Status)
				assert.Equal(t, "Cannot delete: instance is currently deploying", resp.Results[0].Error)
				inst, err := instRepo.FindByID("i1")
				require.NoError(t, err, "an instance in progress must stay")
				assert.Equal(t, models.StackStatusDeploying, inst.Status)
			},
		},
		{
			name:       "stopped instance without deploy manager is not deleted",
			body:       BulkOperationRequest{InstanceIDs: []string{"i1"}},
			callerID:   "uid-1",
			callerRole: "admin",
			setup: func(t *testing.T, instRepo *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusStopped)
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder, instRepo *MockStackInstanceRepository) {
				var resp BulkOperationResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 1, resp.Failed)
				assert.Equal(t, msgDeployerNotConfigured, resp.Results[0].Error)
				_, err := instRepo.FindByID("i1")
				assert.NoError(t, err, "the instance with cluster resources must stay")
			},
		},
		{
			name:        "missing definition gives a per-item error",
			body:        BulkOperationRequest{InstanceIDs: []string{"i1"}},
			callerID:    "uid-1",
			callerRole:  "admin",
			withManager: true,
			setup: func(t *testing.T, instRepo *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i1", "stack-a", "d-missing", "uid-1", models.StackStatusRunning)
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder, instRepo *MockStackInstanceRepository) {
				var resp BulkOperationResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 1, resp.Failed)
				assert.Equal(t, "Stack definition not found", resp.Results[0].Error)
				_, err := instRepo.FindByID("i1")
				assert.NoError(t, err)
			},
		},
		{
			name:       "regular user can delete own instance",
			body:       BulkOperationRequest{InstanceIDs: []string{"i1"}},
			callerID:   "uid-1",
			callerRole: "user",
			setup: func(t *testing.T, instRepo *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository) {
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
			setup: func(t *testing.T, instRepo *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository) {
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
			setup: func(t *testing.T, instRepo *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository) {
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
			setup: func(_ *testing.T, _ *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository) {
			},
			wantStatus: http.StatusBadRequest,
			checkFn:    nil,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			instRepo := NewMockStackInstanceRepository()
			defRepo := NewMockStackDefinitionRepository()
			ccRepo := NewMockChartConfigRepository()
			tt.setup(t, instRepo, defRepo, ccRepo)

			var manager *deployer.Manager
			var logRepo models.DeploymentLogRepository
			if tt.withManager {
				mockLogRepo := NewMockDeploymentLogRepository()
				logRepo = mockLogRepo
				manager = newBulkTestManager(instRepo, mockLogRepo)
			}

			router := setupBulkRouter(t,
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

// TestBulkDelete_ConcurrentDeletes: two bulk deletes of the same running
// instance at the same time start one clean. The other gets an error
// result, and the instance is deleted once.
func TestBulkDelete_ConcurrentDeletes(t *testing.T) {
	t.Parallel()

	instRepo := NewMockStackInstanceRepository()
	defRepo := NewMockStackDefinitionRepository()
	ccRepo := NewMockChartConfigRepository()
	seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusRunning)
	seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
	require.NoError(t, ccRepo.Create(&models.ChartConfig{ID: "c1", StackDefinitionID: "d1", ChartName: "nginx", DeployOrder: 1}))

	logRepo := NewMockDeploymentLogRepository()
	// The handler and the deploy manager share the repository, so the
	// conditional update of the clean start decides.
	manager := newBulkTestManager(instRepo, logRepo)
	router := setupBulkRouter(t, instRepo, NewMockValueOverrideRepository(), defRepo, ccRepo,
		NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
		manager, logRepo, "uid-1", "testuser", "admin")

	const callers = 2
	results := make([]BulkOperationResultItem, callers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for n := 0; n < callers; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			body, _ := json.Marshal(BulkOperationRequest{InstanceIDs: []string{"i1"}})
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/bulk/delete", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)
			var resp BulkOperationResponse
			if json.Unmarshal(w.Body.Bytes(), &resp) == nil && len(resp.Results) == 1 {
				results[n] = resp.Results[0]
			}
		}(n)
	}
	close(start)
	wg.Wait()

	succeeded := 0
	for _, r := range results {
		if r.Status == "success" {
			succeeded++
			assert.NotEmpty(t, r.LogID)
		} else {
			assert.Equal(t, "error", r.Status)
			assert.Contains(t, r.Error, "Cannot delete")
		}
	}
	assert.Equal(t, 1, succeeded, "exactly one delete starts the clean: %+v", results)
	assert.Eventually(t, func() bool {
		_, err := instRepo.FindByID("i1")
		return err != nil
	}, 5*time.Second, 20*time.Millisecond)
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
	router := setupBulkRouter(t,
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

	router := setupBulkRouter(t,
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

	router := setupBulkRouter(t,
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

// setupBulkRouterWithBranches creates a test gin engine with a branchOverrideRepo wired in.
func setupBulkRouterWithBranches(
	t *testing.T,
	instanceRepo *MockStackInstanceRepository,
	branchOverrideRepo *MockChartBranchOverrideRepository,
	callerID, callerUsername, callerRole string,
) *gin.Engine {
	t.Helper()
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

	h, err := NewInstanceHandlerWithDeployer(
		instanceRepo, NewMockValueOverrideRepository(), branchOverrideRepo,
		NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
		NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
		valuesGen, userRepo,
		nil, nil, nil, nil, nil,
		0,
		&mockHandlerTxRunner{repos: database.TxRepos{
			StackInstance:  instanceRepo,
			BranchOverride: branchOverrideRepo,
		}},
	)
	require.NoError(t, err)

	bulk := r.Group("/api/v1/stack-instances/bulk")
	{
		bulk.POST("/delete", h.BulkDelete)
	}
	return r
}

func TestBulkDelete_WithBranchOverrides(t *testing.T) {
	t.Parallel()

	t.Run("deletes branch overrides before deleting instance", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		branchRepo := NewMockChartBranchOverrideRepository()

		seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)

		// Seed a branch override for the instance.
		_ = branchRepo.Set(&models.ChartBranchOverride{
			StackInstanceID: "i1",
			ChartConfigID:   "cc-1",
			Branch:          "feature/test",
		})

		router := setupBulkRouterWithBranches(t, instRepo, branchRepo, "uid-1", "testuser", "admin")

		body, _ := json.Marshal(BulkOperationRequest{InstanceIDs: []string{"i1"}})
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/bulk/delete", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp BulkOperationResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, 1, resp.Succeeded)
		assert.Equal(t, 0, resp.Failed)

		// Instance should be deleted.
		_, err := instRepo.FindByID("i1")
		assert.Error(t, err)

		// Branch overrides for the instance should also be gone.
		overrides, _ := branchRepo.List("i1")
		assert.Empty(t, overrides)
	})

	t.Run("branch override delete error fails the instance", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		branchRepo := NewMockChartBranchOverrideRepository()

		seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)
		branchRepo.SetError(assert.AnError)

		router := setupBulkRouterWithBranches(t, instRepo, branchRepo, "uid-1", "testuser", "admin")

		body, _ := json.Marshal(BulkOperationRequest{InstanceIDs: []string{"i1"}})
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/bulk/delete", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp BulkOperationResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, 0, resp.Succeeded)
		assert.Equal(t, 1, resp.Failed)

		// Instance should still exist since branch override delete failed.
		_, err := instRepo.FindByID("i1")
		assert.NoError(t, err)
	})
}

// Ensure unused imports don't cause issues.
var _ = time.Now

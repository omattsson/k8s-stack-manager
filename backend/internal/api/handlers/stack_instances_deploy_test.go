package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"backend/internal/cluster"
	"backend/internal/deployer"
	"backend/internal/helm"
	"backend/internal/k8s"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// ---- MockDeploymentLogRepository ----

type MockDeploymentLogRepository struct {
	mu    sync.RWMutex
	items map[string]*models.DeploymentLog
	err   error
}

func NewMockDeploymentLogRepository() *MockDeploymentLogRepository {
	return &MockDeploymentLogRepository{items: make(map[string]*models.DeploymentLog)}
}

func (m *MockDeploymentLogRepository) Create(_ context.Context, log *models.DeploymentLog) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	cp := *log
	m.items[log.ID] = &cp
	return nil
}

func (m *MockDeploymentLogRepository) FindByID(_ context.Context, id string) (*models.DeploymentLog, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	log, ok := m.items[id]
	if !ok {
		return nil, errors.New("not found")
	}
	cp := *log
	return &cp, nil
}

func (m *MockDeploymentLogRepository) Update(_ context.Context, log *models.DeploymentLog) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	cp := *log
	m.items[log.ID] = &cp
	return nil
}

func (m *MockDeploymentLogRepository) ListByInstance(_ context.Context, instanceID string) ([]models.DeploymentLog, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	var out []models.DeploymentLog
	for _, log := range m.items {
		if log.StackInstanceID == instanceID {
			out = append(out, *log)
		}
	}
	return out, nil
}

func (m *MockDeploymentLogRepository) ListByInstancePaginated(_ context.Context, filters models.DeploymentLogFilters) (*models.DeploymentLogResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	var out []models.DeploymentLog
	for _, log := range m.items {
		if log.StackInstanceID == filters.InstanceID {
			out = append(out, *log)
		}
	}
	// Sort by StartedAt DESC for consistency.
	sort.Slice(out, func(i, j int) bool {
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	total := int64(len(out))
	limit := filters.Limit
	if limit <= 0 {
		limit = 50
	}
	offset := filters.Offset
	if offset > len(out) {
		offset = len(out)
	}
	out = out[offset:]
	if limit < len(out) {
		out = out[:limit]
	}
	return &models.DeploymentLogResult{
		Data:  out,
		Total: total,
	}, nil
}

func (m *MockDeploymentLogRepository) GetLatestByInstance(_ context.Context, instanceID string) (*models.DeploymentLog, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var latest *models.DeploymentLog
	for _, log := range m.items {
		if log.StackInstanceID == instanceID {
			if latest == nil || log.StartedAt.After(latest.StartedAt) {
				cp := *log
				latest = &cp
			}
		}
	}
	if latest == nil {
		return nil, errors.New("not found")
	}
	return latest, nil
}

func (m *MockDeploymentLogRepository) SummarizeByInstance(_ context.Context, instanceID string) (*models.DeployLogSummary, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	summary := &models.DeployLogSummary{InstanceID: instanceID}
	for _, log := range m.items {
		if log.StackInstanceID != instanceID || log.Action != models.DeployActionDeploy {
			continue
		}
		summary.DeployCount++
		switch log.Status {
		case models.DeployLogSuccess:
			summary.SuccessCount++
		case models.DeployLogError:
			summary.ErrorCount++
		}
	}
	return summary, nil
}

func (m *MockDeploymentLogRepository) SummarizeBatch(ctx context.Context, instanceIDs []string) (map[string]*models.DeployLogSummary, error) {
	result := make(map[string]*models.DeployLogSummary, len(instanceIDs))
	for _, id := range instanceIDs {
		summary, err := m.SummarizeByInstance(ctx, id)
		if err != nil {
			return nil, err
		}
		result[id] = summary
	}
	return result, nil
}

func (m *MockDeploymentLogRepository) CountByAction(_ context.Context, _ string) (int, error) {
	return 0, nil
}

func (m *MockDeploymentLogRepository) SetError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

// ---- deploy router setup ----

// setupDeployRouter creates a test gin engine with deploy/stop/deploy-log/status routes.
func setupDeployRouter(
	t *testing.T,
	instanceRepo *MockStackInstanceRepository,
	overrideRepo *MockValueOverrideRepository,
	defRepo *MockStackDefinitionRepository,
	ccRepo *MockChartConfigRepository,
	tmplRepo *MockStackTemplateRepository,
	tmplChartRepo *MockTemplateChartConfigRepository,
	deployManager *deployer.Manager,
	k8sWatcher *k8s.Watcher,
	registry *cluster.Registry,
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

	h, err := NewInstanceHandlerWithDeployer(
		instanceRepo, overrideRepo, nil, defRepo, ccRepo,
		tmplRepo, tmplChartRepo, valuesGen, userRepo,
		deployManager, k8sWatcher, registry, deployLogRepo, nil,
		0, &mockHandlerTxRunner{},
	)
	require.NoError(t, err)

	insts := r.Group("/api/v1/stack-instances")
	{
		insts.POST("/:id/deploy", h.DeployInstance)
		insts.POST("/:id/stop", h.StopInstance)
		insts.POST("/:id/clean", h.CleanInstance)
		insts.GET("/:id/deploy-log", h.GetDeployLog)
		insts.GET("/:id/status", h.GetInstanceStatus)
		insts.GET("/:id/pods", h.GetInstancePods)
	}
	return r
}

// noopHelmExecutor is a no-op HelmExecutor for handler tests that only verify
// HTTP status codes and don't care about actual Helm output.
type noopHelmExecutor struct{}

func (n *noopHelmExecutor) Install(_ context.Context, _ deployer.InstallRequest) (string, error) {
	return "", nil
}

func (n *noopHelmExecutor) Uninstall(_ context.Context, _ deployer.UninstallRequest) (string, error) {
	return "", nil
}

func (n *noopHelmExecutor) Status(_ context.Context, name, _ string) (*deployer.ReleaseStatus, error) {
	return &deployer.ReleaseStatus{Name: name}, nil
}

func (n *noopHelmExecutor) ListReleases(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

func (n *noopHelmExecutor) History(_ context.Context, _ string, _ string, _ int) ([]deployer.ReleaseRevision, error) {
	return nil, nil
}

func (n *noopHelmExecutor) Rollback(_ context.Context, _ string, _ string, _ int) (string, error) {
	return "", nil
}

func (n *noopHelmExecutor) GetValues(_ context.Context, _ string, _ string, _ int) (string, error) {
	return "", nil
}

func (n *noopHelmExecutor) Timeout() time.Duration {
	return 30 * time.Second
}

// newTestManager creates a Manager with a test registry for handler tests.
func newTestManager(instRepo models.StackInstanceRepository, logRepo models.DeploymentLogRepository) *deployer.Manager {
	testRegistry := cluster.NewRegistryForTest("test-cluster", nil, &noopHelmExecutor{})
	return deployer.NewManager(deployer.ManagerConfig{
		Registry:      testRegistry,
		InstanceRepo:  instRepo,
		DeployLogRepo: logRepo,
		Hub:           &MockBroadcastSender{},
		MaxConcurrent: 2,
	})
}

// ---- DeployInstance tests ----

func TestDeployInstance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		instanceID string
		setup      func(*MockStackInstanceRepository, *MockStackDefinitionRepository, *MockChartConfigRepository)
		noManager  bool
		wantStatus int
		checkFn    func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:       "happy path — draft instance returns 202",
			instanceID: "i1",
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
			wantStatus: http.StatusAccepted,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp map[string]string
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.NotEmpty(t, resp["log_id"])
				assert.Equal(t, "Deployment started", resp["message"])
			},
		},
		{
			name:       "already deploying returns 409",
			instanceID: "i2",
			setup: func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, _ *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i2", "stack-b", "d1", "uid-1", models.StackStatusDeploying)
				seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
			},
			wantStatus: http.StatusConflict,
		},
		{
			name:       "running is not blocked by status check",
			instanceID: "i3",
			setup: func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, _ *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i3", "stack-c", "d1", "uid-1", models.StackStatusRunning)
				seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
			},
			// 400 from "no charts" — NOT 409 from status check.
			wantStatus: http.StatusBadRequest,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				assert.NotContains(t, w.Body.String(), "Cannot deploy")
			},
		},
		{
			name:       "not found returns 404",
			instanceID: "missing",
			setup:      func(_ *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository) {},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "no deploy manager returns 503",
			instanceID: "i4",
			setup: func(instRepo *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i4", "stack-d", "d1", "uid-1", models.StackStatusDraft)
			},
			noManager:  true,
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:       "stopped instance can be deployed",
			instanceID: "i5",
			setup: func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i5", "stack-e", "d1", "uid-1", models.StackStatusStopped)
				seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
				require.NoError(t, ccRepo.Create(&models.ChartConfig{
					ID:                "c2",
					StackDefinitionID: "d1",
					ChartName:         "redis",
					RepositoryURL:     "oci://example.com/charts/redis",
					DeployOrder:       1,
				}))
			},
			wantStatus: http.StatusAccepted,
		},
		{
			name:       "error instance can be deployed",
			instanceID: "i6",
			setup: func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i6", "stack-f", "d1", "uid-1", models.StackStatusError)
				seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
				require.NoError(t, ccRepo.Create(&models.ChartConfig{
					ID:                "c3",
					StackDefinitionID: "d1",
					ChartName:         "app",
					RepositoryURL:     "oci://example.com/charts/app",
					DeployOrder:       1,
				}))
			},
			wantStatus: http.StatusAccepted,
		},
		{
			name:       "no charts returns 400",
			instanceID: "i7",
			setup: func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, _ *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i7", "stack-g", "d1", "uid-1", models.StackStatusDraft)
				seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
				// No charts added to ccRepo.
			},
			wantStatus: http.StatusBadRequest,
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

			var mgr *deployer.Manager
			if !tt.noManager {
				mgr = newTestManager(instRepo, logRepo)
			}

			router := setupDeployRouter(t,
				instRepo, NewMockValueOverrideRepository(), defRepo, ccRepo,
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


// ---- StopInstance tests ----

func TestStopInstance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		instanceID string
		setup      func(*MockStackInstanceRepository, *MockStackDefinitionRepository, *MockChartConfigRepository)
		noManager  bool
		wantStatus int
		checkFn    func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:       "happy path — running instance returns 202",
			instanceID: "i1",
			setup: func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusRunning)
				seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
				seedChartConfig(t, ccRepo, "cc1", "d1", "nginx")
			},
			wantStatus: http.StatusAccepted,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp map[string]string
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.NotEmpty(t, resp["log_id"])
				assert.Equal(t, "Stop initiated", resp["message"])
			},
		},
		{
			name:       "deploying instance can be stopped",
			instanceID: "i2",
			setup: func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i2", "stack-b", "d1", "uid-1", models.StackStatusDeploying)
				seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
				seedChartConfig(t, ccRepo, "cc1", "d1", "nginx")
			},
			wantStatus: http.StatusAccepted,
		},
		{
			name:       "draft instance returns 409",
			instanceID: "i3",
			setup: func(instRepo *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i3", "stack-c", "d1", "uid-1", models.StackStatusDraft)
			},
			wantStatus: http.StatusConflict,
		},
		{
			name:       "stopped instance returns 409",
			instanceID: "i4",
			setup: func(instRepo *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i4", "stack-d", "d1", "uid-1", models.StackStatusStopped)
			},
			wantStatus: http.StatusConflict,
		},
		{
			name:       "not found returns 404",
			instanceID: "missing",
			setup:      func(_ *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository) {},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "no deploy manager returns 503",
			instanceID: "i5",
			setup: func(instRepo *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i5", "stack-e", "d1", "uid-1", models.StackStatusRunning)
			},
			noManager:  true,
			wantStatus: http.StatusServiceUnavailable,
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

			var mgr *deployer.Manager
			if !tt.noManager {
				mgr = newTestManager(instRepo, logRepo)
			}

			router := setupDeployRouter(t,
				instRepo, NewMockValueOverrideRepository(),
				defRepo, ccRepo,
				NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
				mgr, nil, nil, logRepo,
				"uid-1", "alice", "user",
			)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/"+tt.instanceID+"/stop", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.checkFn != nil {
				tt.checkFn(t, w)
			}
		})
	}
}


// ---- GetDeployLog tests ----

func TestGetDeployLog(t *testing.T) {
	t.Parallel()

	t.Run("returns logs for instance", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		logRepo := NewMockDeploymentLogRepository()
		seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusRunning)

		now := time.Now().UTC()
		require.NoError(t, logRepo.Create(context.Background(), &models.DeploymentLog{
			ID:              "log-1",
			StackInstanceID: "i1",
			Action:          models.DeployActionDeploy,
			Status:          models.DeployLogSuccess,
			StartedAt:       now,
		}))
		require.NoError(t, logRepo.Create(context.Background(), &models.DeploymentLog{
			ID:              "log-2",
			StackInstanceID: "i1",
			Action:          models.DeployActionStop,
			Status:          models.DeployLogRunning,
			StartedAt:       now.Add(1 * time.Minute),
		}))

		router := setupDeployRouter(t,
			instRepo, NewMockValueOverrideRepository(),
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, nil, logRepo,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/i1/deploy-log", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var result models.DeploymentLogResult
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
		assert.Len(t, result.Data, 2)
	})

	t.Run("empty logs returns empty array", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		logRepo := NewMockDeploymentLogRepository()
		seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)

		router := setupDeployRouter(t,
			instRepo, NewMockValueOverrideRepository(),
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, nil, logRepo,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/i1/deploy-log", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("instance not found returns 404", func(t *testing.T) {
		t.Parallel()

		logRepo := NewMockDeploymentLogRepository()
		router := setupDeployRouter(t,
			NewMockStackInstanceRepository(), NewMockValueOverrideRepository(),
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, nil, logRepo,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/missing/deploy-log", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("no log repo returns 503", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)

		// Pass nil interface (not a nil *MockDeploymentLogRepository which would be non-nil interface).
		var nilLogRepo models.DeploymentLogRepository
		router := setupDeployRouter(t,
			instRepo, NewMockValueOverrideRepository(),
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, nil, nilLogRepo,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/i1/deploy-log", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	})
}

// ---- GetInstanceStatus tests ----

func TestGetInstanceStatus(t *testing.T) {
	t.Parallel()

	t.Run("returns 503 when no watcher or client configured", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusRunning)

		router := setupDeployRouter(t,
			instRepo, NewMockValueOverrideRepository(),
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, nil, NewMockDeploymentLogRepository(),
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/i1/status", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
		var resp map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Contains(t, resp["error"], "not configured")
	})

	t.Run("instance not found returns 404", func(t *testing.T) {
		t.Parallel()

		router := setupDeployRouter(t,
			NewMockStackInstanceRepository(), NewMockValueOverrideRepository(),
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, nil, NewMockDeploymentLogRepository(),
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/missing/status", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("falls back to direct k8s client", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusRunning)

		// Create a fake K8s client with the namespace.
		cs := fake.NewSimpleClientset(&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "stack-stack-a-owner"},
		})
		k8sClient := k8s.NewClientFromInterface(cs)
		registry := cluster.NewRegistryForTest("default", k8sClient, nil)

		router := setupDeployRouter(t,
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
		var resp k8s.NamespaceStatus
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "stack-stack-a-owner", resp.Namespace)
		assert.Equal(t, "healthy", resp.Status)
	})
}

// ---- GetInstancePods tests ----

func TestGetInstancePods(t *testing.T) {
	t.Parallel()

	t.Run("returns 503 when no registry configured", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusRunning)

		router := setupDeployRouter(t,
			instRepo, NewMockValueOverrideRepository(),
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, nil, NewMockDeploymentLogRepository(),
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/i1/pods", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
		var resp map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Contains(t, resp["error"], "not configured")
	})

	t.Run("instance not found returns 404", func(t *testing.T) {
		t.Parallel()

		router := setupDeployRouter(t,
			NewMockStackInstanceRepository(), NewMockValueOverrideRepository(),
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, nil, NewMockDeploymentLogRepository(),
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/missing/pods", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("success — returns pod and event data", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusRunning)

		now := time.Now()
		// Create a fake K8s client with a namespace, a pod, and a warning event.
		cs := fake.NewSimpleClientset(
			&corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: "stack-stack-a-owner"},
			},
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "nginx-abc123",
					Namespace: "stack-stack-a-owner",
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
				},
			},
			&corev1.Event{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "nginx-abc123.oom",
					Namespace: "stack-stack-a-owner",
				},
				InvolvedObject: corev1.ObjectReference{
					Kind: "Pod",
					Name: "nginx-abc123",
				},
				Type:           "Warning",
				Reason:         "OOMKilled",
				Message:        "Container exceeded memory limit",
				Count:          1,
				FirstTimestamp: metav1.NewTime(now),
				LastTimestamp:  metav1.NewTime(now),
			},
		)
		k8sClient := k8s.NewClientFromInterface(cs)
		registry := cluster.NewRegistryForTest("default", k8sClient, nil)

		router := setupDeployRouter(t,
			instRepo, NewMockValueOverrideRepository(),
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, registry, NewMockDeploymentLogRepository(),
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/i1/pods", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp k8s.NamespaceStatus
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "stack-stack-a-owner", resp.Namespace)
		require.NotEmpty(t, resp.Events, "expected events from IncludeEvents=true")
		assert.Equal(t, "Warning", resp.Events[0].Type)
		assert.Equal(t, "OOMKilled", resp.Events[0].Reason)
	})

	t.Run("unknown cluster_id returns 400", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		inst := seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusRunning)
		inst.ClusterID = "nonexistent-cluster"
		require.NoError(t, instRepo.Update(inst))

		// Use NewRegistry with an empty cluster repo so GetClients returns ErrNotFound.
		registry := cluster.NewRegistry(cluster.RegistryOptions{
			ClusterRepo: NewMockClusterRepository(),
		})

		router := setupDeployRouter(t,
			instRepo, NewMockValueOverrideRepository(),
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, registry, NewMockDeploymentLogRepository(),
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/i1/pods", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		var resp map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Contains(t, resp["error"], "Unknown cluster_id")
	})
}

// ---- CleanInstance tests ----

func TestCleanInstance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		instanceID string
		setup      func(*MockStackInstanceRepository, *MockStackDefinitionRepository, *MockChartConfigRepository)
		noManager  bool
		wantStatus int
		checkFn    func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:       "running instance returns 202",
			instanceID: "i1",
			setup: func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusRunning)
				seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
				seedChartConfig(t, ccRepo, "cc1", "d1", "nginx")
			},
			wantStatus: http.StatusAccepted,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp map[string]string
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.NotEmpty(t, resp["log_id"])
				assert.Equal(t, "Namespace cleanup initiated", resp["message"])
			},
		},
		{
			name:       "stopped instance returns 202",
			instanceID: "i2",
			setup: func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i2", "stack-b", "d1", "uid-1", models.StackStatusStopped)
				seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
				seedChartConfig(t, ccRepo, "cc1", "d1", "nginx")
			},
			wantStatus: http.StatusAccepted,
		},
		{
			name:       "error instance returns 202",
			instanceID: "i3",
			setup: func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i3", "stack-c", "d1", "uid-1", models.StackStatusError)
				seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
				seedChartConfig(t, ccRepo, "cc1", "d1", "nginx")
			},
			wantStatus: http.StatusAccepted,
		},
		{
			name:       "draft instance returns 409",
			instanceID: "i4",
			setup: func(instRepo *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i4", "stack-d", "d1", "uid-1", models.StackStatusDraft)
			},
			wantStatus: http.StatusConflict,
		},
		{
			name:       "deploying instance returns 409",
			instanceID: "i5",
			setup: func(instRepo *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i5", "stack-e", "d1", "uid-1", models.StackStatusDeploying)
			},
			wantStatus: http.StatusConflict,
		},
		{
			name:       "cleaning instance returns 409",
			instanceID: "i6",
			setup: func(instRepo *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i6", "stack-f", "d1", "uid-1", models.StackStatusCleaning)
			},
			wantStatus: http.StatusConflict,
		},
		{
			name:       "nil deploy manager returns 503",
			instanceID: "i7",
			setup: func(instRepo *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i7", "stack-g", "d1", "uid-1", models.StackStatusRunning)
			},
			noManager:  true,
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:       "instance not found returns 404",
			instanceID: "missing",
			setup:      func(_ *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository) {},
			wantStatus: http.StatusNotFound,
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

			var mgr *deployer.Manager
			if !tt.noManager {
				mgr = newTestManager(instRepo, logRepo)
			}

			router := setupDeployRouter(t,
				instRepo, NewMockValueOverrideRepository(),
				defRepo, ccRepo,
				NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
				mgr, nil, nil, logRepo,
				"uid-1", "alice", "user",
			)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/"+tt.instanceID+"/clean", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.checkFn != nil {
				tt.checkFn(t, w)
			}
		})
	}
}

// setupRollbackRouter creates a test gin engine with rollback and deploy-log-values routes.
func setupRollbackRouter(
	t *testing.T,
	instanceRepo *MockStackInstanceRepository,
	defRepo *MockStackDefinitionRepository,
	ccRepo *MockChartConfigRepository,
	deployManager *deployer.Manager,
	deployLogRepo models.DeploymentLogRepository,
	callerID, callerUsername string,
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
		c.Set("role", "user")
		c.Next()
	})

	valuesGen := helm.NewValuesGenerator()
	userRepo := NewMockUserRepository()

	h, err := NewInstanceHandlerWithDeployer(
		instanceRepo, NewMockValueOverrideRepository(), nil, defRepo, ccRepo,
		NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
		valuesGen, userRepo,
		deployManager, nil, nil, deployLogRepo, nil,
		0, &mockHandlerTxRunner{},
	)
	require.NoError(t, err)

	insts := r.Group("/api/v1/stack-instances")
	{
		insts.POST("/:id/rollback", h.RollbackInstance)
		insts.GET("/:id/deploy-log/:logId/values", h.GetDeployLogValues)
	}
	return r
}

// ---- RollbackInstance tests ----

func TestRollbackInstance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		instanceID string
		body       string
		setup      func(*MockStackInstanceRepository, *MockStackDefinitionRepository, *MockChartConfigRepository)
		noManager  bool
		wantStatus int
		checkFn    func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:       "success — running instance returns 202 with log_id",
			instanceID: "i1",
			body:       `{}`,
			setup: func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusRunning)
				seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
				seedChartConfig(t, ccRepo, "cc1", "d1", "nginx")
			},
			wantStatus: http.StatusAccepted,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp map[string]string
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.NotEmpty(t, resp["log_id"])
				assert.Equal(t, "Rollback started", resp["message"])
			},
		},
		{
			name:       "error instance can be rolled back",
			instanceID: "i2",
			body:       `{}`,
			setup: func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i2", "stack-b", "d1", "uid-1", models.StackStatusError)
				seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
				seedChartConfig(t, ccRepo, "cc2", "d1", "redis")
			},
			wantStatus: http.StatusAccepted,
		},
		{
			name:       "draft instance returns 409",
			instanceID: "i3",
			setup: func(instRepo *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i3", "stack-c", "d1", "uid-1", models.StackStatusDraft)
			},
			wantStatus: http.StatusConflict,
		},
		{
			name:       "stopped instance returns 409",
			instanceID: "i4",
			setup: func(instRepo *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i4", "stack-d", "d1", "uid-1", models.StackStatusStopped)
			},
			wantStatus: http.StatusConflict,
		},
		{
			name:       "instance not found returns 404",
			instanceID: "missing",
			setup:      func(_ *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository) {},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "no charts configured returns 400",
			instanceID: "i5",
			setup: func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, _ *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i5", "stack-e", "d1", "uid-1", models.StackStatusRunning)
				seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
				// No charts added to ccRepo.
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "nil deploy manager returns 503",
			instanceID: "i6",
			setup: func(instRepo *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i6", "stack-f", "d1", "uid-1", models.StackStatusRunning)
			},
			noManager:  true,
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:       "malformed JSON body returns 400",
			instanceID: "i7",
			body:       `{bad json`,
			setup: func(instRepo *MockStackInstanceRepository, _ *MockStackDefinitionRepository, _ *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i7", "stack-g", "d1", "uid-1", models.StackStatusRunning)
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "optional target_log_id accepted",
			instanceID: "i8",
			body:       `{"target_log_id":"log-abc"}`,
			setup: func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository) {
				seedInstance(t, instRepo, "i8", "stack-h", "d1", "uid-1", models.StackStatusRunning)
				seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
				seedChartConfig(t, ccRepo, "cc3", "d1", "postgres")
			},
			wantStatus: http.StatusAccepted,
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

			var mgr *deployer.Manager
			if !tt.noManager {
				mgr = newTestManager(instRepo, logRepo)
			}

			router := setupRollbackRouter(t,
				instRepo, defRepo, ccRepo,
				mgr, logRepo,
				"uid-1", "alice",
			)

			body := tt.body
			if body == "" {
				body = "{}"
			}

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/"+tt.instanceID+"/rollback", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.checkFn != nil {
				tt.checkFn(t, w)
			}
		})
	}
}

// ---- GetDeployLogValues tests ----

func TestGetDeployLogValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		instanceID string
		logID      string
		setup      func(*MockStackInstanceRepository, *MockDeploymentLogRepository)
		wantStatus int
		checkFn    func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:       "success — log with values snapshot returns 200",
			instanceID: "i1",
			logID:      "log-1",
			setup: func(instRepo *MockStackInstanceRepository, logRepo *MockDeploymentLogRepository) {
				seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusRunning)
				require.NoError(t, logRepo.Create(context.Background(), &models.DeploymentLog{
					ID:              "log-1",
					StackInstanceID: "i1",
					Action:          models.DeployActionDeploy,
					Status:          models.DeployLogSuccess,
					StartedAt:       time.Now().UTC(),
					ValuesSnapshot:  `{"replicas":3,"image":"myapp:v2"}`,
				}))
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp map[string]interface{}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, "log-1", resp["log_id"])
				vals, ok := resp["values"].(map[string]interface{})
				require.True(t, ok, "values should be a JSON object")
				assert.Equal(t, float64(3), vals["replicas"])
			},
		},
		{
			name:       "log exists but no values snapshot returns 200 with null values",
			instanceID: "i2",
			logID:      "log-2",
			setup: func(instRepo *MockStackInstanceRepository, logRepo *MockDeploymentLogRepository) {
				seedInstance(t, instRepo, "i2", "stack-b", "d1", "uid-1", models.StackStatusRunning)
				require.NoError(t, logRepo.Create(context.Background(), &models.DeploymentLog{
					ID:              "log-2",
					StackInstanceID: "i2",
					Action:          models.DeployActionDeploy,
					Status:          models.DeployLogSuccess,
					StartedAt:       time.Now().UTC(),
					ValuesSnapshot:  "", // no snapshot
				}))
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp map[string]interface{}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, "log-2", resp["log_id"])
				assert.Nil(t, resp["values"])
			},
		},
		{
			name:       "log not found returns 404",
			instanceID: "i3",
			logID:      "nonexistent-log",
			setup: func(instRepo *MockStackInstanceRepository, _ *MockDeploymentLogRepository) {
				seedInstance(t, instRepo, "i3", "stack-c", "d1", "uid-1", models.StackStatusRunning)
			},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "log belongs to different instance returns 404",
			instanceID: "i4",
			logID:      "log-other",
			setup: func(instRepo *MockStackInstanceRepository, logRepo *MockDeploymentLogRepository) {
				seedInstance(t, instRepo, "i4", "stack-d", "d1", "uid-1", models.StackStatusRunning)
				// Log belongs to a different instance.
				require.NoError(t, logRepo.Create(context.Background(), &models.DeploymentLog{
					ID:              "log-other",
					StackInstanceID: "i99", // wrong instance
					Action:          models.DeployActionDeploy,
					Status:          models.DeployLogSuccess,
					StartedAt:       time.Now().UTC(),
					ValuesSnapshot:  `{"key":"val"}`,
				}))
			},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "instance not found returns 404",
			instanceID: "missing-inst",
			logID:      "log-x",
			setup:      func(_ *MockStackInstanceRepository, _ *MockDeploymentLogRepository) {},
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			instRepo := NewMockStackInstanceRepository()
			logRepo := NewMockDeploymentLogRepository()
			tt.setup(instRepo, logRepo)

			router := setupRollbackRouter(t,
				instRepo, NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
				nil, logRepo,
				"uid-1", "alice",
			)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet,
				"/api/v1/stack-instances/"+tt.instanceID+"/deploy-log/"+tt.logID+"/values",
				nil,
			)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.checkFn != nil {
				tt.checkFn(t, w)
			}
		})
	}
}

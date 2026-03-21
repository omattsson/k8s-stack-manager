package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- DeploymentLogRepository mock (analytics-specific) ----

type mockDeployLogRepo struct {
	mu    sync.RWMutex
	items map[string][]models.DeploymentLog // keyed by StackInstanceID
	err   error
}

func newMockDeployLogRepo() *mockDeployLogRepo {
	return &mockDeployLogRepo{items: make(map[string][]models.DeploymentLog)}
}

func (m *mockDeployLogRepo) Create(_ context.Context, l *models.DeploymentLog) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.items[l.StackInstanceID] = append(m.items[l.StackInstanceID], *l)
	return nil
}

func (m *mockDeployLogRepo) FindByID(_ context.Context, id string) (*models.DeploymentLog, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, logs := range m.items {
		for i := range logs {
			if logs[i].ID == id {
				cp := logs[i]
				return &cp, nil
			}
		}
	}
	return nil, nil
}

func (m *mockDeployLogRepo) Update(_ context.Context, l *models.DeploymentLog) error {
	return nil
}

func (m *mockDeployLogRepo) ListByInstance(_ context.Context, instanceID string) ([]models.DeploymentLog, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	logs := m.items[instanceID]
	out := make([]models.DeploymentLog, len(logs))
	copy(out, logs)
	return out, nil
}

func (m *mockDeployLogRepo) GetLatestByInstance(_ context.Context, instanceID string) (*models.DeploymentLog, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	logs := m.items[instanceID]
	if len(logs) == 0 {
		return nil, nil
	}
	cp := logs[len(logs)-1]
	return &cp, nil
}

func (m *mockDeployLogRepo) setError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

// ---- Test setup ----

func setupAnalyticsRouter(
	templateRepo *MockStackTemplateRepository,
	definitionRepo *MockStackDefinitionRepository,
	instanceRepo *MockStackInstanceRepository,
	deployLogRepo *mockDeployLogRepo,
	userRepo *MockUserRepository,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(injectAuthContext("admin-1", "admin"))

	h := NewAnalyticsHandler(templateRepo, definitionRepo, instanceRepo, deployLogRepo, userRepo)

	analytics := r.Group("/api/v1/analytics")
	{
		analytics.GET("/overview", h.GetOverview)
		analytics.GET("/templates", h.GetTemplateStats)
		analytics.GET("/users", h.GetUserStats)
	}
	return r
}

// ---- GetOverview tests ----

func TestGetOverview(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		setupFn        func(*MockStackTemplateRepository, *MockStackDefinitionRepository, *MockStackInstanceRepository, *mockDeployLogRepo, *MockUserRepository)
		expectedStatus int
		checkBody      func(*testing.T, OverviewStats)
	}{
		{
			name: "empty platform",
			setupFn: func(_ *MockStackTemplateRepository, _ *MockStackDefinitionRepository, _ *MockStackInstanceRepository, _ *mockDeployLogRepo, _ *MockUserRepository) {
			},
			expectedStatus: http.StatusOK,
			checkBody: func(t *testing.T, s OverviewStats) {
				t.Helper()
				assert.Equal(t, 0, s.TotalTemplates)
				assert.Equal(t, 0, s.TotalDefinitions)
				assert.Equal(t, 0, s.TotalInstances)
				assert.Equal(t, 0, s.RunningInstances)
				assert.Equal(t, 0, s.TotalDeploys)
				assert.Equal(t, 0, s.TotalUsers)
			},
		},
		{
			name: "counts everything correctly",
			setupFn: func(
				tmplRepo *MockStackTemplateRepository,
				defRepo *MockStackDefinitionRepository,
				instRepo *MockStackInstanceRepository,
				logRepo *mockDeployLogRepo,
				userRepo *MockUserRepository,
			) {
				_ = tmplRepo.Create(&models.StackTemplate{ID: "t1", Name: "Template 1"})
				_ = tmplRepo.Create(&models.StackTemplate{ID: "t2", Name: "Template 2"})
				_ = defRepo.Create(&models.StackDefinition{ID: "d1", SourceTemplateID: "t1"})
				_ = instRepo.Create(&models.StackInstance{ID: "i1", StackDefinitionID: "d1", OwnerID: "u1", Status: "running"})
				_ = instRepo.Create(&models.StackInstance{ID: "i2", StackDefinitionID: "d1", OwnerID: "u2", Status: "stopped"})
				_ = logRepo.Create(context.Background(), &models.DeploymentLog{ID: "l1", StackInstanceID: "i1", Action: "deploy", Status: "success"})
				_ = logRepo.Create(context.Background(), &models.DeploymentLog{ID: "l2", StackInstanceID: "i1", Action: "deploy", Status: "error"})
				_ = logRepo.Create(context.Background(), &models.DeploymentLog{ID: "l3", StackInstanceID: "i1", Action: "stop", Status: "success"})
				_ = userRepo.Create(&models.User{ID: "u1", Username: "alice"})
				_ = userRepo.Create(&models.User{ID: "u2", Username: "bob"})
			},
			expectedStatus: http.StatusOK,
			checkBody: func(t *testing.T, s OverviewStats) {
				t.Helper()
				assert.Equal(t, 2, s.TotalTemplates)
				assert.Equal(t, 1, s.TotalDefinitions)
				assert.Equal(t, 2, s.TotalInstances)
				assert.Equal(t, 1, s.RunningInstances)
				assert.Equal(t, 2, s.TotalDeploys) // only "deploy" actions count
				assert.Equal(t, 2, s.TotalUsers)
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmplRepo := NewMockStackTemplateRepository()
			defRepo := NewMockStackDefinitionRepository()
			instRepo := NewMockStackInstanceRepository()
			logRepo := newMockDeployLogRepo()
			userRepo := NewMockUserRepository()

			tt.setupFn(tmplRepo, defRepo, instRepo, logRepo, userRepo)

			router := setupAnalyticsRouter(tmplRepo, defRepo, instRepo, logRepo, userRepo)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/api/v1/analytics/overview", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)

			if tt.checkBody != nil {
				var stats OverviewStats
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &stats))
				tt.checkBody(t, stats)
			}
		})
	}
}

// ---- GetTemplateStats tests ----

func TestGetTemplateStats(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		setupFn        func(*MockStackTemplateRepository, *MockStackDefinitionRepository, *MockStackInstanceRepository, *mockDeployLogRepo, *MockUserRepository)
		expectedStatus int
		checkBody      func(*testing.T, []TemplateStats)
	}{
		{
			name: "no templates returns empty array",
			setupFn: func(_ *MockStackTemplateRepository, _ *MockStackDefinitionRepository, _ *MockStackInstanceRepository, _ *mockDeployLogRepo, _ *MockUserRepository) {
			},
			expectedStatus: http.StatusOK,
			checkBody: func(t *testing.T, stats []TemplateStats) {
				t.Helper()
				assert.Empty(t, stats)
			},
		},
		{
			name: "template with deploys shows correct stats",
			setupFn: func(
				tmplRepo *MockStackTemplateRepository,
				defRepo *MockStackDefinitionRepository,
				instRepo *MockStackInstanceRepository,
				logRepo *mockDeployLogRepo,
				_ *MockUserRepository,
			) {
				_ = tmplRepo.Create(&models.StackTemplate{ID: "t1", Name: "Web Stack", Category: "Full Stack", IsPublished: true})
				_ = tmplRepo.Create(&models.StackTemplate{ID: "t2", Name: "Empty Template", Category: "Other", IsPublished: false})
				_ = defRepo.Create(&models.StackDefinition{ID: "d1", SourceTemplateID: "t1"})
				_ = defRepo.Create(&models.StackDefinition{ID: "d2", SourceTemplateID: "t1"})
				_ = defRepo.Create(&models.StackDefinition{ID: "d3"}) // standalone, no template
				_ = instRepo.Create(&models.StackInstance{ID: "i1", StackDefinitionID: "d1", OwnerID: "u1", Status: "running"})
				_ = instRepo.Create(&models.StackInstance{ID: "i2", StackDefinitionID: "d2", OwnerID: "u2", Status: "stopped"})
				_ = logRepo.Create(context.Background(), &models.DeploymentLog{ID: "l1", StackInstanceID: "i1", Action: "deploy", Status: "success"})
				_ = logRepo.Create(context.Background(), &models.DeploymentLog{ID: "l2", StackInstanceID: "i1", Action: "deploy", Status: "success"})
				_ = logRepo.Create(context.Background(), &models.DeploymentLog{ID: "l3", StackInstanceID: "i2", Action: "deploy", Status: "error"})
				_ = logRepo.Create(context.Background(), &models.DeploymentLog{ID: "l4", StackInstanceID: "i2", Action: "stop", Status: "success"}) // not a deploy
			},
			expectedStatus: http.StatusOK,
			checkBody: func(t *testing.T, stats []TemplateStats) {
				t.Helper()
				require.Len(t, stats, 2)

				// Find t1 and t2 in results (order is not guaranteed from map iteration).
				var t1, t2 *TemplateStats
				for i := range stats {
					switch stats[i].TemplateID {
					case "t1":
						t1 = &stats[i]
					case "t2":
						t2 = &stats[i]
					}
				}

				require.NotNil(t, t1, "expected template t1 in results")
				assert.Equal(t, "Web Stack", t1.TemplateName)
				assert.Equal(t, "Full Stack", t1.Category)
				assert.True(t, t1.IsPublished)
				assert.Equal(t, 2, t1.DefinitionCount)
				assert.Equal(t, 2, t1.InstanceCount)
				assert.Equal(t, 3, t1.DeployCount)
				assert.Equal(t, 2, t1.SuccessCount)
				assert.Equal(t, 1, t1.ErrorCount)
				assert.InDelta(t, 66.66, t1.SuccessRate, 0.7)

				require.NotNil(t, t2, "expected template t2 in results")
				assert.Equal(t, "Empty Template", t2.TemplateName)
				assert.Equal(t, 0, t2.DefinitionCount)
				assert.Equal(t, 0, t2.InstanceCount)
				assert.Equal(t, 0, t2.DeployCount)
				assert.InDelta(t, 0.0, t2.SuccessRate, 0.01)
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmplRepo := NewMockStackTemplateRepository()
			defRepo := NewMockStackDefinitionRepository()
			instRepo := NewMockStackInstanceRepository()
			logRepo := newMockDeployLogRepo()
			userRepo := NewMockUserRepository()

			tt.setupFn(tmplRepo, defRepo, instRepo, logRepo, userRepo)

			router := setupAnalyticsRouter(tmplRepo, defRepo, instRepo, logRepo, userRepo)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/api/v1/analytics/templates", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)

			if tt.checkBody != nil {
				var stats []TemplateStats
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &stats))
				tt.checkBody(t, stats)
			}
		})
	}
}

// ---- GetUserStats tests ----

func TestGetUserStats(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Truncate(time.Second)
	earlier := now.Add(-2 * time.Hour)

	tests := []struct {
		name           string
		setupFn        func(*MockStackTemplateRepository, *MockStackDefinitionRepository, *MockStackInstanceRepository, *mockDeployLogRepo, *MockUserRepository)
		expectedStatus int
		checkBody      func(*testing.T, []UserStats)
	}{
		{
			name: "no users returns empty array",
			setupFn: func(_ *MockStackTemplateRepository, _ *MockStackDefinitionRepository, _ *MockStackInstanceRepository, _ *mockDeployLogRepo, _ *MockUserRepository) {
			},
			expectedStatus: http.StatusOK,
			checkBody: func(t *testing.T, stats []UserStats) {
				t.Helper()
				assert.Empty(t, stats)
			},
		},
		{
			name: "per-user counts and last active",
			setupFn: func(
				_ *MockStackTemplateRepository,
				_ *MockStackDefinitionRepository,
				instRepo *MockStackInstanceRepository,
				logRepo *mockDeployLogRepo,
				userRepo *MockUserRepository,
			) {
				_ = userRepo.Create(&models.User{ID: "u1", Username: "alice"})
				_ = userRepo.Create(&models.User{ID: "u2", Username: "bob"})

				_ = instRepo.Create(&models.StackInstance{ID: "i1", StackDefinitionID: "d1", OwnerID: "u1", Status: "running"})
				_ = instRepo.Create(&models.StackInstance{ID: "i2", StackDefinitionID: "d1", OwnerID: "u1", Status: "stopped"})
				_ = instRepo.Create(&models.StackInstance{ID: "i3", StackDefinitionID: "d2", OwnerID: "u2", Status: "draft"})

				_ = logRepo.Create(context.Background(), &models.DeploymentLog{ID: "l1", StackInstanceID: "i1", Action: "deploy", Status: "success", StartedAt: earlier})
				_ = logRepo.Create(context.Background(), &models.DeploymentLog{ID: "l2", StackInstanceID: "i1", Action: "deploy", Status: "success", StartedAt: now, CompletedAt: &now})
				_ = logRepo.Create(context.Background(), &models.DeploymentLog{ID: "l3", StackInstanceID: "i1", Action: "stop", Status: "success", StartedAt: now})
			},
			expectedStatus: http.StatusOK,
			checkBody: func(t *testing.T, stats []UserStats) {
				t.Helper()
				require.Len(t, stats, 2)

				var alice, bob *UserStats
				for i := range stats {
					switch stats[i].Username {
					case "alice":
						alice = &stats[i]
					case "bob":
						bob = &stats[i]
					}
				}

				require.NotNil(t, alice)
				assert.Equal(t, 2, alice.InstanceCount)
				assert.Equal(t, 2, alice.DeployCount) // only "deploy" actions
				require.NotNil(t, alice.LastActive)
				assert.Equal(t, now, alice.LastActive.Truncate(time.Second))

				require.NotNil(t, bob)
				assert.Equal(t, 1, bob.InstanceCount)
				assert.Equal(t, 0, bob.DeployCount)
				assert.Nil(t, bob.LastActive)
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmplRepo := NewMockStackTemplateRepository()
			defRepo := NewMockStackDefinitionRepository()
			instRepo := NewMockStackInstanceRepository()
			logRepo := newMockDeployLogRepo()
			userRepo := NewMockUserRepository()

			tt.setupFn(tmplRepo, defRepo, instRepo, logRepo, userRepo)

			router := setupAnalyticsRouter(tmplRepo, defRepo, instRepo, logRepo, userRepo)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/api/v1/analytics/users", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)

			if tt.checkBody != nil {
				var stats []UserStats
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &stats))
				tt.checkBody(t, stats)
			}
		})
	}
}

// ---- failingUserRepo for error tests (MockUserRepository.List doesn't support errors) ----

type failingUserRepo struct{ MockUserRepository }

func (f *failingUserRepo) List() ([]models.User, error) { return nil, assert.AnError }

// ---- Error propagation tests ----

func TestAnalytics_RepoErrors(t *testing.T) {
	t.Parallel()

	t.Run("overview - template repo error", func(t *testing.T) {
		t.Parallel()
		tmplRepo := NewMockStackTemplateRepository()
		tmplRepo.SetError(assert.AnError)
		router := setupAnalyticsRouter(tmplRepo, NewMockStackDefinitionRepository(), NewMockStackInstanceRepository(), newMockDeployLogRepo(), NewMockUserRepository())
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/analytics/overview", nil)
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})

	t.Run("overview - instance repo error", func(t *testing.T) {
		t.Parallel()
		instRepo := NewMockStackInstanceRepository()
		instRepo.SetError(assert.AnError)
		router := setupAnalyticsRouter(NewMockStackTemplateRepository(), NewMockStackDefinitionRepository(), instRepo, newMockDeployLogRepo(), NewMockUserRepository())
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/analytics/overview", nil)
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})

	t.Run("template stats - template repo error", func(t *testing.T) {
		t.Parallel()
		tmplRepo := NewMockStackTemplateRepository()
		tmplRepo.SetError(assert.AnError)
		router := setupAnalyticsRouter(tmplRepo, NewMockStackDefinitionRepository(), NewMockStackInstanceRepository(), newMockDeployLogRepo(), NewMockUserRepository())
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/analytics/templates", nil)
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})

	t.Run("user stats - user repo error", func(t *testing.T) {
		t.Parallel()
		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.Use(injectAuthContext("admin-1", "admin"))
		failUser := &failingUserRepo{*NewMockUserRepository()}
		h := NewAnalyticsHandler(NewMockStackTemplateRepository(), NewMockStackDefinitionRepository(), NewMockStackInstanceRepository(), newMockDeployLogRepo(), failUser)
		r.GET("/api/v1/analytics/users", h.GetUserStats)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/analytics/users", nil)
		r.ServeHTTP(w, req)
		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})
}

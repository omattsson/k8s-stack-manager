package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"backend/internal/models"
	"backend/internal/scheduler"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// mockCleanupPolicyRepo is a minimal in-memory mock for models.CleanupPolicyRepository.
type mockCleanupPolicyRepo struct {
	mu       sync.Mutex
	policies map[string]*models.CleanupPolicy
}

func newMockCleanupPolicyRepo() *mockCleanupPolicyRepo {
	return &mockCleanupPolicyRepo{policies: make(map[string]*models.CleanupPolicy)}
}

func (r *mockCleanupPolicyRepo) Create(p *models.CleanupPolicy) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p.ID == "" {
		p.ID = "test-id"
	}
	now := time.Now().UTC()
	p.CreatedAt = now
	p.UpdatedAt = now
	r.policies[p.ID] = p
	return nil
}

func (r *mockCleanupPolicyRepo) FindByID(id string) (*models.CleanupPolicy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.policies[id]
	if !ok {
		return nil, dberrors.NewDatabaseError("find_by_id", dberrors.ErrNotFound)
	}
	return p, nil
}

func (r *mockCleanupPolicyRepo) Update(p *models.CleanupPolicy) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.policies[p.ID]; !ok {
		return dberrors.NewDatabaseError("update", dberrors.ErrNotFound)
	}
	p.UpdatedAt = time.Now().UTC()
	r.policies[p.ID] = p
	return nil
}

func (r *mockCleanupPolicyRepo) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.policies[id]; !ok {
		return dberrors.NewDatabaseError("delete", dberrors.ErrNotFound)
	}
	delete(r.policies, id)
	return nil
}

func (r *mockCleanupPolicyRepo) List() ([]models.CleanupPolicy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []models.CleanupPolicy
	for _, p := range r.policies {
		result = append(result, *p)
	}
	return result, nil
}

func (r *mockCleanupPolicyRepo) ListEnabled() ([]models.CleanupPolicy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []models.CleanupPolicy
	for _, p := range r.policies {
		if p.Enabled {
			result = append(result, *p)
		}
	}
	return result, nil
}

// failingCleanupPolicyRepo always returns errors.
type failingCleanupPolicyRepo struct{}

func (r *failingCleanupPolicyRepo) Create(_ *models.CleanupPolicy) error {
	return errors.New("db failure")
}
func (r *failingCleanupPolicyRepo) FindByID(_ string) (*models.CleanupPolicy, error) {
	return nil, dberrors.NewDatabaseError("find_by_id", dberrors.ErrNotFound)
}
func (r *failingCleanupPolicyRepo) Update(_ *models.CleanupPolicy) error {
	return errors.New("db failure")
}
func (r *failingCleanupPolicyRepo) Delete(_ string) error {
	return dberrors.NewDatabaseError("delete", dberrors.ErrNotFound)
}
func (r *failingCleanupPolicyRepo) List() ([]models.CleanupPolicy, error) {
	return nil, errors.New("db failure")
}
func (r *failingCleanupPolicyRepo) ListEnabled() ([]models.CleanupPolicy, error) {
	return nil, nil
}

func setupCleanupPolicyRouter(repo models.CleanupPolicyRepository, sched *scheduler.Scheduler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewCleanupPolicyHandler(repo, sched)
	g := r.Group("/api/v1/admin/cleanup-policies")
	{
		g.GET("", h.ListCleanupPolicies)
		g.POST("", h.CreateCleanupPolicy)
		g.PUT("/:id", h.UpdateCleanupPolicy)
		g.DELETE("/:id", h.DeleteCleanupPolicy)
		g.POST("/:id/run", h.RunCleanupPolicy)
	}
	return r
}

func TestListCleanupPolicies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setup      func(*mockCleanupPolicyRepo)
		wantStatus int
		wantCount  int
	}{
		{
			name:       "empty list",
			setup:      func(_ *mockCleanupPolicyRepo) {},
			wantStatus: http.StatusOK,
			wantCount:  0,
		},
		{
			name: "with policies",
			setup: func(repo *mockCleanupPolicyRepo) {
				_ = repo.Create(&models.CleanupPolicy{
					ID: "p1", Name: "test", Action: "stop",
					Condition: "idle_days:7", Schedule: "0 2 * * *",
					ClusterID: "all", Enabled: true,
				})
			},
			wantStatus: http.StatusOK,
			wantCount:  1,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := newMockCleanupPolicyRepo()
			tt.setup(repo)
			router := setupCleanupPolicyRouter(repo, nil)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("GET", "/api/v1/admin/cleanup-policies", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.wantStatus == http.StatusOK {
				var policies []models.CleanupPolicy
				err := json.Unmarshal(w.Body.Bytes(), &policies)
				assert.NoError(t, err)
				assert.Len(t, policies, tt.wantCount)
			}
		})
	}
}

func TestListCleanupPoliciesError(t *testing.T) {
	t.Parallel()

	router := setupCleanupPolicyRouter(&failingCleanupPolicyRepo{}, nil)


	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/admin/cleanup-policies", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestCreateCleanupPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       interface{}
		wantStatus int
	}{
		{
			name: "valid policy",
			body: models.CleanupPolicy{
				Name: "stop-idle", Action: "stop", Condition: "idle_days:7",
				Schedule: "0 2 * * *", ClusterID: "all", Enabled: true,
			},
			wantStatus: http.StatusCreated,
		},
		{
			name: "missing name",
			body: models.CleanupPolicy{
				Action: "stop", Condition: "idle_days:7",
				Schedule: "0 2 * * *", ClusterID: "all",
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "invalid action",
			body: models.CleanupPolicy{
				Name: "test", Action: "restart", Condition: "idle_days:7",
				Schedule: "0 2 * * *", ClusterID: "all",
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "invalid schedule",
			body: models.CleanupPolicy{
				Name: "test", Action: "stop", Condition: "idle_days:7",
				Schedule: "invalid", ClusterID: "all",
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "missing condition",
			body: models.CleanupPolicy{
				Name: "test", Action: "stop",
				Schedule: "0 2 * * *", ClusterID: "all",
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "missing cluster_id",
			body: models.CleanupPolicy{
				Name: "test", Action: "stop", Condition: "idle_days:7",
				Schedule: "0 2 * * *",
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid json",
			body:       "not json",
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := newMockCleanupPolicyRepo()
			router := setupCleanupPolicyRouter(repo, nil)

			body, _ := json.Marshal(tt.body)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest("POST", "/api/v1/admin/cleanup-policies", bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
		})
	}
}

func TestUpdateCleanupPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		id         string
		setup      func(*mockCleanupPolicyRepo)
		body       interface{}
		wantStatus int
	}{
		{
			name: "valid update",
			id:   "p1",
			setup: func(repo *mockCleanupPolicyRepo) {
				_ = repo.Create(&models.CleanupPolicy{
					ID: "p1", Name: "old", Action: "stop", Condition: "idle_days:7",
					Schedule: "0 2 * * *", ClusterID: "all", Enabled: true,
				})
			},
			body: models.CleanupPolicy{
				Name: "updated", Action: "clean", Condition: "status:stopped",
				Schedule: "0 3 * * *", ClusterID: "all", Enabled: false,
			},
			wantStatus: http.StatusOK,
		},
		{
			name:  "not found",
			id:    "nonexistent",
			setup: func(_ *mockCleanupPolicyRepo) {},
			body: models.CleanupPolicy{
				Name: "test", Action: "stop", Condition: "idle_days:7",
				Schedule: "0 2 * * *", ClusterID: "all",
			},
			wantStatus: http.StatusNotFound,
		},
		{
			name: "invalid body",
			id:   "p1",
			setup: func(repo *mockCleanupPolicyRepo) {
				_ = repo.Create(&models.CleanupPolicy{
					ID: "p1", Name: "old", Action: "stop", Condition: "idle_days:7",
					Schedule: "0 2 * * *", ClusterID: "all", Enabled: true,
				})
			},
			body: models.CleanupPolicy{
				Name: "", Action: "stop", Condition: "idle_days:7",
				Schedule: "0 2 * * *", ClusterID: "all",
			},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := newMockCleanupPolicyRepo()
			tt.setup(repo)
			router := setupCleanupPolicyRouter(repo, nil)

			body, _ := json.Marshal(tt.body)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest("PUT", "/api/v1/admin/cleanup-policies/"+tt.id, bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
		})
	}
}

func TestDeleteCleanupPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		id         string
		setup      func(*mockCleanupPolicyRepo)
		wantStatus int
	}{
		{
			name: "successful delete",
			id:   "p1",
			setup: func(repo *mockCleanupPolicyRepo) {
				_ = repo.Create(&models.CleanupPolicy{
					ID: "p1", Name: "test", Action: "stop", Condition: "idle_days:7",
					Schedule: "0 2 * * *", ClusterID: "all", Enabled: true,
				})
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "not found",
			id:         "nonexistent",
			setup:      func(_ *mockCleanupPolicyRepo) {},
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := newMockCleanupPolicyRepo()
			tt.setup(repo)
			router := setupCleanupPolicyRouter(repo, nil)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("DELETE", "/api/v1/admin/cleanup-policies/"+tt.id, nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
		})
	}
}

func TestRunCleanupPolicy(t *testing.T) {
	t.Parallel()

	tenDaysAgo := time.Now().Add(-10 * 24 * time.Hour)

	tests := []struct {
		name       string
		id         string
		dryRun     string
		wantStatus int
		wantCount  int
	}{
		{
			name:       "dry run",
			id:         "p1",
			dryRun:     "true",
			wantStatus: http.StatusOK,
			wantCount:  1,
		},
		{
			name:       "live run",
			id:         "p1",
			dryRun:     "",
			wantStatus: http.StatusOK,
			wantCount:  1,
		},
		{
			name:       "not found",
			id:         "nonexistent",
			dryRun:     "true",
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			policyRepo := newMockCleanupPolicyRepo()
			_ = policyRepo.Create(&models.CleanupPolicy{
				ID: "p1", Name: "stop-idle", Action: "stop",
				Condition: "idle_days:7", Schedule: "0 2 * * *",
				ClusterID: "all", Enabled: true,
			})

			instanceRepo := &cleanupMockInstanceRepo{
				instances: []models.StackInstance{
					{ID: "i1", Name: "old", LastDeployedAt: &tenDaysAgo},
				},
			}
			auditRepo := &cleanupMockAuditRepo{}

			sched := scheduler.NewScheduler(policyRepo, instanceRepo, auditRepo, nil)
			_ = sched.Start()
			defer sched.Stop()

			router := setupCleanupPolicyRouter(policyRepo, sched)

			url := "/api/v1/admin/cleanup-policies/" + tt.id + "/run"
			if tt.dryRun != "" {
				url += "?dry_run=" + tt.dryRun
			}

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("POST", url, nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.wantStatus == http.StatusOK {
				var results []scheduler.CleanupResult
				err := json.Unmarshal(w.Body.Bytes(), &results)
				assert.NoError(t, err)
				assert.Len(t, results, tt.wantCount)
			}
		})
	}
}

func TestRunCleanupPolicyNoScheduler(t *testing.T) {
	t.Parallel()

	repo := newMockCleanupPolicyRepo()
	router := setupCleanupPolicyRouter(repo, nil)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/admin/cleanup-policies/p1/run", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// cleanupMockInstanceRepo implements models.StackInstanceRepository for handler tests.
type cleanupMockInstanceRepo struct {
	instances []models.StackInstance
}

func (r *cleanupMockInstanceRepo) Create(_ *models.StackInstance) error                       { return nil }
func (r *cleanupMockInstanceRepo) FindByID(id string) (*models.StackInstance, error)          { return nil, errors.New("not found") }
func (r *cleanupMockInstanceRepo) FindByNamespace(_ string) (*models.StackInstance, error)    { return nil, errors.New("not found") }
func (r *cleanupMockInstanceRepo) Update(_ *models.StackInstance) error                       { return nil }
func (r *cleanupMockInstanceRepo) Delete(_ string) error                                      { return nil }
func (r *cleanupMockInstanceRepo) List() ([]models.StackInstance, error)                      { return r.instances, nil }
func (r *cleanupMockInstanceRepo) ListByOwner(_ string) ([]models.StackInstance, error)       { return nil, nil }
func (r *cleanupMockInstanceRepo) FindByCluster(id string) ([]models.StackInstance, error) {
	var result []models.StackInstance
	for _, inst := range r.instances {
		if inst.ClusterID == id {
			result = append(result, inst)
		}
	}
	return result, nil
}
func (r *cleanupMockInstanceRepo) ListExpired() ([]*models.StackInstance, error) { return nil, nil }

// cleanupMockAuditRepo implements models.AuditLogRepository for handler tests.
type cleanupMockAuditRepo struct {
	mu      sync.Mutex
	entries []models.AuditLog
}

func (r *cleanupMockAuditRepo) Create(log *models.AuditLog) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, *log)
	return nil
}

func (r *cleanupMockAuditRepo) List(_ models.AuditLogFilters) ([]models.AuditLog, int64, error) {
	return r.entries, int64(len(r.entries)), nil
}

package scheduler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
)

// mockPolicyRepo is a minimal in-memory mock for CleanupPolicyRepository.
type mockPolicyRepo struct {
	mu       sync.Mutex
	policies map[string]*models.CleanupPolicy
}

func newMockPolicyRepo() *mockPolicyRepo {
	return &mockPolicyRepo{policies: make(map[string]*models.CleanupPolicy)}
}

func (r *mockPolicyRepo) Create(p *models.CleanupPolicy) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p.ID == "" {
		p.ID = "mock-id"
	}
	r.policies[p.ID] = p
	return nil
}

func (r *mockPolicyRepo) FindByID(id string) (*models.CleanupPolicy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.policies[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return p, nil
}

func (r *mockPolicyRepo) Update(p *models.CleanupPolicy) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.policies[p.ID] = p
	return nil
}

func (r *mockPolicyRepo) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.policies, id)
	return nil
}

func (r *mockPolicyRepo) List() ([]models.CleanupPolicy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []models.CleanupPolicy
	for _, p := range r.policies {
		result = append(result, *p)
	}
	return result, nil
}

func (r *mockPolicyRepo) ListEnabled() ([]models.CleanupPolicy, error) {
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

// mockInstanceRepo is a minimal in-memory mock for StackInstanceRepository.
type mockInstanceRepo struct {
	instances []models.StackInstance
}

func (r *mockInstanceRepo) Create(inst *models.StackInstance) error { return nil }
func (r *mockInstanceRepo) FindByID(id string) (*models.StackInstance, error) {
	for i := range r.instances {
		if r.instances[i].ID == id {
			return &r.instances[i], nil
		}
	}
	return nil, errors.New("not found")
}
func (r *mockInstanceRepo) FindByNamespace(ns string) (*models.StackInstance, error) {
	return nil, errors.New("not found")
}
func (r *mockInstanceRepo) Update(inst *models.StackInstance) error { return nil }
func (r *mockInstanceRepo) Delete(id string) error                  { return nil }
func (r *mockInstanceRepo) List() ([]models.StackInstance, error) {
	return r.instances, nil
}
func (r *mockInstanceRepo) ListByOwner(ownerID string) ([]models.StackInstance, error) {
	return nil, nil
}
func (r *mockInstanceRepo) FindByCluster(clusterID string) ([]models.StackInstance, error) {
	var result []models.StackInstance
	for _, inst := range r.instances {
		if inst.ClusterID == clusterID {
			result = append(result, inst)
		}
	}
	return result, nil
}
func (r *mockInstanceRepo) CountByClusterAndOwner(string, string) (int, error) { return 0, nil }
func (r *mockInstanceRepo) ListExpired() ([]*models.StackInstance, error)      { return nil, nil }

// mockAuditRepo is a minimal in-memory mock for AuditLogRepository.
type mockAuditRepo struct {
	mu      sync.Mutex
	entries []models.AuditLog
}

func (r *mockAuditRepo) Create(log *models.AuditLog) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, *log)
	return nil
}

func (r *mockAuditRepo) List(_ models.AuditLogFilters) (*models.AuditLogResult, error) {
	return &models.AuditLogResult{
		Data:  r.entries,
		Total: int64(len(r.entries)),
	}, nil
}

// mockExecutor records calls to verify actions were invoked.
type mockExecutor struct {
	mu      sync.Mutex
	stopped []string
	cleaned []string
	deleted []string
}

func (e *mockExecutor) StopInstance(_ context.Context, inst *models.StackInstance) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stopped = append(e.stopped, inst.ID)
	return nil
}

func (e *mockExecutor) CleanInstance(_ context.Context, inst *models.StackInstance) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cleaned = append(e.cleaned, inst.ID)
	return nil
}

func (e *mockExecutor) DeleteInstance(_ context.Context, inst *models.StackInstance) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.deleted = append(e.deleted, inst.ID)
	return nil
}

// failingExecutor always returns an error.
type failingExecutor struct{}

func (e *failingExecutor) StopInstance(_ context.Context, _ *models.StackInstance) error {
	return errors.New("stop failed")
}

func (e *failingExecutor) CleanInstance(_ context.Context, _ *models.StackInstance) error {
	return errors.New("clean failed")
}

func (e *failingExecutor) DeleteInstance(_ context.Context, _ *models.StackInstance) error {
	return errors.New("delete failed")
}

func TestSchedulerStartStop(t *testing.T) {
	t.Parallel()

	policyRepo := newMockPolicyRepo()
	_ = policyRepo.Create(&models.CleanupPolicy{
		ID:        "p1",
		Name:      "test-policy",
		Schedule:  "0 2 * * *",
		Condition: "status:stopped",
		Action:    "delete",
		ClusterID: "all",
		Enabled:   true,
	})

	instanceRepo := &mockInstanceRepo{}
	auditRepo := &mockAuditRepo{}

	s := NewScheduler(policyRepo, instanceRepo, auditRepo, nil)

	err := s.Start()
	assert.NoError(t, err)
	assert.Len(t, s.entryMap, 1)

	s.Stop()
}

func TestSchedulerReload(t *testing.T) {
	t.Parallel()

	policyRepo := newMockPolicyRepo()
	_ = policyRepo.Create(&models.CleanupPolicy{
		ID:        "p1",
		Name:      "first",
		Schedule:  "0 2 * * *",
		Condition: "status:stopped",
		Action:    "delete",
		ClusterID: "all",
		Enabled:   true,
	})

	instanceRepo := &mockInstanceRepo{}
	auditRepo := &mockAuditRepo{}

	s := NewScheduler(policyRepo, instanceRepo, auditRepo, nil)
	err := s.Start()
	assert.NoError(t, err)
	defer s.Stop()

	// Add another policy and reload.
	_ = policyRepo.Create(&models.CleanupPolicy{
		ID:        "p2",
		Name:      "second",
		Schedule:  "0 3 * * *",
		Condition: "idle_days:7",
		Action:    "stop",
		ClusterID: "all",
		Enabled:   true,
	})

	err = s.Reload()
	assert.NoError(t, err)
	assert.Len(t, s.entryMap, 2)
}

func TestRunPolicyDryRun(t *testing.T) {
	t.Parallel()

	tenDaysAgo := time.Now().Add(-10 * 24 * time.Hour)

	policyRepo := newMockPolicyRepo()
	_ = policyRepo.Create(&models.CleanupPolicy{
		ID:        "p1",
		Name:      "stop-idle",
		Schedule:  "0 2 * * *",
		Condition: "idle_days:7",
		Action:    "stop",
		ClusterID: "all",
		Enabled:   true,
	})

	instanceRepo := &mockInstanceRepo{
		instances: []models.StackInstance{
			{ID: "i1", Name: "old-instance", Namespace: "ns-old", LastDeployedAt: &tenDaysAgo},
			{ID: "i2", Name: "new-instance", Namespace: "ns-new", LastDeployedAt: func() *time.Time { t := time.Now().Add(-time.Hour); return &t }()},
		},
	}
	auditRepo := &mockAuditRepo{}

	s := NewScheduler(policyRepo, instanceRepo, auditRepo, nil)
	err := s.Start()
	assert.NoError(t, err)
	defer s.Stop()

	results, err := s.RunPolicy("p1", true)
	assert.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, "i1", results[0].InstanceID)
	assert.Equal(t, "dry_run", results[0].Status)
	assert.Equal(t, "stop", results[0].Action)
}

func TestRunPolicyWithExecutor(t *testing.T) {
	t.Parallel()

	tenDaysAgo := time.Now().Add(-10 * 24 * time.Hour)

	policyRepo := newMockPolicyRepo()
	_ = policyRepo.Create(&models.CleanupPolicy{
		ID:        "p1",
		Name:      "stop-idle",
		Schedule:  "0 2 * * *",
		Condition: "idle_days:7",
		Action:    "stop",
		ClusterID: "all",
		Enabled:   true,
	})

	instanceRepo := &mockInstanceRepo{
		instances: []models.StackInstance{
			{ID: "i1", Name: "old-instance", Namespace: "ns-old", LastDeployedAt: &tenDaysAgo},
		},
	}
	auditRepo := &mockAuditRepo{}
	executor := &mockExecutor{}

	s := NewScheduler(policyRepo, instanceRepo, auditRepo, executor)
	err := s.Start()
	assert.NoError(t, err)
	defer s.Stop()

	results, err := s.RunPolicy("p1", false)
	assert.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, "success", results[0].Status)
	assert.Equal(t, []string{"i1"}, executor.stopped)

	// Verify audit log was created.
	assert.Len(t, auditRepo.entries, 1)
	assert.Equal(t, "cleanup_policy_executed", auditRepo.entries[0].Action)
}

func TestRunPolicyCleanAction(t *testing.T) {
	t.Parallel()

	tenDaysAgo := time.Now().Add(-10 * 24 * time.Hour)

	policyRepo := newMockPolicyRepo()
	_ = policyRepo.Create(&models.CleanupPolicy{
		ID:        "p1",
		Name:      "clean-old",
		Schedule:  "0 2 * * *",
		Condition: "status:stopped,age_days:5",
		Action:    "clean",
		ClusterID: "all",
		Enabled:   true,
	})

	instanceRepo := &mockInstanceRepo{
		instances: []models.StackInstance{
			{ID: "i1", Name: "old-stopped", Status: "stopped", CreatedAt: tenDaysAgo},
			{ID: "i2", Name: "new-stopped", Status: "stopped", CreatedAt: time.Now()},
		},
	}
	auditRepo := &mockAuditRepo{}
	executor := &mockExecutor{}

	s := NewScheduler(policyRepo, instanceRepo, auditRepo, executor)
	err := s.Start()
	assert.NoError(t, err)
	defer s.Stop()

	results, err := s.RunPolicy("p1", false)
	assert.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, "i1", results[0].InstanceID)
	assert.Equal(t, "clean", results[0].Action)
	assert.Equal(t, []string{"i1"}, executor.cleaned)
}

func TestRunPolicyDeleteAction(t *testing.T) {
	t.Parallel()

	policyRepo := newMockPolicyRepo()
	_ = policyRepo.Create(&models.CleanupPolicy{
		ID:        "p1",
		Name:      "delete-expired",
		Schedule:  "0 2 * * *",
		Condition: "ttl_expired",
		Action:    "delete",
		ClusterID: "all",
		Enabled:   true,
	})

	expired := time.Now().Add(-time.Hour)
	instanceRepo := &mockInstanceRepo{
		instances: []models.StackInstance{
			{ID: "i1", Name: "expired-inst", ExpiresAt: &expired, Status: "stopped"},
		},
	}
	auditRepo := &mockAuditRepo{}
	executor := &mockExecutor{}

	s := NewScheduler(policyRepo, instanceRepo, auditRepo, executor)
	err := s.Start()
	assert.NoError(t, err)
	defer s.Stop()

	results, err := s.RunPolicy("p1", false)
	assert.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, "delete", results[0].Action)
	assert.Equal(t, []string{"i1"}, executor.deleted)
}

func TestRunPolicyByCluster(t *testing.T) {
	t.Parallel()

	tenDaysAgo := time.Now().Add(-10 * 24 * time.Hour)

	policyRepo := newMockPolicyRepo()
	_ = policyRepo.Create(&models.CleanupPolicy{
		ID:        "p1",
		Name:      "stop-cluster1",
		Schedule:  "0 2 * * *",
		Condition: "idle_days:7",
		Action:    "stop",
		ClusterID: "cluster-1",
		Enabled:   true,
	})

	instanceRepo := &mockInstanceRepo{
		instances: []models.StackInstance{
			{ID: "i1", Name: "inst-c1", ClusterID: "cluster-1", LastDeployedAt: &tenDaysAgo},
			{ID: "i2", Name: "inst-c2", ClusterID: "cluster-2", LastDeployedAt: &tenDaysAgo},
		},
	}
	auditRepo := &mockAuditRepo{}

	s := NewScheduler(policyRepo, instanceRepo, auditRepo, nil)
	err := s.Start()
	assert.NoError(t, err)
	defer s.Stop()

	results, err := s.RunPolicy("p1", true)
	assert.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, "i1", results[0].InstanceID)
}

func TestRunPolicyExecutorError(t *testing.T) {
	t.Parallel()

	tenDaysAgo := time.Now().Add(-10 * 24 * time.Hour)

	policyRepo := newMockPolicyRepo()
	_ = policyRepo.Create(&models.CleanupPolicy{
		ID:        "p1",
		Name:      "stop-idle",
		Schedule:  "0 2 * * *",
		Condition: "idle_days:7",
		Action:    "stop",
		ClusterID: "all",
		Enabled:   true,
	})

	instanceRepo := &mockInstanceRepo{
		instances: []models.StackInstance{
			{ID: "i1", Name: "old-instance", LastDeployedAt: &tenDaysAgo},
		},
	}
	auditRepo := &mockAuditRepo{}
	executor := &failingExecutor{}

	s := NewScheduler(policyRepo, instanceRepo, auditRepo, executor)
	err := s.Start()
	assert.NoError(t, err)
	defer s.Stop()

	results, err := s.RunPolicy("p1", false)
	assert.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, "error", results[0].Status)
	assert.Equal(t, "stop failed", results[0].Error)
}

func TestRunPolicyNotFound(t *testing.T) {
	t.Parallel()

	policyRepo := newMockPolicyRepo()
	instanceRepo := &mockInstanceRepo{}
	auditRepo := &mockAuditRepo{}

	s := NewScheduler(policyRepo, instanceRepo, auditRepo, nil)
	err := s.Start()
	assert.NoError(t, err)
	defer s.Stop()

	_, err = s.RunPolicy("nonexistent", true)
	assert.Error(t, err)
}

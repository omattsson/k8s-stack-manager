package ttl

import (
	"sync"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockInstanceRepo struct {
	mu    sync.RWMutex
	items map[string]*models.StackInstance
	err   error
}

func newMockInstanceRepo() *mockInstanceRepo {
	return &mockInstanceRepo{items: make(map[string]*models.StackInstance)}
}

func (m *mockInstanceRepo) Create(i *models.StackInstance) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[i.ID] = i
	return nil
}

func (m *mockInstanceRepo) FindByID(id string) (*models.StackInstance, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	i, ok := m.items[id]
	if !ok {
		return nil, nil
	}
	cp := *i
	return &cp, nil
}

func (m *mockInstanceRepo) FindByNamespace(string) (*models.StackInstance, error) {
	return nil, nil
}

func (m *mockInstanceRepo) Update(i *models.StackInstance) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.items[i.ID] = i
	return nil
}

func (m *mockInstanceRepo) Delete(string) error { return nil }

func (m *mockInstanceRepo) List() ([]models.StackInstance, error) { return nil, nil }

func (m *mockInstanceRepo) ListByOwner(string) ([]models.StackInstance, error) {
	return nil, nil
}

func (m *mockInstanceRepo) FindByCluster(string) ([]models.StackInstance, error) {
	return nil, nil
}

func (m *mockInstanceRepo) CountByClusterAndOwner(string, string) (int, error) {
	return 0, nil
}

func (m *mockInstanceRepo) ListPaged(_, _ int) ([]models.StackInstance, int, error) { return nil, 0, nil }
func (m *mockInstanceRepo) CountAll() (int, error)                              { return 0, nil }
func (m *mockInstanceRepo) CountByStatus(_ string) (int, error)                 { return 0, nil }
func (m *mockInstanceRepo) ExistsByDefinitionAndStatus(_, _ string) (bool, error) { return false, nil }

func (m *mockInstanceRepo) ListExpired() ([]*models.StackInstance, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	now := time.Now()
	var out []*models.StackInstance
	for _, i := range m.items {
		if i.Status == models.StackStatusRunning && i.ExpiresAt != nil && i.ExpiresAt.Before(now) {
			cp := *i
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *mockInstanceRepo) get(id string) *models.StackInstance {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.items[id]
}

type mockAuditRepo struct {
	mu   sync.Mutex
	logs []*models.AuditLog
}

func (m *mockAuditRepo) Create(log *models.AuditLog) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logs = append(m.logs, log)
	return nil
}

func (m *mockAuditRepo) List(models.AuditLogFilters) (*models.AuditLogResult, error) {
	return &models.AuditLogResult{}, nil
}

func (m *mockAuditRepo) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.logs)
}

type mockHub struct {
	mu       sync.Mutex
	messages [][]byte
}

func (m *mockHub) Broadcast(msg []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, msg)
}

func (m *mockHub) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.messages)
}

func TestReaper_ExpiredInstancesGetStopped(t *testing.T) {
	t.Parallel()

	repo := newMockInstanceRepo()
	auditRepo := &mockAuditRepo{}
	hub := &mockHub{}

	past := time.Now().Add(-10 * time.Minute)
	future := time.Now().Add(10 * time.Minute)

	expired := &models.StackInstance{
		ID:         "expired-1",
		Name:       "expired",
		Status:     models.StackStatusRunning,
		TTLMinutes: 60,
		ExpiresAt:  &past,
	}
	require.NoError(t, repo.Create(expired))

	active := &models.StackInstance{
		ID:         "active-1",
		Name:       "active",
		Status:     models.StackStatusRunning,
		TTLMinutes: 60,
		ExpiresAt:  &future,
	}
	require.NoError(t, repo.Create(active))

	alreadyStopped := &models.StackInstance{
		ID:         "stopped-1",
		Name:       "stopped",
		Status:     models.StackStatusStopped,
		TTLMinutes: 60,
		ExpiresAt:  &past,
	}
	require.NoError(t, repo.Create(alreadyStopped))

	reaper := NewReaper(repo, auditRepo, hub, nil, 50*time.Millisecond)
	go reaper.Start()
	time.Sleep(200 * time.Millisecond)
	reaper.Stop()

	got := repo.get("expired-1")
	assert.Equal(t, models.StackStatusStopped, got.Status)
	assert.Equal(t, "Expired (TTL)", got.ErrorMessage)

	gotActive := repo.get("active-1")
	assert.Equal(t, models.StackStatusRunning, gotActive.Status)

	gotStopped := repo.get("stopped-1")
	assert.Equal(t, models.StackStatusStopped, gotStopped.Status)
	assert.Equal(t, "", gotStopped.ErrorMessage)

	assert.Equal(t, 1, auditRepo.count())
	assert.Equal(t, 1, hub.count())
}

func TestReaper_NoExpiredInstances(t *testing.T) {
	t.Parallel()

	repo := newMockInstanceRepo()
	auditRepo := &mockAuditRepo{}

	future := time.Now().Add(10 * time.Minute)
	inst := &models.StackInstance{
		ID:         "active-1",
		Name:       "active",
		Status:     models.StackStatusRunning,
		TTLMinutes: 60,
		ExpiresAt:  &future,
	}
	require.NoError(t, repo.Create(inst))

	reaper := NewReaper(repo, auditRepo, nil, nil, 50*time.Millisecond)
	go reaper.Start()
	time.Sleep(200 * time.Millisecond)
	reaper.Stop()

	got := repo.get("active-1")
	assert.Equal(t, models.StackStatusRunning, got.Status)
	assert.Equal(t, 0, auditRepo.count())
}

func TestReaper_GracefulShutdown(t *testing.T) {
	t.Parallel()

	repo := newMockInstanceRepo()
	reaper := NewReaper(repo, nil, nil, nil, 1*time.Second)
	go reaper.Start()

	done := make(chan struct{})
	go func() {
		reaper.Stop()
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("Reaper.Stop() did not return promptly")
	}
}

func TestReaper_NilOptionalDeps(t *testing.T) {
	t.Parallel()

	repo := newMockInstanceRepo()
	past := time.Now().Add(-10 * time.Minute)
	expired := &models.StackInstance{
		ID:         "expired-1",
		Name:       "expired",
		Status:     models.StackStatusRunning,
		TTLMinutes: 60,
		ExpiresAt:  &past,
	}
	require.NoError(t, repo.Create(expired))

	reaper := NewReaper(repo, nil, nil, nil, 50*time.Millisecond)
	go reaper.Start()
	time.Sleep(200 * time.Millisecond)
	reaper.Stop()

	got := repo.get("expired-1")
	assert.Equal(t, models.StackStatusStopped, got.Status)
}

func TestReaper_DoubleStopDoesNotPanic(t *testing.T) {
	t.Parallel()

	repo := newMockInstanceRepo()
	reaper := NewReaper(repo, nil, nil, nil, 1*time.Second)
	go reaper.Start()

	// First stop
	reaper.Stop()

	// Second stop should not panic or hang.
	done := make(chan struct{})
	go func() {
		reaper.Stop()
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("Second Stop() call did not return promptly")
	}
}

func TestReaper_InitialCheckOnStart(t *testing.T) {
	t.Parallel()

	repo := newMockInstanceRepo()
	auditRepo := &mockAuditRepo{}
	hub := &mockHub{}

	past := time.Now().Add(-10 * time.Minute)
	expired := &models.StackInstance{
		ID:         "expired-init",
		Name:       "expired-init",
		Status:     models.StackStatusRunning,
		TTLMinutes: 60,
		ExpiresAt:  &past,
	}
	require.NoError(t, repo.Create(expired))

	// Use a very long interval so only the initial check fires.
	reaper := NewReaper(repo, auditRepo, hub, nil, 10*time.Minute)
	go reaper.Start()

	// Give Start() a moment to run the initial processExpired().
	time.Sleep(100 * time.Millisecond)
	reaper.Stop()

	got := repo.get("expired-init")
	assert.Equal(t, models.StackStatusStopped, got.Status)
	assert.Equal(t, "Expired (TTL)", got.ErrorMessage)
	assert.Equal(t, 1, auditRepo.count())
}

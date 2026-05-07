package ttl

import (
	"context"
	"sync"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Mock notifier
// ---------------------------------------------------------------------------

type notifyCall struct {
	userID, notifType, title, message, entityType, entityID string
}

type mockExpiryNotifier struct {
	mu    sync.Mutex
	calls []notifyCall
}

func (m *mockExpiryNotifier) Notify(_ context.Context, userID, notifType, title, message, entityType, entityID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, notifyCall{
		userID:     userID,
		notifType:  notifType,
		title:      title,
		message:    message,
		entityType: entityType,
		entityID:   entityID,
	})
	return nil
}

func (m *mockExpiryNotifier) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *mockExpiryNotifier) getCalls() []notifyCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]notifyCall, len(m.calls))
	copy(cp, m.calls)
	return cp
}

// ---------------------------------------------------------------------------
// Mock instance repo for warner tests (separate from reaper's mockInstanceRepo)
// ---------------------------------------------------------------------------

type warnerMockInstanceRepo struct {
	mu              sync.Mutex
	expiringSoonItems []*models.StackInstance
}

func (m *warnerMockInstanceRepo) setExpiringSoon(items []*models.StackInstance) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expiringSoonItems = items
}

func (m *warnerMockInstanceRepo) ListExpiringSoon(_ time.Duration) ([]*models.StackInstance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]*models.StackInstance, len(m.expiringSoonItems))
	copy(cp, m.expiringSoonItems)
	return cp, nil
}

// --- no-op stubs for the rest of the interface ---

func (m *warnerMockInstanceRepo) Create(_ *models.StackInstance) error   { return nil }
func (m *warnerMockInstanceRepo) FindByID(_ string) (*models.StackInstance, error) {
	return nil, nil
}
func (m *warnerMockInstanceRepo) FindByNamespace(_ string) (*models.StackInstance, error) {
	return nil, nil
}
func (m *warnerMockInstanceRepo) Update(_ *models.StackInstance) error { return nil }
func (m *warnerMockInstanceRepo) Delete(_ string) error                { return nil }
func (m *warnerMockInstanceRepo) List() ([]models.StackInstance, error) {
	return nil, nil
}
func (m *warnerMockInstanceRepo) ListPaged(_, _ int) ([]models.StackInstance, int, error) {
	return nil, 0, nil
}
func (m *warnerMockInstanceRepo) ListByOwner(_ string) ([]models.StackInstance, error) {
	return nil, nil
}
func (m *warnerMockInstanceRepo) FindByName(_ string) ([]models.StackInstance, error) {
	return nil, nil
}
func (m *warnerMockInstanceRepo) FindByCluster(_ string) ([]models.StackInstance, error) {
	return nil, nil
}
func (m *warnerMockInstanceRepo) CountByClusterAndOwner(_, _ string) (int, error) { return 0, nil }
func (m *warnerMockInstanceRepo) CountAll() (int, error)                          { return 0, nil }
func (m *warnerMockInstanceRepo) CountByStatus(_ string) (int, error)             { return 0, nil }
func (m *warnerMockInstanceRepo) CountByDefinitionIDs(_ []string) (map[string]int, error) {
	return nil, nil
}
func (m *warnerMockInstanceRepo) CountByOwnerIDs(_ []string) (map[string]int, error) {
	return nil, nil
}
func (m *warnerMockInstanceRepo) ListIDsByDefinitionIDs(_ []string) (map[string][]string, error) {
	return nil, nil
}
func (m *warnerMockInstanceRepo) ListIDsByOwnerIDs(_ []string) (map[string][]string, error) {
	return nil, nil
}
func (m *warnerMockInstanceRepo) ExistsByDefinitionAndStatus(_, _ string) (bool, error) {
	return false, nil
}
func (m *warnerMockInstanceRepo) ListExpired() ([]*models.StackInstance, error)          { return nil, nil }
func (m *warnerMockInstanceRepo) ListByStatus(_ string, _ int) ([]*models.StackInstance, error) { return nil, nil }

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestWarner_SendsWarningForExpiringSoon(t *testing.T) {
	t.Parallel()

	expiresAt := time.Now().Add(10 * time.Minute)
	repo := &warnerMockInstanceRepo{
		expiringSoonItems: []*models.StackInstance{
			{
				ID:       "inst-1",
				Name:     "my-stack",
				OwnerID:  "user-42",
				Status:   models.StackStatusRunning,
				ExpiresAt: &expiresAt,
			},
		},
	}
	notifier := &mockExpiryNotifier{}

	w := NewWarner(repo, notifier, 30*time.Minute, 60*time.Second)
	w.check()

	calls := notifier.getCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, "user-42", calls[0].userID)
	assert.Equal(t, "stack.expiring", calls[0].notifType)
	assert.Equal(t, "stack_instance", calls[0].entityType)
	assert.Equal(t, "inst-1", calls[0].entityID)
}

func TestWarner_DeduplicatesWarnings(t *testing.T) {
	t.Parallel()

	expiresAt := time.Now().Add(10 * time.Minute)
	repo := &warnerMockInstanceRepo{
		expiringSoonItems: []*models.StackInstance{
			{
				ID:       "inst-1",
				Name:     "my-stack",
				OwnerID:  "user-42",
				Status:   models.StackStatusRunning,
				ExpiresAt: &expiresAt,
			},
		},
	}
	notifier := &mockExpiryNotifier{}

	w := NewWarner(repo, notifier, 30*time.Minute, 60*time.Second)
	w.check()
	w.check()

	assert.Equal(t, 1, notifier.count(), "notifier should have been called only once; second check should deduplicate")
}

func TestWarner_PrunesExpiredEntries(t *testing.T) {
	t.Parallel()

	pastExpiry := time.Now().Add(-5 * time.Minute)
	repo := &warnerMockInstanceRepo{}
	notifier := &mockExpiryNotifier{}

	w := NewWarner(repo, notifier, 30*time.Minute, 60*time.Second)

	// Pre-populate the warned map with an entry whose ExpiresAt is in the past.
	key := "inst-old|" + pastExpiry.Format(time.RFC3339)
	w.mu.Lock()
	w.warned[key] = pastExpiry
	w.mu.Unlock()

	// check() calls pruneWarned() internally.
	w.check()

	w.mu.Lock()
	remaining := len(w.warned)
	w.mu.Unlock()

	assert.Equal(t, 0, remaining, "expired entries should be pruned from the warned map")
}

func TestWarner_TTLExtensionResetsWarning(t *testing.T) {
	t.Parallel()

	expiresT1 := time.Now().Add(10 * time.Minute)
	inst := &models.StackInstance{
		ID:       "inst-1",
		Name:     "my-stack",
		OwnerID:  "user-42",
		Status:   models.StackStatusRunning,
		ExpiresAt: &expiresT1,
	}

	repo := &warnerMockInstanceRepo{
		expiringSoonItems: []*models.StackInstance{inst},
	}
	notifier := &mockExpiryNotifier{}

	w := NewWarner(repo, notifier, 30*time.Minute, 60*time.Second)

	// First check: warns for expiresT1.
	w.check()
	require.Equal(t, 1, notifier.count())

	// Simulate TTL extension: same instance, new ExpiresAt.
	expiresT2 := time.Now().Add(20 * time.Minute)
	extendedInst := &models.StackInstance{
		ID:       "inst-1",
		Name:     "my-stack",
		OwnerID:  "user-42",
		Status:   models.StackStatusRunning,
		ExpiresAt: &expiresT2,
	}
	repo.setExpiringSoon([]*models.StackInstance{extendedInst})

	// Second check: new ExpiresAt means a different key, so should warn again.
	w.check()
	assert.Equal(t, 2, notifier.count(), "TTL extension should trigger a new warning")
}

func TestWarner_DefaultThresholdAndInterval(t *testing.T) {
	t.Parallel()

	repo := &warnerMockInstanceRepo{}
	notifier := &mockExpiryNotifier{}

	w := NewWarner(repo, notifier, 0, 0)

	assert.Equal(t, 30*time.Minute, w.threshold, "default threshold should be 30m")
	assert.Equal(t, 60*time.Second, w.interval, "default interval should be 60s")
}

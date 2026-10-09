package ttl

import (
	"context"
	"errors"
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

// warnerMockInstanceRepo keeps the expiry warning mark per instance, like
// stack_instances.expiry_warned_at. It is safe for concurrent use.
type warnerMockInstanceRepo struct {
	mu                sync.Mutex
	expiringSoonItems []*models.StackInstance
	markErr           error
	markCalls         int
	listCalls         int
}

// setExpiringSoon replaces the instances. New instances have no mark, as
// after an Update that changed ExpiresAt.
func (m *warnerMockInstanceRepo) setExpiringSoon(items []*models.StackInstance) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expiringSoonItems = items
}

func (m *warnerMockInstanceRepo) ListExpiringSoon(_ time.Duration) ([]*models.StackInstance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listCalls++
	var out []*models.StackInstance
	for _, inst := range m.expiringSoonItems {
		if inst.ExpiryWarnedAt == nil {
			cp := *inst
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *warnerMockInstanceRepo) MarkExpiryWarned(id string, expiresAt, warnedAt time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.markCalls++
	if m.markErr != nil {
		return false, m.markErr
	}
	for _, inst := range m.expiringSoonItems {
		if inst.ID != id {
			continue
		}
		if inst.ExpiryWarnedAt != nil || inst.ExpiresAt == nil || !inst.ExpiresAt.Equal(expiresAt) {
			return false, nil
		}
		inst.ExpiryWarnedAt = &warnedAt
		return true, nil
	}
	return false, nil
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
func (m *warnerMockInstanceRepo) ListPaged(_ models.StackInstanceFilter, _, _ int) ([]models.StackInstance, int, error) {
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

func TestWarner_TwoWarnersOneDatabaseSendOneWarning(t *testing.T) {
	t.Parallel()

	// Two replicas (for example a short leader overlap) check the same
	// database at the same time.
	expiresAt := time.Now().Add(4 * time.Minute)
	repo := &warnerMockInstanceRepo{
		expiringSoonItems: []*models.StackInstance{
			{ID: "inst-1", Name: "my-stack", OwnerID: "user-42", Status: models.StackStatusRunning, ExpiresAt: &expiresAt},
			{ID: "inst-2", Name: "other", OwnerID: "user-7", Status: models.StackStatusRunning, ExpiresAt: &expiresAt},
		},
	}
	notifier := &mockExpiryNotifier{}
	w1 := NewWarner(repo, notifier, 30*time.Minute, time.Minute)
	w2 := NewWarner(repo, notifier, 30*time.Minute, time.Minute)

	var wg sync.WaitGroup
	for _, w := range []*Warner{w1, w2, w1, w2} {
		wg.Add(1)
		go func(w *Warner) {
			defer wg.Done()
			w.check()
		}(w)
	}
	wg.Wait()

	assert.Equal(t, 2, notifier.count(), "one warning per instance")
	ids := map[string]int{}
	for _, c := range notifier.getCalls() {
		ids[c.entityID]++
	}
	assert.Equal(t, map[string]int{"inst-1": 1, "inst-2": 1}, ids)
}

func TestWarner_MarkErrorSendsNoWarning(t *testing.T) {
	t.Parallel()

	expiresAt := time.Now().Add(10 * time.Minute)
	repo := &warnerMockInstanceRepo{
		expiringSoonItems: []*models.StackInstance{
			{ID: "inst-1", Name: "my-stack", OwnerID: "user-42", Status: models.StackStatusRunning, ExpiresAt: &expiresAt},
		},
		markErr: errors.New("database unavailable"),
	}
	notifier := &mockExpiryNotifier{}

	NewWarner(repo, notifier, 30*time.Minute, time.Minute).check()

	assert.Equal(t, 0, notifier.count())
	assert.Equal(t, 1, repo.markCalls)
}

func TestWarner_RunStartStopStartAgain(t *testing.T) {
	t.Parallel()

	expiresAt := time.Now().Add(10 * time.Minute)
	repo := &warnerMockInstanceRepo{
		expiringSoonItems: []*models.StackInstance{
			{ID: "inst-1", Name: "my-stack", OwnerID: "user-42", Status: models.StackStatusRunning, ExpiresAt: &expiresAt},
		},
	}
	notifier := &mockExpiryNotifier{}
	w := NewWarner(repo, notifier, 30*time.Minute, time.Hour)

	for term := 0; term < 2; term++ {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			w.Run(ctx)
		}()
		require.Eventually(t, func() bool {
			repo.mu.Lock()
			defer repo.mu.Unlock()
			return repo.listCalls >= term+1
		}, time.Second, 5*time.Millisecond)
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("Run did not return after cancel")
		}
	}
	// The second term (a new leader) does not warn again.
	assert.Equal(t, 1, notifier.count())
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

func (*warnerMockInstanceRepo) CountByStatuses(statuses []string) (int, error) { return 0, nil }

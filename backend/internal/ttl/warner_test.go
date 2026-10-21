package ttl

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"backend/internal/hooks"
	"backend/internal/models"
	"backend/internal/notifier"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Mock notifier
// ---------------------------------------------------------------------------

type notifyCall struct {
	userID, notifType, title, message, entityType, entityID string
	// trigger is the notifier.WithTrigger option of the call.
	trigger *hooks.Trigger
}

type mockExpiryNotifier struct {
	mu    sync.Mutex
	calls []notifyCall
}

func (m *mockExpiryNotifier) NotifyInstance(_ context.Context, target models.NotificationTarget, notifType, title, message string, opts ...notifier.InstanceOption) error {
	o := notifier.ResolveInstanceOptions(opts...)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, notifyCall{
		userID:     target.OwnerID,
		notifType:  notifType,
		title:      title,
		message:    message,
		entityType: "stack_instance",
		entityID:   target.InstanceID,
		trigger:    o.Trigger,
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

func (m *warnerMockInstanceRepo) ListExpiringSoon(ctx context.Context, _ time.Duration) ([]*models.StackInstance, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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

func (m *warnerMockInstanceRepo) MarkExpiryWarned(ctx context.Context, id string, expiresAt, warnedAt time.Time) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
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

func (m *warnerMockInstanceRepo) Create(ctx context.Context, _ *models.StackInstance) error {
	return ctx.Err()
}
func (m *warnerMockInstanceRepo) FindByID(ctx context.Context, _ string) (*models.StackInstance, error) {
	return nil, ctx.Err()
}
func (m *warnerMockInstanceRepo) FindByNamespace(ctx context.Context, _ string) (*models.StackInstance, error) {
	return nil, ctx.Err()
}
func (m *warnerMockInstanceRepo) Update(ctx context.Context, _ *models.StackInstance) error {
	return ctx.Err()
}
func (m *warnerMockInstanceRepo) Delete(ctx context.Context, _ string) error { return ctx.Err() }
func (m *warnerMockInstanceRepo) List(ctx context.Context) ([]models.StackInstance, error) {
	return nil, ctx.Err()
}
func (m *warnerMockInstanceRepo) ListPaged(ctx context.Context, _ models.StackInstanceFilter, _, _ int) ([]models.StackInstance, int, error) {
	return nil, 0, ctx.Err()
}
func (m *warnerMockInstanceRepo) ListByOwner(ctx context.Context, _ string) ([]models.StackInstance, error) {
	return nil, ctx.Err()
}
func (m *warnerMockInstanceRepo) FindByName(ctx context.Context, _ string) ([]models.StackInstance, error) {
	return nil, ctx.Err()
}
func (m *warnerMockInstanceRepo) FindByCluster(ctx context.Context, _ string) ([]models.StackInstance, error) {
	return nil, ctx.Err()
}
func (m *warnerMockInstanceRepo) CountByClusterAndOwner(ctx context.Context, _, _ string) (int, error) {
	return 0, ctx.Err()
}
func (m *warnerMockInstanceRepo) CountAll(ctx context.Context) (int, error) { return 0, ctx.Err() }
func (m *warnerMockInstanceRepo) CountByStatus(ctx context.Context, _ string) (int, error) {
	return 0, ctx.Err()
}
func (m *warnerMockInstanceRepo) CountByDefinitionIDs(ctx context.Context, _ []string) (map[string]int, error) {
	return nil, ctx.Err()
}
func (m *warnerMockInstanceRepo) CountByOwnerIDs(ctx context.Context, _ []string) (map[string]int, error) {
	return nil, ctx.Err()
}
func (m *warnerMockInstanceRepo) ListIDsByDefinitionIDs(ctx context.Context, _ []string) (map[string][]string, error) {
	return nil, ctx.Err()
}
func (m *warnerMockInstanceRepo) ListIDsByOwnerIDs(ctx context.Context, _ []string) (map[string][]string, error) {
	return nil, ctx.Err()
}
func (m *warnerMockInstanceRepo) ExistsByDefinitionAndStatus(ctx context.Context, _, _ string) (bool, error) {
	return false, ctx.Err()
}
func (m *warnerMockInstanceRepo) ListExpired(ctx context.Context) ([]*models.StackInstance, error) {
	return nil, ctx.Err()
}
func (m *warnerMockInstanceRepo) ListByStatus(ctx context.Context, _ string, _ int) ([]*models.StackInstance, error) {
	return nil, ctx.Err()
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestWarner_SendsWarningForExpiringSoon(t *testing.T) {
	t.Parallel()

	expiresAt := time.Now().Add(10 * time.Minute)
	repo := &warnerMockInstanceRepo{
		expiringSoonItems: []*models.StackInstance{
			{
				ID:        "inst-1",
				Name:      "my-stack",
				OwnerID:   "user-42",
				Status:    models.StackStatusRunning,
				ExpiresAt: &expiresAt,
			},
		},
	}
	notifier := &mockExpiryNotifier{}

	w := NewWarner(repo, notifier, 30*time.Minute, 60*time.Second)
	w.check(context.Background())

	calls := notifier.getCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, "user-42", calls[0].userID)
	assert.Equal(t, "stack.expiring", calls[0].notifType)
	assert.Equal(t, "stack_instance", calls[0].entityType)
	assert.Equal(t, "inst-1", calls[0].entityID)
	assert.Nil(t, calls[0].trigger, "an expiry warning has no operation, so no trigger")
}

func TestWarner_DeduplicatesWarnings(t *testing.T) {
	t.Parallel()

	expiresAt := time.Now().Add(10 * time.Minute)
	repo := &warnerMockInstanceRepo{
		expiringSoonItems: []*models.StackInstance{
			{
				ID:        "inst-1",
				Name:      "my-stack",
				OwnerID:   "user-42",
				Status:    models.StackStatusRunning,
				ExpiresAt: &expiresAt,
			},
		},
	}
	notifier := &mockExpiryNotifier{}

	w := NewWarner(repo, notifier, 30*time.Minute, 60*time.Second)
	w.check(context.Background())
	w.check(context.Background())

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
			w.check(context.Background())
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

	NewWarner(repo, notifier, 30*time.Minute, time.Minute).check(context.Background())

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
		ID:        "inst-1",
		Name:      "my-stack",
		OwnerID:   "user-42",
		Status:    models.StackStatusRunning,
		ExpiresAt: &expiresT1,
	}

	repo := &warnerMockInstanceRepo{
		expiringSoonItems: []*models.StackInstance{inst},
	}
	notifier := &mockExpiryNotifier{}

	w := NewWarner(repo, notifier, 30*time.Minute, 60*time.Second)

	// First check: warns for expiresT1.
	w.check(context.Background())
	require.Equal(t, 1, notifier.count())

	// Simulate TTL extension: same instance, new ExpiresAt.
	expiresT2 := time.Now().Add(20 * time.Minute)
	extendedInst := &models.StackInstance{
		ID:        "inst-1",
		Name:      "my-stack",
		OwnerID:   "user-42",
		Status:    models.StackStatusRunning,
		ExpiresAt: &expiresT2,
	}
	repo.setExpiringSoon([]*models.StackInstance{extendedInst})

	// Second check: new ExpiresAt means a different key, so should warn again.
	w.check(context.Background())
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

func (*warnerMockInstanceRepo) CountByStatuses(ctx context.Context, statuses []string) (int, error) {
	return 0, ctx.Err()
}

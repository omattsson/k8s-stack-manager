package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ctxPolicyRepo records the jobCtxKey marker of the context of each call, and
// runs after (when set) after FindByID.
type ctxPolicyRepo struct {
	*mockPolicyRepo
	mu        sync.Mutex
	seen      map[string]any
	afterFind func()
}

func newCtxPolicyRepo() *ctxPolicyRepo {
	return &ctxPolicyRepo{mockPolicyRepo: newMockPolicyRepo(), seen: map[string]any{}}
}

func (r *ctxPolicyRepo) record(ctx context.Context, method string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen[method] = ctx.Value(jobCtxKey{})
}

func (r *ctxPolicyRepo) marker(method string) any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seen[method]
}

func (r *ctxPolicyRepo) FindByID(ctx context.Context, id string) (*models.CleanupPolicy, error) {
	r.record(ctx, "FindByID")
	p, err := r.mockPolicyRepo.FindByID(ctx, id)
	if r.afterFind != nil {
		r.afterFind()
	}
	return p, err
}

func (r *ctxPolicyRepo) SetLastRunAt(ctx context.Context, id string, t time.Time) error {
	r.record(ctx, "SetLastRunAt")
	return r.mockPolicyRepo.SetLastRunAt(ctx, id, t)
}

func (r *ctxPolicyRepo) ListEnabled(ctx context.Context) ([]models.CleanupPolicy, error) {
	r.record(ctx, "ListEnabled")
	return r.mockPolicyRepo.ListEnabled(ctx)
}

func markedContext(marker string) context.Context {
	return context.WithValue(context.Background(), jobCtxKey{}, marker)
}

// TestScheduler_PolicyRepoUsesCallerContext checks that every call of the
// policy repository runs with the context of its caller (#656): the
// scheduled run and the activation with the context of the term, the reload
// and the manual policy read with the context of the caller.
func TestScheduler_PolicyRepoUsesCallerContext(t *testing.T) {
	t.Parallel()

	t.Run("scheduled run reads and updates the policy", func(t *testing.T) {
		t.Parallel()
		repo := newCtxPolicyRepo()
		require.NoError(t, repo.mockPolicyRepo.Create(context.Background(), newTestPolicy("p1", "0 2 * * *")))
		s := NewScheduler(repo, &mockInstanceRepo{}, &mockAuditRepo{}, nil, nil)
		t.Cleanup(s.Stop)

		s.executeScheduledPolicy(markedContext("job"), "p1")

		assert.Equal(t, "job", repo.marker("FindByID"))
		assert.Equal(t, "job", repo.marker("SetLastRunAt"), "the detached ctx keeps the values")
	})

	t.Run("reload", func(t *testing.T) {
		t.Parallel()
		repo := newCtxPolicyRepo()
		require.NoError(t, repo.mockPolicyRepo.Create(context.Background(), newTestPolicy("p1", "0 2 * * *")))
		s := NewScheduler(repo, &mockInstanceRepo{}, &mockAuditRepo{}, nil, nil)
		require.NoError(t, s.Start())
		t.Cleanup(s.Stop)

		require.NoError(t, s.Reload(markedContext("reload")))

		assert.Equal(t, "reload", repo.marker("ListEnabled"))
	})

	t.Run("activation of a term", func(t *testing.T) {
		t.Parallel()
		repo := newCtxPolicyRepo()
		require.NoError(t, repo.mockPolicyRepo.Create(context.Background(), newTestPolicy("p1", "0 2 * * *")))
		s := NewScheduler(repo, &mockInstanceRepo{}, &mockAuditRepo{}, nil, nil)
		t.Cleanup(s.Stop)

		require.NoError(t, s.activate(markedContext("term")))
		s.deactivate()

		assert.Equal(t, "term", repo.marker("ListEnabled"))
	})

	t.Run("manual run reads the policy with the caller context", func(t *testing.T) {
		t.Parallel()
		repo := newCtxPolicyRepo()
		require.NoError(t, repo.mockPolicyRepo.Create(context.Background(), newTestPolicy("p1", "0 2 * * *")))
		s := NewScheduler(repo, &mockInstanceRepo{}, &mockAuditRepo{}, nil, nil)
		t.Cleanup(s.Stop)

		_, err := s.RunPolicy(markedContext("request"), "p1", true)

		require.NoError(t, err)
		assert.Equal(t, "request", repo.marker("FindByID"))
	})
}

// TestScheduler_RunPolicy_CancelledCallerContext checks that a manual run
// stops at the policy read when the caller context is done, and starts no
// action (#656).
func TestScheduler_RunPolicy_CancelledCallerContext(t *testing.T) {
	t.Parallel()

	repo := newCtxPolicyRepo()
	require.NoError(t, repo.mockPolicyRepo.Create(context.Background(), newTestPolicy("p1", "0 2 * * *")))
	exec := &mockExecutor{}
	s := NewScheduler(repo, &mockInstanceRepo{instances: []models.StackInstance{{ID: "i1", Name: "i1", Status: models.StackStatusStopped}}},
		&mockAuditRepo{}, exec, nil)
	t.Cleanup(s.Stop)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	results, err := s.RunPolicy(ctx, "p1", false)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, results)
	assert.Empty(t, exec.stopped, "no action started")
}

// TestScheduler_RunPolicy_CallerLeavesAfterTheRead checks that a manual run
// that has read its policy runs to the end when the caller context ends: the
// run uses the scheduler context, and its audit entry is written (#656).
func TestScheduler_RunPolicy_CallerLeavesAfterTheRead(t *testing.T) {
	t.Parallel()

	repo := newCtxPolicyRepo()
	require.NoError(t, repo.mockPolicyRepo.Create(context.Background(), newTestPolicy("p1", "0 2 * * *")))
	exec := &mockExecutor{}
	audit := &mockAuditRepo{}
	s := NewScheduler(repo, &mockInstanceRepo{instances: []models.StackInstance{{ID: "i1", Name: "i1", Status: models.StackStatusStopped}}},
		audit, exec, nil)
	t.Cleanup(s.Stop)
	ctx, cancel := context.WithCancel(context.Background())
	repo.afterFind = cancel

	results, err := s.RunPolicy(ctx, "p1", false)

	require.NoError(t, err)
	require.Error(t, ctx.Err(), "the caller left during the run")
	require.Len(t, results, 1)
	assert.Equal(t, "success", results[0].Status)
	assert.Equal(t, []string{"i1"}, exec.stopped)
	assert.Len(t, audit.entries, 1)
}

// cancelAfterStopExecutor ends the run context after a successful stop, as the
// end of a leadership term does.
type cancelAfterStopExecutor struct {
	mockExecutor
	cancel context.CancelFunc
}

func (e *cancelAfterStopExecutor) StopInstance(ctx context.Context, inst *models.StackInstance) error {
	err := e.mockExecutor.StopInstance(ctx, inst)
	e.cancel()
	return err
}

// TestScheduler_AuditEntryOfAnExecutedActionSurvivesTheEndOfTheTerm checks
// that the audit entry of an action that ran is written when the leadership
// term ends right after the action (#656).
func TestScheduler_AuditEntryOfAnExecutedActionSurvivesTheEndOfTheTerm(t *testing.T) {
	t.Parallel()

	repo := newCtxPolicyRepo()
	require.NoError(t, repo.mockPolicyRepo.Create(context.Background(), newTestPolicy("p1", "0 2 * * *")))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exec := &cancelAfterStopExecutor{cancel: cancel}
	audit := &mockAuditRepo{}
	s := NewScheduler(repo, &mockInstanceRepo{instances: []models.StackInstance{{ID: "i1", Name: "i1", Status: models.StackStatusStopped}}},
		audit, exec, nil)
	t.Cleanup(s.Stop)

	s.executeScheduledPolicy(ctx, "p1")

	require.Error(t, ctx.Err(), "the term ended during the run")
	require.Equal(t, []string{"i1"}, exec.stopped)
	require.Len(t, audit.entries, 1, "the stop is audited")
	assert.Equal(t, "cleanup_policy_executed", audit.entries[0].Action)
}

// cancelAtLastRunRepo ends the term context when the run writes LastRunAt:
// the term ends after the check of the context and before the write.
type cancelAtLastRunRepo struct {
	*ctxPolicyRepo
	cancel context.CancelFunc
}

// FindByID returns a copy, as a database read does: the run must not record
// LastRunAt by changing the stored object.
func (r *cancelAtLastRunRepo) FindByID(ctx context.Context, id string) (*models.CleanupPolicy, error) {
	p, err := r.ctxPolicyRepo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	cp := *p
	return &cp, nil
}

func (r *cancelAtLastRunRepo) SetLastRunAt(ctx context.Context, id string, t time.Time) error {
	r.cancel()
	return r.ctxPolicyRepo.SetLastRunAt(ctx, id, t)
}

// TestScheduler_CompletedRunRecordsLastRunAtWhenTheTermEnds checks that a run
// that completed keeps its LastRunAt when the leadership term ends right
// before the write. Else the next leader runs the policy again (#656).
func TestScheduler_CompletedRunRecordsLastRunAtWhenTheTermEnds(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repo := &cancelAtLastRunRepo{ctxPolicyRepo: newCtxPolicyRepo(), cancel: cancel}
	require.NoError(t, repo.mockPolicyRepo.Create(context.Background(), newTestPolicy("p1", "0 2 * * *")))
	s := NewScheduler(repo, &mockInstanceRepo{}, &mockAuditRepo{}, nil, nil)
	t.Cleanup(s.Stop)

	s.executeScheduledPolicy(ctx, "p1")

	require.Error(t, ctx.Err(), "the term ended before the write")
	got, err := repo.mockPolicyRepo.FindByID(context.Background(), "p1")
	require.NoError(t, err)
	assert.NotNil(t, got.LastRunAt, "the completed run is recorded")
}

// editDuringRunExecutor stores an admin edit of the policy while the run
// executes an action.
type editDuringRunExecutor struct {
	mockExecutor
	repo *mockPolicyRepo
}

func (e *editDuringRunExecutor) StopInstance(ctx context.Context, inst *models.StackInstance) error {
	edited := *newTestPolicy("p1", "0 2 * * *")
	edited.Enabled = false
	edited.Name = "edited"
	if err := e.repo.Update(context.Background(), &edited); err != nil {
		return err
	}
	return e.mockExecutor.StopInstance(ctx, inst)
}

// TestScheduler_ScheduledRunKeepsAnEditMadeDuringTheRun checks that the run
// writes LastRunAt only: an admin edit during the run is not overwritten with
// the policy that the run read at its start (#656).
func TestScheduler_ScheduledRunKeepsAnEditMadeDuringTheRun(t *testing.T) {
	t.Parallel()

	repo := newMockPolicyRepo()
	require.NoError(t, repo.Create(context.Background(), newTestPolicy("p1", "0 2 * * *")))
	exec := &editDuringRunExecutor{repo: repo}
	s := NewScheduler(repo, &mockInstanceRepo{instances: []models.StackInstance{{ID: "i1", Name: "i1", Status: models.StackStatusStopped}}},
		&mockAuditRepo{}, exec, nil)
	t.Cleanup(s.Stop)

	s.executeScheduledPolicy(context.Background(), "p1")

	require.Equal(t, []string{"i1"}, exec.stopped)
	got, err := repo.FindByID(context.Background(), "p1")
	require.NoError(t, err)
	assert.False(t, got.Enabled, "the edit stays")
	assert.Equal(t, "edited", got.Name)
	assert.NotNil(t, got.LastRunAt, "the run is recorded")
}

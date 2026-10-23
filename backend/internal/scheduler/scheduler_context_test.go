package scheduler

import (
	"context"
	"sync"
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type jobCtxKey struct{}

// ctxRecordingInstanceRepo records the marker of the context of List and
// FindByCluster.
type ctxRecordingInstanceRepo struct {
	*mockInstanceRepo
	mu   sync.Mutex
	seen map[string]any
}

func (r *ctxRecordingInstanceRepo) record(ctx context.Context, method string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen[method] = ctx.Value(jobCtxKey{})
}

func (r *ctxRecordingInstanceRepo) get(method string) (any, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.seen[method]
	return v, ok
}

func (r *ctxRecordingInstanceRepo) List(ctx context.Context) ([]models.StackInstance, error) {
	r.record(ctx, "List")
	return r.mockInstanceRepo.List(ctx)
}

func (r *ctxRecordingInstanceRepo) FindByCluster(ctx context.Context, clusterID string) ([]models.StackInstance, error) {
	r.record(ctx, "FindByCluster")
	return r.mockInstanceRepo.FindByCluster(ctx, clusterID)
}

// TestScheduler_ScheduledRunUsesJobContext checks that a scheduled policy run
// reads the instances with the context of the cron job (the leadership
// term), not a background context (#656).
func TestScheduler_ScheduledRunUsesJobContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		clusterID string
		method    string
	}{
		{name: "all clusters", clusterID: "all", method: "List"},
		{name: "one cluster", clusterID: "c1", method: "FindByCluster"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			policyRepo := newMockPolicyRepo()
			p := newTestPolicy("p1", "0 2 * * *")
			p.ClusterID = tt.clusterID
			require.NoError(t, policyRepo.Create(context.Background(), p))
			repo := &ctxRecordingInstanceRepo{mockInstanceRepo: &mockInstanceRepo{}, seen: map[string]any{}}
			s := NewScheduler(policyRepo, repo, &mockAuditRepo{}, nil, nil)
			t.Cleanup(s.Stop)

			s.executeScheduledPolicy(context.WithValue(context.Background(), jobCtxKey{}, "job"), "p1")

			v, ok := repo.get(tt.method)
			require.True(t, ok, tt.method)
			assert.Equal(t, "job", v, "%s runs with the job context", tt.method)
		})
	}
}

// TestScheduler_RunPolicyUsesSchedulerContext checks that a manual run keeps
// the context of the scheduler (s.ctx), never the request: the handler calls
// RunPolicy synchronously. Stop cancels s.ctx, and the read sees it.
func TestScheduler_RunPolicyUsesSchedulerContext(t *testing.T) {
	t.Parallel()
	policyRepo := newMockPolicyRepo()
	require.NoError(t, policyRepo.Create(context.Background(), newTestPolicy("p1", "0 2 * * *")))
	s := NewScheduler(policyRepo, &mockInstanceRepo{}, &mockAuditRepo{}, nil, nil)

	_, err := s.RunPolicy(context.Background(), "p1", true)
	require.NoError(t, err)

	s.Stop()
	_, err = s.RunPolicy(context.Background(), "p1", true)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled, "the instance read runs with s.ctx")
}

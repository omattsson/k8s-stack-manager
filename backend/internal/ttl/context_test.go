package ttl

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type workerCtxKey struct{}

// workerCtx returns a cancellable context that carries a marker value, so a
// test can see which context reached the repository.
func workerCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithCancel(context.WithValue(context.Background(), workerCtxKey{}, "worker"))
}

// ctxRecorder records the marker of the context of each repository call.
type ctxRecorder struct {
	models.StackInstanceRepository
	mu   sync.Mutex
	seen map[string]any
}

func newCtxRecorder(inner models.StackInstanceRepository) *ctxRecorder {
	return &ctxRecorder{StackInstanceRepository: inner, seen: map[string]any{}}
}

func (r *ctxRecorder) record(ctx context.Context, method string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen[method] = ctx.Value(workerCtxKey{})
}

func (r *ctxRecorder) marker(method string) (any, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.seen[method]
	return v, ok
}

func (r *ctxRecorder) ListExpired(ctx context.Context) ([]*models.StackInstance, error) {
	r.record(ctx, "ListExpired")
	return r.StackInstanceRepository.ListExpired(ctx)
}

func (r *ctxRecorder) Update(ctx context.Context, inst *models.StackInstance) error {
	r.record(ctx, "Update")
	return r.StackInstanceRepository.Update(ctx, inst)
}

func (*ctxRecorder) FindByIDs(ctx context.Context, _ []string) (map[string]*models.StackInstance, error) {
	return nil, ctx.Err()
}

func (r *ctxRecorder) ListExpiringSoon(ctx context.Context, threshold time.Duration) ([]*models.StackInstance, error) {
	r.record(ctx, "ListExpiringSoon")
	return r.StackInstanceRepository.ListExpiringSoon(ctx, threshold)
}

func (r *ctxRecorder) MarkExpiryWarned(ctx context.Context, id string, expiresAt, warnedAt time.Time) (bool, error) {
	r.record(ctx, "MarkExpiryWarned")
	return r.StackInstanceRepository.MarkExpiryWarned(ctx, id, expiresAt, warnedAt)
}

// ctxStopper records the marker of the context of StopInstance.
type ctxStopper struct {
	mu   sync.Mutex
	seen any
	got  bool
}

func (s *ctxStopper) StopInstance(ctx context.Context, _ *models.StackInstance) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen, s.got = ctx.Value(workerCtxKey{}), true
	return nil
}

func (s *ctxStopper) marker() (any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen, s.got
}

// runUntil runs run(ctx) in a goroutine until done reports true, then
// cancels ctx and waits for run to return.
func runUntil(t *testing.T, ctx context.Context, cancel context.CancelFunc, run func(context.Context), done func() bool) {
	t.Helper()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		run(ctx)
	}()
	require.Eventually(t, done, 5*time.Second, 5*time.Millisecond)
	cancel()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker did not stop")
	}
}

// TestReaper_UsesWorkerContext checks that the reaper runs its queries and
// the stop start with the context of the worker (the leadership term), not
// a background context (#656).
func TestReaper_UsesWorkerContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		stopper bool
	}{
		{name: "stopper starts the stop", stopper: true},
		{name: "no stopper marks the instance stopped", stopper: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			inner := newMockInstanceRepo()
			past := time.Now().Add(-time.Minute)
			require.NoError(t, inner.Create(context.Background(), &models.StackInstance{
				ID: "exp-1", Name: "exp", Status: models.StackStatusRunning, TTLMinutes: 60, ExpiresAt: &past,
			}))
			repo := newCtxRecorder(inner)
			stopper := &ctxStopper{}
			var s InstanceStopper
			if tt.stopper {
				s = stopper
			}
			reaper := NewReaper(repo, nil, nil, s, time.Hour)
			ctx, cancel := workerCtx(t)
			defer cancel()

			runUntil(t, ctx, cancel, reaper.Run, func() bool {
				if tt.stopper {
					_, ok := stopper.marker()
					return ok
				}
				_, ok := repo.marker("Update")
				return ok
			})

			v, ok := repo.marker("ListExpired")
			require.True(t, ok)
			assert.Equal(t, "worker", v, "ListExpired runs with the worker context")
			if tt.stopper {
				v, _ = stopper.marker()
				assert.Equal(t, "worker", v, "StopInstance gets the worker context")
				return
			}
			v, _ = repo.marker("Update")
			assert.Equal(t, "worker", v, "Update runs with the worker context")
		})
	}
}

// TestWarner_UsesWorkerContext checks that the expiry warner runs its list
// and its conditional mark with the context of the worker (#656).
func TestWarner_UsesWorkerContext(t *testing.T) {
	t.Parallel()
	expiresAt := time.Now().Add(10 * time.Minute)
	repo := newCtxRecorder(&warnerMockInstanceRepo{
		expiringSoonItems: []*models.StackInstance{
			{ID: "inst-1", Name: "soon", OwnerID: "u1", Status: models.StackStatusRunning, ExpiresAt: &expiresAt},
		},
	})
	w := NewWarner(repo, &mockExpiryNotifier{}, 30*time.Minute, time.Hour)
	ctx, cancel := workerCtx(t)
	defer cancel()

	runUntil(t, ctx, cancel, w.Run, func() bool {
		_, ok := repo.marker("MarkExpiryWarned")
		return ok
	})

	for _, method := range []string{"ListExpiringSoon", "MarkExpiryWarned"} {
		v, ok := repo.marker(method)
		require.True(t, ok, method)
		assert.Equal(t, "worker", v, "%s runs with the worker context", method)
	}
}

// TestReaper_CancelledWorkerStopsQueries checks that a done worker context
// reaches the repository: the cycle lists nothing and changes nothing.
func TestReaper_CancelledWorkerStopsQueries(t *testing.T) {
	t.Parallel()
	repo := newMockInstanceRepo()
	past := time.Now().Add(-time.Minute)
	require.NoError(t, repo.Create(context.Background(), &models.StackInstance{
		ID: "exp-1", Name: "exp", Status: models.StackStatusRunning, TTLMinutes: 60, ExpiresAt: &past,
	}))
	reaper := NewReaper(repo, nil, nil, nil, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	reaper.processExpired(ctx)

	assert.Equal(t, models.StackStatusRunning, repo.get("exp-1").Status, "a cancelled cycle changes nothing")
}

// updateCounter counts Update calls and runs onUpdate before each.
type updateCounter struct {
	models.StackInstanceRepository
	updates  atomic.Int32
	onUpdate func()
}

func (r *updateCounter) Update(ctx context.Context, inst *models.StackInstance) error {
	r.updates.Add(1)
	if r.onUpdate != nil {
		r.onUpdate()
	}
	return r.StackInstanceRepository.Update(ctx, inst)
}

// cancelStopper ends the worker context at each stop start, as a lost
// leadership does, and fails the stop.
type cancelStopper struct {
	cancel context.CancelFunc
	calls  atomic.Int32
}

func (s *cancelStopper) StopInstance(ctx context.Context, _ *models.StackInstance) error {
	s.calls.Add(1)
	s.cancel()
	return ctx.Err()
}

// TestReaper_LostLeadershipEndsCycle checks that the cycle stops when the
// worker context ends: the other expired instances get no stop start and no
// fallback update (two error lines each), and none is changed.
func TestReaper_LostLeadershipEndsCycle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		stopper     bool
		wantStops   int32
		wantUpdates int32
	}{
		{name: "stop start fails", stopper: true, wantStops: 1, wantUpdates: 0},
		{name: "fallback update fails", stopper: false, wantStops: 0, wantUpdates: 1},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			inner := newMockInstanceRepo()
			past := time.Now().Add(-time.Minute)
			for i := range 3 {
				require.NoError(t, inner.Create(context.Background(), &models.StackInstance{
					ID: fmt.Sprintf("exp-%d", i), Name: "exp", Status: models.StackStatusRunning, TTLMinutes: 60, ExpiresAt: &past,
				}))
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			repo := &updateCounter{StackInstanceRepository: inner}
			stopper := &cancelStopper{cancel: cancel}
			var s InstanceStopper
			if tt.stopper {
				s = stopper
			} else {
				repo.onUpdate = cancel
			}

			NewReaper(repo, nil, nil, s, time.Hour).processExpired(ctx)

			assert.Equal(t, tt.wantStops, stopper.calls.Load(), "stop starts")
			assert.Equal(t, tt.wantUpdates, repo.updates.Load(), "fallback updates")
			for i := range 3 {
				assert.Equal(t, models.StackStatusRunning, inner.get(fmt.Sprintf("exp-%d", i)).Status)
			}
		})
	}
}

// TestReaper_CycleSpanStatus checks the status of the cycle span: Ok for a
// full cycle, Error with the context error after a lost leadership.
// Not parallel: it replaces the package tracer reaperTracer.
func TestReaper_CycleSpanStatus(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prev := reaperTracer
	reaperTracer = sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)).Tracer("test")
	t.Cleanup(func() { reaperTracer = prev })

	tests := []struct {
		name       string
		lost       bool
		wantStatus codes.Code
	}{
		{name: "full cycle", wantStatus: codes.Ok},
		{name: "lost leadership", lost: true, wantStatus: codes.Error},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec.Reset()
			inner := newMockInstanceRepo()
			past := time.Now().Add(-time.Minute)
			require.NoError(t, inner.Create(context.Background(), &models.StackInstance{
				ID: "exp-1", Name: "exp", Status: models.StackStatusRunning, TTLMinutes: 60, ExpiresAt: &past,
			}))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var s InstanceStopper = &ctxStopper{}
			if tt.lost {
				s = &cancelStopper{cancel: cancel}
			}

			NewReaper(inner, nil, nil, s, time.Hour).processExpired(ctx)

			spans := rec.Ended()
			require.Len(t, spans, 1)
			assert.Equal(t, "ttl.reap_cycle", spans[0].Name())
			assert.Equal(t, tt.wantStatus, spans[0].Status().Code)
			if tt.lost {
				assert.Equal(t, context.Canceled.Error(), spans[0].Status().Description)
				require.NotEmpty(t, spans[0].Events(), "the context error is recorded")
			}
		})
	}
}

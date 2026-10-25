package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"backend/internal/api/handlers"
	"backend/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cleanupCtxKey struct{}

// deleteRecordingRepo records the contexts of DeleteExpired.
type deleteRecordingRepo struct {
	stubRefreshTokenRepo
	ctxs []context.Context
	mu   sync.Mutex
}

func (r *deleteRecordingRepo) DeleteExpired(ctx context.Context, _ time.Time) (int64, error) {
	r.mu.Lock()
	r.ctxs = append(r.ctxs, ctx)
	r.mu.Unlock()
	return 0, ctx.Err()
}

func (r *deleteRecordingRepo) recorded() []context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]context.Context(nil), r.ctxs...)
}

// TestRefreshTokenCleanupRun_UsesWorkerContext checks that the leader worker
// refresh-token-cleanup deletes with the worker context: its values reach
// the repository at the start of the term and on each tick, and the end of
// the term cancels it (#656).
func TestRefreshTokenCleanupRun_UsesWorkerContext(t *testing.T) {
	t.Parallel()
	repo := &deleteRecordingRepo{}
	h := handlers.NewAuthHandler(&stubUserRepo{}, &config.AuthConfig{}, &config.OIDCConfig{})
	h.SetRefreshTokenRepo(repo)

	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), cleanupCtxKey{}, "worker"))
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		refreshTokenCleanupRun(h.CleanupExpiredTokens, 5*time.Millisecond)(ctx)
	}()

	require.Eventually(t, func() bool { return len(repo.recorded()) >= 2 }, 2*time.Second, time.Millisecond,
		"the cleanup runs at the start and on a tick")
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the worker did not stop when its context ended")
	}

	for i, got := range repo.recorded() {
		assert.Equal(t, "worker", got.Value(cleanupCtxKey{}), "call %d gets the worker context", i)
		assert.ErrorIs(t, got.Err(), context.Canceled, "call %d ends with the term", i)
	}
}

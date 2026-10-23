package scheduler

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestScheduler_RunEndOfTermLogsNoError checks that the end of a leadership
// term is not logged as an error: not at the activation retry, not at the
// periodic reload. It replaces the default logger, so it does not run in
// parallel.
func TestScheduler_RunEndOfTermLogsNoError(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })

	t.Run("activation retry", func(t *testing.T) {
		repo := newMockPolicyRepo()
		require.NoError(t, repo.Create(context.Background(), newTestPolicy("p1", "0 2 * * *")))
		s := NewScheduler(repo, &mockInstanceRepo{}, &mockAuditRepo{}, nil, nil)
		t.Cleanup(s.Stop)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		s.Run(ctx)

		assert.NotContains(t, buf.String(), "failed to load policies")
	})

	t.Run("periodic reload", func(t *testing.T) {
		repo := &failAfterCancelRepo{mockPolicyRepo: newMockPolicyRepo()}
		require.NoError(t, repo.Create(context.Background(), newTestPolicy("p1", "0 2 * * *")))
		s := NewScheduler(repo, &mockInstanceRepo{}, &mockAuditRepo{}, nil, nil).WithReloadInterval(5 * time.Millisecond)
		t.Cleanup(s.Stop)
		ctx, cancel := context.WithCancel(context.Background())
		repo.cancel = cancel
		done := make(chan struct{})
		go func() { s.Run(ctx); close(done) }()
		<-done

		assert.True(t, repo.reloaded, "the reload ran and failed with the cancelled context")
		assert.NotContains(t, buf.String(), "periodic reload failed")
	})
}

// failAfterCancelRepo ends the term at the first reload, so the reload fails
// with the context error.
type failAfterCancelRepo struct {
	*mockPolicyRepo
	cancel   context.CancelFunc
	calls    int
	reloaded bool
}

func (r *failAfterCancelRepo) ListEnabled(ctx context.Context) ([]models.CleanupPolicy, error) {
	r.calls++
	if r.calls > 1 {
		r.reloaded = true
		r.cancel()
		return nil, ctx.Err()
	}
	return r.mockPolicyRepo.ListEnabled(ctx)
}

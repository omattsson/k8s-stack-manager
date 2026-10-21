package main

import (
	"context"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type touchCtxKey struct{}

// touchRecordingRepo records TouchFamily calls.
type touchRecordingRepo struct {
	stubRefreshTokenRepo
	ctx    context.Context
	family string
	at     time.Time
}

func (r *touchRecordingRepo) TouchFamily(ctx context.Context, familyID string, at time.Time) error {
	r.ctx = ctx
	r.family = familyID
	r.at = at
	return ctx.Err()
}

func TestSessionActivityFunc(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		repo    models.RefreshTokenRepository
		wantNil bool
	}{
		{name: "nil repository disables the hook", repo: nil, wantNil: true},
		{name: "repository hook touches the family", repo: &touchRecordingRepo{}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fn := sessionActivityFunc(tt.repo)
			if tt.wantNil {
				assert.Nil(t, fn)
				return
			}
			require.NotNil(t, fn)
			at := time.Now().UTC()
			// The middleware passes a detached context with its own deadline
			// (middleware.sessionActivityTracker.touch); the hook keeps it.
			ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), touchCtxKey{}, "touch"), time.Minute)
			defer cancel()
			require.NoError(t, fn(ctx, "fam-1", at))
			rec := tt.repo.(*touchRecordingRepo)
			assert.Equal(t, "fam-1", rec.family)
			assert.True(t, rec.at.Equal(at))
			assert.Equal(t, "touch", rec.ctx.Value(touchCtxKey{}), "the hook passes its context to TouchFamily")
			_, hasDeadline := rec.ctx.Deadline()
			assert.True(t, hasDeadline, "the activity deadline reaches the query")
		})
	}
}

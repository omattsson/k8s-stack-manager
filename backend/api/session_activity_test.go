package main

import (
	"context"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// touchRecordingRepo records TouchFamily calls.
type touchRecordingRepo struct {
	stubRefreshTokenRepo
	family string
	at     time.Time
}

func (r *touchRecordingRepo) TouchFamily(_ context.Context, familyID string, at time.Time) error {
	r.family = familyID
	r.at = at
	return nil
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
			require.NoError(t, fn(context.Background(), "fam-1", at))
			rec := tt.repo.(*touchRecordingRepo)
			assert.Equal(t, "fam-1", rec.family)
			assert.True(t, rec.at.Equal(at))
		})
	}
}

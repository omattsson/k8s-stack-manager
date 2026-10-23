package ttl

import (
	"context"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cancelAfterStop ends the worker context after a successful stop, as the
// end of a leadership term does.
type cancelAfterStop struct{ cancel context.CancelFunc }

func (c cancelAfterStop) StopInstance(context.Context, *models.StackInstance) error {
	c.cancel()
	return nil
}

// TestReaper_ExpiryAuditSurvivesTheEndOfTheTerm checks that the audit entry
// of an expired instance is written when the leadership term ends right after
// the stop started (#656).
func TestReaper_ExpiryAuditSurvivesTheEndOfTheTerm(t *testing.T) {
	t.Parallel()

	repo := newMockInstanceRepo()
	past := time.Now().Add(-time.Minute)
	require.NoError(t, repo.Create(context.Background(), &models.StackInstance{
		ID: "exp-1", Name: "exp", OwnerID: "owner-1",
		Status: models.StackStatusRunning, TTLMinutes: 30, ExpiresAt: &past,
	}))
	auditRepo := &mockAuditRepo{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reaper := NewReaper(repo, auditRepo, nil, cancelAfterStop{cancel: cancel}, time.Hour)

	reaper.processExpired(ctx)

	require.Error(t, ctx.Err(), "the term ended during the cycle")
	assert.Equal(t, 1, auditRepo.count())
}

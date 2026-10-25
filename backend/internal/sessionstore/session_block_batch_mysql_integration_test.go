//go:build integration

package sessionstore

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMySQLIntegration_BlockSessions writes more sessions than one batch
// (sessionBlockBatch), so the multi-row upsert runs several times, and checks
// that every session is blocked, that a duplicate is written once, and that
// an earlier until does not shorten an existing block.
func TestMySQLIntegration_BlockSessions(t *testing.T) {
	s := setupMySQLSessionStore(t)
	ctx := context.Background()

	n := 2*sessionBlockBatch + 7
	ids := make([]string, 0, n+1)
	for i := 0; i < n; i++ {
		ids = append(ids, "sid-batch-"+strconv.Itoa(i))
	}
	ids = append(ids, "sid-batch-0") // duplicate

	// One long block in each of the three batches: every batch must keep
	// the GREATEST rule on conflict.
	longBlocked := []string{"sid-batch-1", "sid-batch-" + strconv.Itoa(sessionBlockBatch+100), "sid-batch-" + strconv.Itoa(n-2)}
	for _, id := range longBlocked {
		require.NoError(t, s.BlockSession(ctx, id, time.Now().Add(2*time.Hour)))
	}
	require.NoError(t, s.BlockSessions(ctx, ids, time.Now().Add(time.Hour)))

	var count int64
	require.NoError(t, s.db.Model(&SessionEntry{}).Where("kind = ? AND entry_key LIKE ?", kindSessionBlock, "sid-batch-%").Count(&count).Error)
	assert.Equal(t, int64(n), count, "one row per distinct session")

	for _, id := range []string{"sid-batch-0", "sid-batch-" + strconv.Itoa(sessionBlockBatch), "sid-batch-" + strconv.Itoa(n-1)} {
		blocked, err := s.IsSessionBlocked(ctx, id)
		require.NoError(t, err)
		assert.True(t, blocked, id)
	}

	for _, id := range longBlocked {
		var row SessionEntry
		require.NoError(t, s.db.Where("entry_key = ? AND kind = ?", id, kindSessionBlock).First(&row).Error)
		assert.Greater(t, row.ExpiresAt, time.Now().Add(90*time.Minute).Unix(), "%s: the earlier batch until must not shorten the 2h block", id)
	}

	require.NoError(t, s.BlockSessions(ctx, nil, time.Now().Add(time.Hour)), "an empty batch writes nothing")
}

// TestMySQLIntegration_BlockSessions_PartialWrite checks the documented
// behaviour of a failing later batch: the earlier batches stay written, and
// the error says how many sessions were blocked. An ID longer than the
// entry_key column (255) fails the second batch under the strict sql_mode.
func TestMySQLIntegration_BlockSessions_PartialWrite(t *testing.T) {
	s := setupMySQLSessionStore(t)
	ctx := context.Background()

	ids := make([]string, 0, sessionBlockBatch+2)
	for i := 0; i < sessionBlockBatch+1; i++ {
		ids = append(ids, "sid-part-"+strconv.Itoa(i))
	}
	ids = append(ids, strings.Repeat("x", 300)) // in the second batch

	err := s.BlockSessions(ctx, ids, time.Now().Add(time.Hour))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "blocked "+strconv.Itoa(sessionBlockBatch)+" of "+strconv.Itoa(len(ids))+" sessions")

	blocked, err := s.IsSessionBlocked(ctx, "sid-part-0")
	require.NoError(t, err)
	assert.True(t, blocked, "the first batch stays written")

	blocked, err = s.IsSessionBlocked(ctx, "sid-part-"+strconv.Itoa(sessionBlockBatch))
	require.NoError(t, err)
	assert.False(t, blocked, "the failed second batch wrote nothing")
}

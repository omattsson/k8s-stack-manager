package sessionstore

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryStore_SessionBlock(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		blockID string
		until   time.Duration
		check   string
		want    bool
	}{
		{name: "blocked session", blockID: "fam-1", until: time.Hour, check: "fam-1", want: true},
		{name: "other session passes", blockID: "fam-1", until: time.Hour, check: "fam-2", want: false},
		{name: "expired block passes", blockID: "fam-1", until: -time.Minute, check: "fam-1", want: false},
		{name: "never blocked", blockID: "", check: "fam-1", want: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := NewMemoryStore()
			defer s.Stop()
			ctx := context.Background()

			if tt.blockID != "" {
				require.NoError(t, s.BlockSession(ctx, tt.blockID, time.Now().Add(tt.until)))
			}
			got, err := s.IsSessionBlocked(ctx, tt.check)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestMemoryStore_SessionBlock_IndependentOfTokenAndUserBlocks(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore()
	defer s.Stop()
	ctx := context.Background()

	require.NoError(t, s.BlockToken(ctx, "same-id", time.Now().Add(time.Hour)))
	require.NoError(t, s.BlockUser(ctx, "same-id", time.Now().Add(time.Hour)))

	blocked, err := s.IsSessionBlocked(ctx, "same-id")
	require.NoError(t, err)
	assert.False(t, blocked, "a jti or user block must not block a session")

	require.NoError(t, s.BlockSession(ctx, "sess", time.Now().Add(time.Hour)))
	blocked, err = s.IsTokenBlocked(ctx, "sess")
	require.NoError(t, err)
	assert.False(t, blocked, "a session block must not block a jti")
}

func TestMemoryStore_SessionBlock_Cleanup(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore()
	defer s.Stop()
	ctx := context.Background()

	require.NoError(t, s.BlockSession(ctx, "old", time.Now().Add(-time.Minute)))
	require.NoError(t, s.BlockSession(ctx, "live", time.Now().Add(time.Hour)))
	require.NoError(t, s.Cleanup(ctx))

	s.mu.Lock()
	_, hasOld := s.sessionBlocks["old"]
	_, hasLive := s.sessionBlocks["live"]
	s.mu.Unlock()
	assert.False(t, hasOld)
	assert.True(t, hasLive)
}

func TestMemoryStore_SessionBlock_ReblockEarlierDoesNotShorten(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore()
	defer s.Stop()
	ctx := context.Background()

	require.NoError(t, s.BlockSession(ctx, "fam-1", time.Now().Add(time.Hour)))
	require.NoError(t, s.BlockSession(ctx, "fam-1", time.Now().Add(-time.Minute)))
	blocked, err := s.IsSessionBlocked(ctx, "fam-1")
	require.NoError(t, err)
	assert.True(t, blocked, "an earlier until must not shorten the block")

	require.NoError(t, s.BlockSession(ctx, "fam-2", time.Now().Add(-time.Minute)))
	require.NoError(t, s.BlockSession(ctx, "fam-2", time.Now().Add(time.Hour)))
	blocked, err = s.IsSessionBlocked(ctx, "fam-2")
	require.NoError(t, err)
	assert.True(t, blocked, "a later until extends the block")
}

func TestMemoryStore_BlockSessions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		ids   []string
		until time.Duration
		// before blocks fam-long for an hour first, to check that a batch
		// with an earlier until does not shorten it.
		before  bool
		want    map[string]bool
		notWant []string
	}{
		{name: "no sessions", ids: nil, until: time.Hour, notWant: []string{"fam-1"}},
		{name: "every session of the batch", ids: []string{"fam-1", "fam-2", "fam-3"}, until: time.Hour, want: map[string]bool{"fam-1": true, "fam-2": true, "fam-3": true}},
		{name: "a duplicate ID is harmless", ids: []string{"fam-1", "fam-1"}, until: time.Hour, want: map[string]bool{"fam-1": true}},
		{name: "an earlier until does not shorten a block", ids: []string{"fam-long", "fam-new"}, until: -time.Minute, before: true, want: map[string]bool{"fam-long": true, "fam-new": false}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := NewMemoryStore()
			defer s.Stop()
			ctx := context.Background()
			if tt.before {
				require.NoError(t, s.BlockSession(ctx, "fam-long", time.Now().Add(time.Hour)))
			}

			require.NoError(t, s.BlockSessions(ctx, tt.ids, time.Now().Add(tt.until)))

			for id, want := range tt.want {
				blocked, err := s.IsSessionBlocked(ctx, id)
				require.NoError(t, err)
				assert.Equal(t, want, blocked, id)
			}
			for _, id := range tt.notWant {
				blocked, err := s.IsSessionBlocked(ctx, id)
				require.NoError(t, err)
				assert.False(t, blocked, id)
			}
		})
	}
}

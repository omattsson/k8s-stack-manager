package sessionstore

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryStore_BlockToken(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore()
	defer s.Stop()
	ctx := context.Background()

	blocked, err := s.IsTokenBlocked(ctx, "jti-1")
	require.NoError(t, err)
	assert.False(t, blocked)

	require.NoError(t, s.BlockToken(ctx, "jti-1", time.Now().Add(time.Hour)))

	blocked, err = s.IsTokenBlocked(ctx, "jti-1")
	require.NoError(t, err)
	assert.True(t, blocked)
}

func TestMemoryStore_BlockToken_Expiry(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore()
	defer s.Stop()
	ctx := context.Background()

	require.NoError(t, s.BlockToken(ctx, "jti-exp", time.Now().Add(50*time.Millisecond)))

	blocked, _ := s.IsTokenBlocked(ctx, "jti-exp")
	assert.True(t, blocked)

	time.Sleep(60 * time.Millisecond)

	blocked, _ = s.IsTokenBlocked(ctx, "jti-exp")
	assert.False(t, blocked)
}

func TestMemoryStore_OIDCState(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore()
	defer s.Stop()
	ctx := context.Background()

	data := OIDCStateData{CodeVerifier: "v1", RedirectURL: "/dash"}
	require.NoError(t, s.SaveOIDCState(ctx, "state-1", data, 5*time.Minute))

	got, err := s.ConsumeOIDCState(ctx, "state-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "v1", got.CodeVerifier)
	assert.Equal(t, "/dash", got.RedirectURL)

	// One-time use — second consume returns nil.
	got, err = s.ConsumeOIDCState(ctx, "state-1")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestMemoryStore_OIDCState_Expired(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore()
	defer s.Stop()
	ctx := context.Background()

	require.NoError(t, s.SaveOIDCState(ctx, "state-exp", OIDCStateData{
		CodeVerifier: "v",
		RedirectURL:  "/",
	}, time.Millisecond))

	time.Sleep(2 * time.Millisecond)

	got, err := s.ConsumeOIDCState(ctx, "state-exp")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestMemoryStore_Cleanup(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore()
	defer s.Stop()
	ctx := context.Background()

	require.NoError(t, s.BlockToken(ctx, "jti-clean", time.Now().Add(-time.Second)))
	require.NoError(t, s.SaveOIDCState(ctx, "state-clean", OIDCStateData{}, time.Millisecond))
	time.Sleep(2 * time.Millisecond)

	require.NoError(t, s.Cleanup(ctx))

	s.mu.Lock()
	assert.Empty(t, s.blocked)
	assert.Empty(t, s.oidcStates)
	s.mu.Unlock()
}

func TestMemoryStore_UnknownState(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore()
	defer s.Stop()

	got, err := s.ConsumeOIDCState(context.Background(), "no-such-state")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestMemoryStore_BlockUser(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore()
	ctx := context.Background()

	blocked, err := s.IsUserBlocked(ctx, "user-1")
	require.NoError(t, err)
	assert.False(t, blocked)

	require.NoError(t, s.BlockUser(ctx, "user-1", time.Now().Add(time.Hour)))

	blocked, err = s.IsUserBlocked(ctx, "user-1")
	require.NoError(t, err)
	assert.True(t, blocked)

	s.Stop()

	// Stop only halts the cleanup goroutine — the in-memory data persists.
	blocked, err = s.IsUserBlocked(ctx, "user-1")
	require.NoError(t, err)
	assert.True(t, blocked, "block entry should persist after Stop")
}

func TestMemoryStore_BlockUser_Expiry(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore()
	defer s.Stop()
	ctx := context.Background()

	require.NoError(t, s.BlockUser(ctx, "user-exp", time.Now().Add(50*time.Millisecond)))

	blocked, err := s.IsUserBlocked(ctx, "user-exp")
	require.NoError(t, err)
	assert.True(t, blocked)

	time.Sleep(60 * time.Millisecond)

	blocked, err = s.IsUserBlocked(ctx, "user-exp")
	require.NoError(t, err)
	assert.False(t, blocked, "user block should expire after TTL")
}

func TestMemoryStore_UnblockUser(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore()
	defer s.Stop()
	ctx := context.Background()

	require.NoError(t, s.BlockUser(ctx, "user-1", time.Now().Add(time.Hour)))

	blocked, err := s.IsUserBlocked(ctx, "user-1")
	require.NoError(t, err)
	require.True(t, blocked)

	require.NoError(t, s.UnblockUser(ctx, "user-1"))

	blocked, err = s.IsUserBlocked(ctx, "user-1")
	require.NoError(t, err)
	assert.False(t, blocked, "user should not be blocked after UnblockUser")
}

func TestMemoryStore_UnblockUser_NotBlocked(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore()
	defer s.Stop()

	err := s.UnblockUser(context.Background(), "never-blocked")
	assert.NoError(t, err, "unblocking a never-blocked user should not error")
}

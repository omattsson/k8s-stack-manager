//go:build integration

package sessionstore

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupMySQLSessionStore(t *testing.T) *MySQLStore {
	t.Helper()

	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		dsn = "root:rootpassword@tcp(localhost:3306)/app?charset=utf8mb4&parseTime=True&loc=Local"
	}

	// SkipDefaultTransaction as in production (database.NewFromAppConfig), so
	// multi-statement writes such as BlockSessions run as they do there.
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger:                 logger.Default.LogMode(logger.Silent),
		SkipDefaultTransaction: true,
	})
	require.NoError(t, err, "Failed to connect to MySQL — is the container running?")

	require.NoError(t, db.AutoMigrate(&SessionEntry{}))
	result := db.Exec("TRUNCATE TABLE session_entries")
	require.NoError(t, result.Error, "failed to truncate session_entries")

	s := NewMySQLStore(db)
	t.Cleanup(func() { s.Stop() })
	return s
}

func TestMySQLIntegration_BlockToken(t *testing.T) {
	s := setupMySQLSessionStore(t)
	ctx := context.Background()

	blocked, err := s.IsTokenBlocked(ctx, "jti-1")
	require.NoError(t, err)
	assert.False(t, blocked)

	require.NoError(t, s.BlockToken(ctx, "jti-1", time.Now().Add(time.Hour)))

	blocked, err = s.IsTokenBlocked(ctx, "jti-1")
	require.NoError(t, err)
	assert.True(t, blocked)
}

func TestMySQLIntegration_BlockToken_Expired(t *testing.T) {
	s := setupMySQLSessionStore(t)
	ctx := context.Background()

	require.NoError(t, s.BlockToken(ctx, "jti-exp", time.Now().Add(-time.Second)))

	blocked, err := s.IsTokenBlocked(ctx, "jti-exp")
	require.NoError(t, err)
	assert.False(t, blocked, "expired token should not appear blocked")
}

func TestMySQLIntegration_BlockToken_Upsert(t *testing.T) {
	s := setupMySQLSessionStore(t)
	ctx := context.Background()

	require.NoError(t, s.BlockToken(ctx, "jti-up", time.Now().Add(time.Minute)))
	require.NoError(t, s.BlockToken(ctx, "jti-up", time.Now().Add(time.Hour)))

	blocked, err := s.IsTokenBlocked(ctx, "jti-up")
	require.NoError(t, err)
	assert.True(t, blocked)
}

func TestMySQLIntegration_OIDCState(t *testing.T) {
	s := setupMySQLSessionStore(t)
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

func TestMySQLIntegration_OIDCState_Expired(t *testing.T) {
	s := setupMySQLSessionStore(t)
	ctx := context.Background()

	require.NoError(t, s.SaveOIDCState(ctx, "state-exp", OIDCStateData{
		CodeVerifier: "v",
		RedirectURL:  "/",
	}, time.Millisecond))

	time.Sleep(10 * time.Millisecond)

	got, err := s.ConsumeOIDCState(ctx, "state-exp")
	require.NoError(t, err)
	assert.Nil(t, got, "expired state should return nil")
}

func TestMySQLIntegration_OIDCState_Unknown(t *testing.T) {
	s := setupMySQLSessionStore(t)

	got, err := s.ConsumeOIDCState(context.Background(), "no-such-state")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestMySQLIntegration_Cleanup(t *testing.T) {
	s := setupMySQLSessionStore(t)
	ctx := context.Background()

	require.NoError(t, s.BlockToken(ctx, "jti-clean", time.Now().Add(-time.Second)))
	// expires_at is Unix seconds, so use a negative duration to ensure immediate expiry.
	require.NoError(t, s.SaveOIDCState(ctx, "state-clean", OIDCStateData{}, -time.Second))

	require.NoError(t, s.Cleanup(ctx))

	var count int64
	s.db.Model(&SessionEntry{}).Count(&count)
	assert.Equal(t, int64(0), count, "cleanup should remove all expired entries")
}

func TestMySQLIntegration_Cleanup_PreservesActive(t *testing.T) {
	s := setupMySQLSessionStore(t)
	ctx := context.Background()

	require.NoError(t, s.BlockToken(ctx, "jti-active", time.Now().Add(time.Hour)))
	require.NoError(t, s.BlockToken(ctx, "jti-expired", time.Now().Add(-time.Second)))
	require.NoError(t, s.Cleanup(ctx))

	var count int64
	s.db.Model(&SessionEntry{}).Count(&count)
	assert.Equal(t, int64(1), count, "cleanup should keep active entries")

	blocked, err := s.IsTokenBlocked(ctx, "jti-active")
	require.NoError(t, err)
	assert.True(t, blocked)
}

func TestMySQLIntegration_BlockUser(t *testing.T) {
	s := setupMySQLSessionStore(t)
	ctx := context.Background()

	blocked, err := s.IsUserBlocked(ctx, "user-1", oldToken)
	require.NoError(t, err)
	assert.False(t, blocked)

	require.NoError(t, s.BlockUser(ctx, "user-1", time.Now().Add(time.Hour)))

	blocked, err = s.IsUserBlocked(ctx, "user-1", oldToken)
	require.NoError(t, err)
	assert.True(t, blocked)
}

func TestMySQLIntegration_BlockUser_Expired(t *testing.T) {
	s := setupMySQLSessionStore(t)
	ctx := context.Background()

	require.NoError(t, s.BlockUser(ctx, "user-exp", time.Now().Add(-time.Second)))

	blocked, err := s.IsUserBlocked(ctx, "user-exp", oldToken)
	require.NoError(t, err)
	assert.False(t, blocked, "expired user block should not appear blocked")
}

func TestMySQLIntegration_UnblockUser(t *testing.T) {
	s := setupMySQLSessionStore(t)
	ctx := context.Background()

	require.NoError(t, s.BlockUser(ctx, "user-1", time.Now().Add(time.Hour)))

	blocked, err := s.IsUserBlocked(ctx, "user-1", oldToken)
	require.NoError(t, err)
	require.True(t, blocked)

	require.NoError(t, s.UnblockUser(ctx, "user-1"))

	blocked, err = s.IsUserBlocked(ctx, "user-1", oldToken)
	require.NoError(t, err)
	assert.False(t, blocked, "user should not be blocked after UnblockUser")
}

func TestMySQLIntegration_UnblockUser_NotBlocked(t *testing.T) {
	s := setupMySQLSessionStore(t)
	ctx := context.Background()

	err := s.UnblockUser(ctx, "never-blocked")
	assert.NoError(t, err, "unblocking a never-blocked user should not error")
}

func TestMySQLIntegration_BlockUser_IssuedAt(t *testing.T) {
	s := setupMySQLSessionStore(t)
	ctx := context.Background()

	require.NoError(t, s.BlockUser(ctx, "user-iat", time.Now().Add(time.Hour)))

	blocked, err := s.IsUserBlocked(ctx, "user-iat", oldToken)
	require.NoError(t, err)
	assert.True(t, blocked, "token issued before the block is revoked")

	blocked, err = s.IsUserBlocked(ctx, "user-iat", time.Now().Add(2*time.Second))
	require.NoError(t, err)
	assert.False(t, blocked, "token issued after the block stays valid")

	// A second block moves the block time forward (upsert updates data).
	require.NoError(t, s.BlockUser(ctx, "user-iat", time.Now().Add(time.Hour)))
	blocked, err = s.IsUserBlocked(ctx, "user-iat", oldToken)
	require.NoError(t, err)
	assert.True(t, blocked)
}

func TestMySQLIntegration_BlockSession(t *testing.T) {
	s := setupMySQLSessionStore(t)
	ctx := context.Background()

	blocked, err := s.IsSessionBlocked(ctx, "sid-1")
	require.NoError(t, err)
	assert.False(t, blocked)

	require.NoError(t, s.BlockSession(ctx, "sid-1", time.Now().Add(time.Hour)))

	blocked, err = s.IsSessionBlocked(ctx, "sid-1")
	require.NoError(t, err)
	assert.True(t, blocked)

	blocked, err = s.IsSessionBlocked(ctx, "sid-other")
	require.NoError(t, err)
	assert.False(t, blocked, "another session must not be blocked")
}

func TestMySQLIntegration_BlockSession_Expired(t *testing.T) {
	s := setupMySQLSessionStore(t)
	ctx := context.Background()

	// expires_at is Unix seconds: a block that ended a second ago is over.
	require.NoError(t, s.BlockSession(ctx, "sid-exp", time.Now().Add(-time.Second)))

	blocked, err := s.IsSessionBlocked(ctx, "sid-exp")
	require.NoError(t, err)
	assert.False(t, blocked, "an expired session block must not apply")
}

func TestMySQLIntegration_BlockSession_ReblockExtends(t *testing.T) {
	s := setupMySQLSessionStore(t)
	ctx := context.Background()

	require.NoError(t, s.BlockSession(ctx, "sid-up", time.Now().Add(-time.Second)))
	blocked, err := s.IsSessionBlocked(ctx, "sid-up")
	require.NoError(t, err)
	require.False(t, blocked)

	require.NoError(t, s.BlockSession(ctx, "sid-up", time.Now().Add(time.Hour)))
	blocked, err = s.IsSessionBlocked(ctx, "sid-up")
	require.NoError(t, err)
	assert.True(t, blocked, "a second block must extend the first")

	var count int64
	require.NoError(t, s.db.Model(&SessionEntry{}).Where("entry_key = ? AND kind = ?", "sid-up", kindSessionBlock).Count(&count).Error)
	assert.Equal(t, int64(1), count, "the upsert keeps one row")
}

func TestMySQLIntegration_BlockSession_ReblockEarlierDoesNotShorten(t *testing.T) {
	s := setupMySQLSessionStore(t)
	ctx := context.Background()

	require.NoError(t, s.BlockSession(ctx, "sid-long", time.Now().Add(time.Hour)))
	require.NoError(t, s.BlockSession(ctx, "sid-long", time.Now().Add(-time.Second)))

	blocked, err := s.IsSessionBlocked(ctx, "sid-long")
	require.NoError(t, err)
	assert.True(t, blocked, "a re-block with an earlier until must not shorten the block")

	var row SessionEntry
	require.NoError(t, s.db.Where("entry_key = ? AND kind = ?", "sid-long", kindSessionBlock).First(&row).Error)
	assert.Greater(t, row.ExpiresAt, time.Now().Add(30*time.Minute).Unix())
}

func TestMySQLIntegration_BlockSession_Cleanup(t *testing.T) {
	s := setupMySQLSessionStore(t)
	ctx := context.Background()

	require.NoError(t, s.BlockSession(ctx, "sid-old", time.Now().Add(-time.Second)))
	require.NoError(t, s.BlockSession(ctx, "sid-live", time.Now().Add(time.Hour)))

	require.NoError(t, s.Cleanup(ctx))

	var rows []SessionEntry
	require.NoError(t, s.db.Where("kind = ?", kindSessionBlock).Find(&rows).Error)
	require.Len(t, rows, 1, "cleanup removes the expired session block only")
	assert.Equal(t, "sid-live", rows[0].EntryKey)

	blocked, err := s.IsSessionBlocked(ctx, "sid-live")
	require.NoError(t, err)
	assert.True(t, blocked)
}

// A session block, a token block and a user block with the same key are
// separate rows (the kind is part of the primary key) and do not affect each
// other.
func TestMySQLIntegration_BlockSession_NoCollisionWithOtherKinds(t *testing.T) {
	s := setupMySQLSessionStore(t)
	ctx := context.Background()
	const key = "same-key"

	require.NoError(t, s.BlockToken(ctx, key, time.Now().Add(time.Hour)))
	blocked, err := s.IsSessionBlocked(ctx, key)
	require.NoError(t, err)
	assert.False(t, blocked, "a token block must not block a session")

	require.NoError(t, s.BlockSession(ctx, key, time.Now().Add(time.Hour)))
	blocked, err = s.IsSessionBlocked(ctx, key)
	require.NoError(t, err)
	assert.True(t, blocked)
	blocked, err = s.IsTokenBlocked(ctx, key)
	require.NoError(t, err)
	assert.True(t, blocked, "the token block must survive the session block")

	// Removing the session block leaves the live token block alone (a
	// re-block with an earlier time would not shorten it).
	require.NoError(t, s.db.Where("entry_key = ? AND kind = ?", key, kindSessionBlock).Delete(&SessionEntry{}).Error)
	blocked, err = s.IsSessionBlocked(ctx, key)
	require.NoError(t, err)
	assert.False(t, blocked)
	blocked, err = s.IsTokenBlocked(ctx, key)
	require.NoError(t, err)
	assert.True(t, blocked, "removing the session block must not change the token block")

	require.NoError(t, s.BlockUser(ctx, key, time.Now().Add(time.Hour)))
	blocked, err = s.IsSessionBlocked(ctx, key)
	require.NoError(t, err)
	assert.False(t, blocked, "a user block must not block a session")
}

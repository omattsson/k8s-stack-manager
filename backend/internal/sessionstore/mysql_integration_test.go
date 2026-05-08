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

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
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

	blocked, err := s.IsUserBlocked(ctx, "user-1")
	require.NoError(t, err)
	assert.False(t, blocked)

	require.NoError(t, s.BlockUser(ctx, "user-1", time.Now().Add(time.Hour)))

	blocked, err = s.IsUserBlocked(ctx, "user-1")
	require.NoError(t, err)
	assert.True(t, blocked)
}

func TestMySQLIntegration_BlockUser_Expired(t *testing.T) {
	s := setupMySQLSessionStore(t)
	ctx := context.Background()

	require.NoError(t, s.BlockUser(ctx, "user-exp", time.Now().Add(-time.Second)))

	blocked, err := s.IsUserBlocked(ctx, "user-exp")
	require.NoError(t, err)
	assert.False(t, blocked, "expired user block should not appear blocked")
}

func TestMySQLIntegration_UnblockUser(t *testing.T) {
	s := setupMySQLSessionStore(t)
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

func TestMySQLIntegration_UnblockUser_NotBlocked(t *testing.T) {
	s := setupMySQLSessionStore(t)
	ctx := context.Background()

	err := s.UnblockUser(ctx, "never-blocked")
	assert.NoError(t, err, "unblocking a never-blocked user should not error")
}

package sessionstore

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// oldToken is an issued-at time well before any block created in a test.
var oldToken = time.Now().Add(-time.Hour)

func TestUserBlockApplies(t *testing.T) {
	t.Parallel()

	blockedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name      string
		blockedAt int64
		issuedAt  time.Time
		want      bool
	}{
		{"token issued before block", blockedAt.Unix(), blockedAt.Add(-time.Minute), true},
		{"token issued in the same second", blockedAt.Unix(), blockedAt.Add(500 * time.Millisecond), true},
		{"token issued after block", blockedAt.Unix(), blockedAt.Add(time.Second), false},
		{"legacy row without block time", 0, blockedAt.Add(time.Hour), true},
		{"token without iat", blockedAt.Unix(), time.Time{}, true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, userBlockApplies(tt.blockedAt, tt.issuedAt))
		})
	}
}

// newSQLiteBackedStore runs the MySQLStore code against in-memory SQLite.
// The queries used by the user-block methods are portable.
func newSQLiteBackedStore(t *testing.T) (*MySQLStore, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1) // one connection = one in-memory database
	require.NoError(t, db.AutoMigrate(&SessionEntry{}))
	s := NewMySQLStore(db)
	t.Cleanup(func() {
		s.Stop()
		_ = sqlDB.Close()
	})
	return s, db
}

// userBlockStores returns both store implementations for shared tests.
func userBlockStores(t *testing.T) map[string]SessionStore {
	t.Helper()
	mem := NewMemoryStore()
	t.Cleanup(mem.Stop)
	sqlStore, _ := newSQLiteBackedStore(t)
	return map[string]SessionStore{"memory": mem, "mysql(sqlite)": sqlStore}
}

func TestUserBlock_IssuedAt(t *testing.T) {
	t.Parallel()

	for name, store := range userBlockStores(t) {
		name, store := name, store
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()

			blocked, err := store.IsUserBlocked(ctx, "user-1", oldToken)
			require.NoError(t, err)
			assert.False(t, blocked, "no block entry")

			require.NoError(t, store.BlockUser(ctx, "user-1", time.Now().Add(time.Hour)))

			blocked, err = store.IsUserBlocked(ctx, "user-1", oldToken)
			require.NoError(t, err)
			assert.True(t, blocked, "token issued before the block is revoked")

			blocked, err = store.IsUserBlocked(ctx, "user-1", time.Now().Add(2*time.Second))
			require.NoError(t, err)
			assert.False(t, blocked, "token issued after the block stays valid")

			blocked, err = store.IsUserBlocked(ctx, "user-1", time.Time{})
			require.NoError(t, err)
			assert.True(t, blocked, "token without iat is revoked")

			blocked, err = store.IsUserBlocked(ctx, "user-2", oldToken)
			require.NoError(t, err)
			assert.False(t, blocked, "other users are not affected")

			require.NoError(t, store.UnblockUser(ctx, "user-1"))
			blocked, err = store.IsUserBlocked(ctx, "user-1", oldToken)
			require.NoError(t, err)
			assert.False(t, blocked, "unblocked")
		})
	}
}

func TestUserBlock_ExpiredEntryIgnored(t *testing.T) {
	t.Parallel()

	for name, store := range userBlockStores(t) {
		name, store := name, store
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			require.NoError(t, store.BlockUser(ctx, "user-exp", time.Now().Add(-time.Second)))
			blocked, err := store.IsUserBlocked(ctx, "user-exp", oldToken)
			require.NoError(t, err)
			assert.False(t, blocked)
		})
	}
}

func TestMySQLStore_UserBlock_StoresBlockTimeAndUpserts(t *testing.T) {
	t.Parallel()
	s, db := newSQLiteBackedStore(t)
	ctx := context.Background()

	// Legacy row (written before the block time was stored): blocks every token.
	require.NoError(t, db.Create(&SessionEntry{
		EntryKey: "user-legacy", Kind: kindUserBlock, ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}).Error)
	blocked, err := s.IsUserBlocked(ctx, "user-legacy", time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.True(t, blocked, "legacy row blocks even new tokens")

	// A new block on the same user upserts Data with the block time.
	before := time.Now().Unix()
	require.NoError(t, s.BlockUser(ctx, "user-legacy", time.Now().Add(time.Hour)))
	var entry SessionEntry
	require.NoError(t, db.Where("entry_key = ? AND kind = ?", "user-legacy", kindUserBlock).First(&entry).Error)
	assert.NotEmpty(t, entry.Data)

	blocked, err = s.IsUserBlocked(ctx, "user-legacy", time.Unix(before+2, 0))
	require.NoError(t, err)
	assert.False(t, blocked, "after upsert, tokens issued after the block pass")
}

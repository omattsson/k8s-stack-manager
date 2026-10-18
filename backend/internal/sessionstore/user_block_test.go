package sessionstore

import (
	"context"
	"strconv"
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

	// The block happens 300 ms into a second.
	blockedAt := time.Date(2026, 1, 2, 3, 4, 5, 300*int(time.Millisecond), time.UTC)
	blockSecond := blockedAt.Truncate(time.Second)
	tests := []struct {
		name      string
		blockedAt int64
		issuedAt  time.Time
		want      bool
	}{
		{"token issued before block", blockedAt.UnixMilli(), blockedAt.Add(-time.Minute), true},
		{"token issued 1 ms before block in the same second", blockedAt.UnixMilli(), blockedAt.Add(-time.Millisecond), true},
		{"token issued in the same millisecond", blockedAt.UnixMilli(), blockedAt, true},
		{"token issued 1 ms after block in the same second", blockedAt.UnixMilli(), blockedAt.Add(time.Millisecond), false},
		{"token issued after block", blockedAt.UnixMilli(), blockedAt.Add(time.Second), false},
		{"legacy token (whole second) in the block second", blockedAt.UnixMilli(), blockSecond, true},
		{"legacy token (whole second) after the block second", blockedAt.UnixMilli(), blockSecond.Add(time.Second), false},
		{"legacy row without block time", 0, blockedAt.Add(time.Hour), true},
		{"token without iat", blockedAt.UnixMilli(), time.Time{}, true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, userBlockApplies(tt.blockedAt, tt.issuedAt))
		})
	}
}

func TestParseBlockedAt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data string
		want int64
	}{
		{"milliseconds", "1767323045300", 1767323045300},
		{"legacy seconds give the last millisecond of the second", "1767323045", 1767323045999},
		{"empty", "", 0},
		{"malformed", "abc", 0},
		{"negative", "-5", 0},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, parseBlockedAt(tt.data))
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

// TestMySQLStore_UserBlock_DualWrite checks the two block entries: the
// user_block entry keeps Unix seconds (older versions read it with their
// second rule), and the user_block_ms entry gives this version millisecond
// precision.
func TestMySQLStore_UserBlock_DualWrite(t *testing.T) {
	t.Parallel()
	s, db := newSQLiteBackedStore(t)
	ctx := context.Background()

	before := time.Now()
	require.NoError(t, s.BlockUser(ctx, "user-ms", time.Now().Add(time.Hour)))
	after := time.Now()

	var sec, ms SessionEntry
	require.NoError(t, db.Where("entry_key = ? AND kind = ?", "user-ms", kindUserBlock).First(&sec).Error)
	require.NoError(t, db.Where("entry_key = ? AND kind = ?", "user-ms", kindUserBlockMs).First(&ms).Error)
	assert.Equal(t, sec.ExpiresAt, ms.ExpiresAt, "same TTL")

	// The old reader: strconv seconds and "issued at or before the second".
	oldSeconds, err := strconv.ParseInt(sec.Data, 10, 64)
	require.NoError(t, err)
	assert.Less(t, oldSeconds, int64(msTimestampMin), "the old entry stays in seconds")
	assert.GreaterOrEqual(t, oldSeconds, before.Unix())
	assert.LessOrEqual(t, oldSeconds, after.Unix())

	// The new reader: milliseconds.
	blockedAtMs, err := strconv.ParseInt(ms.Data, 10, 64)
	require.NoError(t, err)
	assert.Equal(t, oldSeconds, blockedAtMs/1000, "both entries describe the same block")
	blocked, err := s.IsUserBlocked(ctx, "user-ms", time.UnixMilli(blockedAtMs+1))
	require.NoError(t, err)
	assert.False(t, blocked, "a token issued 1 ms after the block stays valid")
	blocked, err = s.IsUserBlocked(ctx, "user-ms", time.UnixMilli(blockedAtMs))
	require.NoError(t, err)
	assert.True(t, blocked, "a token issued in the block millisecond is revoked")

	// Unblock removes both entries.
	require.NoError(t, s.UnblockUser(ctx, "user-ms"))
	var count int64
	require.NoError(t, db.Model(&SessionEntry{}).Where("entry_key = ?", "user-ms").Count(&count).Error)
	assert.Zero(t, count)
}

// TestMySQLStore_UserBlock_SecondsEntryOnly checks a block written by an
// older version (no user_block_ms entry): the second rule applies.
func TestMySQLStore_UserBlock_SecondsEntryOnly(t *testing.T) {
	t.Parallel()
	s, db := newSQLiteBackedStore(t)
	ctx := context.Background()

	blockSecond := time.Now().Unix()
	require.NoError(t, db.Create(&SessionEntry{
		EntryKey: "user-legacy-s", Kind: kindUserBlock, Data: strconv.FormatInt(blockSecond, 10),
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}).Error)
	blocked, err := s.IsUserBlocked(ctx, "user-legacy-s", time.UnixMilli(blockSecond*1000+999))
	require.NoError(t, err)
	assert.True(t, blocked, "a token issued in the block second is revoked")
	blocked, err = s.IsUserBlocked(ctx, "user-legacy-s", time.Unix(blockSecond+1, 0))
	require.NoError(t, err)
	assert.False(t, blocked, "a token issued after the block second stays valid")
}

func TestUserBlockTime(t *testing.T) {
	t.Parallel()

	const sec = int64(1767323045)
	msInSec := sec*1000 + 300
	tests := []struct {
		name    string
		secData string
		hasSec  bool
		msData  string
		hasMs   bool
		want    int64
	}{
		{name: "both entries of the same block use ms", secData: "1767323045", hasSec: true, msData: "1767323045300", hasMs: true, want: msInSec},
		{name: "ms entry missing uses the second rule", secData: "1767323045", hasSec: true, want: sec*1000 + 999},
		{name: "newer seconds entry (older version blocked again) wins", secData: "1767323050", hasSec: true, msData: "1767323045300", hasMs: true, want: (sec+5)*1000 + 999},
		{name: "seconds entry without time blocks every token", secData: "", hasSec: true, msData: "1767323045300", hasMs: true, want: 0},
		{name: "only the ms entry", msData: "1767323045300", hasMs: true, want: msInSec},
		{name: "malformed ms entry falls back to seconds", secData: "1767323045", hasSec: true, msData: "x", hasMs: true, want: sec*1000 + 999},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, userBlockTime(tt.secData, tt.hasSec, tt.msData, tt.hasMs))
		})
	}
}

// TestMemoryStore_UserBlock_MsEntryMissing checks the memory store fallback
// to the second rule when no milliseconds time is stored.
func TestMemoryStore_UserBlock_MsEntryMissing(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore()
	t.Cleanup(s.Stop)
	blockSecond := time.Now().Unix()
	s.mu.Lock()
	s.userBlocks["u1"] = memBlockEntry{expiresAt: time.Now().Add(time.Hour), blockedAtSec: blockSecond}
	s.mu.Unlock()

	blocked, err := s.IsUserBlocked(context.Background(), "u1", time.UnixMilli(blockSecond*1000+500))
	require.NoError(t, err)
	assert.True(t, blocked, "same second is revoked with the second rule")
	blocked, err = s.IsUserBlocked(context.Background(), "u1", time.Unix(blockSecond+1, 0))
	require.NoError(t, err)
	assert.False(t, blocked)
}

//go:build integration

package database

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestDatabaseMySQL_InstanceFollowers checks the MySQL SQL of the follower
// repository: ON CONFLICT DO NOTHING (ON DUPLICATE KEY UPDATE), the SELECT
// ... FOR SHARE of Follow, the SUM(CASE ...) of FollowState and the
// follower delete of an instance delete.
func TestDatabaseMySQL_InstanceFollowers(t *testing.T) {
	db := setupMySQLTestDB(t)
	ctx := context.Background()
	instances := NewGORMStackInstanceRepository(db)
	repo := NewGORMInstanceFollowerRepository(db)
	for _, id := range []string{"fi-1", "fi-2"} {
		require.NoError(t, instances.Create(&models.StackInstance{ID: id, Name: id, Namespace: "stack-" + id, OwnerID: "owner", StackDefinitionID: "d1", Status: models.StackStatusDraft}))
	}

	require.NoError(t, repo.Follow(ctx, "u1", "fi-1"))
	require.NoError(t, repo.Follow(ctx, "u1", "fi-1"), "a repeated follow is not an error")
	require.NoError(t, repo.Follow(ctx, "u2", "fi-1"))
	require.NoError(t, repo.Follow(ctx, "u1", "fi-2"))
	assert.ErrorIs(t, repo.Follow(ctx, "u1", "missing"), dberrors.ErrNotFound)

	following, count, err := repo.FollowState(ctx, "u1", "fi-1")
	require.NoError(t, err)
	assert.True(t, following)
	assert.Equal(t, int64(2), count)
	following, count, err = repo.FollowState(ctx, "u9", "fi-1")
	require.NoError(t, err)
	assert.False(t, following)
	assert.Equal(t, int64(2), count)
	following, count, err = repo.FollowState(ctx, "u1", "no-followers")
	require.NoError(t, err)
	assert.False(t, following)
	assert.Equal(t, int64(0), count)

	ids, err := repo.ListUserIDsByInstance(ctx, "fi-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"u1", "u2"}, ids)

	require.NoError(t, repo.Unfollow(ctx, "u2", "fi-1"))
	require.NoError(t, repo.Unfollow(ctx, "u2", "fi-1"), "a repeated unfollow is not an error")

	// The instance delete removes the followers in the same transaction.
	inst, err := instances.FindByID("fi-1")
	require.NoError(t, err)
	require.NoError(t, NewGORMTxRunner(db).RunInTx(func(repos TxRepos) error {
		return DeleteInstanceRecord(repos, inst)
	}))
	ids, err = repo.ListUserIDsByInstance(ctx, "fi-1")
	require.NoError(t, err)
	assert.Empty(t, ids)
	ids, err = repo.ListUserIDsByInstance(ctx, "fi-2")
	require.NoError(t, err)
	assert.Equal(t, []string{"u1"}, ids)
}

// TestDatabaseMySQL_PreferenceUpsert checks the preference upsert from a
// column map on MySQL: enabled=false is stored on insert and on update, and
// DisabledUserIDs reads it.
func TestDatabaseMySQL_PreferenceUpsert(t *testing.T) {
	db := setupMySQLTestDB(t)
	ctx := context.Background()
	repo := NewGORMNotificationRepository(db)

	for i, enabled := range []bool{false, true, false} {
		require.NoError(t, repo.UpdatePreference(ctx, &models.NotificationPreference{
			ID: fmt.Sprintf("pref-%d", i), UserID: "u1", EventType: "deployment.success", Enabled: enabled,
		}))
		prefs, err := repo.GetPreferences(ctx, "u1")
		require.NoError(t, err)
		require.Len(t, prefs, 1, "the upsert keeps one row per user and event type")
		assert.Equal(t, enabled, prefs[0].Enabled)
		assert.Equal(t, "in_app", prefs[0].Channel)
		disabled, err := repo.DisabledUserIDs(ctx, "deployment.success", []string{"u1", "u2"})
		require.NoError(t, err)
		assert.Equal(t, !enabled, disabled["u1"])
		assert.False(t, disabled["u2"])
	}
}

// TestDatabaseMySQL_MigrationsChannelFiltersAndFollowers runs all versioned
// migrations on an empty MySQL database (migration 40 creates the channel
// table with SQL, 52 adds filters, 53 creates instance_followers), then
// checks the channel repository: a disabled channel stays disabled and the
// filters round trip.
func TestDatabaseMySQL_MigrationsChannelFiltersAndFollowers(t *testing.T) {
	base := setupMySQLTestDB(t)
	const schemaName = "app_migtest_followers"
	require.NoError(t, base.Exec("DROP DATABASE IF EXISTS "+schemaName).Error)
	require.NoError(t, base.Exec("CREATE DATABASE "+schemaName).Error)
	t.Cleanup(func() { base.Exec("DROP DATABASE IF EXISTS " + schemaName) })

	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		dsn = "root:rootpassword@tcp(localhost:3306)/app?charset=utf8mb4&parseTime=True&loc=Local"
	}
	slash := strings.LastIndex(dsn, "/")
	query := strings.Index(dsn[slash:], "?")
	require.True(t, slash >= 0 && query >= 0, "DSN needs /dbname?params")
	migDSN := dsn[:slash+1] + schemaName + dsn[slash+query:]
	gdb, err := gorm.Open(mysql.Open(migDSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, gdb.AutoMigrate(&models.Cluster{}), "migration 5 alters clusters")
	db := &Database{DB: gdb}
	require.NoError(t, db.AutoMigrate())

	m := gdb.Migrator()
	assert.True(t, m.HasColumn(&models.NotificationChannel{}, "Filters"))
	assert.True(t, m.HasTable(&models.InstanceFollower{}))
	assert.True(t, m.HasIndex(&models.InstanceFollower{}, "idx_instance_followers_instance"))

	ctx := context.Background()
	repo := NewGORMNotificationChannelRepository(gdb, "")
	filters := models.NotificationChannelFilters{InstanceNamePatterns: []string{"team-a-*"}, OwnerIDs: []string{"u1"}}
	require.NoError(t, repo.CreateChannel(ctx, &models.NotificationChannel{ID: "ch-off", Name: "off", WebhookURL: "https://example.com/off", Enabled: false, Filters: filters}))
	require.NoError(t, repo.CreateChannel(ctx, &models.NotificationChannel{ID: "ch-on", Name: "on", WebhookURL: "https://example.com/on", Enabled: true}))

	off, err := repo.GetChannel(ctx, "ch-off")
	require.NoError(t, err)
	assert.False(t, off.Enabled, "a channel created disabled stays disabled")
	assert.Equal(t, filters, off.Filters)
	on, err := repo.GetChannel(ctx, "ch-on")
	require.NoError(t, err)
	assert.True(t, on.Enabled)
	assert.True(t, on.Filters.IsEmpty(), "no filters is NULL")

	// Down and Up of the new migrations on MySQL.
	for _, mig := range []func() error{
		func() error { return instanceFollowersMigration().Down(gdb) },
		func() error { return notificationChannelFiltersMigration().Down(gdb) },
	} {
		require.NoError(t, mig())
	}
	assert.False(t, m.HasTable(&models.InstanceFollower{}))
	assert.False(t, m.HasColumn(&models.NotificationChannel{}, "Filters"))
	require.NoError(t, notificationChannelFiltersMigration().Up(gdb))
	require.NoError(t, instanceFollowersMigration().Up(gdb))
	assert.True(t, m.HasColumn(&models.NotificationChannel{}, "Filters"))
	assert.True(t, m.HasTable(&models.InstanceFollower{}))
}

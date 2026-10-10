package database

import (
	"context"
	"testing"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// seedFollowInstances creates draft instances with the given IDs, so that
// Follow finds them.
func seedFollowInstances(t *testing.T, db *gorm.DB, ids ...string) {
	t.Helper()
	repo := NewGORMStackInstanceRepository(db)
	for _, id := range ids {
		require.NoError(t, repo.Create(&models.StackInstance{ID: id, Name: id, Namespace: "stack-" + id, OwnerID: "owner", StackDefinitionID: "d1", Status: models.StackStatusDraft}))
	}
}

func TestGORMInstanceFollowerRepository_FollowUnknownInstance(t *testing.T) {
	t.Parallel()
	db := setupTestDBWithAllTables(t)
	r := NewGORMInstanceFollowerRepository(db)
	err := r.Follow(context.Background(), "u1", "missing")
	require.Error(t, err)
	assert.ErrorIs(t, err, dberrors.ErrNotFound)
	ids, err := r.ListUserIDsByInstance(context.Background(), "missing")
	require.NoError(t, err)
	assert.Empty(t, ids, "no orphan row")
}

func TestGORMInstanceFollowerRepository(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tests := []struct {
		name          string
		run           func(t *testing.T, r *GORMInstanceFollowerRepository)
		user          string
		wantFollowing bool
		wantCount     int64
		wantFollowers []string
	}{
		{
			name: "follow is idempotent",
			run: func(t *testing.T, r *GORMInstanceFollowerRepository) {
				require.NoError(t, r.Follow(ctx, "u1", "i1"))
				require.NoError(t, r.Follow(ctx, "u1", "i1"))
			},
			user: "u1", wantFollowing: true, wantCount: 1, wantFollowers: []string{"u1"},
		},
		{
			name: "count and state for another user",
			run: func(t *testing.T, r *GORMInstanceFollowerRepository) {
				require.NoError(t, r.Follow(ctx, "u1", "i1"))
				require.NoError(t, r.Follow(ctx, "u2", "i1"))
				require.NoError(t, r.Follow(ctx, "u3", "i2"))
			},
			user: "u9", wantFollowing: false, wantCount: 2, wantFollowers: []string{"u1", "u2"},
		},
		{
			name: "unfollow is idempotent",
			run: func(t *testing.T, r *GORMInstanceFollowerRepository) {
				require.NoError(t, r.Follow(ctx, "u1", "i1"))
				require.NoError(t, r.Unfollow(ctx, "u1", "i1"))
				require.NoError(t, r.Unfollow(ctx, "u1", "i1"))
			},
			user: "u1", wantFollowing: false, wantCount: 0, wantFollowers: []string{},
		},
		{
			name: "delete by instance",
			run: func(t *testing.T, r *GORMInstanceFollowerRepository) {
				require.NoError(t, r.Follow(ctx, "u1", "i1"))
				require.NoError(t, r.Follow(ctx, "u1", "i2"))
				require.NoError(t, r.DeleteByInstance(ctx, "i1"))
				ids, err := r.ListUserIDsByInstance(ctx, "i2")
				require.NoError(t, err)
				assert.Equal(t, []string{"u1"}, ids, "other instances keep their followers")
			},
			user: "u1", wantFollowing: false, wantCount: 0, wantFollowers: []string{},
		},
		{
			name: "delete by user",
			run: func(t *testing.T, r *GORMInstanceFollowerRepository) {
				require.NoError(t, r.Follow(ctx, "u1", "i1"))
				require.NoError(t, r.Follow(ctx, "u2", "i1"))
				require.NoError(t, r.DeleteByUser(ctx, "u1"))
			},
			user: "u1", wantFollowing: false, wantCount: 1, wantFollowers: []string{"u2"},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			db := setupTestDBWithAllTables(t)
			seedFollowInstances(t, db, "i1", "i2")
			r := NewGORMInstanceFollowerRepository(db)
			tt.run(t, r)

			following, count, err := r.FollowState(ctx, tt.user, "i1")
			require.NoError(t, err)
			assert.Equal(t, tt.wantFollowing, following)
			assert.Equal(t, tt.wantCount, count)
			ids, err := r.ListUserIDsByInstance(ctx, "i1")
			require.NoError(t, err)
			if len(tt.wantFollowers) == 0 {
				assert.Empty(t, ids)
			} else {
				assert.Equal(t, tt.wantFollowers, ids)
			}
		})
	}
}

// TestGORMUserRepository_DeleteRemovesFollows checks that both user delete
// paths remove the follows of the user and keep those of other users.
func TestGORMUserRepository_DeleteRemovesFollows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tests := []struct {
		name   string
		delete func(r *GORMUserRepository) error
	}{
		{name: "Delete", delete: func(r *GORMUserRepository) error { return r.Delete("u1") }},
		{name: "DeleteGuarded", delete: func(r *GORMUserRepository) error { return r.DeleteGuarded("admin", "u1") }},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			db := setupTestDBWithAllTables(t)
			users := NewGORMUserRepository(db)
			followers := NewGORMInstanceFollowerRepository(db)
			seedFollowInstances(t, db, "i1")
			require.NoError(t, users.Create(&models.User{ID: "admin", Username: "admin", PasswordHash: "x", Role: "admin"}))
			require.NoError(t, users.Create(&models.User{ID: "u1", Username: "u1", PasswordHash: "x", Role: "user"}))
			require.NoError(t, followers.Follow(ctx, "u1", "i1"))
			require.NoError(t, followers.Follow(ctx, "u2", "i1"))

			require.NoError(t, tt.delete(users))

			ids, err := followers.ListUserIDsByInstance(ctx, "i1")
			require.NoError(t, err)
			assert.Equal(t, []string{"u2"}, ids)
		})
	}
}

func TestGORMNotificationRepository_DisabledUserIDs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := setupNotificationRepo(t)
	for _, p := range []models.NotificationPreference{
		{ID: "p1", UserID: "u1", EventType: "deployment.success", Enabled: false, Channel: "in_app"},
		{ID: "p2", UserID: "u2", EventType: "deployment.success", Enabled: true, Channel: "in_app"},
		{ID: "p3", UserID: "u3", EventType: "deployment.error", Enabled: false, Channel: "in_app"},
		{ID: "p4", UserID: "u4", EventType: "deployment.success", Enabled: false, Channel: "in_app"},
	} {
		p := p
		require.NoError(t, repo.UpdatePreference(ctx, &p))
	}

	tests := []struct {
		name  string
		event string
		users []string
		want  map[string]bool
	}{
		{name: "switched off for the event", event: "deployment.success", users: []string{"u1", "u2", "u3", "u5"}, want: map[string]bool{"u1": true}},
		{name: "only the given users", event: "deployment.success", users: []string{"u2"}, want: map[string]bool{}},
		{name: "no users", event: "deployment.success", users: nil, want: map[string]bool{}},
		{name: "other event type", event: "deployment.error", users: []string{"u1", "u3"}, want: map[string]bool{"u3": true}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := repo.DisabledUserIDs(ctx, tt.event, tt.users)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestMigration_NotificationFiltersAndFollowers checks migrations 52 and 53:
// the filters column and the followers table exist after the migrations,
// and Up and Down can run again.
func TestMigration_NotificationFiltersAndFollowers(t *testing.T) {
	t.Parallel()
	db := setupMigrationTestDB(t)
	require.NoError(t, db.AutoMigrate())
	m := db.DB.Migrator()

	assert.True(t, m.HasColumn(&models.NotificationChannel{}, "Filters"))
	assert.True(t, m.HasTable(&models.InstanceFollower{}))
	assert.True(t, m.HasIndex(&models.InstanceFollower{}, "idx_instance_followers_instance"))

	filters := notificationChannelFiltersMigration()
	require.NoError(t, filters.Up(db.DB), "Up on an existing column")

	// The SQLite migrator of GORM cannot drop a column of the hand-written
	// CREATE TABLE of migration 40. Check Down and Up on a table that GORM
	// created (MySQL drops the column with ALTER TABLE).
	gormDB := setupMigrationTestDB(t)
	require.NoError(t, gormDB.DB.AutoMigrate(&models.NotificationChannel{}))
	gm := gormDB.DB.Migrator()
	require.NoError(t, filters.Down(gormDB.DB))
	assert.False(t, gm.HasColumn(&models.NotificationChannel{}, "Filters"))
	require.NoError(t, filters.Down(gormDB.DB), "Down without the column")
	require.NoError(t, filters.Up(gormDB.DB))
	assert.True(t, gm.HasColumn(&models.NotificationChannel{}, "Filters"))

	followers := instanceFollowersMigration()
	require.NoError(t, followers.Up(db.DB), "Up on an existing table")
	require.NoError(t, followers.Down(db.DB))
	assert.False(t, m.HasTable(&models.InstanceFollower{}))
	require.NoError(t, followers.Down(db.DB), "Down without the table")
	require.NoError(t, followers.Up(db.DB))
	assert.True(t, m.HasTable(&models.InstanceFollower{}))
}

// TestGORMNotificationChannelRepository_Filters checks that create, update
// and get store and read the channel filters, and that a channel without
// filters reads empty filters (NULL column).
func TestGORMNotificationChannelRepository_Filters(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := setupMigrationTestDB(t)
	require.NoError(t, db.AutoMigrate())
	repo := NewGORMNotificationChannelRepository(db.DB, "")

	filtered := &models.NotificationChannel{ID: "ch1", Name: "team-a", WebhookURL: "https://example.com/a", Enabled: true,
		Filters: models.NotificationChannelFilters{InstanceNamePatterns: []string{"team-a-*"}, ClusterIDs: []string{"c1"}}}
	plain := &models.NotificationChannel{ID: "ch2", Name: "all", WebhookURL: "https://example.com/b", Enabled: true}
	require.NoError(t, repo.CreateChannel(ctx, filtered))
	require.NoError(t, repo.CreateChannel(ctx, plain))
	require.NoError(t, repo.SetSubscriptions(ctx, "ch1", []string{"deployment.success"}))

	got, err := repo.GetChannel(ctx, "ch1")
	require.NoError(t, err)
	assert.Equal(t, filtered.Filters, got.Filters)

	got, err = repo.GetChannel(ctx, "ch2")
	require.NoError(t, err)
	assert.True(t, got.Filters.IsEmpty())

	byEvent, err := repo.FindChannelsByEvent(ctx, "deployment.success")
	require.NoError(t, err)
	require.Len(t, byEvent, 1)
	assert.Equal(t, filtered.Filters, byEvent[0].Filters, "the dispatcher reads the filters")

	filtered.Filters = models.NotificationChannelFilters{OwnerIDs: []string{"u1"}}
	require.NoError(t, repo.UpdateChannel(ctx, filtered, false))
	got, err = repo.GetChannel(ctx, "ch1")
	require.NoError(t, err)
	assert.Equal(t, []string{"u1"}, got.Filters.OwnerIDs)
	assert.Empty(t, got.Filters.InstanceNamePatterns)

	filtered.Filters = models.NotificationChannelFilters{}
	require.NoError(t, repo.UpdateChannel(ctx, filtered, false))
	got, err = repo.GetChannel(ctx, "ch1")
	require.NoError(t, err)
	assert.True(t, got.Filters.IsEmpty(), "update with empty filters removes them")
}

// TestGORMNotificationRepository_UpdatePreference_SwitchOff checks that a
// preference can be switched off on insert and on update, and on again.
// The notifier reads these values (DisabledUserIDs).
func TestGORMNotificationRepository_UpdatePreference_SwitchOff(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := setupNotificationRepo(t)

	steps := []struct {
		id      string
		enabled bool
	}{{"p1", false}, {"p2", true}, {"p3", false}}
	for _, step := range steps {
		require.NoError(t, repo.UpdatePreference(ctx, &models.NotificationPreference{ID: step.id, UserID: "u1", EventType: "deployment.success", Enabled: step.enabled}))
		prefs, err := repo.GetPreferences(ctx, "u1")
		require.NoError(t, err)
		require.Len(t, prefs, 1)
		assert.Equal(t, step.enabled, prefs[0].Enabled)
		assert.Equal(t, "in_app", prefs[0].Channel)
		disabled, err := repo.DisabledUserIDs(ctx, "deployment.success", []string{"u1"})
		require.NoError(t, err)
		assert.Equal(t, !step.enabled, disabled["u1"])
	}
}

// TestGORMNotificationChannelRepository_CreateDisabled checks that a channel
// created with enabled=false is stored disabled (no GORM default replaces
// the zero value) and that the dispatcher does not find it.
func TestGORMNotificationChannelRepository_CreateDisabled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := setupMigrationTestDB(t)
	require.NoError(t, db.AutoMigrate())
	repo := NewGORMNotificationChannelRepository(db.DB, "")

	tests := []struct {
		id      string
		enabled bool
	}{{"off", false}, {"on", true}}
	for _, tt := range tests {
		require.NoError(t, repo.CreateChannel(ctx, &models.NotificationChannel{ID: tt.id, Name: tt.id, WebhookURL: "https://example.com/" + tt.id, Enabled: tt.enabled}))
		require.NoError(t, repo.SetSubscriptions(ctx, tt.id, []string{"deployment.success"}))
		got, err := repo.GetChannel(ctx, tt.id)
		require.NoError(t, err)
		assert.Equal(t, tt.enabled, got.Enabled, tt.id)
	}
	found, err := repo.FindChannelsByEvent(ctx, "deployment.success")
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, "on", found[0].ID)
}

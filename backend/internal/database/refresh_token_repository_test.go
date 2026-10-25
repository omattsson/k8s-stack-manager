package database

import (
	"context"
	"fmt"
	"testing"
	"time"

	"backend/internal/database/schema"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupRefreshTokenRepo(t *testing.T) *GORMRefreshTokenRepository {
	t.Helper()
	db := setupTestDBWithAllTables(t)
	require.NoError(t, db.AutoMigrate(&models.RefreshToken{}))
	return NewGORMRefreshTokenRepository(db)
}

// seedRefreshToken stores a token with sensible defaults; mutate adjusts it.
func seedRefreshToken(t *testing.T, repo *GORMRefreshTokenRepository, id, familyID string, mutate func(*models.RefreshToken)) {
	t.Helper()
	now := time.Now().UTC()
	rt := &models.RefreshToken{
		ID:               id,
		UserID:           "u1",
		FamilyID:         familyID,
		TokenHash:        "hash-" + id,
		ExpiresAt:        now.Add(time.Hour),
		LastActivity:     now.Add(-10 * time.Minute),
		CreatedAt:        now.Add(-10 * time.Minute),
		SessionStartedAt: now.Add(-10 * time.Minute),
	}
	if mutate != nil {
		mutate(rt)
	}
	require.NoError(t, repo.Create(context.Background(), rt))
}

func loadRefreshToken(t *testing.T, repo *GORMRefreshTokenRepository, id string) models.RefreshToken {
	t.Helper()
	var rt models.RefreshToken
	require.NoError(t, repo.db.Where("id = ?", id).First(&rt).Error)
	return rt
}

func TestGORMRefreshTokenRepository_MarkRotatedIfActive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		revoked      bool
		wantAffected int64
		wantRotated  bool
	}{
		{name: "active token is revoked and stamped", wantAffected: 1, wantRotated: true},
		{name: "revoked token is left alone", revoked: true, wantAffected: 0, wantRotated: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupRefreshTokenRepo(t)
			seedRefreshToken(t, repo, "rt-1", "rt-1", func(rt *models.RefreshToken) { rt.Revoked = tt.revoked })

			at := time.Now().UTC().Truncate(time.Second)
			affected, err := repo.MarkRotatedIfActive(context.Background(), "rt-1", at)
			require.NoError(t, err)
			assert.Equal(t, tt.wantAffected, affected)

			got := loadRefreshToken(t, repo, "rt-1")
			assert.True(t, got.Revoked)
			if tt.wantRotated {
				require.NotNil(t, got.RotatedAt)
				assert.WithinDuration(t, at, *got.RotatedAt, time.Second)
			} else {
				assert.Nil(t, got.RotatedAt, "a revoked token must not get rotated_at")
			}

			// A second consumption never succeeds.
			affected, err = repo.MarkRotatedIfActive(context.Background(), "rt-1", at)
			require.NoError(t, err)
			assert.Equal(t, int64(0), affected)
		})
	}
}

func TestGORMRefreshTokenRepository_RevokeFamily(t *testing.T) {
	t.Parallel()
	repo := setupRefreshTokenRepo(t)

	// Legacy first token (empty family) and its successor in the family.
	seedRefreshToken(t, repo, "legacy-1", "", nil)
	seedRefreshToken(t, repo, "succ-1", "legacy-1", nil)
	// Another session of the same user.
	seedRefreshToken(t, repo, "other-1", "other-1", nil)

	require.NoError(t, repo.RevokeFamily(context.Background(), "legacy-1"))
	// RevokeFamily must not set rotated_at (only a rotation does).
	for _, id := range []string{"legacy-1", "succ-1"} {
		got := loadRefreshToken(t, repo, id)
		assert.True(t, got.Revoked, "%s should be revoked", id)
		assert.Nil(t, got.RotatedAt, "%s must not get rotated_at", id)
	}
	assert.False(t, loadRefreshToken(t, repo, "other-1").Revoked, "other session must stay active")
}

func TestGORMRefreshTokenRepository_RevokeFamilyForUser(t *testing.T) {
	t.Parallel()
	repo := setupRefreshTokenRepo(t)

	seedRefreshToken(t, repo, "legacy-1", "", nil)
	seedRefreshToken(t, repo, "succ-1", "legacy-1", nil)
	seedRefreshToken(t, repo, "foreign-1", "foreign-1", func(rt *models.RefreshToken) { rt.UserID = "u2" })

	// A family of another user is left alone.
	require.NoError(t, repo.RevokeFamilyForUser(context.Background(), "foreign-1", "u1"))
	assert.False(t, loadRefreshToken(t, repo, "foreign-1").Revoked)

	// A legacy family (family = token ID) of the owner is revoked.
	require.NoError(t, repo.RevokeFamilyForUser(context.Background(), "legacy-1", "u1"))
	for _, id := range []string{"legacy-1", "succ-1"} {
		got := loadRefreshToken(t, repo, id)
		assert.True(t, got.Revoked, "%s should be revoked", id)
		assert.Nil(t, got.RotatedAt)
	}

	require.NoError(t, repo.RevokeFamilyForUser(context.Background(), "foreign-1", "u2"))
	assert.True(t, loadRefreshToken(t, repo, "foreign-1").Revoked)
}

func TestGORMRefreshTokenRepository_ListRecentFamiliesForUser(t *testing.T) {
	t.Parallel()
	repo := setupRefreshTokenRepo(t)
	now := time.Now().UTC()
	since := now.Add(-10 * time.Minute)

	// fam-a: three rows, one family; the revoked row is included.
	seedRefreshToken(t, repo, "fam-a", "fam-a", func(rt *models.RefreshToken) {
		rt.Revoked = true
		rt.CreatedAt = now.Add(-50 * time.Minute)
	})
	seedRefreshToken(t, repo, "fam-a-2", "fam-a", func(rt *models.RefreshToken) { rt.CreatedAt = now.Add(-40 * time.Minute) })
	seedRefreshToken(t, repo, "fam-a-3", "fam-a", func(rt *models.RefreshToken) { rt.CreatedAt = now.Add(-30 * time.Minute) })
	// fam-b: revoked (idle timeout, token cap), expiry still ahead: included.
	seedRefreshToken(t, repo, "fam-b", "fam-b", func(rt *models.RefreshToken) {
		rt.Revoked = true
		rt.CreatedAt = now.Add(-20 * time.Minute)
	})
	// Legacy row: its own family; the newest family comes first.
	seedRefreshToken(t, repo, "legacy-1", "", func(rt *models.RefreshToken) { rt.CreatedAt = now.Add(-5 * time.Minute) })
	// Expired before since: excluded. Expired after since: included.
	seedRefreshToken(t, repo, "old-1", "old-1", func(rt *models.RefreshToken) { rt.ExpiresAt = since.Add(-time.Minute) })
	seedRefreshToken(t, repo, "just-expired", "just-expired", func(rt *models.RefreshToken) {
		rt.ExpiresAt = since.Add(time.Minute)
		rt.CreatedAt = now.Add(-60 * time.Minute)
	})
	seedRefreshToken(t, repo, "other-user", "other-user", func(rt *models.RefreshToken) { rt.UserID = "u2" })

	got, err := repo.ListRecentFamiliesForUser(context.Background(), "u1", since)
	require.NoError(t, err)
	assert.Equal(t, []string{"legacy-1", "fam-b", "fam-a", "just-expired"}, got, "newest family first")

	got, err = repo.ListRecentFamiliesForUser(context.Background(), "nobody", since)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestGORMRefreshTokenRepository_ListRecentFamiliesForUser_Limit(t *testing.T) {
	t.Parallel()
	repo := setupRefreshTokenRepo(t)
	now := time.Now().UTC()
	rows := make([]models.RefreshToken, 0, models.MaxRecentFamilies+5)
	for i := 0; i < models.MaxRecentFamilies+5; i++ {
		id := fmt.Sprintf("f-%04d", i)
		rows = append(rows, models.RefreshToken{
			ID: id, UserID: "u1", FamilyID: id, TokenHash: "hash-" + id,
			ExpiresAt: now.Add(time.Hour), LastActivity: now, SessionStartedAt: now,
			CreatedAt: now.Add(time.Duration(i) * time.Second),
		})
	}
	require.NoError(t, repo.db.CreateInBatches(&rows, 200).Error)

	got, err := repo.ListRecentFamiliesForUser(context.Background(), "u1", now)
	require.NoError(t, err)
	require.Len(t, got, models.MaxRecentFamilies)
	assert.Equal(t, fmt.Sprintf("f-%04d", models.MaxRecentFamilies+4), got[0], "the newest families are kept")
}

func TestGORMRefreshTokenRepository_CountActiveInFamily(t *testing.T) {
	t.Parallel()
	repo := setupRefreshTokenRepo(t)

	seedRefreshToken(t, repo, "fam-a", "fam-a", func(rt *models.RefreshToken) { rt.Revoked = true })
	seedRefreshToken(t, repo, "fam-a-2", "fam-a", nil)
	seedRefreshToken(t, repo, "fam-a-3", "fam-a", func(rt *models.RefreshToken) { rt.ExpiresAt = time.Now().UTC().Add(-time.Minute) })
	seedRefreshToken(t, repo, "fam-b", "fam-b", nil)

	tests := []struct {
		family string
		want   int64
	}{
		{family: "fam-a", want: 1},
		{family: "fam-b", want: 1},
		{family: "missing", want: 0},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.family, func(t *testing.T) {
			t.Parallel()
			got, err := repo.CountActiveInFamily(context.Background(), tt.family)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGORMRefreshTokenRepository_TouchFamily(t *testing.T) {
	t.Parallel()
	repo := setupRefreshTokenRepo(t)
	now := time.Now().UTC().Truncate(time.Second)

	seedRefreshToken(t, repo, "active", "fam", nil)
	seedRefreshToken(t, repo, "revoked", "fam", func(rt *models.RefreshToken) { rt.Revoked = true })
	seedRefreshToken(t, repo, "other", "fam-other", nil)
	seedRefreshToken(t, repo, "newer", "fam-newer", func(rt *models.RefreshToken) { rt.LastActivity = now.Add(time.Minute) })

	require.NoError(t, repo.TouchFamily(context.Background(), "fam", now))
	require.NoError(t, repo.TouchFamily(context.Background(), "fam-newer", now))

	assert.WithinDuration(t, now, loadRefreshToken(t, repo, "active").LastActivity, time.Second)
	assert.True(t, loadRefreshToken(t, repo, "revoked").LastActivity.Before(now.Add(-time.Minute)), "revoked token must not be touched")
	assert.True(t, loadRefreshToken(t, repo, "other").LastActivity.Before(now.Add(-time.Minute)), "other family must not be touched")
	assert.WithinDuration(t, now.Add(time.Minute), loadRefreshToken(t, repo, "newer").LastActivity, time.Second,
		"TouchFamily must never move last_activity backwards")
}

// TestMigration_RefreshTokenSessionFamily simulates a database from before
// migration 41: the session columns are missing and a legacy token exists.
// The migration adds the columns and backfills family_id and
// session_started_at.
func TestMigration_RefreshTokenSessionFamily(t *testing.T) {
	t.Parallel()
	db := setupMigrationTestDB(t)
	require.NoError(t, db.AutoMigrate())

	m := db.DB.Migrator()
	require.NoError(t, m.DropIndex(&models.RefreshToken{}, "FamilyID"))
	for _, field := range []string{"RotatedAt", "SessionStartedAt", "FamilyID"} {
		require.NoError(t, m.DropColumn(&models.RefreshToken{}, field))
	}
	require.NoError(t, db.DB.Unscoped().Where("version = ?", "20261008000041").Delete(&schema.SchemaVersion{}).Error)

	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	require.NoError(t, db.DB.Exec(
		"INSERT INTO refresh_tokens (id, user_id, token_hash, expires_at, last_activity, created_at, revoked) VALUES (?, ?, ?, ?, ?, ?, ?)",
		"legacy-1", "u1", "hash-legacy", created.Add(time.Hour), created, created, false,
	).Error)

	require.NoError(t, db.AutoMigrate())

	for _, field := range []string{"FamilyID", "SessionStartedAt", "RotatedAt"} {
		assert.True(t, m.HasColumn(&models.RefreshToken{}, field), "column %s should exist", field)
	}
	assert.True(t, m.HasIndex(&models.RefreshToken{}, "FamilyID"))

	var got models.RefreshToken
	require.NoError(t, db.DB.Where("id = ?", "legacy-1").First(&got).Error)
	assert.Equal(t, "legacy-1", got.FamilyID, "legacy token becomes its own family")
	assert.True(t, got.SessionStartedAt.Equal(created), "session start is backfilled from created_at")
	assert.Nil(t, got.RotatedAt)

	// Down removes the columns and the index again; Up after Down restores them.
	mig := refreshTokenSessionFamilyMigration()
	require.NoError(t, mig.Down(db.DB))
	for _, field := range []string{"FamilyID", "SessionStartedAt", "RotatedAt"} {
		assert.False(t, m.HasColumn(&models.RefreshToken{}, field), "column %s should be dropped", field)
	}
	assert.False(t, m.HasIndex(&models.RefreshToken{}, "FamilyID"))
	require.NoError(t, mig.Down(db.DB), "Down must be idempotent")
	require.NoError(t, mig.Up(db.DB))
	assert.True(t, m.HasColumn(&models.RefreshToken{}, "FamilyID"))
	assert.True(t, m.HasIndex(&models.RefreshToken{}, "FamilyID"))
}

func TestGORMRefreshTokenRepository_RevokeOldestForUser(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		excludeID   string
		wantRevoked []string
		keep        int
		wantCount   int64
	}{
		{name: "keeps the newest two", keep: 2, wantRevoked: []string{"t0", "t1"}, wantCount: 2},
		{name: "excluded token is neither kept nor revoked", keep: 2, excludeID: "t3", wantRevoked: []string{"t0"}, wantCount: 1},
		{name: "keep zero revokes all active", keep: 0, wantRevoked: []string{"t0", "t1", "t2", "t3"}, wantCount: 4},
		{name: "keep above count revokes nothing", keep: 10, wantCount: 0},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupRefreshTokenRepo(t)
			now := time.Now().UTC()
			// t0 is the oldest active token, t3 the newest.
			for i, id := range []string{"t0", "t1", "t2", "t3"} {
				created := now.Add(time.Duration(i-10) * time.Minute)
				seedRefreshToken(t, repo, id, id, func(rt *models.RefreshToken) { rt.CreatedAt = created })
			}
			// Not active, or another user: never counted or changed.
			seedRefreshToken(t, repo, "expired", "expired", func(rt *models.RefreshToken) {
				rt.ExpiresAt = now.Add(-time.Minute)
				rt.CreatedAt = now.Add(-time.Hour)
			})
			seedRefreshToken(t, repo, "other", "other", func(rt *models.RefreshToken) {
				rt.UserID = "u2"
				rt.CreatedAt = now.Add(-time.Hour)
			})

			n, err := repo.RevokeOldestForUser(context.Background(), "u1", tt.keep, tt.excludeID)
			require.NoError(t, err)
			assert.Equal(t, tt.wantCount, n)

			want := map[string]bool{}
			for _, id := range tt.wantRevoked {
				want[id] = true
			}
			for _, id := range []string{"t0", "t1", "t2", "t3", "expired", "other"} {
				assert.Equal(t, want[id], loadRefreshToken(t, repo, id).Revoked, "token %s", id)
			}
		})
	}
}

// TestGORMRefreshTokenRepository_DeleteExpired checks that only rows that
// expired before the cutoff are deleted (#706): the cleanup passes a cutoff
// in the past, so a recently expired row stays listable for logout-all.
func TestGORMRefreshTokenRepository_DeleteExpired(t *testing.T) {
	t.Parallel()
	repo := setupRefreshTokenRepo(t)
	now := time.Now().UTC()
	cutoff := now.Add(-20 * time.Minute)

	rows := []struct {
		id       string
		expires  time.Time
		wantKeep bool
	}{
		{id: "rt-live", expires: now.Add(time.Hour), wantKeep: true},
		{id: "rt-just-expired", expires: now.Add(-5 * time.Minute), wantKeep: true},
		{id: "rt-just-after-cutoff", expires: cutoff.Add(time.Second), wantKeep: true},
		{id: "rt-before-cutoff", expires: cutoff.Add(-time.Second), wantKeep: false},
		{id: "rt-long-ago", expires: now.Add(-48 * time.Hour), wantKeep: false},
	}
	for _, r := range rows {
		r := r
		seedRefreshToken(t, repo, r.id, "fam-"+r.id, func(rt *models.RefreshToken) { rt.ExpiresAt = r.expires })
	}

	deleted, err := repo.DeleteExpired(context.Background(), cutoff)
	require.NoError(t, err)
	assert.Equal(t, int64(2), deleted)

	for _, r := range rows {
		var count int64
		require.NoError(t, repo.db.Model(&models.RefreshToken{}).Where("id = ?", r.id).Count(&count).Error)
		assert.Equal(t, r.wantKeep, count == 1, r.id)
	}
}

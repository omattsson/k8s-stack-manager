package database

import (
	"context"
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
	require.NoError(t, repo.Create(rt))
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
			affected, err := repo.MarkRotatedIfActive("rt-1", at)
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
			affected, err = repo.MarkRotatedIfActive("rt-1", at)
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

	require.NoError(t, repo.RevokeFamily("legacy-1"))
	// RevokeFamily must not set rotated_at (only a rotation does).
	for _, id := range []string{"legacy-1", "succ-1"} {
		got := loadRefreshToken(t, repo, id)
		assert.True(t, got.Revoked, "%s should be revoked", id)
		assert.Nil(t, got.RotatedAt, "%s must not get rotated_at", id)
	}
	assert.False(t, loadRefreshToken(t, repo, "other-1").Revoked, "other session must stay active")
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
			got, err := repo.CountActiveInFamily(tt.family)
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

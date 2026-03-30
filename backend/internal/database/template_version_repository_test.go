package database

import (
	"context"
	"errors"
	"testing"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTemplateVersionRepo creates a fresh SQLite DB with all tables created
// via GORM's AutoMigrate, then returns a GORMTemplateVersionRepository.
func setupTemplateVersionRepo(t *testing.T) *GORMTemplateVersionRepository {
	t.Helper()
	db := setupTestDBWithAllTables(t)
	return NewGORMTemplateVersionRepository(db)
}

func TestGORMTemplateVersionRepository_Create(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version models.TemplateVersion
		wantErr bool
	}{
		{
			name: "success",
			version: models.TemplateVersion{
				ID:         "tv-create-1",
				TemplateID: "t1",
				Version:    "1.0.0",
				Snapshot:   `{"template":{"name":"test"},"charts":[]}`,
				CreatedBy:  "admin",
				CreatedAt:  time.Now(),
			},
			wantErr: false,
		},
		{
			name: "minimal fields",
			version: models.TemplateVersion{
				ID:         "tv-create-2",
				TemplateID: "t2",
				Version:    "0.1.0",
				Snapshot:   "{}",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupTemplateVersionRepo(t)
			ctx := context.Background()

			err := repo.Create(ctx, &tt.version)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestGORMTemplateVersionRepository_ListByTemplate(t *testing.T) {
	t.Parallel()

	t.Run("returns versions ordered newest first", func(t *testing.T) {
		t.Parallel()
		repo := setupTemplateVersionRepo(t)
		ctx := context.Background()

		now := time.Now().UTC()
		v1 := models.TemplateVersion{
			ID:         "lbt-1",
			TemplateID: "t-list",
			Version:    "1.0.0",
			Snapshot:   "{}",
			CreatedAt:  now.Add(-2 * time.Hour),
		}
		v2 := models.TemplateVersion{
			ID:         "lbt-2",
			TemplateID: "t-list",
			Version:    "2.0.0",
			Snapshot:   "{}",
			CreatedAt:  now.Add(-1 * time.Hour),
		}
		v3 := models.TemplateVersion{
			ID:         "lbt-3",
			TemplateID: "t-list",
			Version:    "3.0.0",
			Snapshot:   "{}",
			CreatedAt:  now,
		}
		require.NoError(t, repo.Create(ctx, &v1))
		require.NoError(t, repo.Create(ctx, &v2))
		require.NoError(t, repo.Create(ctx, &v3))

		versions, err := repo.ListByTemplate(ctx, "t-list")
		require.NoError(t, err)
		require.Len(t, versions, 3)

		// Newest first: v3 (3.0.0), v2 (2.0.0), v1 (1.0.0)
		assert.Equal(t, "3.0.0", versions[0].Version)
		assert.Equal(t, "2.0.0", versions[1].Version)
		assert.Equal(t, "1.0.0", versions[2].Version)
	})

	t.Run("returns empty for non-existent template", func(t *testing.T) {
		t.Parallel()
		repo := setupTemplateVersionRepo(t)
		ctx := context.Background()

		versions, err := repo.ListByTemplate(ctx, "t-no-versions")
		require.NoError(t, err)
		assert.Empty(t, versions)
	})

	t.Run("does not mix templates", func(t *testing.T) {
		t.Parallel()
		repo := setupTemplateVersionRepo(t)
		ctx := context.Background()

		v1 := models.TemplateVersion{
			ID: "mix-1", TemplateID: "t-a", Version: "1.0.0", Snapshot: "{}",
		}
		v2 := models.TemplateVersion{
			ID: "mix-2", TemplateID: "t-b", Version: "1.0.0", Snapshot: "{}",
		}
		require.NoError(t, repo.Create(ctx, &v1))
		require.NoError(t, repo.Create(ctx, &v2))

		aVersions, err := repo.ListByTemplate(ctx, "t-a")
		require.NoError(t, err)
		assert.Len(t, aVersions, 1)
		assert.Equal(t, "t-a", aVersions[0].TemplateID)

		bVersions, err := repo.ListByTemplate(ctx, "t-b")
		require.NoError(t, err)
		assert.Len(t, bVersions, 1)
		assert.Equal(t, "t-b", bVersions[0].TemplateID)
	})
}

func TestGORMTemplateVersionRepository_GetByID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		templateID string
		versionID  string
		seed       *models.TemplateVersion
		wantErr    bool
		wantNotFound bool
	}{
		{
			name:       "found",
			templateID: "t-getbyid",
			versionID:  "tv-getbyid-1",
			seed: &models.TemplateVersion{
				ID:         "tv-getbyid-1",
				TemplateID: "t-getbyid",
				Version:    "1.0.0",
				Snapshot:   `{"template":{"name":"found"}}`,
				CreatedBy:  "admin",
			},
			wantErr: false,
		},
		{
			name:         "not found - wrong template",
			templateID:   "t-wrong",
			versionID:    "tv-getbyid-2",
			seed: &models.TemplateVersion{
				ID:         "tv-getbyid-2",
				TemplateID: "t-right",
				Version:    "1.0.0",
				Snapshot:   "{}",
			},
			wantErr:      true,
			wantNotFound: true,
		},
		{
			name:         "not found - no such ID",
			templateID:   "t-any",
			versionID:    "tv-nonexistent",
			seed:         nil,
			wantErr:      true,
			wantNotFound: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupTemplateVersionRepo(t)
			ctx := context.Background()

			if tt.seed != nil {
				require.NoError(t, repo.Create(ctx, tt.seed))
			}

			result, err := repo.GetByID(ctx, tt.templateID, tt.versionID)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, result)
				if tt.wantNotFound {
					var dbErr *dberrors.DatabaseError
					assert.True(t, errors.As(err, &dbErr))
					assert.True(t, errors.Is(dbErr.Err, dberrors.ErrNotFound))
				}
			} else {
				require.NoError(t, err)
				assert.NotNil(t, result)
				assert.Equal(t, tt.versionID, result.ID)
				assert.Equal(t, tt.templateID, result.TemplateID)
			}
		})
	}
}

func TestGORMTemplateVersionRepository_GetLatestByTemplate(t *testing.T) {
	t.Parallel()

	t.Run("returns the most recent version", func(t *testing.T) {
		t.Parallel()
		repo := setupTemplateVersionRepo(t)
		ctx := context.Background()

		now := time.Now().UTC()
		v1 := models.TemplateVersion{
			ID: "gl-1", TemplateID: "t-latest", Version: "1.0.0", Snapshot: "{}",
			CreatedAt: now.Add(-2 * time.Hour),
		}
		v2 := models.TemplateVersion{
			ID: "gl-2", TemplateID: "t-latest", Version: "2.0.0", Snapshot: "{}",
			CreatedAt: now.Add(-1 * time.Hour),
		}
		v3 := models.TemplateVersion{
			ID: "gl-3", TemplateID: "t-latest", Version: "3.0.0", Snapshot: "{}",
			CreatedAt: now,
		}
		require.NoError(t, repo.Create(ctx, &v1))
		require.NoError(t, repo.Create(ctx, &v2))
		require.NoError(t, repo.Create(ctx, &v3))

		latest, err := repo.GetLatestByTemplate(ctx, "t-latest")
		require.NoError(t, err)
		assert.Equal(t, "3.0.0", latest.Version)
		assert.Equal(t, "gl-3", latest.ID)
	})

	t.Run("returns not found for template with no versions", func(t *testing.T) {
		t.Parallel()
		repo := setupTemplateVersionRepo(t)
		ctx := context.Background()

		result, err := repo.GetLatestByTemplate(ctx, "t-no-versions")
		assert.Error(t, err)
		assert.Nil(t, result)

		var dbErr *dberrors.DatabaseError
		assert.True(t, errors.As(err, &dbErr))
		assert.True(t, errors.Is(dbErr.Err, dberrors.ErrNotFound))
	})
}

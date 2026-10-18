package database

import (
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeleteAfterCleanMigration(t *testing.T) {
	t.Parallel()
	db := setupTestDBWithAllTables(t)
	m := db.Migrator()
	mig := deleteAfterCleanMigration()
	assert.Equal(t, "20261010000057", mig.Version)

	require.NoError(t, mig.Down(db))
	assert.False(t, m.HasColumn(&models.StackInstance{}, "DeleteAfterClean"))
	require.NoError(t, mig.Down(db), "Down must be idempotent")

	require.NoError(t, mig.Up(db))
	require.NoError(t, mig.Up(db), "Up must be idempotent")
	assert.True(t, m.HasColumn(&models.StackInstance{}, "DeleteAfterClean"))
}

func TestMarkCleanStart(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  string
		wantErr error
	}{
		{name: "running instance is marked", status: models.StackStatusRunning},
		{name: "error instance is marked", status: models.StackStatusError},
		{name: "cleaning instance is a conflict", status: models.StackStatusCleaning, wantErr: models.ErrDeleteConflict},
		{name: "draft instance is a conflict", status: models.StackStatusDraft, wantErr: models.ErrDeleteConflict},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupStackInstanceRepo(t)
			inst := &models.StackInstance{
				Name: "del", StackDefinitionID: "d1", Namespace: "stack-del", OwnerID: "o1",
				Branch: "main", Status: tt.status, ErrorMessage: "old error",
			}
			require.NoError(t, repo.Create(inst))

			err := repo.MarkCleanStart(inst.ID, models.CleanStatuses, true)
			stored, findErr := repo.FindByID(inst.ID)
			require.NoError(t, findErr)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Equal(t, tt.status, stored.Status)
				assert.False(t, stored.DeleteAfterClean)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, models.StackStatusCleaning, stored.Status)
			assert.True(t, stored.DeleteAfterClean)
			assert.Empty(t, stored.ErrorMessage)

			// A second delete loses.
			require.ErrorIs(t, repo.MarkCleanStart(inst.ID, models.CleanStatuses, true), models.ErrDeleteConflict)

			// Update does not write the mark (a stale copy cannot clear it).
			stale := *stored
			stale.DeleteAfterClean = false
			require.NoError(t, repo.Update(&stale))
			stored, findErr = repo.FindByID(inst.ID)
			require.NoError(t, findErr)
			assert.True(t, stored.DeleteAfterClean, "Update must not clear the mark")

			// A plain clean does not overwrite the delete mark, also when
			// the status allows a clean.
			require.NoError(t, repo.Update(&models.StackInstance{
				ID: stored.ID, Name: stored.Name, StackDefinitionID: stored.StackDefinitionID, Namespace: stored.Namespace,
				OwnerID: stored.OwnerID, Branch: stored.Branch, Status: models.StackStatusStopped, CreatedAt: stored.CreatedAt,
			}))
			require.ErrorIs(t, repo.MarkCleanStart(inst.ID, models.CleanStatuses, false), models.ErrCleanConflict)

			require.NoError(t, repo.ClearDeleteAfterClean(inst.ID))
			stored, findErr = repo.FindByID(inst.ID)
			require.NoError(t, findErr)
			assert.False(t, stored.DeleteAfterClean)

			// Without the mark the plain clean starts and leaves the mark off.
			require.NoError(t, repo.MarkCleanStart(inst.ID, models.CleanStatuses, false))
			stored, findErr = repo.FindByID(inst.ID)
			require.NoError(t, findErr)
			assert.Equal(t, models.StackStatusCleaning, stored.Status)
			assert.False(t, stored.DeleteAfterClean)
		})
	}
}

package database

import (
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMigration_PostDeployHookUntil checks migration 51: the column exists
// after AutoMigrate, and Up and Down can run again without an error.
func TestMigration_PostDeployHookUntil(t *testing.T) {
	t.Parallel()
	db := setupMigrationTestDB(t)
	require.NoError(t, db.AutoMigrate())

	m := db.DB.Migrator()
	assert.True(t, m.HasColumn(&models.StackInstance{}, "PostDeployHookUntil"))

	mig := postDeployHookUntilMigration()
	require.NoError(t, mig.Down(db.DB))
	assert.False(t, m.HasColumn(&models.StackInstance{}, "PostDeployHookUntil"))
	require.NoError(t, mig.Down(db.DB), "Down without the column")
	require.NoError(t, mig.Up(db.DB))
	assert.True(t, m.HasColumn(&models.StackInstance{}, "PostDeployHookUntil"))
	require.NoError(t, mig.Up(db.DB), "Up on an existing column")
}

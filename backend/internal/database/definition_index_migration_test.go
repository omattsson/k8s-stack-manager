package database

import (
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMigration_DefinitionOwnerNameIndex checks migration 48: AutoMigrate
// creates the index, and Up and Down can run again without an error.
func TestMigration_DefinitionOwnerNameIndex(t *testing.T) {
	t.Parallel()
	db := setupMigrationTestDB(t)
	require.NoError(t, db.AutoMigrate())

	m := db.DB.Migrator()
	assert.True(t, m.HasIndex(&models.StackDefinition{}, definitionOwnerNameIndexName))

	mig := definitionOwnerNameIndexMigration()
	require.NoError(t, mig.Up(db.DB), "Up on an existing index")

	require.NoError(t, mig.Down(db.DB))
	assert.False(t, m.HasIndex(&models.StackDefinition{}, definitionOwnerNameIndexName))
	require.NoError(t, mig.Down(db.DB), "Down without the index")

	require.NoError(t, mig.Up(db.DB))
	assert.True(t, m.HasIndex(&models.StackDefinition{}, definitionOwnerNameIndexName))
}

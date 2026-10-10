package database

import (
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMigration_UserRoleIndex checks migration 50: the users.role index
// exists after the migrations, and Up and Down can run again.
func TestMigration_UserRoleIndex(t *testing.T) {
	t.Parallel()
	db := setupMigrationTestDB(t)
	require.NoError(t, db.AutoMigrate())

	m := db.DB.Migrator()
	assert.True(t, m.HasIndex(&models.User{}, userRoleIndexName))

	mig := userRoleIndexMigration()
	require.NoError(t, mig.Up(db.DB), "Up on an existing index")
	require.NoError(t, mig.Down(db.DB))
	assert.False(t, m.HasIndex(&models.User{}, userRoleIndexName))
	require.NoError(t, mig.Down(db.DB), "Down without the index")
	require.NoError(t, mig.Up(db.DB))
	assert.True(t, m.HasIndex(&models.User{}, userRoleIndexName))
}

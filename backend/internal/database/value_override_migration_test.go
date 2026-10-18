package database

import (
	"testing"

	"backend/internal/database/schema"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMigration_RemoveEmptyValueOverrides checks migration 42: value override
// rows with empty or whitespace-only values are deleted, real overrides stay.
func TestMigration_RemoveEmptyValueOverrides(t *testing.T) {
	t.Parallel()
	db := setupMigrationTestDB(t)
	require.NoError(t, db.AutoMigrate())
	require.NoError(t, db.DB.Unscoped().Where("version = ?", "20261008000042").Delete(&schema.SchemaVersion{}).Error)

	rows := []models.ValueOverride{
		{ID: "empty", StackInstanceID: "i1", ChartConfigID: "c1", Values: ""},
		{ID: "blank", StackInstanceID: "i1", ChartConfigID: "c2", Values: "  \n\t\r\n"},
		{ID: "real", StackInstanceID: "i1", ChartConfigID: "c3", Values: "replicaCount: 2\n"},
	}
	for i := range rows {
		require.NoError(t, db.DB.Create(&rows[i]).Error)
	}

	require.NoError(t, db.AutoMigrate())

	var ids []string
	require.NoError(t, db.DB.Model(&models.ValueOverride{}).Order("id").Pluck("id", &ids).Error)
	assert.Equal(t, []string{"real"}, ids)

	require.NoError(t, removeEmptyValueOverridesMigration().Down(db.DB))
}

package database

import (
	"fmt"
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGORMChartConfigRepository_CountByDefinitionIDs checks the grouped chart
// count of the definition list.
func TestGORMChartConfigRepository_CountByDefinitionIDs(t *testing.T) {
	t.Parallel()
	db := setupTestDBWithAllTables(t)
	repo := NewGORMChartConfigRepository(db)

	for i, defID := range []string{"d1", "d1", "d1", "d2", "d3"} {
		require.NoError(t, repo.Create(&models.ChartConfig{
			ID:                fmt.Sprintf("c%d", i),
			StackDefinitionID: defID,
			ChartName:         fmt.Sprintf("chart-%d", i),
		}))
	}

	got, err := repo.CountByDefinitionIDs([]string{"d1", "d2", "empty"})
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"d1": 3, "d2": 1}, got)

	none, err := repo.CountByDefinitionIDs(nil)
	require.NoError(t, err)
	assert.Empty(t, none)
}

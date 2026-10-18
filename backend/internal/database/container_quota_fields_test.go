package database

import (
	"context"
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContainerQuotaFieldsMigration(t *testing.T) {
	t.Parallel()
	db := setupTestDBWithAllTables(t)
	m := db.Migrator()
	mig := containerQuotaFieldsMigration()
	assert.Equal(t, "20261012000058", mig.Version)

	tables := []interface{}{&models.ResourceQuotaConfig{}, &models.InstanceQuotaOverride{}}

	require.NoError(t, mig.Down(db))
	for _, table := range tables {
		for _, col := range containerQuotaFieldColumns {
			assert.False(t, m.HasColumn(table, col), "column %s after Down", col)
		}
	}
	require.NoError(t, mig.Down(db), "Down must be idempotent")

	require.NoError(t, mig.Up(db))
	require.NoError(t, mig.Up(db), "Up must be idempotent")
	for _, table := range tables {
		for _, col := range containerQuotaFieldColumns {
			assert.True(t, m.HasColumn(table, col), "column %s after Up", col)
		}
	}
}

func TestContainerQuotaFields_RoundTrip(t *testing.T) {
	t.Parallel()
	db := setupTestDBWithAllTables(t)
	ctx := context.Background()

	quotaRepo := NewGORMResourceQuotaRepository(db)
	cfg := &models.ResourceQuotaConfig{
		ClusterID:                        "c-container",
		CPULimit:                         "8",
		MemoryLimit:                      "16Gi",
		ContainerCPURequest:              "100m",
		ContainerCPULimit:                "500m",
		ContainerCPUMax:                  "2",
		ContainerMemoryRequest:           "128Mi",
		ContainerMemoryLimit:             "512Mi",
		ContainerMemoryMax:               "4Gi",
		EphemeralStorageLimit:            "20Gi",
		ContainerEphemeralStorageRequest: "128Mi",
		ContainerEphemeralStorageLimit:   "1Gi",
	}
	require.NoError(t, quotaRepo.Upsert(ctx, cfg))
	gotCfg, err := quotaRepo.GetByClusterID(ctx, "c-container")
	require.NoError(t, err)
	assert.Equal(t, "100m", gotCfg.ContainerCPURequest)
	assert.Equal(t, "500m", gotCfg.ContainerCPULimit)
	assert.Equal(t, "2", gotCfg.ContainerCPUMax)
	assert.Equal(t, "128Mi", gotCfg.ContainerMemoryRequest)
	assert.Equal(t, "512Mi", gotCfg.ContainerMemoryLimit)
	assert.Equal(t, "4Gi", gotCfg.ContainerMemoryMax)
	assert.Equal(t, "20Gi", gotCfg.EphemeralStorageLimit)
	assert.Equal(t, "128Mi", gotCfg.ContainerEphemeralStorageRequest)
	assert.Equal(t, "1Gi", gotCfg.ContainerEphemeralStorageLimit)

	overrideRepo := NewGORMInstanceQuotaOverrideRepository(db)
	ov := &models.InstanceQuotaOverride{
		StackInstanceID:       "i-container",
		ContainerCPUMax:       "4",
		ContainerMemoryLimit:  "1Gi",
		EphemeralStorageLimit: "10Gi",
	}
	require.NoError(t, overrideRepo.Upsert(ctx, ov))
	gotOv, err := overrideRepo.GetByInstanceID(ctx, "i-container")
	require.NoError(t, err)
	assert.Equal(t, "4", gotOv.ContainerCPUMax)
	assert.Equal(t, "1Gi", gotOv.ContainerMemoryLimit)
	assert.Equal(t, "10Gi", gotOv.EphemeralStorageLimit)
	assert.Empty(t, gotOv.ContainerCPURequest)

	// Validation runs in the repository: container limit above its max.
	bad := &models.ResourceQuotaConfig{ClusterID: "c-bad", ContainerCPULimit: "4", ContainerCPUMax: "2"}
	require.Error(t, quotaRepo.Upsert(ctx, bad))
}

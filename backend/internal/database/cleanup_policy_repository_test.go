package database

import (
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupCleanupPolicyRepo(t *testing.T) *GORMCleanupPolicyRepository {
	t.Helper()
	db := setupTestDBWithAllTables(t)
	return NewGORMCleanupPolicyRepository(db)
}

func TestGORMCleanupPolicyRepository_CRUD(t *testing.T) {
	t.Parallel()

	repo := setupCleanupPolicyRepo(t)

	// Create
	policy := &models.CleanupPolicy{
		Name:      "nightly-cleanup",
		ClusterID: "all",
		Action:    "stop",
		Condition: "idle_days:7",
		Schedule:  "0 2 * * *",
		Enabled:   true,
	}
	err := repo.Create(policy)
	require.NoError(t, err)
	assert.NotEmpty(t, policy.ID)
	assert.False(t, policy.CreatedAt.IsZero())

	// FindByID
	found, err := repo.FindByID(policy.ID)
	require.NoError(t, err)
	assert.Equal(t, "nightly-cleanup", found.Name)

	// Update
	found.Schedule = "0 3 * * *"
	err = repo.Update(found)
	require.NoError(t, err)
	updated, err := repo.FindByID(policy.ID)
	require.NoError(t, err)
	assert.Equal(t, "0 3 * * *", updated.Schedule)

	// Delete
	err = repo.Delete(policy.ID)
	require.NoError(t, err)
	_, err = repo.FindByID(policy.ID)
	assert.Error(t, err)
}

func TestGORMCleanupPolicyRepository_Create_ValidationError(t *testing.T) {
	t.Parallel()

	repo := setupCleanupPolicyRepo(t)
	// Missing required fields.
	err := repo.Create(&models.CleanupPolicy{})
	assert.Error(t, err)
}

func TestGORMCleanupPolicyRepository_Update_ValidationError(t *testing.T) {
	t.Parallel()

	repo := setupCleanupPolicyRepo(t)
	policy := &models.CleanupPolicy{
		Name:      "valid-policy",
		ClusterID: "all",
		Action:    "stop",
		Condition: "idle_days:7",
		Schedule:  "0 2 * * *",
		Enabled:   true,
	}
	require.NoError(t, repo.Create(policy))

	// Clear required field and try to update.
	policy.Name = ""
	err := repo.Update(policy)
	assert.Error(t, err)
}

func TestGORMCleanupPolicyRepository_FindByID_NotFound(t *testing.T) {
	t.Parallel()

	repo := setupCleanupPolicyRepo(t)
	_, err := repo.FindByID("nonexistent")
	assert.Error(t, err)
}

func TestGORMCleanupPolicyRepository_Delete_NotFound(t *testing.T) {
	t.Parallel()

	repo := setupCleanupPolicyRepo(t)
	err := repo.Delete("nonexistent")
	assert.Error(t, err)
}

func TestGORMCleanupPolicyRepository_List(t *testing.T) {
	t.Parallel()

	repo := setupCleanupPolicyRepo(t)
	require.NoError(t, repo.Create(&models.CleanupPolicy{Name: "p1", ClusterID: "all", Action: "stop", Condition: "idle_days:7", Schedule: "0 * * * *", Enabled: true}))
	require.NoError(t, repo.Create(&models.CleanupPolicy{Name: "p2", ClusterID: "c1", Action: "clean", Condition: "ttl_expired", Schedule: "0 * * * *", Enabled: false}))

	policies, err := repo.List()
	require.NoError(t, err)
	assert.Len(t, policies, 2)
}

func TestGORMCleanupPolicyRepository_ListEnabled(t *testing.T) {
	t.Parallel()

	repo := setupCleanupPolicyRepo(t)
	require.NoError(t, repo.Create(&models.CleanupPolicy{Name: "enabled", ClusterID: "all", Action: "stop", Condition: "idle_days:7", Schedule: "0 * * * *", Enabled: true}))
	require.NoError(t, repo.Create(&models.CleanupPolicy{Name: "disabled", ClusterID: "c1", Action: "clean", Condition: "ttl_expired", Schedule: "0 * * * *", Enabled: false}))
	require.NoError(t, repo.Create(&models.CleanupPolicy{Name: "enabled2", ClusterID: "c2", Action: "delete", Condition: "status:stopped,age_days:14", Schedule: "0 * * * *", Enabled: true}))

	policies, err := repo.ListEnabled()
	require.NoError(t, err)
	assert.Len(t, policies, 2)
}

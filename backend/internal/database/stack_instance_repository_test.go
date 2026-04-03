package database

import (
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupStackInstanceRepo(t *testing.T) *GORMStackInstanceRepository {
	t.Helper()
	db := setupTestDBWithAllTables(t)
	return NewGORMStackInstanceRepository(db)
}

func TestGORMStackInstanceRepository_CRUD(t *testing.T) {
	t.Parallel()

	repo := setupStackInstanceRepo(t)

	// Create
	inst := &models.StackInstance{
		Name:              "my-instance",
		StackDefinitionID: "def-1",
		Namespace:         "stack-my-instance-alice",
		OwnerID:           "alice",
		Branch:            "main",
		Status:            models.StackStatusDraft,
	}
	err := repo.Create(inst)
	require.NoError(t, err)
	assert.NotEmpty(t, inst.ID)
	assert.False(t, inst.CreatedAt.IsZero())

	// FindByID
	found, err := repo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, "my-instance", found.Name)

	// Update
	found.Status = models.StackStatusRunning
	err = repo.Update(found)
	require.NoError(t, err)

	updated, err := repo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusRunning, updated.Status)

	// Delete
	err = repo.Delete(inst.ID)
	require.NoError(t, err)
	_, err = repo.FindByID(inst.ID)
	assert.Error(t, err)
}

func TestGORMStackInstanceRepository_FindByID_NotFound(t *testing.T) {
	t.Parallel()

	repo := setupStackInstanceRepo(t)
	_, err := repo.FindByID("nonexistent")
	assert.Error(t, err)
}

func TestGORMStackInstanceRepository_FindByNamespace(t *testing.T) {
	t.Parallel()

	repo := setupStackInstanceRepo(t)
	require.NoError(t, repo.Create(&models.StackInstance{
		Name:              "ns-test",
		StackDefinitionID: "d1",
		Namespace:         "stack-ns-test-bob",
		OwnerID:           "bob",
		Branch:            "main",
		Status:            models.StackStatusDraft,
	}))

	t.Run("found", func(t *testing.T) {
		t.Parallel()
		inst, err := repo.FindByNamespace("stack-ns-test-bob")
		require.NoError(t, err)
		assert.Equal(t, "ns-test", inst.Name)
	})

	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		_, err := repo.FindByNamespace("nope")
		assert.Error(t, err)
	})
}

func TestGORMStackInstanceRepository_Delete_NotFound(t *testing.T) {
	t.Parallel()

	repo := setupStackInstanceRepo(t)
	err := repo.Delete("nonexistent")
	assert.Error(t, err)
}

func TestGORMStackInstanceRepository_List(t *testing.T) {
	t.Parallel()

	repo := setupStackInstanceRepo(t)
	require.NoError(t, repo.Create(&models.StackInstance{Name: "i1", StackDefinitionID: "d1", Namespace: "ns1", OwnerID: "o1", Branch: "main", Status: "draft"}))
	require.NoError(t, repo.Create(&models.StackInstance{Name: "i2", StackDefinitionID: "d1", Namespace: "ns2", OwnerID: "o2", Branch: "main", Status: "draft"}))

	instances, err := repo.List()
	require.NoError(t, err)
	assert.Len(t, instances, 2)
}

func TestGORMStackInstanceRepository_ListPaged(t *testing.T) {
	t.Parallel()

	repo := setupStackInstanceRepo(t)
	for i := 0; i < 5; i++ {
		require.NoError(t, repo.Create(&models.StackInstance{
			Name:              "paged-" + string(rune('a'+i)),
			StackDefinitionID: "d1",
			Namespace:         "ns-paged-" + string(rune('a'+i)),
			OwnerID:           "o1",
			Branch:            "main",
			Status:            "draft",
		}))
	}

	instances, total, err := repo.ListPaged(2, 0)
	require.NoError(t, err)
	assert.Len(t, instances, 2)
	assert.Equal(t, 5, total)

	instances2, total2, err := repo.ListPaged(2, 2)
	require.NoError(t, err)
	assert.Len(t, instances2, 2)
	assert.Equal(t, 5, total2)
}

func TestGORMStackInstanceRepository_ListByOwner(t *testing.T) {
	t.Parallel()

	repo := setupStackInstanceRepo(t)
	require.NoError(t, repo.Create(&models.StackInstance{Name: "i1", StackDefinitionID: "d1", Namespace: "ns-o1", OwnerID: "owner-a", Branch: "main", Status: "draft"}))
	require.NoError(t, repo.Create(&models.StackInstance{Name: "i2", StackDefinitionID: "d1", Namespace: "ns-o2", OwnerID: "owner-a", Branch: "main", Status: "draft"}))
	require.NoError(t, repo.Create(&models.StackInstance{Name: "i3", StackDefinitionID: "d1", Namespace: "ns-o3", OwnerID: "owner-b", Branch: "main", Status: "draft"}))

	instances, err := repo.ListByOwner("owner-a")
	require.NoError(t, err)
	assert.Len(t, instances, 2)
}

func TestGORMStackInstanceRepository_FindByCluster(t *testing.T) {
	t.Parallel()

	repo := setupStackInstanceRepo(t)
	require.NoError(t, repo.Create(&models.StackInstance{Name: "c1-i1", StackDefinitionID: "d1", Namespace: "ns-c1", OwnerID: "o1", Branch: "main", Status: "draft", ClusterID: "cluster-a"}))
	require.NoError(t, repo.Create(&models.StackInstance{Name: "c1-i2", StackDefinitionID: "d1", Namespace: "ns-c2", OwnerID: "o1", Branch: "main", Status: "draft", ClusterID: "cluster-a"}))
	require.NoError(t, repo.Create(&models.StackInstance{Name: "c2-i1", StackDefinitionID: "d1", Namespace: "ns-c3", OwnerID: "o1", Branch: "main", Status: "draft", ClusterID: "cluster-b"}))

	instances, err := repo.FindByCluster("cluster-a")
	require.NoError(t, err)
	assert.Len(t, instances, 2)
}

func TestGORMStackInstanceRepository_CountByClusterAndOwner(t *testing.T) {
	t.Parallel()

	repo := setupStackInstanceRepo(t)
	require.NoError(t, repo.Create(&models.StackInstance{Name: "cnt1", StackDefinitionID: "d1", Namespace: "ns-cnt1", OwnerID: "o1", Branch: "main", Status: "draft", ClusterID: "c1"}))
	require.NoError(t, repo.Create(&models.StackInstance{Name: "cnt2", StackDefinitionID: "d1", Namespace: "ns-cnt2", OwnerID: "o1", Branch: "main", Status: "draft", ClusterID: "c1"}))
	require.NoError(t, repo.Create(&models.StackInstance{Name: "cnt3", StackDefinitionID: "d1", Namespace: "ns-cnt3", OwnerID: "o2", Branch: "main", Status: "draft", ClusterID: "c1"}))

	count, err := repo.CountByClusterAndOwner("c1", "o1")
	require.NoError(t, err)
	assert.Equal(t, 2, count)
}

func TestGORMStackInstanceRepository_ListExpired(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setup     func(t *testing.T, repo *GORMStackInstanceRepository)
		wantCount int
	}{
		{
			name: "returns expired running instances",
			setup: func(t *testing.T, repo *GORMStackInstanceRepository) {
				t.Helper()
				past := time.Now().UTC().Add(-1 * time.Hour)
				future := time.Now().UTC().Add(1 * time.Hour)

				// Expired running — should be returned.
				expiredRunning := &models.StackInstance{
					Name: "expired-run", StackDefinitionID: "d1", Namespace: "ns-exp1",
					OwnerID: "o1", Branch: "main", Status: models.StackStatusRunning,
				}
				require.NoError(t, repo.Create(expiredRunning))
				expiredRunning.ExpiresAt = &past
				require.NoError(t, repo.Update(expiredRunning))

				// Running but not expired — should not be returned.
				futureRunning := &models.StackInstance{
					Name: "future-run", StackDefinitionID: "d1", Namespace: "ns-exp2",
					OwnerID: "o1", Branch: "main", Status: models.StackStatusRunning,
				}
				require.NoError(t, repo.Create(futureRunning))
				futureRunning.ExpiresAt = &future
				require.NoError(t, repo.Update(futureRunning))

				// Expired but not running — should not be returned.
				expiredDraft := &models.StackInstance{
					Name: "expired-draft", StackDefinitionID: "d1", Namespace: "ns-exp3",
					OwnerID: "o1", Branch: "main", Status: models.StackStatusDraft,
				}
				require.NoError(t, repo.Create(expiredDraft))
				expiredDraft.ExpiresAt = &past
				require.NoError(t, repo.Update(expiredDraft))

				// Running with no ExpiresAt — should not be returned.
				require.NoError(t, repo.Create(&models.StackInstance{
					Name: "no-expiry", StackDefinitionID: "d1", Namespace: "ns-exp4",
					OwnerID: "o1", Branch: "main", Status: models.StackStatusRunning,
				}))
			},
			wantCount: 1,
		},
		{
			name:      "empty when no instances exist",
			setup:     func(_ *testing.T, _ *GORMStackInstanceRepository) {},
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repo := setupStackInstanceRepo(t)
			tt.setup(t, repo)

			expired, err := repo.ListExpired()
			require.NoError(t, err)
			assert.Len(t, expired, tt.wantCount)
		})
	}
}

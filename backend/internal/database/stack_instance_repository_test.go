package database

import (
	"context"
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
	err := repo.Create(context.Background(), inst)
	require.NoError(t, err)
	assert.NotEmpty(t, inst.ID)
	assert.False(t, inst.CreatedAt.IsZero())

	// FindByID
	found, err := repo.FindByID(context.Background(), inst.ID)
	require.NoError(t, err)
	assert.Equal(t, "my-instance", found.Name)

	// Update
	found.Status = models.StackStatusRunning
	err = repo.Update(context.Background(), found)
	require.NoError(t, err)

	updated, err := repo.FindByID(context.Background(), inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusRunning, updated.Status)

	// Delete
	err = repo.Delete(context.Background(), inst.ID)
	require.NoError(t, err)
	_, err = repo.FindByID(context.Background(), inst.ID)
	assert.Error(t, err)
}

func TestGORMStackInstanceRepository_FindByID_NotFound(t *testing.T) {
	t.Parallel()

	repo := setupStackInstanceRepo(t)
	_, err := repo.FindByID(context.Background(), "nonexistent")
	assert.Error(t, err)
}

func TestGORMStackInstanceRepository_FindByNamespace(t *testing.T) {
	t.Parallel()

	repo := setupStackInstanceRepo(t)
	require.NoError(t, repo.Create(context.Background(), &models.StackInstance{
		Name:              "ns-test",
		StackDefinitionID: "d1",
		Namespace:         "stack-ns-test-bob",
		OwnerID:           "bob",
		Branch:            "main",
		Status:            models.StackStatusDraft,
	}))

	t.Run("found", func(t *testing.T) {
		t.Parallel()
		inst, err := repo.FindByNamespace(context.Background(), "stack-ns-test-bob")
		require.NoError(t, err)
		assert.Equal(t, "ns-test", inst.Name)
	})

	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		_, err := repo.FindByNamespace(context.Background(), "nope")
		assert.Error(t, err)
	})
}

func TestGORMStackInstanceRepository_Delete_NotFound(t *testing.T) {
	t.Parallel()

	repo := setupStackInstanceRepo(t)
	err := repo.Delete(context.Background(), "nonexistent")
	assert.Error(t, err)
}

func TestGORMStackInstanceRepository_List(t *testing.T) {
	t.Parallel()

	repo := setupStackInstanceRepo(t)
	require.NoError(t, repo.Create(context.Background(), &models.StackInstance{Name: "i1", StackDefinitionID: "d1", Namespace: "ns1", OwnerID: "o1", Branch: "main", Status: "draft"}))
	require.NoError(t, repo.Create(context.Background(), &models.StackInstance{Name: "i2", StackDefinitionID: "d1", Namespace: "ns2", OwnerID: "o2", Branch: "main", Status: "draft"}))

	instances, err := repo.List(context.Background())
	require.NoError(t, err)
	assert.Len(t, instances, 2)
}

func TestGORMStackInstanceRepository_ListPaged(t *testing.T) {
	t.Parallel()

	repo := setupStackInstanceRepo(t)
	for i := 0; i < 5; i++ {
		require.NoError(t, repo.Create(context.Background(), &models.StackInstance{
			Name:              "paged-" + string(rune('a'+i)),
			StackDefinitionID: "d1",
			Namespace:         "ns-paged-" + string(rune('a'+i)),
			OwnerID:           "o1",
			Branch:            "main",
			Status:            "draft",
		}))
	}

	instances, total, err := repo.ListPaged(context.Background(), models.StackInstanceFilter{}, 2, 0)
	require.NoError(t, err)
	assert.Len(t, instances, 2)
	assert.Equal(t, 5, total)

	instances2, total2, err := repo.ListPaged(context.Background(), models.StackInstanceFilter{}, 2, 2)
	require.NoError(t, err)
	assert.Len(t, instances2, 2)
	assert.Equal(t, 5, total2)
}

func TestGORMStackInstanceRepository_ListByOwner(t *testing.T) {
	t.Parallel()

	repo := setupStackInstanceRepo(t)
	require.NoError(t, repo.Create(context.Background(), &models.StackInstance{Name: "i1", StackDefinitionID: "d1", Namespace: "ns-o1", OwnerID: "owner-a", Branch: "main", Status: "draft"}))
	require.NoError(t, repo.Create(context.Background(), &models.StackInstance{Name: "i2", StackDefinitionID: "d1", Namespace: "ns-o2", OwnerID: "owner-a", Branch: "main", Status: "draft"}))
	require.NoError(t, repo.Create(context.Background(), &models.StackInstance{Name: "i3", StackDefinitionID: "d1", Namespace: "ns-o3", OwnerID: "owner-b", Branch: "main", Status: "draft"}))

	instances, err := repo.ListByOwner(context.Background(), "owner-a")
	require.NoError(t, err)
	assert.Len(t, instances, 2)
}

func TestGORMStackInstanceRepository_FindByCluster(t *testing.T) {
	t.Parallel()

	repo := setupStackInstanceRepo(t)
	require.NoError(t, repo.Create(context.Background(), &models.StackInstance{Name: "c1-i1", StackDefinitionID: "d1", Namespace: "ns-c1", OwnerID: "o1", Branch: "main", Status: "draft", ClusterID: "cluster-a"}))
	require.NoError(t, repo.Create(context.Background(), &models.StackInstance{Name: "c1-i2", StackDefinitionID: "d1", Namespace: "ns-c2", OwnerID: "o1", Branch: "main", Status: "draft", ClusterID: "cluster-a"}))
	require.NoError(t, repo.Create(context.Background(), &models.StackInstance{Name: "c2-i1", StackDefinitionID: "d1", Namespace: "ns-c3", OwnerID: "o1", Branch: "main", Status: "draft", ClusterID: "cluster-b"}))

	instances, err := repo.FindByCluster(context.Background(), "cluster-a")
	require.NoError(t, err)
	assert.Len(t, instances, 2)
}

// TestGORMStackInstanceRepository_CountForInstanceLimit checks the count of
// the per-user instance limit: with includeUnassigned the instances without
// a cluster count too, because they run on the default cluster (#682).
func TestGORMStackInstanceRepository_CountForInstanceLimit(t *testing.T) {
	t.Parallel()

	repo := setupStackInstanceRepo(t)
	seed := []struct{ name, owner, cluster string }{
		{"cnt1", "o1", "c1"}, {"cnt2", "o1", "c1"}, {"cnt3", "o2", "c1"},
		{"cnt4", "o1", ""}, {"cnt5", "o1", "c2"},
	}
	for _, s := range seed {
		require.NoError(t, repo.Create(context.Background(), &models.StackInstance{
			Name: s.name, StackDefinitionID: "d1", Namespace: "ns-" + s.name, OwnerID: s.owner,
			Branch: "main", Status: "draft", ClusterID: s.cluster,
		}))
	}

	tests := []struct {
		name              string
		cluster           string
		owner             string
		includeUnassigned bool
		want              int
	}{
		{name: "own cluster only", cluster: "c1", owner: "o1", want: 2},
		{name: "default cluster with unassigned", cluster: "c1", owner: "o1", includeUnassigned: true, want: 3},
		{name: "other owner", cluster: "c1", owner: "o2", includeUnassigned: true, want: 1},
		{name: "other cluster", cluster: "c2", owner: "o1", want: 1},
		{name: "owner without a user row", cluster: "c9", owner: "o9", want: 0},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			count, err := repo.CountForInstanceLimit(context.Background(), tt.cluster, tt.owner, tt.includeUnassigned)
			require.NoError(t, err)
			assert.Equal(t, tt.want, count)
		})
	}
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
				require.NoError(t, repo.Create(context.Background(), expiredRunning))
				expiredRunning.ExpiresAt = &past
				require.NoError(t, repo.Update(context.Background(), expiredRunning))

				// Running but not expired — should not be returned.
				futureRunning := &models.StackInstance{
					Name: "future-run", StackDefinitionID: "d1", Namespace: "ns-exp2",
					OwnerID: "o1", Branch: "main", Status: models.StackStatusRunning,
				}
				require.NoError(t, repo.Create(context.Background(), futureRunning))
				futureRunning.ExpiresAt = &future
				require.NoError(t, repo.Update(context.Background(), futureRunning))

				// Expired but not running — should not be returned.
				expiredDraft := &models.StackInstance{
					Name: "expired-draft", StackDefinitionID: "d1", Namespace: "ns-exp3",
					OwnerID: "o1", Branch: "main", Status: models.StackStatusDraft,
				}
				require.NoError(t, repo.Create(context.Background(), expiredDraft))
				expiredDraft.ExpiresAt = &past
				require.NoError(t, repo.Update(context.Background(), expiredDraft))

				// Running with no ExpiresAt — should not be returned.
				require.NoError(t, repo.Create(context.Background(), &models.StackInstance{
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

			expired, err := repo.ListExpired(context.Background())
			require.NoError(t, err)
			assert.Len(t, expired, tt.wantCount)
		})
	}
}

func TestGORMStackInstanceRepository_CountByStatuses(t *testing.T) {
	t.Parallel()

	repo := setupStackInstanceRepo(t)
	seed := []struct{ name, status string }{
		{"cbs-run1", models.StackStatusRunning},
		{"cbs-run2", models.StackStatusRunning},
		{"cbs-dep1", models.StackStatusDeploying},
		{"cbs-stop1", models.StackStatusStopped},
	}
	for _, s := range seed {
		require.NoError(t, repo.Create(context.Background(), &models.StackInstance{
			Name: s.name, StackDefinitionID: "d1", Namespace: "ns-" + s.name,
			OwnerID: "o1", Branch: "main", Status: s.status, ClusterID: "c1",
		}))
	}

	tests := []struct {
		name     string
		statuses []string
		want     int
	}{
		{"multiple matching statuses", []string{models.StackStatusRunning, models.StackStatusDeploying}, 3},
		{"single status", []string{models.StackStatusRunning}, 2},
		{"no matching status", []string{models.StackStatusError}, 0},
		{"empty slice short-circuits", nil, 0},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := repo.CountByStatuses(context.Background(), tt.statuses)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGORMStackInstanceRepository_FindByNameOnCluster(t *testing.T) {
	t.Parallel()

	repo := setupStackInstanceRepo(t)
	for _, inst := range []models.StackInstance{
		{Name: "demo", Namespace: "stack-demo-alice", OwnerID: "alice", ClusterID: "c1"},
		{Name: "Demo", Namespace: "stack-demo-bob", OwnerID: "bob", ClusterID: "c2"},
		{Name: "demo", Namespace: "stack-demo-carol", OwnerID: "carol"},
		{Name: "other", Namespace: "stack-other-alice", OwnerID: "alice", ClusterID: "c1"},
	} {
		inst := inst
		inst.StackDefinitionID = "d1"
		inst.Branch = "main"
		inst.Status = models.StackStatusDraft
		require.NoError(t, repo.Create(context.Background(), &inst))
	}

	tests := []struct {
		name              string
		lookup            string
		clusterID         string
		includeUnassigned bool
		wantOwners        []string
	}{
		{name: "same cluster", lookup: "demo", clusterID: "c1", wantOwners: []string{"alice"}},
		{name: "case-insensitive", lookup: "DEMO", clusterID: "c2", wantOwners: []string{"bob"}},
		{name: "default cluster includes unassigned", lookup: "demo", clusterID: "c1", includeUnassigned: true, wantOwners: []string{"alice", "carol"}},
		{name: "other cluster", lookup: "demo", clusterID: "c3", wantOwners: []string{}},
		{name: "no cluster", lookup: "demo", clusterID: "", includeUnassigned: true, wantOwners: []string{"carol"}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := repo.FindByNameOnCluster(context.Background(), tt.lookup, tt.clusterID, tt.includeUnassigned)
			require.NoError(t, err)
			owners := make([]string, 0, len(got))
			for _, inst := range got {
				owners = append(owners, inst.OwnerID)
			}
			assert.ElementsMatch(t, tt.wantOwners, owners)
		})
	}
}

func TestGORMStackInstanceRepository_DuplicateInstanceNames(t *testing.T) {
	t.Parallel()

	repo := setupStackInstanceRepo(t)
	for _, inst := range []models.StackInstance{
		{Name: "demo", Namespace: "stack-demo-alice", OwnerID: "alice", ClusterID: "c1"},
		{Name: "Demo", Namespace: "stack-demo-bob", OwnerID: "bob", ClusterID: "c1"},
		{Name: "demo", Namespace: "stack-demo-carol", OwnerID: "carol", ClusterID: "c2"},
		{Name: "api", Namespace: "stack-api-alice", OwnerID: "alice", ClusterID: "c2"},
		{Name: "api", Namespace: "stack-api-bob", OwnerID: "bob"},
		{Name: "solo", Namespace: "stack-solo-bob", OwnerID: "bob", ClusterID: "c1"},
	} {
		inst := inst
		inst.StackDefinitionID = "d1"
		inst.Branch = "main"
		inst.Status = models.StackStatusDraft
		require.NoError(t, repo.Create(context.Background(), &inst))
	}

	got, err := repo.DuplicateInstanceNames(context.Background(), "c2")
	require.NoError(t, err)
	assert.Equal(t, []models.DuplicateInstanceName{
		{ClusterID: "c1", Name: "demo", Count: 2},
		{ClusterID: "c2", Name: "api", Count: 2},
	}, got)
}

package database

import (
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGORMStackInstanceRepository_ListPagedFilter(t *testing.T) {
	t.Parallel()

	repo := setupStackInstanceRepo(t)
	seed := []models.StackInstance{
		{Name: "a1", StackDefinitionID: "d1", OwnerID: "o1", ClusterID: "c1", Status: models.StackStatusRunning},
		{Name: "a2", StackDefinitionID: "d1", OwnerID: "o1", ClusterID: "c2", Status: models.StackStatusStopped},
		{Name: "a3", StackDefinitionID: "d2", OwnerID: "o2", ClusterID: "c1", Status: models.StackStatusRunning},
		{Name: "a4", StackDefinitionID: "d2", OwnerID: "o2", ClusterID: "c2", Status: models.StackStatusError},
		{Name: "a5", StackDefinitionID: "d1", OwnerID: "o2", ClusterID: "c1", Status: models.StackStatusRunning},
	}
	for i := range seed {
		seed[i].Namespace = "ns-" + seed[i].Name
		seed[i].Branch = "main"
		require.NoError(t, repo.Create(&seed[i]))
	}

	tests := []struct {
		name      string
		filter    models.StackInstanceFilter
		limit     int
		offset    int
		wantNames []string
		wantTotal int
	}{
		{name: "no filter", filter: models.StackInstanceFilter{}, limit: 10, wantNames: []string{"a1", "a2", "a3", "a4", "a5"}, wantTotal: 5},
		{name: "status", filter: models.StackInstanceFilter{Status: models.StackStatusRunning}, limit: 10, wantNames: []string{"a1", "a3", "a5"}, wantTotal: 3},
		{name: "cluster", filter: models.StackInstanceFilter{ClusterID: "c2"}, limit: 10, wantNames: []string{"a2", "a4"}, wantTotal: 2},
		{name: "definition", filter: models.StackInstanceFilter{DefinitionID: "d2"}, limit: 10, wantNames: []string{"a3", "a4"}, wantTotal: 2},
		{name: "owner", filter: models.StackInstanceFilter{OwnerID: "o1"}, limit: 10, wantNames: []string{"a1", "a2"}, wantTotal: 2},
		{name: "name", filter: models.StackInstanceFilter{Name: "a4"}, limit: 10, wantNames: []string{"a4"}, wantTotal: 1},
		{name: "combined", filter: models.StackInstanceFilter{Status: models.StackStatusRunning, ClusterID: "c1", DefinitionID: "d1", OwnerID: "o2"}, limit: 10, wantNames: []string{"a5"}, wantTotal: 1},
		{name: "no match", filter: models.StackInstanceFilter{OwnerID: "nobody"}, limit: 10, wantNames: []string{}, wantTotal: 0},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, total, err := repo.ListPaged(tt.filter, tt.limit, tt.offset)
			require.NoError(t, err)
			assert.Equal(t, tt.wantTotal, total)
			names := make([]string, len(got))
			for i, inst := range got {
				names[i] = inst.Name
			}
			assert.ElementsMatch(t, tt.wantNames, names)
		})
	}

	t.Run("count matches the filter when the page is smaller", func(t *testing.T) {
		t.Parallel()
		page1, total, err := repo.ListPaged(models.StackInstanceFilter{Status: models.StackStatusRunning}, 2, 0)
		require.NoError(t, err)
		assert.Equal(t, 3, total)
		assert.Len(t, page1, 2)
		page2, total2, err := repo.ListPaged(models.StackInstanceFilter{Status: models.StackStatusRunning}, 2, 2)
		require.NoError(t, err)
		assert.Equal(t, 3, total2)
		require.Len(t, page2, 1)
		for _, inst := range append(page1, page2...) {
			assert.Equal(t, models.StackStatusRunning, inst.Status)
		}
	})
}

func TestGORMStackDefinitionRepository_ListPagedFilterAndNames(t *testing.T) {
	t.Parallel()

	repo := setupStackDefinitionRepo(t)
	defs := []models.StackDefinition{
		{ID: "def-1", Name: "alpha", OwnerID: "o1", DefaultBranch: "main"},
		{ID: "def-2", Name: "beta", OwnerID: "o2", DefaultBranch: "main"},
		{ID: "def-3", Name: "beta", OwnerID: "o1", DefaultBranch: "main"},
	}
	for i := range defs {
		require.NoError(t, repo.Create(&defs[i]))
	}

	tests := []struct {
		name      string
		filter    models.StackDefinitionFilter
		wantIDs   []string
		wantTotal int64
	}{
		{name: "no filter", wantIDs: []string{"def-1", "def-2", "def-3"}, wantTotal: 3},
		{name: "owner", filter: models.StackDefinitionFilter{OwnerID: "o1"}, wantIDs: []string{"def-1", "def-3"}, wantTotal: 2},
		{name: "name", filter: models.StackDefinitionFilter{Name: "beta"}, wantIDs: []string{"def-2", "def-3"}, wantTotal: 2},
		{name: "owner and name", filter: models.StackDefinitionFilter{Name: "beta", OwnerID: "o1"}, wantIDs: []string{"def-3"}, wantTotal: 1},
		{name: "no match", filter: models.StackDefinitionFilter{OwnerID: "nobody"}, wantIDs: []string{}, wantTotal: 0},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, total, err := repo.ListPaged(tt.filter, 10, 0)
			require.NoError(t, err)
			assert.Equal(t, tt.wantTotal, total)
			ids := make([]string, len(got))
			for i, d := range got {
				ids[i] = d.ID
			}
			assert.ElementsMatch(t, tt.wantIDs, ids)
		})
	}

	t.Run("names by IDs", func(t *testing.T) {
		t.Parallel()
		names, err := repo.NamesByIDs([]string{"def-1", "def-3", "missing"})
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"def-1": "alpha", "def-3": "beta"}, names)

		empty, err := repo.NamesByIDs(nil)
		require.NoError(t, err)
		assert.Empty(t, empty)
	})
}

func TestGORMClusterRepository_NamesByIDs(t *testing.T) {
	t.Parallel()

	repo := setupClusterRepo(t)
	c1 := &models.Cluster{Name: "first", APIServerURL: "https://c1.example.com", KubeconfigData: "apiVersion: v1"}
	c2 := &models.Cluster{Name: "second", APIServerURL: "https://c2.example.com"}
	require.NoError(t, repo.Create(c1))
	require.NoError(t, repo.Create(c2))

	tests := []struct {
		name string
		ids  []string
		want map[string]string
	}{
		{name: "existing and missing IDs", ids: []string{c1.ID, c2.ID, "missing"}, want: map[string]string{c1.ID: "first", c2.ID: "second"}},
		{name: "no IDs", ids: nil, want: map[string]string{}},
		{name: "only missing IDs", ids: []string{"missing"}, want: map[string]string{}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := repo.NamesByIDs(tt.ids)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("a deleted cluster has no name", func(t *testing.T) {
		t.Parallel()
		c3 := &models.Cluster{Name: "third", APIServerURL: "https://c3.example.com"}
		require.NoError(t, repo.Create(c3))
		require.NoError(t, repo.Delete(c3.ID))
		got, err := repo.NamesByIDs([]string{c3.ID})
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}

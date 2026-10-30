package database

import (
	"context"
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
		require.NoError(t, repo.Create(context.Background(), &seed[i]))
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
			got, total, err := repo.ListPaged(context.Background(), tt.filter, tt.limit, tt.offset)
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
		page1, total, err := repo.ListPaged(context.Background(), models.StackInstanceFilter{Status: models.StackStatusRunning}, 2, 0)
		require.NoError(t, err)
		assert.Equal(t, 3, total)
		assert.Len(t, page1, 2)
		page2, total2, err := repo.ListPaged(context.Background(), models.StackInstanceFilter{Status: models.StackStatusRunning}, 2, 2)
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
		require.NoError(t, repo.Create(context.Background(), &defs[i]))
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
			got, total, err := repo.ListPaged(context.Background(), tt.filter, 10, 0)
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
		names, err := repo.NamesByIDs(context.Background(), []string{"def-1", "def-3", "missing"})
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"def-1": "alpha", "def-3": "beta"}, names)

		empty, err := repo.NamesByIDs(context.Background(), nil)
		require.NoError(t, err)
		assert.Empty(t, empty)
	})
}

func TestGORMClusterRepository_NamesByIDs(t *testing.T) {
	t.Parallel()

	repo := setupClusterRepo(t)
	c1 := &models.Cluster{Name: "first", APIServerURL: "https://c1.example.com", KubeconfigData: "apiVersion: v1"}
	c2 := &models.Cluster{Name: "second", APIServerURL: "https://c2.example.com"}
	require.NoError(t, repo.Create(context.Background(), c1))
	require.NoError(t, repo.Create(context.Background(), c2))

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
			got, err := repo.NamesByIDs(context.Background(), tt.ids)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("a deleted cluster has no name", func(t *testing.T) {
		t.Parallel()
		c3 := &models.Cluster{Name: "third", APIServerURL: "https://c3.example.com"}
		require.NoError(t, repo.Create(context.Background(), c3))
		require.NoError(t, repo.Delete(context.Background(), c3.ID))
		got, err := repo.NamesByIDs(context.Background(), []string{c3.ID})
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}

func TestGORMStackDefinitionRepository_TemplateRefsByIDs(t *testing.T) {
	t.Parallel()

	db := setupTestDBWithAllTables(t)
	defs := NewGORMStackDefinitionRepository(db)
	templates := NewGORMStackTemplateRepository(db)
	ctx := context.Background()

	tpl := &models.StackTemplate{Name: "Team A stack", Category: "team-a", OwnerID: "o1"}
	require.NoError(t, templates.Create(ctx, tpl))
	linked := &models.StackDefinition{Name: "linked", OwnerID: "o1", SourceTemplateID: tpl.ID}
	unlinked := &models.StackDefinition{Name: "unlinked", OwnerID: "o1"}
	orphan := &models.StackDefinition{Name: "orphan", OwnerID: "o1", SourceTemplateID: "deleted-template"}
	for _, d := range []*models.StackDefinition{linked, unlinked, orphan} {
		require.NoError(t, defs.Create(ctx, d))
	}

	tests := []struct {
		name string
		ids  []string
		want map[string]models.DefinitionTemplateRef
	}{
		{
			name: "only definitions with an existing template",
			ids:  []string{linked.ID, unlinked.ID, orphan.ID, "missing"},
			want: map[string]models.DefinitionTemplateRef{
				linked.ID: {TemplateID: tpl.ID, TemplateName: "Team A stack", TemplateCategory: "team-a"},
			},
		},
		{name: "no IDs", ids: nil, want: map[string]models.DefinitionTemplateRef{}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := defs.TemplateRefsByIDs(ctx, tt.ids)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGORMClusterRepository_StreamFlagsByIDs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := setupClusterRepo(t)
	on := &models.Cluster{Name: "on", APIServerURL: "https://on.example.com", AllowPortForward: true}
	off := &models.Cluster{Name: "off", APIServerURL: "https://off.example.com"}
	def := &models.Cluster{Name: "default", APIServerURL: "https://def.example.com", AllowPortForward: true}
	for _, c := range []*models.Cluster{on, off, def} {
		require.NoError(t, repo.Create(ctx, c))
	}
	require.NoError(t, repo.SetDefault(ctx, def.ID))
	// A damaged secret column of an unrelated cluster must not matter: the
	// query reads neither the kubeconfig nor the registry password.
	broken := &models.Cluster{Name: "broken", APIServerURL: "https://broken.example.com", AllowPortForward: true}
	require.NoError(t, repo.Create(ctx, broken))
	require.NoError(t, repo.db.Model(&models.Cluster{}).Where("id = ?", broken.ID).
		Updates(map[string]any{"kubeconfig_data": "QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQQ=="}).Error)
	_, listErr := repo.List(ctx)
	require.Error(t, listErr, "the full read fails on the damaged row")

	tests := []struct {
		name           string
		ids            []string
		includeDefault bool
		want           map[string]bool
	}{
		{"by ID", []string{on.ID, off.ID}, false, map[string]bool{on.ID: true, off.ID: false}},
		{"missing ID is absent", []string{on.ID, "missing"}, false, map[string]bool{on.ID: true}},
		{"no IDs", nil, false, map[string]bool{}},
		{"default under the empty key", []string{off.ID}, true, map[string]bool{off.ID: false, models.DefaultClusterKey: true}},
		{"only the default", nil, true, map[string]bool{models.DefaultClusterKey: true}},
		{"damaged row is readable", []string{broken.ID}, false, map[string]bool{broken.ID: true}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := repo.StreamFlagsByIDs(ctx, tt.ids, tt.includeDefault)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("no default cluster", func(t *testing.T) {
		t.Parallel()
		empty := setupClusterRepo(t)
		got, err := empty.StreamFlagsByIDs(ctx, nil, true)
		require.NoError(t, err)
		assert.Empty(t, got)
	})
	t.Run("more IDs than one query holds", func(t *testing.T) {
		t.Parallel()
		ids := make([]string, 0, clusterIDsPerQuery+10)
		for i := 0; i < clusterIDsPerQuery+9; i++ {
			ids = append(ids, "missing")
		}
		ids = append(ids, on.ID)
		got, err := repo.StreamFlagsByIDs(ctx, ids, false)
		require.NoError(t, err)
		assert.Equal(t, map[string]bool{on.ID: true}, got)
	})
}

func TestGORMStackInstanceRepository_FindByIDs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := setupStackInstanceRepo(t)
	a := &models.StackInstance{Name: "a", StackDefinitionID: "d1", Namespace: "ns-a", OwnerID: "alice", ClusterID: "cl-1", Branch: "main", Status: models.StackStatusRunning}
	b := &models.StackInstance{Name: "b", StackDefinitionID: "d1", Namespace: "ns-b", OwnerID: "bob", Branch: "dev", Status: models.StackStatusStopped}
	require.NoError(t, repo.Create(ctx, a))
	require.NoError(t, repo.Create(ctx, b))

	tests := []struct {
		name string
		ids  []string
		want []string
	}{
		{"existing", []string{a.ID, b.ID}, []string{a.ID, b.ID}},
		{"missing is absent", []string{a.ID, "missing"}, []string{a.ID}},
		{"none", nil, nil},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := repo.FindByIDs(ctx, tt.ids)
			require.NoError(t, err)
			keys := make([]string, 0, len(got))
			for id := range got {
				keys = append(keys, id)
			}
			assert.ElementsMatch(t, tt.want, keys)
		})
	}

	t.Run("projected columns", func(t *testing.T) {
		t.Parallel()
		got, err := repo.FindByIDs(ctx, []string{a.ID})
		require.NoError(t, err)
		g := got[a.ID]
		require.NotNil(t, g)
		assert.Equal(t, "alice", g.OwnerID)
		assert.Equal(t, models.StackStatusRunning, g.Status)
		assert.Equal(t, "ns-a", g.Namespace)
		assert.Equal(t, "cl-1", g.ClusterID)
		assert.Empty(t, g.Name, "columns outside the projection stay empty")
		assert.Empty(t, g.Branch)
	})
	t.Run("more IDs than one query holds", func(t *testing.T) {
		t.Parallel()
		ids := make([]string, 0, instanceIDsPerQuery+10)
		for i := 0; i < instanceIDsPerQuery+9; i++ {
			ids = append(ids, "missing")
		}
		ids = append(ids, b.ID)
		got, err := repo.FindByIDs(ctx, ids)
		require.NoError(t, err)
		assert.Len(t, got, 1)
	})
}

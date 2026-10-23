package database

import (
	"context"
	"reflect"
	"testing"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	otellog "go.opentelemetry.io/otel/log"
	"gorm.io/gorm"
)

// chartConfigRepoCalls has one call of each ChartConfigRepository method
// (#656). The calls work on the seed of seedContextCharts.
var chartConfigRepoCalls = []struct {
	name string
	call func(ctx context.Context, r *GORMChartConfigRepository) error
}{
	{name: "Create", call: func(ctx context.Context, r *GORMChartConfigRepository) error {
		return r.Create(ctx, &models.ChartConfig{ID: "cc-new", StackDefinitionID: "def-1", ChartName: "new"})
	}},
	{name: "FindByID", call: func(ctx context.Context, r *GORMChartConfigRepository) error {
		_, err := r.FindByID(ctx, "cc-1")
		return err
	}},
	{name: "Update", call: func(ctx context.Context, r *GORMChartConfigRepository) error {
		return r.Update(ctx, &models.ChartConfig{ID: "cc-1", StackDefinitionID: "def-1", ChartName: "renamed", DeployOrder: 1})
	}},
	{name: "Delete", call: func(ctx context.Context, r *GORMChartConfigRepository) error {
		return r.Delete(ctx, "cc-2")
	}},
	{name: "ListByDefinition", call: func(ctx context.Context, r *GORMChartConfigRepository) error {
		_, err := r.ListByDefinition(ctx, "def-1")
		return err
	}},
	{name: "CountByDefinitionIDs", call: func(ctx context.Context, r *GORMChartConfigRepository) error {
		_, err := r.CountByDefinitionIDs(ctx, []string{"def-1", "def-2"})
		return err
	}},
	{name: "DeleteByDefinition", call: func(ctx context.Context, r *GORMChartConfigRepository) error {
		return r.DeleteByDefinition(ctx, "def-1")
	}},
}

// branchOverrideRepoCalls has one call of each ChartBranchOverrideRepository
// method (#656). The calls work on the seed of seedContextBranchOverrides.
var branchOverrideRepoCalls = []struct {
	name string
	call func(ctx context.Context, r *GORMChartBranchOverrideRepository) error
}{
	{name: "List", call: func(ctx context.Context, r *GORMChartBranchOverrideRepository) error {
		_, err := r.List(ctx, "si-1")
		return err
	}},
	{name: "Get", call: func(ctx context.Context, r *GORMChartBranchOverrideRepository) error {
		_, err := r.Get(ctx, "si-1", "cc-1")
		return err
	}},
	{name: "Set", call: func(ctx context.Context, r *GORMChartBranchOverrideRepository) error {
		return r.Set(ctx, &models.ChartBranchOverride{StackInstanceID: "si-1", ChartConfigID: "cc-1", Branch: "changed"})
	}},
	{name: "Delete", call: func(ctx context.Context, r *GORMChartBranchOverrideRepository) error {
		return r.Delete(ctx, "si-1", "cc-1")
	}},
	{name: "DeleteByInstance", call: func(ctx context.Context, r *GORMChartBranchOverrideRepository) error {
		return r.DeleteByInstance(ctx, "si-1")
	}},
}

// assertCallsCoverInterface checks that every method of iface takes ctx first
// and has a row in names, so a new method cannot skip the guard tests.
func assertCallsCoverInterface(t *testing.T, iface reflect.Type, names []string) {
	t.Helper()
	ctxType := reflect.TypeOf((*context.Context)(nil)).Elem()
	want := make([]string, 0, iface.NumMethod())
	for i := 0; i < iface.NumMethod(); i++ {
		m := iface.Method(i)
		want = append(want, m.Name)
		if assert.Positive(t, m.Type.NumIn(), m.Name) {
			assert.Equal(t, ctxType, m.Type.In(0), "%s takes ctx first", m.Name)
		}
	}
	assert.ElementsMatch(t, want, names)
}

// TestChartRepoCalls_CoverInterface checks both guard tables against their
// interfaces.
func TestChartRepoCalls_CoverInterface(t *testing.T) {
	t.Parallel()

	t.Run("ChartConfigRepository", func(t *testing.T) {
		t.Parallel()
		names := make([]string, 0, len(chartConfigRepoCalls))
		for _, c := range chartConfigRepoCalls {
			names = append(names, c.name)
		}
		assertCallsCoverInterface(t, reflect.TypeOf((*models.ChartConfigRepository)(nil)).Elem(), names)
	})
	t.Run("ChartBranchOverrideRepository", func(t *testing.T) {
		t.Parallel()
		names := make([]string, 0, len(branchOverrideRepoCalls))
		for _, c := range branchOverrideRepoCalls {
			names = append(names, c.name)
		}
		assertCallsCoverInterface(t, reflect.TypeOf((*models.ChartBranchOverrideRepository)(nil)).Elem(), names)
	})
}

// seedContextCharts stores cc-1 and cc-2 (def-1) and cc-3 (def-2).
func seedContextCharts(t *testing.T, r *GORMChartConfigRepository) {
	t.Helper()
	for _, c := range []*models.ChartConfig{
		{ID: "cc-1", StackDefinitionID: "def-1", ChartName: "web", DeployOrder: 1},
		{ID: "cc-2", StackDefinitionID: "def-1", ChartName: "api", DeployOrder: 2},
		{ID: "cc-3", StackDefinitionID: "def-2", ChartName: "db", DeployOrder: 1},
	} {
		require.NoError(t, r.Create(context.Background(), c))
	}
}

// assertChartSeedUnchanged checks that the seed of seedContextCharts is
// unchanged.
func assertChartSeedUnchanged(t *testing.T, db *gorm.DB) {
	t.Helper()
	var rows []models.ChartConfig
	require.NoError(t, db.Order("id").Find(&rows).Error)
	require.Len(t, rows, 3, "no chart was created or deleted")
	assert.Equal(t, "web", rows[0].ChartName, "cc-1 keeps its name")
	assert.Equal(t, "api", rows[1].ChartName)
	assert.Equal(t, "db", rows[2].ChartName)
}

// seedContextBranchOverrides stores the override of si-1 / cc-1 and of
// si-2 / cc-1.
func seedContextBranchOverrides(t *testing.T, r *GORMChartBranchOverrideRepository) {
	t.Helper()
	for _, o := range []*models.ChartBranchOverride{
		{ID: "bo-1", StackInstanceID: "si-1", ChartConfigID: "cc-1", Branch: "feature"},
		{ID: "bo-2", StackInstanceID: "si-2", ChartConfigID: "cc-1", Branch: "other"},
	} {
		require.NoError(t, r.Set(context.Background(), o))
	}
}

// assertBranchSeedUnchanged checks that the seed of
// seedContextBranchOverrides is unchanged.
func assertBranchSeedUnchanged(t *testing.T, db *gorm.DB) {
	t.Helper()
	var rows []models.ChartBranchOverride
	require.NoError(t, db.Order("id").Find(&rows).Error)
	require.Len(t, rows, 2, "no override was created or deleted")
	assert.Equal(t, "feature", rows[0].Branch, "the override of si-1 keeps its branch")
	assert.Equal(t, "other", rows[1].Branch)
}

// TestGORMChartConfigRepository_CancelledContext checks that every method runs
// its queries with the caller context: a cancelled context fails the call
// with context.Canceled and changes no row.
func TestGORMChartConfigRepository_CancelledContext(t *testing.T) {
	t.Parallel()

	for _, tt := range chartConfigRepoCalls {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupChartConfigRepo(t)
			seedContextCharts(t, repo)

			err := tt.call(cancelledContext(), repo)

			require.Error(t, err)
			assert.ErrorIs(t, err, context.Canceled)
			var dbErr *dberrors.DatabaseError
			assert.ErrorAs(t, err, &dbErr)
			assertChartSeedUnchanged(t, repo.db)
		})
	}
}

// TestGORMChartConfigRepository_LiveContext checks the methods with a live
// context, so the guard table above does not pass on calls that always fail.
func TestGORMChartConfigRepository_LiveContext(t *testing.T) {
	t.Parallel()

	for _, tt := range chartConfigRepoCalls {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupChartConfigRepo(t)
			seedContextCharts(t, repo)

			require.NoError(t, tt.call(context.Background(), repo))
		})
	}
}

// TestGORMChartBranchOverrideRepository_CancelledContext checks that every
// method runs its queries with the caller context: a cancelled context fails
// the call with context.Canceled and changes no row.
func TestGORMChartBranchOverrideRepository_CancelledContext(t *testing.T) {
	t.Parallel()

	for _, tt := range branchOverrideRepoCalls {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupChartBranchOverrideRepo(t)
			seedContextBranchOverrides(t, repo)

			err := tt.call(cancelledContext(), repo)

			require.Error(t, err)
			assert.ErrorIs(t, err, context.Canceled)
			var dbErr *dberrors.DatabaseError
			assert.ErrorAs(t, err, &dbErr)
			assertBranchSeedUnchanged(t, repo.db)
		})
	}
}

// TestGORMChartBranchOverrideRepository_LiveContext checks the methods with a
// live context.
func TestGORMChartBranchOverrideRepository_LiveContext(t *testing.T) {
	t.Parallel()

	for _, tt := range branchOverrideRepoCalls {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupChartBranchOverrideRepo(t)
			seedContextBranchOverrides(t, repo)

			require.NoError(t, tt.call(context.Background(), repo))
		})
	}
}

// TestCleanupOwnedDefinition_ChartDeleteTraceContext checks that the chart
// delete of CleanupOwnedDefinition runs with the caller context: the chart
// table is missing, so the chart delete is the statement that fails.
func TestCleanupOwnedDefinition_ChartDeleteTraceContext(t *testing.T) {
	t.Parallel()
	db, records := newTraceTestDB(t, &models.StackDefinition{}, &models.StackInstance{})
	require.NoError(t, NewGORMStackDefinitionRepository(db).Create(context.Background(),
		&models.StackDefinition{ID: "d1", Name: "qd", OwnerID: "o1", OwnerInstanceID: "inst-1"}))
	ctx, traceID, spanID := newTraceTestSpanContext(t)
	inst := &models.StackInstance{ID: "inst-1", StackDefinitionID: "d1"}

	err := NewGORMTxRunner(db).RunInTx(ctx, func(repos TxRepos) error {
		return CleanupOwnedDefinition(ctx, repos, inst)
	})

	require.ErrorContains(t, err, "deleting charts")
	recs := records()
	require.NotEmpty(t, recs)
	for _, r := range recs {
		assert.Equal(t, "SQL error", r.Body().AsString())
		assert.Equal(t, traceID, r.TraceID())
		assert.Equal(t, spanID, r.SpanID())
	}
}

// TestChartRepositories_LogTraceContext checks that the SQL log lines of
// every method of both repositories carry the trace and span ID of the
// caller context, for a failed and for a cancelled query.
func TestChartRepositories_LogTraceContext(t *testing.T) {
	t.Parallel()

	type traceCase struct {
		call  func(ctx context.Context, db *gorm.DB) error
		seed  func(t *testing.T, db *gorm.DB)
		table any
		name  string
	}
	var cases []traceCase
	for _, c := range chartConfigRepoCalls {
		c := c
		cases = append(cases, traceCase{
			name:  "ChartConfig." + c.name,
			table: &models.ChartConfig{},
			call:  func(ctx context.Context, db *gorm.DB) error { return c.call(ctx, NewGORMChartConfigRepository(db)) },
			seed:  func(t *testing.T, db *gorm.DB) { seedContextCharts(t, NewGORMChartConfigRepository(db)) },
		})
	}
	for _, c := range branchOverrideRepoCalls {
		c := c
		cases = append(cases, traceCase{
			name:  "ChartBranchOverride." + c.name,
			table: &models.ChartBranchOverride{},
			call: func(ctx context.Context, db *gorm.DB) error {
				return c.call(ctx, NewGORMChartBranchOverrideRepository(db))
			},
			seed: func(t *testing.T, db *gorm.DB) {
				seedContextBranchOverrides(t, NewGORMChartBranchOverrideRepository(db))
			},
		})
	}

	for _, tt := range cases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			t.Run("SQL error", func(t *testing.T) {
				t.Parallel()
				// No tables: every query fails.
				db, records := newTraceTestDB(t)
				ctx, traceID, spanID := newTraceTestSpanContext(t)

				require.Error(t, tt.call(ctx, db))

				recs := records()
				require.NotEmpty(t, recs)
				for _, r := range recs {
					assert.Equal(t, "SQL error", r.Body().AsString())
					assert.Equal(t, otellog.SeverityError, r.Severity())
					assert.Equal(t, traceID, r.TraceID())
					assert.Equal(t, spanID, r.SpanID())
				}
			})

			t.Run("cancelled query", func(t *testing.T) {
				t.Parallel()
				db, records := newTraceTestDB(t, tt.table)
				tt.seed(t, db)
				spanCtx, traceID, spanID := newTraceTestSpanContext(t)
				ctx, cancel := context.WithCancel(spanCtx)
				defer cancel()
				cancelBeforeStatements(t, db, cancel)

				require.ErrorIs(t, tt.call(ctx, db), context.Canceled)

				recs := records()
				require.NotEmpty(t, recs)
				for _, r := range recs {
					assert.Equal(t, "SQL query cancelled", r.Body().AsString())
					assert.Equal(t, otellog.SeverityDebug, r.Severity())
					assert.Equal(t, traceID, r.TraceID())
					assert.Equal(t, spanID, r.SpanID())
				}
			})
		})
	}
}

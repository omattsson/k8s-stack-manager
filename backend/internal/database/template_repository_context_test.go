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
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"
)

// stackTemplateRepoCalls has one call of each StackTemplateRepository method
// (#656). The calls work on the seed of seedContextTemplates.
var stackTemplateRepoCalls = []struct {
	name string
	call func(ctx context.Context, r models.StackTemplateRepository) error
}{
	{name: "Create", call: func(ctx context.Context, r models.StackTemplateRepository) error {
		return r.Create(ctx, &models.StackTemplate{ID: "t-new", Name: "new", OwnerID: "o1", DefaultBranch: "main"})
	}},
	{name: "FindByID", call: func(ctx context.Context, r models.StackTemplateRepository) error {
		_, err := r.FindByID(ctx, "t-1")
		return err
	}},
	{name: "FindByIDForUpdate", call: func(ctx context.Context, r models.StackTemplateRepository) error {
		_, err := r.FindByIDForUpdate(ctx, "t-1")
		return err
	}},
	{name: "Update", call: func(ctx context.Context, r models.StackTemplateRepository) error {
		return r.Update(ctx, &models.StackTemplate{ID: "t-1", Name: "renamed", OwnerID: "o1", DefaultBranch: "main"})
	}},
	{name: "Delete", call: func(ctx context.Context, r models.StackTemplateRepository) error {
		return r.Delete(ctx, "t-2")
	}},
	{name: "List", call: func(ctx context.Context, r models.StackTemplateRepository) error {
		_, err := r.List(ctx)
		return err
	}},
	{name: "ListPaged", call: func(ctx context.Context, r models.StackTemplateRepository) error {
		_, _, err := r.ListPaged(ctx, 10, 0, "")
		return err
	}},
	{name: "ListPublished", call: func(ctx context.Context, r models.StackTemplateRepository) error {
		_, err := r.ListPublished(ctx)
		return err
	}},
	{name: "ListPublishedPaged", call: func(ctx context.Context, r models.StackTemplateRepository) error {
		_, _, err := r.ListPublishedPaged(ctx, 10, 0, "")
		return err
	}},
	{name: "ListByOwner", call: func(ctx context.Context, r models.StackTemplateRepository) error {
		_, err := r.ListByOwner(ctx, "o1")
		return err
	}},
	{name: "Count", call: func(ctx context.Context, r models.StackTemplateRepository) error {
		_, err := r.Count(ctx)
		return err
	}},
}

// templateChartRepoCalls has one call of each TemplateChartConfigRepository
// method (#656). The calls work on the seed of seedContextTemplateCharts.
var templateChartRepoCalls = []struct {
	name string
	call func(ctx context.Context, r models.TemplateChartConfigRepository) error
}{
	{name: "Create", call: func(ctx context.Context, r models.TemplateChartConfigRepository) error {
		return r.Create(ctx, &models.TemplateChartConfig{ID: "tc-new", StackTemplateID: "t-1", ChartName: "new"})
	}},
	{name: "FindByID", call: func(ctx context.Context, r models.TemplateChartConfigRepository) error {
		_, err := r.FindByID(ctx, "tc-1")
		return err
	}},
	{name: "Update", call: func(ctx context.Context, r models.TemplateChartConfigRepository) error {
		return r.Update(ctx, &models.TemplateChartConfig{ID: "tc-1", StackTemplateID: "t-1", ChartName: "renamed"})
	}},
	{name: "Delete", call: func(ctx context.Context, r models.TemplateChartConfigRepository) error {
		return r.Delete(ctx, "tc-2")
	}},
	{name: "ListByTemplate", call: func(ctx context.Context, r models.TemplateChartConfigRepository) error {
		_, err := r.ListByTemplate(ctx, "t-1")
		return err
	}},
}

// TestTemplateRepoCalls_CoverInterface checks both guard tables against their
// interfaces.
func TestTemplateRepoCalls_CoverInterface(t *testing.T) {
	t.Parallel()

	t.Run("StackTemplateRepository", func(t *testing.T) {
		t.Parallel()
		names := make([]string, 0, len(stackTemplateRepoCalls))
		for _, c := range stackTemplateRepoCalls {
			names = append(names, c.name)
		}
		assertCallsCoverInterface(t, reflect.TypeOf((*models.StackTemplateRepository)(nil)).Elem(), names)
	})
	t.Run("TemplateChartConfigRepository", func(t *testing.T) {
		t.Parallel()
		names := make([]string, 0, len(templateChartRepoCalls))
		for _, c := range templateChartRepoCalls {
			names = append(names, c.name)
		}
		assertCallsCoverInterface(t, reflect.TypeOf((*models.TemplateChartConfigRepository)(nil)).Elem(), names)
	})
}

// seedContextTemplates stores the published template t-1 and the draft
// templates t-2 and t-3 (owner o1).
func seedContextTemplates(t *testing.T, r *GORMStackTemplateRepository) {
	t.Helper()
	for _, tmpl := range []*models.StackTemplate{
		{ID: "t-1", Name: "base", OwnerID: "o1", DefaultBranch: "main", IsPublished: true},
		{ID: "t-2", Name: "draft", OwnerID: "o1", DefaultBranch: "main"},
		{ID: "t-3", Name: "other", OwnerID: "o1", DefaultBranch: "main"},
	} {
		require.NoError(t, r.Create(context.Background(), tmpl))
	}
}

// assertTemplateSeedUnchanged checks that the seed of seedContextTemplates is
// unchanged.
func assertTemplateSeedUnchanged(t *testing.T, db *gorm.DB) {
	t.Helper()
	var rows []models.StackTemplate
	require.NoError(t, db.Order("id").Find(&rows).Error)
	require.Len(t, rows, 3, "no template was created or deleted")
	assert.Equal(t, "base", rows[0].Name, "t-1 keeps its name")
	assert.Equal(t, "draft", rows[1].Name)
	assert.Equal(t, "other", rows[2].Name)
}

// seedContextTemplateCharts stores tc-1 and tc-2 (t-1) and tc-3 (t-2).
func seedContextTemplateCharts(t *testing.T, r *GORMTemplateChartConfigRepository) {
	t.Helper()
	for _, c := range []*models.TemplateChartConfig{
		{ID: "tc-1", StackTemplateID: "t-1", ChartName: "web", DeployOrder: 1},
		{ID: "tc-2", StackTemplateID: "t-1", ChartName: "db", DeployOrder: 2},
		{ID: "tc-3", StackTemplateID: "t-2", ChartName: "cache"},
	} {
		require.NoError(t, r.Create(context.Background(), c))
	}
}

// assertTemplateChartSeedUnchanged checks that the seed of
// seedContextTemplateCharts is unchanged.
func assertTemplateChartSeedUnchanged(t *testing.T, db *gorm.DB) {
	t.Helper()
	var rows []models.TemplateChartConfig
	require.NoError(t, db.Order("id").Find(&rows).Error)
	require.Len(t, rows, 3, "no chart was created or deleted")
	assert.Equal(t, "web", rows[0].ChartName, "tc-1 keeps its name")
	assert.Equal(t, "db", rows[1].ChartName)
	assert.Equal(t, "cache", rows[2].ChartName)
}

// TestGORMStackTemplateRepository_CancelledContext checks that every method
// runs its queries with the caller context: a cancelled context fails the
// call with context.Canceled and changes no row.
func TestGORMStackTemplateRepository_CancelledContext(t *testing.T) {
	t.Parallel()

	for _, tt := range stackTemplateRepoCalls {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupStackTemplateRepo(t)
			seedContextTemplates(t, repo)

			err := tt.call(cancelledContext(), repo)

			require.Error(t, err)
			assert.ErrorIs(t, err, context.Canceled)
			var dbErr *dberrors.DatabaseError
			assert.ErrorAs(t, err, &dbErr)
			assertTemplateSeedUnchanged(t, repo.db)
		})
	}
}

// TestGORMStackTemplateRepository_LiveContext checks the methods with a live
// context, so the guard table above does not pass on calls that always fail.
func TestGORMStackTemplateRepository_LiveContext(t *testing.T) {
	t.Parallel()

	for _, tt := range stackTemplateRepoCalls {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupStackTemplateRepo(t)
			seedContextTemplates(t, repo)

			require.NoError(t, tt.call(context.Background(), repo))
		})
	}
}

// TestGORMTemplateChartConfigRepository_CancelledContext checks that every
// method runs its queries with the caller context: a cancelled context fails
// the call with context.Canceled and changes no row.
func TestGORMTemplateChartConfigRepository_CancelledContext(t *testing.T) {
	t.Parallel()

	for _, tt := range templateChartRepoCalls {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupTemplateChartConfigRepo(t)
			seedContextTemplateCharts(t, repo)

			err := tt.call(cancelledContext(), repo)

			require.Error(t, err)
			assert.ErrorIs(t, err, context.Canceled)
			var dbErr *dberrors.DatabaseError
			assert.ErrorAs(t, err, &dbErr)
			assertTemplateChartSeedUnchanged(t, repo.DB)
		})
	}
}

// TestGORMTemplateChartConfigRepository_LiveContext checks the methods with a
// live context.
func TestGORMTemplateChartConfigRepository_LiveContext(t *testing.T) {
	t.Parallel()

	for _, tt := range templateChartRepoCalls {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupTemplateChartConfigRepo(t)
			seedContextTemplateCharts(t, repo)

			require.NoError(t, tt.call(context.Background(), repo))
		})
	}
}

// newOtherSpanContext returns a context with the trace ID traceID and a span
// ID that differs from other.
func newOtherSpanContext(t *testing.T, traceID trace.TraceID, other trace.SpanID) (context.Context, trace.SpanID) {
	t.Helper()
	spanID, err := trace.SpanIDFromHex("1112131415161718")
	require.NoError(t, err)
	require.NotEqual(t, other, spanID)
	return trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled,
	})), spanID
}

// TestTemplateRepositories_LogTraceContext checks that the SQL log lines of
// every method of both repositories carry the trace and span ID of the
// caller context, for a failed and for a cancelled query, and inside a
// transaction that carries another span.
func TestTemplateRepositories_LogTraceContext(t *testing.T) {
	t.Parallel()

	type traceCase struct {
		call  func(ctx context.Context, repos TxRepos) error
		seed  func(t *testing.T, db *gorm.DB)
		table any
		name  string
	}
	var cases []traceCase
	for _, c := range stackTemplateRepoCalls {
		c := c
		cases = append(cases, traceCase{
			name:  "StackTemplate." + c.name,
			table: &models.StackTemplate{},
			call:  func(ctx context.Context, repos TxRepos) error { return c.call(ctx, repos.StackTemplate) },
			seed: func(t *testing.T, db *gorm.DB) {
				seedContextTemplates(t, NewGORMStackTemplateRepository(db))
			},
		})
	}
	for _, c := range templateChartRepoCalls {
		c := c
		cases = append(cases, traceCase{
			name:  "TemplateChart." + c.name,
			table: &models.TemplateChartConfig{},
			call:  func(ctx context.Context, repos TxRepos) error { return c.call(ctx, repos.TemplateChart) },
			seed: func(t *testing.T, db *gorm.DB) {
				seedContextTemplateCharts(t, NewGORMTemplateChartConfigRepository(db))
			},
		})
	}
	direct := func(db *gorm.DB) TxRepos {
		return TxRepos{StackTemplate: NewGORMStackTemplateRepository(db), TemplateChart: NewGORMTemplateChartConfigRepository(db)}
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

				require.Error(t, tt.call(ctx, direct(db)))

				recs := records()
				require.NotEmpty(t, recs)
				for _, r := range recs {
					assert.Equal(t, "SQL error", r.Body().AsString())
					assert.Equal(t, otellog.SeverityError, r.Severity())
					assert.Equal(t, traceID, r.TraceID())
					assert.Equal(t, spanID, r.SpanID())
				}
			})

			t.Run("SQL error in a transaction", func(t *testing.T) {
				t.Parallel()
				db, records := newTraceTestDB(t)
				// The tx carries txCtx; a different span proves that the repository uses the passed ctx.
				txCtx, traceID, txSpanID := newTraceTestSpanContext(t)
				ctx, spanID := newOtherSpanContext(t, traceID, txSpanID)

				err := NewGORMTxRunner(db).RunInTx(txCtx, func(repos TxRepos) error {
					return tt.call(ctx, repos)
				})

				require.Error(t, err)
				recs := records()
				require.NotEmpty(t, recs)
				for _, r := range recs {
					assert.Equal(t, "SQL error", r.Body().AsString())
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

				require.ErrorIs(t, tt.call(ctx, direct(db)), context.Canceled)

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

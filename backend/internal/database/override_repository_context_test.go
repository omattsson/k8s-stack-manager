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

// valueOverrideRepoCalls has one call of each ValueOverrideRepository method
// (#656). The calls work on the seed of seedContextValueOverrides.
var valueOverrideRepoCalls = []struct {
	name string
	call func(ctx context.Context, r *GORMValueOverrideRepository) error
}{
	{name: "Create", call: func(ctx context.Context, r *GORMValueOverrideRepository) error {
		return r.Create(ctx, &models.ValueOverride{ID: "vo-new", StackInstanceID: "si-1", ChartConfigID: "cc-new", Values: "n: 1"})
	}},
	{name: "FindByID", call: func(ctx context.Context, r *GORMValueOverrideRepository) error {
		_, err := r.FindByID(ctx, "vo-1")
		return err
	}},
	{name: "FindByInstanceAndChart", call: func(ctx context.Context, r *GORMValueOverrideRepository) error {
		_, err := r.FindByInstanceAndChart(ctx, "si-1", "cc-1")
		return err
	}},
	{name: "Update", call: func(ctx context.Context, r *GORMValueOverrideRepository) error {
		return r.Update(ctx, &models.ValueOverride{ID: "vo-1", StackInstanceID: "si-1", ChartConfigID: "cc-1", Values: "changed: 1"})
	}},
	{name: "Delete", call: func(ctx context.Context, r *GORMValueOverrideRepository) error {
		return r.Delete(ctx, "vo-2")
	}},
	{name: "ListByInstance", call: func(ctx context.Context, r *GORMValueOverrideRepository) error {
		_, err := r.ListByInstance(ctx, "si-1")
		return err
	}},
	{name: "DeleteByInstance", call: func(ctx context.Context, r *GORMValueOverrideRepository) error {
		return r.DeleteByInstance(ctx, "si-1")
	}},
}

// sharedValuesRepoCalls has one call of each SharedValuesRepository method
// (#656). The calls work on the seed of seedContextSharedValues.
var sharedValuesRepoCalls = []struct {
	name string
	call func(ctx context.Context, r *GORMSharedValuesRepository) error
}{
	{name: "Create", call: func(ctx context.Context, r *GORMSharedValuesRepository) error {
		return r.Create(ctx, &models.SharedValues{ID: "sv-new", ClusterID: "c-1", Name: "new"})
	}},
	{name: "FindByID", call: func(ctx context.Context, r *GORMSharedValuesRepository) error {
		_, err := r.FindByID(ctx, "sv-1")
		return err
	}},
	{name: "FindByClusterAndID", call: func(ctx context.Context, r *GORMSharedValuesRepository) error {
		_, err := r.FindByClusterAndID(ctx, "c-1", "sv-1")
		return err
	}},
	{name: "Update", call: func(ctx context.Context, r *GORMSharedValuesRepository) error {
		return r.Update(ctx, &models.SharedValues{ID: "sv-1", ClusterID: "c-1", Name: "renamed", Values: "a: 1"})
	}},
	{name: "Delete", call: func(ctx context.Context, r *GORMSharedValuesRepository) error {
		return r.Delete(ctx, "sv-2")
	}},
	{name: "ListByCluster", call: func(ctx context.Context, r *GORMSharedValuesRepository) error {
		_, err := r.ListByCluster(ctx, "c-1")
		return err
	}},
}

// TestOverrideRepoCalls_CoverInterface checks both guard tables against their
// interfaces.
func TestOverrideRepoCalls_CoverInterface(t *testing.T) {
	t.Parallel()

	t.Run("ValueOverrideRepository", func(t *testing.T) {
		t.Parallel()
		names := make([]string, 0, len(valueOverrideRepoCalls))
		for _, c := range valueOverrideRepoCalls {
			names = append(names, c.name)
		}
		assertCallsCoverInterface(t, reflect.TypeOf((*models.ValueOverrideRepository)(nil)).Elem(), names)
	})
	t.Run("SharedValuesRepository", func(t *testing.T) {
		t.Parallel()
		names := make([]string, 0, len(sharedValuesRepoCalls))
		for _, c := range sharedValuesRepoCalls {
			names = append(names, c.name)
		}
		assertCallsCoverInterface(t, reflect.TypeOf((*models.SharedValuesRepository)(nil)).Elem(), names)
	})
}

// seedContextValueOverrides stores vo-1 (si-1 / cc-1), vo-2 (si-1 / cc-2) and
// vo-3 (si-2 / cc-1).
func seedContextValueOverrides(t *testing.T, r *GORMValueOverrideRepository) {
	t.Helper()
	for _, o := range []*models.ValueOverride{
		{ID: "vo-1", StackInstanceID: "si-1", ChartConfigID: "cc-1", Values: "a: 1"},
		{ID: "vo-2", StackInstanceID: "si-1", ChartConfigID: "cc-2", Values: "b: 2"},
		{ID: "vo-3", StackInstanceID: "si-2", ChartConfigID: "cc-1", Values: "c: 3"},
	} {
		require.NoError(t, r.Create(context.Background(), o))
	}
}

// assertValueOverrideSeedUnchanged checks that the seed of
// seedContextValueOverrides is unchanged.
func assertValueOverrideSeedUnchanged(t *testing.T, db *gorm.DB) {
	t.Helper()
	var rows []models.ValueOverride
	require.NoError(t, db.Order("id").Find(&rows).Error)
	require.Len(t, rows, 3, "no override was created or deleted")
	assert.Equal(t, "a: 1", rows[0].Values, "vo-1 keeps its values")
	assert.Equal(t, "b: 2", rows[1].Values)
	assert.Equal(t, "c: 3", rows[2].Values)
}

// seedContextSharedValues stores sv-1 and sv-2 (c-1) and sv-3 (c-2).
func seedContextSharedValues(t *testing.T, r *GORMSharedValuesRepository) {
	t.Helper()
	for _, sv := range []*models.SharedValues{
		{ID: "sv-1", ClusterID: "c-1", Name: "base", Values: "a: 1"},
		{ID: "sv-2", ClusterID: "c-1", Name: "extra", Values: "b: 2", Priority: 1},
		{ID: "sv-3", ClusterID: "c-2", Name: "other", Values: "c: 3"},
	} {
		require.NoError(t, r.Create(context.Background(), sv))
	}
}

// assertSharedValuesSeedUnchanged checks that the seed of
// seedContextSharedValues is unchanged.
func assertSharedValuesSeedUnchanged(t *testing.T, db *gorm.DB) {
	t.Helper()
	var rows []models.SharedValues
	require.NoError(t, db.Order("id").Find(&rows).Error)
	require.Len(t, rows, 3, "no shared values were created or deleted")
	assert.Equal(t, "base", rows[0].Name, "sv-1 keeps its name")
	assert.Equal(t, "extra", rows[1].Name)
	assert.Equal(t, "other", rows[2].Name)
}

// TestGORMValueOverrideRepository_CancelledContext checks that every method
// runs its queries with the caller context: a cancelled context fails the
// call with context.Canceled and changes no row.
func TestGORMValueOverrideRepository_CancelledContext(t *testing.T) {
	t.Parallel()

	for _, tt := range valueOverrideRepoCalls {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupValueOverrideRepo(t)
			seedContextValueOverrides(t, repo)

			err := tt.call(cancelledContext(), repo)

			require.Error(t, err)
			assert.ErrorIs(t, err, context.Canceled)
			var dbErr *dberrors.DatabaseError
			assert.ErrorAs(t, err, &dbErr)
			assertValueOverrideSeedUnchanged(t, repo.DB)
		})
	}
}

// TestGORMValueOverrideRepository_LiveContext checks the methods with a live
// context, so the guard table above does not pass on calls that always fail.
func TestGORMValueOverrideRepository_LiveContext(t *testing.T) {
	t.Parallel()

	for _, tt := range valueOverrideRepoCalls {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupValueOverrideRepo(t)
			seedContextValueOverrides(t, repo)

			require.NoError(t, tt.call(context.Background(), repo))
		})
	}
}

// TestGORMSharedValuesRepository_CancelledContext checks that every method
// runs its queries with the caller context: a cancelled context fails the
// call with context.Canceled and changes no row.
func TestGORMSharedValuesRepository_CancelledContext(t *testing.T) {
	t.Parallel()

	for _, tt := range sharedValuesRepoCalls {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupSharedValuesRepo(t)
			seedContextSharedValues(t, repo)

			err := tt.call(cancelledContext(), repo)

			require.Error(t, err)
			assert.ErrorIs(t, err, context.Canceled)
			var dbErr *dberrors.DatabaseError
			assert.ErrorAs(t, err, &dbErr)
			assertSharedValuesSeedUnchanged(t, repo.DB)
		})
	}
}

// TestGORMSharedValuesRepository_LiveContext checks the methods with a live
// context.
func TestGORMSharedValuesRepository_LiveContext(t *testing.T) {
	t.Parallel()

	for _, tt := range sharedValuesRepoCalls {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupSharedValuesRepo(t)
			seedContextSharedValues(t, repo)

			require.NoError(t, tt.call(context.Background(), repo))
		})
	}
}

// TestDeleteInstanceRecord_ValueOverrideDeleteTraceContext checks that the
// value override delete of DeleteInstanceRecord runs with the caller context:
// the value override table is missing, so that delete is the statement that
// fails.
func TestDeleteInstanceRecord_ValueOverrideDeleteTraceContext(t *testing.T) {
	t.Parallel()
	db, records := newTraceTestDB(t, &models.ChartBranchOverride{})
	ctx, traceID, spanID := newTraceTestSpanContext(t)
	inst := &models.StackInstance{ID: "inst-1"}

	err := NewGORMTxRunner(db).RunInTx(ctx, func(repos TxRepos) error {
		return DeleteInstanceRecord(ctx, repos, inst)
	})

	require.Error(t, err)
	recs := records()
	require.NotEmpty(t, recs)
	for _, r := range recs {
		assert.Equal(t, "SQL error", r.Body().AsString())
		assert.Equal(t, traceID, r.TraceID())
		assert.Equal(t, spanID, r.SpanID())
	}
}

// TestOverrideRepositories_LogTraceContext checks that the SQL log lines of
// every method of both repositories carry the trace and span ID of the
// caller context, for a failed and for a cancelled query.
func TestOverrideRepositories_LogTraceContext(t *testing.T) {
	t.Parallel()

	type traceCase struct {
		call  func(ctx context.Context, db *gorm.DB) error
		seed  func(t *testing.T, db *gorm.DB)
		table any
		name  string
	}
	var cases []traceCase
	for _, c := range valueOverrideRepoCalls {
		c := c
		cases = append(cases, traceCase{
			name:  "ValueOverride." + c.name,
			table: &models.ValueOverride{},
			call: func(ctx context.Context, db *gorm.DB) error {
				return c.call(ctx, NewGORMValueOverrideRepository(db))
			},
			seed: func(t *testing.T, db *gorm.DB) {
				seedContextValueOverrides(t, NewGORMValueOverrideRepository(db))
			},
		})
	}
	for _, c := range sharedValuesRepoCalls {
		c := c
		cases = append(cases, traceCase{
			name:  "SharedValues." + c.name,
			table: &models.SharedValues{},
			call: func(ctx context.Context, db *gorm.DB) error {
				return c.call(ctx, NewGORMSharedValuesRepository(db))
			},
			seed: func(t *testing.T, db *gorm.DB) {
				seedContextSharedValues(t, NewGORMSharedValuesRepository(db))
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

package database

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	otellog "go.opentelemetry.io/otel/log"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// definitionRepoCalls has one call of each StackDefinitionRepository method
// (#656). The calls work on the seed of seedContextDefinitions.
var definitionRepoCalls = []struct {
	name string
	call func(ctx context.Context, r *GORMStackDefinitionRepository) error
}{
	{name: "Create", call: func(ctx context.Context, r *GORMStackDefinitionRepository) error {
		return r.Create(ctx, &models.StackDefinition{ID: "d-new", Name: "new", OwnerID: "o1", DefaultBranch: "main"})
	}},
	{name: "FindByID", call: func(ctx context.Context, r *GORMStackDefinitionRepository) error {
		_, err := r.FindByID(ctx, "d1")
		return err
	}},
	{name: "FindByIDForUpdate", call: func(ctx context.Context, r *GORMStackDefinitionRepository) error {
		_, err := r.FindByIDForUpdate(ctx, "d1")
		return err
	}},
	{name: "FindByName", call: func(ctx context.Context, r *GORMStackDefinitionRepository) error {
		_, err := r.FindByName(ctx, "one")
		return err
	}},
	{name: "Update", call: func(ctx context.Context, r *GORMStackDefinitionRepository) error {
		return r.Update(ctx, &models.StackDefinition{ID: "d1", Name: "renamed", Description: "changed", OwnerID: "o1", DefaultBranch: "main"})
	}},
	{name: "SetTemplateLink", call: func(ctx context.Context, r *GORMStackDefinitionRepository) error {
		return r.SetTemplateLink(ctx, "d1", "t2", "2.0")
	}},
	{name: "Delete", call: func(ctx context.Context, r *GORMStackDefinitionRepository) error {
		return r.Delete(ctx, "d1")
	}},
	{name: "List", call: func(ctx context.Context, r *GORMStackDefinitionRepository) error {
		_, err := r.List(ctx)
		return err
	}},
	{name: "ListPaged", call: func(ctx context.Context, r *GORMStackDefinitionRepository) error {
		_, _, err := r.ListPaged(ctx, models.StackDefinitionFilter{OwnerID: "o1"}, 10, 0)
		return err
	}},
	{name: "NamesByIDs", call: func(ctx context.Context, r *GORMStackDefinitionRepository) error {
		_, err := r.NamesByIDs(ctx, []string{"d1", "d2"})
		return err
	}},
	{name: "TemplateRefsByIDs", call: func(ctx context.Context, r *GORMStackDefinitionRepository) error {
		_, err := r.TemplateRefsByIDs(ctx, []string{"d1", "d2"})
		return err
	}},
	{name: "ListByOwner", call: func(ctx context.Context, r *GORMStackDefinitionRepository) error {
		_, err := r.ListByOwner(ctx, "o1")
		return err
	}},
	{name: "ListByTemplate", call: func(ctx context.Context, r *GORMStackDefinitionRepository) error {
		_, err := r.ListByTemplate(ctx, "t1")
		return err
	}},
	{name: "CountByTemplateIDs", call: func(ctx context.Context, r *GORMStackDefinitionRepository) error {
		_, err := r.CountByTemplateIDs(ctx, []string{"t1"})
		return err
	}},
	{name: "ListIDsByTemplateIDs", call: func(ctx context.Context, r *GORMStackDefinitionRepository) error {
		_, err := r.ListIDsByTemplateIDs(ctx, []string{"t1"})
		return err
	}},
	{name: "Count", call: func(ctx context.Context, r *GORMStackDefinitionRepository) error {
		_, err := r.Count(ctx)
		return err
	}},
}

// seedContextDefinitions stores d1 (owner o1, linked to t1 1.0) and d2
// (owner o2, linked to t1 1.0).
func seedContextDefinitions(t *testing.T, r *GORMStackDefinitionRepository) {
	t.Helper()
	for _, def := range []*models.StackDefinition{
		{ID: "d1", Name: "one", Description: "seed", OwnerID: "o1", SourceTemplateID: "t1", SourceTemplateVersion: "1.0", DefaultBranch: "main"},
		{ID: "d2", Name: "two", Description: "seed", OwnerID: "o2", SourceTemplateID: "t1", SourceTemplateVersion: "1.0", DefaultBranch: "main"},
	} {
		require.NoError(t, r.Create(context.Background(), def))
	}
}

// assertDefinitionSeedUnchanged checks that the seed of seedContextDefinitions
// is unchanged.
func assertDefinitionSeedUnchanged(t *testing.T, r *GORMStackDefinitionRepository) {
	t.Helper()
	n, err := r.Count(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(2), n, "no definition was created or deleted")
	one, err := r.FindByID(context.Background(), "d1")
	require.NoError(t, err)
	assert.Equal(t, "one", one.Name)
	assert.Equal(t, "seed", one.Description)
	assert.Equal(t, "t1", one.SourceTemplateID)
	assert.Equal(t, "1.0", one.SourceTemplateVersion)
}

// TestGORMStackDefinitionRepository_CancelledContext checks that every method
// runs its queries with the caller context: a cancelled context fails the
// call with a DatabaseError that keeps context.Canceled, and changes no row.
func TestGORMStackDefinitionRepository_CancelledContext(t *testing.T) {
	t.Parallel()

	for _, tt := range definitionRepoCalls {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupStackDefinitionRepo(t)
			seedContextDefinitions(t, repo)

			err := tt.call(cancelledContext(), repo)

			require.Error(t, err)
			assert.ErrorIs(t, err, context.Canceled)
			var dbErr *dberrors.DatabaseError
			assert.ErrorAs(t, err, &dbErr)
			assertDefinitionSeedUnchanged(t, repo)
		})
	}
}

// TestGORMStackDefinitionRepository_ListPagedCancelAfterCount cancels after
// the count query of ListPaged: the page query must also run with the caller
// context, so the call fails with context.Canceled instead of a page.
func TestGORMStackDefinitionRepository_ListPagedCancelAfterCount(t *testing.T) {
	t.Parallel()
	repo := setupStackDefinitionRepo(t)
	seedContextDefinitions(t, repo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	counted := false
	require.NoError(t, repo.db.Callback().Query().After("gorm:query").Register("test:cancel_after_count", func(tx *gorm.DB) {
		if _, isCount := tx.Statement.Dest.(*int64); isCount && tx.Statement.Context == ctx {
			counted = true
			cancel()
		}
	}))

	defs, total, err := repo.ListPaged(ctx, models.StackDefinitionFilter{OwnerID: "o1"}, 10, 0)

	require.True(t, counted, "the count query ran with the caller context")
	require.ErrorIs(t, err, context.Canceled)
	var dbErr *dberrors.DatabaseError
	assert.ErrorAs(t, err, &dbErr)
	assert.Nil(t, defs)
	assert.Zero(t, total)
}

// TestGORMStackDefinitionRepository_SetTemplateLinkCancelBeforeCheck cancels
// after the update of SetTemplateLink matched no row: the existence check
// must also run with the caller context, so the call fails with
// context.Canceled instead of "not found".
func TestGORMStackDefinitionRepository_SetTemplateLinkCancelBeforeCheck(t *testing.T) {
	t.Parallel()
	repo := setupStackDefinitionRepo(t)
	seedContextDefinitions(t, repo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, repo.db.Callback().Update().After("gorm:update").Register("test:cancel_after_link", func(tx *gorm.DB) {
		if tx.Statement.Context == ctx {
			cancel()
		}
	}))

	err := repo.SetTemplateLink(ctx, "missing", "t2", "2.0")

	require.ErrorIs(t, err, context.Canceled)
	assert.NotErrorIs(t, err, dberrors.ErrNotFound)
}

// TestGORMStackDefinitionRepository_UpdateCancelBeforeCommit cancels after
// the statement of Update in a default transaction (a database without
// SkipDefaultTransaction). database/sql then rolls it back and the commit
// returns sql.ErrTxDone; the error must still match context.Canceled (499,
// not 500), and nothing is stored.
func TestGORMStackDefinitionRepository_UpdateCancelBeforeCommit(t *testing.T) {
	t.Parallel()
	// A file, not :memory:: database/sql closes the connection of a cancelled
	// transaction, and an in-memory database goes with it.
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "definitions.db")), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&models.StackDefinition{}))
	repo := NewGORMStackDefinitionRepository(db)
	seedContextDefinitions(t, repo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, db.Callback().Update().Before("gorm:commit_or_rollback_transaction").Register("test:cancel_before_commit", func(tx *gorm.DB) {
		if tx.Statement.Context == ctx && tx.Error == nil {
			cancel()
			// Let database/sql roll the transaction back before the commit.
			time.Sleep(50 * time.Millisecond)
		}
	}))

	err = repo.Update(ctx, &models.StackDefinition{ID: "d1", Name: "renamed", Description: "changed", OwnerID: "o1", DefaultBranch: "main"})

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	var dbErr *dberrors.DatabaseError
	assert.ErrorAs(t, err, &dbErr)
	assertDefinitionSeedUnchanged(t, repo)
}

// TestGORMStackDefinitionRepository_TxRepoUsesCallerContext checks a
// definition repository of RunInTx: a call with a cancelled context fails
// also inside a live transaction, and the transaction commits nothing.
func TestGORMStackDefinitionRepository_TxRepoUsesCallerContext(t *testing.T) {
	t.Parallel()
	db := setupTestDBWithAllTables(t)
	repo := NewGORMStackDefinitionRepository(db)
	seedContextDefinitions(t, repo)

	err := NewGORMTxRunner(db).RunInTx(context.Background(), func(repos TxRepos) error {
		return repos.StackDefinition.Create(cancelledContext(), &models.StackDefinition{ID: "d-tx", Name: "tx", OwnerID: "o1"})
	})

	require.ErrorIs(t, err, context.Canceled)
	assertDefinitionSeedUnchanged(t, repo)
}

// TestGORMStackDefinitionRepository_LogTraceContext checks that the SQL log
// lines of every method carry the trace and span ID of the caller context.
func TestGORMStackDefinitionRepository_LogTraceContext(t *testing.T) {
	t.Parallel()

	for _, tt := range definitionRepoCalls {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			t.Run("SQL error", func(t *testing.T) {
				t.Parallel()
				// No stack_definitions table: every query fails.
				db, records := newTraceTestDB(t)
				ctx, traceID, spanID := newTraceTestSpanContext(t)

				require.Error(t, tt.call(ctx, NewGORMStackDefinitionRepository(db)))

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
				db, records := newTraceTestDB(t, &models.StackDefinition{})
				repo := NewGORMStackDefinitionRepository(db)
				seedContextDefinitions(t, repo)
				spanCtx, traceID, spanID := newTraceTestSpanContext(t)
				ctx, cancel := context.WithCancel(spanCtx)
				defer cancel()
				cancelBeforeStatements(t, db, cancel)

				require.ErrorIs(t, tt.call(ctx, repo), context.Canceled)

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

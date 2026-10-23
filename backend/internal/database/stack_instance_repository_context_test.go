package database

import (
	"context"
	"errors"
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

// instanceRepoCalls has one call of each StackInstanceRepository method and of
// the secondary interfaces CleanStartMarker, InstanceErrorMarker and
// InstanceNameLookup (#656). The calls work on the seed of seedContextInstances.
var instanceRepoCalls = []struct {
	name string
	call func(ctx context.Context, r *GORMStackInstanceRepository) error
}{
	{name: "Create", call: func(ctx context.Context, r *GORMStackInstanceRepository) error {
		return r.Create(ctx, &models.StackInstance{ID: "new-1", Name: "new", Namespace: "stack-new", OwnerID: "o1", Status: models.StackStatusDraft})
	}},
	{name: "FindByID", call: func(ctx context.Context, r *GORMStackInstanceRepository) error {
		_, err := r.FindByID(ctx, "inst-1")
		return err
	}},
	{name: "FindByNamespace", call: func(ctx context.Context, r *GORMStackInstanceRepository) error {
		_, err := r.FindByNamespace(ctx, "stack-one")
		return err
	}},
	{name: "Update", call: func(ctx context.Context, r *GORMStackInstanceRepository) error {
		later := time.Now().UTC().Add(time.Hour)
		return r.Update(ctx, &models.StackInstance{ID: "inst-1", Name: "renamed", Namespace: "stack-one", OwnerID: "o1",
			StackDefinitionID: "d1", ClusterID: "c1", Status: models.StackStatusStopped, ExpiresAt: &later})
	}},
	{name: "Delete", call: func(ctx context.Context, r *GORMStackInstanceRepository) error {
		return r.Delete(ctx, "inst-1")
	}},
	{name: "List", call: func(ctx context.Context, r *GORMStackInstanceRepository) error {
		_, err := r.List(ctx)
		return err
	}},
	{name: "ListPaged", call: func(ctx context.Context, r *GORMStackInstanceRepository) error {
		_, _, err := r.ListPaged(ctx, models.StackInstanceFilter{OwnerID: "o1"}, 10, 0)
		return err
	}},
	{name: "ListByOwner", call: func(ctx context.Context, r *GORMStackInstanceRepository) error {
		_, err := r.ListByOwner(ctx, "o1")
		return err
	}},
	{name: "FindByName", call: func(ctx context.Context, r *GORMStackInstanceRepository) error {
		_, err := r.FindByName(ctx, "one")
		return err
	}},
	{name: "FindByCluster", call: func(ctx context.Context, r *GORMStackInstanceRepository) error {
		_, err := r.FindByCluster(ctx, "c1")
		return err
	}},
	{name: "CountForInstanceLimit", call: func(ctx context.Context, r *GORMStackInstanceRepository) error {
		_, err := r.CountForInstanceLimit(ctx, "c1", "o1", true)
		return err
	}},
	{name: "CountAll", call: func(ctx context.Context, r *GORMStackInstanceRepository) error {
		_, err := r.CountAll(ctx)
		return err
	}},
	{name: "CountByStatus", call: func(ctx context.Context, r *GORMStackInstanceRepository) error {
		_, err := r.CountByStatus(ctx, models.StackStatusRunning)
		return err
	}},
	{name: "CountByStatuses", call: func(ctx context.Context, r *GORMStackInstanceRepository) error {
		_, err := r.CountByStatuses(ctx, []string{models.StackStatusRunning})
		return err
	}},
	{name: "CountByDefinitionIDs", call: func(ctx context.Context, r *GORMStackInstanceRepository) error {
		_, err := r.CountByDefinitionIDs(ctx, []string{"d1"})
		return err
	}},
	{name: "CountByOwnerIDs", call: func(ctx context.Context, r *GORMStackInstanceRepository) error {
		_, err := r.CountByOwnerIDs(ctx, []string{"o1"})
		return err
	}},
	{name: "ListIDsByDefinitionIDs", call: func(ctx context.Context, r *GORMStackInstanceRepository) error {
		_, err := r.ListIDsByDefinitionIDs(ctx, []string{"d1"})
		return err
	}},
	{name: "ListIDsByOwnerIDs", call: func(ctx context.Context, r *GORMStackInstanceRepository) error {
		_, err := r.ListIDsByOwnerIDs(ctx, []string{"o1"})
		return err
	}},
	{name: "ExistsByDefinitionAndStatus", call: func(ctx context.Context, r *GORMStackInstanceRepository) error {
		_, err := r.ExistsByDefinitionAndStatus(ctx, "d1", models.StackStatusRunning)
		return err
	}},
	{name: "ListExpired", call: func(ctx context.Context, r *GORMStackInstanceRepository) error {
		_, err := r.ListExpired(ctx)
		return err
	}},
	{name: "ListExpiringSoon", call: func(ctx context.Context, r *GORMStackInstanceRepository) error {
		_, err := r.ListExpiringSoon(ctx, time.Hour)
		return err
	}},
	{name: "MarkExpiryWarned", call: func(ctx context.Context, r *GORMStackInstanceRepository) error {
		_, err := r.MarkExpiryWarned(ctx, "inst-2", seedExpiringAt, time.Now().UTC())
		return err
	}},
	{name: "ListByStatus", call: func(ctx context.Context, r *GORMStackInstanceRepository) error {
		_, err := r.ListByStatus(ctx, models.StackStatusRunning, 10)
		return err
	}},
	{name: "MarkCleanStart", call: func(ctx context.Context, r *GORMStackInstanceRepository) error {
		return r.MarkCleanStart(ctx, "inst-2", models.CleanStatuses, true)
	}},
	{name: "ClearDeleteAfterClean", call: func(ctx context.Context, r *GORMStackInstanceRepository) error {
		return r.ClearDeleteAfterClean(ctx, "inst-1")
	}},
	{name: "MarkNamespaceError", call: func(ctx context.Context, r *GORMStackInstanceRepository) error {
		_, err := r.MarkNamespaceError(ctx, "inst-1", "namespace error", time.Now().UTC())
		return err
	}},
	{name: "FindByNameOnCluster", call: func(ctx context.Context, r *GORMStackInstanceRepository) error {
		_, err := r.FindByNameOnCluster(ctx, "one", "c1", true)
		return err
	}},
	{name: "DuplicateInstanceNames", call: func(ctx context.Context, r *GORMStackInstanceRepository) error {
		_, err := r.DuplicateInstanceNames(ctx, "c1")
		return err
	}},
}

// seedExpiringAt is the expiry of inst-2, the instance of the expiry warning.
var seedExpiringAt = time.Now().UTC().Add(30 * time.Minute).Truncate(time.Second)

// seedContextInstances stores inst-1 (running, expired, delete mark set) and
// inst-2 (running, expires soon, no expiry warning).
func seedContextInstances(t *testing.T, r *GORMStackInstanceRepository) {
	t.Helper()
	past := time.Now().UTC().Add(-time.Hour)
	expiring := seedExpiringAt
	for _, inst := range []*models.StackInstance{
		{ID: "inst-1", Name: "one", Namespace: "stack-one", OwnerID: "o1", StackDefinitionID: "d1", ClusterID: "c1",
			Status: models.StackStatusRunning, ErrorMessage: "seed", ExpiresAt: &past, DeleteAfterClean: true},
		{ID: "inst-2", Name: "two", Namespace: "stack-two", OwnerID: "o1", StackDefinitionID: "d1", ClusterID: "c1",
			Status: models.StackStatusRunning, ErrorMessage: "seed", ExpiresAt: &expiring},
	} {
		require.NoError(t, r.Create(context.Background(), inst))
	}
}

// assertSeedUnchanged checks that the seed of seedContextInstances is unchanged.
func assertSeedUnchanged(t *testing.T, r *GORMStackInstanceRepository) {
	t.Helper()
	all, err := r.List(context.Background())
	require.NoError(t, err)
	assert.Len(t, all, 2, "no instance was created or deleted")
	one, err := r.FindByID(context.Background(), "inst-1")
	require.NoError(t, err)
	assert.Equal(t, "one", one.Name)
	assert.Equal(t, models.StackStatusRunning, one.Status)
	assert.Equal(t, "seed", one.ErrorMessage)
	assert.True(t, one.DeleteAfterClean)
	two, err := r.FindByID(context.Background(), "inst-2")
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusRunning, two.Status)
	assert.False(t, two.DeleteAfterClean)
	assert.Nil(t, two.ExpiryWarnedAt)
}

func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// TestGORMStackInstanceRepository_CancelledContext checks that every method
// runs its queries with the caller context: a cancelled context fails the
// call with a DatabaseError that keeps context.Canceled, and changes no row.
func TestGORMStackInstanceRepository_CancelledContext(t *testing.T) {
	t.Parallel()

	for _, tt := range instanceRepoCalls {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupStackInstanceRepo(t)
			seedContextInstances(t, repo)

			err := tt.call(cancelledContext(), repo)

			require.Error(t, err)
			assert.ErrorIs(t, err, context.Canceled)
			var dbErr *dberrors.DatabaseError
			assert.ErrorAs(t, err, &dbErr)
			assertSeedUnchanged(t, repo)
		})
	}
}

// seedDeleteFixture stores inst-1 (from quick deploy: d1 has OwnerInstanceID
// inst-1) with a branch override, a value override, a quota override, a
// follower and an action run.
func seedDeleteFixture(t *testing.T, db *gorm.DB) *models.StackInstance {
	t.Helper()
	ctx := context.Background()
	inst := &models.StackInstance{ID: "inst-1", Name: "qd", Namespace: "stack-qd", OwnerID: "o1",
		StackDefinitionID: "d1", Status: models.StackStatusDraft}
	require.NoError(t, NewGORMStackInstanceRepository(db).Create(ctx, inst))
	require.NoError(t, NewGORMStackDefinitionRepository(db).Create(context.Background(), &models.StackDefinition{ID: "d1", Name: "qd", OwnerID: "o1", OwnerInstanceID: "inst-1"}))
	require.NoError(t, NewGORMChartBranchOverrideRepository(db).Set(context.Background(), &models.ChartBranchOverride{StackInstanceID: "inst-1", ChartConfigID: "cc1", Branch: "feature"}))
	require.NoError(t, NewGORMValueOverrideRepository(db).Create(ctx, &models.ValueOverride{StackInstanceID: "inst-1", ChartConfigID: "cc1", Values: "a: 1"}))
	require.NoError(t, NewGORMInstanceQuotaOverrideRepository(db).Upsert(ctx, &models.InstanceQuotaOverride{StackInstanceID: "inst-1", CPULimit: "1"}))
	require.NoError(t, NewGORMInstanceFollowerRepository(db).Follow(ctx, "o2", "inst-1"))
	require.NoError(t, NewGORMActionRunRepository(db).Create(ctx, &models.ActionRun{InstanceID: "inst-1", Action: "a", UserID: "o1"}))
	return inst
}

// assertDeleteFixtureKept checks that nothing of seedDeleteFixture was deleted.
func assertDeleteFixtureKept(t *testing.T, db *gorm.DB) {
	t.Helper()
	for table, want := range map[string]int64{
		"stack_instances": 1, "stack_definitions": 1, "chart_branch_overrides": 1, "value_overrides": 1,
		"instance_quota_overrides": 1, "instance_followers": 1, "action_runs": 1,
	} {
		var n int64
		require.NoError(t, db.Table(table).Count(&n).Error)
		assert.Equal(t, want, n, "rows of %s", table)
	}
}

// TestInstanceDeleteHelpers_CancelledContext checks RunInTx and the instance
// delete helpers with a cancelled context: the error keeps context.Canceled
// and nothing is deleted (#656).
func TestInstanceDeleteHelpers_CancelledContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		call func(ctx context.Context, runner *GORMTxRunner, inst *models.StackInstance) error
	}{
		{name: "RunInTx", call: func(ctx context.Context, runner *GORMTxRunner, inst *models.StackInstance) error {
			called := false
			err := runner.RunInTx(ctx, func(repos TxRepos) error {
				called = true
				return repos.StackInstance.Delete(ctx, inst.ID)
			})
			assert.False(t, called, "fn does not run without a transaction")
			return err
		}},
		{name: "DeleteInstanceWithOwnedDefinition", call: func(ctx context.Context, runner *GORMTxRunner, inst *models.StackInstance) error {
			return DeleteInstanceWithOwnedDefinition(ctx, runner, inst)
		}},
		{name: "DeleteInstanceRecord", call: func(ctx context.Context, runner *GORMTxRunner, inst *models.StackInstance) error {
			// The transaction itself is live: only the ctx of the helper is cancelled.
			return runner.RunInTx(context.Background(), func(repos TxRepos) error {
				return DeleteInstanceRecord(ctx, repos, inst)
			})
		}},
		{name: "CleanupOwnedDefinition", call: func(ctx context.Context, runner *GORMTxRunner, inst *models.StackInstance) error {
			return runner.RunInTx(context.Background(), func(repos TxRepos) error {
				return CleanupOwnedDefinition(ctx, repos, inst)
			})
		}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			db := setupTestDBWithAllTables(t)
			inst := seedDeleteFixture(t, db)

			err := tt.call(cancelledContext(), NewGORMTxRunner(db), inst)

			require.Error(t, err)
			assert.ErrorIs(t, err, context.Canceled)
			assertDeleteFixtureKept(t, db)
		})
	}
}

// openFileDeleteDB returns a file SQLite database with the tables of the
// instance delete. A file, not :memory:: database/sql closes the connection
// of a cancelled transaction, and an in-memory database goes with it.
func openFileDeleteDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "instances.db")), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&models.StackInstance{}, &models.StackDefinition{}, &models.ChartConfig{},
		&models.ChartBranchOverride{}, &models.ValueOverride{}, &models.InstanceQuotaOverride{},
		&models.InstanceFollower{}, &models.ActionRun{}, &models.DeploymentLog{}))
	return db
}

// TestGORMTxRunner_CancelInsideTx checks a cancel inside a transaction: the
// error of RunInTx matches context.Canceled (499, not 500), also when the
// statements succeeded and only the commit fails, and nothing is stored.
func TestGORMTxRunner_CancelInsideTx(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		fn   func(ctx context.Context, cancel context.CancelFunc, repos TxRepos) error
	}{
		{name: "cancel before commit", fn: func(ctx context.Context, cancel context.CancelFunc, repos TxRepos) error {
			if err := repos.StackInstance.Delete(ctx, "inst-1"); err != nil {
				return err
			}
			cancel()
			// Let database/sql roll the transaction back before the commit.
			time.Sleep(50 * time.Millisecond)
			return nil
		}},
		{name: "cancel before a statement", fn: func(ctx context.Context, cancel context.CancelFunc, repos TxRepos) error {
			if err := repos.StackInstance.Delete(ctx, "inst-1"); err != nil {
				return err
			}
			cancel()
			if err := repos.ValueOverride.DeleteByInstance(ctx, "inst-1"); err != nil {
				return errors.New("deleting value overrides failed")
			}
			return nil
		}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			db := openFileDeleteDB(t)
			seedDeleteFixture(t, db)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			err := NewGORMTxRunner(db).RunInTx(ctx, func(repos TxRepos) error {
				return tt.fn(ctx, cancel, repos)
			})

			require.Error(t, err)
			assert.ErrorIs(t, err, context.Canceled)
			var dbErr *dberrors.DatabaseError
			assert.ErrorAs(t, err, &dbErr)
			assertDeleteFixtureKept(t, db)
		})
	}
}

// TestDeleteInstanceWithOwnedDefinition_CancelBeforeCommit cancels after the
// last statement of the instance delete transaction: nothing is deleted, the
// error matches context.Canceled, and the owned definition stays.
func TestDeleteInstanceWithOwnedDefinition_CancelBeforeCommit(t *testing.T) {
	t.Parallel()
	db := openFileDeleteDB(t)
	inst := seedDeleteFixture(t, db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, db.Callback().Delete().After("gorm:delete").Register("test:cancel_after_action_runs", func(tx *gorm.DB) {
		if tx.Statement.Context == ctx && tx.Statement.Table == "action_runs" {
			cancel()
			time.Sleep(50 * time.Millisecond)
		}
	}))

	err := DeleteInstanceWithOwnedDefinition(ctx, NewGORMTxRunner(db), inst)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assertDeleteFixtureKept(t, db)
}

// TestGORMStackInstanceRepository_UpdateCancelBeforeCommit cancels after the
// last statement of the Update transaction. database/sql then rolls it back
// and Commit returns sql.ErrTxDone; the error must still match
// context.Canceled, and nothing is stored.
func TestGORMStackInstanceRepository_UpdateCancelBeforeCommit(t *testing.T) {
	t.Parallel()
	db := openFileDeleteDB(t)
	repo := NewGORMStackInstanceRepository(db)
	seedDeleteFixture(t, db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates := 0
	require.NoError(t, db.Callback().Update().After("gorm:update").Register("test:cancel_after_save", func(tx *gorm.DB) {
		if tx.Statement.Context != ctx || tx.Statement.Table != "stack_instances" {
			return
		}
		// The first statement clears the expiry warning, the second saves the row.
		if updates++; updates == 2 {
			cancel()
			time.Sleep(50 * time.Millisecond)
		}
	}))

	err := repo.Update(ctx, &models.StackInstance{ID: "inst-1", Name: "renamed", Namespace: "stack-qd", OwnerID: "o1",
		StackDefinitionID: "d1", Status: models.StackStatusStopped})

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	var dbErr *dberrors.DatabaseError
	assert.ErrorAs(t, err, &dbErr)
	stored, findErr := repo.FindByID(context.Background(), "inst-1")
	require.NoError(t, findErr)
	assert.Equal(t, "qd", stored.Name)
	assert.Equal(t, models.StackStatusDraft, stored.Status)
}

// TestGORMStackInstanceRepository_ListPagedCancelAfterCount cancels after the
// count query of ListPaged: the page query must also run with the caller
// context, so the call fails with context.Canceled instead of a page.
func TestGORMStackInstanceRepository_ListPagedCancelAfterCount(t *testing.T) {
	t.Parallel()
	repo := setupStackInstanceRepo(t)
	seedContextInstances(t, repo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	counted := false
	require.NoError(t, repo.db.Callback().Query().After("gorm:query").Register("test:cancel_after_count", func(tx *gorm.DB) {
		if _, isCount := tx.Statement.Dest.(*int64); isCount && tx.Statement.Context == ctx {
			counted = true
			cancel()
		}
	}))

	instances, total, err := repo.ListPaged(ctx, models.StackInstanceFilter{OwnerID: "o1"}, 10, 0)

	require.True(t, counted, "the count query ran with the caller context")
	require.ErrorIs(t, err, context.Canceled)
	var dbErr *dberrors.DatabaseError
	assert.ErrorAs(t, err, &dbErr)
	assert.Nil(t, instances)
	assert.Zero(t, total)
}

// TestDeleteInstanceWithOwnedDefinition_CleanupIgnoresCancel checks that the
// owned definition cleanup after the committed instance delete runs also when
// the caller context ends between the two transactions.
func TestDeleteInstanceWithOwnedDefinition_CleanupIgnoresCancel(t *testing.T) {
	t.Parallel()
	db := openFileDeleteDB(t)
	inst := seedDeleteFixture(t, db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &afterFirstTxRunner{inner: NewGORMTxRunner(db), after: cancel}

	require.NoError(t, DeleteInstanceWithOwnedDefinition(ctx, runner, inst))

	var n int64
	require.NoError(t, db.Table("stack_instances").Count(&n).Error)
	assert.Zero(t, n, "the instance is deleted")
	require.NoError(t, db.Table("stack_definitions").Count(&n).Error)
	assert.Zero(t, n, "the owned definition is deleted after the cancel")
}

// afterFirstTxRunner calls after once, when the first transaction committed.
type afterFirstTxRunner struct {
	inner TxRunner
	after func()
	calls int
}

func (r *afterFirstTxRunner) RunInTx(ctx context.Context, fn func(repos TxRepos) error) error {
	err := r.inner.RunInTx(ctx, fn)
	r.calls++
	if r.calls == 1 && err == nil {
		r.after()
	}
	return err
}

// TestDeleteInstanceWithOwnedDefinition_CleanupHasDeadline checks that the
// detached owned definition cleanup keeps a deadline: it holds a FOR UPDATE
// lock, so a caller without a deadline must not make it unbounded (#656).
func TestDeleteInstanceWithOwnedDefinition_CleanupHasDeadline(t *testing.T) {
	t.Parallel()
	db := openFileDeleteDB(t)
	inst := seedDeleteFixture(t, db)
	runner := &txCtxRecorder{inner: NewGORMTxRunner(db)}

	require.NoError(t, DeleteInstanceWithOwnedDefinition(context.Background(), runner, inst))

	require.Greater(t, len(runner.ctxs), 1, "the cleanup runs its own transactions")
	_, ok := runner.ctxs[0].Deadline()
	assert.False(t, ok, "the instance delete keeps the caller context")
	for i, ctx := range runner.ctxs[1:] {
		deadline, ok := ctx.Deadline()
		require.True(t, ok, "cleanup transaction %d has a deadline", i+1)
		assert.WithinDuration(t, time.Now().Add(ownedDefinitionCleanupTimeout), deadline, 5*time.Second)
	}
	var n int64
	require.NoError(t, db.Table("stack_definitions").Count(&n).Error)
	assert.Zero(t, n, "the owned definition is deleted")
}

// txCtxRecorder records the context of each transaction.
type txCtxRecorder struct {
	inner TxRunner
	ctxs  []context.Context
}

func (r *txCtxRecorder) RunInTx(ctx context.Context, fn func(repos TxRepos) error) error {
	r.ctxs = append(r.ctxs, ctx)
	return r.inner.RunInTx(ctx, fn)
}

// TestGORMStackInstanceRepository_LogTraceContext checks that the SQL log
// lines of every method carry the trace and span ID of the caller context,
// also for the statements inside a transaction.
func TestGORMStackInstanceRepository_LogTraceContext(t *testing.T) {
	t.Parallel()

	for _, tt := range instanceRepoCalls {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			t.Run("SQL error", func(t *testing.T) {
				t.Parallel()
				// No stack_instances table: every query fails.
				db, records := newTraceTestDB(t)
				ctx, traceID, spanID := newTraceTestSpanContext(t)

				require.Error(t, tt.call(ctx, NewGORMStackInstanceRepository(db)))

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
				db, records := newTraceTestDB(t, &models.StackInstance{})
				repo := NewGORMStackInstanceRepository(db)
				seedContextInstances(t, repo)
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

// TestInstanceDeleteHelpers_LogTraceContext checks that the statements of
// RunInTx and of the delete helpers carry the trace of the caller context.
func TestInstanceDeleteHelpers_LogTraceContext(t *testing.T) {
	t.Parallel()

	inst := &models.StackInstance{ID: "inst-1", StackDefinitionID: "d1"}
	tests := []struct {
		name string
		call func(ctx context.Context, runner *GORMTxRunner) error
	}{
		{name: "RunInTx", call: func(ctx context.Context, runner *GORMTxRunner) error {
			return runner.RunInTx(ctx, func(repos TxRepos) error {
				_, err := repos.StackTemplate.FindByID(ctx, "tpl-1")
				return err
			})
		}},
		{name: "DeleteInstanceWithOwnedDefinition", call: func(ctx context.Context, runner *GORMTxRunner) error {
			return DeleteInstanceWithOwnedDefinition(ctx, runner, inst)
		}},
		{name: "DeleteInstanceRecord", call: func(ctx context.Context, runner *GORMTxRunner) error {
			return runner.RunInTx(ctx, func(repos TxRepos) error {
				return DeleteInstanceRecord(ctx, repos, inst)
			})
		}},
		{name: "CleanupOwnedDefinition", call: func(ctx context.Context, runner *GORMTxRunner) error {
			return runner.RunInTx(ctx, func(repos TxRepos) error {
				return CleanupOwnedDefinition(ctx, repos, inst)
			})
		}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// No tables: the first statement fails.
			db, records := newTraceTestDB(t)
			ctx, traceID, spanID := newTraceTestSpanContext(t)

			require.Error(t, tt.call(ctx, NewGORMTxRunner(db)))

			recs := records()
			require.NotEmpty(t, recs)
			for _, r := range recs {
				assert.Equal(t, "SQL error", r.Body().AsString())
				assert.Equal(t, traceID, r.TraceID())
				assert.Equal(t, spanID, r.SpanID())
			}
		})
	}
}

//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// setupInstanceContextMySQL returns the raw DB for the lock holder, and the
// instance repository and the transaction runner on a session like
// production (SkipDefaultTransaction, factory.go).
func setupInstanceContextMySQL(t *testing.T, maxConns int) (*gorm.DB, *GORMStackInstanceRepository, *GORMTxRunner) {
	t.Helper()
	db := setupMySQLTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	// A broken connection left in the pool would fail the last query.
	sqlDB.SetMaxOpenConns(maxConns)

	var lockWaitSeconds int
	require.NoError(t, db.Raw("SELECT @@innodb_lock_wait_timeout").Scan(&lockWaitSeconds).Error)
	require.GreaterOrEqual(t, lockWaitSeconds, 5, "the test needs a lock wait timeout of at least 5s")

	session := db.Session(&gorm.Session{SkipDefaultTransaction: true})
	return db, NewGORMStackInstanceRepository(session), NewGORMTxRunner(session)
}

// lockTableRows starts a transaction that locks the rows of table that match
// query with SELECT ... FOR UPDATE. The returned function rolls it back
// (also at cleanup).
func lockTableRows(t *testing.T, db *gorm.DB, table, query string, args ...any) func() {
	t.Helper()
	tx := db.Begin()
	require.NoError(t, tx.Error)
	done := false
	rollback := func() {
		if !done {
			done = true
			require.NoError(t, tx.Rollback().Error)
		}
	}
	t.Cleanup(func() {
		if !done {
			tx.Rollback()
		}
	})
	var held []map[string]any
	require.NoError(t, tx.Table(table).Clauses(clause.Locking{Strength: "UPDATE"}).Where(query, args...).Find(&held).Error)
	require.NotEmpty(t, held)
	return rollback
}

// TestDatabaseMySQL_InstanceContextEndsLockWait checks on MySQL that the
// caller context ends an instance query that waits for a row lock (#656):
// the call returns the context error long before innodb_lock_wait_timeout,
// and the pool still serves the next query. It uses the deadline and cancel
// cases of the user tests (userContextCases).
func TestDatabaseMySQL_InstanceContextEndsLockWait(t *testing.T) {
	for _, tt := range userContextCases {
		t.Run(tt.name, func(t *testing.T) {
			db, repo, _ := setupInstanceContextMySQL(t, 2)
			inst := &models.StackInstance{ID: "i-lock-" + tt.name, Name: "lock", Namespace: "stack-lock-" + tt.name,
				OwnerID: "u1", StackDefinitionID: "d1", Status: models.StackStatusRunning}
			require.NoError(t, repo.Create(context.Background(), inst))
			release := lockTableRows(t, db, "stack_instances", "id = ?", inst.ID)

			ctx, cancel := tt.ctx()
			defer cancel()
			start := time.Now()
			// Update is a transaction: its first statement waits for the lock.
			changed := *inst
			changed.Status = models.StackStatusStopped
			err := repo.Update(ctx, &changed)
			elapsed := time.Since(start)

			require.Error(t, err)
			assert.ErrorIs(t, err, tt.wantErr)
			assert.Less(t, elapsed, 3*time.Second, "the context, not the lock wait timeout, ends the call")

			release()
			// The transaction never sent COMMIT; the server rolls it back.
			assert.Never(t, func() bool {
				got, findErr := repo.FindByID(context.Background(), inst.ID)
				return findErr != nil || got.Status != models.StackStatusRunning
			}, time.Second, 100*time.Millisecond, "the cancelled update did not commit")

			nextCtx, nextCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer nextCancel()
			require.NoError(t, repo.Update(nextCtx, &changed), "the pool serves the next query")
		})
	}
}

// TestDatabaseMySQL_InstanceConditionalUpdateContext checks a single
// conditional update (the k8s status watcher) on MySQL: the context ends its
// lock wait, and the pool serves the next query.
func TestDatabaseMySQL_InstanceConditionalUpdateContext(t *testing.T) {
	for _, tt := range userContextCases {
		t.Run(tt.name, func(t *testing.T) {
			db, repo, _ := setupInstanceContextMySQL(t, 2)
			inst := &models.StackInstance{ID: "i-mark-" + tt.name, Name: "mark", Namespace: "stack-mark-" + tt.name,
				OwnerID: "u1", StackDefinitionID: "d1", Status: models.StackStatusRunning}
			require.NoError(t, repo.Create(context.Background(), inst))
			release := lockTableRows(t, db, "stack_instances", "id = ?", inst.ID)

			ctx, cancel := tt.ctx()
			defer cancel()
			start := time.Now()
			// A cancelled autocommit statement can still run on the server
			// after the lock is free, so the test checks only the client side.
			_, err := repo.MarkNamespaceError(ctx, inst.ID, "namespace error", time.Now())
			elapsed := time.Since(start)

			require.Error(t, err)
			assert.ErrorIs(t, err, tt.wantErr)
			assert.Less(t, elapsed, 3*time.Second, "the context, not the lock wait timeout, ends the call")

			release()
			nextCtx, nextCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer nextCancel()
			got, err := repo.FindByID(nextCtx, inst.ID)
			require.NoError(t, err, "the pool serves the next query")
			assert.Equal(t, inst.ID, got.ID)
		})
	}
}

// TestDatabaseMySQL_DeleteInstanceWithOwnedDefinitionContext checks on MySQL
// that a context that ends during the instance delete transaction commits
// nothing (#656): the overrides and the instance row are deleted in the
// transaction before the action runs, whose rows another transaction locks.
// The error matches the context error, and the pool serves the next delete.
func TestDatabaseMySQL_DeleteInstanceWithOwnedDefinitionContext(t *testing.T) {
	for _, tt := range userContextCases {
		t.Run(tt.name, func(t *testing.T) {
			db, repo, runner := setupInstanceContextMySQL(t, 3)
			ctx0 := context.Background()
			inst := &models.StackInstance{ID: "i-del-" + tt.name, Name: "qd", Namespace: "stack-qd-" + tt.name,
				OwnerID: "u1", StackDefinitionID: "d-" + tt.name, Status: models.StackStatusDraft}
			require.NoError(t, repo.Create(ctx0, inst))
			require.NoError(t, NewGORMStackDefinitionRepository(db).Create(&models.StackDefinition{
				ID: inst.StackDefinitionID, Name: "qd", OwnerID: "u1", OwnerInstanceID: inst.ID}))
			require.NoError(t, NewGORMChartBranchOverrideRepository(db).Set(&models.ChartBranchOverride{
				StackInstanceID: inst.ID, ChartConfigID: "cc1", Branch: "feature"}))
			require.NoError(t, NewGORMValueOverrideRepository(db).Create(&models.ValueOverride{
				StackInstanceID: inst.ID, ChartConfigID: "cc1", Values: "a: 1"}))
			require.NoError(t, NewGORMActionRunRepository(db).Create(ctx0, &models.ActionRun{
				InstanceID: inst.ID, Action: "a", UserID: "u1"}))
			release := lockTableRows(t, db, "action_runs", "instance_id = ?", inst.ID)

			ctx, cancel := tt.ctx()
			defer cancel()
			start := time.Now()
			err := DeleteInstanceWithOwnedDefinition(ctx, runner, inst)
			elapsed := time.Since(start)

			require.Error(t, err)
			assert.ErrorIs(t, err, tt.wantErr)
			assert.Less(t, elapsed, 3*time.Second, "the context, not the lock wait timeout, ends the call")

			release()
			// The transaction never sent COMMIT; the server rolls it back.
			assert.Never(t, func() bool {
				var instances, overrides, branches, defs int64
				db.Table("stack_instances").Where("id = ?", inst.ID).Count(&instances)
				db.Table("value_overrides").Where("stack_instance_id = ?", inst.ID).Count(&overrides)
				db.Table("chart_branch_overrides").Where("stack_instance_id = ?", inst.ID).Count(&branches)
				db.Table("stack_definitions").Where("id = ?", inst.StackDefinitionID).Count(&defs)
				return instances != 1 || overrides != 1 || branches != 1 || defs != 1
			}, time.Second, 100*time.Millisecond, "the cancelled delete did not commit")

			nextCtx, nextCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer nextCancel()
			require.NoError(t, DeleteInstanceWithOwnedDefinition(nextCtx, runner, inst), "the pool serves the next delete")
			_, err = repo.FindByID(context.Background(), inst.ID)
			assert.Error(t, err, "the instance is deleted")
			var defs int64
			require.NoError(t, db.Table("stack_definitions").Where("id = ?", inst.StackDefinitionID).Count(&defs).Error)
			assert.Zero(t, defs, "the owned definition is deleted")
		})
	}
}

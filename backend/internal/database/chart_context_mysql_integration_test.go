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
)

// setupChartContextMySQL returns the raw DB for the lock holder, and the
// chart repositories and the transaction runner on a session like production
// (SkipDefaultTransaction, factory.go). It seeds chart cc-lock (definition
// d-lock) and the override of i-lock / cc-lock.
func setupChartContextMySQL(t *testing.T, maxConns int) (*gorm.DB, *GORMChartConfigRepository, *GORMChartBranchOverrideRepository, *GORMTxRunner) {
	t.Helper()
	db, _, _ := setupInstanceContextMySQL(t, maxConns)
	session := db.Session(&gorm.Session{SkipDefaultTransaction: true})
	charts := NewGORMChartConfigRepository(session)
	branches := NewGORMChartBranchOverrideRepository(session)
	ctx := context.Background()
	require.NoError(t, charts.Create(ctx, &models.ChartConfig{ID: "cc-lock", StackDefinitionID: "d-lock", ChartName: "web"}))
	require.NoError(t, branches.Set(ctx, &models.ChartBranchOverride{ID: "bo-lock", StackInstanceID: "i-lock", ChartConfigID: "cc-lock", Branch: "main"}))
	return db, charts, branches, NewGORMTxRunner(session)
}

// TestDatabaseMySQL_ChartContextEndsLockWait checks on MySQL that the caller
// context ends a chart config or branch override write that waits for a row
// lock (#656): the call returns the context error long before
// innodb_lock_wait_timeout, and the pool still serves the next query.
func TestDatabaseMySQL_ChartContextEndsLockWait(t *testing.T) {
	ops := []struct {
		call  func(ctx context.Context, charts *GORMChartConfigRepository, branches *GORMChartBranchOverrideRepository) error
		name  string
		table string
		query string
		arg   string
	}{
		{
			name: "ChartConfig.Update", table: "chart_configs", query: "id = ?", arg: "cc-lock",
			call: func(ctx context.Context, charts *GORMChartConfigRepository, _ *GORMChartBranchOverrideRepository) error {
				return charts.Update(ctx, &models.ChartConfig{ID: "cc-lock", StackDefinitionID: "d-lock", ChartName: "renamed"})
			},
		},
		{
			name: "ChartConfig.DeleteByDefinition", table: "chart_configs", query: "id = ?", arg: "cc-lock",
			call: func(ctx context.Context, charts *GORMChartConfigRepository, _ *GORMChartBranchOverrideRepository) error {
				return charts.DeleteByDefinition(ctx, "d-lock")
			},
		},
		{
			name: "ChartBranchOverride.Set", table: "chart_branch_overrides", query: "id = ?", arg: "bo-lock",
			call: func(ctx context.Context, _ *GORMChartConfigRepository, branches *GORMChartBranchOverrideRepository) error {
				return branches.Set(ctx, &models.ChartBranchOverride{StackInstanceID: "i-lock", ChartConfigID: "cc-lock", Branch: "feature"})
			},
		},
		{
			name: "ChartBranchOverride.DeleteByInstance", table: "chart_branch_overrides", query: "id = ?", arg: "bo-lock",
			call: func(ctx context.Context, _ *GORMChartConfigRepository, branches *GORMChartBranchOverrideRepository) error {
				return branches.DeleteByInstance(ctx, "i-lock")
			},
		},
	}
	for _, op := range ops {
		for _, tt := range userContextCases {
			t.Run(op.name+"/"+tt.name, func(t *testing.T) {
				db, charts, branches, _ := setupChartContextMySQL(t, 2)
				release := lockTableRows(t, db, op.table, op.query, op.arg)

				ctx, cancel := tt.ctx()
				defer cancel()
				start := time.Now()
				// A cancelled autocommit statement can still run on the server
				// after the lock is free, so the test checks only the client side.
				err := op.call(ctx, charts, branches)
				elapsed := time.Since(start)

				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				assert.Less(t, elapsed, 3*time.Second, "the context, not the lock wait timeout, ends the call")

				release()
				nextCtx, nextCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer nextCancel()
				_, err = charts.ListByDefinition(nextCtx, "d-lock")
				require.NoError(t, err, "the pool serves the next query")
				_, err = branches.List(nextCtx, "i-lock")
				require.NoError(t, err, "the pool serves the next query")
			})
		}
	}
}

// TestDatabaseMySQL_ChartTxContextCommitsNothing checks on MySQL that a
// context that ends inside a transaction with chart writes commits nothing
// (#656): the transaction creates a chart, then waits for the row lock of a
// branch override. The error matches the context error, the created chart is
// rolled back, and the pool serves the next transaction.
func TestDatabaseMySQL_ChartTxContextCommitsNothing(t *testing.T) {
	for _, tt := range userContextCases {
		t.Run(tt.name, func(t *testing.T) {
			db, charts, branches, runner := setupChartContextMySQL(t, 3)
			release := lockTableRows(t, db, "chart_branch_overrides", "id = ?", "bo-lock")
			created := "cc-new-" + tt.name

			ctx, cancel := tt.ctx()
			defer cancel()
			start := time.Now()
			err := runner.RunInTx(ctx, func(repos TxRepos) error {
				if err := repos.ChartConfig.Create(ctx, &models.ChartConfig{ID: created, StackDefinitionID: "d-new", ChartName: "new"}); err != nil {
					return err
				}
				return repos.BranchOverride.Set(ctx, &models.ChartBranchOverride{StackInstanceID: "i-lock", ChartConfigID: "cc-lock", Branch: "feature"})
			})
			elapsed := time.Since(start)

			require.Error(t, err)
			assert.ErrorIs(t, err, tt.wantErr)
			assert.Less(t, elapsed, 3*time.Second, "the context, not the lock wait timeout, ends the call")

			release()
			// The transaction never sent COMMIT; the server rolls it back.
			assert.Never(t, func() bool {
				var n int64
				db.Table("chart_configs").Where("id = ?", created).Count(&n)
				return n != 0
			}, time.Second, 100*time.Millisecond, "the cancelled transaction did not commit")

			nextCtx, nextCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer nextCancel()
			require.NoError(t, runner.RunInTx(nextCtx, func(repos TxRepos) error {
				return repos.ChartConfig.Create(nextCtx, &models.ChartConfig{ID: created, StackDefinitionID: "d-new", ChartName: "new"})
			}), "the pool serves the next transaction")
			got, err := charts.FindByID(context.Background(), created)
			require.NoError(t, err)
			assert.Equal(t, "new", got.ChartName)
			bo, err := branches.Get(context.Background(), "i-lock", "cc-lock")
			require.NoError(t, err)
			assert.Equal(t, "main", bo.Branch, "the override keeps its branch")
		})
	}
}

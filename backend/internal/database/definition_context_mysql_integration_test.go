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

// setupDefinitionContextMySQL returns the raw DB for the lock holder, and the
// definition repository and the transaction runner on a session like
// production (SkipDefaultTransaction, factory.go).
func setupDefinitionContextMySQL(t *testing.T, maxConns int) (*gorm.DB, *GORMStackDefinitionRepository, *GORMTxRunner) {
	t.Helper()
	db, _, _ := setupInstanceContextMySQL(t, maxConns)
	session := db.Session(&gorm.Session{SkipDefaultTransaction: true})
	return db, NewGORMStackDefinitionRepository(session), NewGORMTxRunner(session)
}

// TestDatabaseMySQL_DefinitionContextEndsLockWait checks on MySQL that the
// caller context ends a definition query that waits for a row lock (#656):
// the call returns the context error long before innodb_lock_wait_timeout,
// and the pool still serves the next query.
func TestDatabaseMySQL_DefinitionContextEndsLockWait(t *testing.T) {
	ops := []struct {
		call func(ctx context.Context, repo *GORMStackDefinitionRepository, runner *GORMTxRunner, id string) error
		name string
	}{
		{name: "FindByIDForUpdate in a transaction", call: func(ctx context.Context, _ *GORMStackDefinitionRepository, runner *GORMTxRunner, id string) error {
			return runner.RunInTx(ctx, func(repos TxRepos) error {
				_, err := repos.StackDefinition.FindByIDForUpdate(ctx, id)
				return err
			})
		}},
		{name: "SetTemplateLink", call: func(ctx context.Context, repo *GORMStackDefinitionRepository, _ *GORMTxRunner, id string) error {
			// A cancelled autocommit statement can still run on the server
			// after the lock is free, so the test checks only the client side.
			return repo.SetTemplateLink(ctx, id, "t-lock", "9.9.9")
		}},
	}
	for _, op := range ops {
		for _, tt := range userContextCases {
			t.Run(op.name+"/"+tt.name, func(t *testing.T) {
				db, repo, runner := setupDefinitionContextMySQL(t, 2)
				def := &models.StackDefinition{ID: "d-lock-" + tt.name, Name: "lock", OwnerID: "u1", DefaultBranch: "main"}
				require.NoError(t, repo.Create(context.Background(), def))
				release := lockTableRows(t, db, "stack_definitions", "id = ?", def.ID)

				ctx, cancel := tt.ctx()
				defer cancel()
				start := time.Now()
				err := op.call(ctx, repo, runner, def.ID)
				elapsed := time.Since(start)

				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				assert.Less(t, elapsed, 3*time.Second, "the context, not the lock wait timeout, ends the call")

				release()
				nextCtx, nextCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer nextCancel()
				require.NoError(t, runner.RunInTx(nextCtx, func(repos TxRepos) error {
					_, findErr := repos.StackDefinition.FindByIDForUpdate(nextCtx, def.ID)
					return findErr
				}), "the pool serves the next query")
			})
		}
	}
}

// TestDatabaseMySQL_DefinitionTxContextCommitsNothing checks on MySQL that a
// context that ends inside a definition transaction commits nothing (#656):
// the transaction creates a definition, then waits for the row lock of
// another definition. The error matches the context error, the created row
// is rolled back, and the pool serves the next transaction.
func TestDatabaseMySQL_DefinitionTxContextCommitsNothing(t *testing.T) {
	for _, tt := range userContextCases {
		t.Run(tt.name, func(t *testing.T) {
			db, repo, runner := setupDefinitionContextMySQL(t, 3)
			locked := &models.StackDefinition{ID: "d-held-" + tt.name, Name: "held", OwnerID: "u1", DefaultBranch: "main"}
			require.NoError(t, repo.Create(context.Background(), locked))
			release := lockTableRows(t, db, "stack_definitions", "id = ?", locked.ID)
			created := "d-new-" + tt.name

			ctx, cancel := tt.ctx()
			defer cancel()
			start := time.Now()
			err := runner.RunInTx(ctx, func(repos TxRepos) error {
				if err := repos.StackDefinition.Create(ctx, &models.StackDefinition{ID: created, Name: "new", OwnerID: "u1", DefaultBranch: "main"}); err != nil {
					return err
				}
				_, err := repos.StackDefinition.FindByIDForUpdate(ctx, locked.ID)
				return err
			})
			elapsed := time.Since(start)

			require.Error(t, err)
			assert.ErrorIs(t, err, tt.wantErr)
			assert.Less(t, elapsed, 3*time.Second, "the context, not the lock wait timeout, ends the call")

			release()
			// The transaction never sent COMMIT; the server rolls it back.
			assert.Never(t, func() bool {
				var n int64
				db.Table("stack_definitions").Where("id = ?", created).Count(&n)
				return n != 0
			}, time.Second, 100*time.Millisecond, "the cancelled transaction did not commit")

			nextCtx, nextCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer nextCancel()
			require.NoError(t, runner.RunInTx(nextCtx, func(repos TxRepos) error {
				return repos.StackDefinition.Create(nextCtx, &models.StackDefinition{ID: created, Name: "new", OwnerID: "u1", DefaultBranch: "main"})
			}), "the pool serves the next transaction")
			got, err := repo.FindByID(context.Background(), created)
			require.NoError(t, err)
			assert.Equal(t, "new", got.Name)
		})
	}
}

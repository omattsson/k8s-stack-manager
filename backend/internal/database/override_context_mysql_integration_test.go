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

// setupOverrideContextMySQL returns the raw DB for the lock holder, and the
// value override and shared values repositories on a session like production
// (SkipDefaultTransaction, factory.go). It seeds the override vo-lock
// (i-lock / cc-lock) and the shared values sv-lock (cluster c-lock).
func setupOverrideContextMySQL(t *testing.T, maxConns int) (*gorm.DB, *GORMValueOverrideRepository, *GORMSharedValuesRepository) {
	t.Helper()
	db, _, _ := setupInstanceContextMySQL(t, maxConns)
	session := db.Session(&gorm.Session{SkipDefaultTransaction: true})
	overrides := NewGORMValueOverrideRepository(session)
	shared := NewGORMSharedValuesRepository(session)
	ctx := context.Background()
	require.NoError(t, overrides.Create(ctx, &models.ValueOverride{ID: "vo-lock", StackInstanceID: "i-lock", ChartConfigID: "cc-lock", Values: "a: 1"}))
	require.NoError(t, shared.Create(ctx, &models.SharedValues{ID: "sv-lock", ClusterID: "c-lock", Name: "base", Values: "a: 1"}))
	return db, overrides, shared
}

// TestDatabaseMySQL_OverrideContextEndsLockWait checks on MySQL that the
// caller context ends a value override or shared values write that waits for
// a row lock (#656): the call returns the context error long before
// innodb_lock_wait_timeout, and the pool still serves the next query.
func TestDatabaseMySQL_OverrideContextEndsLockWait(t *testing.T) {
	ops := []struct {
		call  func(ctx context.Context, overrides *GORMValueOverrideRepository, shared *GORMSharedValuesRepository) error
		name  string
		table string
		arg   string
	}{
		{
			name: "ValueOverride.Update", table: "value_overrides", arg: "vo-lock",
			call: func(ctx context.Context, overrides *GORMValueOverrideRepository, _ *GORMSharedValuesRepository) error {
				return overrides.Update(ctx, &models.ValueOverride{ID: "vo-lock", StackInstanceID: "i-lock", ChartConfigID: "cc-lock", Values: "b: 2"})
			},
		},
		{
			name: "ValueOverride.DeleteByInstance", table: "value_overrides", arg: "vo-lock",
			call: func(ctx context.Context, overrides *GORMValueOverrideRepository, _ *GORMSharedValuesRepository) error {
				return overrides.DeleteByInstance(ctx, "i-lock")
			},
		},
		{
			name: "SharedValues.Update", table: "shared_values", arg: "sv-lock",
			call: func(ctx context.Context, _ *GORMValueOverrideRepository, shared *GORMSharedValuesRepository) error {
				return shared.Update(ctx, &models.SharedValues{ID: "sv-lock", ClusterID: "c-lock", Name: "renamed", Values: "b: 2"})
			},
		},
		{
			name: "SharedValues.Delete", table: "shared_values", arg: "sv-lock",
			call: func(ctx context.Context, _ *GORMValueOverrideRepository, shared *GORMSharedValuesRepository) error {
				return shared.Delete(ctx, "sv-lock")
			},
		},
	}
	for _, op := range ops {
		for _, tt := range userContextCases {
			t.Run(op.name+"/"+tt.name, func(t *testing.T) {
				db, overrides, shared := setupOverrideContextMySQL(t, 2)
				release := lockTableRows(t, db, op.table, "id = ?", op.arg)

				ctx, cancel := tt.ctx()
				defer cancel()
				start := time.Now()
				// A cancelled autocommit statement can still run on the server
				// after the lock is free, so the test checks only the client side.
				err := op.call(ctx, overrides, shared)
				elapsed := time.Since(start)

				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				assert.Less(t, elapsed, 3*time.Second, "the context, not the lock wait timeout, ends the call")

				release()
				nextCtx, nextCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer nextCancel()
				_, err = overrides.ListByInstance(nextCtx, "i-lock")
				require.NoError(t, err, "the pool serves the next query")
				_, err = shared.ListByCluster(nextCtx, "c-lock")
				require.NoError(t, err, "the pool serves the next query")
			})
		}
	}
}

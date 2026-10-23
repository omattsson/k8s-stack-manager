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

// setupTemplateContextMySQL returns the raw DB for the lock holder, and the
// template and template chart repositories on a session like production
// (SkipDefaultTransaction, factory.go). It seeds the template t-lock and its
// chart tc-lock.
func setupTemplateContextMySQL(t *testing.T, maxConns int) (*gorm.DB, *GORMStackTemplateRepository, *GORMTemplateChartConfigRepository) {
	t.Helper()
	db, _, _ := setupInstanceContextMySQL(t, maxConns)
	session := db.Session(&gorm.Session{SkipDefaultTransaction: true})
	templates := NewGORMStackTemplateRepository(session)
	charts := NewGORMTemplateChartConfigRepository(session)
	ctx := context.Background()
	require.NoError(t, templates.Create(ctx, &models.StackTemplate{ID: "t-lock", Name: "lock", OwnerID: "o-lock", DefaultBranch: "main"}))
	require.NoError(t, charts.Create(ctx, &models.TemplateChartConfig{ID: "tc-lock", StackTemplateID: "t-lock", ChartName: "web"}))
	return db, templates, charts
}

// TestDatabaseMySQL_TemplateContextEndsLockWait checks on MySQL that the
// caller context ends a template or template chart query that waits for a
// row lock (#656): the call returns the context error long before
// innodb_lock_wait_timeout, and the pool still serves the next query.
func TestDatabaseMySQL_TemplateContextEndsLockWait(t *testing.T) {
	ops := []struct {
		call  func(ctx context.Context, templates *GORMStackTemplateRepository, charts *GORMTemplateChartConfigRepository) error
		name  string
		table string
		arg   string
	}{
		{
			name: "StackTemplate.Update", table: "stack_templates", arg: "t-lock",
			call: func(ctx context.Context, templates *GORMStackTemplateRepository, _ *GORMTemplateChartConfigRepository) error {
				return templates.Update(ctx, &models.StackTemplate{ID: "t-lock", Name: "renamed", OwnerID: "o-lock", DefaultBranch: "main"})
			},
		},
		{
			name: "StackTemplate.Delete", table: "stack_templates", arg: "t-lock",
			call: func(ctx context.Context, templates *GORMStackTemplateRepository, _ *GORMTemplateChartConfigRepository) error {
				return templates.Delete(ctx, "t-lock")
			},
		},
		{
			name: "StackTemplate.FindByIDForUpdate", table: "stack_templates", arg: "t-lock",
			call: func(ctx context.Context, templates *GORMStackTemplateRepository, _ *GORMTemplateChartConfigRepository) error {
				_, err := templates.FindByIDForUpdate(ctx, "t-lock")
				return err
			},
		},
		{
			name: "TemplateChart.Update", table: "template_chart_configs", arg: "tc-lock",
			call: func(ctx context.Context, _ *GORMStackTemplateRepository, charts *GORMTemplateChartConfigRepository) error {
				return charts.Update(ctx, &models.TemplateChartConfig{ID: "tc-lock", StackTemplateID: "t-lock", ChartName: "renamed"})
			},
		},
		{
			name: "TemplateChart.Delete", table: "template_chart_configs", arg: "tc-lock",
			call: func(ctx context.Context, _ *GORMStackTemplateRepository, charts *GORMTemplateChartConfigRepository) error {
				return charts.Delete(ctx, "tc-lock")
			},
		},
	}
	for _, op := range ops {
		for _, tt := range userContextCases {
			t.Run(op.name+"/"+tt.name, func(t *testing.T) {
				db, templates, charts := setupTemplateContextMySQL(t, 2)
				release := lockTableRows(t, db, op.table, "id = ?", op.arg)

				ctx, cancel := tt.ctx()
				defer cancel()
				start := time.Now()
				// A cancelled autocommit statement can still run on the server
				// after the lock is free, so the test checks only the client side.
				err := op.call(ctx, templates, charts)
				elapsed := time.Since(start)

				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				assert.Less(t, elapsed, 3*time.Second, "the context, not the lock wait timeout, ends the call")

				release()
				nextCtx, nextCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer nextCancel()
				_, err = templates.Count(nextCtx)
				require.NoError(t, err, "the pool serves the next query")
				_, err = charts.ListByTemplate(nextCtx, "t-lock")
				require.NoError(t, err, "the pool serves the next query")
			})
		}
	}
}

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

// userRepoCalls has one call of each UserRepository method (#656). The
// guarded changes use admin-1 as the caller and user-1 as the target.
var userRepoCalls = []struct {
	name string
	call func(ctx context.Context, r *GORMUserRepository) error
}{
	{name: "Create", call: func(ctx context.Context, r *GORMUserRepository) error {
		return r.Create(ctx, &models.User{ID: "new-1", Username: "new-1", Role: models.RoleUser})
	}},
	{name: "FindByID", call: func(ctx context.Context, r *GORMUserRepository) error {
		_, err := r.FindByID(ctx, "user-1")
		return err
	}},
	{name: "FindByIDs", call: func(ctx context.Context, r *GORMUserRepository) error {
		_, err := r.FindByIDs(ctx, []string{"user-1"})
		return err
	}},
	{name: "FindByUsername", call: func(ctx context.Context, r *GORMUserRepository) error {
		_, err := r.FindByUsername(ctx, "user-1")
		return err
	}},
	{name: "FindByExternalID", call: func(ctx context.Context, r *GORMUserRepository) error {
		_, err := r.FindByExternalID(ctx, "oidc", "ext-1")
		return err
	}},
	{name: "UpdatePassword", call: func(ctx context.Context, r *GORMUserRepository) error {
		return r.UpdatePassword(ctx, "user-1", "new-hash")
	}},
	{name: "UpdateProfile", call: func(ctx context.Context, r *GORMUserRepository) error {
		display, role := "changed", models.RoleDevOps
		return r.UpdateProfile(ctx, "user-1", models.UserProfileUpdate{DisplayName: &display, Role: &role})
	}},
	{name: "Delete", call: func(ctx context.Context, r *GORMUserRepository) error {
		return r.Delete(ctx, "user-1")
	}},
	{name: "List", call: func(ctx context.Context, r *GORMUserRepository) error {
		_, err := r.List(ctx)
		return err
	}},
	{name: "ListByRoles", call: func(ctx context.Context, r *GORMUserRepository) error {
		_, err := r.ListByRoles(ctx, []string{models.RoleAdmin})
		return err
	}},
	{name: "Count", call: func(ctx context.Context, r *GORMUserRepository) error {
		_, err := r.Count(ctx)
		return err
	}},
	{name: "UpdateRole", call: func(ctx context.Context, r *GORMUserRepository) error {
		_, err := r.UpdateRole(ctx, "admin-1", "user-1", models.RoleDevOps)
		return err
	}},
	{name: "SetDisabled", call: func(ctx context.Context, r *GORMUserRepository) error {
		return r.SetDisabled(ctx, "admin-1", "user-1", true)
	}},
	{name: "DeleteGuarded", call: func(ctx context.Context, r *GORMUserRepository) error {
		return r.DeleteGuarded(ctx, "admin-1", "user-1")
	}},
}

// seedContextUsers stores two admins and an SSO user (user-1).
func seedContextUsers(t *testing.T, r *GORMUserRepository) {
	t.Helper()
	ext := "ext-1"
	for _, u := range []*models.User{
		{ID: "admin-1", Username: "admin-1", Role: models.RoleAdmin},
		{ID: "admin-2", Username: "admin-2", Role: models.RoleAdmin},
		{ID: "user-1", Username: "user-1", DisplayName: "seed", Role: models.RoleUser,
			AuthProvider: "oidc", ExternalID: &ext, PasswordHash: "seed-hash"},
	} {
		require.NoError(t, r.Create(context.Background(), u))
	}
}

// TestGORMUserRepository_CancelledContext checks that every method runs its
// queries with the caller context: a cancelled context fails the call with a
// DatabaseError that keeps context.Canceled, and changes no row.
func TestGORMUserRepository_CancelledContext(t *testing.T) {
	t.Parallel()

	for _, tt := range userRepoCalls {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupUserRepo(t)
			seedContextUsers(t, repo)

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err := tt.call(ctx, repo)

			require.Error(t, err)
			assert.ErrorIs(t, err, context.Canceled)
			var dbErr *dberrors.DatabaseError
			assert.ErrorAs(t, err, &dbErr)

			users, err := repo.List(context.Background())
			require.NoError(t, err)
			assert.Len(t, users, 3, "no user was created or deleted")
			u, err := repo.FindByID(context.Background(), "user-1")
			require.NoError(t, err)
			assert.Equal(t, models.RoleUser, u.Role)
			assert.False(t, u.Disabled)
			assert.Equal(t, "seed", u.DisplayName)
			assert.Equal(t, "seed-hash", u.PasswordHash)
		})
	}
}

// TestGORMUserRepository_CancelBeforeCommit checks a cancel after the last
// statement of a transaction and before its commit. database/sql then rolls
// the transaction back and Commit returns sql.ErrTxDone; the error must still
// match context.Canceled (499, not 500), and nothing is stored.
func TestGORMUserRepository_CancelBeforeCommit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		lastTable string // the table of the last statement of the transaction
		call      func(ctx context.Context, r *GORMUserRepository) error
	}{
		{name: "UpdateRole", lastTable: "users", call: func(ctx context.Context, r *GORMUserRepository) error {
			_, err := r.UpdateRole(ctx, "admin-1", "user-1", models.RoleDevOps)
			return err
		}},
		{name: "SetDisabled", lastTable: "users", call: func(ctx context.Context, r *GORMUserRepository) error {
			return r.SetDisabled(ctx, "admin-1", "user-1", true)
		}},
		{name: "DeleteGuarded", lastTable: "instance_followers", call: func(ctx context.Context, r *GORMUserRepository) error {
			return r.DeleteGuarded(ctx, "admin-1", "user-1")
		}},
		{name: "Delete", lastTable: "instance_followers", call: func(ctx context.Context, r *GORMUserRepository) error {
			return r.Delete(ctx, "user-1")
		}},
		{name: "UpdateProfile", lastTable: "users", call: func(ctx context.Context, r *GORMUserRepository) error {
			display := "changed"
			return r.UpdateProfile(ctx, "user-1", models.UserProfileUpdate{DisplayName: &display})
		}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// A file, not :memory:: database/sql closes the connection of a
			// cancelled transaction, and an in-memory database goes with it.
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "users.db")), &gorm.Config{Logger: logger.Discard})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			require.NoError(t, db.AutoMigrate(&models.User{}, &models.InstanceFollower{}))
			repo := NewGORMUserRepository(db)
			seedContextUsers(t, repo)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			after := func(tx *gorm.DB) {
				if tx.Statement.Context == ctx && tx.Statement.Table == tt.lastTable {
					cancel()
					// Let database/sql roll the transaction back before the commit.
					time.Sleep(50 * time.Millisecond)
				}
			}
			require.NoError(t, db.Callback().Update().After("gorm:update").Register("test:cancel_after_update", after))
			require.NoError(t, db.Callback().Delete().After("gorm:delete").Register("test:cancel_after_delete", after))

			err = tt.call(ctx, repo)

			require.Error(t, err)
			assert.ErrorIs(t, err, context.Canceled)
			var dbErr *dberrors.DatabaseError
			assert.ErrorAs(t, err, &dbErr)
			u, err := repo.FindByID(context.Background(), "user-1")
			require.NoError(t, err, "the rolled back delete kept the user")
			assert.Equal(t, models.RoleUser, u.Role)
			assert.False(t, u.Disabled)
			assert.Equal(t, "seed", u.DisplayName)
		})
	}
}

// cancelBeforeStatements cancels ctx just before each statement of db runs,
// so a statement inside an open transaction sees the cancel.
func cancelBeforeStatements(t *testing.T, db *gorm.DB, cancel context.CancelFunc) {
	t.Helper()
	cb := func(*gorm.DB) { cancel() }
	cbs := db.Callback()
	require.NoError(t, cbs.Create().Before("gorm:create").Register("test:cancel_create", cb))
	require.NoError(t, cbs.Query().Before("gorm:query").Register("test:cancel_query", cb))
	require.NoError(t, cbs.Update().Before("gorm:update").Register("test:cancel_update", cb))
	require.NoError(t, cbs.Delete().Before("gorm:delete").Register("test:cancel_delete", cb))
	require.NoError(t, cbs.Row().Before("gorm:row").Register("test:cancel_row", cb))
	require.NoError(t, cbs.Raw().Before("gorm:raw").Register("test:cancel_raw", cb))
}

// TestGORMUserRepository_LogTraceContext checks that the SQL log lines of
// every method carry the trace and span ID of the caller context, also for
// the statements inside a transaction.
func TestGORMUserRepository_LogTraceContext(t *testing.T) {
	t.Parallel()

	for _, tt := range userRepoCalls {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			t.Run("SQL error", func(t *testing.T) {
				t.Parallel()
				// No users table: every query fails.
				db, records := newTraceTestDB(t)
				ctx, traceID, spanID := newTraceTestSpanContext(t)

				require.Error(t, tt.call(ctx, NewGORMUserRepository(db)))

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
				db, records := newTraceTestDB(t, &models.User{}, &models.InstanceFollower{})
				repo := NewGORMUserRepository(db)
				seedContextUsers(t, repo)
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

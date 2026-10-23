package database

import (
	"context"
	"reflect"
	"testing"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	otellog "go.opentelemetry.io/otel/log"
	"gorm.io/gorm"
)

// userFavoriteRepoCalls has one call of each UserFavoriteRepository method
// (#656). The calls work on the seed of seedContextFavorites.
var userFavoriteRepoCalls = []struct {
	name string
	call func(ctx context.Context, r models.UserFavoriteRepository) error
}{
	{name: "List", call: func(ctx context.Context, r models.UserFavoriteRepository) error {
		_, err := r.List(ctx, "u1")
		return err
	}},
	{name: "Add", call: func(ctx context.Context, r models.UserFavoriteRepository) error {
		return r.Add(ctx, &models.UserFavorite{UserID: "u1", EntityType: "template", EntityID: "tmpl-new"})
	}},
	{name: "Remove", call: func(ctx context.Context, r models.UserFavoriteRepository) error {
		return r.Remove(ctx, "u1", "template", "tmpl-2")
	}},
	{name: "IsFavorite", call: func(ctx context.Context, r models.UserFavoriteRepository) error {
		_, err := r.IsFavorite(ctx, "u1", "template", "tmpl-1")
		return err
	}},
}

// cleanupPolicyRepoCalls has one call of each CleanupPolicyRepository method
// (#656). The calls work on the seed of seedContextPolicies.
var cleanupPolicyRepoCalls = []struct {
	name string
	call func(ctx context.Context, r models.CleanupPolicyRepository) error
}{
	{name: "Create", call: func(ctx context.Context, r models.CleanupPolicyRepository) error {
		return r.Create(ctx, &models.CleanupPolicy{ID: "p-new", Name: "new", ClusterID: "all", Action: "stop", Condition: "idle_days:7", Schedule: "0 2 * * *"})
	}},
	{name: "FindByID", call: func(ctx context.Context, r models.CleanupPolicyRepository) error {
		_, err := r.FindByID(ctx, "p-1")
		return err
	}},
	{name: "Update", call: func(ctx context.Context, r models.CleanupPolicyRepository) error {
		return r.Update(ctx, &models.CleanupPolicy{ID: "p-1", Name: "renamed", ClusterID: "all", Action: "stop", Condition: "idle_days:7", Schedule: "0 2 * * *"})
	}},
	{name: "Delete", call: func(ctx context.Context, r models.CleanupPolicyRepository) error {
		return r.Delete(ctx, "p-2")
	}},
	{name: "List", call: func(ctx context.Context, r models.CleanupPolicyRepository) error {
		_, err := r.List(ctx)
		return err
	}},
	{name: "ListEnabled", call: func(ctx context.Context, r models.CleanupPolicyRepository) error {
		_, err := r.ListEnabled(ctx)
		return err
	}},
}

// auditLogRepoCalls has the calls of each AuditLogRepository method (#656).
// List has one call for each query path. The calls work on the seed of
// seedContextAuditLogs.
var auditLogRepoCalls = []struct {
	name   string
	method string
	call   func(ctx context.Context, r models.AuditLogRepository) error
}{
	{name: "Create", method: "Create", call: func(ctx context.Context, r models.AuditLogRepository) error {
		return r.Create(ctx, &models.AuditLog{ID: "a-new", UserID: "u1", Action: "create", EntityType: "stack_instance", EntityID: "si-9"})
	}},
	{name: "List unfiltered", method: "List", call: func(ctx context.Context, r models.AuditLogRepository) error {
		_, err := r.List(ctx, models.AuditLogFilters{Limit: 10})
		return err
	}},
	{name: "List filtered", method: "List", call: func(ctx context.Context, r models.AuditLogRepository) error {
		_, err := r.List(ctx, models.AuditLogFilters{Limit: 10, UserID: "u1"})
		return err
	}},
	{name: "List cursor", method: "List", call: func(ctx context.Context, r models.AuditLogRepository) error {
		_, err := r.List(ctx, models.AuditLogFilters{Limit: 10, Cursor: encodeAuditCursor(time.Now().Add(time.Hour), "zzz")})
		return err
	}},
}

// TestFavoritePolicyAuditRepoCalls_CoverInterface checks the guard tables
// against the interfaces.
func TestFavoritePolicyAuditRepoCalls_CoverInterface(t *testing.T) {
	t.Parallel()

	t.Run("UserFavoriteRepository", func(t *testing.T) {
		t.Parallel()
		var names []string
		for _, c := range userFavoriteRepoCalls {
			names = append(names, c.name)
		}
		assertCallsCoverInterface(t, reflect.TypeOf((*models.UserFavoriteRepository)(nil)).Elem(), names)
	})
	t.Run("CleanupPolicyRepository", func(t *testing.T) {
		t.Parallel()
		var names []string
		for _, c := range cleanupPolicyRepoCalls {
			names = append(names, c.name)
		}
		assertCallsCoverInterface(t, reflect.TypeOf((*models.CleanupPolicyRepository)(nil)).Elem(), names)
	})
	t.Run("AuditLogRepository", func(t *testing.T) {
		t.Parallel()
		names := map[string]bool{}
		for _, c := range auditLogRepoCalls {
			names[c.method] = true
		}
		var list []string
		for n := range names {
			list = append(list, n)
		}
		assertCallsCoverInterface(t, reflect.TypeOf((*models.AuditLogRepository)(nil)).Elem(), list)
	})
}

// seedContextFavorites stores two favorites of u1.
func seedContextFavorites(t *testing.T, r *GORMUserFavoriteRepository) {
	t.Helper()
	for _, id := range []string{"tmpl-1", "tmpl-2"} {
		require.NoError(t, r.Add(context.Background(), &models.UserFavorite{UserID: "u1", EntityType: "template", EntityID: id}))
	}
}

// seedContextPolicies stores the policies p-1 (enabled) and p-2.
func seedContextPolicies(t *testing.T, r *GORMCleanupPolicyRepository) {
	t.Helper()
	for _, p := range []*models.CleanupPolicy{
		{ID: "p-1", Name: "one", ClusterID: "all", Action: "stop", Condition: "idle_days:7", Schedule: "0 2 * * *", Enabled: true},
		{ID: "p-2", Name: "two", ClusterID: "all", Action: "stop", Condition: "idle_days:7", Schedule: "0 2 * * *"},
	} {
		require.NoError(t, r.Create(context.Background(), p))
	}
}

// seedContextAuditLogs stores two audit entries of u1.
func seedContextAuditLogs(t *testing.T, r *GORMAuditLogRepository) {
	t.Helper()
	for _, id := range []string{"a-1", "a-2"} {
		require.NoError(t, r.Create(context.Background(), &models.AuditLog{ID: id, UserID: "u1", Action: "create", EntityType: "stack_instance", EntityID: "si-" + id}))
	}
}

func countRows(t *testing.T, db *gorm.DB, model any) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(model).Count(&n).Error)
	return n
}

// TestFavoritePolicyAuditRepositories_CancelledContext checks that every
// method runs its queries with the caller context: a cancelled context fails
// the call with context.Canceled and changes no row (#656).
func TestFavoritePolicyAuditRepositories_CancelledContext(t *testing.T) {
	t.Parallel()

	assertCancelled := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
		var dbErr *dberrors.DatabaseError
		assert.ErrorAs(t, err, &dbErr)
	}

	for _, tt := range userFavoriteRepoCalls {
		tt := tt
		t.Run("UserFavorite."+tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupUserFavoriteRepo(t)
			seedContextFavorites(t, repo)

			assertCancelled(t, tt.call(cancelledContext(), repo))

			assert.EqualValues(t, 2, countRows(t, repo.db, &models.UserFavorite{}), "no favorite was added or removed")
		})
		t.Run("UserFavorite."+tt.name+"/live", func(t *testing.T) {
			t.Parallel()
			repo := setupUserFavoriteRepo(t)
			seedContextFavorites(t, repo)

			require.NoError(t, tt.call(context.Background(), repo))
		})
	}
	for _, tt := range cleanupPolicyRepoCalls {
		tt := tt
		t.Run("CleanupPolicy."+tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupCleanupPolicyRepo(t)
			seedContextPolicies(t, repo)

			assertCancelled(t, tt.call(cancelledContext(), repo))

			assert.EqualValues(t, 2, countRows(t, repo.DB, &models.CleanupPolicy{}), "no policy was added or removed")
			got, err := repo.FindByID(context.Background(), "p-1")
			require.NoError(t, err)
			assert.Equal(t, "one", got.Name, "p-1 keeps its name")
		})
		t.Run("CleanupPolicy."+tt.name+"/live", func(t *testing.T) {
			t.Parallel()
			repo := setupCleanupPolicyRepo(t)
			seedContextPolicies(t, repo)

			require.NoError(t, tt.call(context.Background(), repo))
		})
	}
	for _, tt := range auditLogRepoCalls {
		tt := tt
		t.Run("AuditLog."+tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupAuditLogRepo(t)
			seedContextAuditLogs(t, repo)

			assertCancelled(t, tt.call(cancelledContext(), repo))

			assert.EqualValues(t, 2, countRows(t, repo.db, &models.AuditLog{}), "no entry was added")
		})
		t.Run("AuditLog."+tt.name+"/live", func(t *testing.T) {
			t.Parallel()
			repo := setupAuditLogRepo(t)
			seedContextAuditLogs(t, repo)

			require.NoError(t, tt.call(context.Background(), repo))
		})
	}
}

// TestFavoritePolicyAuditRepositories_LogTraceContext checks that the SQL log
// lines of every method carry the trace and span ID of the caller context,
// for a failed and for a cancelled query, and inside a transaction that
// carries another span (#656).
func TestFavoritePolicyAuditRepositories_LogTraceContext(t *testing.T) {
	t.Parallel()

	type repos struct {
		favorites models.UserFavoriteRepository
		policies  models.CleanupPolicyRepository
		audit     models.AuditLogRepository
	}
	type traceCase struct {
		call  func(ctx context.Context, r repos) error
		seed  func(t *testing.T, db *gorm.DB)
		table any
		name  string
	}
	var cases []traceCase
	for _, c := range userFavoriteRepoCalls {
		c := c
		cases = append(cases, traceCase{
			name: "UserFavorite." + c.name, table: &models.UserFavorite{},
			call: func(ctx context.Context, r repos) error { return c.call(ctx, r.favorites) },
			seed: func(t *testing.T, db *gorm.DB) { seedContextFavorites(t, NewGORMUserFavoriteRepository(db)) },
		})
	}
	for _, c := range cleanupPolicyRepoCalls {
		c := c
		cases = append(cases, traceCase{
			name: "CleanupPolicy." + c.name, table: &models.CleanupPolicy{},
			call: func(ctx context.Context, r repos) error { return c.call(ctx, r.policies) },
			seed: func(t *testing.T, db *gorm.DB) { seedContextPolicies(t, NewGORMCleanupPolicyRepository(db)) },
		})
	}
	for _, c := range auditLogRepoCalls {
		c := c
		cases = append(cases, traceCase{
			name: "AuditLog." + c.name, table: &models.AuditLog{},
			call: func(ctx context.Context, r repos) error { return c.call(ctx, r.audit) },
			seed: func(t *testing.T, db *gorm.DB) { seedContextAuditLogs(t, NewGORMAuditLogRepository(db)) },
		})
	}
	on := func(db *gorm.DB) repos {
		return repos{
			favorites: NewGORMUserFavoriteRepository(db),
			policies:  NewGORMCleanupPolicyRepository(db),
			audit:     NewGORMAuditLogRepository(db),
		}
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

				require.Error(t, tt.call(ctx, on(db)))

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

				err := db.WithContext(txCtx).Transaction(func(tx *gorm.DB) error {
					return tt.call(ctx, on(tx))
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

				require.ErrorIs(t, tt.call(ctx, on(db)), context.Canceled)

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

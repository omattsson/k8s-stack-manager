package database

import (
	"context"
	"path/filepath"
	"reflect"
	"sync/atomic"
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

const testClusterEncryptionKey = "test-encryption-key-for-unit-tests"

// clusterRepoCalls has one call of each ClusterRepository method (#656). The
// calls work on the seed of seedContextClusters.
var clusterRepoCalls = []struct {
	name string
	call func(ctx context.Context, r *GORMClusterRepository) error
}{
	{name: "Create", call: func(ctx context.Context, r *GORMClusterRepository) error {
		return r.Create(ctx, &models.Cluster{
			ID: "cl-new", Name: "new", APIServerURL: "https://new.example.com", KubeconfigPath: "/kc/new",
		})
	}},
	{name: "FindByID", call: func(ctx context.Context, r *GORMClusterRepository) error {
		_, err := r.FindByID(ctx, "cl-1")
		return err
	}},
	{name: "Update", call: func(ctx context.Context, r *GORMClusterRepository) error {
		return r.Update(ctx, &models.Cluster{
			ID: "cl-2", Name: "renamed", APIServerURL: "https://two.example.com", KubeconfigPath: "/kc/two",
			HealthStatus: models.ClusterHealthy, CreatedAt: time.Now().UTC().Add(-time.Hour),
		})
	}},
	{name: "Delete", call: func(ctx context.Context, r *GORMClusterRepository) error {
		return r.Delete(ctx, "cl-2")
	}},
	{name: "List", call: func(ctx context.Context, r *GORMClusterRepository) error {
		_, err := r.List(ctx)
		return err
	}},
	{name: "NamesByIDs", call: func(ctx context.Context, r *GORMClusterRepository) error {
		_, err := r.NamesByIDs(ctx, []string{"cl-1", "cl-2"})
		return err
	}},
	{name: "CountAll", call: func(ctx context.Context, r *GORMClusterRepository) error {
		_, err := r.CountAll(ctx)
		return err
	}},
	{name: "CountByHealthStatus", call: func(ctx context.Context, r *GORMClusterRepository) error {
		_, err := r.CountByHealthStatus(ctx, models.ClusterHealthy)
		return err
	}},
	{name: "FindDefault", call: func(ctx context.Context, r *GORMClusterRepository) error {
		_, err := r.FindDefault(ctx)
		return err
	}},
	{name: "SetDefault", call: func(ctx context.Context, r *GORMClusterRepository) error {
		return r.SetDefault(ctx, "cl-2")
	}},
}

// TestClusterRepoCalls_CoverInterface checks that every ClusterRepository
// method takes ctx first and has a row in clusterRepoCalls, so a new method
// cannot skip the guard tests.
func TestClusterRepoCalls_CoverInterface(t *testing.T) {
	t.Parallel()
	iface := reflect.TypeOf((*models.ClusterRepository)(nil)).Elem()
	ctxType := reflect.TypeOf((*context.Context)(nil)).Elem()
	want := make([]string, 0, iface.NumMethod())
	for i := 0; i < iface.NumMethod(); i++ {
		m := iface.Method(i)
		want = append(want, m.Name)
		if assert.Positive(t, m.Type.NumIn(), m.Name) {
			assert.Equal(t, ctxType, m.Type.In(0), "%s takes ctx first", m.Name)
		}
	}
	got := make([]string, 0, len(clusterRepoCalls))
	for _, c := range clusterRepoCalls {
		got = append(got, c.name)
	}
	assert.ElementsMatch(t, want, got)
}

// seedContextClusters stores cl-1 (default, healthy, kubeconfig data) and
// cl-2 (not default, unreachable).
func seedContextClusters(t *testing.T, r *GORMClusterRepository) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, r.Create(ctx, &models.Cluster{
		ID: "cl-1", Name: "one", APIServerURL: "https://one.example.com", KubeconfigData: "kubeconfig-one",
		HealthStatus: models.ClusterHealthy, IsDefault: true,
	}))
	require.NoError(t, r.Create(ctx, &models.Cluster{
		ID: "cl-2", Name: "two", APIServerURL: "https://two.example.com", KubeconfigPath: "/kc/two",
		HealthStatus: models.ClusterUnreachable,
	}))
}

// assertClusterSeedUnchanged checks that the seed of seedContextClusters is
// unchanged.
func assertClusterSeedUnchanged(t *testing.T, r *GORMClusterRepository) {
	t.Helper()
	var rows []models.Cluster
	require.NoError(t, r.db.Order("id").Find(&rows).Error)
	require.Len(t, rows, 2, "no cluster was created or deleted")
	assert.Equal(t, "one", rows[0].Name)
	assert.True(t, rows[0].IsDefault, "cl-1 stays the default")
	assert.Equal(t, "two", rows[1].Name, "cl-2 keeps its name")
	assert.False(t, rows[1].IsDefault, "cl-2 does not become the default")
	assert.Equal(t, models.ClusterUnreachable, rows[1].HealthStatus)
}

// TestGORMClusterRepository_CancelledContext checks that every method runs
// its queries with the caller context: a cancelled context fails the call
// with context.Canceled and changes no row.
func TestGORMClusterRepository_CancelledContext(t *testing.T) {
	t.Parallel()

	for _, tt := range clusterRepoCalls {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupClusterRepo(t)
			seedContextClusters(t, repo)

			err := tt.call(cancelledContext(), repo)

			require.Error(t, err)
			assert.ErrorIs(t, err, context.Canceled)
			var dbErr *dberrors.DatabaseError
			assert.ErrorAs(t, err, &dbErr)
			assertClusterSeedUnchanged(t, repo)
		})
	}
}

// fileClusterRepo returns a cluster repository on a SQLite file with the seed
// of seedContextClusters. A file, not :memory:: database/sql closes the
// connection of a cancelled transaction, and an in-memory database goes with it.
func fileClusterRepo(t *testing.T) *GORMClusterRepository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "clusters.db")), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&models.Cluster{}))
	repo := NewGORMClusterRepository(db, testClusterEncryptionKey)
	seedContextClusters(t, repo)
	return repo
}

// cancelAfterUpdate cancels ctx after update number n that runs with ctx. A
// positive wait lets database/sql roll the transaction back before the next
// step.
func cancelAfterUpdate(t *testing.T, db *gorm.DB, ctx context.Context, cancel context.CancelFunc, n int32, wait time.Duration) *atomic.Int32 {
	t.Helper()
	var updates atomic.Int32
	require.NoError(t, db.Callback().Update().After("gorm:update").Register("test:cancel_after_update", func(tx *gorm.DB) {
		if tx.Statement.Context != ctx {
			return
		}
		if updates.Add(1) == n {
			cancel()
			time.Sleep(wait)
		}
	}))
	return &updates
}

// TestGORMClusterRepository_SetDefaultCancelMidTransaction cancels SetDefault
// after it cleared the old default: the update of the new default fails, the
// transaction rolls back, and the old default stays.
func TestGORMClusterRepository_SetDefaultCancelMidTransaction(t *testing.T) {
	t.Parallel()
	repo := fileClusterRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates := cancelAfterUpdate(t, repo.db, ctx, cancel, 1, 0)

	err := repo.SetDefault(ctx, "cl-2")

	// The callback also runs after the failed second update.
	require.Equal(t, int32(2), updates.Load(), "both updates ran with the caller context")
	require.ErrorIs(t, err, context.Canceled)
	var dbErr *dberrors.DatabaseError
	assert.ErrorAs(t, err, &dbErr)
	assertClusterSeedUnchanged(t, repo)
}

// TestGORMClusterRepository_SetDefaultCancelBeforeCommit cancels SetDefault
// after both updates and before the commit. database/sql then rolls the
// transaction back and the commit returns sql.ErrTxDone; the error must still
// match context.Canceled (499, not 500), and nothing is stored.
func TestGORMClusterRepository_SetDefaultCancelBeforeCommit(t *testing.T) {
	t.Parallel()
	repo := fileClusterRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates := cancelAfterUpdate(t, repo.db, ctx, cancel, 2, 50*time.Millisecond)

	err := repo.SetDefault(ctx, "cl-2")

	require.Equal(t, int32(2), updates.Load(), "both updates ran")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	var dbErr *dberrors.DatabaseError
	assert.ErrorAs(t, err, &dbErr)
	assertClusterSeedUnchanged(t, repo)
}

// TestGORMClusterRepository_LiveContext checks the methods with a live
// context, so the guard table above does not pass on calls that always fail.
func TestGORMClusterRepository_LiveContext(t *testing.T) {
	t.Parallel()

	for _, tt := range clusterRepoCalls {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupClusterRepo(t)
			seedContextClusters(t, repo)

			require.NoError(t, tt.call(context.Background(), repo))
		})
	}
}

// TestGORMClusterRepository_LogTraceContext checks that the SQL log lines of
// every method carry the trace and span ID of the caller context, also for
// the statements inside the SetDefault transaction.
func TestGORMClusterRepository_LogTraceContext(t *testing.T) {
	t.Parallel()

	for _, tt := range clusterRepoCalls {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			t.Run("SQL error", func(t *testing.T) {
				t.Parallel()
				// No clusters table: every query fails.
				db, records := newTraceTestDB(t)
				ctx, traceID, spanID := newTraceTestSpanContext(t)

				require.Error(t, tt.call(ctx, NewGORMClusterRepository(db, testClusterEncryptionKey)))

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
				db, records := newTraceTestDB(t, &models.Cluster{})
				repo := NewGORMClusterRepository(db, testClusterEncryptionKey)
				seedContextClusters(t, repo)
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

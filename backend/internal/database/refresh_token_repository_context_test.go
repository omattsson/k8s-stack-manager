package database

import (
	"context"
	"path/filepath"
	"reflect"
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

// refreshTokenRepoCalls has one call of each RefreshTokenRepository method
// (#656). The calls work on the seed of seedContextRefreshTokens.
var refreshTokenRepoCalls = []struct {
	name string
	call func(ctx context.Context, r *GORMRefreshTokenRepository) error
}{
	{name: "Create", call: func(ctx context.Context, r *GORMRefreshTokenRepository) error {
		now := time.Now().UTC()
		return r.Create(ctx, &models.RefreshToken{
			ID: "rt-new", UserID: "u1", FamilyID: "f1", TokenHash: "hash-rt-new",
			ExpiresAt: now.Add(time.Hour), LastActivity: now, CreatedAt: now, SessionStartedAt: now,
		})
	}},
	{name: "FindByTokenHash", call: func(ctx context.Context, r *GORMRefreshTokenRepository) error {
		_, err := r.FindByTokenHash(ctx, "hash-rt1")
		return err
	}},
	{name: "RevokeByID", call: func(ctx context.Context, r *GORMRefreshTokenRepository) error {
		return r.RevokeByID(ctx, "rt1")
	}},
	{name: "MarkRotatedIfActive", call: func(ctx context.Context, r *GORMRefreshTokenRepository) error {
		_, err := r.MarkRotatedIfActive(ctx, "rt1", time.Now().UTC())
		return err
	}},
	{name: "RevokeFamily", call: func(ctx context.Context, r *GORMRefreshTokenRepository) error {
		return r.RevokeFamily(ctx, "f1")
	}},
	{name: "CountActiveInFamily", call: func(ctx context.Context, r *GORMRefreshTokenRepository) error {
		_, err := r.CountActiveInFamily(ctx, "f1")
		return err
	}},
	{name: "TouchFamily", call: func(ctx context.Context, r *GORMRefreshTokenRepository) error {
		return r.TouchFamily(ctx, "f1", time.Now().UTC())
	}},
	{name: "RevokeAllForUser", call: func(ctx context.Context, r *GORMRefreshTokenRepository) error {
		return r.RevokeAllForUser(ctx, "u1")
	}},
	{name: "RevokeAllForUserExcept", call: func(ctx context.Context, r *GORMRefreshTokenRepository) error {
		return r.RevokeAllForUserExcept(ctx, "u1", "rt1")
	}},
	{name: "RevokeOldestForUser", call: func(ctx context.Context, r *GORMRefreshTokenRepository) error {
		_, err := r.RevokeOldestForUser(ctx, "u1", 0, "")
		return err
	}},
	{name: "DeleteExpired", call: func(ctx context.Context, r *GORMRefreshTokenRepository) error {
		_, err := r.DeleteExpired(ctx)
		return err
	}},
	{name: "CountActiveForUser", call: func(ctx context.Context, r *GORMRefreshTokenRepository) error {
		_, err := r.CountActiveForUser(ctx, "u1")
		return err
	}},
	{name: "WithTx", call: func(ctx context.Context, r *GORMRefreshTokenRepository) error {
		return r.WithTx(ctx, func(tx models.RefreshTokenRepository) error {
			return tx.RevokeAllForUser(ctx, "u1")
		})
	}},
}

// TestRefreshTokenRepoCalls_CoverInterface checks that every
// RefreshTokenRepository method takes ctx first and has a row in
// refreshTokenRepoCalls, so a new method cannot skip the guard tests.
func TestRefreshTokenRepoCalls_CoverInterface(t *testing.T) {
	t.Parallel()
	iface := reflect.TypeOf((*models.RefreshTokenRepository)(nil)).Elem()
	ctxType := reflect.TypeOf((*context.Context)(nil)).Elem()
	want := make([]string, 0, iface.NumMethod())
	for i := 0; i < iface.NumMethod(); i++ {
		m := iface.Method(i)
		want = append(want, m.Name)
		if assert.Positive(t, m.Type.NumIn(), m.Name) {
			assert.Equal(t, ctxType, m.Type.In(0), "%s takes ctx first", m.Name)
		}
	}
	got := make([]string, 0, len(refreshTokenRepoCalls))
	for _, c := range refreshTokenRepoCalls {
		got = append(got, c.name)
	}
	assert.ElementsMatch(t, want, got)
}

// seedContextRefreshTokens stores rt1 and rt2 (family f1, active) and
// rt-expired (family f2, expired), all of user u1.
func seedContextRefreshTokens(t *testing.T, r *GORMRefreshTokenRepository) {
	t.Helper()
	seedRefreshToken(t, r, "rt1", "f1", nil)
	seedRefreshToken(t, r, "rt2", "f1", nil)
	seedRefreshToken(t, r, "rt-expired", "f2", func(rt *models.RefreshToken) {
		rt.ExpiresAt = time.Now().UTC().Add(-time.Hour)
	})
}

// assertRefreshSeedUnchanged checks that the seed of seedContextRefreshTokens
// is unchanged.
func assertRefreshSeedUnchanged(t *testing.T, r *GORMRefreshTokenRepository) {
	t.Helper()
	var n int64
	require.NoError(t, r.db.Model(&models.RefreshToken{}).Count(&n).Error)
	assert.Equal(t, int64(3), n, "no token was created or deleted")
	for _, id := range []string{"rt1", "rt2", "rt-expired"} {
		rt := loadRefreshToken(t, r, id)
		assert.False(t, rt.Revoked, "%s is not revoked", id)
		assert.Nil(t, rt.RotatedAt, "%s is not rotated", id)
		assert.WithinDuration(t, time.Now().UTC().Add(-10*time.Minute), rt.LastActivity, time.Minute, "%s keeps its activity", id)
	}
}

// TestGORMRefreshTokenRepository_CancelledContext checks that every method
// runs its queries with the caller context: a cancelled context fails the
// call with context.Canceled and changes no row.
func TestGORMRefreshTokenRepository_CancelledContext(t *testing.T) {
	t.Parallel()

	for _, tt := range refreshTokenRepoCalls {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupRefreshTokenRepo(t)
			seedContextRefreshTokens(t, repo)

			err := tt.call(cancelledContext(), repo)

			require.Error(t, err)
			assert.ErrorIs(t, err, context.Canceled)
			assertRefreshSeedUnchanged(t, repo)
		})
	}
}

// TestGORMRefreshTokenRepository_WithTxCancelledContextRunsNoWrite checks
// that WithTx starts its transaction with ctx: with a cancelled ctx, fn never
// runs (even a write with a live context inside fn), and the error is a
// DatabaseError that keeps context.Canceled.
func TestGORMRefreshTokenRepository_WithTxCancelledContextRunsNoWrite(t *testing.T) {
	t.Parallel()
	repo := setupRefreshTokenRepo(t)
	seedContextRefreshTokens(t, repo)
	ran := false

	err := repo.WithTx(cancelledContext(), func(tx models.RefreshTokenRepository) error {
		ran = true
		return tx.RevokeAllForUser(context.Background(), "u1")
	})

	require.ErrorIs(t, err, context.Canceled)
	var dbErr *dberrors.DatabaseError
	assert.ErrorAs(t, err, &dbErr)
	assert.False(t, ran, "fn does not run")
	assertRefreshSeedUnchanged(t, repo)
}

// TestGORMRefreshTokenRepository_TxRepoUsesCallerContext checks a repository
// of WithTx: a call with a cancelled context fails also inside a live
// transaction, and the transaction commits nothing.
func TestGORMRefreshTokenRepository_TxRepoUsesCallerContext(t *testing.T) {
	t.Parallel()
	repo := setupRefreshTokenRepo(t)
	seedContextRefreshTokens(t, repo)

	err := repo.WithTx(context.Background(), func(tx models.RefreshTokenRepository) error {
		if _, err := tx.MarkRotatedIfActive(context.Background(), "rt1", time.Now().UTC()); err != nil {
			return err
		}
		return tx.RevokeFamily(cancelledContext(), "f1")
	})

	require.ErrorIs(t, err, context.Canceled)
	assertRefreshSeedUnchanged(t, repo)
}

// TestGORMRefreshTokenRepository_WithTxCancelBeforeCommit cancels a rotation
// after its write and before the commit. database/sql then rolls the
// transaction back and the commit returns sql.ErrTxDone; the error must
// still match context.Canceled (499, not 500), and nothing is stored.
func TestGORMRefreshTokenRepository_WithTxCancelBeforeCommit(t *testing.T) {
	t.Parallel()
	// A file, not :memory:: database/sql closes the connection of a cancelled
	// transaction, and an in-memory database goes with it.
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "refresh.db")), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&models.RefreshToken{}))
	repo := NewGORMRefreshTokenRepository(db)
	seedContextRefreshTokens(t, repo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = repo.WithTx(ctx, func(tx models.RefreshTokenRepository) error {
		affected, err := tx.MarkRotatedIfActive(ctx, "rt1", time.Now().UTC())
		if err != nil {
			return err
		}
		require.Equal(t, int64(1), affected)
		cancel()
		// Let database/sql roll the transaction back before the commit.
		time.Sleep(50 * time.Millisecond)
		return nil
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	var dbErr *dberrors.DatabaseError
	assert.ErrorAs(t, err, &dbErr)
	assertRefreshSeedUnchanged(t, repo)
}

// TestGORMRefreshTokenRepository_RevokeOldestCancelAfterRead cancels after
// the ID read of RevokeOldestForUser: the update must also run with the
// caller context, so nothing is revoked.
func TestGORMRefreshTokenRepository_RevokeOldestCancelAfterRead(t *testing.T) {
	t.Parallel()
	repo := setupRefreshTokenRepo(t)
	seedContextRefreshTokens(t, repo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	read := false
	require.NoError(t, repo.db.Callback().Query().After("gorm:query").Register("test:cancel_after_pluck", func(tx *gorm.DB) {
		if tx.Statement.Context == ctx {
			read = true
			cancel()
		}
	}))

	n, err := repo.RevokeOldestForUser(ctx, "u1", 0, "")

	require.True(t, read, "the ID read ran with the caller context")
	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, n)
	assertRefreshSeedUnchanged(t, repo)
}

// TestGORMRefreshTokenRepository_LogTraceContext checks that the SQL log
// lines of every method carry the trace and span ID of the caller context.
func TestGORMRefreshTokenRepository_LogTraceContext(t *testing.T) {
	t.Parallel()

	for _, tt := range refreshTokenRepoCalls {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			t.Run("SQL error", func(t *testing.T) {
				t.Parallel()
				// No refresh_tokens table: every query fails.
				db, records := newTraceTestDB(t)
				ctx, traceID, spanID := newTraceTestSpanContext(t)

				require.Error(t, tt.call(ctx, NewGORMRefreshTokenRepository(db)))

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
				db, records := newTraceTestDB(t, &models.RefreshToken{})
				repo := NewGORMRefreshTokenRepository(db)
				seedContextRefreshTokens(t, repo)
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

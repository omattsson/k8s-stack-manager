package database

import (
	"context"
	"testing"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	otellog "go.opentelemetry.io/otel/log"
)

// apiKeyRepoCalls has one call of each APIKeyRepository method (#656).
var apiKeyRepoCalls = []struct {
	name string
	call func(ctx context.Context, r *GORMAPIKeyRepository) error
}{
	{name: "Create", call: func(ctx context.Context, r *GORMAPIKeyRepository) error {
		return r.Create(ctx, &models.APIKey{UserID: "u-ctx", Name: "new", Prefix: "p-new", KeyHash: "h"})
	}},
	{name: "FindByID", call: func(ctx context.Context, r *GORMAPIKeyRepository) error {
		_, err := r.FindByID(ctx, "u-ctx", "k-ctx")
		return err
	}},
	{name: "FindByIDs", call: func(ctx context.Context, r *GORMAPIKeyRepository) error {
		_, err := r.FindByIDs(ctx, []string{"k-ctx"})
		return err
	}},
	{name: "FindByPrefix", call: func(ctx context.Context, r *GORMAPIKeyRepository) error {
		_, err := r.FindByPrefix(ctx, "p-ctx")
		return err
	}},
	{name: "ListByUser", call: func(ctx context.Context, r *GORMAPIKeyRepository) error {
		_, err := r.ListByUser(ctx, "u-ctx")
		return err
	}},
	{name: "UpdateLastUsed", call: func(ctx context.Context, r *GORMAPIKeyRepository) error {
		return r.UpdateLastUsed(ctx, "u-ctx", "k-ctx", time.Now())
	}},
	{name: "Delete", call: func(ctx context.Context, r *GORMAPIKeyRepository) error {
		return r.Delete(ctx, "u-ctx", "k-ctx")
	}},
	{name: "DeleteAllForUser", call: func(ctx context.Context, r *GORMAPIKeyRepository) error {
		_, err := r.DeleteAllForUser(ctx, "u-ctx")
		return err
	}},
}

// TestGORMAPIKeyRepository_CancelledContext checks that every method runs its
// query with the caller context: a cancelled context fails the call, and the
// error keeps context.Canceled for errors.Is.
func TestGORMAPIKeyRepository_CancelledContext(t *testing.T) {
	t.Parallel()

	for _, tt := range apiKeyRepoCalls {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupAPIKeyRepo(t)
			require.NoError(t, repo.Create(context.Background(),
				&models.APIKey{ID: "k-ctx", UserID: "u-ctx", Name: "seed", Prefix: "p-ctx", KeyHash: "h"}))

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err := tt.call(ctx, repo)

			require.Error(t, err)
			assert.ErrorIs(t, err, context.Canceled)
			var dbErr *dberrors.DatabaseError
			assert.ErrorAs(t, err, &dbErr)

			keys, err := repo.ListByUser(context.Background(), "u-ctx")
			require.NoError(t, err)
			require.Len(t, keys, 1, "the cancelled call changed nothing")
			assert.Nil(t, keys[0].LastUsedAt)
		})
	}
}

// TestGORMAPIKeyRepository_LogTraceContext checks that the SQL log lines of
// every method carry the trace and span ID of the caller context.
func TestGORMAPIKeyRepository_LogTraceContext(t *testing.T) {
	t.Parallel()

	for _, tt := range apiKeyRepoCalls {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			t.Run("SQL error", func(t *testing.T) {
				t.Parallel()
				// No api_keys table: every query fails.
				db, records := newTraceTestDB(t)
				ctx, traceID, spanID := newTraceTestSpanContext(t)

				require.Error(t, tt.call(ctx, NewGORMAPIKeyRepository(db)))

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
				db, records := newTraceTestDB(t, &models.APIKey{})
				spanCtx, traceID, spanID := newTraceTestSpanContext(t)
				ctx, cancel := context.WithCancel(spanCtx)
				cancel()

				require.ErrorIs(t, tt.call(ctx, NewGORMAPIKeyRepository(db)), context.Canceled)

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

package database

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGORMNotificationRepository_ListByUser_TotalCap checks the windowed
// total of ListByUser (#654): exact up to offset + cap, then
// offset + cap with capped = true. The cap is lowered to 5, so the test
// needs no 1000 rows.
func TestGORMNotificationRepository_ListByUser_TotalCap(t *testing.T) {
	t.Parallel()

	const testCap = 5
	tests := []struct {
		name       string
		unread     int // unread rows of the user
		read       int // read rows of the user
		unreadOnly bool
		limit      int
		offset     int
		wantLen    int
		wantTotal  int64
		wantCapped bool
	}{
		{name: "below the cap is exact", unread: 2, read: 1, limit: 10, wantLen: 3, wantTotal: 3},
		{name: "exactly at the cap is exact", unread: 3, read: 2, limit: 10, wantLen: 5, wantTotal: 5},
		{name: "one above the cap is capped", unread: 3, read: 3, limit: 10, wantLen: 6, wantTotal: 5, wantCapped: true},
		{name: "far above the cap is capped", unread: 10, read: 10, limit: 2, wantLen: 2, wantTotal: 5, wantCapped: true},
		{name: "offset moves the window", unread: 10, read: 10, limit: 2, offset: 3, wantLen: 2, wantTotal: 8, wantCapped: true},
		{name: "offset with an exact total", unread: 4, read: 3, limit: 2, offset: 3, wantLen: 2, wantTotal: 7},
		{name: "offset at offset + cap is exact", unread: 4, read: 4, limit: 2, offset: 3, wantLen: 2, wantTotal: 8},
		{name: "offset past the end gives the true count", unread: 2, read: 2, limit: 10, offset: 50, wantLen: 0, wantTotal: 4},
		{name: "unread only below the cap", unread: 3, read: 10, unreadOnly: true, limit: 10, wantLen: 3, wantTotal: 3},
		{name: "unread only above the cap", unread: 8, read: 1, unreadOnly: true, limit: 2, wantLen: 2, wantTotal: 5, wantCapped: true},
		{name: "huge offset does not overflow", unread: 3, read: 3, limit: 10, offset: math.MaxInt64, wantLen: 0, wantTotal: 6},
		{name: "offset just below the overflow limit", unread: 3, read: 3, limit: 10, offset: math.MaxInt64 - testCap - 1, wantLen: 0, wantTotal: 6},
		{name: "negative offset counts from 0", unread: 3, read: 3, limit: 10, offset: -4, wantLen: 6, wantTotal: 5, wantCapped: true},
		{name: "no rows", limit: 10, wantLen: 0, wantTotal: 0},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupNotificationRepo(t)
			repo.totalCap = testCap
			ctx := context.Background()

			now := time.Now().UTC()
			for i := 0; i < tt.unread+tt.read; i++ {
				require.NoError(t, repo.Create(ctx, &models.Notification{
					ID: fmt.Sprintf("n-%d", i), UserID: "u-cap", Type: "deployment.success", Title: "t",
					IsRead: i >= tt.unread, CreatedAt: now.Add(-time.Duration(i) * time.Second),
				}))
			}
			// Rows of another user never count.
			for i := 0; i < 2*testCap; i++ {
				require.NoError(t, repo.Create(ctx, &models.Notification{
					ID: fmt.Sprintf("o-%d", i), UserID: "u-other", Type: "deployment.success", Title: "t", CreatedAt: now,
				}))
			}

			list, total, capped, err := repo.ListByUser(ctx, "u-cap", tt.unreadOnly, tt.limit, tt.offset)
			require.NoError(t, err)
			assert.Len(t, list, tt.wantLen)
			assert.Equal(t, tt.wantTotal, total)
			assert.Equal(t, tt.wantCapped, capped)
			for _, n := range list {
				assert.Equal(t, "u-cap", n.UserID)
				if tt.unreadOnly {
					assert.False(t, n.IsRead)
				}
			}
		})
	}
}

// TestNewGORMNotificationRepository_TotalCap checks the default cap.
func TestNewGORMNotificationRepository_TotalCap(t *testing.T) {
	t.Parallel()
	repo := NewGORMNotificationRepository(setupTestDBWithAllTables(t))
	assert.Equal(t, models.NotificationTotalCap, repo.totalCap)
	assert.Equal(t, 1000, models.NotificationTotalCap)
}

// TestGORMNotificationRepository_ListByUser_CountError checks that a failed
// count query gives a database error and no page.
func TestGORMNotificationRepository_ListByUser_CountError(t *testing.T) {
	t.Parallel()
	db := setupTestDBWithAllTables(t)
	repo := NewGORMNotificationRepository(db)
	require.NoError(t, db.Migrator().DropTable(&models.Notification{}))

	list, total, capped, err := repo.ListByUser(context.Background(), "u1", false, 10, 0)
	require.Error(t, err)
	assert.Nil(t, list)
	assert.Zero(t, total)
	assert.False(t, capped)
}

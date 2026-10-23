//go:build integration

package database

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"backend/internal/models"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDatabaseNotificationCreateMarkAllReadNoDeadlock runs concurrent
// Create and MarkAllAsRead calls for one user on MySQL (#648): no
// call may fail with a deadlock, and every notification is stored once.
func TestDatabaseNotificationCreateMarkAllReadNoDeadlock(t *testing.T) {
	const (
		userID    = "notify-user"
		creators  = 8
		perWorker = 25
		markers   = 3
		minRounds = 3
		maxRounds = 20
	)

	db := setupMySQLTestDB(t)
	// An older test schema can still have the index that migration 62 drops.
	require.NoError(t, dropNotificationIsReadIndexMigration().Up(db))
	assert.False(t, db.Migrator().HasIndex(&models.Notification{}, notificationIsReadIndexName))

	repo := NewGORMNotificationRepository(db)
	ctx := context.Background()
	retriesBefore := deadlockRetries.Load()

	// Run until at least one deadlock was retried, so the test exercises the retry path.
	for round := 0; round < maxRounds && (round < minRounds || deadlockRetries.Load() == retriesBefore); round++ {
		require.NoError(t, db.Exec("DELETE FROM notifications").Error)

		var wg sync.WaitGroup
		start := make(chan struct{})
		created := make(chan string, creators*perWorker)
		errs := make(chan error, creators*perWorker+markers*perWorker*2)
		for w := 0; w < creators; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				<-start
				for i := 0; i < perWorker; i++ {
					id := uuid.New().String()
					err := repo.Create(ctx, &models.Notification{ID: id, UserID: userID, Type: "instance.deleted",
						Title: fmt.Sprintf("round %d worker %d #%d", round, w, i)})
					if err != nil {
						errs <- fmt.Errorf("create: %w", err)
						continue
					}
					created <- id
				}
			}(w)
		}
		for w := 0; w < markers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for i := 0; i < perWorker; i++ {
					if err := repo.MarkAllAsRead(ctx, userID); err != nil {
						errs <- fmt.Errorf("mark all read: %w", err)
					}
				}
			}()
		}
		close(start)
		wg.Wait()
		close(created)
		close(errs)

		for err := range errs {
			assert.False(t, isDeadlockError(err), "round %d: deadlock after the retries: %v", round, err)
			assert.NoError(t, err, "round %d", round)
		}
		ids := map[string]bool{}
		for id := range created {
			ids[id] = true
		}
		require.Len(t, ids, creators*perWorker, "round %d: every create succeeded", round)

		var stored int64
		require.NoError(t, db.Model(&models.Notification{}).Where("user_id = ?", userID).Count(&stored).Error)
		assert.Equal(t, int64(creators*perWorker), stored, "round %d: every notification stored once", round)

		// A final mark all read leaves no unread notification.
		require.NoError(t, repo.MarkAllAsRead(ctx, userID))
		unread, _, err := repo.CountUnread(ctx, userID)
		require.NoError(t, err)
		assert.Zero(t, unread, "round %d", round)
	}
	// MySQL decides when it deadlocks, so zero retries is not a failure.
	// TestGORMNotificationRepository_DeadlockRetry proves the retry path.
	t.Logf("deadlock retries: %d", deadlockRetries.Load()-retriesBefore)
}

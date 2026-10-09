package database

import (
	"context"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupWSEventRepo(t *testing.T) *GORMWSEventRepository {
	t.Helper()
	db := setupTestDBWithAllTables(t)
	require.NoError(t, wsEventsMigration().Up(db))
	return NewGORMWSEventRepository(db)
}

func TestWSEventsMigration(t *testing.T) {
	t.Parallel()
	db := setupTestDBWithAllTables(t)
	m := db.Migrator()
	mig := wsEventsMigration()

	require.NoError(t, mig.Up(db))
	require.NoError(t, mig.Up(db), "Up must be idempotent")
	assert.True(t, m.HasTable("ws_events"))
	assert.True(t, m.HasIndex(&models.WSEvent{}, "idx_ws_events_created_at"))

	require.NoError(t, mig.Down(db))
	assert.False(t, m.HasTable("ws_events"))
	require.NoError(t, mig.Down(db), "Down must be idempotent")
}

func TestGORMWSEventRepository_InsertAndList(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := setupWSEventRepo(t)

	maxID, err := repo.MaxID(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), maxID, "empty table")

	now := time.Now().UTC()
	events := []*models.WSEvent{
		{CreatedAt: now, Target: models.WSEventTargetAll, Origin: "a", Payload: `{"n":1}`},
		{CreatedAt: now, Target: "instance:i1", Origin: "b", Payload: `{"n":2}`},
		{CreatedAt: now, Target: "user:u1", Origin: "a", Payload: `{"n":3}`},
	}
	require.NoError(t, repo.Insert(ctx, events))
	require.NoError(t, repo.Insert(ctx, nil), "empty insert is a no-op")
	for i, ev := range events {
		require.NotZero(t, ev.ID, "event %d gets an ID", i)
	}
	assert.Less(t, events[0].ID, events[1].ID)
	assert.Less(t, events[1].ID, events[2].ID)

	maxID, err = repo.MaxID(ctx)
	require.NoError(t, err)
	assert.Equal(t, events[2].ID, maxID)

	tests := []struct {
		name         string
		afterID      int64
		limit        int
		skipOrigin   string
		wantIDs      []int64
		wantPayloads []string
	}{
		{
			name:         "all rows, own origin a has no payload",
			afterID:      0,
			limit:        10,
			skipOrigin:   "a",
			wantIDs:      []int64{events[0].ID, events[1].ID, events[2].ID},
			wantPayloads: []string{"", `{"n":2}`, ""},
		},
		{
			name:         "after the first row",
			afterID:      events[0].ID,
			limit:        10,
			skipOrigin:   "b",
			wantIDs:      []int64{events[1].ID, events[2].ID},
			wantPayloads: []string{"", `{"n":3}`},
		},
		{
			name:         "limit",
			afterID:      0,
			limit:        2,
			skipOrigin:   "c",
			wantIDs:      []int64{events[0].ID, events[1].ID},
			wantPayloads: []string{`{"n":1}`, `{"n":2}`},
		},
		{
			name:       "nothing after the last row",
			afterID:    events[2].ID,
			limit:      10,
			skipOrigin: "a",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := repo.ListAfter(ctx, tt.afterID, tt.limit, tt.skipOrigin)
			require.NoError(t, err)
			require.Len(t, got, len(tt.wantIDs))
			for i, ev := range got {
				assert.Equal(t, tt.wantIDs[i], ev.ID)
				assert.Equal(t, tt.wantPayloads[i], ev.Payload)
				assert.NotEmpty(t, ev.Target)
				assert.NotEmpty(t, ev.Origin)
			}
		})
	}
}

func TestGORMWSEventRepository_ListByIDs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := setupWSEventRepo(t)

	now := time.Now().UTC()
	events := []*models.WSEvent{
		{CreatedAt: now, Target: models.WSEventTargetAll, Origin: "a", Payload: "p1"},
		{CreatedAt: now, Target: models.WSEventTargetAll, Origin: "b", Payload: "p2"},
	}
	require.NoError(t, repo.Insert(ctx, events))

	got, err := repo.ListByIDs(ctx, []int64{events[1].ID, 999, events[0].ID}, "a")
	require.NoError(t, err)
	require.Len(t, got, 2, "unknown IDs are left out")
	assert.Equal(t, events[0].ID, got[0].ID)
	assert.Empty(t, got[0].Payload, "own origin has no payload")
	assert.Equal(t, "p2", got[1].Payload)

	got, err = repo.ListByIDs(ctx, nil, "a")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestGORMWSEventRepository_DeleteOlderThan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		batchSize int
	}{
		{name: "one batch", batchSize: 100},
		{name: "several batches", batchSize: 2},
		{name: "default batch size", batchSize: 0},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			repo := setupWSEventRepo(t)
			now := time.Now().UTC()

			// Old and fresh rows interleave in ID order (created_at is not
			// strictly in ID order across replicas).
			var events []*models.WSEvent
			var fresh []*models.WSEvent
			for i := 0; i < 7; i++ {
				created := now.Add(-10 * time.Minute)
				if i%3 == 2 {
					created = now
				}
				ev := &models.WSEvent{CreatedAt: created, Target: models.WSEventTargetAll, Origin: "a", Payload: "p"}
				events = append(events, ev)
				if created.Equal(now) {
					fresh = append(fresh, ev)
				}
			}
			require.NoError(t, repo.Insert(ctx, events))

			deleted, err := repo.DeleteOlderThan(ctx, now.Add(-5*time.Minute), tt.batchSize)
			require.NoError(t, err)
			assert.Equal(t, int64(7-len(fresh)), deleted)

			got, err := repo.ListAfter(ctx, 0, 10, "")
			require.NoError(t, err)
			require.Len(t, got, len(fresh))
			for i, ev := range got {
				assert.Equal(t, fresh[i].ID, ev.ID, "fresh rows stay")
			}
		})
	}

	t.Run("stops when the context is done", func(t *testing.T) {
		t.Parallel()
		repo := setupWSEventRepo(t)
		old := &models.WSEvent{CreatedAt: time.Now().UTC().Add(-time.Hour), Target: models.WSEventTargetAll, Origin: "a", Payload: "p"}
		require.NoError(t, repo.Insert(context.Background(), []*models.WSEvent{old}))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		deleted, err := repo.DeleteOlderThan(ctx, time.Now().UTC(), 1)
		assert.ErrorIs(t, err, context.Canceled)
		assert.Zero(t, deleted)
	})
}

func TestGORMWSEventRepository_IDsAfter(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := setupWSEventRepo(t)
	now := time.Now().UTC()
	var events []*models.WSEvent
	for i := 0; i < 4; i++ {
		events = append(events, &models.WSEvent{CreatedAt: now, Target: models.WSEventTargetAll, Origin: "a", Payload: "p"})
	}
	require.NoError(t, repo.Insert(ctx, events))

	ids, err := repo.IDsAfter(ctx, events[0].ID, 2)
	require.NoError(t, err)
	assert.Equal(t, []int64{events[1].ID, events[2].ID}, ids)

	ids, err = repo.IDsAfter(ctx, events[3].ID, 10)
	require.NoError(t, err)
	assert.Empty(t, ids)
}

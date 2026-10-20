package database

import (
	"context"
	"fmt"
	"testing"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedActionRuns creates n runs of instanceID, one second apart (run-00 is
// the oldest), and returns their IDs newest first.
func seedActionRuns(t *testing.T, repo *GORMActionRunRepository, instanceID string, n int, base time.Time) []string {
	t.Helper()
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		run := &models.ActionRun{
			ID:         fmt.Sprintf("%s-run-%02d", instanceID, i),
			InstanceID: instanceID,
			Action:     "refresh-db",
			UserID:     "u1",
			StartedAt:  base.Add(time.Duration(i) * time.Second),
			StatusCode: 200,
			Parameters: `{}`,
		}
		require.NoError(t, repo.Create(context.Background(), run))
		ids[n-1-i] = run.ID
	}
	return ids
}

func TestGORMActionRunRepository_CreateDefaults(t *testing.T) {
	t.Parallel()
	repo := NewGORMActionRunRepository(setupTestDBWithAllTables(t))
	ctx := context.Background()

	before := time.Now().UTC()
	run := &models.ActionRun{InstanceID: "i1", Action: "seed", UserID: "u1", StatusCode: 202, JobID: "job-1", Parameters: `{"a":"b"}`}
	require.NoError(t, repo.Create(ctx, run))
	assert.Len(t, run.ID, 36, "Create sets a UUID")
	assert.False(t, run.StartedAt.Before(before.Add(-time.Second)), "Create sets StartedAt")

	runs, next, err := repo.ListByInstance(ctx, "i1", "", 0)
	require.NoError(t, err)
	assert.Empty(t, next)
	require.Len(t, runs, 1)
	got := runs[0]
	assert.Equal(t, run.ID, got.ID)
	assert.Equal(t, "seed", got.Action)
	assert.Equal(t, "u1", got.UserID)
	assert.Equal(t, 202, got.StatusCode)
	assert.Equal(t, "job-1", got.JobID)
	assert.Equal(t, `{"a":"b"}`, got.Parameters)
	assert.Empty(t, got.JobStatus)
	assert.Nil(t, got.FinishedAt)
}

func TestGORMActionRunRepository_ListByInstance(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		limit     int
		wantPages []int // rows of each page
	}{
		{name: "default limit", limit: 0, wantPages: []int{20, 5}},
		{name: "small pages", limit: 10, wantPages: []int{10, 10, 5}},
		{name: "exact page", limit: 25, wantPages: []int{25}},
		{name: "limit above the maximum", limit: 1000, wantPages: []int{25}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := NewGORMActionRunRepository(setupTestDBWithAllTables(t))
			ctx := context.Background()
			want := seedActionRuns(t, repo, "i1", 25, base)
			seedActionRuns(t, repo, "i2", 3, base)

			var got []string
			cursor := ""
			for page, rows := range tt.wantPages {
				runs, next, err := repo.ListByInstance(ctx, "i1", cursor, tt.limit)
				require.NoError(t, err)
				require.Len(t, runs, rows, "page %d", page)
				for _, r := range runs {
					assert.Equal(t, "i1", r.InstanceID)
					got = append(got, r.ID)
				}
				if page == len(tt.wantPages)-1 {
					assert.Empty(t, next, "no cursor on the last page")
				} else {
					require.NotEmpty(t, next)
				}
				cursor = next
			}
			assert.Equal(t, want, got, "newest first, no row twice")
		})
	}
}

func TestGORMActionRunRepository_MaxPageSize(t *testing.T) {
	t.Parallel()
	repo := NewGORMActionRunRepository(setupTestDBWithAllTables(t))
	seedActionRuns(t, repo, "i1", models.MaxActionRunPageSize+1, time.Now().UTC().Add(-time.Hour))

	runs, next, err := repo.ListByInstance(context.Background(), "i1", "", 500)
	require.NoError(t, err)
	assert.Len(t, runs, models.MaxActionRunPageSize)
	assert.NotEmpty(t, next)
}

func TestGORMActionRunRepository_SameStartTimeOrdersByID(t *testing.T) {
	t.Parallel()
	repo := NewGORMActionRunRepository(setupTestDBWithAllTables(t))
	ctx := context.Background()
	at := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	for _, id := range []string{"b", "c", "a"} {
		require.NoError(t, repo.Create(ctx, &models.ActionRun{ID: id, InstanceID: "i1", Action: "x", UserID: "u1", StartedAt: at}))
	}

	first, next, err := repo.ListByInstance(ctx, "i1", "", 2)
	require.NoError(t, err)
	require.Len(t, first, 2)
	assert.Equal(t, []string{"c", "b"}, []string{first[0].ID, first[1].ID})
	second, next, err := repo.ListByInstance(ctx, "i1", next, 2)
	require.NoError(t, err)
	require.Len(t, second, 1)
	assert.Equal(t, "a", second[0].ID)
	assert.Empty(t, next)
}

func TestGORMActionRunRepository_BadCursor(t *testing.T) {
	t.Parallel()
	repo := NewGORMActionRunRepository(setupTestDBWithAllTables(t))

	tests := []struct {
		name   string
		cursor string
	}{
		{"not base64", "%%%"},
		{"no separator", "bm9zZXBhcmF0b3I"},
		{"bad time", "bm90LWEtdGltZXxpZA"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := repo.ListByInstance(context.Background(), "i1", tt.cursor, 10)
			require.Error(t, err)
			assert.ErrorIs(t, err, dberrors.ErrValidation)
		})
	}
}

func TestGORMActionRunRepository_SetJobFinalStatus(t *testing.T) {
	t.Parallel()
	repo := NewGORMActionRunRepository(setupTestDBWithAllTables(t))
	ctx := context.Background()
	started := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	require.NoError(t, repo.Create(ctx, &models.ActionRun{ID: "job-run", InstanceID: "i1", Action: "refresh-db", UserID: "u1", StartedAt: started, StatusCode: 202, JobID: "job-1"}))
	require.NoError(t, repo.Create(ctx, &models.ActionRun{ID: "other-inst", InstanceID: "i2", Action: "refresh-db", UserID: "u1", StartedAt: started, StatusCode: 202, JobID: "job-1"}))
	finished := started.Add(time.Minute)
	require.NoError(t, repo.Create(ctx, &models.ActionRun{ID: "sync-run", InstanceID: "i1", Action: "seed", UserID: "u1", StartedAt: started, StatusCode: 200, FinishedAt: &finished}))

	tests := []struct {
		name                              string
		instanceID, action, jobID, status string
		want                              bool
	}{
		{"other action changes nothing", "i1", "seed", "job-1", "succeeded", false},
		{"unknown job changes nothing", "i1", "refresh-db", "job-2", "succeeded", false},
		{"empty job ID changes nothing", "i1", "seed", "", "succeeded", false},
		{"empty status changes nothing", "i1", "refresh-db", "job-1", "", false},
		{"first final status wins", "i1", "refresh-db", "job-1", "failed", true},
		{"second call changes nothing", "i1", "refresh-db", "job-1", "succeeded", false},
	}
	// Sequential: each case depends on the state of the previous case.
	for _, tt := range tests {
		updated, err := repo.SetJobFinalStatus(ctx, tt.instanceID, tt.action, tt.jobID, tt.status, finished)
		require.NoError(t, err, tt.name)
		assert.Equal(t, tt.want, updated, tt.name)
	}

	runs, _, err := repo.ListByInstance(ctx, "i1", "", 10)
	require.NoError(t, err)
	byID := map[string]models.ActionRun{}
	for _, r := range runs {
		byID[r.ID] = r
	}
	assert.Equal(t, "failed", byID["job-run"].JobStatus)
	require.NotNil(t, byID["job-run"].FinishedAt)
	assert.True(t, byID["job-run"].FinishedAt.Equal(finished))
	assert.Empty(t, byID["sync-run"].JobStatus)

	others, _, err := repo.ListByInstance(ctx, "i2", "", 10)
	require.NoError(t, err)
	require.Len(t, others, 1)
	assert.Empty(t, others[0].JobStatus, "a job ID of another instance is not changed")
	assert.Nil(t, others[0].FinishedAt)
}

func TestGORMActionRunRepository_DeleteByInstance(t *testing.T) {
	t.Parallel()
	repo := NewGORMActionRunRepository(setupTestDBWithAllTables(t))
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)
	seedActionRuns(t, repo, "i1", 3, base)
	seedActionRuns(t, repo, "i2", 2, base)

	require.NoError(t, repo.DeleteByInstance(ctx, "i1"))
	require.NoError(t, repo.DeleteByInstance(ctx, "i1"), "no rows is not an error")

	runs, _, err := repo.ListByInstance(ctx, "i1", "", 10)
	require.NoError(t, err)
	assert.Empty(t, runs)
	runs, _, err = repo.ListByInstance(ctx, "i2", "", 10)
	require.NoError(t, err)
	assert.Len(t, runs, 2)
}

func TestGORMActionRunRepository_DatabaseErrors(t *testing.T) {
	t.Parallel()
	db := setupMigrationTestDB(t) // no action_runs table
	repo := NewGORMActionRunRepository(db.DB)
	ctx := context.Background()

	assert.Error(t, repo.Create(ctx, &models.ActionRun{InstanceID: "i1", Action: "a", UserID: "u1"}))
	_, _, err := repo.ListByInstance(ctx, "i1", "", 10)
	assert.Error(t, err)
	_, err = repo.SetJobFinalStatus(ctx, "i1", "a", "job-1", "succeeded", time.Now())
	assert.Error(t, err)
	assert.Error(t, repo.DeleteByInstance(ctx, "i1"))
}

// TestActionRunsMigration checks migration 61: the table and its indexes,
// and idempotent Up and Down.
func TestActionRunsMigration(t *testing.T) {
	t.Parallel()
	mig := actionRunsMigration()
	assert.Equal(t, "20261020000061", mig.Version)
	assert.Equal(t, "create_action_runs", mig.Name)

	db := setupMigrationTestDB(t)
	require.NoError(t, db.AutoMigrate())
	m := db.DB.Migrator()
	assert.True(t, m.HasTable(&models.ActionRun{}))
	for _, idx := range []string{"idx_action_runs_instance_started", "idx_action_runs_started", "idx_action_runs_instance_job"} {
		assert.True(t, m.HasIndex(&models.ActionRun{}, idx), idx)
	}

	require.NoError(t, mig.Up(db.DB), "Up on an existing table")
	require.NoError(t, mig.Down(db.DB))
	assert.False(t, m.HasTable(&models.ActionRun{}))
	require.NoError(t, mig.Down(db.DB), "Down without the table")
	require.NoError(t, mig.Up(db.DB))
	assert.True(t, m.HasTable(&models.ActionRun{}))
}

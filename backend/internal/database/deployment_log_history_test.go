package database

import (
	"context"
	"math"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// historyBase is the start time of the history fixtures (whole seconds, so
// SQLite and MySQL store the same value).
var historyBase = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// historyLog is a fixture log: minute is the start time after historyBase.
type historyLog struct {
	id, action, status, target string
	minute                     int
}

// seedHistoryLogs creates the logs of instanceID with large columns set, so
// the tests can check that the summary rows do not load them.
func seedHistoryLogs(t *testing.T, repo *GORMDeploymentLogRepository, instanceID string, logs []historyLog) {
	t.Helper()
	for _, l := range logs {
		completed := historyBase.Add(time.Duration(l.minute)*time.Minute + 30*time.Second)
		log := &models.DeploymentLog{
			ID:              l.id,
			StackInstanceID: instanceID,
			Action:          l.action,
			Status:          l.status,
			TargetLogID:     l.target,
			Output:          "output of " + l.id,
			ValuesSnapshot:  `{"log":"` + l.id + `"}`,
			ChartVersions:   `{"web":"1.0.0"}`,
			ErrorMessage:    "error of " + l.id,
			Branch:          "main",
			UserID:          "user-" + l.id,
			StartedAt:       historyBase.Add(time.Duration(l.minute) * time.Minute),
		}
		if l.status != models.DeployLogRunning {
			log.CompletedAt = &completed
		}
		require.NoError(t, repo.Create(context.Background(), log))
	}
}

// logIDs returns the IDs of the logs in order.
func logIDs(logs []models.DeploymentLog) []string {
	ids := make([]string, 0, len(logs))
	for _, l := range logs {
		ids = append(ids, l.ID)
	}
	return ids
}

// assertSummaryRow checks that a summary row has the row columns and none
// of the large columns.
func assertSummaryRow(t *testing.T, l *models.DeploymentLog) {
	t.Helper()
	assert.Empty(t, l.Output, "output of %s", l.ID)
	assert.Empty(t, l.ValuesSnapshot, "values snapshot of %s", l.ID)
	assert.Empty(t, l.ChartVersions, "chart versions of %s", l.ID)
	assert.NotEmpty(t, l.StackInstanceID)
	assert.NotEmpty(t, l.Action)
	assert.NotEmpty(t, l.Status)
	assert.Equal(t, "main", l.Branch)
	assert.Equal(t, "error of "+l.ID, l.ErrorMessage)
	assert.Equal(t, "user-"+l.ID, l.UserID)
	assert.False(t, l.StartedAt.IsZero())
	if l.Status != models.DeployLogRunning {
		assert.NotNil(t, l.CompletedAt, "completed_at of %s", l.ID)
	}
}

// runDeployLogHistoryListChecks checks the filters, the summary columns and
// the next cursor of ListByInstancePaginated. The SQLite unit test and the
// MySQL integration test share it.
func runDeployLogHistoryListChecks(t *testing.T, repo *GORMDeploymentLogRepository) {
	t.Helper()
	ctx := context.Background()
	const inst = "hist-list"
	seedHistoryLogs(t, repo, inst, []historyLog{
		{id: "l1", action: models.DeployActionDeploy, status: models.DeployLogSuccess, minute: 0},
		{id: "l2", action: models.DeployActionDeploy, status: models.DeployLogError, minute: 1},
		{id: "l3", action: models.DeployActionStop, status: models.DeployLogSuccess, minute: 2},
		{id: "l4", action: models.DeployActionRollback, status: models.DeployLogSuccess, target: "l1", minute: 3},
		{id: "l5", action: models.DeployActionClean, status: models.DeployLogError, minute: 4},
		{id: "l6", action: models.DeployActionDeploy, status: models.DeployLogRunning, minute: 5},
	})
	seedHistoryLogs(t, repo, "hist-list-other", []historyLog{
		{id: "lo1", action: models.DeployActionDeploy, status: models.DeployLogError, minute: 6},
	})

	tests := []struct {
		name       string
		filters    models.DeploymentLogFilters
		wantIDs    []string
		wantTotal  int64
		wantCursor bool
	}{
		{name: "no filters", wantIDs: []string{"l6", "l5", "l4", "l3", "l2", "l1"}, wantTotal: 6},
		{name: "action filter", filters: models.DeploymentLogFilters{Actions: []string{models.DeployActionDeploy}},
			wantIDs: []string{"l6", "l2", "l1"}, wantTotal: 3},
		{name: "status filter", filters: models.DeploymentLogFilters{Statuses: []string{models.DeployLogError}},
			wantIDs: []string{"l5", "l2"}, wantTotal: 2},
		{name: "action and status filters", filters: models.DeploymentLogFilters{
			Actions:  []string{models.DeployActionDeploy, models.DeployActionRollback},
			Statuses: []string{models.DeployLogSuccess},
		}, wantIDs: []string{"l4", "l1"}, wantTotal: 2},
		{name: "first page has a next cursor", filters: models.DeploymentLogFilters{Limit: 2},
			wantIDs: []string{"l6", "l5"}, wantTotal: 6, wantCursor: true},
		{name: "last page has no next cursor", filters: models.DeploymentLogFilters{Limit: 2, Offset: 4},
			wantIDs: []string{"l2", "l1"}, wantTotal: 6},
		{name: "filtered page has a next cursor", filters: models.DeploymentLogFilters{Limit: 2, Actions: []string{models.DeployActionDeploy}},
			wantIDs: []string{"l6", "l2"}, wantTotal: 3, wantCursor: true},
		{name: "no match", filters: models.DeploymentLogFilters{Statuses: []string{models.DeployLogPartial}},
			wantIDs: []string{}, wantTotal: 0},
	}
	for _, tt := range tests {
		tt := tt
		for _, summary := range []bool{false, true} {
			summary := summary
			name := tt.name
			if summary {
				name += " (summary)"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				f := tt.filters
				f.InstanceID = inst
				f.Summary = summary
				res, err := repo.ListByInstancePaginated(ctx, f)
				require.NoError(t, err)
				assert.Equal(t, tt.wantIDs, logIDs(res.Data))
				assert.Equal(t, tt.wantTotal, res.Total)
				assert.Equal(t, tt.wantCursor, res.NextCursor != "", "next cursor %q", res.NextCursor)
				assert.Nil(t, res.Highlights)
				for i := range res.Data {
					l := &res.Data[i]
					if summary {
						assertSummaryRow(t, l)
						continue
					}
					assert.Equal(t, "output of "+l.ID, l.Output)
					assert.Equal(t, `{"log":"`+l.ID+`"}`, l.ValuesSnapshot)
					assert.Equal(t, `{"web":"1.0.0"}`, l.ChartVersions)
				}
			})
		}
	}

	t.Run("offset next cursor continues on the cursor path with the filters", func(t *testing.T) {
		t.Parallel()
		filters := models.DeploymentLogFilters{InstanceID: inst, Limit: 1, Statuses: []string{models.DeployLogError}, Summary: true}
		first, err := repo.ListByInstancePaginated(ctx, filters)
		require.NoError(t, err)
		assert.Equal(t, []string{"l5"}, logIDs(first.Data))
		assert.Equal(t, int64(2), first.Total)
		require.NotEmpty(t, first.NextCursor)

		// Without the status filter the next row would be l4.
		filters.Cursor = first.NextCursor
		second, err := repo.ListByInstancePaginated(ctx, filters)
		require.NoError(t, err)
		assert.Equal(t, []string{"l2"}, logIDs(second.Data))
		assert.Equal(t, int64(-1), second.Total)
		assert.Empty(t, second.NextCursor)
		assertSummaryRow(t, &second.Data[0])
	})

	t.Run("limit above the maximum gives the maximum on both paths", func(t *testing.T) {
		t.Parallel()
		offset, err := repo.ListByInstancePaginated(ctx, models.DeploymentLogFilters{InstanceID: inst, Limit: math.MaxInt})
		require.NoError(t, err)
		assert.Len(t, offset.Data, 6)
		assert.Empty(t, offset.NextCursor)

		first, err := repo.ListByInstancePaginated(ctx, models.DeploymentLogFilters{InstanceID: inst, Limit: 1})
		require.NoError(t, err)
		require.NotEmpty(t, first.NextCursor)
		// limit+1 of the cursor path must not overflow.
		rest, err := repo.ListByInstancePaginated(ctx, models.DeploymentLogFilters{InstanceID: inst, Limit: math.MaxInt, Cursor: first.NextCursor})
		require.NoError(t, err)
		assert.Equal(t, []string{"l5", "l4", "l3", "l2", "l1"}, logIDs(rest.Data))
		assert.Empty(t, rest.NextCursor)
	})

	t.Run("cursor path with an action filter", func(t *testing.T) {
		t.Parallel()
		filters := models.DeploymentLogFilters{InstanceID: inst, Limit: 1, Actions: []string{models.DeployActionRollback, models.DeployActionStop}}
		first, err := repo.ListByInstancePaginated(ctx, filters)
		require.NoError(t, err)
		assert.Equal(t, []string{"l4"}, logIDs(first.Data))
		require.NotEmpty(t, first.NextCursor)

		filters.Cursor = first.NextCursor
		filters.Limit = 5
		second, err := repo.ListByInstancePaginated(ctx, filters)
		require.NoError(t, err)
		assert.Equal(t, []string{"l3"}, logIDs(second.Data))
		assert.Equal(t, "output of l3", second.Data[0].Output, "full view on the cursor path")
	})
}

// runDeployLogHighlightChecks checks HistoryHighlights. The SQLite unit
// test and the MySQL integration test share it.
func runDeployLogHighlightChecks(t *testing.T, repo *GORMDeploymentLogRepository) {
	t.Helper()
	ctx := context.Background()

	// A deploy of another instance, as a rollback target that is not allowed.
	seedHistoryLogs(t, repo, "hl-foreign", []historyLog{
		{id: "hl-foreign-d", action: models.DeployActionDeploy, status: models.DeployLogSuccess, minute: 0},
		{id: "hl-foreign-r", action: models.DeployActionDeploy, status: models.DeployLogRunning, minute: 30},
		{id: "hl-foreign-e", action: models.DeployActionDeploy, status: models.DeployLogError, minute: 31},
	})

	tests := []struct {
		name        string
		logs        []historyLog
		wantRunning string
		wantCurrent string
		wantFailure string
	}{
		{name: "no logs"},
		{name: "deploy success is current", logs: []historyLog{
			{id: "d1", action: models.DeployActionDeploy, status: models.DeployLogSuccess, minute: 0},
			{id: "s1", action: models.DeployActionStop, status: models.DeployLogSuccess, minute: 1},
		}, wantCurrent: "d1"},
		{name: "newest success wins", logs: []historyLog{
			{id: "d1", action: models.DeployActionDeploy, status: models.DeployLogSuccess, minute: 0},
			{id: "d2", action: models.DeployActionDeploy, status: models.DeployLogSuccess, minute: 1},
		}, wantCurrent: "d2"},
		{name: "failure after current", logs: []historyLog{
			{id: "d1", action: models.DeployActionDeploy, status: models.DeployLogSuccess, minute: 0},
			{id: "e1", action: models.DeployActionDeploy, status: models.DeployLogError, minute: 1},
			{id: "e2", action: models.DeployActionStop, status: models.DeployLogError, minute: 2},
		}, wantCurrent: "d1", wantFailure: "e2"},
		{name: "failure older than current", logs: []historyLog{
			{id: "e1", action: models.DeployActionDeploy, status: models.DeployLogError, minute: 0},
			{id: "d1", action: models.DeployActionDeploy, status: models.DeployLogSuccess, minute: 1},
		}, wantCurrent: "d1"},
		{name: "no successful deploy gives the newest error", logs: []historyLog{
			{id: "e1", action: models.DeployActionDeploy, status: models.DeployLogError, minute: 0},
			{id: "e2", action: models.DeployActionClean, status: models.DeployLogError, minute: 1},
			{id: "s1", action: models.DeployActionStop, status: models.DeployLogSuccess, minute: 2},
		}, wantFailure: "e2"},
		{name: "partial deploy after a success is current and not a failure", logs: []historyLog{
			{id: "d1", action: models.DeployActionDeploy, status: models.DeployLogSuccess, minute: 0},
			{id: "d2", action: models.DeployActionDeploy, status: models.DeployLogPartial, minute: 1},
		}, wantCurrent: "d2"},
		{name: "failure before a partial deploy is not shown", logs: []historyLog{
			{id: "d1", action: models.DeployActionDeploy, status: models.DeployLogSuccess, minute: 0},
			{id: "e1", action: models.DeployActionDeploy, status: models.DeployLogError, minute: 1},
			{id: "d2", action: models.DeployActionDeploy, status: models.DeployLogPartial, minute: 2},
		}, wantCurrent: "d2"},
		{name: "failure after a partial deploy is shown", logs: []historyLog{
			{id: "d1", action: models.DeployActionDeploy, status: models.DeployLogSuccess, minute: 0},
			{id: "d2", action: models.DeployActionDeploy, status: models.DeployLogPartial, minute: 1},
			{id: "e1", action: models.DeployActionDeploy, status: models.DeployLogError, minute: 2},
		}, wantCurrent: "d2", wantFailure: "e1"},
		{name: "only a partial deploy", logs: []historyLog{
			{id: "d1", action: models.DeployActionDeploy, status: models.DeployLogPartial, minute: 0},
		}, wantCurrent: "d1"},
		{name: "rollback with a target makes the target current", logs: []historyLog{
			{id: "d1", action: models.DeployActionDeploy, status: models.DeployLogSuccess, minute: 0},
			{id: "d2", action: models.DeployActionDeploy, status: models.DeployLogSuccess, minute: 1},
			{id: "r1", action: models.DeployActionRollback, status: models.DeployLogSuccess, target: "d1", minute: 2},
		}, wantCurrent: "d1"},
		{name: "rollback without a target omits current", logs: []historyLog{
			{id: "d1", action: models.DeployActionDeploy, status: models.DeployLogSuccess, minute: 0},
			{id: "e1", action: models.DeployActionDeploy, status: models.DeployLogError, minute: 1},
			{id: "r1", action: models.DeployActionRollback, status: models.DeployLogSuccess, minute: 2},
		}},
		{name: "rollback target of another instance omits current", logs: []historyLog{
			{id: "d1", action: models.DeployActionDeploy, status: models.DeployLogSuccess, minute: 0},
			{id: "r1", action: models.DeployActionRollback, status: models.DeployLogSuccess, target: "hl-foreign-d", minute: 1},
		}},
		{name: "failed rollback is a failure, not current", logs: []historyLog{
			{id: "d1", action: models.DeployActionDeploy, status: models.DeployLogSuccess, minute: 0},
			{id: "r1", action: models.DeployActionRollback, status: models.DeployLogError, target: "d1", minute: 1},
		}, wantCurrent: "d1", wantFailure: "r1"},
		{name: "failure after a rollback to a target", logs: []historyLog{
			{id: "d1", action: models.DeployActionDeploy, status: models.DeployLogSuccess, minute: 0},
			{id: "e1", action: models.DeployActionDeploy, status: models.DeployLogError, minute: 1},
			{id: "r1", action: models.DeployActionRollback, status: models.DeployLogSuccess, target: "d1", minute: 2},
			{id: "e2", action: models.DeployActionDeploy, status: models.DeployLogError, minute: 3},
		}, wantCurrent: "d1", wantFailure: "e2"},
		{name: "newest running log of any action", logs: []historyLog{
			{id: "d1", action: models.DeployActionDeploy, status: models.DeployLogSuccess, minute: 0},
			{id: "s1", action: models.DeployActionStop, status: models.DeployLogRunning, minute: 1},
			{id: "d2", action: models.DeployActionDeploy, status: models.DeployLogRunning, minute: 2},
		}, wantRunning: "d2", wantCurrent: "d1"},
	}

	for i, tt := range tests {
		tt := tt
		inst := "hl-" + string(rune('a'+i))
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Log IDs are unique for each instance.
			logs := make([]historyLog, len(tt.logs))
			for j, l := range tt.logs {
				l.id = inst + "-" + l.id
				if l.target != "" && l.target != "hl-foreign-d" {
					l.target = inst + "-" + l.target
				}
				logs[j] = l
			}
			seedHistoryLogs(t, repo, inst, logs)

			hl, err := repo.HistoryHighlights(ctx, inst)
			require.NoError(t, err)
			require.NotNil(t, hl)

			check := func(field string, got *models.DeploymentLog, want string) {
				t.Helper()
				if want == "" {
					assert.Nil(t, got, field)
					return
				}
				require.NotNil(t, got, field)
				assert.Equal(t, inst+"-"+want, got.ID, field)
				assert.Equal(t, inst, got.StackInstanceID, field)
				assertSummaryRow(t, got)
			}
			check("running", hl.Running, tt.wantRunning)
			check("current", hl.Current, tt.wantCurrent)
			check("latest_failure", hl.LatestFailure, tt.wantFailure)
		})
	}
}

func TestGORMDeploymentLogRepository_History(t *testing.T) {
	t.Parallel()

	t.Run("list", func(t *testing.T) {
		t.Parallel()
		runDeployLogHistoryListChecks(t, setupDeploymentLogRepo(t))
	})
	t.Run("highlights", func(t *testing.T) {
		t.Parallel()
		runDeployLogHighlightChecks(t, setupDeploymentLogRepo(t))
	})
}

func TestGORMDeploymentLogRepository_PageSizeCap(t *testing.T) {
	t.Parallel()

	repo := setupDeploymentLogRepo(t)
	ctx := context.Background()
	for i := 0; i <= models.MaxDeployLogPageSize; i++ {
		require.NoError(t, repo.Create(ctx, &models.DeploymentLog{
			StackInstanceID: "cap",
			Action:          models.DeployActionDeploy,
			Status:          models.DeployLogSuccess,
			StartedAt:       historyBase.Add(time.Duration(i) * time.Second),
		}))
	}

	tests := []struct {
		name  string
		limit int
	}{
		{name: "above the maximum", limit: models.MaxDeployLogPageSize + 1},
		{name: "largest int", limit: math.MaxInt},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			offset, err := repo.ListByInstancePaginated(ctx, models.DeploymentLogFilters{InstanceID: "cap", Limit: tt.limit, Summary: true})
			require.NoError(t, err)
			assert.Len(t, offset.Data, models.MaxDeployLogPageSize)
			assert.Equal(t, int64(models.MaxDeployLogPageSize+1), offset.Total)
			require.NotEmpty(t, offset.NextCursor)

			first, err := repo.ListByInstancePaginated(ctx, models.DeploymentLogFilters{InstanceID: "cap", Limit: 1})
			require.NoError(t, err)
			cursor, err := repo.ListByInstancePaginated(ctx, models.DeploymentLogFilters{InstanceID: "cap", Limit: tt.limit, Cursor: first.NextCursor})
			require.NoError(t, err)
			assert.Len(t, cursor.Data, models.MaxDeployLogPageSize)
			assert.Empty(t, cursor.NextCursor, "no row after the page")
		})
	}
}

func TestEncodeDeployLogCursorRoundTrip(t *testing.T) {
	t.Parallel()

	ts := time.Date(2026, 10, 1, 12, 0, 0, 123456789, time.UTC)
	gotTS, gotID, err := decodeDeployLogCursor(encodeDeployLogCursor(ts, "log-1"))
	require.NoError(t, err)
	assert.True(t, ts.Equal(gotTS))
	assert.Equal(t, "log-1", gotID)

	for _, bad := range []string{"!!!", "bm8tc2VwYXJhdG9y", "fGxvZy0x"} {
		_, _, err := decodeDeployLogCursor(bad)
		assert.Error(t, err, bad)
	}
}

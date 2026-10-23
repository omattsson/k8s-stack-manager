//go:build integration

package database

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// explainRows returns all EXPLAIN rows of query (a derived table gives more
// than one row) as column name to value.
func explainRows(t *testing.T, db *gorm.DB, query string, args ...any) []map[string]string {
	t.Helper()
	rows, err := db.Raw("EXPLAIN "+query, args...).Rows()
	require.NoError(t, err)
	defer rows.Close()
	cols, err := rows.Columns()
	require.NoError(t, err)
	var out []map[string]string
	for rows.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		require.NoError(t, rows.Scan(ptrs...))
		row := make(map[string]string, len(cols))
		for i, c := range cols {
			row[c] = vals[i].String
		}
		out = append(out, row)
	}
	require.NoError(t, rows.Err())
	require.NotEmpty(t, out)
	return out
}

// explainAnalyze returns the EXPLAIN ANALYZE tree of query (actual rows).
func explainAnalyze(t *testing.T, db *gorm.DB, query string, args ...any) string {
	t.Helper()
	var plan string
	require.NoError(t, db.Raw("EXPLAIN ANALYZE "+query, args...).Row().Scan(&plan))
	return strings.TrimSpace(plan)
}

// indexReads sums the index row reads of handler stats (not the scan of a
// materialized derived table, Handler_read_rnd_next).
func indexReads(stats map[string]int64) int64 {
	return stats["Handler_read_key"] + stats["Handler_read_next"] + stats["Handler_read_prev"] +
		stats["Handler_read_first"] + stats["Handler_read_last"]
}

// TestDatabaseMySQL_NotificationTotalCap checks the windowed total of the
// notification list on MySQL (#654): a user with more than 1000
// notifications gets a capped total, and the count query reads about
// offset + 1001 index rows, not all rows of the user. It logs the EXPLAIN
// and the row reads of the old count, the new count and CountUnread.
func TestDatabaseMySQL_NotificationTotalCap(t *testing.T) {
	gdb := openMySQLSchema(t, "app_migtest_notification_total_cap")
	require.NoError(t, gdb.AutoMigrate(&models.Cluster{}), "migration 5 alters clusters")
	require.NoError(t, (&Database{DB: gdb}).AutoMigrate())

	// The list user has 1500 unread and 1000 read rows; other users have
	// 3000 rows, some of them newer.
	const listUser = "u-cap"
	const unread, read = 1500, 1000
	now := time.Now().UTC()
	var rows []models.Notification
	for i := 0; i < unread+read; i++ {
		rows = append(rows, models.Notification{
			ID: uuid.New().String(), UserID: listUser, Type: "deployment.success", Title: "t",
			IsRead: i >= unread, CreatedAt: now.Add(-24*time.Hour - time.Duration(i)*time.Second),
		})
	}
	for _, u := range []string{"u-a", "u-b", "u-c"} {
		for i := 0; i < 1000; i++ {
			rows = append(rows, models.Notification{
				ID: uuid.New().String(), UserID: u, Type: "deployment.success", Title: "t",
				IsRead: i%2 == 0, CreatedAt: now.Add(-time.Duration(i) * time.Second),
			})
		}
	}
	require.NoError(t, gdb.CreateInBatches(rows, 500).Error)
	require.NoError(t, gdb.Exec("ANALYZE TABLE notifications").Error)

	// The SQL of the repository, with its values (debug mode logs them).
	log, h := newRecordingLogger(slog.LevelDebug)
	repo := NewGORMNotificationRepository(gdb.Session(&gorm.Session{Logger: newSlogGormLogger(log, true)}))
	ctx := context.Background()

	const capN = int64(models.NotificationTotalCap)
	cases := []struct {
		name       string
		unreadOnly bool
		offset     int
		wantLen    int
		wantTotal  int64
		wantCapped bool
	}{
		{name: "first page", offset: 0, wantLen: 20, wantTotal: capN, wantCapped: true},
		{name: "offset moves the window", offset: 100, wantLen: 20, wantTotal: 100 + capN, wantCapped: true},
		{name: "offset near the end is exact", offset: 2400, wantLen: 20, wantTotal: unread + read},
		{name: "offset past the end is exact", offset: 3000, wantLen: 0, wantTotal: unread + read},
		{name: "unread only capped", unreadOnly: true, offset: 0, wantLen: 20, wantTotal: capN, wantCapped: true},
		{name: "unread only exact", unreadOnly: true, offset: 1000, wantLen: 20, wantTotal: unread},
	}
	// The subtests share the rows above and run in order (not parallel):
	// the code after the loop reads the same rows.
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			list, total, capped, err := repo.ListByUser(ctx, listUser, tc.unreadOnly, 20, tc.offset)
			require.NoError(t, err)
			assert.Len(t, list, tc.wantLen)
			assert.Equal(t, tc.wantTotal, total)
			assert.Equal(t, tc.wantCapped, capped)
			for _, n := range list {
				assert.Equal(t, listUser, n.UserID)
				if tc.unreadOnly {
					assert.False(t, n.IsRead)
				}
			}
		})
	}
	unreadCount, err := repo.CountUnread(ctx, listUser)
	require.NoError(t, err)
	assert.Equal(t, int64(unread), unreadCount, "CountUnread stays exact")

	var repoCount string
	for _, r := range h.all() {
		if s := recordAttrs(r)["sql"].String(); strings.Contains(s, "FROM (SELECT 1 FROM notifications") && !strings.Contains(s, "is_read") {
			repoCount = s
			break
		}
	}
	require.NotEmpty(t, repoCount, "the repository runs the windowed count")
	t.Logf("repository count query: %s", repoCount)

	// The indexes that start with user_id.
	var userIndexes []string
	require.NoError(t, gdb.Raw(`SELECT index_name FROM information_schema.statistics
		WHERE table_schema = DATABASE() AND table_name = 'notifications' AND seq_in_index = 1 AND column_name = 'user_id'`).
		Scan(&userIndexes).Error)
	require.NotEmpty(t, userIndexes)

	window := capN + 1
	queries := []struct {
		name string
		sql  string
		args []any
	}{
		{"old count", "SELECT count(*) FROM notifications WHERE user_id = ?", []any{listUser}},
		{"new count", notificationCountAll, []any{listUser, window}},
		{"new count, unread only", notificationCountUnread, []any{listUser, false, window}},
		{"CountUnread", "SELECT count(*) FROM notifications WHERE user_id = ? AND is_read = ?", []any{listUser, false}},
	}
	reads := map[string]int64{}
	for _, q := range queries {
		var parts []string
		for _, row := range explainRows(t, gdb, q.sql, q.args...) {
			parts = append(parts, fmt.Sprintf("[select_type=%q table=%q %s]", row["select_type"], row["table"], formatExplain(row)))
			if q.name == "new count" && row["table"] == "notifications" {
				assert.Contains(t, userIndexes, row["key"], "the windowed count uses an index on user_id")
			}
		}
		t.Logf("EXPLAIN %s: %s", q.name, strings.Join(parts, " "))
		t.Logf("EXPLAIN ANALYZE %s:\n%s", q.name, explainAnalyze(t, gdb, q.sql, q.args...))
		stats := handlerReadStats(t, gdb, q.sql, q.args...)
		reads[q.name] = indexReads(stats)
		t.Logf("row reads %s: index %d, all Handler_read_* %d (%v)", q.name, reads[q.name], listRowReads(t, gdb, q.sql, q.args...), stats)
	}
	assert.GreaterOrEqual(t, reads["old count"], int64(unread+read), "the old count reads every row of the user")
	assert.LessOrEqual(t, reads["new count"], window+5, "the windowed count reads about cap + 1 index rows")
	assert.LessOrEqual(t, reads["new count, unread only"], window+5, "the windowed unread count reads about cap + 1 index rows")
	// unread_count stays exact (#654): CountUnread reads every unread row.
	assert.GreaterOrEqual(t, reads["CountUnread"], int64(unread), "CountUnread reads every unread row of the user")
}

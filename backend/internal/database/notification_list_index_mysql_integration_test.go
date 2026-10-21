//go:build integration

package database

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"backend/internal/models"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// openMySQLSchema creates the empty MySQL database name for the test and
// returns a connection to it.
func openMySQLSchema(t *testing.T, name string) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		dsn = "root:rootpassword@tcp(localhost:3306)/app?charset=utf8mb4&parseTime=True&loc=Local"
	}
	cfg, err := mysqldriver.ParseDSN(dsn)
	require.NoError(t, err)
	admin, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err, "Failed to connect to MySQL — is the container running?")
	require.NoError(t, admin.Exec("DROP DATABASE IF EXISTS "+name).Error)
	require.NoError(t, admin.Exec("CREATE DATABASE "+name).Error)
	t.Cleanup(func() { _ = admin.Exec("DROP DATABASE IF EXISTS " + name).Error })

	cfg.DBName = name
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	return db
}

// explainRow returns the EXPLAIN row of a single-table query as column name
// to value.
func explainRow(t *testing.T, db *gorm.DB, query string, args ...any) map[string]string {
	t.Helper()
	rows, err := db.Raw("EXPLAIN "+query, args...).Rows()
	require.NoError(t, err)
	defer rows.Close()
	cols, err := rows.Columns()
	require.NoError(t, err)
	require.True(t, rows.Next(), "EXPLAIN returns a row")
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	require.NoError(t, rows.Scan(ptrs...))
	out := make(map[string]string, len(cols))
	for i, c := range cols {
		out[c] = vals[i].String
	}
	assert.False(t, rows.Next(), "one table, one EXPLAIN row")
	return out
}

// TestDatabaseMySQL_NotificationListIndex runs migration 63 (#654) on MySQL
// (the full chain on an empty database, then Up and Down again) and checks
// that the notification list query uses the new index without a filesort.
func TestDatabaseMySQL_NotificationListIndex(t *testing.T) {
	gdb := openMySQLSchema(t, "app_migtest_notification_index")
	require.NoError(t, gdb.AutoMigrate(&models.Cluster{}), "migration 5 alters clusters")
	require.NoError(t, (&Database{DB: gdb}).AutoMigrate())

	m := gdb.Migrator()
	assert.True(t, m.HasIndex(&models.Notification{}, notificationListIndexName))
	assert.True(t, m.HasIndex(&models.Notification{}, "idx_notifications_user_read_created"))

	mig := notificationListIndexMigration()
	for _, step := range []struct {
		name string
		run  func(*gorm.DB) error
		want bool
	}{
		{"Up on the existing index", mig.Up, true},
		{"Down", mig.Down, false},
		{"Down without the index", mig.Down, false},
		{"Up", mig.Up, true},
		{"Up again", mig.Up, true},
	} {
		require.NoError(t, step.run(gdb), step.name)
		assert.Equal(t, step.want, m.HasIndex(&models.Notification{}, notificationListIndexName), step.name)
	}

	// Seed one user with many rows; other users have the newer rows.
	const listUser = "u-list"
	now := time.Now().UTC()
	var rows []models.Notification
	for u, n := range map[string]int{listUser: 3000, "u-a": 1000, "u-b": 1000, "u-c": 1000} {
		start := now
		if u == listUser {
			start = now.Add(-24 * time.Hour)
		}
		for i := 0; i < n; i++ {
			rows = append(rows, models.Notification{
				ID: uuid.New().String(), UserID: u, Type: "deployment.success", Title: "t",
				IsRead: i%3 == 0, CreatedAt: start.Add(-time.Duration(i) * time.Second),
			})
		}
	}
	require.NoError(t, gdb.CreateInBatches(rows, 500).Error)
	require.NoError(t, gdb.Exec("ANALYZE TABLE notifications").Error)

	queries := map[string]string{
		"list query": "SELECT * FROM notifications WHERE user_id = '" + listUser + "' ORDER BY created_at DESC LIMIT 20",
	}
	// The SQL of the repository, with its values (debug mode logs them).
	log, h := newRecordingLogger(slog.LevelDebug)
	repo := NewGORMNotificationRepository(gdb.Session(&gorm.Session{Logger: newSlogGormLogger(log, true)}))
	list, total, err := repo.ListByUser(context.Background(), listUser, false, 20, 0)
	require.NoError(t, err)
	assert.Len(t, list, 20)
	assert.Equal(t, int64(3000), total, "the exact total stays")
	for _, r := range h.all() {
		if s := recordAttrs(r)["sql"].String(); strings.Contains(s, "ORDER BY") {
			queries["repository query"] = s
		}
	}
	require.Contains(t, queries, "repository query")

	require.NoError(t, mig.Down(gdb))
	t.Logf("EXPLAIN without the index: %s", formatExplain(explainRow(t, gdb, queries["list query"])))
	t.Logf("row reads without the index: %d", listRowReads(t, gdb, queries["list query"]))
	require.NoError(t, mig.Up(gdb))
	require.NoError(t, gdb.Exec("ANALYZE TABLE notifications").Error)

	for name, q := range queries {
		row := explainRow(t, gdb, q)
		t.Logf("EXPLAIN %s: %s", name, formatExplain(row))
		assert.Equal(t, notificationListIndexName, row["key"], name)
		assert.NotContains(t, row["Extra"], "filesort", name)
	}
	reads := listRowReads(t, gdb, queries["list query"])
	t.Logf("row reads with the index: %d", reads)
	assert.LessOrEqual(t, reads, int64(25), "the list reads about one page of rows")
}

// listRowReads runs query on one connection and returns the InnoDB handler
// row reads of the session (Handler_read_*).
func listRowReads(t *testing.T, db *gorm.DB, query string) int64 {
	t.Helper()
	var total int64
	require.NoError(t, db.Connection(func(conn *gorm.DB) error {
		if err := conn.Exec("FLUSH STATUS").Error; err != nil {
			return err
		}
		var out []map[string]any
		if err := conn.Raw(query).Scan(&out).Error; err != nil {
			return err
		}
		var stats []struct {
			VariableName string `gorm:"column:Variable_name"`
			Value        int64  `gorm:"column:Value"`
		}
		if err := conn.Raw("SHOW SESSION STATUS LIKE 'Handler_read%'").Scan(&stats).Error; err != nil {
			return err
		}
		for _, s := range stats {
			total += s.Value
		}
		return nil
	}))
	return total
}

func formatExplain(row map[string]string) string {
	var b strings.Builder
	for _, c := range []string{"type", "possible_keys", "key", "key_len", "ref", "rows", "filtered", "Extra"} {
		fmt.Fprintf(&b, "%s=%q ", c, row[c])
	}
	return strings.TrimSpace(b.String())
}

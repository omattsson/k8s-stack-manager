//go:build integration

package database

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"backend/internal/models"
)

// TestDatabaseMySQL_ActionRuns checks the action run history (#640) on
// MySQL: migration 61 Up, Down and Up with its column types and indexes,
// cursor paging on datetime(3), the conditional final status update under
// concurrency, the delete of an instance in a real transaction and the
// retention by database time.
func TestDatabaseMySQL_ActionRuns(t *testing.T) {
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		dsn = "root:rootpassword@tcp(localhost:3306)/app?charset=utf8mb4&parseTime=True&loc=Local"
	}
	cfg, err := mysqldriver.ParseDSN(dsn)
	require.NoError(t, err)
	admin, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err, "Failed to connect to MySQL — is the container running?")

	const dbName = "migration_test_action_runs"
	require.NoError(t, admin.Exec("DROP DATABASE IF EXISTS "+dbName).Error)
	require.NoError(t, admin.Exec("CREATE DATABASE "+dbName).Error)
	t.Cleanup(func() { _ = admin.Exec("DROP DATABASE IF EXISTS " + dbName).Error })

	cfg.DBName = dbName
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Cluster{}), "migration 5 alters clusters")
	require.NoError(t, (&Database{DB: db}).AutoMigrate())

	ctx := context.Background()
	m := db.Migrator()

	t.Run("migration 61 down and up", func(t *testing.T) {
		mig := actionRunsMigration()
		require.NoError(t, mig.Up(db), "Up on an existing table")
		require.NoError(t, mig.Down(db))
		assert.False(t, m.HasTable(&models.ActionRun{}))
		require.NoError(t, mig.Down(db), "Down without the table")
		require.NoError(t, mig.Up(db))
		require.True(t, m.HasTable(&models.ActionRun{}))

		indexes := map[string][]string{}
		var rows []struct {
			IndexName  string `gorm:"column:INDEX_NAME"`
			ColumnName string `gorm:"column:COLUMN_NAME"`
		}
		require.NoError(t, db.Raw(`SELECT INDEX_NAME, COLUMN_NAME FROM information_schema.STATISTICS
			WHERE TABLE_SCHEMA = ? AND TABLE_NAME = 'action_runs' ORDER BY INDEX_NAME, SEQ_IN_INDEX`, dbName).Scan(&rows).Error)
		for _, r := range rows {
			indexes[r.IndexName] = append(indexes[r.IndexName], r.ColumnName)
		}
		assert.Equal(t, []string{"instance_id", "started_at", "id"}, indexes["idx_action_runs_instance_started"])
		assert.Equal(t, []string{"started_at"}, indexes["idx_action_runs_started"])
		assert.Equal(t, []string{"instance_id", "job_id"}, indexes["idx_action_runs_instance_job"])

		types := map[string]string{}
		var cols []struct {
			ColumnName string `gorm:"column:COLUMN_NAME"`
			ColumnType string `gorm:"column:COLUMN_TYPE"`
		}
		require.NoError(t, db.Raw(`SELECT COLUMN_NAME, COLUMN_TYPE FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = ? AND TABLE_NAME = 'action_runs'`, dbName).Scan(&cols).Error)
		for _, c := range cols {
			types[c.ColumnName] = c.ColumnType
		}
		assert.Equal(t, "datetime(3)", types["started_at"])
		assert.Equal(t, "datetime(3)", types["finished_at"])
		assert.Equal(t, "varchar(255)", types["action"])
		assert.Equal(t, "varchar(500)", types["message"])
		assert.Equal(t, "varchar(128)", types["job_id"])
		assert.Equal(t, "varchar(32)", types["job_status"])
		assert.Equal(t, "varchar(64)", types["request_id"])
		assert.Equal(t, "mediumtext", types["parameters"])
		assert.Equal(t, "int", types["status_code"])
	})

	repo := NewGORMActionRunRepository(db)

	t.Run("cursor paging on millisecond precision", func(t *testing.T) {
		// Pairs of runs share a stored millisecond: the ID breaks the tie.
		// Relative to now: a fixed date ages into the retention window of the
		// retention subtest below, which then deletes these rows too.
		base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
		for i := 0; i < 9; i++ {
			require.NoError(t, repo.Create(ctx, &models.ActionRun{
				ID:         fmt.Sprintf("page-%02d", i),
				InstanceID: "inst-page",
				Action:     "refresh-db",
				UserID:     "u1",
				StartedAt:  base.Add(time.Duration(i/2) * time.Millisecond).Add(100 * time.Microsecond),
				Parameters: `{"image":"golden"}`,
			}))
		}
		all, next, err := repo.ListByInstance(ctx, "inst-page", "", 100)
		require.NoError(t, err)
		require.Len(t, all, 9)
		assert.Empty(t, next)
		want := []string{"page-08", "page-07", "page-06", "page-05", "page-04", "page-03", "page-02", "page-01", "page-00"}
		got := make([]string, 0, len(all))
		for _, r := range all {
			got = append(got, r.ID)
		}
		assert.Equal(t, want, got)

		var paged []string
		cursor := ""
		for pages := 0; pages < 10; pages++ {
			runs, next, err := repo.ListByInstance(ctx, "inst-page", cursor, 2)
			require.NoError(t, err)
			for _, r := range runs {
				paged = append(paged, r.ID)
			}
			if next == "" {
				break
			}
			cursor = next
		}
		assert.Equal(t, want, paged, "every run once, newest first")
	})

	t.Run("concurrent final status has one winner", func(t *testing.T) {
		require.NoError(t, repo.Create(ctx, &models.ActionRun{
			ID: "race", InstanceID: "inst-race", Action: "refresh-db", UserID: "u1", StatusCode: 202, JobID: "job-race",
		}))
		const callers = 8
		var wg sync.WaitGroup
		results := make([]bool, callers)
		errs := make([]error, callers)
		start := make(chan struct{})
		for i := 0; i < callers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				results[i], errs[i] = repo.SetJobFinalStatus(ctx, "inst-race", "refresh-db", "job-race",
					fmt.Sprintf("status_%c", 'a'+i), time.Now().UTC())
			}(i)
		}
		close(start)
		wg.Wait()

		winners := 0
		winner := ""
		for i := range results {
			require.NoError(t, errs[i])
			if results[i] {
				winners++
				winner = fmt.Sprintf("status_%c", 'a'+i)
			}
		}
		assert.Equal(t, 1, winners, "exactly one caller stores the final status")
		runs, _, err := repo.ListByInstance(ctx, "inst-race", "", 10)
		require.NoError(t, err)
		require.Len(t, runs, 1)
		assert.Equal(t, winner, runs[0].JobStatus)
		assert.NotNil(t, runs[0].FinishedAt)
	})

	t.Run("instance delete removes the runs in the transaction", func(t *testing.T) {
		instances := NewGORMStackInstanceRepository(db)
		for _, id := range []string{"inst-del", "inst-keep"} {
			require.NoError(t, instances.Create(context.Background(), &models.StackInstance{ID: id, Name: id, Namespace: "stack-" + id, OwnerID: "owner", StackDefinitionID: "d1", Status: models.StackStatusDraft}))
			for i := 0; i < 3; i++ {
				require.NoError(t, repo.Create(ctx, &models.ActionRun{InstanceID: id, Action: "seed", UserID: "u1", StatusCode: 200}))
			}
		}
		inst, err := instances.FindByID(context.Background(), "inst-del")
		require.NoError(t, err)
		require.NoError(t, NewGORMTxRunner(db).RunInTx(context.Background(), func(repos TxRepos) error {
			return DeleteInstanceRecord(context.Background(), repos, inst)
		}))

		runs, _, err := repo.ListByInstance(ctx, "inst-del", "", 10)
		require.NoError(t, err)
		assert.Empty(t, runs)
		runs, _, err = repo.ListByInstance(ctx, "inst-keep", "", 10)
		require.NoError(t, err)
		assert.Len(t, runs, 3)
	})

	t.Run("retention by database time", func(t *testing.T) {
		for i := 0; i < 4; i++ {
			id := fmt.Sprintf("ret-%d", i)
			require.NoError(t, repo.Create(ctx, &models.ActionRun{ID: id, InstanceID: "inst-ret", Action: "seed", UserID: "u1", StatusCode: 200}))
			age := 5 * 24 * time.Hour
			if i == 3 {
				age = time.Hour
			}
			setRowAge(t, db, "action_runs", "started_at", id, age)
		}
		deleted, err := NewGORMRetentionRepository(db).WithPause(0).DeleteActionRunsOlderThan(ctx, retentionAge, 2)
		require.NoError(t, err)
		assert.EqualValues(t, 3, deleted)
		runs, _, err := repo.ListByInstance(ctx, "inst-ret", "", 10)
		require.NoError(t, err)
		require.Len(t, runs, 1)
		assert.Equal(t, "ret-3", runs[0].ID)
	})
}

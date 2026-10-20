package database

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// retentionAge is the retention of the tests. "Old" rows are 5 days old.
const retentionAge = 2 * 24 * time.Hour

// setRowAge moves the time column of one row to age before the database time.
func setRowAge(t *testing.T, db *gorm.DB, table, column, id string, age time.Duration) {
	t.Helper()
	// #nosec G202 -- test code with constant names.
	require.NoError(t, db.Exec("UPDATE "+table+" SET "+column+" = "+dbNowMinus(db, age)+" WHERE id = ?", id).Error)
}

// deployLogFixture is one deploy log of seedDeployLogFixtures and whether
// retention must keep it.
type deployLogFixture struct {
	id, instance, action, status string
	age                          time.Duration
	keep                         bool
}

// seedDeployLogFixtures creates instances inst-a, inst-d, inst-f and inst-r
// (inst-gone does not exist) and the deploy logs of the keep rules.
func seedDeployLogFixtures(t *testing.T, db *gorm.DB) []deployLogFixture {
	t.Helper()
	d := func(days int) time.Duration { return time.Duration(days) * 24 * time.Hour }
	recent := time.Hour
	for _, id := range []string{"inst-a", "inst-d", "inst-f", "inst-r"} {
		require.NoError(t, db.Create(&models.StackInstance{
			ID: id, Name: id, StackDefinitionID: "def-1", OwnerID: "u1", Namespace: "stack-" + id, Branch: "main",
			Status: models.StackStatusRunning, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}).Error)
	}
	deploy, rollback, stop := models.DeployActionDeploy, models.DeployActionRollback, models.DeployActionStop
	ok, failed := models.DeployLogSuccess, models.DeployLogError
	fixtures := []deployLogFixture{
		// inst-a: the two newest successful deploy or rollback logs stay.
		{id: "a-oldest-deploy", instance: "inst-a", action: deploy, status: ok, age: d(10)},
		{id: "a-older-rollback", instance: "inst-a", action: rollback, status: ok, age: d(8)},
		{id: "a-newest-deploy", instance: "inst-a", action: deploy, status: ok, age: d(6), keep: true},
		{id: "a-failed-deploy", instance: "inst-a", action: deploy, status: failed, age: d(5)},
		{id: "a-partial-deploy", instance: "inst-a", action: deploy, status: models.DeployLogPartial, age: d(5)},
		{id: "a-newest-rollback", instance: "inst-a", action: rollback, status: ok, age: d(4), keep: true},
		{id: "a-old-stop", instance: "inst-a", action: stop, status: ok, age: d(4)},
		{id: "a-running", instance: "inst-a", action: deploy, status: models.DeployLogRunning, age: d(10), keep: true},
		{id: "a-recent-stop", instance: "inst-a", action: stop, status: ok, age: recent, keep: true},
		// inst-r: two newer rollbacks, but the newest deploy stays.
		{id: "r-newest-deploy", instance: "inst-r", action: deploy, status: ok, age: d(9), keep: true},
		{id: "r-rollback-1", instance: "inst-r", action: rollback, status: ok, age: d(8)},
		{id: "r-rollback-2", instance: "inst-r", action: rollback, status: ok, age: d(7), keep: true},
		{id: "r-rollback-3", instance: "inst-r", action: rollback, status: ok, age: d(6), keep: true},
		// inst-d: a recent deploy; the old one is the second newest and stays.
		{id: "d-oldest-deploy", instance: "inst-d", action: deploy, status: ok, age: d(12)},
		{id: "d-old-deploy", instance: "inst-d", action: deploy, status: ok, age: d(10), keep: true},
		{id: "d-recent-deploy", instance: "inst-d", action: deploy, status: ok, age: recent, keep: true},
		// inst-f: the newest log is an old failed deploy. It stays, so a
		// rollback still targets the newest successful deploy.
		{id: "f-deploy-1", instance: "inst-f", action: deploy, status: ok, age: d(9)},
		{id: "f-deploy-2", instance: "inst-f", action: deploy, status: ok, age: d(8), keep: true},
		{id: "f-deploy-3", instance: "inst-f", action: deploy, status: ok, age: d(7), keep: true},
		{id: "f-failed-newest", instance: "inst-f", action: deploy, status: failed, age: d(6), keep: true},
		// A deleted instance keeps nothing old.
		{id: "gone-deploy", instance: "inst-gone", action: deploy, status: ok, age: d(5)},
	}
	for _, f := range fixtures {
		require.NoError(t, db.Create(&models.DeploymentLog{
			ID: f.id, StackInstanceID: f.instance, Action: f.action, Status: f.status, StartedAt: time.Now().UTC(),
		}).Error)
		setRowAge(t, db, "deployment_logs", "started_at", f.id, f.age)
	}
	return fixtures
}

// assertDeployLogFixtures checks that exactly the kept fixtures remain.
func assertDeployLogFixtures(t *testing.T, db *gorm.DB, fixtures []deployLogFixture, deleted int64) {
	t.Helper()
	var ids []string
	require.NoError(t, db.Model(&models.DeploymentLog{}).Order("id").Pluck("id", &ids).Error)
	var want []string
	wantDeleted := int64(0)
	for _, f := range fixtures {
		if f.keep {
			want = append(want, f.id)
		} else {
			wantDeleted++
		}
	}
	assert.ElementsMatch(t, want, ids)
	assert.Equal(t, wantDeleted, deleted)
}

func TestGORMRetentionRepository_DeploymentLogs(t *testing.T) {
	t.Parallel()
	db := setupTestDBWithAllTables(t)
	fixtures := seedDeployLogFixtures(t, db)
	repo := NewGORMRetentionRepository(db).WithPause(0)

	deleted, err := repo.DeleteDeploymentLogsOlderThan(context.Background(), retentionAge, 3)
	require.NoError(t, err)
	assertDeployLogFixtures(t, db, fixtures, deleted)

	// A second run deletes nothing.
	deleted, err = repo.DeleteDeploymentLogsOlderThan(context.Background(), retentionAge, 3)
	require.NoError(t, err)
	assert.Zero(t, deleted)
}

func TestGORMRetentionRepository_OtherTables(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		table  string
		column string
		create func(db *gorm.DB, id string) error
		del    func(r *GORMRetentionRepository, ctx context.Context, age time.Duration, batch int) (int64, error)
	}{
		{
			name: "audit logs", table: "audit_logs", column: "timestamp",
			create: func(db *gorm.DB, id string) error {
				return db.Create(&models.AuditLog{ID: id, UserID: "u1", Action: "create", EntityType: "stack_instance", EntityID: "e1", Timestamp: time.Now().UTC()}).Error
			},
			del: (*GORMRetentionRepository).DeleteAuditLogsOlderThan,
		},
		{
			name: "notifications", table: "notifications", column: "created_at",
			create: func(db *gorm.DB, id string) error {
				return db.Create(&models.Notification{ID: id, UserID: "u1", Type: "deployment.success", Title: "t", CreatedAt: time.Now().UTC()}).Error
			},
			del: (*GORMRetentionRepository).DeleteNotificationsOlderThan,
		},
		{
			name: "delivery logs", table: "notification_delivery_logs", column: "created_at",
			create: func(db *gorm.DB, id string) error {
				return db.Create(&models.NotificationDeliveryLog{ID: id, ChannelID: "ch1", EventType: "deployment.success", CreatedAt: time.Now().UTC()}).Error
			},
			del: (*GORMRetentionRepository).DeleteDeliveryLogsOlderThan,
		},
		{
			name: "action runs", table: "action_runs", column: "started_at",
			create: func(db *gorm.DB, id string) error {
				return db.Create(&models.ActionRun{ID: id, InstanceID: "i1", Action: "seed", UserID: "u1", StartedAt: time.Now().UTC(), StatusCode: 200}).Error
			},
			del: (*GORMRetentionRepository).DeleteActionRunsOlderThan,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			db := setupTestDBWithAllTables(t)
			// setupTestDBWithAllTables has no delivery log table.
			require.NoError(t, db.AutoMigrate(&models.NotificationDeliveryLog{}))
			// 25 old rows and 2 recent rows.
			for i := 0; i < 27; i++ {
				id := fmt.Sprintf("row-%02d", i)
				require.NoError(t, tt.create(db, id))
				age := 5 * 24 * time.Hour
				if i >= 25 {
					age = time.Hour
				}
				setRowAge(t, db, tt.table, tt.column, id, age)
			}
			// Count the DELETE statements.
			var deletes atomic.Int32
			require.NoError(t, db.Callback().Delete().After("gorm:delete").Register("count_deletes_"+tt.table, func(*gorm.DB) {
				deletes.Add(1)
			}))
			repo := NewGORMRetentionRepository(db).WithPause(0)

			deleted, err := tt.del(repo, context.Background(), retentionAge, 10)
			require.NoError(t, err)

			assert.EqualValues(t, 25, deleted)
			assert.EqualValues(t, 3, deletes.Load(), "25 rows in batches of 10")
			var left int64
			require.NoError(t, db.Table(tt.table).Count(&left).Error)
			assert.EqualValues(t, 2, left)
		})
	}
}

func TestGORMRetentionRepository_CanceledContext(t *testing.T) {
	t.Parallel()
	db := setupTestDBWithAllTables(t)
	repo := NewGORMRetentionRepository(db)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := repo.DeleteAuditLogsOlderThan(ctx, retentionAge, 10)
	assert.ErrorIs(t, err, context.Canceled)
}

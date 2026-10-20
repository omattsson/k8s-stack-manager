package database

import (
	"context"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"gorm.io/gorm"
)

// DefaultRetentionBatchSize is the number of rows of one delete statement
// when a retention method gets a batch size <= 0.
const DefaultRetentionBatchSize = 1000

// DefaultDeployLogRetentionBatchSize is the batch size of deployment_logs.
// A deploy log row holds up to 64 KiB of output and a values snapshot, and a
// row-based binary log writes the full rows: 100 rows keep one transaction
// small enough for replication.
const DefaultDeployLogRetentionBatchSize = 100

// defaultRetentionPause is the pause between two delete batches. It gives
// other writers and the binary log time between the statements.
const defaultRetentionPause = 100 * time.Millisecond

// finishedDeployLogStatuses are the deploy log states that retention may
// delete. A running log is never deleted (interrupted operation recovery).
// The list (not "status <> running") uses the index (status, started_at).
var finishedDeployLogStatuses = []string{models.DeployLogSuccess, models.DeployLogError, models.DeployLogPartial}

// GORMRetentionRepository implements models.RetentionRepository.
type GORMRetentionRepository struct {
	db    *gorm.DB
	pause time.Duration
}

// NewGORMRetentionRepository creates a GORMRetentionRepository.
func NewGORMRetentionRepository(db *gorm.DB) *GORMRetentionRepository {
	return &GORMRetentionRepository{db: db, pause: defaultRetentionPause}
}

// WithPause sets the pause between two batches (tests use 0). Returns r.
func (r *GORMRetentionRepository) WithPause(pause time.Duration) *GORMRetentionRepository {
	r.pause = pause
	return r
}

// DeleteDeploymentLogsOlderThan deletes finished deploy logs older than age.
// For each existing instance it keeps:
//   - the newest deploy or rollback log of any status (the readers below
//     check its status);
//   - the newest successful deploy log (deploy history, rollback target);
//   - the two newest successful deploy or rollback logs: the values drift
//     check reads the newest one (lastOperationWasRollback), and a rollback
//     without a target reads the branch of the second newest
//     (previousRevisionBranch).
//
// A batch size <= 0 uses DefaultDeployLogRetentionBatchSize: a row holds the
// log output and a values snapshot, so a large batch is a large transaction.
func (r *GORMRetentionRepository) DeleteDeploymentLogsOlderThan(ctx context.Context, age time.Duration, batchSize int) (int64, error) {
	if batchSize <= 0 {
		batchSize = DefaultDeployLogRetentionBatchSize
	}
	cutoff := dbNowMinus(r.db, age)
	// newer(a, b) is true when log a of the same instance started after log
	// b (ties by ID).
	newer := func(a, b string) string {
		return a + `.stack_instance_id = ` + b + `.stack_instance_id AND (` + a + `.started_at > ` + b + `.started_at ` +
			`OR (` + a + `.started_at = ` + b + `.started_at AND ` + a + `.id > ` + b + `.id))`
	}
	const dl = "deployment_logs"
	history := []string{models.DeployActionDeploy, models.DeployActionRollback}
	// A deploy or rollback log may go only when:
	//   - a newer deploy or rollback log exists (any status): the readers
	//     check the status of the newest one;
	//   - it is not successful, or two newer successful deploy or rollback
	//     logs exist and (for a deploy) a newer successful deploy exists.
	// The two nested EXISTS stop at the first match (no full count).
	// #nosec G202 -- the expressions are built from constants.
	deletable := `NOT EXISTS (SELECT 1 FROM stack_instances si WHERE si.id = ` + dl + `.stack_instance_id) ` +
		`OR ` + dl + `.action NOT IN ? ` +
		`OR (EXISTS (SELECT 1 FROM deployment_logs nx WHERE nx.action IN ? AND ` + newer("nx", dl) + `) ` +
		`AND (` + dl + `.status <> ? ` +
		`OR ((` + dl + `.action <> ? OR EXISTS (SELECT 1 FROM deployment_logs nd WHERE nd.action = ? AND nd.status = ? AND ` + newer("nd", dl) + `)) ` +
		`AND EXISTS (SELECT 1 FROM deployment_logs n1 WHERE n1.action IN ? AND n1.status = ? AND ` + newer("n1", dl) + ` ` +
		`AND EXISTS (SELECT 1 FROM deployment_logs n2 WHERE n2.action IN ? AND n2.status = ? AND ` + newer("n2", "n1") + `)))))`
	deletableArgs := []any{
		history,
		history,
		models.DeployLogSuccess,
		models.DeployActionDeploy, models.DeployActionDeploy, models.DeployLogSuccess,
		history, models.DeployLogSuccess,
		history, models.DeployLogSuccess,
	}

	finished := func(tx *gorm.DB) *gorm.DB {
		// #nosec G202 -- cutoff is built from a duration, not from input.
		return tx.Where("deployment_logs.status IN ? AND deployment_logs.started_at < "+cutoff, finishedDeployLogStatuses)
	}
	// MySQL refuses a DELETE with a subquery on its own table (error 1093),
	// so only the ID select checks the keep rules. A row that the select
	// returns cannot become protected before the delete: a finished log keeps
	// its status, and the newer logs that make it deletable are newer than
	// the logs that retention deletes.
	return r.deleteInBatches(ctx, &models.DeploymentLog{}, batchSize,
		func(tx *gorm.DB) *gorm.DB { return finished(tx).Where(deletable, deletableArgs...) },
		finished)
}

// DeleteAuditLogsOlderThan deletes audit logs older than age.
func (r *GORMRetentionRepository) DeleteAuditLogsOlderThan(ctx context.Context, age time.Duration, batchSize int) (int64, error) {
	cutoff := dbNowMinus(r.db, age)
	older := func(tx *gorm.DB) *gorm.DB {
		return tx.Where("timestamp < " + cutoff) // #nosec G202 -- built from a duration
	}
	return r.deleteInBatches(ctx, &models.AuditLog{}, batchSize, older, older)
}

// DeleteNotificationsOlderThan deletes notifications older than age.
func (r *GORMRetentionRepository) DeleteNotificationsOlderThan(ctx context.Context, age time.Duration, batchSize int) (int64, error) {
	cutoff := dbNowMinus(r.db, age)
	older := func(tx *gorm.DB) *gorm.DB {
		return tx.Where("created_at < " + cutoff) // #nosec G202 -- built from a duration
	}
	return r.deleteInBatches(ctx, &models.Notification{}, batchSize, older, older)
}

// DeleteDeliveryLogsOlderThan deletes notification channel delivery logs
// older than age.
func (r *GORMRetentionRepository) DeleteDeliveryLogsOlderThan(ctx context.Context, age time.Duration, batchSize int) (int64, error) {
	cutoff := dbNowMinus(r.db, age)
	older := func(tx *gorm.DB) *gorm.DB {
		return tx.Where("created_at < " + cutoff) // #nosec G202 -- built from a duration
	}
	return r.deleteInBatches(ctx, &models.NotificationDeliveryLog{}, batchSize, older, older)
}

// DeleteActionRunsOlderThan deletes action runs that started before age.
func (r *GORMRetentionRepository) DeleteActionRunsOlderThan(ctx context.Context, age time.Duration, batchSize int) (int64, error) {
	cutoff := dbNowMinus(r.db, age)
	older := func(tx *gorm.DB) *gorm.DB {
		return tx.Where("started_at < " + cutoff) // #nosec G202 -- built from a duration
	}
	return r.deleteInBatches(ctx, &models.ActionRun{}, batchSize, older, older)
}

// deleteInBatches deletes the rows of model that match selectScope. Each
// batch reads up to batchSize IDs with selectScope and deletes them with
// deleteScope (a row that stopped matching stays). The IDs are UUIDs, so a primary key range
// (as in ws_events) does not work; MySQL has no portable DELETE ... LIMIT
// with SQLite. Between two batches it waits r.pause.
func (r *GORMRetentionRepository) deleteInBatches(ctx context.Context, model any, batchSize int, selectScope, deleteScope func(*gorm.DB) *gorm.DB) (int64, error) {
	if batchSize <= 0 {
		batchSize = DefaultRetentionBatchSize
	}
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		var ids []string
		if err := selectScope(r.db.WithContext(ctx).Model(model)).Limit(batchSize).Pluck("id", &ids).Error; err != nil {
			return total, dberrors.NewDatabaseError("retention_select", err)
		}
		if len(ids) == 0 {
			return total, nil
		}
		res := deleteScope(r.db.WithContext(ctx)).Where("id IN ?", ids).Delete(model)
		if res.Error != nil {
			return total, dberrors.NewDatabaseError("retention_delete", res.Error)
		}
		total += res.RowsAffected
		if len(ids) < batchSize {
			return total, nil
		}
		if r.pause > 0 {
			select {
			case <-ctx.Done():
				return total, ctx.Err()
			case <-time.After(r.pause):
			}
		}
	}
}

package models

import (
	"context"
	"time"
)

// RetentionRepository deletes old rows of the log tables for the leader
// worker log-retention. Each method deletes the rows older than age (database
// time) in batches of batchSize rows and returns the number of deleted rows.
// It returns the rows deleted so far with an error.
type RetentionRepository interface {
	// DeleteDeploymentLogsOlderThan deletes finished deploy logs whose
	// started_at is older than age. For each existing instance it keeps the
	// running logs, the newest deploy or rollback log (any status), the newest
	// successful deploy log and the two newest successful deploy or rollback
	// logs.
	DeleteDeploymentLogsOlderThan(ctx context.Context, age time.Duration, batchSize int) (int64, error)
	// DeleteAuditLogsOlderThan deletes audit logs by timestamp.
	DeleteAuditLogsOlderThan(ctx context.Context, age time.Duration, batchSize int) (int64, error)
	// DeleteNotificationsOlderThan deletes notifications (read and unread)
	// by created_at.
	DeleteNotificationsOlderThan(ctx context.Context, age time.Duration, batchSize int) (int64, error)
	// DeleteDeliveryLogsOlderThan deletes notification channel delivery
	// logs by created_at.
	DeleteDeliveryLogsOlderThan(ctx context.Context, age time.Duration, batchSize int) (int64, error)
	// DeleteActionRunsOlderThan deletes action runs by started_at.
	DeleteActionRunsOlderThan(ctx context.Context, age time.Duration, batchSize int) (int64, error)
}

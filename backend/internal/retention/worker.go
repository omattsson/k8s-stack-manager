// Package retention deletes old rows of the log tables (deployment_logs,
// audit_logs, notifications, notification_delivery_logs). The leader runs it
// as the worker log-retention.
package retention

import (
	"context"
	"log/slog"
	"time"

	"backend/internal/config"
	"backend/internal/models"
)

// Table names, also the "table" attribute of the metric.
const (
	TableDeploymentLogs = "deployment_logs"
	TableAuditLogs      = "audit_logs"
	TableNotifications  = "notifications"
	TableDeliveryLogs   = "notification_delivery_logs"
)

// day is the length of one retention day.
const day = 24 * time.Hour

// Worker deletes the rows that are older than their retention.
type Worker struct {
	repo      models.RetentionRepository
	cfg       config.RetentionConfig
	interval  time.Duration
	batchSize int
}

// NewWorker creates a Worker. A config Interval of 0 uses
// config.DefaultLogRetentionInterval.
func NewWorker(repo models.RetentionRepository, cfg config.RetentionConfig) *Worker {
	interval := cfg.Interval
	if interval <= 0 {
		interval = config.DefaultLogRetentionInterval
	}
	return &Worker{repo: repo, cfg: cfg, interval: interval}
}

// WithBatchSize sets the rows of one delete statement (0: the repository
// default). Returns w.
func (w *Worker) WithBatchSize(n int) *Worker {
	w.batchSize = n
	return w
}

// Run deletes old rows once at the start of the leader term, then every
// interval, until ctx ends. It can run again after a leader change.
func (w *Worker) Run(ctx context.Context) {
	w.RunOnce(ctx)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.RunOnce(ctx)
		}
	}
}

// table is one table with its retention and its delete method.
type table struct {
	del  func(ctx context.Context, age time.Duration, batchSize int) (int64, error)
	name string
	days int
}

// RunOnce deletes the old rows of each table with a retention (days > 0).
// A failure in one table does not stop the other tables. It returns the
// number of deleted rows by table.
func (w *Worker) RunOnce(ctx context.Context) map[string]int64 {
	tables := []table{
		{name: TableDeploymentLogs, days: w.cfg.DeployLogDays, del: w.repo.DeleteDeploymentLogsOlderThan},
		{name: TableAuditLogs, days: w.cfg.AuditLogDays, del: w.repo.DeleteAuditLogsOlderThan},
		{name: TableNotifications, days: w.cfg.NotificationDays, del: w.repo.DeleteNotificationsOlderThan},
		{name: TableDeliveryLogs, days: w.cfg.DeliveryLogDays, del: w.repo.DeleteDeliveryLogsOlderThan},
	}
	deleted := make(map[string]int64, len(tables))
	for _, t := range tables {
		if t.days <= 0 {
			continue
		}
		if ctx.Err() != nil {
			return deleted
		}
		start := time.Now()
		n, err := t.del(ctx, time.Duration(t.days)*day, w.batchSize)
		deleted[t.name] = n
		recordDeleted(t.name, n)
		if err != nil {
			if ctx.Err() != nil {
				// The leader term ended: the next term continues.
				return deleted
			}
			slog.Error("log retention failed", "table", t.name, "retention_days", t.days, "deleted", n, "error", err)
			continue
		}
		level := slog.LevelDebug
		if n > 0 {
			level = slog.LevelInfo
		}
		slog.Log(ctx, level, "log retention run", "table", t.name, "retention_days", t.days,
			"deleted", n, "duration", time.Since(start).Round(time.Millisecond))
	}
	return deleted
}

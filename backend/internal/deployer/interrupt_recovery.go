package deployer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"backend/internal/models"
	"backend/internal/replica"
	"backend/internal/websocket"
	"backend/pkg/dberrors"
)

const (
	// DeadlineMargin is added to the time budget of an operation for its
	// deadline (deployment_logs.deadline_at).
	DeadlineMargin = 5 * time.Minute
	// DefaultInterruptCheckInterval is how often the leader checks for
	// interrupted operations.
	DefaultInterruptCheckInterval = time.Minute
	// heartbeatRetention is the age after which the leader deletes a
	// heartbeat row. A missing row counts as a stopped process, so the
	// value only keeps rows for diagnosis.
	heartbeatRetention = time.Hour
	// interruptBatch is the number of running logs that one check reads.
	interruptBatch = 100
	// interruptQueryTimeout limits each database call of a check.
	interruptQueryTimeout = 30 * time.Second
	// auditActionInterrupted is the audit action of a recovery (see
	// middleware.AuditActionInterrupted).
	auditActionInterrupted = "interrupted"
)

// InterruptRecoveryConfig holds the dependencies of InterruptRecovery.
type InterruptRecoveryConfig struct {
	Operations models.InterruptedOperationRepository
	Heartbeats models.ReplicaHeartbeatRepository
	Instances  models.StackInstanceRepository
	// AuditLog is optional: nil writes no audit entry.
	AuditLog models.AuditLogRepository
	// Hub is optional: nil sends no status message.
	Hub websocket.BroadcastSender
	// Notifier is optional: nil sends no notification.
	Notifier LifecycleNotifier
	// SelfID is the process identity of this process. Its operations are
	// never ended by the recovery.
	SelfID string
	// StaleAfter is the heartbeat age after which a process counts as
	// stopped. 0 uses replica.StaleAfter.
	StaleAfter time.Duration
	// Interval is the time between two checks. 0 uses
	// DefaultInterruptCheckInterval.
	Interval time.Duration
}

// InterruptRecovery is a leader-only worker. It ends the operations
// (deploy, rollback, stop, clean) whose replica stopped without a clean
// shutdown (SIGKILL, OOM kill, node loss): the running deploy log passed
// its deadline (deadline_at: the time budget of the operation plus
// DeadlineMargin), and the process identity on the log has no heartbeat for
// StaleAfter (database time). Logs without a replica ID or a deadline
// (written before migration 54) are skipped. The log and the instance get error, the instance loses the
// post-deploy hook marker, and the owner and the followers get the error
// notification of the action. An operation whose process has a fresh
// heartbeat is never changed.
type InterruptRecovery struct {
	cfg InterruptRecoveryConfig
	now func() time.Time
}

// NewInterruptRecovery creates the worker. It returns nil when a required
// repository is missing; Run of a nil worker returns at once.
func NewInterruptRecovery(cfg InterruptRecoveryConfig) *InterruptRecovery {
	if cfg.Operations == nil || cfg.Heartbeats == nil || cfg.Instances == nil {
		return nil
	}
	if cfg.StaleAfter <= 0 {
		cfg.StaleAfter = replica.StaleAfter
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterruptCheckInterval
	}
	return &InterruptRecovery{cfg: cfg, now: time.Now}
}

// Run checks at the start of the leadership term and then every interval
// until ctx is done.
func (r *InterruptRecovery) Run(ctx context.Context) {
	if r == nil {
		return
	}
	r.RunOnce(ctx)
	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.RunOnce(ctx)
		}
	}
}

// RunOnce runs one check and returns the number of deploy logs it closed.
// It also deletes old heartbeat rows.
func (r *InterruptRecovery) RunOnce(ctx context.Context) int {
	if r == nil {
		return 0
	}
	now := r.now().UTC()
	closed := r.recover(ctx, now)

	pctx, cancel := context.WithTimeout(ctx, interruptQueryTimeout)
	defer cancel()
	if _, err := r.cfg.Heartbeats.DeleteOlderThan(pctx, heartbeatRetention); err != nil && ctx.Err() == nil {
		slog.Warn("Replica heartbeat cleanup failed", "error", err)
	}
	return closed
}

func (r *InterruptRecovery) recover(ctx context.Context, now time.Time) int {
	qctx, cancel := context.WithTimeout(ctx, interruptQueryTimeout)
	defer cancel()
	// The query excludes the own process and the processes with a fresh
	// heartbeat (database time), so live operations cannot fill the batch.
	logs, err := r.cfg.Operations.ListInterruptCandidates(qctx, now, r.cfg.SelfID, r.cfg.StaleAfter, interruptBatch)
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("Interrupted operation check: listing running deploy logs failed", "error", err)
		}
		return 0
	}

	closed := 0
	for i := range logs {
		if ctx.Err() != nil {
			return closed
		}
		l := logs[i]
		if l.ReplicaID == "" || l.ReplicaID == r.cfg.SelfID {
			continue
		}
		if r.interrupt(ctx, l, now) {
			closed++
		}
	}
	return closed
}

// interrupt ends one operation. It reports whether it closed the log.
func (r *InterruptRecovery) interrupt(ctx context.Context, l models.DeploymentLog, now time.Time) bool {
	inst, err := r.cfg.Instances.FindByID(l.StackInstanceID)
	if err != nil && !errors.Is(err, dberrors.ErrNotFound) {
		slog.Error("Interrupted operation check: reading the instance failed",
			"instance_id", l.StackInstanceID, "deploy_log_id", l.ID, "error", err)
		return false
	}
	if err != nil {
		inst = nil
	}

	message := models.InterruptedOperationMessage(l.Action)
	req := models.InterruptRequest{
		LogID:        l.ID,
		LogStartedAt: l.StartedAt,
		InstanceID:   l.StackInstanceID,
		ReplicaID:    l.ReplicaID,
		StaleAfter:   r.cfg.StaleAfter,
		Message:      message,
		Now:          now,
	}
	if inst != nil && isInProgressStatus(inst.Status) {
		req.InstanceStatus = inst.Status
		req.InstanceUpdatedAt = inst.UpdatedAt
	}

	ictx, cancel := context.WithTimeout(ctx, interruptQueryTimeout)
	defer cancel()
	res, err := r.cfg.Operations.InterruptOperation(ictx, req)
	if errors.Is(err, models.ErrInterruptConflict) {
		slog.Info("Interrupted operation skipped: the instance changed; next check tries again",
			"instance_id", l.StackInstanceID, "deploy_log_id", l.ID)
		return false
	}
	if err != nil {
		slog.Error("Interrupted operation could not be ended",
			"instance_id", l.StackInstanceID, "deploy_log_id", l.ID, "error", err)
		return false
	}
	if !res.LogClosed {
		return false
	}

	slog.Warn("Interrupted operation ended: its replica stopped",
		"instance_id", l.StackInstanceID,
		"deploy_log_id", l.ID,
		"action", l.Action,
		"replica_id", l.ReplicaID,
		"started_at", l.StartedAt,
		"instance_set_to_error", res.InstanceUpdated)
	recordInterruptEvent(l.Action, interruptEventRecovered)
	r.audit(l, res.InstanceUpdated, now)

	if res.InstanceUpdated && inst != nil {
		r.broadcast(l, message)
		r.notify(ctx, inst, l.Action, message)
	}
	return true
}

func (r *InterruptRecovery) audit(l models.DeploymentLog, instanceUpdated bool, now time.Time) {
	if r.cfg.AuditLog == nil {
		return
	}
	details := fmt.Sprintf("Operation %s (deploy log %s) interrupted: replica %s stopped", l.Action, l.ID, l.ReplicaID)
	if instanceUpdated {
		details += "; instance set to error"
	}
	entry := &models.AuditLog{
		UserID:     "system",
		Username:   "system",
		Action:     auditActionInterrupted,
		EntityType: "stack_instance",
		EntityID:   l.StackInstanceID,
		Details:    details,
		Timestamp:  now,
	}
	if err := r.cfg.AuditLog.Create(entry); err != nil {
		slog.Error("Failed to create audit log for an interrupted operation",
			"instance_id", l.StackInstanceID, "deploy_log_id", l.ID, "error", err)
	}
}

// broadcast sends the error status to the clients of all replicas (the hub
// writes the fan-out row when fan-out is on).
func (r *InterruptRecovery) broadcast(l models.DeploymentLog, message string) {
	if r.cfg.Hub == nil {
		return
	}
	msg, err := websocket.NewMessage("deployment.status", deploymentStatusPayload{
		InstanceID:   l.StackInstanceID,
		Status:       models.StackStatusError,
		LogID:        l.ID,
		ErrorMessage: message,
		Action:       l.Action,
	})
	if err != nil {
		slog.Error("failed to create deployment status message", "error", err)
		return
	}
	data, err := msg.Bytes()
	if err != nil {
		slog.Error("failed to serialize deployment status message", "error", err)
		return
	}
	r.cfg.Hub.Broadcast(data)
}

// notify sends the error notification of the action to the owner and the
// followers, as for a failed operation.
func (r *InterruptRecovery) notify(ctx context.Context, inst *models.StackInstance, action, message string) {
	if r.cfg.Notifier == nil {
		return
	}
	notifType, title, text := "deployment.error", "Deployment failed", fmt.Sprintf("Deployment of %s failed: %s", inst.Name, message)
	switch action {
	case models.DeployActionRollback:
		notifType, title, text = "rollback.error", "Rollback failed", fmt.Sprintf("Rollback of %s failed: %s", inst.Name, message)
	case models.DeployActionStop:
		notifType, title, text = "stop.error", "Stop failed", fmt.Sprintf("Stopping %s failed: %s", inst.Name, message)
	case models.DeployActionClean:
		notifType, title, text = "clean.error", "Cleanup failed", fmt.Sprintf("Cleanup of %s failed: %s", inst.Name, message)
	}
	if err := r.cfg.Notifier.NotifyInstance(ctx, models.NewNotificationTarget(inst), notifType, title, text); err != nil {
		slog.Error("failed to create interrupted operation notification",
			"instance_id", inst.ID, "type", notifType, "error", err)
	}
}

// isInProgressStatus reports whether status is a status of a running
// operation.
func isInProgressStatus(status string) bool {
	switch status {
	case models.StackStatusQueued, models.StackStatusDeploying, models.StackStatusStabilizing,
		models.StackStatusStopping, models.StackStatusCleaning:
		return true
	}
	return false
}

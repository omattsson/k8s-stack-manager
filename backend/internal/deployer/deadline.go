package deployer

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"backend/internal/hooks"
	"backend/internal/models"
)

// defaultHelmTimeout is the helm timeout when the executor is nil (the same
// fallback as the operation goroutines).
const defaultHelmTimeout = 5 * time.Minute

// helmTimeoutOf returns the helm timeout of one helm command.
func helmTimeoutOf(helm HelmExecutor) time.Duration {
	if helm == nil {
		return defaultHelmTimeout
	}
	return helm.Timeout()
}

// preSlotBudget returns the longest time an operation can spend before it
// gets its concurrency slot, without the wait for the slot: the pre-deploy
// or pre-rollback hooks (subscribers run one after the other).
func (m *Manager) preSlotBudget(action string) time.Duration {
	switch action {
	case models.DeployActionDeploy:
		return m.hooks.TotalTimeout(hooks.EventPreDeploy)
	case models.DeployActionRollback:
		return m.hooks.TotalTimeout(hooks.EventPreRollback)
	}
	return 0
}

// runBudget returns the longest time an operation can run after it got its
// concurrency slot until its deploy log is written:
//   - deploy: one helm timeout per chart plus a margin (deployBudget), the
//     readiness wait and the blocking post-deploy hooks (their timeouts plus
//     the phase margin);
//   - rollback: deployBudget and the readiness wait;
//   - stop and clean: one helm timeout for all charts plus a chart margin.
func (m *Manager) runBudget(action string, helmTimeout time.Duration, charts int) time.Duration {
	switch action {
	case models.DeployActionDeploy:
		budget := deployBudget(helmTimeout, charts) + m.stabilizeTimeout
		if m.hooks.HasBlocking(hooks.EventPostDeploy) {
			budget += m.hooks.BlockingTimeout(hooks.EventPostDeploy) + blockingPhaseMargin
		}
		return budget
	case models.DeployActionRollback:
		return deployBudget(helmTimeout, charts) + m.stabilizeTimeout
	}
	return helmTimeout + chartTimeoutMargin
}

// operationDeadline returns the deadline of an operation that starts at
// start: the pre-slot budget, the run budget and DeadlineMargin. The wait
// for a concurrency slot is not known yet; extendDeadline moves the
// deadline when the operation gets its slot.
func (m *Manager) operationDeadline(start time.Time, action string, helm HelmExecutor, charts int) *time.Time {
	d := start.Add(m.preSlotBudget(action) + m.runBudget(action, helmTimeoutOf(helm), charts) + DeadlineMargin).UTC()
	return &d
}

// extendDeadline moves the deadline of a running log to now + run budget +
// DeadlineMargin when the operation got its concurrency slot (the wait for
// the slot has no limit). It returns false when the log is no longer
// running: the leader ended the operation while it waited for the slot
// (interrupted operation recovery). The caller then stops before any helm
// call and writes nothing. Another write error keeps the first deadline
// (logged); the heartbeat still protects the operation.
func (m *Manager) extendDeadline(deployLog *models.DeploymentLog, helm HelmExecutor, charts int) bool {
	d := time.Now().UTC().Add(m.runBudget(deployLog.Action, helmTimeoutOf(helm), charts) + DeadlineMargin)
	ext, ok := m.logRepo.(models.DeploymentLogDeadlineExtender)
	if !ok {
		if deployLog.DeadlineAt == nil || d.After(*deployLog.DeadlineAt) {
			deployLog.DeadlineAt = &d
		}
		return true
	}
	ctx, cancel := context.WithTimeout(m.shutdownCtx, 10*time.Second)
	defer cancel()
	err := ext.ExtendDeadline(ctx, deployLog.ID, d)
	if errors.Is(err, models.ErrDeployLogNotRunning) {
		slog.Warn("queued operation cancelled: its deploy log was ended by the interrupted operation recovery while it waited for a concurrency slot",
			"instance_id", deployLog.StackInstanceID, "deploy_log_id", deployLog.ID, "action", deployLog.Action)
		recordInterruptEvent(deployLog.Action, interruptEventQueuedCancelled)
		m.logActions.Delete(deployLog.ID)
		return false
	}
	if err != nil {
		slog.Warn("failed to extend the deploy log deadline; the first deadline stays",
			"instance_id", deployLog.StackInstanceID, "deploy_log_id", deployLog.ID,
			"deadline", d, "error", err)
		return true
	}
	if deployLog.DeadlineAt == nil || d.After(*deployLog.DeadlineAt) {
		deployLog.DeadlineAt = &d
	}
	return true
}

package deployer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"backend/internal/models"
	"backend/internal/replica"
)

// helmLogActions are the operations whose Helm work a stop must not overlap.
var helmLogActions = []string{models.DeployActionDeploy, models.DeployActionRollback}

// earlierOperationReadBudget bounds the time that the wait retries failed
// log reads before the stop goes on without it.
const earlierOperationReadBudget = 2 * time.Minute

// errStopInterrupted ends a stop that a server shutdown interrupted before
// its uninstalls. The text is user-safe (sanitizeDeployError keeps it).
var errStopInterrupted = errors.New("Interrupted by a server restart. Stop again.") //nolint:staticcheck // ST1005: user-facing sentence

// waitForEarlierOperations waits until no deploy or rollback of the instance
// that started before the stop of stopLog is still running. A stop may
// start while a deploy runs (status deploying): the deploy sees the stop at
// its next ownership check and ends without installing further charts (a
// running install is cancelled), and a rollback ends at its finalize. Only
// then may the stop uninstall the releases. Without the wait, an install
// that ends after the uninstall leaves releases in a stopped stack.
//
// The wait reads the database, so it also sees an operation of another
// replica. A log is not awaited after its deadline, without a deadline, or
// when its replica has no heartbeat within replica.StaleAfter (the
// interrupted operation recovery ends it later). Failed reads are retried
// for earlierOperationReadBudget. The wait ends at a server shutdown. It
// returns a line for the output of the stop when it waited, else "".
func (m *Manager) waitForEarlierOperations(instanceID string, stopLog *models.DeploymentLog) string {
	if m.logRepo == nil {
		return ""
	}
	ticker := time.NewTicker(m.stabilizePollInterval)
	defer ticker.Stop()
	waitLine := ""
	var failingSince time.Time
	for {
		running, err := m.runningEarlierOperation(instanceID, stopLog)
		switch {
		case err != nil:
			if failingSince.IsZero() {
				failingSince = time.Now()
			}
			if time.Since(failingSince) >= earlierOperationReadBudget {
				slog.Warn("stop: could not check for a running deploy, continuing",
					"instance_id", instanceID, "log_id", stopLog.ID, "error", err)
				return waitLine
			}
		case running == nil:
			return waitLine
		default:
			failingSince = time.Time{}
			if waitLine == "" {
				line := fmt.Sprintf("Waiting for the running %s (log %s) to end before the uninstall", running.Action, running.ID)
				waitLine = line + "\n"
				m.broadcastLog(instanceID, stopLog.ID, line)
				slog.Info("stop waits for a running operation",
					"instance_id", instanceID, "log_id", stopLog.ID, "running_log_id", running.ID, "action", running.Action)
			}
		}
		select {
		case <-m.shutdownCtx.Done():
			return waitLine
		case <-ticker.C:
		}
	}
}

// runningEarlierOperation returns the deploy or rollback log of the instance
// that the stop of stopLog must wait for, nil when there is none.
func (m *Manager) runningEarlierOperation(instanceID string, stopLog *models.DeploymentLog) (*models.DeploymentLog, error) {
	ctx, cancel := context.WithTimeout(m.shutdownCtx, 10*time.Second)
	defer cancel()
	logs, err := m.logRepo.ListLatestByActions(ctx, instanceID, helmLogActions, len(helmLogActions)+1)
	if err != nil {
		return nil, err
	}
	for {
		running := runningEarlierLog(logs, stopLog, time.Now().UTC())
		if running == nil {
			return nil, nil
		}
		alive, err := m.replicaAlive(ctx, running.ReplicaID)
		if err != nil {
			return nil, err
		}
		if alive {
			return running, nil
		}
		slog.Warn("stop: the running deploy belongs to a replica without a heartbeat; not waiting for it",
			"instance_id", instanceID, "log_id", stopLog.ID, "running_log_id", running.ID, "replica_id", running.ReplicaID)
		logs = withoutLog(logs, running.ID)
	}
}

// replicaAlive reports whether the replica of a running log can still end
// it: this process, a log without a replica ID, no heartbeat repository,
// or a heartbeat within replica.StaleAfter.
func (m *Manager) replicaAlive(ctx context.Context, replicaID string) (bool, error) {
	if replicaID == "" || replicaID == m.replicaID || m.heartbeats == nil {
		return true, nil
	}
	seen, err := m.heartbeats.SeenWithin(ctx, []string{replicaID}, replica.StaleAfter)
	if err != nil {
		return false, err
	}
	return seen[replicaID], nil
}

// runningEarlierLog returns the first log of logs (any order) that is not
// the stop log, started before it (or at the same time), is running and has
// a deadline after now, else nil. A deploy that started after the stop is a
// newer operation: the stop does not wait for it (see executeStopWithCharts).
func runningEarlierLog(logs []models.DeploymentLog, stopLog *models.DeploymentLog, now time.Time) *models.DeploymentLog {
	for i := range logs {
		l := &logs[i]
		if l.ID == stopLog.ID || l.Status != models.DeployLogRunning || l.StartedAt.After(stopLog.StartedAt) {
			continue
		}
		if l.DeadlineAt == nil || !now.Before(*l.DeadlineAt) {
			continue
		}
		return l
	}
	return nil
}

// withoutLog returns logs without the log of id.
func withoutLog(logs []models.DeploymentLog, id string) []models.DeploymentLog {
	out := make([]models.DeploymentLog, 0, len(logs))
	for _, l := range logs {
		if l.ID != id {
			out = append(out, l)
		}
	}
	return out
}

// cancelStopForNewerOperation ends a stop whose instance a newer operation
// took while the stop waited (a deploy whose request read the status before
// the stop). Only the stop log changes: the newer operation owns the
// instance and its releases.
func (m *Manager) cancelStopForNewerOperation(instanceID string, stopLog *models.DeploymentLog, output string) {
	ctx, cancel := m.finalWriteCtx()
	defer cancel()
	msg := "stop cancelled: a newer operation started on the instance while the stop waited"
	m.broadcastLog(instanceID, stopLog.ID, "WARNING: "+msg)
	slog.Warn("stop cancelled: a newer operation owns the instance", "instance_id", instanceID, "log_id", stopLog.ID)
	now := time.Now().UTC()
	stopLog.Output = truncateString(output+"WARNING: "+msg+"\n", maxOutputLen)
	stopLog.CompletedAt = &now
	stopLog.Status = models.DeployLogError
	stopLog.ErrorMessage = msg
	m.logActions.Delete(stopLog.ID)
	m.writeFinal(ctx, nil, stopLog, "stop")
}

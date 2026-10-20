package deployer

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"backend/internal/models"
)

// ownershipWatch cancels the context of a running hook call when the
// operation loses the instance to another operation (a stop, a clean, a
// delete or a new deploy). Create it with watchOwnership. Call stop when the
// hook call returns.
type ownershipWatch struct {
	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.Mutex
	// status is the instance status that the watcher saw when the operation
	// lost the instance.
	status string
	lost   bool
}

// watchOwnership starts a goroutine that calls owned every interval until
// ctx ends. When owned returns false, the goroutine keeps the status and
// calls cancel. cancel must cancel ctx.
func watchOwnership(ctx context.Context, cancel context.CancelFunc, interval time.Duration, owned func() (bool, string)) *ownershipWatch {
	w := &ownershipWatch{cancel: cancel}
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if ok, current := owned(); !ok {
					w.mu.Lock()
					w.status = current
					w.lost = true
					w.mu.Unlock()
					cancel()
					return
				}
			}
		}
	}()
	return w
}

// stop cancels the context and waits for the goroutine. It returns the
// status that the watcher saw and true when the watcher cancelled the hook
// call because the operation lost the instance.
func (w *ownershipWatch) stop() (string, bool) {
	w.cancel()
	w.wg.Wait()
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.status, w.lost
}

// hookPhaseOwned reports whether the operation of logID still owns the
// instance while one of its hooks runs: the status is one of okStatuses and
// no newer deployment log exists. It also returns the current status. A read
// error of the instance counts as owned (logged), so a short database outage
// does not cancel the hook call.
func (m *Manager) hookPhaseOwned(instanceID, logID string, okStatuses ...string) (bool, string) {
	inst, err := m.instanceRepo.FindByID(instanceID)
	if err != nil {
		slog.Warn("hook watch: instance check failed, still waiting",
			"instance_id", instanceID, "log_id", logID, "error", err)
		return true, ""
	}
	statusOK := false
	for _, st := range okStatuses {
		if inst.Status == st {
			statusOK = true
			break
		}
	}
	if !statusOK {
		return false, inst.Status
	}
	if m.newerOperation(instanceID, logID) {
		return false, inst.Status
	}
	return true, inst.Status
}

// preHookResult is the result of firePreHook.
type preHookResult struct {
	// err is the error of the hook call (a denied or failed subscriber with
	// failure_policy=fail). Ignore it when lost is true.
	err error
	// status is the instance status that the watcher saw (set when lost).
	status string
	// lost is true when another operation took the instance during the hook
	// call and the watcher cancelled the call.
	lost bool
}

// firePreHook fires a pre-deploy or pre-rollback event for the operation of
// deployLog. A watcher checks the instance every stabilize poll interval
// while the subscribers run. When another operation took the instance (the
// status is not deploying, or a newer deployment log exists), the watcher
// cancels the hook call and the result has lost set. The caller then takes
// its cancelled path: it closes the log and keeps the status of the other
// operation. A server shutdown cancels the hook call as before; lost stays
// false then.
func (m *Manager) firePreHook(event string, instance *models.StackInstance, deployLog *models.DeploymentLog, opts hookOpts) preHookResult {
	if m.hooks == nil {
		return preHookResult{}
	}
	hookCtx, cancelHook := context.WithCancel(m.shutdownCtx)
	defer cancelHook()
	watch := watchOwnership(hookCtx, cancelHook, m.stabilizePollInterval, func() (bool, string) {
		return m.hookPhaseOwned(instance.ID, deployLog.ID, models.StackStatusDeploying)
	})
	err := m.fireDeployHook(hookCtx, event, instance, deployLog.ID, deployLog.StartedAt, opts)
	status, lost := watch.stop()
	if lost {
		slog.Warn("hook call cancelled: another operation changed the instance",
			"event", event, "instance_id", instance.ID, "log_id", deployLog.ID,
			"status", status, "hook_error", err)
	}
	return preHookResult{err: err, status: status, lost: lost}
}

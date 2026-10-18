package deployer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"backend/internal/hooks"
	"backend/internal/models"
)

// postDeployHookPrefix starts the deploy error of a failed blocking
// post-deploy subscriber (failure_policy=fail).
const postDeployHookPrefix = "post-deploy hook"

// blockingPostDeployResult is the result of runBlockingPostDeploy.
type blockingPostDeployResult struct {
	// output holds the progress lines and the WARNING/ERROR lines of the
	// subscribers for the deploy log output (at most maxHookProgressLen).
	output string
	// err is set when a subscriber with failure_policy=fail failed, denied
	// or timed out. It starts with postDeployHookPrefix and wraps the
	// *hooks.FailedError.
	err error
	// ignored lists the failed subscribers with failure_policy=ignore.
	ignored []hooks.IgnoredFailure
	// cancelled is set when another operation (stop, clean, delete, a new
	// deploy) changed the instance during the subscribers.
	cancelled error
}

// isPostDeployHookError reports whether err is the error of a failed
// blocking post-deploy subscriber.
func isPostDeployHookError(err error) bool {
	if err == nil || !strings.HasPrefix(err.Error(), postDeployHookPrefix) {
		return false
	}
	var failed *hooks.FailedError
	return errors.As(err, &failed)
}

// ErrDeployInterrupted ends a deploy whose blocking post-deploy hooks were
// stopped by a server shutdown. Its message is user-safe.
var ErrDeployInterrupted = errors.New("Interrupted by a server restart. Deploy again.") //nolint:staticcheck // ST1005: user-facing sentence

// blockingPhaseMargin is added to the sum of the blocking hook timeouts for
// the context and the watcher marker of the blocking phase.
const blockingPhaseMargin = time.Minute

// runBlockingPostDeploy calls the blocking post-deploy subscribers and waits
// for them. The instance keeps (or gets) the status stabilizing meanwhile,
// and PostDeployHookUntil tells the k8s status watcher (on the leader) not
// to set error. The "LOG: " lines of the subscribers go to the WebSocket log
// and into the result output, the same as for pre-deploy. Each call is
// limited by its subscription timeout; the phase has its own context (the
// sum of the timeouts plus a margin), separate from the Helm deploy budget.
//
// While the subscribers run, the instance is checked every stabilize poll
// interval. When another operation changed it (status not deploying or
// stabilizing, or a newer deployment log), the wait is cancelled and
// result.cancelled is set. A read error of the instance does not cancel.
// A server shutdown ends the deploy with ErrDeployInterrupted for every
// failure policy.
func (m *Manager) runBlockingPostDeploy(instanceID string, deployLog *models.DeploymentLog, opts hookOpts) blockingPostDeployResult {
	var res blockingPostDeployResult
	if m.hooks == nil || !m.hooks.HasBlocking(hooks.EventPostDeploy) {
		return res
	}
	// Another operation during the Helm phase or the readiness wait:
	// finalizeDeploy keeps its status and only closes the log.
	if owned, _ := m.ownsInstance(instanceID, deployLog.ID, models.StackStatusDeploying, models.StackStatusStabilizing); !owned {
		return res
	}
	inst, err := m.instanceRepo.FindByID(instanceID)
	if err != nil {
		slog.Error("blocking post-deploy: find instance failed", "instance_id", instanceID, "error", err)
		return res
	}
	phaseTimeout := m.hooks.BlockingTimeout(hooks.EventPostDeploy) + blockingPhaseMargin
	until := time.Now().UTC().Add(phaseTimeout)
	inst.Status = models.StackStatusStabilizing
	inst.PostDeployHookUntil = &until
	if err := m.instanceRepo.Update(inst); err != nil {
		// The watcher below also accepts deploying, so the wait goes on.
		slog.Warn("failed to mark the blocking post-deploy phase", "instance_id", instanceID, "error", err)
	} else {
		m.broadcastStatus(instanceID, models.StackStatusStabilizing, deployLog.ID)
	}

	progress := newHookProgress(maxHookProgressLen)
	logLine := func(line string) {
		m.broadcastLog(instanceID, deployLog.ID, line)
		progress.add(line)
	}

	// One time for the hook context and the watcher marker.
	hookCtx, cancelHooks := context.WithDeadline(m.shutdownCtx, until)
	defer cancelHooks()

	// Watch the instance; cancel the wait when another operation took it.
	var (
		watchMu       sync.Mutex
		changedStatus string
		watchWG       sync.WaitGroup
	)
	watchWG.Add(1)
	go func() {
		defer watchWG.Done()
		ticker := time.NewTicker(m.stabilizePollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-hookCtx.Done():
				return
			case <-ticker.C:
				if owned, current := m.blockingPhaseOwned(instanceID, deployLog.ID); !owned {
					watchMu.Lock()
					changedStatus = current
					watchMu.Unlock()
					cancelHooks()
					return
				}
			}
		}
	}()

	meta := make(map[string]string, len(opts.Metadata)+1)
	for k, v := range opts.Metadata {
		meta[k] = v
	}
	meta["blocking"] = "true"
	env := m.hookEnvelope(inst, deployLog.ID, deployLog.StartedAt, hookOpts{Charts: opts.Charts, Metadata: meta})

	ignored, hookErr := m.hooks.FireBlocking(hookCtx, hooks.EventPostDeploy, env, hooks.BlockingCallbacks{
		OnStart: func(hook string) {
			logLine(fmt.Sprintf("Running post-deploy step %q ...", hook))
		},
		OnProgress: logLine,
	})
	cancelHooks()
	watchWG.Wait()

	watchMu.Lock()
	current := changedStatus
	watchMu.Unlock()
	if current == "" {
		// The answer can come just after another operation started.
		if owned, st := m.blockingPhaseOwned(instanceID, deployLog.ID); !owned {
			current = st
		}
	}
	if current != "" {
		res.cancelled = fmt.Errorf("deploy cancelled: another operation changed the instance (status %s) during the post-deploy hook", current)
		logLine("WARNING: " + res.cancelled.Error())
		slog.Warn("deploy cancelled during blocking post-deploy hooks: instance changed",
			"instance_id", instanceID, "log_id", deployLog.ID, "status", current)
		res.output = progress.String()
		return res
	}

	if m.shutdownCtx.Err() != nil {
		// The subscribers did not finish; their result is unknown.
		slog.Warn("blocking post-deploy hooks interrupted by shutdown",
			"instance_id", instanceID, "log_id", deployLog.ID, "error", hookErr)
		logLine("ERROR: " + ErrDeployInterrupted.Error())
		res.err = ErrDeployInterrupted
		res.output = progress.String()
		return res
	}

	for _, f := range ignored {
		logLine("WARNING: " + f.UserMessage(hooks.EventPostDeploy, "deployment"))
	}
	res.ignored = ignored
	if hookErr != nil {
		slog.Error("blocking post-deploy hook failed the deployment",
			"instance_id", instanceID, "log_id", deployLog.ID, "error", hookErr)
		logLine("ERROR: " + hooks.UserMessage(hookErr, hooks.EventPostDeploy, "deployment"))
		res.err = fmt.Errorf("%s: %w", postDeployHookPrefix, hookErr)
	} else {
		logLine("Post-deploy steps finished")
	}
	res.output = progress.String()
	return res
}

// blockingPhaseOwned reports whether the deploy of logID still owns the
// instance during its blocking post-deploy phase: the status is stabilizing
// (or deploying, when the stabilizing update failed) and no newer deployment
// log exists. A read error of the instance counts as owned (logged), so a
// short database outage does not cancel the wait.
func (m *Manager) blockingPhaseOwned(instanceID, logID string) (bool, string) {
	inst, err := m.instanceRepo.FindByID(instanceID)
	if err != nil {
		slog.Warn("blocking post-deploy: instance check failed, still waiting",
			"instance_id", instanceID, "error", err)
		return true, ""
	}
	if inst.Status != models.StackStatusStabilizing && inst.Status != models.StackStatusDeploying {
		return false, inst.Status
	}
	if m.newerOperation(instanceID, logID) {
		return false, inst.Status
	}
	return true, inst.Status
}

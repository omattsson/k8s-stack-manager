package deployer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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

// ErrDeployInterrupted ends a deploy whose readiness wait or blocking
// post-deploy hooks a server shutdown stopped. Its message is user-safe.
var ErrDeployInterrupted = errors.New("Interrupted by a server restart. Deploy again.") //nolint:staticcheck // ST1005: user-facing sentence

// errPostDeployNotStarted ends a deploy whose blocking post-deploy phase
// could not read the stack, so the subscribers did not run. Its message is
// user-safe.
var errPostDeployNotStarted = errors.New("The post-deploy steps did not run: the stack could not be read. Deploy again.") //nolint:staticcheck // ST1005: user-facing sentence

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
// failure policy. A failed read at the start ends it with
// errPostDeployNotStarted.
func (m *Manager) runBlockingPostDeploy(instanceID string, deployLog *models.DeploymentLog, opts hookOpts) blockingPostDeployResult {
	var res blockingPostDeployResult
	if m.hooks == nil || !m.hooks.HasBlocking(hooks.EventPostDeploy) {
		return res
	}
	inst, ref, state, applies := m.blockingPhaseTarget(instanceID, deployLog.ID)
	switch state {
	case ownershipLost:
		return res
	case ownershipUnknown:
		if m.shutdownCtx.Err() != nil {
			return m.blockingNotStarted(instanceID, deployLog.ID, ErrDeployInterrupted)
		}
		return m.blockingNotStarted(instanceID, deployLog.ID, errPostDeployNotStarted)
	}
	if !applies {
		// No blocking subscriber applies to the instance (applies_to): the
		// deploy finalizes as without blocking subscribers, also during a
		// shutdown.
		return res
	}
	// The subscribers never ran: an empty result would finalize as success.
	if m.shutdownCtx.Err() != nil {
		slog.Warn("blocking post-deploy hooks skipped: server shutdown",
			"instance_id", instanceID, "log_id", deployLog.ID)
		return m.blockingNotStarted(instanceID, deployLog.ID, ErrDeployInterrupted)
	}
	until := m.markBlockingPhase(inst, ref, deployLog.ID)

	progress := newHookProgress(maxHookProgressLen)
	logLine := func(line string) {
		m.broadcastLog(instanceID, deployLog.ID, line)
		progress.add(line)
	}

	// One time for the hook context and the watcher marker.
	hookCtx, cancelHooks := context.WithDeadline(m.shutdownCtx, until)
	defer cancelHooks()

	// Watch the instance. Cancel the wait when another operation took it.
	watch := watchOwnership(hookCtx, cancelHooks, m.stabilizePollInterval, func(ctx context.Context) (bool, string) {
		return m.blockingPhaseOwned(ctx, instanceID, deployLog.ID)
	})

	meta := make(map[string]string, len(opts.Metadata)+1)
	for k, v := range opts.Metadata {
		meta[k] = v
	}
	meta["blocking"] = "true"
	env := m.hookEnvelope(inst, deployLog.ID, deployLog.StartedAt, hookOpts{Charts: opts.Charts, Metadata: meta})
	// The resolved ref of startBlockingPhase: FireBlocking calls the
	// subscribers that the marker counted.
	env.InstanceRef = ref

	ignored, hookErr := m.hooks.FireBlocking(hookCtx, hooks.EventPostDeploy, env, hooks.BlockingCallbacks{
		OnStart: func(hook string) {
			logLine(fmt.Sprintf("Running post-deploy step %q ...", hook))
		},
		OnProgress: logLine,
	})
	current, lost := watch.stop()
	if !lost {
		// The answer can come just after another operation started.
		if owned, st := m.blockingPhaseOwned(m.shutdownCtx, instanceID, deployLog.ID); !owned {
			current, lost = st, true
		}
	}
	if lost {
		res.cancelled = fmt.Errorf("%w: another operation changed the instance (status %s) during the post-deploy hook", ErrDeployCancelled, current)
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

// blockingNotStarted is the result of a blocking phase that did not start:
// err fails the deploy, and its ERROR line goes into the log.
func (m *Manager) blockingNotStarted(instanceID, logID string, err error) blockingPostDeployResult {
	line := "ERROR: " + err.Error()
	m.broadcastLog(instanceID, logID, line)
	return blockingPostDeployResult{err: err, output: line + "\n"}
}

// blockingPhaseTarget reads the instance at the start of the blocking phase
// and decides which blocking subscribers apply to it. ownershipLost: another
// operation took the instance during the Helm phase or the readiness wait;
// finalizeDeploy keeps its status and only closes the log. ownershipUnknown:
// the read failed; the caller fails the deploy. A shutdown must not fail the
// read, see finalWriteCtx.
//
// With ownershipOwned, applies is false when no blocking subscriber applies
// to the instance (applies_to, #720). Else ref is the instance of the
// envelope, with the template looked up once, so the marker time and
// FireBlocking count the same subscribers.
func (m *Manager) blockingPhaseTarget(instanceID, logID string) (inst *models.StackInstance, ref *hooks.InstanceRef, state ownership, applies bool) {
	ctx, cancel := m.finalWriteCtx()
	defer cancel()
	state, inst, err := m.instanceOwnership(ctx, instanceID, logID, models.StackStatusDeploying, models.StackStatusStabilizing)
	if state == ownershipUnknown {
		slog.Error("blocking post-deploy: find instance failed", "instance_id", instanceID, "error", err)
	}
	if state != ownershipOwned {
		return nil, nil, state, false
	}
	ref = HookInstanceRef(inst)
	ref.Status = models.StackStatusStabilizing
	ref = m.hooks.ResolveInstance(ctx, ref)
	if !m.hooks.HasBlockingFor(hooks.EventPostDeploy, ref) {
		return inst, nil, ownershipOwned, false
	}
	return inst, ref, ownershipOwned, true
}

// markBlockingPhase marks the instance stabilizing with PostDeployHookUntil
// (the timeouts of the subscribers that apply to ref plus a margin) and
// returns that time.
func (m *Manager) markBlockingPhase(inst *models.StackInstance, ref *hooks.InstanceRef, logID string) time.Time {
	ctx, cancel := m.finalWriteCtx()
	defer cancel()
	until := time.Now().UTC().Add(m.hooks.BlockingTimeoutFor(hooks.EventPostDeploy, ref) + blockingPhaseMargin)
	inst.Status = models.StackStatusStabilizing
	inst.PostDeployHookUntil = &until
	if err := m.instanceRepo.Update(ctx, inst); err != nil {
		// The watcher of the phase also accepts deploying, so the wait goes on.
		slog.Warn("failed to mark the blocking post-deploy phase", "instance_id", inst.ID, "error", err)
	} else {
		m.broadcastStatus(inst.ID, models.StackStatusStabilizing, logID)
	}
	return until
}

// blockingPhaseOwned reports whether the deploy of logID still owns the
// instance during its blocking post-deploy phase: the status is stabilizing
// (or deploying, when the stabilizing update failed) and no newer deployment
// log exists. A read error of the instance counts as owned (logged), so a
// short database outage does not cancel the wait.
func (m *Manager) blockingPhaseOwned(ctx context.Context, instanceID, logID string) (bool, string) {
	return m.hookPhaseOwned(ctx, instanceID, logID, models.StackStatusStabilizing, models.StackStatusDeploying)
}

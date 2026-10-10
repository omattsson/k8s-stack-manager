package deployer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"backend/internal/database"
	helmvalues "backend/internal/helm"
	"backend/internal/hooks"
	"backend/internal/k8s"
	"backend/internal/models"
)

// rollbackTarget holds the data of a rollback to a specific deploy log.
type rollbackTarget struct {
	values   map[string]string // chart name -> merged values YAML of the target deploy
	versions map[string]string // chart name -> chart version of the target deploy
	regCfg   *models.RegistryConfig
}

// chartVersionsJSON returns a JSON object chart name -> chart version for the
// charts of a deploy. Charts without a version are left out. Returns "" when
// no chart has a version.
func chartVersionsJSON(charts []ChartDeployInfo) string {
	versions := make(map[string]string, len(charts))
	for _, c := range charts {
		if c.ChartConfig.ChartVersion != "" {
			versions[c.ChartConfig.ChartName] = c.ChartConfig.ChartVersion
		}
	}
	if len(versions) == 0 {
		return ""
	}
	data, err := json.Marshal(versions)
	if err != nil {
		return ""
	}
	return string(data)
}

// ParseChartVersions decodes DeploymentLog.ChartVersions. It returns an empty
// map for an empty or invalid value.
func ParseChartVersions(s string) map[string]string {
	out := map[string]string{}
	if s == "" {
		return out
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return map[string]string{}
	}
	return out
}

// chartReference returns the chart reference and the --repo value for helm
// install. An OCI repository URL becomes part of the chart reference.
func chartReference(cfg models.ChartConfig) (chartRef, repoURL string) {
	chartRef = cfg.ChartPath
	if chartRef == "" {
		chartRef = cfg.ChartName
	}
	repoURL = cfg.RepositoryURL
	if strings.HasPrefix(repoURL, "oci://") {
		chartRef = strings.TrimRight(repoURL, "/") + "/" + chartRef
		repoURL = ""
	}
	return chartRef, repoURL
}

// rollbackToTarget installs the values snapshot of the target deploy for each
// chart (helm upgrade --install). The chart version is the version recorded by
// the target deploy; when no version is recorded, the current chart config
// version is used. A chart that the target deploy did not include is left
// unchanged. A chart of the target deploy that is no longer in the definition
// is skipped with a warning. The first chart error stops the rollback.
//
// It returns the helm output, the values applied per chart and the error.
func (m *Manager) rollbackToTarget(ctx context.Context, helm HelmExecutor, k8sClient *k8s.Client, streaming bool, instanceID string, deployLog *models.DeploymentLog, namespace string, charts []ChartDeployInfo, target *rollbackTarget) (string, map[string]string, error) {
	var out strings.Builder
	applied := make(map[string]string, len(target.values))

	tmpDir, err := os.MkdirTemp("", "rollback-"+instanceID+"-")
	if err != nil {
		return "", nil, fmt.Errorf("creating temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if target.regCfg != nil {
		host := target.regCfg.URL
		if u, parseErr := url.Parse(target.regCfg.URL); parseErr == nil && u.Host != "" {
			host = u.Host
		}
		if loginErr := helm.RegistryLogin(ctx, host, target.regCfg.Username, target.regCfg.Password); loginErr != nil {
			slog.Warn("helm registry login failed before rollback",
				"instance_id", instanceID, "registry", target.regCfg.URL, "error", loginErr)
			fmt.Fprintf(&out, "WARNING: helm registry login failed: %s\n", loginErr.Error())
		}
	}

	timeout := helm.Timeout()
	inDefinition := make(map[string]bool, len(charts))
	for _, chart := range charts {
		name := chart.ChartConfig.ChartName
		inDefinition[name] = true

		values, ok := target.values[name]
		if !ok {
			fmt.Fprintf(&out, "=== Chart: %s === (skipped: not part of the target deploy; left unchanged)\n", name)
			continue
		}

		version := chart.ChartConfig.ChartVersion
		if v := target.versions[name]; v != "" {
			version = v
		}

		valuesFile := ""
		if values != "" {
			valuesPath := filepath.Join(tmpDir, name+"-values.yaml")
			if writeErr := os.WriteFile(valuesPath, []byte(values), 0600); writeErr != nil {
				return out.String(), applied, fmt.Errorf("writing values for chart %q: %w", name, writeErr)
			}
			valuesFile = valuesPath
		}

		// A release left in pending-* by an interrupted operation blocks every
		// upgrade; remove the stuck revision first (as deploy does).
		if k8sClient != nil {
			msg, recErr := recoverPendingRelease(ctx, helm, k8sClient.Clientset(), name, namespace)
			if recErr != nil {
				fmt.Fprintf(&out, "WARNING: %s\n", recErr.Error())
			} else if msg != "" {
				fmt.Fprintf(&out, "%s\n", msg)
				m.broadcastLog(instanceID, deployLog.ID, msg)
			}
		}

		chartRef, repoURL := chartReference(chart.ChartConfig)
		slog.Info("rolling back chart to target deploy",
			"instance_id", instanceID, "chart", name, "target_log_id", deployLog.TargetLogID, "version", version)

		chartCtx, chartCancel := context.WithTimeout(ctx, timeout+chartTimeoutMargin)
		output, installErr := helm.Install(chartCtx, InstallRequest{
			ReleaseName: name,
			ChartPath:   chartRef,
			RepoURL:     repoURL,
			Version:     version,
			ValuesFile:  valuesFile,
			Namespace:   namespace,
			SkipCRDs:    true,
		})
		chartCancel()

		fmt.Fprintf(&out, "=== Chart: %s (-> values of deploy %s, chart version %q) ===\n%s\n", name, deployLog.TargetLogID, version, output)
		if !streaming {
			m.broadcastLog(instanceID, deployLog.ID, output)
		}
		if installErr != nil {
			return out.String(), applied, fmt.Errorf("rolling back chart %q to deploy %s: %w", name, deployLog.TargetLogID, installErr)
		}
		applied[name] = values
	}

	missing := make([]string, 0)
	for name := range target.values {
		if !inDefinition[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	for _, name := range missing {
		fmt.Fprintf(&out, "WARNING: chart %s is in the target deploy but not in the stack definition; skipped\n", name)
	}

	return out.String(), applied, nil
}

// mergeRunningValues overlays applied (chart name -> values YAML) on the
// JSON map previous (the instance's LastDeployedValues) and returns the
// JSON result. Charts that the operation did not touch keep their previous
// values. Returns previous unchanged when applied is empty.
func mergeRunningValues(previous string, applied map[string]string) string {
	if len(applied) == 0 {
		return previous
	}
	merged := map[string]string{}
	if previous != "" {
		if err := json.Unmarshal([]byte(previous), &merged); err != nil {
			merged = map[string]string{}
		}
	}
	for name, values := range applied {
		merged[name] = values
	}
	data, err := json.Marshal(merged)
	if err != nil {
		return previous
	}
	return string(data)
}

// chartRefsFor builds the hook chart list (name, version, branch, image tag)
// for charts. A chart without its own branch uses defaultBranch. versions,
// when not nil, overrides the chart version per chart name.
func chartRefsFor(charts []ChartDeployInfo, defaultBranch string, versions map[string]string) []hooks.ChartRef {
	refs := make([]hooks.ChartRef, 0, len(charts))
	for _, c := range charts {
		branch := c.Branch
		if branch == "" {
			branch = defaultBranch
		}
		version := c.ChartConfig.ChartVersion
		if v := versions[c.ChartConfig.ChartName]; v != "" {
			version = v
		}
		refs = append(refs, hooks.ChartRef{
			Name:            c.ChartConfig.ChartName,
			ReleaseName:     c.ChartConfig.ChartName,
			Version:         version,
			SourceRepoURL:   c.ChartConfig.SourceRepoURL,
			BuildPipelineID: c.ChartConfig.BuildPipelineID,
			Branch:          branch,
			ImageTag:        helmvalues.SanitizeImageTag(branch),
		})
	}
	return refs
}

// rollbackHookOpts builds the pre-rollback payload: the same chart list as
// pre-deploy, plus metadata rollback_mode, target_log_id, target_branch and
// branch_source.
//
// A rollback to a target lists only the charts of the target deploy, with
// the target branch (the per-chart branch overrides of that deploy are not
// recorded) and the recorded chart versions. A rollback by one revision lists
// all charts. Their branch is previousBranch (the branch of the previous
// successful deploy or rollback, the revision that helm rollback goes to)
// when it is known, else the current branch of each chart.
func rollbackHookOpts(req RollbackRequest, regCfg *models.RegistryConfig, previousBranch string) hookOpts {
	meta := map[string]string{"rollback_mode": "previous_revision"}
	if regCfg != nil {
		meta["registry_url"] = regCfg.URL
	}
	if len(req.TargetValues) == 0 {
		if previousBranch == "" {
			meta["branch_source"] = "current"
			return hookOpts{Charts: chartRefsFor(req.Charts, req.Instance.Branch, nil), Metadata: meta}
		}
		meta["branch_source"] = "previous_deploy"
		prev := make([]ChartDeployInfo, len(req.Charts))
		for i, c := range req.Charts {
			c.Branch = previousBranch
			prev[i] = c
		}
		return hookOpts{Charts: chartRefsFor(prev, previousBranch, nil), Metadata: meta}
	}
	meta["branch_source"] = "target_deploy"
	meta["rollback_mode"] = "target"
	meta["target_log_id"] = req.TargetLogID
	branch := req.TargetBranch
	if branch == "" {
		branch = req.Instance.Branch
	}
	meta["target_branch"] = branch
	targetCharts := make([]ChartDeployInfo, 0, len(req.Charts))
	for _, c := range req.Charts {
		if _, ok := req.TargetValues[c.ChartConfig.ChartName]; ok {
			c.Branch = branch
			targetCharts = append(targetCharts, c)
		}
	}
	return hookOpts{Charts: chartRefsFor(targetCharts, branch, req.TargetChartVersions), Metadata: meta}
}

// rollbackReadiness waits for the pods after a successful rollback when
// readiness gating is configured (as deploy does). It returns a warning line
// for the output on timeout, else "".
func (m *Manager) rollbackReadiness(k8sClient *k8s.Client, instanceID, namespace, logID string, rollbackErr error) string {
	if rollbackErr != nil || m.stabilizeTimeout <= 0 || k8sClient == nil {
		return ""
	}
	if err := m.awaitReadiness(k8sClient, instanceID, namespace, logID); err != nil {
		return fmt.Sprintf("WARNING: %s\n", err.Error())
	}
	return ""
}

// rollbackJob is the work of one async rollback.
type rollbackJob struct {
	helm       HelmExecutor
	k8sClient  *k8s.Client          // nil: no pending-release recovery, no readiness gating
	instance   models.StackInstance // snapshot for the hook envelope
	deployLog  *models.DeploymentLog
	charts     []ChartDeployInfo // sorted by deploy order
	target     *rollbackTarget   // nil: one Helm revision back
	hookOpts   hookOpts          // pre-rollback payload (OnProgress is set in the goroutine)
	prevStatus string            // instance status before the rollback
	prevError  string            // instance error message before the rollback
}

// previousRevisionBranch returns the branch of the revision that a
// one-revision helm rollback goes to, from the deploy and rollback logs
// (newest first):
//   - newest log failed (a failed deploy still created a Helm revision): the
//     target is the newest successful revision, so the newest successful log;
//   - newest log succeeded: the target is the revision before it, so the
//     second newest successful log.
//
// Returns "" when it is not known (no such log, older logs without branch,
// or a lookup error).
func (m *Manager) previousRevisionBranch(ctx context.Context, instanceID string) string {
	if m.logRepo == nil {
		return ""
	}
	logs, err := m.logRepo.ListLatestByActions(ctx, instanceID,
		[]string{models.DeployActionDeploy, models.DeployActionRollback}, 20)
	if err != nil {
		slog.Warn("rollback: failed to look up the previous deploy", "instance_id", instanceID, "error", err)
		return ""
	}
	if len(logs) == 0 {
		return ""
	}
	want := 2
	if logs[0].Status != models.DeployLogSuccess {
		want = 1
	}
	successful := 0
	for _, l := range logs {
		if l.Status != models.DeployLogSuccess {
			continue
		}
		successful++
		if successful == want {
			return l.Branch
		}
	}
	return ""
}

// allLogActions are the actions of every deployment log.
var allLogActions = []string{models.DeployActionDeploy, models.DeployActionStop, models.DeployActionClean, models.DeployActionRollback}

// ownsInstance reports whether the operation of logID still owns the
// instance: its log is the newest deployment log of the instance (no other
// deploy, stop, clean or rollback started since) and the instance status is
// one of okStatuses. It also returns the current status. When the log lookup
// fails, only the status is checked.
func (m *Manager) ownsInstance(instanceID, logID string, okStatuses ...string) (bool, string) {
	inst, err := m.instanceRepo.FindByID(instanceID)
	if err != nil {
		return false, "unknown"
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

// newerOperation reports whether the instance has a deployment log that is
// newer than logID (another operation started after it).
func (m *Manager) newerOperation(instanceID, logID string) bool {
	if m.logRepo == nil {
		return false
	}
	logs, err := m.logRepo.ListLatestByActions(m.shutdownCtx, instanceID, allLogActions, 1)
	return err == nil && len(logs) > 0 && logs[0].ID != logID
}

// fallbackLogUpdate writes the final state of a deployment log alone when
// the finalize transaction failed, so the log does not stay "running". It
// returns false when the log is no longer running (see writeFinal).
func (m *Manager) fallbackLogUpdate(instanceID string, deployLog *models.DeploymentLog) bool {
	if err := m.logRepo.Update(m.shutdownCtx, deployLog); err != nil {
		if errors.Is(err, models.ErrDeployLogNotRunning) {
			warnLogNotRunning(deployLog)
			return false
		}
		slog.Error("fallback deployment log update failed",
			"instance_id", instanceID, "deploy_log_id", deployLog.ID, "error", err)
	}
	return true
}

// warnLogNotRunning logs that a finalize write was skipped because the log
// is no longer running.
func warnLogNotRunning(deployLog *models.DeploymentLog) {
	recordInterruptEvent(deployLog.Action, interruptEventLateFinalizeSkipped)
	slog.Warn("deploy log is no longer running (ended by the interrupted operation recovery); the result of this operation is not written",
		"instance_id", deployLog.StackInstanceID, "deploy_log_id", deployLog.ID, "action", deployLog.Action)
}

// writeFinal writes the final deploy log and then the instance (nil: the
// log only). The log write is conditional: when the stored log is no longer
// running (the leader ended the operation as interrupted), nothing is
// written, a warning is logged and writeFinal returns false. The caller then
// skips the rest of its finalize work (status broadcast, hooks,
// notifications): the recovery already did it. With a transaction runner
// both writes run in one transaction.
func (m *Manager) writeFinal(instance *models.StackInstance, deployLog *models.DeploymentLog, op string) bool {
	instanceID := deployLog.StackInstanceID
	if instance == nil {
		return m.fallbackLogUpdate(instanceID, deployLog)
	}
	if m.txRunner != nil {
		err := m.txRunner.RunInTx(func(repos database.TxRepos) error {
			if err := repos.DeploymentLog.Update(context.Background(), deployLog); err != nil {
				return fmt.Errorf("updating deploy log: %w", err)
			}
			if err := repos.StackInstance.Update(instance); err != nil {
				return fmt.Errorf("updating instance: %w", err)
			}
			return nil
		})
		if err == nil {
			return true
		}
		if errors.Is(err, models.ErrDeployLogNotRunning) {
			warnLogNotRunning(deployLog)
			return false
		}
		slog.Error("failed to finalize "+op+" atomically",
			"instance_id", instanceID, "error", err)
		return m.fallbackLogUpdate(instanceID, deployLog)
	}
	if !m.fallbackLogUpdate(instanceID, deployLog) {
		return false
	}
	if err := m.instanceRepo.Update(instance); err != nil {
		slog.Error("failed to update instance after "+op,
			"instance_id", instanceID, "deploy_log_id", deployLog.ID, "error", err)
	}
	return true
}

// Outcomes of rollback-completed (metadata "outcome").
const (
	rollbackOutcomeSucceeded = "succeeded"
	rollbackOutcomeFailed    = "failed"
	rollbackOutcomeRejected  = "rejected"
	rollbackOutcomeCancelled = "cancelled"
)

// outcomeOpts returns hook options with metadata outcome.
func outcomeOpts(outcome string) hookOpts {
	return hookOpts{Metadata: map[string]string{"outcome": outcome}}
}

// finalizeRollbackCancelled closes a rollback log whose operation no longer
// owns the instance (another operation started). The instance is not
// changed. rollback-completed fires with outcome=cancelled.
func (m *Manager) finalizeRollbackCancelled(instanceID string, deployLog *models.DeploymentLog, output, reason string) {
	defer m.logActions.Delete(deployLog.ID)
	now := time.Now().UTC()
	slog.Warn("rollback cancelled", "instance_id", instanceID, "log_id", deployLog.ID, "reason", reason)
	m.broadcastLog(instanceID, deployLog.ID, "WARNING: "+reason)
	deployLog.Output = truncateString(output+"WARNING: "+reason+"\n", maxOutputLen)
	deployLog.CompletedAt = &now
	deployLog.Status = models.DeployLogError
	deployLog.ErrorMessage = truncateString(reason, maxLogErrorLen)
	if !m.writeFinal(nil, deployLog, "rollback") {
		return
	}
	instance, err := m.instanceRepo.FindByID(instanceID)
	if err != nil {
		return
	}
	_ = m.fireDeployHook(m.shutdownCtx, hooks.EventRollbackCompleted, instance, deployLog.ID, deployLog.StartedAt, outcomeOpts(rollbackOutcomeCancelled))
}

// finalizeRollbackRejected ends a rollback that the pre-rollback hook
// rejected. The rollback log ends with status error and a safe reason (the
// subscriber's deny message, or a generic text for a failed call; the full
// error only goes to slog). When the rollback still owns the instance, the
// instance gets its status and error message from before the rollback back.
func (m *Manager) finalizeRollbackRejected(job rollbackJob, output string, hookErr error) {
	defer m.logActions.Delete(job.deployLog.ID)
	now := time.Now().UTC()
	instanceID := job.instance.ID
	deployLog := job.deployLog
	reason := hooks.UserMessage(hookErr, hooks.EventPreRollback, "rollback")
	slog.Warn("pre-rollback hook rejected the rollback", "instance_id", instanceID, "log_id", deployLog.ID, "error", hookErr)
	m.broadcastLog(instanceID, deployLog.ID, "ERROR: "+reason)

	deployLog.Output = truncateString(output+"ERROR: "+reason+"\n", maxOutputLen)
	deployLog.CompletedAt = &now
	deployLog.Status = models.DeployLogError
	deployLog.ErrorMessage = truncateString(reason, maxLogErrorLen)

	restore, _ := m.ownsInstance(instanceID, deployLog.ID, models.StackStatusDeploying)
	instance, err := m.instanceRepo.FindByID(instanceID)
	if err != nil {
		slog.Error("failed to find instance for rejected rollback", "instance_id", instanceID, "error", err)
		m.fallbackLogUpdate(instanceID, deployLog)
		return
	}
	if restore {
		instance.Status = job.prevStatus
		instance.ErrorMessage = job.prevError
	}

	restoreTarget := instance
	if !restore {
		restoreTarget = nil
	}
	if !m.writeFinal(restoreTarget, deployLog, "rejected rollback") {
		return
	}

	if restore {
		m.broadcastStatusWithError(instanceID, instance.Status, deployLog.ID, instance.ErrorMessage)
	}
	_ = m.fireDeployHook(m.shutdownCtx, hooks.EventRollbackCompleted, instance, deployLog.ID, deployLog.StartedAt, outcomeOpts(rollbackOutcomeRejected))
	m.notifyInstance(models.NewNotificationTarget(instance), "rollback.error", "Rollback rejected",
		fmt.Sprintf("Rollback of %s was rejected: %s", instance.Name, reason))
}

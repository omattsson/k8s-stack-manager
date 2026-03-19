package deployer

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"backend/internal/k8s"
	"backend/internal/models"
	"backend/internal/websocket"

	"github.com/google/uuid"
)

// maxInstanceErrorLen is the maximum length of error messages stored on
// StackInstance records. These appear in list views, so they must be short.
const maxInstanceErrorLen = 256

// maxLogErrorLen is the maximum length of error messages stored on
// DeploymentLog records. Logs are viewed individually, so they can be longer.
const maxLogErrorLen = 1024

// maxOutputLen is the maximum length of the aggregated Helm output stored in
// DeploymentLog.Output. Azure Table Storage has a 64 KB entity size limit,
// and large Helm output could exceed it.
const maxOutputLen = 64 * 1024

// Manager orchestrates asynchronous deployments with concurrency control.
type Manager struct {
	helm           HelmExecutor
	instanceRepo   models.StackInstanceRepository
	logRepo        models.DeploymentLogRepository
	hub            websocket.BroadcastSender
	k8sClient      *k8s.Client
	semaphore      chan struct{}
	shutdownCtx    context.Context
	shutdownCancel context.CancelFunc
}

// ManagerConfig holds the dependencies for creating a Manager.
type ManagerConfig struct {
	HelmClient    HelmExecutor
	InstanceRepo  models.StackInstanceRepository
	DeployLogRepo models.DeploymentLogRepository
	Hub           websocket.BroadcastSender
	K8sClient     *k8s.Client
	MaxConcurrent int
}

// DeployRequest contains everything needed to deploy a stack instance.
type DeployRequest struct {
	Instance   *models.StackInstance
	Definition *models.StackDefinition
	Charts     []ChartDeployInfo
	Owner      string
}

// ChartDeployInfo holds chart configuration and pre-generated merged values.
type ChartDeployInfo struct {
	ChartConfig models.ChartConfig
	ValuesYAML  []byte
}

// NewManager creates a new deployment Manager with the given configuration.
func NewManager(cfg ManagerConfig) *Manager {
	maxConcurrent := cfg.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 3
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &Manager{
		helm:           cfg.HelmClient,
		instanceRepo:   cfg.InstanceRepo,
		logRepo:        cfg.DeployLogRepo,
		hub:            cfg.Hub,
		k8sClient:      cfg.K8sClient,
		semaphore:      make(chan struct{}, maxConcurrent),
		shutdownCtx:    ctx,
		shutdownCancel: cancel,
	}
}

// Shutdown cancels the context used by background deploy/stop goroutines,
// signalling them to abort at the next cancellation check point.
func (m *Manager) Shutdown() {
	m.shutdownCancel()
}

// Deploy starts an async deployment. Returns the deployment log ID immediately.
// The actual deployment runs in a background goroutine with concurrency limiting.
//
// The ctx parameter is used for early cancellation: if the request context is
// already done before work begins, Deploy returns an error immediately. The
// background goroutine uses m.shutdownCtx instead, because it outlives the
// HTTP request that triggered the deploy.
func (m *Manager) Deploy(ctx context.Context, req DeployRequest) (string, error) {
	// Short-circuit if the request context is already cancelled.
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("request cancelled: %w", err)
	}

	logID := uuid.New().String()
	now := time.Now().UTC()

	// Create deployment log entry.
	deployLog := &models.DeploymentLog{
		ID:              logID,
		StackInstanceID: req.Instance.ID,
		Action:          models.DeployActionDeploy,
		Status:          models.DeployLogRunning,
		StartedAt:       now,
	}
	if err := m.logRepo.Create(ctx, deployLog); err != nil {
		return "", fmt.Errorf("creating deployment log: %w", err)
	}

	// Update instance status to deploying.
	req.Instance.Status = models.StackStatusDeploying
	req.Instance.ErrorMessage = ""
	if err := m.instanceRepo.Update(req.Instance); err != nil {
		return "", fmt.Errorf("updating instance status: %w", err)
	}

	// Broadcast initial status.
	m.broadcastStatus(req.Instance.ID, models.StackStatusDeploying, logID)

	// Sort charts by deploy order.
	charts := make([]ChartDeployInfo, len(req.Charts))
	copy(charts, req.Charts)
	sort.Slice(charts, func(i, j int) bool {
		return charts[i].ChartConfig.DeployOrder < charts[j].ChartConfig.DeployOrder
	})

	// Launch async deployment, passing the deployLog to avoid re-fetching.
	go m.executeDeploy(req.Instance.ID, deployLog, req.Instance.Namespace, charts)

	return logID, nil
}

// executeDeploy runs the helm install for each chart sequentially within
// a concurrency-limited goroutine.
func (m *Manager) executeDeploy(instanceID string, deployLog *models.DeploymentLog, namespace string, charts []ChartDeployInfo) {
	// Acquire semaphore.
	m.semaphore <- struct{}{}
	defer func() { <-m.semaphore }()

	var allOutput string
	var deployErr error

	// Create a temp directory for values files.
	tmpDir, err := os.MkdirTemp("", "deploy-"+instanceID+"-")
	if err != nil {
		deployErr = fmt.Errorf("creating temp directory: %w", err)
		slog.Error("deployment failed", "instance_id", instanceID, "error", deployErr)
		m.finalizeDeploy(instanceID, deployLog, allOutput, deployErr)
		return
	}
	defer os.RemoveAll(tmpDir)

	// Use a bounded context derived from the shutdown context so that
	// operations are cancelled both on timeout and on server shutdown.
	ctx, cancel := context.WithTimeout(m.shutdownCtx, m.helm.Timeout())
	defer cancel()

	// Track successfully installed charts for rollback on partial failure.
	var installedCharts []ChartDeployInfo

	for _, chart := range charts {
		// Use chart name as release name. Releases are namespace-scoped, so
		// there is no collision risk across instances (each gets its own namespace).
		// This keeps names short to stay within the K8s 63-char name limit.
		releaseName := chart.ChartConfig.ChartName

		slog.Info("deploying chart",
			"instance_id", instanceID,
			"chart", chart.ChartConfig.ChartName,
			"release", releaseName,
			"namespace", namespace,
		)

		// Write values to temp file.
		valuesFile := ""
		if len(chart.ValuesYAML) > 0 {
			valuesPath := filepath.Join(tmpDir, chart.ChartConfig.ChartName+"-values.yaml")
			if err := os.WriteFile(valuesPath, chart.ValuesYAML, 0600); err != nil {
				deployErr = fmt.Errorf("writing values file for chart %q: %w", chart.ChartConfig.ChartName, err)
				break
			}
			valuesFile = valuesPath
		}

		// Determine chart reference and optional --repo URL.
		// For OCI registries (oci://...), combine repo URL and chart path into a
		// single chart reference (e.g. oci://registry/charts/node) — --repo is
		// not supported for OCI. For HTTP repos, pass --repo separately.
		chartRef := chart.ChartConfig.ChartPath
		if chartRef == "" {
			chartRef = chart.ChartConfig.ChartName
		}
		repoURL := chart.ChartConfig.RepositoryURL
		if strings.HasPrefix(repoURL, "oci://") {
			chartRef = strings.TrimRight(repoURL, "/") + "/" + chartRef
			repoURL = ""
		}

		output, err := m.helm.Install(ctx, InstallRequest{
			ReleaseName: releaseName,
			ChartPath:   chartRef,
			RepoURL:     repoURL,
			Version:     chart.ChartConfig.ChartVersion,
			ValuesFile:  valuesFile,
			Namespace:   namespace,
		})

		allOutput += fmt.Sprintf("=== Chart: %s ===\n%s\n", chart.ChartConfig.ChartName, output)
		m.broadcastLog(instanceID, deployLog.ID, output)

		if err != nil {
			deployErr = fmt.Errorf("deploying chart %q: %w", chart.ChartConfig.ChartName, err)
			allOutput += fmt.Sprintf("ERROR: %s\n", deployErr.Error())
			break
		}

		installedCharts = append(installedCharts, chart)
	}

	// Roll back successfully installed charts on partial failure.
	if deployErr != nil && len(installedCharts) > 0 {
		rollbackOutput := m.rollbackCharts(ctx, instanceID, deployLog.ID, namespace, installedCharts)
		allOutput += rollbackOutput
	}

	m.finalizeDeploy(instanceID, deployLog, allOutput, deployErr)
}

// rollbackCharts uninstalls previously-installed charts in reverse order after
// a partial deployment failure. It is best-effort: individual uninstall failures
// are logged but do not stop the remaining rollbacks. Returns the accumulated
// rollback output for inclusion in the deployment log.
func (m *Manager) rollbackCharts(ctx context.Context, instanceID, logID, namespace string, charts []ChartDeployInfo) string {
	var rollbackOutput string
	rollbackOutput += "=== Rolling back installed charts ===\n"

	// Iterate in reverse order (last installed first).
	for i := len(charts) - 1; i >= 0; i-- {
		chart := charts[i]
		releaseName := chart.ChartConfig.ChartName

		slog.Warn("rolling back chart",
			"instance_id", instanceID,
			"chart", releaseName,
			"namespace", namespace,
		)

		output, err := m.helm.Uninstall(ctx, UninstallRequest{
			ReleaseName: releaseName,
			Namespace:   namespace,
		})

		rollbackOutput += fmt.Sprintf("=== Rollback: %s ===\n%s\n", releaseName, output)
		m.broadcastLog(instanceID, logID, output)

		if err != nil {
			slog.Error("rollback failed for chart",
				"instance_id", instanceID,
				"chart", releaseName,
				"namespace", namespace,
				"error", err,
			)
			rollbackOutput += fmt.Sprintf("ROLLBACK ERROR: %s\n", err.Error())
		}
	}

	return rollbackOutput
}

// finalizeDeploy updates the instance and deployment log with the final status.
// The deployLog is passed directly from the goroutine closure to avoid a
// partition-scanning FindByID call on Azure Table Storage.
func (m *Manager) finalizeDeploy(instanceID string, deployLog *models.DeploymentLog, output string, deployErr error) {
	now := time.Now().UTC()

	instance, err := m.instanceRepo.FindByID(instanceID)
	if err != nil {
		slog.Error("failed to find instance for finalization",
			"instance_id", instanceID, "error", err)
		return
	}

	deployLog.Output = truncateString(output, maxOutputLen)
	deployLog.CompletedAt = &now

	if deployErr != nil {
		// Use a sanitized, high-level message for user-visible fields.
		// The full Helm output (which may contain sensitive data) is only
		// stored in deployLog.Output.
		sanitized := sanitizeDeployError(deployErr)
		instance.Status = models.StackStatusError
		instance.ErrorMessage = truncateString(sanitized, maxInstanceErrorLen)
		deployLog.Status = models.DeployLogError
		deployLog.ErrorMessage = truncateString(sanitized, maxLogErrorLen)

		slog.Error("deployment failed",
			"instance_id", instanceID,
			"log_id", deployLog.ID,
			"error", deployErr,
		)
	} else {
		instance.Status = models.StackStatusRunning
		instance.ErrorMessage = ""
		instance.LastDeployedAt = &now
		deployLog.Status = models.DeployLogSuccess

		slog.Info("deployment succeeded",
			"instance_id", instanceID,
			"log_id", deployLog.ID,
		)
	}

	if err := m.instanceRepo.Update(instance); err != nil {
		slog.Error("failed to update instance status after deploy",
			"instance_id", instanceID, "error", err)
	}

	if err := m.logRepo.Update(m.shutdownCtx, deployLog); err != nil {
		slog.Error("failed to update deployment log after deploy",
			"log_id", deployLog.ID, "error", err)
	}

	// Broadcast final status.
	if deployErr != nil {
		m.broadcastStatusWithError(instanceID, models.StackStatusError, deployLog.ID, instance.ErrorMessage)
	} else {
		m.broadcastStatus(instanceID, models.StackStatusRunning, deployLog.ID)
	}
}

// StopWithCharts starts an async stop/uninstall with explicit chart information.
// Returns the deployment log ID immediately. If charts is nil or empty, the
// stop finalizes immediately without running helm uninstall.
//
// The ctx parameter is used for early cancellation: if the request context is
// already done before work begins, StopWithCharts returns an error immediately.
// The background goroutine uses m.shutdownCtx instead, because it outlives the
// HTTP request that triggered the stop.
func (m *Manager) StopWithCharts(ctx context.Context, instance *models.StackInstance, charts []ChartDeployInfo) (string, error) {
	// Short-circuit if the request context is already cancelled.
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("request cancelled: %w", err)
	}

	logID := uuid.New().String()
	now := time.Now().UTC()

	deployLog := &models.DeploymentLog{
		ID:              logID,
		StackInstanceID: instance.ID,
		Action:          models.DeployActionStop,
		Status:          models.DeployLogRunning,
		StartedAt:       now,
	}
	if err := m.logRepo.Create(ctx, deployLog); err != nil {
		return "", fmt.Errorf("creating deployment log: %w", err)
	}

	instance.Status = models.StackStatusStopping
	instance.ErrorMessage = ""
	if err := m.instanceRepo.Update(instance); err != nil {
		return "", fmt.Errorf("updating instance status: %w", err)
	}

	m.broadcastStatus(instance.ID, models.StackStatusStopping, logID)

	// Sort charts in reverse deploy order for teardown.
	sortedCharts := make([]ChartDeployInfo, len(charts))
	copy(sortedCharts, charts)
	sort.Slice(sortedCharts, func(i, j int) bool {
		return sortedCharts[i].ChartConfig.DeployOrder > sortedCharts[j].ChartConfig.DeployOrder
	})

	// Pass deployLog into the goroutine to avoid a partition-scanning re-fetch.
	go m.executeStopWithCharts(instance.ID, deployLog, instance.Namespace, sortedCharts)

	return logID, nil
}

// executeStopWithCharts runs helm uninstall for each chart in reverse order.
func (m *Manager) executeStopWithCharts(instanceID string, deployLog *models.DeploymentLog, namespace string, charts []ChartDeployInfo) {
	m.semaphore <- struct{}{}
	defer func() { <-m.semaphore }()

	var allOutput string
	var stopErr error

	// Use a bounded context derived from the shutdown context so that
	// operations are cancelled both on timeout and on server shutdown.
	ctx, cancel := context.WithTimeout(m.shutdownCtx, m.helm.Timeout())
	defer cancel()

	for _, chart := range charts {
		releaseName := chart.ChartConfig.ChartName

		slog.Info("uninstalling chart",
			"instance_id", instanceID,
			"chart", chart.ChartConfig.ChartName,
			"release", releaseName,
			"namespace", namespace,
		)

		output, err := m.helm.Uninstall(ctx, UninstallRequest{
			ReleaseName: releaseName,
			Namespace:   namespace,
		})

		allOutput += fmt.Sprintf("=== Chart: %s ===\n%s\n", chart.ChartConfig.ChartName, output)
		m.broadcastLog(instanceID, deployLog.ID, output)

		if err != nil {
			stopErr = fmt.Errorf("uninstalling chart %q: %w", chart.ChartConfig.ChartName, err)
			allOutput += fmt.Sprintf("ERROR: %s\n", stopErr.Error())
			break
		}
	}

	m.finalizeStop(instanceID, deployLog, allOutput, stopErr)
}

// finalizeStop updates the instance and deployment log with the final stop status.
// The deployLog is passed directly from the goroutine closure to avoid a
// partition-scanning FindByID call on Azure Table Storage.
func (m *Manager) finalizeStop(instanceID string, deployLog *models.DeploymentLog, output string, stopErr error) {
	now := time.Now().UTC()

	instance, err := m.instanceRepo.FindByID(instanceID)
	if err != nil {
		slog.Error("failed to find instance for stop finalization",
			"instance_id", instanceID, "error", err)
		return
	}

	deployLog.Output = truncateString(output, maxOutputLen)
	deployLog.CompletedAt = &now

	if stopErr != nil {
		sanitized := sanitizeDeployError(stopErr)
		instance.Status = models.StackStatusError
		instance.ErrorMessage = truncateString(sanitized, maxInstanceErrorLen)
		deployLog.Status = models.DeployLogError
		deployLog.ErrorMessage = truncateString(sanitized, maxLogErrorLen)

		slog.Error("stop failed",
			"instance_id", instanceID,
			"log_id", deployLog.ID,
			"error", stopErr,
		)
	} else {
		instance.Status = models.StackStatusStopped
		instance.ErrorMessage = ""
		deployLog.Status = models.DeployLogSuccess

		slog.Info("stop succeeded",
			"instance_id", instanceID,
			"log_id", deployLog.ID,
		)
	}

	if err := m.instanceRepo.Update(instance); err != nil {
		slog.Error("failed to update instance status after stop",
			"instance_id", instanceID, "error", err)
	}

	if err := m.logRepo.Update(m.shutdownCtx, deployLog); err != nil {
		slog.Error("failed to update deployment log after stop",
			"log_id", deployLog.ID, "error", err)
	}

	if stopErr != nil {
		m.broadcastStatusWithError(instanceID, models.StackStatusError, deployLog.ID, instance.ErrorMessage)
	} else {
		m.broadcastStatus(instanceID, models.StackStatusStopped, deployLog.ID)
	}
}

// sanitizeDeployError extracts a high-level, safe error message from a
// deployment or stop error. The full Helm output may contain sensitive data
// (cluster names, internal URLs, credentials in env vars) and must not be
// exposed in user-visible fields like StackInstance.ErrorMessage. The raw
// error is still available in DeploymentLog.Output for debugging.
//
// Expected error format from executeDeploy/executeStopWithCharts:
//
//	"deploying chart \"nginx\": helm command failed: exit status 1"
//	"uninstalling chart \"nginx\": helm command failed: exit status 1"
//	"creating temp directory: <os error>"
//
// The function keeps only the first wrapped layer (e.g. "deploying chart
// \"nginx\"") plus a generic suffix.
func sanitizeDeployError(err error) string {
	msg := err.Error()

	// Look for the pattern "deploying chart ..." or "uninstalling chart ..."
	// which is the outermost fmt.Errorf wrapper in executeDeploy / executeStopWithCharts.
	for _, prefix := range []string{"deploying chart ", "uninstalling chart ", "deleting namespace ", "creating temp directory"} {
		if strings.HasPrefix(msg, prefix) {
			// Find the chart name in quotes if present.
			if idx := strings.Index(msg, ":"); idx > 0 {
				return msg[:idx] + ": operation failed"
			}
			return prefix + "operation failed"
		}
	}

	// Fallback: return a generic message if the format is unexpected.
	return "deployment operation failed"
}

// Clean starts an async namespace cleanup. It uninstalls all Helm releases
// and deletes the Kubernetes namespace, returning the instance to draft status.
// Returns the deployment log ID immediately.
//
// The ctx parameter is used for early cancellation: if the request context is
// already done before work begins, Clean returns an error immediately. The
// background goroutine uses m.shutdownCtx instead, because it outlives the
// HTTP request that triggered the clean.
func (m *Manager) Clean(ctx context.Context, instance *models.StackInstance, charts []models.ChartConfig) (string, error) {
	// Short-circuit if the request context is already cancelled.
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("request cancelled: %w", err)
	}

	logID := uuid.New().String()
	now := time.Now().UTC()

	deployLog := &models.DeploymentLog{
		ID:              logID,
		StackInstanceID: instance.ID,
		Action:          models.DeployActionClean,
		Status:          models.DeployLogRunning,
		StartedAt:       now,
	}
	if err := m.logRepo.Create(ctx, deployLog); err != nil {
		return "", fmt.Errorf("creating deployment log: %w", err)
	}

	instance.Status = models.StackStatusCleaning
	instance.ErrorMessage = ""
	if err := m.instanceRepo.Update(instance); err != nil {
		return "", fmt.Errorf("updating instance status: %w", err)
	}

	m.broadcastStatus(instance.ID, models.StackStatusCleaning, logID)

	// Sort charts in reverse deploy order for teardown.
	sortedCharts := make([]ChartDeployInfo, len(charts))
	for i, ch := range charts {
		sortedCharts[i] = ChartDeployInfo{ChartConfig: ch}
	}
	sort.Slice(sortedCharts, func(i, j int) bool {
		return sortedCharts[i].ChartConfig.DeployOrder > sortedCharts[j].ChartConfig.DeployOrder
	})

	go m.executeClean(instance.ID, deployLog, instance.Namespace, sortedCharts)

	return logID, nil
}

// executeClean runs helm uninstall for each chart in reverse order, then
// deletes the Kubernetes namespace.
func (m *Manager) executeClean(instanceID string, deployLog *models.DeploymentLog, namespace string, charts []ChartDeployInfo) {
	m.semaphore <- struct{}{}
	defer func() { <-m.semaphore }()

	var allOutput string
	var cleanErr error

	// Use a bounded context derived from the shutdown context so that
	// operations are cancelled both on timeout and on server shutdown.
	ctx, cancel := context.WithTimeout(m.shutdownCtx, m.helm.Timeout())
	defer cancel()

	var uninstallErrors int
	for _, chart := range charts {
		releaseName := chart.ChartConfig.ChartName

		slog.Info("cleaning: uninstalling chart",
			"instance_id", instanceID,
			"chart", chart.ChartConfig.ChartName,
			"release", releaseName,
			"namespace", namespace,
		)

		output, err := m.helm.Uninstall(ctx, UninstallRequest{
			ReleaseName: releaseName,
			Namespace:   namespace,
		})

		allOutput += fmt.Sprintf("=== Chart: %s ===\n%s\n", chart.ChartConfig.ChartName, output)
		m.broadcastLog(instanceID, deployLog.ID, output)

		if err != nil {
			// If the release is already gone (e.g. instance was stopped),
			// treat as a successful no-op rather than an error.
			if strings.Contains(err.Error(), "not found") {
				allOutput += fmt.Sprintf("Release %q already removed, skipping\n", releaseName)
				continue
			}
			// Best-effort: log warning and continue with remaining charts,
			// matching the pattern used in rollbackCharts.
			slog.Warn("clean: uninstall failed for chart",
				"instance_id", instanceID,
				"chart", releaseName,
				"namespace", namespace,
				"error", err,
			)
			allOutput += fmt.Sprintf("ERROR: uninstalling chart %q: %s\n", chart.ChartConfig.ChartName, err.Error())
			uninstallErrors++
		}
	}

	// Always attempt namespace deletion, even if some uninstalls failed.
	// The namespace delete will clean up any remaining resources.
	if m.k8sClient != nil {
		slog.Info("cleaning: deleting namespace",
			"instance_id", instanceID,
			"namespace", namespace,
		)

		if err := m.k8sClient.DeleteNamespace(ctx, namespace); err != nil {
			cleanErr = fmt.Errorf("deleting namespace %q: %w", namespace, err)
			allOutput += fmt.Sprintf("ERROR: %s\n", cleanErr.Error())
		} else {
			allOutput += fmt.Sprintf("=== Namespace %s deleted ===\n", namespace)
		}
	}

	// If namespace deletion succeeded but some uninstalls failed, report a summary error.
	if cleanErr == nil && uninstallErrors > 0 {
		cleanErr = fmt.Errorf("uninstalling chart: %d of %d charts failed to uninstall", uninstallErrors, len(charts))
	}

	m.finalizeClean(instanceID, deployLog, allOutput, cleanErr)
}

// finalizeClean updates the instance and deployment log with the final clean status.
// On success the instance is returned to draft status with cleared error and deploy time.
func (m *Manager) finalizeClean(instanceID string, deployLog *models.DeploymentLog, output string, cleanErr error) {
	now := time.Now().UTC()

	instance, err := m.instanceRepo.FindByID(instanceID)
	if err != nil {
		slog.Error("failed to find instance for clean finalization",
			"instance_id", instanceID, "error", err)
		return
	}

	deployLog.Output = truncateString(output, maxOutputLen)
	deployLog.CompletedAt = &now

	if cleanErr != nil {
		sanitized := sanitizeDeployError(cleanErr)
		instance.Status = models.StackStatusError
		instance.ErrorMessage = truncateString(sanitized, maxInstanceErrorLen)
		deployLog.Status = models.DeployLogError
		deployLog.ErrorMessage = truncateString(sanitized, maxLogErrorLen)

		slog.Error("clean failed",
			"instance_id", instanceID,
			"log_id", deployLog.ID,
			"error", cleanErr,
		)
	} else {
		instance.Status = models.StackStatusDraft
		instance.ErrorMessage = ""
		instance.LastDeployedAt = nil
		deployLog.Status = models.DeployLogSuccess

		slog.Info("clean succeeded",
			"instance_id", instanceID,
			"log_id", deployLog.ID,
		)
	}

	if err := m.instanceRepo.Update(instance); err != nil {
		slog.Error("failed to update instance status after clean",
			"instance_id", instanceID, "error", err)
	}

	if err := m.logRepo.Update(m.shutdownCtx, deployLog); err != nil {
		slog.Error("failed to update deployment log after clean",
			"log_id", deployLog.ID, "error", err)
	}

	if cleanErr != nil {
		m.broadcastStatusWithError(instanceID, models.StackStatusError, deployLog.ID, instance.ErrorMessage)
	} else {
		m.broadcastStatus(instanceID, models.StackStatusDraft, deployLog.ID)
	}
}

// truncateString returns s truncated to maxLen characters. If truncation
// occurs, the last three characters are replaced with "..." to signal that
// the string was cut.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

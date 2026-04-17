package deployer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"backend/internal/database"
	"backend/internal/k8s"
	"backend/internal/models"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
)

// Refresh-DB phase timeouts. These are deliberately short enough to catch
// stuck deployments but long enough to tolerate image pulls and the ~2 min
// golden-db tarball extraction.
const (
	refreshDBScaleDownWait = 60 * time.Second
	refreshDBCleanupWait   = 3 * time.Minute
	refreshDBMysqlReadyWait = 5 * time.Minute
	refreshDBOverallBudget = 10 * time.Minute
)

// defaultRefreshDBScaleTargets is intentionally empty. RefreshDB scaling must
// be explicitly configured per environment via REFRESH_DB_SCALE_TARGETS (or
// ManagerConfig.RefreshDBScaleTargets), so the feature doesn't silently drag
// in any specific company's resource naming scheme.
var defaultRefreshDBScaleTargets = []string{}

// ErrRefreshDBNotConfigured is returned by Manager.RefreshDB when the caller
// has not provided the minimum configuration (scale targets + MySQL/Redis/sync
// release names) needed to run the flow safely.
var ErrRefreshDBNotConfigured = errors.New("refresh-db is not configured: set REFRESH_DB_SCALE_TARGETS, REFRESH_DB_MYSQL_RELEASE, REFRESH_DB_REDIS_RELEASE, and REFRESH_DB_SYNC_JOB_NAME")

// ErrRefreshDBInstanceNotRunning is returned by Manager.RefreshDB when the
// instance is not in the running state. This defense matches the HTTP handler
// check so direct/future callers cannot bypass it.
var ErrRefreshDBInstanceNotRunning = errors.New("refresh-db: instance is not running")

// RefreshDB wipes the MySQL PVC, flushes Redis, and restarts the app
// Deployments in a stack namespace without re-running Helm. The MySQL chart's
// init container then re-extracts the golden dataset on first boot (skips
// when /var/lib/mysql/ibdata1 exists; absent after the wipe => re-extract).
//
// Returns the deployment log ID immediately. Work continues in a background
// goroutine and broadcasts progress via WebSocket like Deploy/Clean.
//
// RBAC: the backend's existing ClusterRole grants all verbs on all resources,
// so scale/exec/jobs operations require no extra permissions.
func (m *Manager) RefreshDB(ctx context.Context, instance *models.StackInstance) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("request cancelled: %w", err)
	}
	if m.shuttingDown.Load() {
		return "", fmt.Errorf("server is shutting down")
	}
	if m.registry == nil {
		return "", fmt.Errorf("cluster registry is not configured")
	}
	// Defense in depth: the HTTP handler already enforces this, but a direct
	// caller must not be able to mutate a non-running instance (the mid-flight
	// state transitions below would otherwise corrupt clean/stop/deploy races).
	if instance.Status != models.StackStatusRunning {
		return "", ErrRefreshDBInstanceNotRunning
	}
	// Require an explicit configuration — refuse to run with empty defaults.
	if len(m.refreshDBScaleTargets) == 0 ||
		m.refreshDBMysqlRelease == "" ||
		m.refreshDBRedisRelease == "" ||
		m.refreshDBSyncJobName == "" {
		return "", ErrRefreshDBNotConfigured
	}

	clusterID, err := m.registry.ResolveClusterID(instance.ClusterID)
	if err != nil {
		return "", fmt.Errorf("resolving cluster: %w", err)
	}
	k8sClient, err := m.registry.GetK8sClient(clusterID)
	if err != nil {
		return "", fmt.Errorf("getting cluster k8s client: %w", err)
	}
	if k8sClient == nil {
		return "", fmt.Errorf("getting cluster clients: k8s client is nil")
	}

	logID := uuid.New().String()
	now := time.Now().UTC()

	deployLog := &models.DeploymentLog{
		ID:              logID,
		StackInstanceID: instance.ID,
		Action:          models.DeployActionRefreshDB,
		Status:          models.DeployLogRunning,
		StartedAt:       now,
	}
	// Reuse the deploying status so the UI treats refresh-db like a normal
	// deploy for progress indicators. Finalize returns to running/error.
	instance.Status = models.StackStatusDeploying
	instance.ErrorMessage = ""

	if m.txRunner != nil {
		if txErr := m.txRunner.RunInTx(func(repos database.TxRepos) error {
			if err := repos.DeploymentLog.Create(ctx, deployLog); err != nil {
				return fmt.Errorf("creating deployment log: %w", err)
			}
			if err := repos.StackInstance.Update(instance); err != nil {
				return fmt.Errorf("updating instance status: %w", err)
			}
			return nil
		}); txErr != nil {
			return "", txErr
		}
	} else {
		if err := m.logRepo.Create(ctx, deployLog); err != nil {
			return "", fmt.Errorf("creating deployment log: %w", err)
		}
		if err := m.instanceRepo.Update(instance); err != nil {
			return "", fmt.Errorf("updating instance status: %w", err)
		}
	}

	m.broadcastStatus(instance.ID, models.StackStatusDeploying, logID)

	m.wg.Add(1)
	go m.executeRefreshDB(k8sClient, instance.ID, deployLog, instance.Namespace)

	return logID, nil
}

// refreshDBConfig returns effective RefreshDB configuration. All required
// fields are validated up front in RefreshDB(), so callers can rely on them
// being non-empty here. Only the cleanup-Job image falls back to a generic
// default (alpine) when not configured.
func (m *Manager) refreshDBConfig() (scaleTargets []string, mysqlRelease, redisRelease, syncJobName, cleanupImage string) {
	scaleTargets = m.refreshDBScaleTargets
	mysqlRelease = m.refreshDBMysqlRelease
	redisRelease = m.refreshDBRedisRelease
	syncJobName = m.refreshDBSyncJobName
	cleanupImage = m.refreshDBCleanupImage
	if cleanupImage == "" {
		cleanupImage = "alpine:3.20"
	}
	return
}

// executeRefreshDB runs the 8-step orchestration sequence described on the
// feature spec. Each step writes a progress line to DeploymentLog.Output and
// broadcasts it via WebSocket. A step failure aborts the sequence; subsequent
// steps are skipped. The app scale-up in step 7 is attempted even on failure
// so we don't strand the stack with zero replicas.
func (m *Manager) executeRefreshDB(k8sClient *k8s.Client, instanceID string, deployLog *models.DeploymentLog, namespace string) {
	defer m.wg.Done()
	m.semaphore <- struct{}{}
	defer func() { <-m.semaphore }()

	_, _, finishSpan := startDeploySpan(context.Background(), "deployer.refresh-db", //nolint:gosec // G118: intentional — operations must outlive HTTP request; shutdown coordinated via sync.WaitGroup
		attribute.String("instance.id", instanceID),
		attribute.String("namespace", namespace),
		attribute.String("log.id", deployLog.ID),
	)

	scaleTargets, mysqlRelease, redisRelease, syncJobName, cleanupImage := m.refreshDBConfig()

	var allOutput string
	var refreshErr error
	defer func() { finishSpan(refreshErr) }()

	ctx, cancel := context.WithTimeout(m.shutdownCtx, refreshDBOverallBudget)
	defer cancel()

	appendLine := func(format string, args ...any) {
		line := fmt.Sprintf(format, args...) + "\n"
		allOutput += line
		m.broadcastLog(instanceID, deployLog.ID, line)
	}

	appendLine("=== refresh-db: starting for namespace %s ===", namespace)

	// Step 1 — scale app Deployments to 0 (best effort per target).
	appendLine("[1/8] Scaling down app deployments: %v", scaleTargets)
	for _, target := range scaleTargets {
		if err := k8sClient.ScaleDeployment(ctx, namespace, target, 0); err != nil {
			if errors.Is(err, k8s.ErrDeploymentNotFound) {
				appendLine("  - %s: not present, skipping", target)
				continue
			}
			refreshErr = fmt.Errorf("scaling down %s: %w", target, err)
			appendLine("ERROR: %s", refreshErr.Error())
			// Best-effort rollback: any targets we already scaled to 0 would
			// otherwise be stranded. Scaling a running deployment to 1 is a no-op.
			m.scaleAppsUp(ctx, k8sClient, namespace, scaleTargets, appendLine)
			m.finalizeRefreshDB(instanceID, deployLog, allOutput, refreshErr)
			return
		}
		appendLine("  - %s: scaled to 0", target)
	}

	// Step 2 — scale MySQL to 0 and wait for pods gone.
	appendLine("[2/8] Scaling MySQL deployment %q to 0", mysqlRelease)
	if err := k8sClient.ScaleDeployment(ctx, namespace, mysqlRelease, 0); err != nil {
		refreshErr = fmt.Errorf("scaling down %s: %w", mysqlRelease, err)
		appendLine("ERROR: %s", refreshErr.Error())
		// All apps are at 0 after step 1; don't leave the stack dark.
		m.scaleAppsUp(ctx, k8sClient, namespace, scaleTargets, appendLine)
		m.finalizeRefreshDB(instanceID, deployLog, allOutput, refreshErr)
		return
	}
	appendLine("  waiting up to %s for MySQL pods to terminate", refreshDBScaleDownWait)
	if err := k8sClient.WaitForDeploymentPodsGone(ctx, namespace, mysqlRelease, refreshDBScaleDownWait); err != nil {
		refreshErr = fmt.Errorf("waiting for MySQL pods to terminate: %w", err)
		appendLine("ERROR: %s", refreshErr.Error())
		// Best-effort scale up of apps so we don't leave the stack dark.
		m.scaleAppsUp(ctx, k8sClient, namespace, scaleTargets, appendLine)
		m.finalizeRefreshDB(instanceID, deployLog, allOutput, refreshErr)
		return
	}
	appendLine("  MySQL pods terminated")

	// Step 3 — run a Job that wipes the MySQL PVC contents.
	pvcName := mysqlRelease + "-data"
	jobName := mysqlRelease + "-pvc-cleanup"
	appendLine("[3/8] Wiping PVC %q via Job %q (image %s, timeout %s)", pvcName, jobName, cleanupImage, refreshDBCleanupWait)
	if err := k8sClient.RunPVCCleanupJob(ctx, k8s.PVCCleanupJobRequest{
		Namespace: namespace,
		JobName:   jobName,
		PVCName:   pvcName,
		Image:     cleanupImage,
		Timeout:   refreshDBCleanupWait,
	}); err != nil {
		refreshErr = fmt.Errorf("running PVC cleanup Job: %w", err)
		appendLine("ERROR: %s", refreshErr.Error())
		m.scaleAppsUp(ctx, k8sClient, namespace, scaleTargets, appendLine)
		m.finalizeRefreshDB(instanceID, deployLog, allOutput, refreshErr)
		return
	}
	appendLine("  PVC wipe completed")

	// Step 4 — scale MySQL back to 1 and wait for Available (init container
	// re-extracts the golden-db tarball — ~2 min for a 7 GB tarball).
	appendLine("[4/8] Scaling MySQL deployment %q to 1 and waiting up to %s for Available", mysqlRelease, refreshDBMysqlReadyWait)
	if err := k8sClient.ScaleDeployment(ctx, namespace, mysqlRelease, 1); err != nil {
		refreshErr = fmt.Errorf("scaling up %s: %w", mysqlRelease, err)
		appendLine("ERROR: %s", refreshErr.Error())
		m.scaleAppsUp(ctx, k8sClient, namespace, scaleTargets, appendLine)
		m.finalizeRefreshDB(instanceID, deployLog, allOutput, refreshErr)
		return
	}
	if err := k8sClient.WaitForDeploymentAvailable(ctx, namespace, mysqlRelease, refreshDBMysqlReadyWait); err != nil {
		refreshErr = fmt.Errorf("waiting for MySQL Available: %w", err)
		appendLine("ERROR: %s", refreshErr.Error())
		m.scaleAppsUp(ctx, k8sClient, namespace, scaleTargets, appendLine)
		m.finalizeRefreshDB(instanceID, deployLog, allOutput, refreshErr)
		return
	}
	appendLine("  MySQL is Available")

	// Step 5 — Redis FLUSHALL. Non-fatal: if the pod isn't ready or exec
	// fails (e.g. fake clientset in tests), warn and continue.
	appendLine("[5/8] Flushing Redis via redis-cli FLUSHALL on deployment %q", redisRelease)
	out, execErr := k8sClient.ExecInDeploymentPod(ctx, namespace, redisRelease, "", []string{"redis-cli", "FLUSHALL"})
	switch {
	case errors.Is(execErr, k8s.ErrRESTConfigUnavailable):
		appendLine("  WARNING: REST config unavailable (likely running in a test); skipping FLUSHALL")
	case execErr != nil:
		appendLine("  WARNING: redis-cli FLUSHALL failed: %s (output: %s)", execErr.Error(), out)
	default:
		appendLine("  redis-cli FLUSHALL: %s", out)
	}

	// Step 6 — delete the Helm sync Job so a future `stack deploy` can recreate it.
	appendLine("[6/8] Deleting sync Job %q (if present)", syncJobName)
	if err := k8sClient.DeleteJob(ctx, namespace, syncJobName); err != nil {
		appendLine("  WARNING: failed to delete sync Job: %s", err.Error())
	} else {
		appendLine("  sync Job removed (if it existed). Re-run `stack deploy` to re-populate Redis via the sync Job.")
	}

	// Step 7 — scale app Deployments back up to 1.
	appendLine("[7/8] Scaling app deployments back to 1: %v", scaleTargets)
	m.scaleAppsUp(ctx, k8sClient, namespace, scaleTargets, appendLine)

	// Step 8 — final log entry.
	appendLine("[8/8] refresh-db completed successfully")

	m.finalizeRefreshDB(instanceID, deployLog, allOutput, refreshErr)
}

// scaleAppsUp scales every app Deployment in targets back up to 1 replica,
// logging each action. Missing Deployments are skipped silently. Errors are
// logged but do not abort the caller; refresh-db always tries to finish the
// scale-up so stacks aren't left at zero replicas.
func (m *Manager) scaleAppsUp(ctx context.Context, k8sClient *k8s.Client, namespace string, targets []string, appendLine func(format string, args ...any)) {
	for _, target := range targets {
		if err := k8sClient.ScaleDeployment(ctx, namespace, target, 1); err != nil {
			if errors.Is(err, k8s.ErrDeploymentNotFound) {
				appendLine("  - %s: not present, skipping", target)
				continue
			}
			appendLine("  WARNING: scaling up %s failed: %s", target, err.Error())
			continue
		}
		appendLine("  - %s: scaled to 1", target)
	}
}

// finalizeRefreshDB mirrors finalizeClean: persists final log + instance state
// and broadcasts the outcome. Success returns the instance to Running.
func (m *Manager) finalizeRefreshDB(instanceID string, deployLog *models.DeploymentLog, output string, refreshErr error) {
	now := time.Now().UTC()

	instance, err := m.instanceRepo.FindByID(instanceID)
	if err != nil {
		slog.Error("failed to find instance for refresh-db finalization",
			"instance_id", instanceID, "error", err)
		return
	}

	deployLog.Output = truncateString(output, maxOutputLen)
	deployLog.CompletedAt = &now

	if refreshErr != nil {
		sanitized := sanitizeDeployError(refreshErr)
		instance.Status = models.StackStatusError
		instance.ErrorMessage = truncateString(sanitized, maxInstanceErrorLen)
		deployLog.Status = models.DeployLogError
		deployLog.ErrorMessage = truncateString(sanitized, maxLogErrorLen)

		slog.Error("refresh-db failed",
			"instance_id", instanceID,
			"log_id", deployLog.ID,
			"error", refreshErr,
		)
	} else {
		instance.Status = models.StackStatusRunning
		instance.ErrorMessage = ""
		deployLog.Status = models.DeployLogSuccess

		slog.Info("refresh-db succeeded",
			"instance_id", instanceID,
			"log_id", deployLog.ID,
		)
	}

	if m.txRunner != nil {
		if err := m.txRunner.RunInTx(func(repos database.TxRepos) error {
			if err := repos.StackInstance.Update(instance); err != nil {
				return fmt.Errorf("updating instance: %w", err)
			}
			if err := repos.DeploymentLog.Update(context.Background(), deployLog); err != nil {
				return fmt.Errorf("updating deploy log: %w", err)
			}
			return nil
		}); err != nil {
			slog.Error("failed to finalize refresh-db atomically",
				"instance_id", instanceID, "error", err)
		}
	} else {
		if err := m.instanceRepo.Update(instance); err != nil {
			slog.Error("failed to update instance after refresh-db",
				"instance_id", instanceID, "deploy_log_id", deployLog.ID, "error", err)
		}
		if err := m.logRepo.Update(m.shutdownCtx, deployLog); err != nil {
			slog.Error("failed to update deploy log after refresh-db",
				"instance_id", instanceID, "deploy_log_id", deployLog.ID, "error", err)
		}
	}

	if refreshErr != nil {
		m.broadcastStatusWithError(instanceID, models.StackStatusError, deployLog.ID, instance.ErrorMessage)
	} else {
		m.broadcastStatus(instanceID, models.StackStatusRunning, deployLog.ID)
	}
}

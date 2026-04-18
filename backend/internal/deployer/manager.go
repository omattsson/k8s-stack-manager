package deployer

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"backend/internal/database"
	"backend/internal/k8s"
	"backend/internal/models"
	"backend/internal/websocket"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
)

// maxInstanceErrorLen is the maximum length of error messages stored on
// StackInstance records. These appear in list views, so they must be short.
const maxInstanceErrorLen = 256

// maxLogErrorLen is the maximum length of error messages stored on
// DeploymentLog records. Logs are viewed individually, so they can be longer.
const maxLogErrorLen = 1024

// maxOutputLen is the maximum length of the aggregated Helm output stored in
// DeploymentLog.Output. Large Helm output is truncated to keep DB rows manageable.
const maxOutputLen = 64 * 1024

// ClusterResolver resolves per-cluster Helm and K8s clients.
// cluster.Registry implements this interface.
type ClusterResolver interface {
	ResolveClusterID(clusterID string) (string, error)
	GetHelmExecutor(clusterID string) (HelmExecutor, error)
	GetK8sClient(clusterID string) (*k8s.Client, error)
}

// Manager orchestrates asynchronous deployments with concurrency control.
type Manager struct {
	registry          ClusterResolver
	instanceRepo      models.StackInstanceRepository
	logRepo           models.DeploymentLogRepository
	hub               websocket.BroadcastSender
	txRunner          database.TxRunner
	quotaRepo         models.ResourceQuotaRepository
	quotaOverrideRepo models.InstanceQuotaOverrideRepository
	semaphore         chan struct{}
	shutdownCtx       context.Context
	shutdownCancel    context.CancelFunc
	wg                sync.WaitGroup
	shuttingDown      atomic.Bool
	// Wildcard TLS secret replication (local dev). When wildcardTLSSourceSecret
	// is empty, replication is disabled.
	wildcardTLSSourceNS     string
	wildcardTLSSourceSecret string
	wildcardTLSTargetSecret string

	// RefreshDB configuration — see ManagerConfig.RefreshDB* for semantics.
	refreshDBScaleTargets []string
	refreshDBMysqlRelease string
	refreshDBRedisRelease string
	refreshDBSyncJobName  string
	refreshDBCleanupImage string
}

// ManagerConfig holds the dependencies for creating a Manager.
type ManagerConfig struct {
	Registry          ClusterResolver
	InstanceRepo      models.StackInstanceRepository
	DeployLogRepo     models.DeploymentLogRepository
	Hub               websocket.BroadcastSender
	TxRunner          database.TxRunner // when set, wraps instance+log updates in a transaction; when nil, calls repos sequentially
	MaxConcurrent     int
	QuotaRepo         models.ResourceQuotaRepository         // optional: apply quotas on deploy
	QuotaOverrideRepo models.InstanceQuotaOverrideRepository // optional: per-instance quota overrides
	// Optional wildcard TLS secret replication. When WildcardTLSSourceSecret is
	// empty, the feature is disabled. When set, the secret named by it in
	// WildcardTLSSourceNamespace is copied into each stack namespace before any
	// charts install, so ingresses can reference the shared TLS secret.
	WildcardTLSSourceNamespace string
	WildcardTLSSourceSecret    string
	WildcardTLSTargetSecret    string // optional — defaults to WildcardTLSSourceSecret

	// RefreshDBScaleTargets lists app Deployment names to scale to 0 at the
	// start of a RefreshDB operation and back to 1 at the end. Entries that
	// don't exist in the target namespace are skipped silently.
	RefreshDBScaleTargets []string
	// RefreshDBMysqlRelease is the Deployment name of the MySQL chart. Its
	// PVC is assumed to be <RefreshDBMysqlRelease>-data.
	RefreshDBMysqlRelease string
	// RefreshDBRedisRelease is the Deployment name of the Redis chart. A
	// redis-cli FLUSHALL is exec'd into the first Ready pod.
	RefreshDBRedisRelease string
	// RefreshDBSyncJobName is the Helm post-install hook Job deleted during
	// RefreshDB so the next `stack deploy` recreates it.
	RefreshDBSyncJobName string
	// RefreshDBCleanupImage is the container image used by the short-lived
	// PVC cleanup Job. Defaults to alpine when empty.
	RefreshDBCleanupImage string
}

// DeployRequest contains everything needed to deploy a stack instance.
type DeployRequest struct {
	Instance           *models.StackInstance
	Definition         *models.StackDefinition
	Charts             []ChartDeployInfo
	LastDeployedValues string // JSON-serialized merged values for deploy preview
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

	wildcardTarget := cfg.WildcardTLSTargetSecret
	if wildcardTarget == "" {
		wildcardTarget = cfg.WildcardTLSSourceSecret
	}

	slog.Info("deploy manager init",
		"wildcard_tls_source_ns", cfg.WildcardTLSSourceNamespace,
		"wildcard_tls_source_secret", cfg.WildcardTLSSourceSecret,
		"wildcard_tls_target_secret", wildcardTarget,
	)

	return &Manager{
		registry:                cfg.Registry,
		instanceRepo:            cfg.InstanceRepo,
		logRepo:                 cfg.DeployLogRepo,
		hub:                     cfg.Hub,
		txRunner:                cfg.TxRunner,
		quotaRepo:               cfg.QuotaRepo,
		quotaOverrideRepo:       cfg.QuotaOverrideRepo,
		semaphore:               make(chan struct{}, maxConcurrent),
		shutdownCtx:             ctx,
		shutdownCancel:          cancel,
		wildcardTLSSourceNS:     cfg.WildcardTLSSourceNamespace,
		wildcardTLSSourceSecret: cfg.WildcardTLSSourceSecret,
		wildcardTLSTargetSecret: wildcardTarget,

		refreshDBScaleTargets: cfg.RefreshDBScaleTargets,
		refreshDBMysqlRelease: cfg.RefreshDBMysqlRelease,
		refreshDBRedisRelease: cfg.RefreshDBRedisRelease,
		refreshDBSyncJobName:  cfg.RefreshDBSyncJobName,
		refreshDBCleanupImage: cfg.RefreshDBCleanupImage,
	}
}

// Shutdown cancels the context used by background deploy/stop goroutines,
// signalling them to abort at the next cancellation check point.
func (m *Manager) Shutdown() {
	m.shuttingDown.Store(true)
	m.shutdownCancel()
	m.wg.Wait()
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

	// Reject new work if shutting down.
	if m.shuttingDown.Load() {
		return "", fmt.Errorf("server is shutting down")
	}

	// Resolve cluster clients before starting the async goroutine so that
	// lookup errors are returned synchronously to the caller.
	if m.registry == nil {
		return "", fmt.Errorf("cluster registry is not configured")
	}
	clusterID, err := m.registry.ResolveClusterID(req.Instance.ClusterID)
	if err != nil {
		return "", fmt.Errorf("resolving cluster: %w", err)
	}
	helmExec, err := m.registry.GetHelmExecutor(clusterID)
	if err != nil {
		return "", fmt.Errorf("getting cluster clients: %w", err)
	}
	if helmExec == nil {
		return "", fmt.Errorf("getting cluster clients: helm executor is nil")
	}
	// Resolve the k8s client up front too, only when we'll actually need it
	// for wildcard TLS replication. Avoids the executeDeploy goroutine
	// re-fetching the instance + re-resolving the cluster on every deploy.
	var k8sClient *k8s.Client
	if m.wildcardTLSSourceSecret != "" && m.wildcardTLSSourceNS != "" {
		k8sClient, err = m.registry.GetK8sClient(clusterID)
		if err != nil {
			return "", fmt.Errorf("getting cluster k8s client: %w", err)
		}
	}

	logID := uuid.New().String()
	now := time.Now().UTC()

	// Create deployment log entry and update instance status atomically.
	deployLog := &models.DeploymentLog{
		ID:              logID,
		StackInstanceID: req.Instance.ID,
		Action:          models.DeployActionDeploy,
		Status:          models.DeployLogRunning,
		StartedAt:       now,
	}
	req.Instance.Status = models.StackStatusDeploying
	req.Instance.ErrorMessage = ""

	if m.txRunner != nil {
		if err := m.txRunner.RunInTx(func(repos database.TxRepos) error {
			if err := repos.DeploymentLog.Create(ctx, deployLog); err != nil {
				return fmt.Errorf("creating deployment log: %w", err)
			}
			if err := repos.StackInstance.Update(req.Instance); err != nil {
				return fmt.Errorf("updating instance status: %w", err)
			}
			return nil
		}); err != nil {
			return "", err
		}
	} else {
		if err := m.logRepo.Create(ctx, deployLog); err != nil {
			return "", fmt.Errorf("creating deployment log: %w", err)
		}
		if err := m.instanceRepo.Update(req.Instance); err != nil {
			return "", fmt.Errorf("updating instance status: %w", err)
		}
	}

	// Broadcast initial status.
	m.broadcastStatus(req.Instance.ID, models.StackStatusDeploying, logID)

	// Sort charts by deploy order.
	charts := make([]ChartDeployInfo, len(req.Charts))
	copy(charts, req.Charts)
	sort.Slice(charts, func(i, j int) bool {
		return charts[i].ChartConfig.DeployOrder < charts[j].ChartConfig.DeployOrder
	})

	// Launch async deployment, passing pre-resolved clients/log to avoid
	// re-fetching the instance and re-resolving the cluster in the goroutine.
	m.wg.Add(1)
	go m.executeDeploy(helmExec, k8sClient, req.Instance.ID, deployLog, req.Instance.Namespace, charts, req.LastDeployedValues)

	return logID, nil
}

// executeDeploy runs the helm install for each chart sequentially within
// a concurrency-limited goroutine. k8sClient is only non-nil when wildcard
// TLS replication is configured — Deploy() resolves it up front so this
// goroutine doesn't re-hit the repo/registry on every run.
func (m *Manager) executeDeploy(helm HelmExecutor, k8sClient *k8s.Client, instanceID string, deployLog *models.DeploymentLog, namespace string, charts []ChartDeployInfo, lastDeployedValues string) {
	defer m.wg.Done()
	// Acquire semaphore.
	m.semaphore <- struct{}{}
	defer func() { <-m.semaphore }()

	// Start a root span for the background deployment (the HTTP request context
	// is long gone by the time this goroutine runs).
	_, _, finishSpan := startDeploySpan(context.Background(), "deployer.deploy", //nolint:gosec // G118: intentional — Helm operations must outlive HTTP request; shutdown coordinated via sync.WaitGroup
		attribute.String("instance.id", instanceID),
		attribute.String("namespace", namespace),
		attribute.String("log.id", deployLog.ID),
	)

	var allOutput string
	var deployErr error
	defer func() { finishSpan(deployErr) }()

	// Create a temp directory for values files.
	tmpDir, err := os.MkdirTemp("", "deploy-"+instanceID+"-")
	if err != nil {
		deployErr = fmt.Errorf("creating temp directory: %w", err)
		slog.Error("deployment failed", "instance_id", instanceID, "error", deployErr)
		m.finalizeDeploy(instanceID, deployLog, allOutput, deployErr, lastDeployedValues)
		return
	}
	defer os.RemoveAll(tmpDir)

	// Use a bounded context derived from the shutdown context so that
	// operations are cancelled both on timeout and on server shutdown.
	var timeout time.Duration
	if helm != nil {
		timeout = helm.Timeout()
	} else {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(m.shutdownCtx, timeout)
	defer cancel()

	// Replicate the wildcard TLS secret into the target namespace before any
	// chart installs, so ingresses with tlsSecretName can reference it from
	// the first reconcile. Non-fatal: log and continue on error (the ingress
	// will still route plaintext; only TLS termination breaks).
	//
	// Feature requires both source namespace and source secret to be configured.
	// If only one is set, warn and skip rather than calling with an empty value
	// (which would produce a confusing "" namespace error).
	switch {
	case m.wildcardTLSSourceSecret != "" && m.wildcardTLSSourceNS == "":
		slog.Warn("wildcard TLS source secret configured without source namespace — skipping replication",
			"instance_id", instanceID,
			"source_secret", m.wildcardTLSSourceSecret,
		)
	case m.wildcardTLSSourceSecret != "":
		if wildcardErr := m.replicateWildcardTLS(ctx, k8sClient, namespace); wildcardErr != nil {
			slog.Warn("failed to replicate wildcard TLS secret",
				"instance_id", instanceID,
				"namespace", namespace,
				"error", wildcardErr,
			)
			allOutput += fmt.Sprintf("WARNING: failed to replicate wildcard TLS secret: %s\n", wildcardErr.Error())
		}
	}

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

		output, err := helm.Install(ctx, InstallRequest{
			ReleaseName: releaseName,
			ChartPath:   chartRef,
			RepoURL:     repoURL,
			Version:     chart.ChartConfig.ChartVersion,
			ValuesFile:  valuesFile,
			Namespace:   namespace,
			SkipCRDs:    true, // CRDs are cluster-scoped; skip to avoid conflicts across namespaces
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

	// Apply resource quotas to the namespace if configured.
	if deployErr == nil && m.quotaRepo != nil {
		if quotaErr := m.applyNamespaceQuotas(ctx, instanceID, namespace); quotaErr != nil {
			// Quota application failure is non-fatal — log warning but don't fail the deploy.
			slog.Warn("failed to apply resource quotas",
				"instance_id", instanceID,
				"namespace", namespace,
				"error", quotaErr,
			)
			allOutput += fmt.Sprintf("WARNING: failed to apply resource quotas: %s\n", quotaErr.Error())
		}
	}

	// Roll back successfully installed charts on partial failure.
	if deployErr != nil && len(installedCharts) > 0 {
		rollbackOutput := m.rollbackCharts(helm, ctx, instanceID, deployLog.ID, namespace, installedCharts)
		allOutput += rollbackOutput
	}

	m.finalizeDeploy(instanceID, deployLog, allOutput, deployErr, lastDeployedValues)
}

// rollbackCharts uninstalls previously-installed charts in reverse order after
// a partial deployment failure. It is best-effort: individual uninstall failures
// are logged but do not stop the remaining rollbacks. Returns the accumulated
// rollback output for inclusion in the deployment log.
func (m *Manager) rollbackCharts(helm HelmExecutor, ctx context.Context, instanceID, logID, namespace string, charts []ChartDeployInfo) string {
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

		output, err := helm.Uninstall(ctx, UninstallRequest{
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
// The deployLog is passed directly from the goroutine closure to avoid an
// extra FindByID call.
func (m *Manager) finalizeDeploy(instanceID string, deployLog *models.DeploymentLog, output string, deployErr error, lastDeployedValues string) {
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
		instance.LastDeployedValues = lastDeployedValues
		deployLog.Status = models.DeployLogSuccess

		slog.Info("deployment succeeded",
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
			slog.Error("failed to finalize deploy atomically",
				"instance_id", instanceID, "error", err)
		}
	} else {
		if err := m.instanceRepo.Update(instance); err != nil {
			slog.Error("failed to update instance after deploy",
				"instance_id", instanceID, "deploy_log_id", deployLog.ID, "error", err)
		}
		if err := m.logRepo.Update(m.shutdownCtx, deployLog); err != nil {
			slog.Error("failed to update deploy log after deploy",
				"instance_id", instanceID, "deploy_log_id", deployLog.ID, "error", err)
		}
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

	// Reject new work if shutting down.
	if m.shuttingDown.Load() {
		return "", fmt.Errorf("server is shutting down")
	}

	// Resolve cluster clients before starting the async goroutine.
	if m.registry == nil {
		return "", fmt.Errorf("cluster registry is not configured")
	}
	clusterID, err := m.registry.ResolveClusterID(instance.ClusterID)
	if err != nil {
		return "", fmt.Errorf("resolving cluster: %w", err)
	}
	helmExec, err := m.registry.GetHelmExecutor(clusterID)
	if err != nil {
		return "", fmt.Errorf("getting cluster clients: %w", err)
	}
	if helmExec == nil {
		return "", fmt.Errorf("getting cluster clients: helm executor is nil")
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
	instance.Status = models.StackStatusStopping
	instance.ErrorMessage = ""

	if m.txRunner != nil {
		if err := m.txRunner.RunInTx(func(repos database.TxRepos) error {
			if err := repos.DeploymentLog.Create(ctx, deployLog); err != nil {
				return fmt.Errorf("creating deployment log: %w", err)
			}
			if err := repos.StackInstance.Update(instance); err != nil {
				return fmt.Errorf("updating instance status: %w", err)
			}
			return nil
		}); err != nil {
			return "", err
		}
	} else {
		if err := m.logRepo.Create(ctx, deployLog); err != nil {
			return "", fmt.Errorf("creating deployment log: %w", err)
		}
		if err := m.instanceRepo.Update(instance); err != nil {
			return "", fmt.Errorf("updating instance status: %w", err)
		}
	}

	m.broadcastStatus(instance.ID, models.StackStatusStopping, logID)

	// Sort charts in reverse deploy order for teardown.
	sortedCharts := make([]ChartDeployInfo, len(charts))
	copy(sortedCharts, charts)
	sort.Slice(sortedCharts, func(i, j int) bool {
		return sortedCharts[i].ChartConfig.DeployOrder > sortedCharts[j].ChartConfig.DeployOrder
	})

	// Pass deployLog into the goroutine to avoid a partition-scanning re-fetch.
	m.wg.Add(1)
	go m.executeStopWithCharts(helmExec, instance.ID, deployLog, instance.Namespace, sortedCharts)

	return logID, nil
}

// executeStopWithCharts runs helm uninstall for each chart in reverse order.
func (m *Manager) executeStopWithCharts(helm HelmExecutor, instanceID string, deployLog *models.DeploymentLog, namespace string, charts []ChartDeployInfo) {
	defer m.wg.Done()
	m.semaphore <- struct{}{}
	defer func() { <-m.semaphore }()

	_, _, finishSpan := startDeploySpan(context.Background(), "deployer.undeploy", //nolint:gosec // G118: intentional — Helm operations must outlive HTTP request; shutdown coordinated via sync.WaitGroup
		attribute.String("instance.id", instanceID),
		attribute.String("namespace", namespace),
		attribute.String("log.id", deployLog.ID),
	)

	var allOutput string
	var stopErr error
	defer func() { finishSpan(stopErr) }()

	// Use a bounded context derived from the shutdown context so that
	// operations are cancelled both on timeout and on server shutdown.
	var timeout time.Duration
	if helm != nil {
		timeout = helm.Timeout()
	} else {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(m.shutdownCtx, timeout)
	defer cancel()

	for _, chart := range charts {
		releaseName := chart.ChartConfig.ChartName

		slog.Info("uninstalling chart",
			"instance_id", instanceID,
			"chart", chart.ChartConfig.ChartName,
			"release", releaseName,
			"namespace", namespace,
		)

		output, err := helm.Uninstall(ctx, UninstallRequest{
			ReleaseName: releaseName,
			Namespace:   namespace,
		})

		allOutput += fmt.Sprintf("=== Chart: %s ===\n%s\n", chart.ChartConfig.ChartName, output)
		m.broadcastLog(instanceID, deployLog.ID, output)

		if err != nil {
			// If the release is already gone, treat as a no-op.
			if strings.Contains(output, "not found") {
				allOutput += fmt.Sprintf("Release %q already removed, skipping\n", releaseName)
				continue
			}
			stopErr = fmt.Errorf("uninstalling chart %q: %w", chart.ChartConfig.ChartName, err)
			allOutput += fmt.Sprintf("ERROR: %s\n", stopErr.Error())
			break
		}
	}

	m.finalizeStop(instanceID, deployLog, allOutput, stopErr)
}

// finalizeStop updates the instance and deployment log with the final stop status.
// The deployLog is passed directly from the goroutine closure to avoid an
// extra FindByID call.
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
			slog.Error("failed to finalize stop atomically",
				"instance_id", instanceID, "error", err)
		}
	} else {
		if err := m.instanceRepo.Update(instance); err != nil {
			slog.Error("failed to update instance after stop",
				"instance_id", instanceID, "deploy_log_id", deployLog.ID, "error", err)
		}
		if err := m.logRepo.Update(m.shutdownCtx, deployLog); err != nil {
			slog.Error("failed to update deploy log after stop",
				"instance_id", instanceID, "deploy_log_id", deployLog.ID, "error", err)
		}
	}

	if stopErr != nil {
		m.broadcastStatusWithError(instanceID, models.StackStatusError, deployLog.ID, instance.ErrorMessage)
	} else {
		m.broadcastStatus(instanceID, models.StackStatusStopped, deployLog.ID)
	}
}

// sanitizeDeployError extracts the relevant error message from Helm/K8s
// command output for deploy, stop, and clean operations, stripping verbose output.
// The full Helm output may contain sensitive data
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
	for _, prefix := range []string{
		"deploying chart ", "uninstalling chart ", "deleting namespace ", "creating temp directory",
		"scaling down ", "scaling up ", "waiting for MySQL", "running PVC cleanup",
	} {
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

	// Reject new work if shutting down.
	if m.shuttingDown.Load() {
		return "", fmt.Errorf("server is shutting down")
	}

	// Resolve cluster clients before starting the async goroutine.
	if m.registry == nil {
		return "", fmt.Errorf("cluster registry is not configured")
	}
	clusterID, err := m.registry.ResolveClusterID(instance.ClusterID)
	if err != nil {
		return "", fmt.Errorf("resolving cluster: %w", err)
	}
	helmExec, err := m.registry.GetHelmExecutor(clusterID)
	if err != nil {
		return "", fmt.Errorf("getting cluster clients: %w", err)
	}
	if helmExec == nil {
		return "", fmt.Errorf("getting cluster clients: helm executor is nil")
	}
	k8sClient, err := m.registry.GetK8sClient(clusterID)
	if err != nil {
		return "", fmt.Errorf("getting cluster k8s client: %w", err)
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
	instance.Status = models.StackStatusCleaning
	instance.ErrorMessage = ""

	if m.txRunner != nil {
		if err := m.txRunner.RunInTx(func(repos database.TxRepos) error {
			if err := repos.DeploymentLog.Create(ctx, deployLog); err != nil {
				return fmt.Errorf("creating deployment log: %w", err)
			}
			if err := repos.StackInstance.Update(instance); err != nil {
				return fmt.Errorf("updating instance status: %w", err)
			}
			return nil
		}); err != nil {
			return "", err
		}
	} else {
		if err := m.logRepo.Create(ctx, deployLog); err != nil {
			return "", fmt.Errorf("creating deployment log: %w", err)
		}
		if err := m.instanceRepo.Update(instance); err != nil {
			return "", fmt.Errorf("updating instance status: %w", err)
		}
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

	m.wg.Add(1)
	go m.executeClean(helmExec, k8sClient, instance.ID, deployLog, instance.Namespace, sortedCharts)

	return logID, nil
}

// executeClean runs helm uninstall for each chart in reverse order, then
// deletes the Kubernetes namespace.
func (m *Manager) executeClean(helm HelmExecutor, k8sClient *k8s.Client, instanceID string, deployLog *models.DeploymentLog, namespace string, charts []ChartDeployInfo) {
	defer m.wg.Done()
	m.semaphore <- struct{}{}
	defer func() { <-m.semaphore }()

	_, _, finishSpan := startDeploySpan(context.Background(), "deployer.clean", //nolint:gosec // G118: intentional — Helm operations must outlive HTTP request; shutdown coordinated via sync.WaitGroup
		attribute.String("instance.id", instanceID),
		attribute.String("namespace", namespace),
		attribute.String("log.id", deployLog.ID),
	)

	var allOutput string
	var cleanErr error
	defer func() { finishSpan(cleanErr) }()

	// Use a bounded context derived from the shutdown context so that
	// operations are cancelled both on timeout and on server shutdown.
	var timeout time.Duration
	if helm != nil {
		timeout = helm.Timeout()
	} else {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(m.shutdownCtx, timeout)
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

		output, err := helm.Uninstall(ctx, UninstallRequest{
			ReleaseName: releaseName,
			Namespace:   namespace,
		})

		allOutput += fmt.Sprintf("=== Chart: %s ===\n%s\n", chart.ChartConfig.ChartName, output)
		m.broadcastLog(instanceID, deployLog.ID, output)

		if err != nil {
			// If the release is already gone (e.g. instance was stopped),
			// treat as a successful no-op rather than an error.
			// The "not found" message appears in the output (stderr), not
			// in the Go error which is just "helm command failed: exit status 1".
			if strings.Contains(output, "not found") {
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
	if k8sClient != nil {
		slog.Info("cleaning: deleting namespace",
			"instance_id", instanceID,
			"namespace", namespace,
		)

		if err := k8sClient.DeleteNamespace(ctx, namespace); err != nil {
			cleanErr = fmt.Errorf("deleting namespace %q: %w", namespace, err)
			allOutput += fmt.Sprintf("ERROR: %s\n", cleanErr.Error())
		} else {
			allOutput += fmt.Sprintf("=== Namespace %s deleted ===\n", namespace)
		}
	}

	// If namespace deletion succeeded but some uninstalls failed, include a
	// warning in the output but do NOT fail the operation. The namespace delete
	// already cleaned up remaining resources, so the clean is successful.
	if cleanErr == nil && uninstallErrors > 0 {
		allOutput += fmt.Sprintf("WARNING: %d of %d charts failed to uninstall (namespace deletion cleaned up remaining resources)\n", uninstallErrors, len(charts))
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
			slog.Error("failed to finalize clean atomically",
				"instance_id", instanceID, "error", err)
		}
	} else {
		if err := m.instanceRepo.Update(instance); err != nil {
			slog.Error("failed to update instance after clean",
				"instance_id", instanceID, "deploy_log_id", deployLog.ID, "error", err)
		}
		if err := m.logRepo.Update(m.shutdownCtx, deployLog); err != nil {
			slog.Error("failed to update deploy log after clean",
				"instance_id", instanceID, "deploy_log_id", deployLog.ID, "error", err)
		}
	}

	if cleanErr != nil {
		m.broadcastStatusWithError(instanceID, models.StackStatusError, deployLog.ID, instance.ErrorMessage)
	} else {
		m.broadcastStatus(instanceID, models.StackStatusDraft, deployLog.ID)
	}
}

// replicateWildcardTLS ensures the target namespace exists and copies the
// configured wildcard TLS secret into it. Deploy() pre-resolves the k8s
// client so this method avoids re-hitting the repo/registry on every deploy.
// The caller is responsible for only invoking this when wildcard TLS is
// configured (source namespace + source secret non-empty).
func (m *Manager) replicateWildcardTLS(ctx context.Context, k8sClient *k8s.Client, namespace string) error {
	if k8sClient == nil {
		return fmt.Errorf("replicateWildcardTLS: k8s client is nil")
	}

	if err := k8sClient.EnsureNamespace(ctx, namespace); err != nil {
		return fmt.Errorf("ensuring namespace: %w", err)
	}

	return k8sClient.CopySecret(ctx,
		m.wildcardTLSSourceNS, m.wildcardTLSSourceSecret,
		namespace, m.wildcardTLSTargetSecret,
	)
}

// truncateString returns s truncated to maxLen characters. If truncation
// occurs, the last three characters are replaced with "..." to signal that
// the string was cut.
// applyNamespaceQuotas fetches the quota config for the instance's cluster and
// applies ResourceQuota + LimitRange to the namespace via the K8s API.
func (m *Manager) applyNamespaceQuotas(ctx context.Context, instanceID, namespace string) error {
	// Look up the instance to get its cluster ID.
	instance, err := m.instanceRepo.FindByID(instanceID)
	if err != nil {
		return fmt.Errorf("finding instance: %w", err)
	}

	clusterID, err := m.registry.ResolveClusterID(instance.ClusterID)
	if err != nil {
		return fmt.Errorf("resolving cluster: %w", err)
	}

	// Get cluster-level defaults. If none configured, start with an empty base.
	clusterQuota, err := m.quotaRepo.GetByClusterID(ctx, clusterID)
	if err != nil {
		clusterQuota = &models.ResourceQuotaConfig{}
	}

	// Merge per-instance overrides on top of cluster defaults.
	effectiveQuota := clusterQuota
	hasOverride := false
	if m.quotaOverrideRepo != nil {
		override, overrideErr := m.quotaOverrideRepo.GetByInstanceID(ctx, instanceID)
		if overrideErr == nil && override != nil {
			effectiveQuota = mergeQuotaOverride(clusterQuota, override)
			hasOverride = true
		}
	}

	// If the effective quota is completely empty, skip.
	if effectiveQuota.CPURequest == "" && effectiveQuota.CPULimit == "" &&
		effectiveQuota.MemoryRequest == "" && effectiveQuota.MemoryLimit == "" &&
		effectiveQuota.StorageLimit == "" && effectiveQuota.PodLimit == 0 {
		return nil
	}

	k8sClient, err := m.registry.GetK8sClient(clusterID)
	if err != nil {
		return fmt.Errorf("getting k8s client: %w", err)
	}

	if err := k8sClient.EnsureResourceQuota(ctx, namespace, effectiveQuota); err != nil {
		return fmt.Errorf("ensuring resource quota: %w", err)
	}
	if err := k8sClient.EnsureLimitRange(ctx, namespace, effectiveQuota); err != nil {
		return fmt.Errorf("ensuring limit range: %w", err)
	}

	slog.Info("applied resource quotas to namespace",
		"instance_id", instanceID,
		"namespace", namespace,
		"cluster_id", clusterID,
		"has_instance_override", hasOverride,
	)
	return nil
}

// mergeQuotaOverride applies per-instance overrides on top of cluster defaults.
// Non-empty override fields replace the cluster default; empty/nil fields fall
// back to the cluster value.
func mergeQuotaOverride(cluster *models.ResourceQuotaConfig, override *models.InstanceQuotaOverride) *models.ResourceQuotaConfig {
	merged := *cluster // copy
	if override.CPURequest != "" {
		merged.CPURequest = override.CPURequest
	}
	if override.CPULimit != "" {
		merged.CPULimit = override.CPULimit
	}
	if override.MemoryRequest != "" {
		merged.MemoryRequest = override.MemoryRequest
	}
	if override.MemoryLimit != "" {
		merged.MemoryLimit = override.MemoryLimit
	}
	if override.StorageLimit != "" {
		merged.StorageLimit = override.StorageLimit
	}
	if override.PodLimit != nil {
		merged.PodLimit = *override.PodLimit
	}
	return &merged
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

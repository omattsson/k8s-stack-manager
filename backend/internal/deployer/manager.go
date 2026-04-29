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
	"sync"
	"sync/atomic"
	"time"

	"backend/internal/database"
	"backend/internal/hooks"
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
	GetRegistryConfig(clusterID string) (*models.RegistryConfig, error)
}

// LifecycleNotifier creates in-app notifications for stack lifecycle events.
type LifecycleNotifier interface {
	Notify(ctx context.Context, userID, notifType, title, message, entityType, entityID string) error
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

	// hooks dispatches lifecycle events to user-configured webhooks.
	// nil disables all hook dispatch.
	hooks *hooks.Dispatcher

	// notifier creates in-app notifications for stack lifecycle events.
	// nil disables notification creation.
	notifier LifecycleNotifier

	// pendingDeletes tracks instances that should be deleted from the database
	// after their async clean operation completes successfully.
	pendingDeletes sync.Map
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

	// Hooks dispatches lifecycle events (pre-deploy, post-deploy, deploy-finalized)
	// to configured outbound webhooks. Optional — nil disables all hook dispatch.
	// Stack-manager-specific operations like database refreshes are implemented
	// as out-of-process action subscribers (see internal/api/routes for the
	// /actions/{name} router); the core no longer ships any such operations itself.
	Hooks *hooks.Dispatcher

	// Notifier creates in-app notifications for lifecycle events.
	// Optional — nil disables notification creation from the deployer.
	Notifier LifecycleNotifier
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

		hooks:    cfg.Hooks,
		notifier: cfg.Notifier,
	}
}

// ScheduleDeleteAfterClean marks an instance for DB deletion once its async
// clean operation finishes successfully. If the clean fails, the instance
// remains in the database with an error status.
func (m *Manager) ScheduleDeleteAfterClean(instanceID string) {
	m.pendingDeletes.Store(instanceID, struct{}{})
}

// fireDeployHook dispatches event to configured hook subscribers using a
// snapshot of instance state. Returns nil immediately when no dispatcher is
// configured. The instance pointer must not be nil.
//
// Hook dispatch is synchronous: a subscriber with FailurePolicyFail can abort
// pre-* events. Post-* events should use FailurePolicyIgnore (the default) so
// a slow subscriber cannot stall the deploy goroutine indefinitely; per-call
// timeout (max 30s) bounds the worst case.
func (m *Manager) fireDeployHook(ctx context.Context, event string, instance *models.StackInstance, deploymentID string, deployStartedAt time.Time) error {
	if m.hooks == nil || instance == nil {
		return nil
	}
	env := hooks.EventEnvelope{
		InstanceRef: &hooks.InstanceRef{
			ID:                instance.ID,
			Name:              instance.Name,
			Namespace:         instance.Namespace,
			OwnerID:           instance.OwnerID,
			StackDefinitionID: instance.StackDefinitionID,
			Branch:            instance.Branch,
			ClusterID:         instance.ClusterID,
			Status:            instance.Status,
		},
	}
	if deploymentID != "" {
		startedAt := deployStartedAt
		if startedAt.IsZero() {
			startedAt = time.Now().UTC()
		}
		env.Deployment = &hooks.DeploymentRef{ID: deploymentID, StartedAt: startedAt.UTC()}
	}
	return m.hooks.Fire(ctx, event, env)
}

// wrapStreaming checks if helm implements StreamingHelmExecutor and, if so,
// returns a wrapped executor that broadcasts each output line via WebSocket.
// The returned bool indicates whether streaming is active (used to gate
// per-chart broadcastLog calls that would duplicate streaming output).
func (m *Manager) wrapStreaming(helm HelmExecutor, instanceID, logID string) (HelmExecutor, bool) {
	if streamer, ok := helm.(StreamingHelmExecutor); ok {
		slog.Info("streaming enabled for helm operations", "instance_id", instanceID, "log_id", logID)
		return streamer.WithLineHandler(func(line string) {
			m.broadcastLog(instanceID, logID, line)
		}), true
	}
	return helm, false
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
	// Resolve per-cluster registry config for automatic pull secret provisioning.
	// nil means no registry is configured for this cluster.
	regCfg, err := m.registry.GetRegistryConfig(clusterID)
	if err != nil {
		slog.Warn("failed to get registry config, skipping pull secret provisioning",
			"cluster_id", clusterID, "error", err)
	}

	// Resolve the k8s client up front when we'll need it for wildcard TLS
	// replication or image pull secret provisioning. Avoids the executeDeploy
	// goroutine re-fetching the instance + re-resolving the cluster on every deploy.
	var k8sClient *k8s.Client
	needsK8s := (m.wildcardTLSSourceSecret != "" && m.wildcardTLSSourceNS != "") || regCfg != nil
	if needsK8s {
		k8sClient, err = m.registry.GetK8sClient(clusterID)
		if err != nil {
			return "", fmt.Errorf("getting cluster k8s client: %w", err)
		}
	}

	logID := uuid.New().String()

	// Fire pre-deploy hook before any state changes. A subscriber with
	// failure_policy=fail can abort the deploy here — the instance keeps its
	// previous status and no deployment log is created.
	if err := m.fireDeployHook(ctx, hooks.EventPreDeploy, req.Instance, logID, time.Time{}); err != nil {
		return "", fmt.Errorf("pre-deploy hook: %w", err)
	}

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
	go m.executeDeploy(helmExec, k8sClient, regCfg, req.Instance.ID, deployLog, req.Instance.Namespace, charts, req.LastDeployedValues)

	return logID, nil
}

// executeDeploy runs the helm install for each chart sequentially within
// a concurrency-limited goroutine. k8sClient is only non-nil when wildcard
// TLS replication or image pull secret provisioning is needed — Deploy()
// resolves it up front so this goroutine doesn't re-hit the repo/registry
// on every run. regCfg is nil when the cluster has no container registry configured.
func (m *Manager) executeDeploy(helm HelmExecutor, k8sClient *k8s.Client, regCfg *models.RegistryConfig, instanceID string, deployLog *models.DeploymentLog, namespace string, charts []ChartDeployInfo, lastDeployedValues string) {
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

	helm, streaming := m.wrapStreaming(helm, instanceID, deployLog.ID)

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

	// Ensure the target namespace exists before any pre-install resource
	// provisioning (wildcard TLS, pull secrets). Called once here so that
	// the individual feature blocks don't each need their own EnsureNamespace.
	needsNS := (m.wildcardTLSSourceSecret != "" && m.wildcardTLSSourceNS != "") || regCfg != nil
	nsReady := false
	if needsNS && k8sClient != nil {
		if err := k8sClient.EnsureNamespace(ctx, namespace); err != nil {
			slog.Warn("failed to ensure namespace for pre-install resources",
				"instance_id", instanceID, "namespace", namespace, "error", err,
			)
		} else {
			nsReady = true
		}
	}

	// Replicate the wildcard TLS secret into the target namespace before any
	// chart installs, so ingresses with tlsSecretName can reference it from
	// the first reconcile. Non-fatal: log and continue on error (the ingress
	// will still route plaintext; only TLS termination breaks).
	switch {
	case m.wildcardTLSSourceSecret != "" && m.wildcardTLSSourceNS == "":
		slog.Warn("wildcard TLS source secret configured without source namespace — skipping replication",
			"instance_id", instanceID,
			"source_secret", m.wildcardTLSSourceSecret,
		)
	case m.wildcardTLSSourceSecret != "" && nsReady:
		if wildcardErr := k8sClient.CopySecret(ctx,
			m.wildcardTLSSourceNS, m.wildcardTLSSourceSecret,
			namespace, m.wildcardTLSTargetSecret,
		); wildcardErr != nil {
			slog.Warn("failed to replicate wildcard TLS secret",
				"instance_id", instanceID,
				"namespace", namespace,
				"error", wildcardErr,
			)
			allOutput += fmt.Sprintf("WARNING: failed to replicate wildcard TLS secret: %s\n", wildcardErr.Error())
		}
	}

	// Provision image pull secret from per-cluster registry config. Non-fatal:
	// log and continue — charts may still work if images are public or use a
	// different pull mechanism.
	if regCfg != nil && nsReady {
		if secretErr := k8sClient.EnsureDockerRegistrySecret(
			ctx, namespace, regCfg.SecretName,
			regCfg.URL, regCfg.Username, regCfg.Password,
		); secretErr != nil {
			slog.Warn("failed to provision image pull secret",
				"instance_id", instanceID,
				"namespace", namespace,
				"registry", regCfg.URL,
				"error", secretErr,
			)
			allOutput += fmt.Sprintf("WARNING: failed to provision image pull secret: %s\n", secretErr.Error())
		} else {
			allOutput += fmt.Sprintf("Image pull secret %q provisioned for registry %s\n", regCfg.SecretName, regCfg.URL)
			if saErr := k8sClient.EnsureServiceAccountPullSecret(ctx, namespace, regCfg.SecretName); saErr != nil {
				slog.Warn("failed to patch default SA with imagePullSecret",
					"instance_id", instanceID,
					"namespace", namespace,
					"error", saErr,
				)
				allOutput += fmt.Sprintf("WARNING: failed to patch default SA with imagePullSecret: %s\n", saErr.Error())
			}
		}
	}

	// Ensure helm is logged in to OCI registries before installing charts.
	if regCfg != nil {
		host := regCfg.URL
		if u, err := url.Parse(regCfg.URL); err == nil && u.Host != "" {
			host = u.Host
		}
		if loginErr := helm.RegistryLogin(ctx, host, regCfg.Username, regCfg.Password); loginErr != nil {
			slog.Warn("helm registry login failed",
				"instance_id", instanceID,
				"registry", regCfg.URL,
				"error", loginErr,
			)
			allOutput += fmt.Sprintf("WARNING: helm registry login failed: %s\n", loginErr.Error())
		} else {
			allOutput += fmt.Sprintf("Helm registry login succeeded for %s\n", host)
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
		if !streaming {
			m.broadcastLog(instanceID, deployLog.ID, output)
		}

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
		rollbackOutput := m.rollbackCharts(helm, ctx, instanceID, deployLog.ID, namespace, installedCharts, streaming)
		allOutput += rollbackOutput
	}

	m.finalizeDeploy(instanceID, deployLog, allOutput, deployErr, lastDeployedValues)
}

// rollbackCharts uninstalls previously-installed charts in reverse order after
// a partial deployment failure. It is best-effort: individual uninstall failures
// are logged but do not stop the remaining rollbacks. Returns the accumulated
// rollback output for inclusion in the deployment log.
func (m *Manager) rollbackCharts(helm HelmExecutor, ctx context.Context, instanceID, logID, namespace string, charts []ChartDeployInfo, streaming bool) string {
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
		if !streaming {
			m.broadcastLog(instanceID, logID, output)
		}

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
		deployLog.ValuesSnapshot = lastDeployedValues

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

	// Fire post-* hooks. These use the manager's shutdown context so a Shutdown
	// call cancels in-flight deliveries instead of letting them run past
	// process exit. Errors are absorbed by the dispatcher when subscribers use
	// failure_policy=ignore (the default for post-* events).
	hookCtx := m.shutdownCtx
	if deployErr == nil {
		_ = m.fireDeployHook(hookCtx, hooks.EventPostDeploy, instance, deployLog.ID, deployLog.StartedAt)
	}
	_ = m.fireDeployHook(hookCtx, hooks.EventDeployFinalized, instance, deployLog.ID, deployLog.StartedAt)

	if deployErr != nil {
		if isTimeoutError(deployErr) {
			m.notifyUser(instance.OwnerID, instanceID, "deploy.timeout",
				"Deployment timed out",
				fmt.Sprintf("Deployment of %s exceeded the timeout threshold", instance.Name))
			_ = m.fireDeployHook(hookCtx, hooks.EventDeployTimeout, instance, deployLog.ID, deployLog.StartedAt)
		} else {
			m.notifyUser(instance.OwnerID, instanceID, "deployment.error",
				"Deployment failed",
				fmt.Sprintf("Deployment of %s failed: %s", instance.Name, instance.ErrorMessage))
		}
	} else {
		m.notifyUser(instance.OwnerID, instanceID, "deployment.success", "Deployment succeeded", fmt.Sprintf("Deployment of %s completed successfully", instance.Name))
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

	helm, streaming := m.wrapStreaming(helm, instanceID, deployLog.ID)

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
		if !streaming {
			m.broadcastLog(instanceID, deployLog.ID, output)
		}

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

	_ = m.fireDeployHook(m.shutdownCtx, hooks.EventStopCompleted, instance, deployLog.ID, deployLog.StartedAt)
	if stopErr != nil {
		m.notifyUser(instance.OwnerID, instanceID, "stop.error", "Stop failed", fmt.Sprintf("Stopping %s failed: %s", instance.Name, instance.ErrorMessage))
	} else {
		m.notifyUser(instance.OwnerID, instanceID, "deployment.stopped", "Stack stopped", fmt.Sprintf("Stack %s has been stopped", instance.Name))
	}
}

// sanitizeDeployError extracts the relevant error message from Helm/K8s
// command output for deploy, stop, clean, and rollback operations, stripping verbose output.
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
func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "timed out") || strings.Contains(msg, "deadline exceeded")
}

func sanitizeDeployError(err error) string {
	msg := err.Error()

	// Look for the pattern "deploying chart ..." or "uninstalling chart ..."
	// which is the outermost fmt.Errorf wrapper in executeDeploy / executeStopWithCharts.
	for _, prefix := range []string{
		"deploying chart ", "uninstalling chart ", "deleting namespace ", "creating temp directory",
		"scaling down ", "scaling up ", "waiting for MySQL", "running PVC cleanup",
		"getting history for chart ", "rolling back chart ",
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

	helm, streaming := m.wrapStreaming(helm, instanceID, deployLog.ID)

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
		if !streaming {
			m.broadcastLog(instanceID, deployLog.ID, output)
		}

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

	_, shouldDelete := m.pendingDeletes.LoadAndDelete(instanceID)

	if cleanErr != nil {
		m.broadcastStatusWithError(instanceID, models.StackStatusError, deployLog.ID, instance.ErrorMessage)
	} else if shouldDelete {
		if m.txRunner != nil {
			if err := m.txRunner.RunInTx(func(repos database.TxRepos) error {
				if err := repos.BranchOverride.DeleteByInstance(instanceID); err != nil {
					return fmt.Errorf("deleting branch overrides: %w", err)
				}
				return repos.StackInstance.Delete(instanceID)
			}); err != nil {
				slog.Error("failed to delete instance after clean",
					"instance_id", instanceID, "error", err)
				m.broadcastStatus(instanceID, models.StackStatusDraft, deployLog.ID)
			} else {
				slog.Info("instance deleted after clean",
					"instance_id", instanceID, "log_id", deployLog.ID)
			}
		} else {
			if err := m.instanceRepo.Delete(instanceID); err != nil {
				slog.Error("failed to delete instance after clean",
					"instance_id", instanceID, "error", err)
			}
		}
	} else {
		m.broadcastStatus(instanceID, models.StackStatusDraft, deployLog.ID)
	}

	_ = m.fireDeployHook(m.shutdownCtx, hooks.EventCleanCompleted, instance, deployLog.ID, deployLog.StartedAt)

	if cleanErr != nil {
		m.notifyUser(instance.OwnerID, instanceID, "clean.error", "Cleanup failed", fmt.Sprintf("Cleanup of %s failed: %s", instance.Name, instance.ErrorMessage))
	} else if shouldDelete {
		m.notifyUser(instance.OwnerID, instanceID, "instance.deleted", "Stack deleted", fmt.Sprintf("Stack %s has been deleted", instance.Name))
		_ = m.fireDeployHook(m.shutdownCtx, hooks.EventDeleteCompleted, instance, deployLog.ID, deployLog.StartedAt)
	} else {
		m.notifyUser(instance.OwnerID, instanceID, "clean.completed", "Cleanup completed", fmt.Sprintf("Stack %s has been cleaned and returned to draft", instance.Name))
	}
}

// RollbackRequest contains everything needed to rollback a stack instance.
type RollbackRequest struct {
	Instance    *models.StackInstance
	Charts      []ChartDeployInfo
	TargetLogID string
}

// Rollback starts an async rollback of all charts in a stack instance to their
// previous Helm revision. Each chart release is rolled back by one revision.
// Returns the deployment log ID immediately.
func (m *Manager) Rollback(ctx context.Context, req RollbackRequest) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("request cancelled: %w", err)
	}
	if m.shuttingDown.Load() {
		return "", fmt.Errorf("server is shutting down")
	}
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

	logID := uuid.New().String()

	if err := m.fireDeployHook(ctx, hooks.EventPreRollback, req.Instance, logID, time.Time{}); err != nil {
		return "", fmt.Errorf("pre-rollback hook: %w", err)
	}

	now := time.Now().UTC()

	deployLog := &models.DeploymentLog{
		ID:              logID,
		StackInstanceID: req.Instance.ID,
		Action:          models.DeployActionRollback,
		Status:          models.DeployLogRunning,
		StartedAt:       now,
		TargetLogID:     req.TargetLogID,
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

	m.broadcastStatus(req.Instance.ID, models.StackStatusDeploying, logID)

	charts := make([]ChartDeployInfo, len(req.Charts))
	copy(charts, req.Charts)
	sort.Slice(charts, func(i, j int) bool {
		return charts[i].ChartConfig.DeployOrder < charts[j].ChartConfig.DeployOrder
	})

	m.wg.Add(1)
	go m.executeRollback(helmExec, req.Instance.ID, deployLog, req.Instance.Namespace, charts)

	return logID, nil
}

// executeRollback rolls back each chart release by one Helm revision.
func (m *Manager) executeRollback(helm HelmExecutor, instanceID string, deployLog *models.DeploymentLog, namespace string, charts []ChartDeployInfo) {
	defer m.wg.Done()
	m.semaphore <- struct{}{}
	defer func() { <-m.semaphore }()

	_, _, finishSpan := startDeploySpan(context.Background(), "deployer.rollback", //nolint:gosec // G118: intentional — Helm operations must outlive HTTP request
		attribute.String("instance.id", instanceID),
		attribute.String("namespace", namespace),
		attribute.String("log.id", deployLog.ID),
	)

	var allOutput string
	var rollbackErr error
	defer func() { finishSpan(rollbackErr) }()

	helm, streaming := m.wrapStreaming(helm, instanceID, deployLog.ID)

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

		slog.Info("rolling back chart",
			"instance_id", instanceID,
			"chart", releaseName,
			"namespace", namespace,
		)

		// Query Helm history to find the current revision, then roll back by one.
		revisions, err := helm.History(ctx, releaseName, namespace, 2)
		if err != nil {
			rollbackErr = fmt.Errorf("getting history for chart %q: %w", releaseName, err)
			allOutput += fmt.Sprintf("ERROR: %s\n", rollbackErr.Error())
			break
		}

		if len(revisions) < 2 {
			allOutput += fmt.Sprintf("=== Chart: %s === (skipped: only %d revision)\n", releaseName, len(revisions))
			continue
		}

		// Helm history returns revisions sorted ascending (oldest first).
		// With max=2 we get [previous, current]; index 0 is the rollback target.
		targetRevision := revisions[0].Revision

		output, err := helm.Rollback(ctx, releaseName, namespace, targetRevision)
		allOutput += fmt.Sprintf("=== Chart: %s (→ rev %d) ===\n%s\n", releaseName, targetRevision, output)
		if !streaming {
			m.broadcastLog(instanceID, deployLog.ID, output)
		}

		if err != nil {
			rollbackErr = fmt.Errorf("rolling back chart %q to revision %d: %w", releaseName, targetRevision, err)
			allOutput += fmt.Sprintf("ERROR: %s\n", rollbackErr.Error())
			break
		}
	}

	// Collect post-rollback values for snapshot.
	var valuesSnapshot string
	if rollbackErr == nil {
		valuesSnapshot = m.collectChartValues(context.Background(), helm, namespace, charts) //nolint:gosec // G118: intentional — must outlive HTTP request
	}

	m.finalizeRollback(instanceID, deployLog, allOutput, rollbackErr, valuesSnapshot)
}

// collectChartValues gathers the current Helm values for all charts and returns
// them as a JSON string for storage in the deployment log values snapshot.
func (m *Manager) collectChartValues(ctx context.Context, helm HelmExecutor, namespace string, charts []ChartDeployInfo) string {
	allValues := make(map[string]interface{})
	for _, chart := range charts {
		vals, err := helm.GetValues(ctx, chart.ChartConfig.ChartName, namespace, 0)
		if err != nil {
			continue
		}
		var parsed interface{}
		if jsonErr := json.Unmarshal([]byte(vals), &parsed); jsonErr == nil {
			allValues[chart.ChartConfig.ChartName] = parsed
		}
	}
	if len(allValues) == 0 {
		return ""
	}
	data, err := json.Marshal(allValues)
	if err != nil {
		return ""
	}
	return string(data)
}

// finalizeRollback updates the instance and deployment log with the final rollback status.
func (m *Manager) finalizeRollback(instanceID string, deployLog *models.DeploymentLog, output string, rollbackErr error, valuesSnapshot string) {
	now := time.Now().UTC()

	instance, err := m.instanceRepo.FindByID(instanceID)
	if err != nil {
		slog.Error("failed to find instance for rollback finalization",
			"instance_id", instanceID, "error", err)
		return
	}

	deployLog.Output = truncateString(output, maxOutputLen)
	deployLog.CompletedAt = &now

	if rollbackErr != nil {
		sanitized := sanitizeDeployError(rollbackErr)
		instance.Status = models.StackStatusError
		instance.ErrorMessage = truncateString(sanitized, maxInstanceErrorLen)
		deployLog.Status = models.DeployLogError
		deployLog.ErrorMessage = truncateString(sanitized, maxLogErrorLen)

		slog.Error("rollback failed",
			"instance_id", instanceID,
			"log_id", deployLog.ID,
			"error", rollbackErr,
		)
	} else {
		instance.Status = models.StackStatusRunning
		instance.ErrorMessage = ""
		instance.LastDeployedAt = &now
		deployLog.Status = models.DeployLogSuccess

		if valuesSnapshot != "" {
			instance.LastDeployedValues = valuesSnapshot
			deployLog.ValuesSnapshot = valuesSnapshot
		}

		slog.Info("rollback succeeded",
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
			slog.Error("failed to finalize rollback atomically",
				"instance_id", instanceID, "error", err)
		}
	} else {
		if err := m.instanceRepo.Update(instance); err != nil {
			slog.Error("failed to update instance after rollback",
				"instance_id", instanceID, "deploy_log_id", deployLog.ID, "error", err)
		}
		if err := m.logRepo.Update(m.shutdownCtx, deployLog); err != nil {
			slog.Error("failed to update deploy log after rollback",
				"instance_id", instanceID, "deploy_log_id", deployLog.ID, "error", err)
		}
	}

	if rollbackErr != nil {
		m.broadcastStatusWithError(instanceID, models.StackStatusError, deployLog.ID, instance.ErrorMessage)
	} else {
		m.broadcastStatus(instanceID, models.StackStatusRunning, deployLog.ID)
	}

	hookCtx := m.shutdownCtx
	_ = m.fireDeployHook(hookCtx, hooks.EventRollbackCompleted, instance, deployLog.ID, deployLog.StartedAt)
	if rollbackErr == nil {
		_ = m.fireDeployHook(hookCtx, hooks.EventPostRollback, instance, deployLog.ID, deployLog.StartedAt)
		m.notifyUser(instance.OwnerID, instanceID, "rollback.completed", "Rollback completed", fmt.Sprintf("Stack %s has been rolled back successfully", instance.Name))
	} else {
		m.notifyUser(instance.OwnerID, instanceID, "rollback.error", "Rollback failed", fmt.Sprintf("Rollback of %s failed: %s", instance.Name, instance.ErrorMessage))
	}
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

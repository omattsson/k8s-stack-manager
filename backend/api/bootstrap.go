package main

import (
	"backend/internal/api/handlers"
	"backend/internal/api/middleware"
	"backend/internal/api/routes"
	"backend/internal/auth"
	"backend/internal/cluster"
	"backend/internal/config"
	"backend/internal/database"
	"backend/internal/deployer"
	"backend/internal/gitprovider"
	"backend/internal/health"
	"backend/internal/helm"
	"backend/internal/hooks"
	"backend/internal/k8s"
	"backend/internal/leader"
	"backend/internal/models"
	"backend/internal/notifier"
	"backend/internal/notifier/channel"
	"backend/internal/podstream"
	"backend/internal/replica"
	"backend/internal/retention"
	"backend/internal/scheduler"
	"backend/internal/sessionstore"
	"backend/internal/telemetry"
	"backend/internal/ttl"
	"backend/internal/websocket"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"runtime"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// domainServices holds all domain-layer services wired during bootstrap.
// HealthPoller, SecretRefresher, K8sWatcher and CleanupScheduler are
// leader-only workers: buildLeaderWorkers adds them to the leader worker
// group, and only the leader replica runs them.
type domainServices struct {
	GitRegistry       *gitprovider.Registry
	ClusterRegistry   *cluster.Registry
	HealthPoller      *cluster.HealthPoller
	SecretRefresher   *cluster.SecretRefresher
	K8sWatcher        *k8s.Watcher
	DeployManager     *deployer.Manager
	HookDispatcher    *hooks.Dispatcher
	ClusterNames      *hooks.ClusterNameResolver // cluster_name for hooks, actions and notification channels; nil without all three
	TemplateRefs      *hooks.TemplateResolver    // template fields and applies_to for hooks and actions; nil without both
	ActionRegistry    *hooks.ActionRegistry
	LifecycleNotifier *notifier.Notifier
	CleanupExecutor   *deployer.CleanupExecutor
	CleanupScheduler  *scheduler.Scheduler
	ValuesGen         *helm.ValuesGenerator
	// ReplicaID is the process identity of this process (replica.ProcessID):
	// stored on each deploy log and written by Heartbeat.
	ReplicaID string
	// Heartbeat writes the heartbeat row of this process. Every replica
	// starts it (main), not only the leader. Nil without a heartbeat
	// repository.
	Heartbeat *replica.Heartbeat
	// InterruptRecovery is a leader-only worker: it ends the operations of
	// stopped replicas. Nil without its repositories.
	InterruptRecovery *deployer.InterruptRecovery
}

// handlerSet holds all HTTP handlers wired during bootstrap.
type handlerSet struct {
	Auth                  *handlers.AuthHandler
	OIDC                  *handlers.OIDCHandler
	Template              *handlers.TemplateHandler
	Definition            *handlers.DefinitionHandler
	TemplateVersion       *handlers.TemplateVersionHandler
	Instance              *handlers.InstanceHandler
	Git                   *handlers.GitHandler
	AuditLog              *handlers.AuditLogHandler
	User                  *handlers.UserHandler
	APIKey                *handlers.APIKeyHandler
	Admin                 *handlers.AdminHandler
	Cluster               *handlers.ClusterHandler
	BranchOverride        *handlers.BranchOverrideHandler
	InstanceQuotaOverride *handlers.InstanceQuotaOverrideHandler
	SharedValues          *handlers.SharedValuesHandler
	Notification          *handlers.NotificationHandler
	Favorite              *handlers.FavoriteHandler
	QuickDeploy           *handlers.QuickDeployHandler
	Analytics             *handlers.AnalyticsHandler
	Dashboard             *handlers.DashboardHandler
	CleanupPolicy         *handlers.CleanupPolicyHandler
	NotificationChannel   *handlers.NotificationChannelHandler
	Stream                *handlers.StreamHandler
	// Streams is the session manager of the stream handler; main shuts it
	// down right after the HTTP server.
	Streams *podstream.Manager
	// ExecStreams is the session manager of the exec sessions (kind exec).
	ExecStreams *podstream.Manager
}

// routerDeps holds non-handler dependencies required to wire the router.
type routerDeps struct {
	HealthChecker *health.HealthChecker
	Hub           *websocket.Hub
	SessionStore  sessionstore.SessionStore
	Repos         *database.RepositorySet
	Svc           *domainServices
}

// leaderWorkers holds the background workers that only the leader replica
// runs. Group runs all of them for one leadership term at a time.
type leaderWorkers struct {
	Group         *leader.Group
	Reaper        *ttl.Reaper
	ExpiryWarner  *ttl.Warner
	QuotaMonitor  *cluster.QuotaMonitor
	SecretMonitor *cluster.SecretMonitor
}

// setupOTelLogging sends the logs also to the OTel log pipeline (OTLP to the
// collector) when OTEL_ENABLED, with LOG_LEVEL for both outputs. Without an
// OTel log pipeline it changes nothing.
//
// The local output is a slog text handler on stderr, not the package default
// handler: after slog.SetDefault, the log package writes through the new
// default, and the package default handler writes through the log package
// again, which deadlocks (for example on a log.Print of a library). So with
// OTEL_ENABLED the local lines use the slog text format (time=... level=...).
func setupOTelLogging(tel *telemetry.Telemetry, logCfg config.LogConfig) {
	setupOTelLoggingTo(tel, logCfg, os.Stderr)
}

// setupOTelLoggingTo is setupOTelLogging with the local output as a
// parameter (tests).
func setupOTelLoggingTo(tel *telemetry.Telemetry, logCfg config.LogConfig, local io.Writer) {
	if tel == nil || tel.LoggerProvider == nil {
		return
	}
	level := slog.LevelInfo
	if l, err := logCfg.SlogLevel(); err == nil {
		level = l
	}
	base := slog.NewTextHandler(local, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(tel.LogHandler(base, level)))
}

// initDatabase opens the GORM database connection and returns the generic
// repository plus the underlying *gorm.DB for domain-repo construction.
func initDatabase(cfg *config.Config) (models.Repository, *gorm.DB, error) {
	repo, db, err := database.NewRepositoryWithGormDB(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize repository: %w", err)
	}
	return repo, db, nil
}

// initRepositories creates all domain-specific repositories.
func initRepositories(cfg *config.Config, db *gorm.DB) (*database.RepositorySet, error) {
	repos, err := database.NewRepositorySet(cfg, db)
	if err != nil {
		return nil, fmt.Errorf("create domain repositories: %w", err)
	}
	return repos, nil
}

// buildSessionStore creates the session store based on the configured backend.
func buildSessionStore(backend string, db *gorm.DB) sessionstore.SessionStore {
	switch backend {
	case "memory":
		return sessionstore.NewMemoryStore()
	default:
		return sessionstore.NewMySQLStore(db)
	}
}

// buildDomainServices creates all domain-layer services: git providers,
// cluster registry, health poller, deployer, hooks, etc.
func buildDomainServices(
	cfg *config.Config,
	repos *database.RepositorySet,
	hub *websocket.Hub,
	healthChecker *health.HealthChecker,
) (*domainServices, error) {
	// Git provider registry. Azure DevOps uses a PAT, or workload identity
	// when AZURE_DEVOPS_AUTH=workload-identity. A workload identity that cannot
	// be set up leaves the Azure DevOps provider off (branch listing fails with
	// a clear error) instead of stopping the server.
	var azdoTokenSource gitprovider.TokenSource
	if cfg.GitProvider.AzureDevOpsAuth == "workload-identity" {
		ts, err := gitprovider.NewWorkloadIdentityTokenSource()
		if err != nil {
			slog.Error("Azure DevOps workload identity is not available; branch listing is off", "error", err)
		} else {
			azdoTokenSource = ts
			slog.Info("Azure DevOps branch listing uses workload identity")
		}
	}
	gitRegistry, err := gitprovider.NewRegistry(gitprovider.Config{
		AzureDevOps: gitprovider.AzureDevOpsConfig{
			PAT:         cfg.GitProvider.AzureDevOpsPAT,
			DefaultOrg:  cfg.GitProvider.AzureDevOpsDefaultOrg,
			TokenSource: azdoTokenSource,
		},
		GitHub: gitprovider.GitHubConfig{
			Token:               cfg.GitProvider.GitHubToken,
			AllowedRepositories: cfg.GitProvider.GitHubAllowedRepositories,
		},
		GitLab: gitprovider.GitLabConfig{
			Token:   cfg.GitProvider.GitLabToken,
			BaseURL: cfg.GitProvider.GitLabBaseURL,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("build Git provider registry: %w", err)
	}

	valuesGen := helm.NewValuesGenerator()

	// Auto-create default cluster from KUBECONFIG_PATH for single-cluster migration.
	ensureDefaultCluster(repos.Cluster, repos.StackInstance, cfg)
	warnPlaintextRegistryPasswords(repos.Cluster)
	warnChannelSecrets(repos.NotificationChannel, cfg.Deployment.KubeconfigEncryptionKey != "")

	// Chart repository allowlist (ALLOWED_CHART_REPOS): checked when chart
	// configs are saved and again at deploy time.
	models.SetAllowedChartSources(cfg.Deployment.AllowedChartRepos)
	if len(cfg.Deployment.AllowedChartRepos) > 0 {
		slog.Info("chart repository allowlist enabled", "prefixes", cfg.Deployment.AllowedChartRepos)
	}

	// Cluster registry for multi-cluster client management.
	clusterRegistry := cluster.NewRegistry(cluster.RegistryOptions{
		ClusterRepo:     repos.Cluster,
		HelmBinary:      cfg.Deployment.HelmBinary,
		HelmTimeout:     cfg.Deployment.DeploymentTimeout,
		HelmHistoryMax:  cfg.Deployment.HelmHistoryMax,
		NamespaceLabels: cfg.Deployment.NamespaceLabels,
		AccessBinding:   stackAccessBinding(cfg.Deployment.StackAccess),
		// Local development only (#582); empty in a pod.
		InClusterFallbackKubeconfig: cfg.Deployment.InClusterFallbackKubeconfig,
		K8sClient: k8s.ClientOptions{
			QPS:   float32(cfg.Deployment.K8sClientQPS),
			Burst: cfg.Deployment.K8sClientBurst,
		},
	})

	// Extended health checks.
	healthChecker.AddCheck("cluster_registry", func(ctx context.Context) error {
		return clusterRegistry.HealthCheck(ctx)
	})
	// Optional: the git provider only serves branch lists in the UI. An Azure
	// DevOps or GitLab outage must not take the backend out of service.
	healthChecker.AddOptionalCheck("git_provider", func(ctx context.Context) error {
		return gitRegistry.HealthCheck(ctx)
	})
	healthChecker.AddCheck("helm", deployer.HelmHealthCheck(cfg.Deployment.HelmBinary))

	// Load hooks config before starting goroutines so errors don't leak them.
	hookCfg, actionSpecs, hookErr := hooks.LoadConfigFile(cfg.Deployment.HooksConfigFile)
	if hookErr != nil {
		return nil, fmt.Errorf("load hooks config: %w", hookErr)
	}

	// cluster_name in hook envelopes, action requests and the notification
	// channel payload: the batch lookup that the API uses, cached and with a
	// bounded wait.
	var clusterNames *hooks.ClusterNameResolver
	if len(hookCfg.Subscriptions) > 0 || len(actionSpecs) > 0 || repos.NotificationChannel != nil {
		clusterNames = hooks.NewClusterNameResolver(repos.Cluster.NamesByIDs)
	}

	// The template of the instance in hook envelopes and action requests,
	// and the applies_to scopes that need it (#720): one batch query,
	// cached and with a bounded wait, like cluster_name.
	var templateRefs *hooks.TemplateResolver
	if len(hookCfg.Subscriptions) > 0 || len(actionSpecs) > 0 {
		templateRefs = hooks.NewTemplateResolver(templateRefLookup(repos.StackDefinition))
	}

	var hookDispatcher *hooks.Dispatcher
	if len(hookCfg.Subscriptions) > 0 {
		hookDispatcher, hookErr = hooks.NewDispatcher(hookCfg, http.DefaultClient)
		if hookErr != nil {
			clusterNames.Stop()
			templateRefs.Stop()
			return nil, fmt.Errorf("build hooks dispatcher: %w", hookErr)
		}
		hookDispatcher.WithClusterNames(clusterNames).WithTemplates(templateRefs)
	}

	var actionRegistry *hooks.ActionRegistry
	if len(actionSpecs) > 0 {
		actionRegistry, hookErr = hooks.NewActionRegistry(actionSpecs, http.DefaultClient)
		if hookErr != nil {
			clusterNames.Stop()
			templateRefs.Stop()
			return nil, fmt.Errorf("build action registry: %w", hookErr)
		}
		actionRegistry.WithClusterNames(clusterNames).WithTemplates(templateRefs)
	}

	// Cluster health poller.
	// Leader-only worker: started by the leader worker group.
	healthPoller := cluster.NewHealthPoller(cluster.HealthPollerConfig{
		ClusterRepo: repos.Cluster,
		Registry:    clusterRegistry,
		Interval:    cfg.Deployment.ClusterHealthPollInterval,
		Hub:         hub,
	})

	// Image pull secret refresher. Leader-only worker.
	secretRefresher := cluster.NewSecretRefresher(cluster.SecretRefresherConfig{
		ClusterRepo:  repos.Cluster,
		InstanceRepo: repos.StackInstance,
		Registry:     clusterRegistry,
	})

	// K8s watcher for multi-cluster monitoring. Leader-only worker. On other
	// replicas, GetStatus has no cached status and the status handler reads
	// the cluster directly.
	k8sWatcher := k8s.NewWatcher(clusterRegistry, repos.StackInstance, hub, 30*time.Second)

	var subscribedEvents []string
	if hookDispatcher != nil {
		subscribedEvents = hookDispatcher.EventNames()
	}
	var actionNames []string
	if actionRegistry != nil {
		actionNames = actionRegistry.Names()
	}
	slog.Info("hooks configured",
		"config_file", cfg.Deployment.HooksConfigFile,
		"subscribed_events", subscribedEvents,
		"actions", actionNames,
	)

	// Lifecycle notifier — in-app notifications for stack events.
	// Followers of an instance get its in-app notifications too.
	// The channel payload gets cluster_name through the resolver and
	// instance_url from APP_PUBLIC_URL (validated in config.LoadConfig).
	lifecycleNotifier := notifier.NewNotifier(repos.Notification, hub, repos.User).
		WithFollowers(repos.InstanceFollower).
		WithClusterNames(clusterNames).
		WithPublicURL(cfg.App.PublicURL)

	// Wire external channel dispatcher for webhook-based notifications.
	if repos.NotificationChannel != nil {
		channelDispatcher := channel.NewDispatcher(repos.NotificationChannel)
		lifecycleNotifier.WithChannelDispatcher(channelDispatcher)
	}

	// Translate config.NamespaceRoleBindingSpec → deployer.NamespaceRoleBindingSpec.
	// They have the same shape but live in independent packages (deployer
	// doesn't import config) so we walk the slice rather than expose the
	// type across the boundary.
	rbs := make([]deployer.NamespaceRoleBindingSpec, 0, len(cfg.Deployment.NamespaceRoleBindings))
	for _, s := range cfg.Deployment.NamespaceRoleBindings {
		rbs = append(rbs, deployer.NamespaceRoleBindingSpec{
			ClusterRoleName:         s.ClusterRoleName,
			ServiceAccountName:      s.ServiceAccountName,
			ServiceAccountNamespace: s.ServiceAccountNamespace,
			RoleBindingName:         s.RoleBindingName,
		})
	}

	// Process identity: the replica identity plus a random suffix per
	// process. Deploy logs and the heartbeat row carry it.
	replicaID := replica.ProcessID(cfg.LeaderElection.Identity)

	// Deployment manager — multi-cluster deploys.
	deployManager := deployer.NewManager(deployer.ManagerConfig{
		Registry:                   clusterRegistry,
		InstanceRepo:               repos.StackInstance,
		DeployLogRepo:              repos.DeploymentLog,
		Hub:                        hub,
		TxRunner:                   repos.TxRunner,
		MaxConcurrent:              int(cfg.Deployment.MaxConcurrentDeploys),
		QuotaRepo:                  repos.ResourceQuota,
		QuotaOverrideRepo:          repos.InstanceQuotaOverride,
		WildcardTLSSourceNamespace: cfg.Deployment.WildcardTLSSourceNamespace,
		WildcardTLSSourceSecret:    cfg.Deployment.WildcardTLSSourceSecret,
		WildcardTLSTargetSecret:    cfg.Deployment.WildcardTLSTargetSecret,
		NamespaceRoleBindings:      rbs,
		StabilizeTimeout:           cfg.Deployment.StabilizeTimeout,
		StabilizePollInterval:      cfg.Deployment.StabilizePollInterval,
		Hooks:                      hookDispatcher,
		Notifier:                   lifecycleNotifier,
		ReplicaID:                  replicaID,
		Heartbeats:                 repos.ReplicaHeartbeat,
	})

	// Recovery of operations whose replica stopped (leader-only worker) and
	// the heartbeat of this process (every replica). Each deploy log has a
	// deadline (its time budget, set by the deploy manager); the recovery
	// waits for it.
	interruptRecovery := deployer.NewInterruptRecovery(deployer.InterruptRecoveryConfig{
		Operations: repos.InterruptedOperation,
		Heartbeats: repos.ReplicaHeartbeat,
		Instances:  repos.StackInstance,
		AuditLog:   repos.AuditLog,
		Hub:        hub,
		Notifier:   lifecycleNotifier,
		SelfID:     replicaID,
	})
	heartbeat := replica.NewHeartbeat(repos.ReplicaHeartbeat, replicaID, replica.HeartbeatInterval)

	// Cleanup executor + scheduler. The scheduler runs cron jobs only on the
	// leader replica (leader worker group).
	cleanupExecutor := deployer.NewCleanupExecutor(deployManager, repos.StackDefinition, repos.ChartConfig, repos.StackInstance)
	cleanupScheduler := scheduler.NewScheduler(repos.CleanupPolicy, repos.StackInstance, repos.AuditLog, cleanupExecutor, lifecycleNotifier)
	if hookDispatcher != nil {
		cleanupScheduler.WithHooks(hookDispatcher)
	}

	return &domainServices{
		GitRegistry:       gitRegistry,
		ClusterRegistry:   clusterRegistry,
		HealthPoller:      healthPoller,
		SecretRefresher:   secretRefresher,
		K8sWatcher:        k8sWatcher,
		DeployManager:     deployManager,
		HookDispatcher:    hookDispatcher,
		ClusterNames:      clusterNames,
		TemplateRefs:      templateRefs,
		ActionRegistry:    actionRegistry,
		LifecycleNotifier: lifecycleNotifier,
		CleanupExecutor:   cleanupExecutor,
		CleanupScheduler:  cleanupScheduler,
		ValuesGen:         valuesGen,
		ReplicaID:         replicaID,
		Heartbeat:         heartbeat,
		InterruptRecovery: interruptRecovery,
	}, nil
}

// duplicateNamesTimeout bounds the startup query for duplicate instance
// names (UNIQUE_INSTANCE_NAMES, #656).
const duplicateNamesTimeout = 30 * time.Second

// startupDefaultClusterID returns the default cluster ID for the duplicate
// name check, or "". A failed lookup (not a missing default) logs a warning.
func startupDefaultClusterID(reg *cluster.Registry) string {
	if reg == nil {
		return ""
	}
	id, err := reg.ResolveClusterID("")
	switch {
	case err == nil:
		return id
	case !errors.Is(err, cluster.ErrNoDefaultCluster):
		slog.Warn("Duplicate instance name check: default cluster lookup failed; instances without a cluster are not counted on it", "error", err)
	}
	return ""
}

// portForwarderProvider adapts the cluster registry to the provider of the
// stream handler. An empty cluster ID is the default cluster.
func portForwarderProvider(reg *cluster.Registry) handlers.PortForwarderProvider {
	return func(clusterID string) (k8s.PortForwarder, error) {
		if reg == nil {
			return nil, errors.New("cluster registry not configured")
		}
		c, err := reg.GetK8sClient(clusterID)
		if err != nil {
			return nil, err
		}
		if c == nil {
			return nil, errors.New("cluster client not available")
		}
		return c, nil
	}
}

// execClientProvider adapts the cluster registry to the exec client provider.
func execClientProvider(reg *cluster.Registry) handlers.ExecClientProvider {
	return func(clusterID string) (k8s.ExecClient, error) {
		if reg == nil {
			return nil, errors.New("cluster registry not configured")
		}
		c, err := reg.GetK8sClient(clusterID)
		if err != nil {
			return nil, err
		}
		if c == nil {
			return nil, errors.New("cluster client not available")
		}
		return c, nil
	}
}

// logExecWarnings logs the startup warnings of the exec settings.
func logExecWarnings(c config.ExecConfig) {
	if !c.Enabled {
		return
	}
	if len(c.AllowedContainers) == 0 {
		slog.Warn("STACK_EXEC_ENABLED is true but STACK_EXEC_ALLOWED_CONTAINERS is empty: every exec request is refused with container_not_allowed")
	}
	if c.AllowServiceAccountToken {
		slog.Warn("STACK_EXEC_ALLOW_SERVICE_ACCOUNT_TOKEN is true: exec also reaches containers that hold a service-account token")
	}
}

// buildHandlers creates all HTTP handlers.
func buildHandlers(
	cfg *config.Config,
	repos *database.RepositorySet,
	svc *domainServices,
	sessStore sessionstore.SessionStore,
	hub *websocket.Hub,
) (*handlerSet, error) {
	// Auth handler.
	authHandler := handlers.NewAuthHandler(repos.User, &cfg.Auth, &cfg.OIDC)
	authHandler.SetSessionStore(sessStore)
	if hub != nil {
		authHandler.SetWebSocketRevoker(hub)
	}
	if repos.RefreshToken != nil {
		authHandler.SetRefreshTokenRepo(repos.RefreshToken)
	}

	// OIDC handler — conditionally initialize when enabled.
	var oidcHandler *handlers.OIDCHandler
	if cfg.OIDC.Enabled {
		oidcProvider, oidcErr := auth.NewProvider(context.Background(), &cfg.OIDC)
		if oidcErr != nil {
			return nil, fmt.Errorf("initialize OIDC provider: %w", oidcErr)
		}
		oidcHandler = handlers.NewOIDCHandler(oidcProvider, sessStore, repos.User, &cfg.OIDC, &cfg.Auth)
		if repos.RefreshToken != nil {
			oidcHandler.SetRefreshTokenRepo(repos.RefreshToken)
		}
		slog.Info("OIDC authentication enabled", "provider_url", cfg.OIDC.ProviderURL)
	}

	// Template handler.
	templateHandler, err := handlers.NewTemplateHandlerWithVersions(
		repos.StackTemplate, repos.TemplateChartConfig, repos.StackDefinition,
		repos.ChartConfig, repos.TemplateVersion, repos.TxRunner,
	)
	if err != nil {
		return nil, fmt.Errorf("create template handler: %w", err)
	}

	// Definition handler.
	definitionHandler, err := handlers.NewDefinitionHandlerWithVersions(
		repos.StackDefinition, repos.ChartConfig, repos.StackInstance,
		repos.StackTemplate, repos.TemplateChartConfig, repos.TemplateVersion, repos.TxRunner,
	)
	if err != nil {
		return nil, fmt.Errorf("create definition handler: %w", err)
	}
	definitionHandler.WithUserRepo(repos.User)

	// Template version handler.
	templateVersionHandler := handlers.NewTemplateVersionHandler(repos.TemplateVersion, repos.StackTemplate).
		WithTemplateCharts(repos.TemplateChartConfig).
		WithUserRepo(repos.User)

	// UNIQUE_INSTANCE_NAMES: an instance name is unique on its cluster.
	if cfg.App.UniqueInstanceNames {
		lookup, ok := repos.StackInstance.(models.InstanceNameLookup)
		if !ok {
			return nil, fmt.Errorf("UNIQUE_INSTANCE_NAMES: the instance repository cannot look up names on a cluster")
		}
		defaultClusterID := startupDefaultClusterID(svc.ClusterRegistry)
		slog.Info("Instance names are unique for each cluster (UNIQUE_INSTANCE_NAMES)")
		// One query over all instances on every replica start; small table.
		dupCtx, cancelDup := context.WithTimeout(context.Background(), duplicateNamesTimeout)
		handlers.LogDuplicateInstanceNames(dupCtx, lookup, defaultClusterID)
		cancelDup()
	}

	// Instance handler.
	instanceHandler, err := handlers.NewInstanceHandlerWithDeployer(
		repos.StackInstance, repos.ValueOverride, repos.ChartBranchOverride,
		repos.StackDefinition, repos.ChartConfig,
		repos.StackTemplate, repos.TemplateChartConfig, svc.ValuesGen, repos.User,
		svc.DeployManager, svc.K8sWatcher, svc.ClusterRegistry, repos.DeploymentLog, repos.Cluster,
		cfg.App.DefaultInstanceTTLMinutes,
		repos.TxRunner,
	)
	if err != nil {
		return nil, fmt.Errorf("create instance handler: %w", err)
	}
	instanceHandler.WithHooks(svc.HookDispatcher).WithActions(svc.ActionRegistry).WithNotifier(svc.LifecycleNotifier).WithSharedValues(repos.SharedValues).
		WithTemplateVersions(repos.TemplateVersion).WithClusterQuotas(repos.ResourceQuota).
		WithFollowers(repos.InstanceFollower).WithUniqueInstanceNames(cfg.App.UniqueInstanceNames).
		WithMaxTTLDays(cfg.App.MaxInstanceTTLDays).WithActionRuns(repos.ActionRun)
	if hub != nil {
		// Delete, stop and clean close the open tunnels of the instance.
		instanceHandler.WithStreamRevoker(hub)
	}

	// Stream handler (port-forward) and its session manager.
	streams := podstream.NewManager(podstream.Config{
		MaxPerUser:  cfg.Stream.PortForward.MaxPerUser,
		MaxDuration: cfg.Stream.PortForward.MaxDuration,
		IdleTimeout: cfg.Stream.PortForward.IdleTimeout,
	})
	logExecWarnings(cfg.Stream.Exec)
	// Exec has its own manager: its own limit per user and maximum duration,
	// and no idle timeout. Its sessions are counted with kind=exec.
	execStreams := podstream.NewManager(podstream.Config{
		Kind:          podstream.KindExec,
		MaxPerUser:    cfg.Stream.Exec.MaxPerUser,
		MaxDuration:   cfg.Stream.Exec.MaxDuration,
		NoIdleTimeout: true,
	})
	streamDeps := handlers.StreamDeps{
		ExecManager:     execStreams,
		ExecProvider:    execClientProvider(svc.ClusterRegistry),
		ChartConfigRepo: repos.ChartConfig,
		InstanceRepo:    repos.StackInstance,
		ClusterRepo:     repos.Cluster,
		Provider:        portForwarderProvider(svc.ClusterRegistry),
		Manager:         streams,
		Audit:           repos.AuditLog,
		AllowedOrigins:  cfg.CORS.AllowedOrigins,
		SessionStore:    sessStore,
		UserRepo:        repos.User,
		APIKeyRepo:      repos.APIKey,
		Config:          cfg.Stream,
	}
	if hub != nil {
		streamDeps.Hub = hub
	}
	streamHandler := handlers.NewStreamHandler(streamDeps)
	// /status and /pods with exec_hints=true use the checks of the exec route.
	instanceHandler.WithExecHints(streamHandler)

	// Git handler.
	gitHandler := handlers.NewGitHandler(svc.GitRegistry)

	// Audit log handler.
	auditLogHandler := handlers.NewAuditLogHandler(repos.AuditLog)

	// User handler.
	userHandler := handlers.NewUserHandler(repos.User, repos.RefreshToken, repos.APIKey)
	userHandler.SetSessionStore(sessStore)
	userHandler.SetAccessTokenExpiration(cfg.Auth.AccessTokenExpiration)
	userHandler.SetJWTExpiration(cfg.Auth.JWTExpiration)
	if hub != nil {
		userHandler.SetWebSocketRevoker(hub)
	}

	// API key handler.
	apiKeyHandler := handlers.NewAPIKeyHandler(repos.APIKey, repos.User, &cfg.Auth)
	if hub != nil {
		apiKeyHandler.SetWebSocketRevoker(hub)
	}

	// Admin handler.
	adminHandler := handlers.NewAdminHandler(svc.ClusterRegistry, repos.StackInstance)

	// Cluster handler.
	clusterHandler := handlers.NewClusterHandlerWithQuotas(repos.Cluster, svc.ClusterRegistry, repos.StackInstance, repos.ResourceQuota).
		WithInstanceQuotaOverrides(repos.InstanceQuotaOverride)

	// Branch override handler.
	branchOverrideHandler := handlers.NewBranchOverrideHandler(repos.ChartBranchOverride, repos.StackInstance, repos.ChartConfig)

	// Instance quota override handler.
	instanceQuotaOverrideHandler := handlers.NewInstanceQuotaOverrideHandler(repos.InstanceQuotaOverride, repos.StackInstance).
		WithClusterQuotas(repos.ResourceQuota, svc.ClusterRegistry)

	// Shared values handler.
	sharedValuesHandler := handlers.NewSharedValuesHandler(repos.SharedValues, repos.Cluster)

	// Notification handler.
	notificationHandler := handlers.NewNotificationHandler(repos.Notification)

	// Favorite handler.
	favoriteHandler := handlers.NewFavoriteHandler(repos.UserFavorite)

	// Quick deploy handler.
	quickDeployHandler, err := handlers.NewQuickDeployHandler(
		repos.StackTemplate, repos.TemplateChartConfig, repos.StackDefinition, repos.ChartConfig,
		repos.StackInstance, repos.ChartBranchOverride, repos.ValueOverride, svc.ValuesGen,
		svc.DeployManager, repos.User, repos.DeploymentLog, repos.AuditLog,
		hub, svc.ClusterRegistry, svc.K8sWatcher,
		cfg.App.DefaultInstanceTTLMinutes,
		repos.TxRunner,
	)
	if err != nil {
		return nil, fmt.Errorf("create quick deploy handler: %w", err)
	}
	quickDeployHandler.WithSharedValues(repos.SharedValues).WithTemplateVersions(repos.TemplateVersion).
		WithUniqueInstanceNames(cfg.App.UniqueInstanceNames).WithMaxTTLDays(cfg.App.MaxInstanceTTLDays).
		WithNotifier(svc.LifecycleNotifier).WithClusterRepo(repos.Cluster)

	// Cluster shared values are the lowest values layer of every deploy.
	// Warn loudly if they are not wired: deploys would silently skip them.
	if !instanceHandler.SharedValuesConfigured() {
		slog.Warn("shared values repository not wired on the instance handler; cluster shared values are not applied to deploy, preview, export or compare")
	}
	if !quickDeployHandler.SharedValuesConfigured() {
		slog.Warn("shared values repository not wired on the quick deploy handler; cluster shared values are not applied to quick deploy")
	}

	// Analytics handler.
	analyticsHandler := handlers.NewAnalyticsHandler(repos.StackTemplate, repos.StackDefinition, repos.StackInstance, repos.DeploymentLog, repos.User).
		WithDeployLogRetention(cfg.Retention.DeployLogDays)

	// Dashboard handler.
	dashboardHandler := handlers.NewDashboardHandler(repos.Cluster, repos.StackInstance, repos.DeploymentLog, svc.ClusterRegistry)

	// Cleanup policy handler.
	cleanupPolicyHandler := handlers.NewCleanupPolicyHandler(repos.CleanupPolicy, svc.CleanupScheduler)

	// Notification channel handler (only when repo is available).
	var notificationChannelHandler *handlers.NotificationChannelHandler
	if repos.NotificationChannel != nil {
		notificationChannelHandler = handlers.NewNotificationChannelHandler(repos.NotificationChannel).
			WithFilterLookups(repos.User, repos.StackDefinition, repos.Cluster)
	}

	// Auto-create admin user on startup.
	adminCtx, cancelAdmin := context.WithTimeout(context.Background(), 30*time.Second)
	authHandler.EnsureAdminUser(adminCtx)
	cancelAdmin()

	return &handlerSet{
		Auth:                  authHandler,
		OIDC:                  oidcHandler,
		Template:              templateHandler,
		Definition:            definitionHandler,
		TemplateVersion:       templateVersionHandler,
		Instance:              instanceHandler,
		Git:                   gitHandler,
		AuditLog:              auditLogHandler,
		User:                  userHandler,
		APIKey:                apiKeyHandler,
		Admin:                 adminHandler,
		Cluster:               clusterHandler,
		BranchOverride:        branchOverrideHandler,
		InstanceQuotaOverride: instanceQuotaOverrideHandler,
		SharedValues:          sharedValuesHandler,
		Notification:          notificationHandler,
		Favorite:              favoriteHandler,
		QuickDeploy:           quickDeployHandler,
		Analytics:             analyticsHandler,
		Dashboard:             dashboardHandler,
		CleanupPolicy:         cleanupPolicyHandler,
		NotificationChannel:   notificationChannelHandler,
		Stream:                streamHandler,
		Streams:               streams,
		ExecStreams:           execStreams,
	}, nil
}

// sessionActivityFunc returns the hook that moves the idle-timeout clock of a
// refresh-token family forward on authenticated requests. nil repo: no hook.
func sessionActivityFunc(repo models.RefreshTokenRepository) middleware.SessionActivityFunc {
	if repo == nil {
		return nil
	}
	return func(ctx context.Context, sessionID string, at time.Time) error {
		return repo.TouchFamily(ctx, sessionID, at)
	}
}

// buildRouter creates the Gin engine, wires all routes, and returns the
// router plus the rate limiters (caller must stop them on shutdown).
func buildRouter(cfg *config.Config, hs *handlerSet, deps routerDeps) (*gin.Engine, *routes.RateLimiters) {
	gin.SetMode(ginMode(os.Getenv("GIN_MODE"), cfg.App.Debug))
	router := gin.New()
	rateLimiters := routes.SetupRoutes(router, routes.Deps{
		HealthChecker:                deps.HealthChecker,
		Config:                       cfg,
		Hub:                          deps.Hub,
		AuthHandler:                  hs.Auth,
		TemplateHandler:              hs.Template,
		DefinitionHandler:            hs.Definition,
		InstanceHandler:              hs.Instance,
		GitHandler:                   hs.Git,
		AuditLogHandler:              hs.AuditLog,
		AuditLogger:                  deps.Repos.AuditLog,
		UserHandler:                  hs.User,
		APIKeyHandler:                hs.APIKey,
		AdminHandler:                 hs.Admin,
		BranchOverrideHandler:        hs.BranchOverride,
		InstanceQuotaOverrideHandler: hs.InstanceQuotaOverride,
		TemplateVersionHandler:       hs.TemplateVersion,
		NotificationHandler:          hs.Notification,
		FavoriteHandler:              hs.Favorite,
		QuickDeployHandler:           hs.QuickDeploy,
		AnalyticsHandler:             hs.Analytics,
		DashboardHandler:             hs.Dashboard,
		CleanupPolicyHandler:         hs.CleanupPolicy,
		NotificationChannelHandler:   hs.NotificationChannel,
		StreamHandler:                hs.Stream,
		CleanupScheduler:             deps.Svc.CleanupScheduler,
		ClusterHandler:               hs.Cluster,
		SharedValuesHandler:          hs.SharedValues,
		UserRepo:                     deps.Repos.User,
		APIKeyRepo:                   deps.Repos.APIKey,
		OIDCHandler:                  hs.OIDC,
		SessionStore:                 deps.SessionStore,
		SessionActivity:              sessionActivityFunc(deps.Repos.RefreshToken),
		HealthVerbose:                cfg.Server.HealthVerbose,
	})
	return router, rateLimiters
}

// refreshTokenCleanupInterval is how often the leader deletes expired
// refresh tokens.
const refreshTokenCleanupInterval = time.Hour

// refreshTokenCleanupRun returns the Run function of the leader worker
// refresh-token-cleanup. Each cleanup runs with the worker context, so the
// end of the leadership term stops a running delete (#656).
func refreshTokenCleanupRun(cleanup func(ctx context.Context), interval time.Duration) func(ctx context.Context) {
	return func(ctx context.Context) {
		// Once at the start of the term, then each interval: a leader change
		// must not delay the cleanup by up to one interval each time.
		cleanup(ctx)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cleanup(ctx)
			}
		}
	}
}

// buildLeaderWorkers creates the background workers that run on the leader
// replica only (TTL reaper, expiry warner, cleanup scheduler, quota and
// secret monitors, secret refresher, cluster health poller, k8s status
// watcher, refresh token cleanup, interrupted operation recovery). It does
// not start them: the leader
// election starts the group for each leadership term. Every replica runs
// the HTTP server, the WebSocket hub and its revalidation.
func buildLeaderWorkers(
	svc *domainServices,
	hs *handlerSet,
	repos *database.RepositorySet,
	hub *websocket.Hub,
	stopTimeout time.Duration,
) *leaderWorkers {
	// TTL reaper for auto-expiring stack instances.
	expiryStopper := deployer.NewExpiryStopper(svc.DeployManager, repos.StackDefinition, repos.ChartConfig)
	reaper := ttl.NewReaper(repos.StackInstance, repos.AuditLog, hub, expiryStopper, 60*time.Second).
		WithNotifier(svc.LifecycleNotifier)

	// TTL expiry warner — warns users before their stack expires.
	expiryWarner := ttl.NewWarner(repos.StackInstance, svc.LifecycleNotifier, 30*time.Minute, 60*time.Second)

	// Quota monitor — alerts admins when cluster resource usage is high.
	quotaMonitor := cluster.NewQuotaMonitor(cluster.QuotaMonitorConfig{
		ClusterRepo:  repos.Cluster,
		InstanceRepo: repos.StackInstance,
		QuotaRepo:    repos.ResourceQuota,
		Registry:     svc.ClusterRegistry,
		Notifier:     svc.LifecycleNotifier,
	})

	// Secret expiry monitor — alerts admins before secrets expire.
	secretMonitor := cluster.NewSecretMonitor(cluster.SecretMonitorConfig{
		ClusterRepo:  repos.Cluster,
		InstanceRepo: repos.StackInstance,
		Registry:     svc.ClusterRegistry,
		Notifier:     svc.LifecycleNotifier,
	})

	group := leader.NewGroup(stopTimeout,
		leader.Worker{Name: "ttl-reaper", Run: reaper.Run},
		leader.Worker{Name: "expiry-warner", Run: expiryWarner.Run},
		leader.Worker{Name: "cleanup-scheduler", Run: svc.CleanupScheduler.Run},
		leader.Worker{Name: "quota-monitor", Run: quotaMonitor.Run},
		leader.Worker{Name: "secret-monitor", Run: secretMonitor.Run},
		leader.Worker{Name: "secret-refresher", Run: svc.SecretRefresher.Run},
		leader.Worker{Name: "cluster-health-poller", Run: svc.HealthPoller.Run},
		leader.Worker{Name: "k8s-status-watcher", Run: svc.K8sWatcher.Run},
		leader.Worker{Name: "refresh-token-cleanup", Run: refreshTokenCleanupRun(hs.Auth.CleanupExpiredTokens, refreshTokenCleanupInterval)},
	)

	// Namespace-scoped access: give stack namespaces that existed before
	// (or lost the RoleBinding) the access RoleBinding of the backend.
	if svc.ClusterRegistry != nil && svc.ClusterRegistry.HasAccessBinding() {
		reconciler := cluster.NewAccessReconciler(repos.Cluster, svc.ClusterRegistry, 0)
		group.Add(leader.Worker{Name: "stack-access-reconcile", Run: reconciler.Run})
	}

	// Ends the operations of replicas without a heartbeat. Once at the
	// start of the term, then every minute.
	if svc.InterruptRecovery != nil {
		group.Add(leader.Worker{Name: "interrupted-operation-recovery", Run: svc.InterruptRecovery.Run})
	}

	return &leaderWorkers{
		Group:         group,
		Reaper:        reaper,
		ExpiryWarner:  expiryWarner,
		QuotaMonitor:  quotaMonitor,
		SecretMonitor: secretMonitor,
	}
}

// buildLogRetention returns the leader worker log-retention, which deletes
// old rows of the log tables, or nil when no table has a retention (all
// *_RETENTION_DAYS are 0).
func buildLogRetention(cfg *config.Config, repos *database.RepositorySet) *leader.Worker {
	if repos == nil || repos.Retention == nil || !cfg.Retention.Enabled() {
		return nil
	}
	slog.Info("log retention enabled",
		"deployment_logs_days", cfg.Retention.DeployLogDays,
		"audit_logs_days", cfg.Retention.AuditLogDays,
		"notifications_days", cfg.Retention.NotificationDays,
		"delivery_logs_days", cfg.Retention.DeliveryLogDays,
		"action_runs_days", cfg.Retention.ActionRunDays,
	)
	w := retention.NewWorker(repos.Retention, cfg.Retention)
	return &leader.Worker{Name: "log-retention", Run: w.Run}
}

// buildWSFanout creates the WebSocket fan-out between replicas when
// WS_FANOUT_ENABLED is true. It returns the fan-out (not started) and the
// leader-only cleanup worker for ws_events. When fan-out is disabled it
// returns nil for both: the hub then serves its own clients only and writes
// nothing to the database.
func buildWSFanout(cfg *config.Config, hub *websocket.Hub, repo models.WSEventRepository) (*websocket.Fanout, *leader.Worker) {
	// Config validation requires an identity when fan-out is on.
	if !cfg.WSFanout.Enabled || repo == nil || cfg.LeaderElection.Identity == "" {
		return nil, nil
	}
	fanout := websocket.NewFanout(hub, repo, websocket.FanoutConfig{
		Origin:       wsFanoutOrigin(cfg.LeaderElection.Identity),
		PollInterval: cfg.WSFanout.PollInterval,
	})
	if fanout == nil {
		return nil, nil
	}
	cleaner := websocket.NewEventCleaner(repo, cfg.WSFanout.Retention, websocket.DefaultFanoutCleanupInterval)
	return fanout, &leader.Worker{Name: "ws-event-cleanup", Run: cleaner.Run}
}

// wsFanoutOrigin returns the fan-out origin of this process: the replica
// identity (POD_NAME or host name) plus a random suffix. The suffix keeps two
// processes with the same host name (local development, a restarted pod with
// a reused name) from skipping each other's rows. The leader election keeps
// the plain identity. The result fits the 253-character origin column.
func wsFanoutOrigin(identity string) string {
	const maxIdentity = 253 - 9 // "-" + 8 hex characters
	if len(identity) > maxIdentity {
		identity = identity[:maxIdentity]
	}
	return identity + "-" + uuid.New().String()[:8]
}

// leaderConfig converts the configuration to the leader package settings.
func leaderConfig(c config.LeaderElectionConfig) leader.Config {
	return leader.Config{
		Enabled:       c.Enabled,
		LeaseName:     c.LeaseName,
		Namespace:     c.Namespace,
		Identity:      c.Identity,
		LeaseDuration: c.LeaseDuration,
		RenewDeadline: c.RenewDeadline,
		RetryPeriod:   c.RetryPeriod,
	}
}

// leaderRuntime runs the leader election and the leader worker group.
type leaderRuntime struct {
	elector *leader.Elector
	workers *leader.Group
	cancel  context.CancelFunc
	done    chan struct{}
}

// startLeaderElection campaigns for the lease in a goroutine. The leader
// starts workers for each leadership term and stops them (and waits for
// them) when the term ends. With election disabled, this process is the
// leader until shutdown.
func startLeaderElection(elector *leader.Elector, workers *leader.Group) *leaderRuntime {
	ctx, cancel := context.WithCancel(context.Background())
	r := &leaderRuntime{elector: elector, workers: workers, cancel: cancel, done: make(chan struct{})}
	slog.Info("Leader workers configured", "workers", workers.Names(), "election_enabled", elector.Enabled())
	go func() {
		defer close(r.done)
		elector.Run(ctx, workers.Start, workers.Stop)
	}()
	return r
}

// Shutdown stops the leader workers first and then ends the election. The
// leader releases the lease only after its workers stopped, so the next
// leader does not run the same jobs at the same time.
func (r *leaderRuntime) Shutdown(timeout time.Duration) {
	r.workers.Close(timeout)
	r.cancel()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-r.done:
	case <-timer.C:
		slog.Error("Leader election did not stop in time", "timeout", timeout)
	}
}

// servers holds the HTTP servers started during bootstrap.
type servers struct {
	Main    *http.Server
	Pprof   *http.Server // nil when pprof is disabled
	Metrics *http.Server // nil when METRICS_ENABLED=false
}

// startHTTPServer binds the main server and, when enabled, the pprof and
// metrics servers, then serves them in background goroutines. It binds
// before it serves, so a bind failure of the main server is an error for the
// caller (#662). A pprof or metrics bind failure is only logged. The Addr of
// each started server is its bound address (port 0 gets the real port).
func startHTTPServer(router *gin.Engine, cfg *config.Config, tel *telemetry.Telemetry) (*servers, error) {
	s := &servers{}

	s.Main = &http.Server{
		Addr:         fmt.Sprintf("%s:%s", cfg.Server.Host, cfg.Server.Port),
		Handler:      router,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
	}
	mainLn, err := net.Listen("tcp", s.Main.Addr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", s.Main.Addr, err)
	}
	s.Main.Addr = mainLn.Addr().String()

	// Start pprof server on a separate port when PPROF_ENABLED=true.
	if cfg.Server.PprofEnabled {
		runtime.SetMutexProfileFraction(cfg.Server.PprofMutexFraction)
		runtime.SetBlockProfileRate(cfg.Server.PprofBlockRate)
		slog.Info("pprof profiles configured",
			"mutex_fraction", cfg.Server.PprofMutexFraction, "block_rate_ns", cfg.Server.PprofBlockRate)
		pprofMux := http.NewServeMux()
		pprofMux.HandleFunc("/debug/pprof/", pprof.Index)
		pprofMux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		pprofMux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		pprofMux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		pprofMux.HandleFunc("/debug/pprof/trace", pprof.Trace)
		s.Pprof = &http.Server{
			Addr:         cfg.Server.PprofAddr,
			Handler:      pprofMux,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 30 * time.Second,
			IdleTimeout:  60 * time.Second,
		}
		serveOptional(s.Pprof, "pprof server")
	}

	// Start Prometheus metrics server when enabled.
	if tel != nil && tel.MetricsHandler != nil {
		metricsMux := http.NewServeMux()
		metricsMux.Handle("/metrics", tel.MetricsHandler)
		s.Metrics = &http.Server{
			Addr:         cfg.Otel.MetricsAddr,
			Handler:      metricsMux,
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 10 * time.Second,
			IdleTimeout:  30 * time.Second,
		}
		serveOptional(s.Metrics, "metrics server")
	}

	slog.Info("Server starting", "addr", s.Main.Addr)
	go func() {
		if err := s.Main.Serve(mainLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("Server stopped", "error", err)
			os.Exit(1)
		}
	}()

	return s, nil
}

// serveOptional binds srv and serves it in a background goroutine. A bind or
// serve failure is only logged: the main server runs without it.
func serveOptional(srv *http.Server, name string) {
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		slog.Error(name+" failed", "addr", srv.Addr, "error", err)
		return
	}
	srv.Addr = ln.Addr().String()
	slog.Info(name+" starting", "addr", srv.Addr)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error(name+" failed", "error", err)
		}
	}()
}

// stackAccessBinding converts the STACK_ACCESS_* config to the k8s access
// binding; nil when namespace-scoped access is off.
func stackAccessBinding(sa config.StackAccessConfig) *k8s.AccessBinding {
	if !sa.Enabled() {
		return nil
	}
	return &k8s.AccessBinding{
		ClusterRole:             sa.ClusterRole,
		ServiceAccount:          sa.ServiceAccount,
		ServiceAccountNamespace: sa.ServiceAccountNamespace,
	}
}

// ginMode returns the Gin mode: GIN_MODE when it is set, else release mode
// unless APP_DEBUG is true. Debug mode prints every route at startup and
// warnings that are not wanted in production.
func ginMode(envMode string, debug bool) string {
	if envMode != "" {
		return envMode
	}
	if debug {
		return gin.DebugMode
	}
	return gin.ReleaseMode
}

// templateRefLookup adapts the definition repository to the template
// lookup of the hooks package, which does not import models.
func templateRefLookup(repo models.StackDefinitionRepository) hooks.TemplateRefLookup {
	if repo == nil {
		return nil
	}
	return func(ctx context.Context, definitionIDs []string) (map[string]hooks.TemplateRef, error) {
		refs, err := repo.TemplateRefsByIDs(ctx, definitionIDs)
		if err != nil {
			return nil, err
		}
		out := make(map[string]hooks.TemplateRef, len(refs))
		for id, ref := range refs {
			out[id] = hooks.TemplateRef{ID: ref.TemplateID, Name: ref.TemplateName, Category: ref.TemplateCategory}
		}
		return out, nil
	}
}

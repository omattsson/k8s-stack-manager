package main

import (
	"backend/internal/api/handlers"
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
	"backend/internal/models"
	"backend/internal/notifier"
	"backend/internal/scheduler"
	"backend/internal/sessionstore"
	"backend/internal/ttl"
	"backend/internal/websocket"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// domainServices holds all domain-layer services wired during bootstrap.
type domainServices struct {
	GitRegistry       *gitprovider.Registry
	ClusterRegistry   *cluster.Registry
	HealthPoller      *cluster.HealthPoller
	SecretRefresher   *cluster.SecretRefresher
	K8sWatcher        *k8s.Watcher
	DeployManager     *deployer.Manager
	HookDispatcher    *hooks.Dispatcher
	ActionRegistry    *hooks.ActionRegistry
	LifecycleNotifier *notifier.Notifier
	CleanupExecutor   *deployer.CleanupExecutor
	CleanupScheduler  *scheduler.Scheduler
	ValuesGen         *helm.ValuesGenerator
	WatcherCancel     context.CancelFunc
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
}

// routerDeps holds non-handler dependencies required to wire the router.
type routerDeps struct {
	Repo          models.Repository
	HealthChecker *health.HealthChecker
	Hub           *websocket.Hub
	SessionStore  sessionstore.SessionStore
	Repos         *database.RepositorySet
	Svc           *domainServices
}

// backgroundServices holds services started after the router is ready.
type backgroundServices struct {
	Reaper                    *ttl.Reaper
	ExpiryWarner              *ttl.Warner
	QuotaMonitor              *cluster.QuotaMonitor
	SecretMonitor             *cluster.SecretMonitor
	RefreshTokenCleanupCancel context.CancelFunc
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
	// Git provider registry.
	gitRegistry := gitprovider.NewRegistry(gitprovider.Config{
		AzureDevOps: gitprovider.AzureDevOpsConfig{
			PAT:        cfg.GitProvider.AzureDevOpsPAT,
			DefaultOrg: cfg.GitProvider.AzureDevOpsDefaultOrg,
		},
		GitLab: gitprovider.GitLabConfig{
			Token:   cfg.GitProvider.GitLabToken,
			BaseURL: cfg.GitProvider.GitLabBaseURL,
		},
	})

	valuesGen := helm.NewValuesGenerator()

	// Auto-create default cluster from KUBECONFIG_PATH for single-cluster migration.
	ensureDefaultCluster(repos.Cluster, repos.StackInstance, cfg)

	// Cluster registry for multi-cluster client management.
	clusterRegistry := cluster.NewRegistry(cluster.RegistryOptions{
		ClusterRepo: repos.Cluster,
		HelmBinary:  cfg.Deployment.HelmBinary,
		HelmTimeout: cfg.Deployment.DeploymentTimeout,
	})

	// Extended health checks.
	healthChecker.AddCheck("cluster_registry", func(ctx context.Context) error {
		return clusterRegistry.HealthCheck(ctx)
	})
	healthChecker.AddCheck("git_provider", func(ctx context.Context) error {
		return gitRegistry.HealthCheck(ctx)
	})
	healthChecker.AddCheck("helm", deployer.HelmHealthCheck(cfg.Deployment.HelmBinary))

	// Load hooks config before starting goroutines so errors don't leak them.
	hookCfg, actionSpecs, hookErr := hooks.LoadConfigFile(cfg.Deployment.HooksConfigFile)
	if hookErr != nil {
		return nil, fmt.Errorf("load hooks config: %w", hookErr)
	}

	var hookDispatcher *hooks.Dispatcher
	if len(hookCfg.Subscriptions) > 0 {
		hookDispatcher, hookErr = hooks.NewDispatcher(hookCfg, http.DefaultClient)
		if hookErr != nil {
			return nil, fmt.Errorf("build hooks dispatcher: %w", hookErr)
		}
	}

	var actionRegistry *hooks.ActionRegistry
	if len(actionSpecs) > 0 {
		actionRegistry, hookErr = hooks.NewActionRegistry(actionSpecs, http.DefaultClient)
		if hookErr != nil {
			return nil, fmt.Errorf("build action registry: %w", hookErr)
		}
	}

	// Cluster health poller.
	healthPoller := cluster.NewHealthPoller(cluster.HealthPollerConfig{
		ClusterRepo: repos.Cluster,
		Registry:    clusterRegistry,
		Interval:    cfg.Deployment.ClusterHealthPollInterval,
		Hub:         hub,
	})
	healthPoller.Start()

	// Image pull secret refresher.
	secretRefresher := cluster.NewSecretRefresher(cluster.SecretRefresherConfig{
		ClusterRepo:  repos.Cluster,
		InstanceRepo: repos.StackInstance,
		Registry:     clusterRegistry,
	})
	secretRefresher.Start()

	// K8s watcher for multi-cluster monitoring.
	k8sWatcher := k8s.NewWatcher(clusterRegistry, repos.StackInstance, hub, 30*time.Second)
	watcherCtx, watcherCancel := context.WithCancel(context.Background())
	k8sWatcher.Start(watcherCtx)

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
	lifecycleNotifier := notifier.NewNotifier(repos.Notification, hub, repos.User)

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
		StabilizeTimeout:           cfg.Deployment.StabilizeTimeout,
		StabilizePollInterval:      cfg.Deployment.StabilizePollInterval,
		Hooks:                      hookDispatcher,
		Notifier:                   lifecycleNotifier,
	})

	// Cleanup executor + scheduler.
	cleanupExecutor := deployer.NewCleanupExecutor(deployManager, repos.StackDefinition, repos.ChartConfig, repos.StackInstance)
	cleanupScheduler := scheduler.NewScheduler(repos.CleanupPolicy, repos.StackInstance, repos.AuditLog, cleanupExecutor, lifecycleNotifier)

	return &domainServices{
		GitRegistry:       gitRegistry,
		ClusterRegistry:   clusterRegistry,
		HealthPoller:      healthPoller,
		SecretRefresher:   secretRefresher,
		K8sWatcher:        k8sWatcher,
		DeployManager:     deployManager,
		HookDispatcher:    hookDispatcher,
		ActionRegistry:    actionRegistry,
		LifecycleNotifier: lifecycleNotifier,
		CleanupExecutor:   cleanupExecutor,
		CleanupScheduler:  cleanupScheduler,
		ValuesGen:         valuesGen,
		WatcherCancel:     watcherCancel,
	}, nil
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

	// Template version handler.
	templateVersionHandler := handlers.NewTemplateVersionHandler(repos.TemplateVersion, repos.StackTemplate)

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
	instanceHandler.WithHooks(svc.HookDispatcher).WithActions(svc.ActionRegistry).WithNotifier(svc.LifecycleNotifier)

	// Git handler.
	gitHandler := handlers.NewGitHandler(svc.GitRegistry)

	// Audit log handler.
	auditLogHandler := handlers.NewAuditLogHandler(repos.AuditLog)

	// User handler.
	userHandler := handlers.NewUserHandler(repos.User)
	userHandler.SetSessionStore(sessStore)
	if repos.RefreshToken != nil {
		userHandler.SetRefreshTokenRepo(repos.RefreshToken)
	}
	userHandler.SetAccessTokenExpiration(cfg.Auth.AccessTokenExpiration)
	userHandler.SetJWTExpiration(cfg.Auth.JWTExpiration)

	// API key handler.
	apiKeyHandler := handlers.NewAPIKeyHandler(repos.APIKey, repos.User, &cfg.Auth)

	// Admin handler.
	adminHandler := handlers.NewAdminHandler(svc.ClusterRegistry, repos.StackInstance)

	// Cluster handler.
	clusterHandler := handlers.NewClusterHandlerWithQuotas(repos.Cluster, svc.ClusterRegistry, repos.StackInstance, repos.ResourceQuota)

	// Branch override handler.
	branchOverrideHandler := handlers.NewBranchOverrideHandler(repos.ChartBranchOverride, repos.StackInstance)

	// Instance quota override handler.
	instanceQuotaOverrideHandler := handlers.NewInstanceQuotaOverrideHandler(repos.InstanceQuotaOverride, repos.StackInstance)

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

	// Analytics handler.
	analyticsHandler := handlers.NewAnalyticsHandler(repos.StackTemplate, repos.StackDefinition, repos.StackInstance, repos.DeploymentLog, repos.User)

	// Dashboard handler.
	dashboardHandler := handlers.NewDashboardHandler(repos.Cluster, repos.StackInstance, repos.DeploymentLog, svc.ClusterRegistry)

	// Cleanup policy handler.
	cleanupPolicyHandler := handlers.NewCleanupPolicyHandler(repos.CleanupPolicy, svc.CleanupScheduler)

	// Auto-create admin user on startup.
	authHandler.EnsureAdminUser()

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
	}, nil
}

// buildRouter creates the Gin engine, wires all routes, and returns the
// router plus the rate limiters (caller must stop them on shutdown).
func buildRouter(cfg *config.Config, hs *handlerSet, deps routerDeps) (*gin.Engine, *routes.RateLimiters) {
	router := gin.New()
	rateLimiters := routes.SetupRoutes(router, routes.Deps{
		Repository:                   deps.Repo,
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
		CleanupScheduler:             deps.Svc.CleanupScheduler,
		ClusterHandler:               hs.Cluster,
		SharedValuesHandler:          hs.SharedValues,
		UserRepo:                     deps.Repos.User,
		APIKeyRepo:                   deps.Repos.APIKey,
		OIDCHandler:                  hs.OIDC,
		SessionStore:                 deps.SessionStore,
		HealthVerbose:                cfg.Server.HealthVerbose,
	})
	return router, rateLimiters
}

// startBackgroundServices starts all background goroutines (TTL reaper, expiry
// warner, quota monitor, secret monitor, refresh token cleanup, cleanup scheduler)
// and returns a struct the caller can use to stop them.
func startBackgroundServices(
	svc *domainServices,
	hs *handlerSet,
	repos *database.RepositorySet,
	hub *websocket.Hub,
) (*backgroundServices, error) {
	// TTL reaper for auto-expiring stack instances.
	expiryStopper := deployer.NewExpiryStopper(svc.DeployManager, repos.StackDefinition, repos.ChartConfig)
	reaper := ttl.NewReaper(repos.StackInstance, repos.AuditLog, hub, expiryStopper, 60*time.Second)
	go reaper.Start()

	// TTL expiry warner — warns users before their stack expires.
	expiryWarner := ttl.NewWarner(repos.StackInstance, svc.LifecycleNotifier, 30*time.Minute, 60*time.Second)
	go expiryWarner.Start()

	// Quota monitor — alerts admins when cluster resource usage is high.
	quotaMonitor := cluster.NewQuotaMonitor(cluster.QuotaMonitorConfig{
		ClusterRepo:  repos.Cluster,
		InstanceRepo: repos.StackInstance,
		QuotaRepo:    repos.ResourceQuota,
		Registry:     svc.ClusterRegistry,
		Notifier:     svc.LifecycleNotifier,
	})
	quotaMonitor.Start()

	// Secret expiry monitor — alerts admins before secrets expire.
	secretMonitor := cluster.NewSecretMonitor(cluster.SecretMonitorConfig{
		ClusterRepo:  repos.Cluster,
		InstanceRepo: repos.StackInstance,
		Registry:     svc.ClusterRegistry,
		Notifier:     svc.LifecycleNotifier,
	})
	secretMonitor.Start()

	// Periodically clean up expired refresh tokens (every hour).
	refreshTokenCleanupCtx, refreshTokenCleanupCancel := context.WithCancel(context.Background())
	go func(ctx context.Context) {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				hs.Auth.CleanupExpiredTokens()
			}
		}
	}(refreshTokenCleanupCtx)

	// Start cleanup scheduler.
	if err := svc.CleanupScheduler.Start(); err != nil {
		refreshTokenCleanupCancel()
		reaper.Stop()
		expiryWarner.Stop()
		quotaMonitor.Stop()
		secretMonitor.Stop()
		return nil, fmt.Errorf("start cleanup scheduler: %w", err)
	}

	return &backgroundServices{
		Reaper:                    reaper,
		ExpiryWarner:              expiryWarner,
		QuotaMonitor:              quotaMonitor,
		SecretMonitor:             secretMonitor,
		RefreshTokenCleanupCancel: refreshTokenCleanupCancel,
	}, nil
}

// servers holds the HTTP servers started during bootstrap.
type servers struct {
	Main  *http.Server
	Pprof *http.Server // nil when pprof is disabled
}

// startHTTPServer creates, configures, and starts the HTTP server (and
// optionally a pprof server) in background goroutines.
func startHTTPServer(router *gin.Engine, cfg *config.Config) *servers {
	s := &servers{}

	// Start pprof server on a separate port when PPROF_ENABLED=true.
	if cfg.Server.PprofEnabled {
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
		go func() {
			slog.Info("pprof server starting", "addr", s.Pprof.Addr)
			if err := s.Pprof.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("pprof server failed", "error", err)
			}
		}()
	}

	s.Main = &http.Server{
		Addr:         fmt.Sprintf("%s:%s", cfg.Server.Host, cfg.Server.Port),
		Handler:      router,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
	}

	go func() {
		slog.Info("Server starting", "addr", s.Main.Addr)
		if err := s.Main.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("Failed to start server", "error", err)
			os.Exit(1)
		}
	}()

	return s
}

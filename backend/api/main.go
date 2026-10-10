// Package main provides the entry point for the backend API service.
// It handles configuration loading, database setup, and HTTP server initialization.
package main

import (
	"backend/internal/config"
	"backend/internal/deployer"
	"backend/internal/health"
	"backend/internal/leader"
	"backend/internal/models"
	"backend/internal/notifier"
	"backend/internal/replica"
	"backend/internal/scheduler"
	"backend/internal/sessionstore"
	"backend/internal/telemetry"
	"backend/internal/websocket"
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"backend/internal/api/handlers"
	"backend/internal/cluster"

	"github.com/google/uuid"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	defaultShutdownTimeout = 5 * time.Second
)

// must logs a fatal error and exits if err is non-nil.
// Used to reduce repetitive error-checking boilerplate during startup.
func must(name string, err error) {
	if err != nil {
		slog.Error("Failed to create "+name, "error", err)
		os.Exit(1)
	}
}

// @title           Backend API
// @version         1.0
// @description     This is the API documentation for the backend service
// @host            localhost:8081
// @BasePath        /
// @schemes         http https
// @produce         json
// @consumes        json

// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description JWT Bearer token (format: "Bearer <token>")

// @securityDefinitions.apikey ApiKeyAuth
// @in header
// @name X-API-Key
// @description API key (format: "sk_<key>")
func main() {
	// Load configuration.
	cfg, err := config.LoadConfig()
	if err != nil {
		slog.Error("Failed to load configuration", "error", err)
		os.Exit(1)
	}

	if cfg.CORS.AllowedOrigins == "" || cfg.CORS.AllowedOrigins == "*" {
		slog.Warn("CORS wildcard mode: cookie-based refresh tokens require an explicit CORS_ALLOWED_ORIGINS when frontend runs on a different origin")
	}

	// Initialize OpenTelemetry (no-op when OTEL_ENABLED=false).
	tel, err := telemetry.Init(cfg.Otel)
	if err != nil {
		slog.Error("Failed to initialize OpenTelemetry", "error", err)
		os.Exit(1)
	}

	// Leader election. With LEADER_ELECTION_ENABLED=true, startup fails
	// here when the process does not run in a Kubernetes pod.
	elector, err := leader.NewInCluster(leaderConfig(cfg.LeaderElection))
	must("leader election", err)

	// Database + generic repository.
	repo, mysqlGormDB, err := initDatabase(cfg)
	must("database", err)

	// Health checker.
	healthChecker := health.New()
	healthChecker.AddCheck("database", func(ctx context.Context) error {
		return repo.Ping(ctx)
	})
	healthChecker.SetReady(true)

	// WebSocket hub.
	hub := websocket.NewHub()
	go hub.Run()

	// Domain-specific repositories.
	repos, err := initRepositories(cfg, mysqlGormDB)
	must("domain repositories", err)

	// WebSocket fan-out between replicas (WS_FANOUT_ENABLED). Each hub send
	// also writes a ws_events row; a poller delivers the rows of the other
	// replicas to the local clients. Disabled: no database writes.
	wsFanout, wsEventCleanup := buildWSFanout(cfg, hub, repos.WSEvent)
	wsFanout.Start()

	// Register database/sql pool metrics with OTel after repositories are available.
	if cfg.Otel.Enabled || cfg.Otel.MetricsEnabled {
		sqlDB, dbErr := mysqlGormDB.DB()
		if dbErr == nil {
			if metricsErr := telemetry.StartDBMetrics(sqlDB); metricsErr != nil {
				slog.Warn("Failed to register DB pool metrics", "error", metricsErr)
			}
		}
		if metricsErr := telemetry.StartBusinessMetrics(
			repos.StackInstance,
			repos.User,
			repos.StackTemplate,
			repos.Cluster,
		); metricsErr != nil {
			slog.Warn("Failed to register business metrics", "error", metricsErr)
		}
		if metricsErr := telemetry.StartLeaderMetric(elector.IsLeader); metricsErr != nil {
			slog.Warn("Failed to register leader metric", "error", metricsErr)
		}
	}

	// Domain services (git, cluster, deployer, hooks, etc.).
	svc, err := buildDomainServices(cfg, repos, hub, healthChecker)
	must("domain services", err)

	// Heartbeat of this process (every replica). The leader treats a
	// process without a fresh heartbeat as stopped and ends its operations.
	svc.Heartbeat.Start()

	// Session store for token blocklist and OIDC state.
	sessStore := buildSessionStore(cfg.SessionStore.Backend, mysqlGormDB)

	// HTTP handlers.
	hs, err := buildHandlers(cfg, repos, svc, sessStore, hub)
	must("handlers", err)

	// Close revoked WebSocket connections that this replica did not revoke
	// itself (revoke on another replica, token blocklist). Stops on hub
	// shutdown.
	hub.StartRevalidation(websocket.DefaultRevalidateInterval,
		handlers.NewWebSocketRevocationChecker(sessStore, repos.User))

	// Router.
	router, rateLimiters := buildRouter(cfg, hs, routerDeps{
		Repo:          repo,
		HealthChecker: healthChecker,
		Hub:           hub,
		SessionStore:  sessStore,
		Repos:         repos,
		Svc:           svc,
	})
	defer rateLimiters.Stop()

	// Leader-only background workers (TTL reaper, expiry warner, cleanup
	// scheduler, monitors, pollers, k8s watcher). Only the leader replica
	// runs them; readiness does not depend on leadership.
	workers := buildLeaderWorkers(svc, hs, repos, hub, leader.DefaultStopTimeout)
	if wsEventCleanup != nil {
		workers.Group.Add(*wsEventCleanup)
	}
	leaderRun := startLeaderElection(elector, workers.Group)

	// HTTP server.
	srvs := startHTTPServer(router, cfg, tel)

	// Wait for interrupt signal.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	slog.Info("Shutting down server...")

	shutdownTimeout := cfg.Server.ShutdownTimeout
	if shutdownTimeout == 0 {
		shutdownTimeout = defaultShutdownTimeout
	}

	gracefulShutdown(srvs, shutdownTimeout, shutdownDeps{
		telemetry:        tel,
		leader:           leaderRun,
		cleanupScheduler: svc.CleanupScheduler,
		deployManager:    svc.DeployManager,
		heartbeat:        svc.Heartbeat,
		hub:              hub,
		wsFanout:         wsFanout,
		clusterRegistry:  svc.ClusterRegistry,
		sessionStore:     sessStore,
		dashboardHandler: hs.Dashboard,
		notifier:         svc.LifecycleNotifier,
		repo:             repo,
	})
}

// shutdownDeps holds all dependencies that need to be stopped during graceful shutdown.
type shutdownDeps struct {
	telemetry        *telemetry.Telemetry
	leader           *leaderRuntime // nil: no leader workers to stop
	cleanupScheduler *scheduler.Scheduler
	deployManager    *deployer.Manager
	heartbeat        *replica.Heartbeat // nil: no heartbeat repository
	hub              *websocket.Hub
	wsFanout         *websocket.Fanout // nil: fan-out disabled
	clusterRegistry  *cluster.Registry
	sessionStore     sessionstore.SessionStore
	dashboardHandler *handlers.DashboardHandler
	notifier         *notifier.Notifier
	repo             models.Repository
}

// gracefulShutdown stops all services in the correct order.
func gracefulShutdown(srvs *servers, timeout time.Duration, deps shutdownDeps) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// 1. Stop HTTP servers — no new requests.
	if err := srvs.Main.Shutdown(ctx); err != nil {
		slog.Error("Server forced to shutdown", "error", err)
	}
	if srvs.Pprof != nil {
		if err := srvs.Pprof.Shutdown(ctx); err != nil {
			slog.Error("Pprof server forced to shutdown", "error", err)
		}
	}
	if srvs.Metrics != nil {
		if err := srvs.Metrics.Shutdown(ctx); err != nil {
			slog.Error("Metrics server forced to shutdown", "error", err)
		}
	}

	// 2. Stop producers of deploy work: the leader workers (TTL reaper,
	//    cleanup scheduler, monitors, pollers, watcher), then release the
	//    lease so another replica takes over. Then cancel manual cleanup runs.
	if deps.leader != nil {
		deps.leader.Shutdown(timeout)
	}
	if deps.cleanupScheduler != nil {
		deps.cleanupScheduler.Stop()
	}

	// 3. Now safe to wait for in-flight deploys. Then stop the heartbeat
	//    and delete its row: the operations of this process have ended.
	deps.deployManager.Shutdown()
	deps.heartbeat.Stop(timeout)

	// 4. Stop remaining services. The fan-out writes its queued rows
	//    before the hub and the database close.
	deps.wsFanout.Stop()
	deps.hub.Shutdown()
	deps.clusterRegistry.Close()
	if deps.notifier != nil {
		deps.notifier.Stop()
	}
	if deps.sessionStore != nil {
		deps.sessionStore.Stop()
	}
	if deps.dashboardHandler != nil {
		deps.dashboardHandler.Stop()
	}

	// 5. Flush OTel spans/metrics/logs AFTER all services stop,
	//    so telemetry from graceful shutdown is captured.
	if deps.telemetry != nil {
		if err := deps.telemetry.Shutdown(ctx); err != nil {
			slog.Error("Failed to shutdown OpenTelemetry", "error", err)
		}
	}

	// 6. Close data layer.
	if closeErr := deps.repo.Close(); closeErr != nil {
		slog.Error("Failed to close repository", "error", closeErr)
	}

	slog.Info("Server exited gracefully")
}

// ensureDefaultCluster auto-creates a default cluster from KUBECONFIG_PATH
// when no clusters exist yet. This provides a migration path for existing
// single-cluster setups.
func ensureDefaultCluster(clusterRepo models.ClusterRepository, instanceRepo models.StackInstanceRepository, cfg *config.Config) {
	clusters, err := clusterRepo.List()
	if err != nil {
		slog.Error("Failed to list clusters for default cluster check", "error", err)
		return
	}
	if len(clusters) > 0 {
		return
	}
	if cfg.Deployment.KubeconfigPath == "" {
		return
	}

	// Extract the API server URL from the kubeconfig so the auto-created
	// default cluster satisfies the Validate() requirement.
	apiServerURL := extractAPIServerURL(cfg.Deployment.KubeconfigPath)
	if apiServerURL == "" {
		slog.Warn("Cannot auto-create default cluster: unable to determine API server URL from kubeconfig",
			"kubeconfig_path", cfg.Deployment.KubeconfigPath)
		return
	}

	defaultCluster := &models.Cluster{
		ID:             uuid.New().String(),
		Name:           "default",
		Description:    "Auto-created from KUBECONFIG_PATH",
		KubeconfigPath: cfg.Deployment.KubeconfigPath,
		APIServerURL:   apiServerURL,
		IsDefault:      true,
		HealthStatus:   models.ClusterUnreachable,
	}
	if createErr := clusterRepo.Create(defaultCluster); createErr != nil {
		slog.Error("Failed to auto-create default cluster", "error", createErr)
		return
	}
	slog.Info("auto-created default cluster from KUBECONFIG_PATH",
		"cluster_id", defaultCluster.ID,
		"kubeconfig_path", cfg.Deployment.KubeconfigPath,
		"api_server_url", apiServerURL,
	)

	// Backfill: persist the new default cluster ID onto any existing
	// StackInstance records that have an empty ClusterID, so they won't
	// implicitly follow whatever happens to be the default later.
	backfillInstanceClusterIDs(instanceRepo, defaultCluster.ID)
}

// backfillInstanceClusterIDs sets the given clusterID on all stack instances
// that currently have an empty ClusterID. It lists all instances and filters
// in-memory so that migration is straightforward.
func backfillInstanceClusterIDs(instanceRepo models.StackInstanceRepository, clusterID string) {
	instances, err := instanceRepo.List()
	if err != nil {
		slog.Warn("Failed to list instances for cluster ID backfill", "error", err)
		return
	}

	backfilledCount := 0
	for i := range instances {
		inst := &instances[i]
		if inst.ClusterID != "" {
			continue
		}

		inst.ClusterID = clusterID
		if updateErr := instanceRepo.Update(inst); updateErr != nil {
			slog.Error("Failed to backfill cluster ID on instance",
				"instance_id", inst.ID,
				"error", updateErr,
			)
			continue
		}
		backfilledCount++
	}

	if backfilledCount > 0 {
		slog.Info("Backfilled default cluster ID on existing instances",
			"cluster_id", clusterID,
			"count", backfilledCount,
		)
	}
}

// extractAPIServerURL reads the kubeconfig file and returns the API server URL
// from the current context. Returns empty string if the URL cannot be determined.
func extractAPIServerURL(kubeconfigPath string) string {
	restCfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		slog.Warn("Failed to parse kubeconfig for API server URL", "path", kubeconfigPath, "error", err)
		return ""
	}
	return restCfg.Host
}

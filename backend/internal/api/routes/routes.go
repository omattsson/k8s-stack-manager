package routes

import (
	_ "backend/docs"
	"backend/internal/api/handlers"
	"backend/internal/api/middleware"
	"backend/internal/cluster"
	"backend/internal/config"
	"backend/internal/health"
	"backend/internal/models"
	"backend/internal/scheduler"
	"backend/internal/websocket"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
)

// Deps holds all handler dependencies needed by SetupRoutes.
type Deps struct {
	Repository    models.Repository
	HealthChecker *health.HealthChecker
	Config        *config.Config
	Hub           *websocket.Hub

	// Phase 1 handlers — constructed by the caller (main.go).
	AuthHandler       *handlers.AuthHandler
	TemplateHandler   *handlers.TemplateHandler
	DefinitionHandler *handlers.DefinitionHandler
	InstanceHandler   *handlers.InstanceHandler
	GitHandler        *handlers.GitHandler
	AuditLogHandler   *handlers.AuditLogHandler
	AuditLogger       middleware.AuditLogger

	// User management and API key handlers.
	UserHandler   *handlers.UserHandler
	APIKeyHandler *handlers.APIKeyHandler

	// Admin handlers.
	AdminHandler *handlers.AdminHandler

	// Branch override handler.
	BranchOverrideHandler *handlers.BranchOverrideHandler

	// Instance quota override handler.
	InstanceQuotaOverrideHandler *handlers.InstanceQuotaOverrideHandler

	// Favorites handler.
	FavoriteHandler *handlers.FavoriteHandler

	// Quick deploy handler.
	QuickDeployHandler *handlers.QuickDeployHandler

	// Analytics handler.
	AnalyticsHandler *handlers.AnalyticsHandler

	// Cleanup policy handler and scheduler.
	CleanupPolicyHandler *handlers.CleanupPolicyHandler
	CleanupScheduler     *scheduler.Scheduler

	// Cluster management.
	ClusterHandler      *handlers.ClusterHandler
	SharedValuesHandler *handlers.SharedValuesHandler
	ClusterRepo         models.ClusterRepository
	Registry            *cluster.Registry
	InstanceRepo        models.StackInstanceRepository

	// Template version handler.
	TemplateVersionHandler *handlers.TemplateVersionHandler

	// Notification handler.
	NotificationHandler *handlers.NotificationHandler

	// Repos needed by combined JWT+API-key auth middleware.
	UserRepo   models.UserRepository
	APIKeyRepo models.APIKeyRepository

	// OIDC handler — nil when OIDC is disabled.
	OIDCHandler *handlers.OIDCHandler

	// Token blocklist for immediate JWT revocation on logout.
	TokenBlocklist *middleware.TokenBlocklist

	// HealthVerbose enables verbose health check output.
	HealthVerbose bool
}

// RateLimiters groups the rate limiters created by SetupRoutes so the
// caller can stop all of them during graceful shutdown.
type RateLimiters struct {
	API   *handlers.RateLimiter
	Login *handlers.RateLimiter
}

// Stop terminates the background cleanup goroutines for all rate limiters.
func (r *RateLimiters) Stop() {
	if r == nil {
		return
	}
	if r.API != nil {
		r.API.Stop()
	}
	if r.Login != nil {
		r.Login.Stop()
	}
}

// SetupRoutes configures all the routes for our application.
// healthChecker is injected from main so the readiness endpoint reflects real dependency health.
// Returns the rate limiters so the caller can stop them during shutdown.
func SetupRoutes(router *gin.Engine, deps Deps) *RateLimiters {
	cfg := deps.Config

	// Add middleware
	router.Use(middleware.RequestID())
	if cfg.Otel.Enabled {
		router.Use(otelgin.Middleware(cfg.Otel.ServiceName))
	}
	router.Use(middleware.Logger())
	router.Use(middleware.Recovery())
	router.Use(middleware.SecurityHeaders())
	router.Use(middleware.CORS(cfg.CORS.AllowedOrigins))
	router.Use(middleware.MaxBodySize(1 << 20)) // 1 MB default

	// WebSocket endpoint (top-level, outside rate limiter — connections are long-lived)
	wsHandler := handlers.NewWebSocketHandler(deps.Hub, cfg.CORS.AllowedOrigins, cfg.Auth.JWTSecret)
	router.GET("/ws", wsHandler.HandleWebSocket)

	// Swagger UI (only when explicitly enabled via ENABLE_SWAGGER=true)
	if cfg.App.EnableSwagger {
		router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	}

	// Health check endpoints
	healthGroup := router.Group("/health")
	{
		healthGroup.GET("/live", handlers.LivenessHandler(deps.HealthChecker))
		healthGroup.GET("/ready", handlers.ReadinessHandler(deps.HealthChecker, deps.HealthVerbose))
		healthGroup.GET("", handlers.HealthCheck) // Keep the original health check for backward compatibility
	}

	// Rate limiter for API routes
	rateLimiter := handlers.NewRateLimiter(int(cfg.Server.RateLimit), time.Minute)

	// Stricter rate limiter for login endpoint to prevent brute-force attacks.
	// Only created when LoginRateLimit > 0; otherwise login uses the general API limiter.
	var loginRateLimiter *handlers.RateLimiter
	if cfg.Server.LoginRateLimit > 0 {
		loginRateLimiter = handlers.NewRateLimiter(int(cfg.Server.LoginRateLimit), time.Minute)
	}

	// API v1 routes
	v1 := router.Group("/api/v1")
	v1.Use(rateLimiter.RateLimit())
	{
		// Ping endpoint
		v1.GET("/ping", handlers.Ping)

		// Items endpoints
		itemsHandler := handlers.NewHandlerWithHub(deps.Repository, deps.Hub)
		items := v1.Group("/items")
		{
			items.GET("", itemsHandler.GetItems)
			items.GET("/:id", itemsHandler.GetItem)
			items.POST("", itemsHandler.CreateItem)
			items.PUT("/:id", itemsHandler.UpdateItem)
			items.DELETE("/:id", itemsHandler.DeleteItem)
		}
	}

	// Phase 1 routes — only register if handlers are provided (they are nil in legacy tests).
	if deps.AuthHandler != nil {
		jwtSecret := cfg.Auth.JWTSecret
		authMW := middleware.CombinedAuth(middleware.APIKeyAuthDeps{
			JWTSecret:  jwtSecret,
			APIKeyRepo: deps.APIKeyRepo,
			UserRepo:   deps.UserRepo,
			Blocklist:  deps.TokenBlocklist,
		})

		// Auth — login and refresh are public; register requires auth.
		auth := v1.Group("/auth")
		{
			loginHandlers := []gin.HandlerFunc{}
			if loginRateLimiter != nil {
				loginHandlers = append(loginHandlers, loginRateLimiter.RateLimit())
			}
			loginHandlers = append(loginHandlers, deps.AuthHandler.Login)
			auth.POST("/login", loginHandlers...)
			registerHandlers := []gin.HandlerFunc{authMW}
			if deps.AuditLogger != nil {
				registerHandlers = append(registerHandlers, middleware.NewAuditMiddleware(deps.AuditLogger))
			}
			registerHandlers = append(registerHandlers, deps.AuthHandler.Register)
			auth.POST("/register", registerHandlers...)
			auth.GET("/me", authMW, deps.AuthHandler.GetCurrentUser)

			// Refresh — public (no auth middleware), uses login rate limiter.
			refreshHandlers := []gin.HandlerFunc{}
			if loginRateLimiter != nil {
				refreshHandlers = append(refreshHandlers, loginRateLimiter.RateLimit())
			}
			refreshHandlers = append(refreshHandlers, deps.AuthHandler.Refresh)
			auth.POST("/refresh", refreshHandlers...)

			// Logout — public so refresh cookie can be cleared even with expired JWT.
			auth.POST("/logout", deps.AuthHandler.Logout)
			auth.POST("/logout-all", authMW, deps.AuthHandler.LogoutAll)

			// OIDC routes — public (no auth required).
			if deps.OIDCHandler != nil {
				oidcGroup := auth.Group("/oidc")
				{
					oidcGroup.GET("/config", deps.OIDCHandler.GetConfig)
					oidcGroup.GET("/authorize", deps.OIDCHandler.Authorize)
					oidcGroup.GET("/callback", deps.OIDCHandler.Callback)
				}
			} else {
				// Return disabled config when OIDC is not configured.
				auth.GET("/oidc/config", func(c *gin.Context) {
					c.JSON(http.StatusOK, gin.H{
						"enabled":            false,
						"provider_name":      "",
						"local_auth_enabled": true,
					})
				})
			}
		}

		// All remaining Phase 1 routes require authentication.
		authed := v1.Group("")
		authed.Use(authMW)

		// Enrich OTel spans with authenticated user attributes.
		if cfg.Otel.Enabled {
			authed.Use(middleware.SpanEnrichUser())
		}

		// Attach audit middleware if an audit logger is provided.
		if deps.AuditLogger != nil {
			authed.Use(middleware.NewAuditMiddleware(deps.AuditLogger))
		}

		devops := middleware.RequireDevOps()

		// Stack Templates
		if deps.TemplateHandler != nil {
			if deps.UserRepo != nil {
				deps.TemplateHandler.SetUserRepo(deps.UserRepo)
			}
			templates := authed.Group("/templates")
			{
				templates.GET("", deps.TemplateHandler.ListTemplates)
				templates.POST("", devops, deps.TemplateHandler.CreateTemplate)
				templates.GET("/:id", deps.TemplateHandler.GetTemplate)
				templates.PUT("/:id", devops, deps.TemplateHandler.UpdateTemplate)
				templates.DELETE("/:id", devops, deps.TemplateHandler.DeleteTemplate)
				templates.POST("/:id/publish", devops, deps.TemplateHandler.PublishTemplate)
				templates.POST("/:id/unpublish", devops, deps.TemplateHandler.UnpublishTemplate)
				templates.POST("/:id/instantiate", deps.TemplateHandler.InstantiateTemplate)
				templates.POST("/:id/clone", devops, deps.TemplateHandler.CloneTemplate)

				// Bulk template operations (DevOps/Admin only)
				bulk := templates.Group("/bulk")
				bulk.Use(devops)
				{
					bulk.POST("/delete", deps.TemplateHandler.BulkDeleteTemplates)
					bulk.POST("/publish", deps.TemplateHandler.BulkPublishTemplates)
					bulk.POST("/unpublish", deps.TemplateHandler.BulkUnpublishTemplates)
				}

				templates.POST("/:id/charts", devops, deps.TemplateHandler.AddTemplateChart)
				templates.PUT("/:id/charts/:chartId", devops, deps.TemplateHandler.UpdateTemplateChart)
				templates.DELETE("/:id/charts/:chartId", devops, deps.TemplateHandler.DeleteTemplateChart)
			}
			// Template versions
			if deps.TemplateVersionHandler != nil {
				templates.GET("/:id/versions", deps.TemplateVersionHandler.ListVersions)
				templates.GET("/:id/versions/diff", deps.TemplateVersionHandler.DiffVersions)
				templates.GET("/:id/versions/:versionId", deps.TemplateVersionHandler.GetVersion)
			}
			if deps.QuickDeployHandler != nil {
				templates.POST("/:id/quick-deploy", deps.QuickDeployHandler.QuickDeploy)
			}
		}

		// Stack Definitions
		if deps.DefinitionHandler != nil {
			definitions := authed.Group("/stack-definitions")
			{
				definitions.GET("", deps.DefinitionHandler.ListDefinitions)
				definitions.POST("", deps.DefinitionHandler.CreateDefinition)
				definitions.POST("/import", devops, deps.DefinitionHandler.ImportDefinition)
				definitions.GET("/:id", deps.DefinitionHandler.GetDefinition)
				definitions.GET("/:id/export", devops, deps.DefinitionHandler.ExportDefinition)
				definitions.PUT("/:id", deps.DefinitionHandler.UpdateDefinition)
				definitions.DELETE("/:id", deps.DefinitionHandler.DeleteDefinition)
				definitions.GET("/:id/check-upgrade", deps.DefinitionHandler.CheckUpgrade)
				definitions.POST("/:id/upgrade", deps.DefinitionHandler.ApplyUpgrade)
				definitions.POST("/:id/charts", deps.DefinitionHandler.AddChartConfig)
				definitions.PUT("/:id/charts/:chartId", deps.DefinitionHandler.UpdateChartConfig)
				definitions.DELETE("/:id/charts/:chartId", deps.DefinitionHandler.DeleteChartConfig)
			}
		}

		// Stack Instances + Value Overrides + Values Export
		if deps.InstanceHandler != nil {
			instances := authed.Group("/stack-instances")
			{
				instances.GET("", deps.InstanceHandler.ListInstances)
				instances.POST("", deps.InstanceHandler.CreateInstance)
				instances.GET("/compare", deps.InstanceHandler.CompareInstances)
				instances.GET("/recent", deps.InstanceHandler.GetRecentInstances)
				instances.GET("/:id", deps.InstanceHandler.GetInstance)
				instances.PUT("/:id", deps.InstanceHandler.UpdateInstance)
				instances.DELETE("/:id", deps.InstanceHandler.DeleteInstance)
				instances.POST("/:id/clone", deps.InstanceHandler.CloneInstance)
				instances.GET("/:id/values", deps.InstanceHandler.ExportAllValues)
				instances.GET("/:id/values/:chartId", deps.InstanceHandler.ExportChartValues)
				instances.GET("/:id/overrides", deps.InstanceHandler.GetOverrides)
				instances.PUT("/:id/overrides/:chartId", deps.InstanceHandler.SetOverride)
				instances.POST("/:id/deploy", deps.InstanceHandler.DeployInstance)
				instances.GET("/:id/deploy-preview", deps.InstanceHandler.DeployPreview)
				instances.POST("/:id/stop", deps.InstanceHandler.StopInstance)
				instances.POST("/:id/clean", deps.InstanceHandler.CleanInstance)
				instances.POST("/:id/extend", deps.InstanceHandler.ExtendTTL)
				instances.GET("/:id/deploy-log", deps.InstanceHandler.GetDeployLog)
				instances.GET("/:id/status", deps.InstanceHandler.GetInstanceStatus)

				// Bulk operations (DevOps/Admin only)
				bulk := instances.Group("/bulk")
				bulk.Use(devops)
				{
					bulk.POST("/deploy", deps.InstanceHandler.BulkDeploy)
					bulk.POST("/stop", deps.InstanceHandler.BulkStop)
					bulk.POST("/clean", deps.InstanceHandler.BulkClean)
					bulk.POST("/delete", deps.InstanceHandler.BulkDelete)
				}

				// Per-chart branch overrides
				if deps.BranchOverrideHandler != nil {
					instances.GET("/:id/branches", deps.BranchOverrideHandler.ListBranchOverrides)
					instances.PUT("/:id/branches/:chartId", deps.BranchOverrideHandler.SetBranchOverride)
					instances.DELETE("/:id/branches/:chartId", deps.BranchOverrideHandler.DeleteBranchOverride)
				}

				// Per-instance quota overrides
				if deps.InstanceQuotaOverrideHandler != nil {
					instances.GET("/:id/quota-overrides", deps.InstanceQuotaOverrideHandler.GetQuotaOverride)
					instances.PUT("/:id/quota-overrides", deps.InstanceQuotaOverrideHandler.SetQuotaOverride)
					instances.DELETE("/:id/quota-overrides", deps.InstanceQuotaOverrideHandler.DeleteQuotaOverride)
				}
			}
		}

		// Git
		if deps.GitHandler != nil {
			git := authed.Group("/git")
			{
				git.GET("/branches", deps.GitHandler.ListBranches)
				git.GET("/validate-branch", deps.GitHandler.ValidateBranch)
				git.GET("/providers", deps.GitHandler.GetProviders)
			}
		}

		// Audit Logs
		if deps.AuditLogHandler != nil {
			auditLogs := authed.Group("/audit-logs")
			{
				auditLogs.GET("/export", middleware.RequireAdmin(), deps.AuditLogHandler.ExportAuditLogs)
				auditLogs.GET("", deps.AuditLogHandler.ListAuditLogs)
			}
		}

		// User management (admin only)
		if deps.UserHandler != nil {
			admin := middleware.RequireAdmin()
			users := authed.Group("/users")
			{
				users.GET("", admin, deps.UserHandler.ListUsers)
				users.DELETE("/:id", admin, deps.UserHandler.DeleteUser)
			}
		}

		// API key management (admin or own user)
		if deps.APIKeyHandler != nil {
			if deps.UserHandler == nil {
				// Ensure the parent /users group exists even without UserHandler.
				_ = authed.Group("/users")
			}
			userKeys := authed.Group("/users/:id/api-keys")
			{
				userKeys.GET("", deps.APIKeyHandler.ListAPIKeys)
				userKeys.POST("", deps.APIKeyHandler.CreateAPIKey)
				userKeys.DELETE("/:keyId", deps.APIKeyHandler.DeleteAPIKey)
			}
		}

		// Admin endpoints (admin only)
		if deps.AdminHandler != nil {
			admin := middleware.RequireAdmin()
			adminGroup := authed.Group("/admin")
			adminGroup.Use(admin)
			{
				adminGroup.GET("/orphaned-namespaces", deps.AdminHandler.ListOrphanedNamespaces)
				adminGroup.DELETE("/orphaned-namespaces/:namespace", deps.AdminHandler.DeleteOrphanedNamespace)
			}

			// Cleanup policies — nested in admin group
			if deps.CleanupPolicyHandler != nil {
				cleanupPolicies := adminGroup.Group("/cleanup-policies")
				{
					cleanupPolicies.GET("", deps.CleanupPolicyHandler.ListCleanupPolicies)
					cleanupPolicies.POST("", deps.CleanupPolicyHandler.CreateCleanupPolicy)
					cleanupPolicies.PUT("/:id", deps.CleanupPolicyHandler.UpdateCleanupPolicy)
					cleanupPolicies.DELETE("/:id", deps.CleanupPolicyHandler.DeleteCleanupPolicy)
					cleanupPolicies.POST("/:id/run", deps.CleanupPolicyHandler.RunCleanupPolicy)
				}
			}
		}

		// Cluster management
		clusterHandler := deps.ClusterHandler
		// Only construct a fallback ClusterHandler when all required dependencies are present.
		if clusterHandler == nil && deps.ClusterRepo != nil && deps.InstanceRepo != nil {
			clusterHandler = handlers.NewClusterHandler(deps.ClusterRepo, deps.Registry, deps.InstanceRepo)
		}
		if clusterHandler != nil {
			admin := middleware.RequireAdmin()
			clusters := authed.Group("/clusters")
			{
				// GET routes are intentionally open to all authenticated users
				// so the instance-creation form can populate the cluster dropdown.
				clusters.GET("", clusterHandler.ListClusters)
				clusters.GET("/:id", clusterHandler.GetCluster)
				clusters.POST("", admin, clusterHandler.CreateCluster)
				clusters.PUT("/:id", admin, clusterHandler.UpdateCluster)
				clusters.DELETE("/:id", admin, clusterHandler.DeleteCluster)
				clusters.POST("/:id/test", admin, clusterHandler.TestClusterConnection)
				clusters.POST("/:id/default", admin, clusterHandler.SetDefaultCluster)

				// Cluster health dashboard — requires DevOps or Admin role.
				clusters.GET("/:id/health/summary", devops, clusterHandler.GetClusterHealthSummary)
				clusters.GET("/:id/health/nodes", devops, clusterHandler.GetClusterNodes)
				clusters.GET("/:id/namespaces", devops, clusterHandler.GetClusterNamespaces)

				// Resource quotas
				clusters.GET("/:id/quotas", devops, clusterHandler.GetQuotas)
				clusters.PUT("/:id/quotas", admin, clusterHandler.UpdateQuotas)
				clusters.DELETE("/:id/quotas", admin, clusterHandler.DeleteQuotas)
				clusters.GET("/:id/utilization", devops, clusterHandler.GetUtilization)
			}

			// Shared values (Phase 6.4)
			if deps.SharedValuesHandler != nil {
				sharedValues := clusters.Group("/:id/shared-values")
				sharedValues.Use(admin)
				{
					sharedValues.GET("", deps.SharedValuesHandler.ListSharedValues)
					sharedValues.POST("", deps.SharedValuesHandler.CreateSharedValues)
					sharedValues.PUT("/:valueId", deps.SharedValuesHandler.UpdateSharedValues)
					sharedValues.DELETE("/:valueId", deps.SharedValuesHandler.DeleteSharedValues)
				}
			}
		}

		// Analytics
		if deps.AnalyticsHandler != nil {
			analytics := authed.Group("/analytics")
			analytics.Use(devops)
			{
				analytics.GET("/overview", deps.AnalyticsHandler.GetOverview)
				analytics.GET("/templates", deps.AnalyticsHandler.GetTemplateStats)
				analytics.GET("/users", middleware.RequireAdmin(), deps.AnalyticsHandler.GetUserStats)
			}
		}

		// Notifications
		if deps.NotificationHandler != nil {
			notifications := authed.Group("/notifications")
			{
				notifications.GET("", deps.NotificationHandler.List)
				notifications.GET("/count", deps.NotificationHandler.CountUnread)
				notifications.POST("/:id/read", deps.NotificationHandler.MarkAsRead)
				notifications.POST("/read-all", deps.NotificationHandler.MarkAllAsRead)
				notifications.GET("/preferences", deps.NotificationHandler.GetPreferences)
				notifications.PUT("/preferences", deps.NotificationHandler.UpdatePreferences)
			}
		}

		// Favorites
		if deps.FavoriteHandler != nil {
			favorites := authed.Group("/favorites")
			{
				favorites.GET("", deps.FavoriteHandler.ListFavorites)
				favorites.POST("", deps.FavoriteHandler.AddFavorite)
				favorites.DELETE("/:entityType/:entityId", deps.FavoriteHandler.RemoveFavorite)
				favorites.GET("/check", deps.FavoriteHandler.CheckFavorite)
			}
		}
	}

	return &RateLimiters{API: rateLimiter, Login: loginRateLimiter}
}

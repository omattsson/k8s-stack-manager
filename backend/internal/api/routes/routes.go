package routes

import (
	"backend/internal/api/handlers"
	"backend/internal/api/middleware"
	"backend/internal/cluster"
	"backend/internal/config"
	"backend/internal/health"
	"backend/internal/models"
	"backend/internal/websocket"
	"time"

	"github.com/gin-gonic/gin"
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

	// Cluster management.
	ClusterHandler *handlers.ClusterHandler
	ClusterRepo    models.ClusterRepository
	Registry       *cluster.Registry
	InstanceRepo   models.StackInstanceRepository

	// Repos needed by combined JWT+API-key auth middleware.
	UserRepo   models.UserRepository
	APIKeyRepo models.APIKeyRepository
}

// SetupRoutes configures all the routes for our application.
// healthChecker is injected from main so the readiness endpoint reflects real dependency health.
// Returns the rate limiter so the caller can stop it during shutdown.
func SetupRoutes(router *gin.Engine, deps Deps) *handlers.RateLimiter {
	cfg := deps.Config

	// Add middleware
	router.Use(middleware.RequestID())
	router.Use(middleware.Logger())
	router.Use(middleware.Recovery())
	router.Use(middleware.CORS(cfg.CORS.AllowedOrigins))
	router.Use(middleware.MaxBodySize(1 << 20)) // 1 MB default

	// WebSocket endpoint (top-level, outside rate limiter — connections are long-lived)
	wsHandler := handlers.NewWebSocketHandler(deps.Hub, cfg.CORS.AllowedOrigins)
	router.GET("/ws", wsHandler.HandleWebSocket)

	// Health check endpoints
	healthGroup := router.Group("/health")
	{
		healthGroup.GET("/live", handlers.LivenessHandler(deps.HealthChecker))
		healthGroup.GET("/ready", handlers.ReadinessHandler(deps.HealthChecker))
		healthGroup.GET("", handlers.HealthCheck) // Keep the original health check for backward compatibility
	}

	// Rate limiter for API routes
	rateLimiter := handlers.NewRateLimiter(int(cfg.Server.RateLimit), time.Minute)

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
		})

		// Auth — login is public; register requires auth (admin or self-reg checked inside handler).
		auth := v1.Group("/auth")
		{
			auth.POST("/login", deps.AuthHandler.Login)
			registerHandlers := []gin.HandlerFunc{authMW}
			if deps.AuditLogger != nil {
				registerHandlers = append(registerHandlers, middleware.NewAuditMiddleware(deps.AuditLogger))
			}
			registerHandlers = append(registerHandlers, deps.AuthHandler.Register)
			auth.POST("/register", registerHandlers...)
			auth.GET("/me", authMW, deps.AuthHandler.GetCurrentUser)
		}

		// All remaining Phase 1 routes require authentication.
		authed := v1.Group("")
		authed.Use(authMW)

		// Attach audit middleware if an audit logger is provided.
		if deps.AuditLogger != nil {
			authed.Use(middleware.NewAuditMiddleware(deps.AuditLogger))
		}

		devops := middleware.RequireDevOps()

		// Stack Templates
		if deps.TemplateHandler != nil {
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
				templates.POST("/:id/charts", devops, deps.TemplateHandler.AddTemplateChart)
				templates.PUT("/:id/charts/:chartId", devops, deps.TemplateHandler.UpdateTemplateChart)
				templates.DELETE("/:id/charts/:chartId", devops, deps.TemplateHandler.DeleteTemplateChart)
			}
		}

		// Stack Definitions
		if deps.DefinitionHandler != nil {
			definitions := authed.Group("/stack-definitions")
			{
				definitions.GET("", deps.DefinitionHandler.ListDefinitions)
				definitions.POST("", deps.DefinitionHandler.CreateDefinition)
				definitions.GET("/:id", deps.DefinitionHandler.GetDefinition)
				definitions.PUT("/:id", deps.DefinitionHandler.UpdateDefinition)
				definitions.DELETE("/:id", deps.DefinitionHandler.DeleteDefinition)
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
				instances.GET("/:id", deps.InstanceHandler.GetInstance)
				instances.PUT("/:id", deps.InstanceHandler.UpdateInstance)
				instances.DELETE("/:id", deps.InstanceHandler.DeleteInstance)
				instances.POST("/:id/clone", deps.InstanceHandler.CloneInstance)
				instances.GET("/:id/values", deps.InstanceHandler.ExportAllValues)
				instances.GET("/:id/values/:chartId", deps.InstanceHandler.ExportChartValues)
				instances.GET("/:id/overrides", deps.InstanceHandler.GetOverrides)
				instances.PUT("/:id/overrides/:chartId", deps.InstanceHandler.SetOverride)
				instances.POST("/:id/deploy", deps.InstanceHandler.DeployInstance)
				instances.POST("/:id/stop", deps.InstanceHandler.StopInstance)
				instances.POST("/:id/clean", deps.InstanceHandler.CleanInstance)
				instances.GET("/:id/deploy-log", deps.InstanceHandler.GetDeployLog)
				instances.GET("/:id/status", deps.InstanceHandler.GetInstanceStatus)
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
			authed.GET("/audit-logs", deps.AuditLogHandler.ListAuditLogs)
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
		}

		// Cluster management
		clusterHandler := deps.ClusterHandler
		if clusterHandler == nil && deps.ClusterRepo != nil {
			clusterHandler = handlers.NewClusterHandler(deps.ClusterRepo, deps.Registry, deps.InstanceRepo)
		}
		if clusterHandler != nil {
			admin := middleware.RequireAdmin()
			clusters := authed.Group("/clusters")
			{
				clusters.GET("", clusterHandler.ListClusters)
				clusters.GET("/:id", clusterHandler.GetCluster)
				clusters.POST("", admin, clusterHandler.CreateCluster)
				clusters.PUT("/:id", admin, clusterHandler.UpdateCluster)
				clusters.DELETE("/:id", admin, clusterHandler.DeleteCluster)
				clusters.POST("/:id/test", admin, clusterHandler.TestClusterConnection)
				clusters.POST("/:id/default", admin, clusterHandler.SetDefaultCluster)
			}
		}
	}

	return rateLimiter
}

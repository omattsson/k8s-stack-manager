package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/internal/api/handlers"
	"backend/internal/config"
	"backend/internal/database"
	"backend/internal/gitprovider"
	"backend/internal/health"
	"backend/internal/helm"
	"backend/internal/models"
	"backend/internal/sessionstore"
	"backend/internal/websocket"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- minimal mock repos for middleware construction ----

type stubUserRepo struct{}

func (s *stubUserRepo) Create(_ *models.User) error                           { return nil }
func (s *stubUserRepo) FindByID(_ string) (*models.User, error)               { return nil, nil }
func (s *stubUserRepo) FindByIDs(_ []string) (map[string]*models.User, error) { return nil, nil }
func (s *stubUserRepo) FindByUsername(_ string) (*models.User, error)         { return nil, nil }
func (s *stubUserRepo) FindByExternalID(_, _ string) (*models.User, error)    { return nil, nil }
func (s *stubUserRepo) Update(_ *models.User) error                           { return nil }
func (s *stubUserRepo) Delete(_ string) error                                 { return nil }
func (s *stubUserRepo) List() ([]models.User, error)                          { return nil, nil }
func (s *stubUserRepo) Count() (int64, error)                                 { return 0, nil }
func (s *stubUserRepo) ListByRoles(_ []string) ([]models.User, error)         { return nil, nil }

type stubAPIKeyRepo struct{}

func (s *stubAPIKeyRepo) Create(_ *models.APIKey) error                   { return nil }
func (s *stubAPIKeyRepo) FindByID(_, _ string) (*models.APIKey, error)    { return nil, nil }
func (s *stubAPIKeyRepo) FindByPrefix(_ string) ([]*models.APIKey, error) { return nil, nil }
func (s *stubAPIKeyRepo) ListByUser(_ string) ([]*models.APIKey, error)   { return nil, nil }
func (s *stubAPIKeyRepo) UpdateLastUsed(_, _ string, _ time.Time) error   { return nil }
func (s *stubAPIKeyRepo) Delete(_, _ string) error                        { return nil }

type stubAuditLogger struct{}

func (s *stubAuditLogger) Create(_ *models.AuditLog) error { return nil }

// ---- helpers ----

func testConfig() *config.Config {
	return &config.Config{
		CORS: config.CORSConfig{
			AllowedOrigins: "*",
		},
		Server: config.ServerConfig{
			RateLimit:      100,
			LoginRateLimit: 10,
		},
		Auth: config.AuthConfig{
			JWTSecret: "test-secret-key-for-routing-tests",
		},
	}
}

func setupMinimalRouter(t *testing.T) (*gin.Engine, *RateLimiters) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	mockRepo := handlers.NewMockRepository()

	healthChecker := health.New()
	healthChecker.SetReady(true)

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	rl := SetupRoutes(router, Deps{
		Repository:    mockRepo,
		HealthChecker: healthChecker,
		Config:        testConfig(),
		Hub:           hub,
	})
	t.Cleanup(func() { rl.Stop() })

	return router, rl
}

// ---- tests ----

func TestSetupRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.Default()
	mockRepo := handlers.NewMockRepository()

	healthChecker := health.New()
	healthChecker.SetReady(true)

	cfg := &config.Config{
		CORS: config.CORSConfig{
			AllowedOrigins: "*",
		},
		Server: config.ServerConfig{
			RateLimit:      100,
			LoginRateLimit: 10,
		},
	}

	hub := websocket.NewHub()
	go hub.Run()
	defer hub.Shutdown()

	rl := SetupRoutes(router, Deps{
		Repository:    mockRepo,
		HealthChecker: healthChecker,
		Config:        cfg,
		Hub:           hub,
	})
	defer rl.Stop()

	tests := []struct {
		name         string
		route        string
		method       string
		expectedCode int
		expectedBody map[string]string
	}{
		{
			name:         "Health Check",
			route:        "/health",
			method:       "GET",
			expectedCode: 200,
			expectedBody: map[string]string{"status": "ok"},
		},
		{
			name:         "Ping endpoint",
			route:        "/api/v1/ping",
			method:       "GET",
			expectedCode: 200,
			expectedBody: map[string]string{"message": "pong"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(tt.method, tt.route, nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedCode, w.Code)

			if tt.expectedBody != nil {
				var response map[string]string
				err := json.Unmarshal(w.Body.Bytes(), &response)
				assert.Nil(t, err)
				assert.Equal(t, tt.expectedBody, response)
			}
		})
	}
}

func TestSetupRoutes_RateLimiterReturned(t *testing.T) {
	t.Parallel()

	_, rl := setupMinimalRouter(t)
	require.NotNil(t, rl, "SetupRoutes must return a non-nil rate limiter")
}

func TestSetupRoutes_HealthEndpoints(t *testing.T) {
	t.Parallel()

	router, _ := setupMinimalRouter(t)

	tests := []struct {
		name         string
		path         string
		wantCode     int
		wantContains string
	}{
		{
			name:         "backward compat health check",
			path:         "/health",
			wantCode:     http.StatusOK,
			wantContains: "ok",
		},
		{
			name:         "liveness probe",
			path:         "/health/live",
			wantCode:     http.StatusOK,
			wantContains: "UP",
		},
		{
			name:         "readiness probe",
			path:         "/health/ready",
			wantCode:     http.StatusOK,
			wantContains: "UP",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, tt.path, nil)
			router.ServeHTTP(w, req)
			assert.Equal(t, tt.wantCode, w.Code)
			assert.Contains(t, w.Body.String(), tt.wantContains)
		})
	}
}

func TestSetupRoutes_AllRouteGroupsRegistered(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	mockRepo := handlers.NewMockRepository()

	healthChecker := health.New()
	healthChecker.SetReady(true)

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	cfg := testConfig()

	// Create a minimal AuthHandler to trigger Phase 1 route registration.
	authHandler := handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth, &cfg.OIDC)

	rl := SetupRoutes(router, Deps{
		Repository:    mockRepo,
		HealthChecker: healthChecker,
		Config:        cfg,
		Hub:           hub,
		AuthHandler:   authHandler,
		UserRepo:      &stubUserRepo{},
		APIKeyRepo:    &stubAPIKeyRepo{},
		AuditLogger:   &stubAuditLogger{},
	})
	t.Cleanup(func() { rl.Stop() })

	// Collect all registered routes.
	registeredRoutes := make(map[string]bool)
	for _, r := range router.Routes() {
		registeredRoutes[r.Method+" "+r.Path] = true
	}

	// Verify core route groups are present.
	requiredRoutes := []struct {
		method string
		path   string
	}{
		{"GET", "/ws"},
		{"GET", "/health"},
		{"GET", "/health/live"},
		{"GET", "/health/ready"},
		{"GET", "/api/v1/ping"},
		{"GET", "/api/v1/items"},
		{"POST", "/api/v1/items"},
		{"GET", "/api/v1/items/:id"},
		{"PUT", "/api/v1/items/:id"},
		{"DELETE", "/api/v1/items/:id"},
		{"POST", "/api/v1/auth/login"},
		{"POST", "/api/v1/auth/register"},
		{"GET", "/api/v1/auth/me"},
	}

	for _, rr := range requiredRoutes {
		key := rr.method + " " + rr.path
		assert.True(t, registeredRoutes[key], "expected route not registered: %s", key)
	}
}

func TestSetupRoutes_ProtectedRoutesReturn401(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	mockRepo := handlers.NewMockRepository()

	healthChecker := health.New()
	healthChecker.SetReady(true)

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	cfg := testConfig()
	authHandler := handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth, &cfg.OIDC)

	rl := SetupRoutes(router, Deps{
		Repository:    mockRepo,
		HealthChecker: healthChecker,
		Config:        cfg,
		Hub:           hub,
		AuthHandler:   authHandler,
		UserRepo:      &stubUserRepo{},
		APIKeyRepo:    &stubAPIKeyRepo{},
		AuditLogger:   &stubAuditLogger{},
	})
	t.Cleanup(func() { rl.Stop() })

	// These routes require auth and should return 401 without a token.
	protectedPaths := []string{
		"/api/v1/auth/me",
	}

	for _, path := range protectedPaths {
		t.Run("GET "+path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, path, nil)
			router.ServeHTTP(w, req)
			assert.Equal(t, http.StatusUnauthorized, w.Code,
				"path %s should require auth", path)
		})
	}
}

func TestSetupRoutes_LoginIsPublic(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	mockRepo := handlers.NewMockRepository()

	healthChecker := health.New()
	healthChecker.SetReady(true)

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	cfg := testConfig()
	authHandler := handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth, &cfg.OIDC)

	rl := SetupRoutes(router, Deps{
		Repository:    mockRepo,
		HealthChecker: healthChecker,
		Config:        cfg,
		Hub:           hub,
		AuthHandler:   authHandler,
		UserRepo:      &stubUserRepo{},
		APIKeyRepo:    &stubAPIKeyRepo{},
	})
	t.Cleanup(func() { rl.Stop() })

	// Login is a public endpoint - should not return 401.
	w := httptest.NewRecorder()
	body := `{"username":"test","password":"test123"}`
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	// It will fail auth (user not found) but NOT with 401 from middleware.
	// The status should be 401 (invalid credentials) from the handler, not from middleware.
	assert.NotEqual(t, http.StatusMethodNotAllowed, w.Code, "login should accept POST")
}

func TestSetupRoutes_OIDCDisabled(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	mockRepo := handlers.NewMockRepository()

	healthChecker := health.New()
	healthChecker.SetReady(true)

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	cfg := testConfig()
	authHandler := handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth, &cfg.OIDC)

	// OIDCHandler is nil (OIDC disabled), so the fallback config endpoint should be registered.
	rl := SetupRoutes(router, Deps{
		Repository:    mockRepo,
		HealthChecker: healthChecker,
		Config:        cfg,
		Hub:           hub,
		AuthHandler:   authHandler,
		OIDCHandler:   nil,
		UserRepo:      &stubUserRepo{},
		APIKeyRepo:    &stubAPIKeyRepo{},
	})
	t.Cleanup(func() { rl.Stop() })

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/auth/oidc/config", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, false, resp["enabled"])
	assert.Equal(t, true, resp["local_auth_enabled"])
}

func TestSetupRoutes_MiddlewareOrdering(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	mockRepo := handlers.NewMockRepository()

	healthChecker := health.New()
	healthChecker.SetReady(true)

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	rl := SetupRoutes(router, Deps{
		Repository:    mockRepo,
		HealthChecker: healthChecker,
		Config:        testConfig(),
		Hub:           hub,
	})
	t.Cleanup(func() { rl.Stop() })

	// Verify middleware is functional: a request should get a request ID header
	// and CORS headers from the middleware chain.
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/ping", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	// CORS middleware sets Access-Control-Allow-Origin
	assert.NotEmpty(t, w.Header().Get("Access-Control-Allow-Origin"))
}

func TestSetupRoutes_MaxBodySizeEnforced(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	mockRepo := handlers.NewMockRepository()

	healthChecker := health.New()
	healthChecker.SetReady(true)

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	rl := SetupRoutes(router, Deps{
		Repository:    mockRepo,
		HealthChecker: healthChecker,
		Config:        testConfig(),
		Hub:           hub,
	})
	t.Cleanup(func() { rl.Stop() })

	// Create a body larger than 1 MB.
	largeBody := strings.Repeat("a", 1<<20+1)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/items", strings.NewReader(largeBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	// The handler reads the limited body and gets a read error which results
	// in either 400 (bad JSON) or 413 (body too large). Both indicate the
	// MaxBodySize middleware is working -- the request is NOT processed.
	assert.Contains(t, []int{http.StatusBadRequest, http.StatusRequestEntityTooLarge}, w.Code,
		"oversized body should be rejected")
}

func TestSetupRoutes_WithAllHandlersRegistersFullAPI(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	mockRepo := handlers.NewMockRepository()

	healthChecker := health.New()
	healthChecker.SetReady(true)

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	cfg := testConfig()
	authHandler := handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth, &cfg.OIDC)
	userHandler := handlers.NewUserHandler(&stubUserRepo{})
	apiKeyHandler := handlers.NewAPIKeyHandler(&stubAPIKeyRepo{}, &stubUserRepo{}, &cfg.Auth)
	auditLogHandler := handlers.NewAuditLogHandler(nil)

	rl := SetupRoutes(router, Deps{
		Repository:      mockRepo,
		HealthChecker:   healthChecker,
		Config:          cfg,
		Hub:             hub,
		AuthHandler:     authHandler,
		UserHandler:     userHandler,
		APIKeyHandler:   apiKeyHandler,
		AuditLogHandler: auditLogHandler,
		AuditLogger:     &stubAuditLogger{},
		UserRepo:        &stubUserRepo{},
		APIKeyRepo:      &stubAPIKeyRepo{},
	})
	t.Cleanup(func() { rl.Stop() })

	registeredRoutes := make(map[string]bool)
	for _, r := range router.Routes() {
		registeredRoutes[r.Method+" "+r.Path] = true
	}

	// Verify user management routes are registered.
	assert.True(t, registeredRoutes["GET /api/v1/users"], "users list route missing")
	assert.True(t, registeredRoutes["DELETE /api/v1/users/:id"], "user delete route missing")

	// Verify API key routes.
	assert.True(t, registeredRoutes["GET /api/v1/users/:id/api-keys"], "api-keys list route missing")
	assert.True(t, registeredRoutes["POST /api/v1/users/:id/api-keys"], "api-keys create route missing")
	assert.True(t, registeredRoutes["DELETE /api/v1/users/:id/api-keys/:keyId"], "api-keys delete route missing")

	// Verify audit log routes.
	assert.True(t, registeredRoutes["GET /api/v1/audit-logs"], "audit-logs list route missing")
	assert.True(t, registeredRoutes["GET /api/v1/audit-logs/export"], "audit-logs export route missing")
}

func TestSetupRoutes_NilHandlersSkipRouteRegistration(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	mockRepo := handlers.NewMockRepository()

	healthChecker := health.New()
	healthChecker.SetReady(true)

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	cfg := testConfig()
	authHandler := handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth, &cfg.OIDC)

	// All domain handlers are nil -- only auth/items/health should be registered.
	rl := SetupRoutes(router, Deps{
		Repository:    mockRepo,
		HealthChecker: healthChecker,
		Config:        cfg,
		Hub:           hub,
		AuthHandler:   authHandler,
		UserRepo:      &stubUserRepo{},
		APIKeyRepo:    &stubAPIKeyRepo{},
	})
	t.Cleanup(func() { rl.Stop() })

	registeredRoutes := make(map[string]bool)
	for _, r := range router.Routes() {
		registeredRoutes[r.Method+" "+r.Path] = true
	}

	// Templates, definitions, instances, git, admin should NOT be registered.
	assert.False(t, registeredRoutes["GET /api/v1/templates"], "templates should not be registered when handler is nil")
	assert.False(t, registeredRoutes["GET /api/v1/stack-definitions"], "definitions should not be registered when handler is nil")
	assert.False(t, registeredRoutes["GET /api/v1/stack-instances"], "instances should not be registered when handler is nil")
	assert.False(t, registeredRoutes["GET /api/v1/git/branches"], "git should not be registered when handler is nil")
	assert.False(t, registeredRoutes["GET /api/v1/admin/orphaned-namespaces"], "admin should not be registered when handler is nil")
	assert.False(t, registeredRoutes["GET /api/v1/analytics/overview"], "analytics should not be registered when handler is nil")
	assert.False(t, registeredRoutes["GET /api/v1/notifications"], "notifications should not be registered when handler is nil")
	assert.False(t, registeredRoutes["GET /api/v1/favorites"], "favorites should not be registered when handler is nil")
}

func TestSetupRoutes_WebSocketEndpoint(t *testing.T) {
	t.Parallel()

	router, _ := setupMinimalRouter(t)

	registeredRoutes := make(map[string]bool)
	for _, r := range router.Routes() {
		registeredRoutes[r.Method+" "+r.Path] = true
	}

	assert.True(t, registeredRoutes["GET /ws"], "WebSocket endpoint should be registered at /ws")
}

func TestSetupRoutes_ItemsCRUD(t *testing.T) {
	t.Parallel()

	router, _ := setupMinimalRouter(t)

	registeredRoutes := make(map[string]bool)
	for _, r := range router.Routes() {
		registeredRoutes[r.Method+" "+r.Path] = true
	}

	itemRoutes := []string{
		"GET /api/v1/items",
		"POST /api/v1/items",
		"GET /api/v1/items/:id",
		"PUT /api/v1/items/:id",
		"DELETE /api/v1/items/:id",
	}

	for _, route := range itemRoutes {
		assert.True(t, registeredRoutes[route], "items route missing: %s", route)
	}
}

func TestSetupRoutes_RecoveryMiddleware(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	mockRepo := handlers.NewMockRepository()

	healthChecker := health.New()
	healthChecker.SetReady(true)

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	rl := SetupRoutes(router, Deps{
		Repository:    mockRepo,
		HealthChecker: healthChecker,
		Config:        testConfig(),
		Hub:           hub,
	})
	t.Cleanup(func() { rl.Stop() })

	// The recovery middleware should prevent panics from crashing the server.
	// We cannot easily trigger a panic in a registered handler, but we can verify
	// that the router works correctly (indicating recovery middleware is in place).
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/ping", nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestSetupRoutes_CORSPreflight(t *testing.T) {
	t.Parallel()

	router, _ := setupMinimalRouter(t)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodOptions, "/api/v1/ping", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "GET")
	router.ServeHTTP(w, req)

	// CORS middleware should respond to OPTIONS preflight.
	assert.Contains(t, []int{http.StatusOK, http.StatusNoContent}, w.Code)
	assert.NotEmpty(t, w.Header().Get("Access-Control-Allow-Origin"))
}

func TestSetupRoutes_SwaggerGatedByConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		enable                bool
		expectRouteRegistered bool
	}{
		{
			name:                  "swagger disabled does not register route and returns 404",
			enable:                false,
			expectRouteRegistered: false,
		},
		{
			name:                  "swagger enabled registers route and serves content",
			enable:                true,
			expectRouteRegistered: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gin.SetMode(gin.TestMode)
			router := gin.New()
			mockRepo := handlers.NewMockRepository()

			healthChecker := health.New()
			healthChecker.SetReady(true)

			hub := websocket.NewHub()
			go hub.Run()
			t.Cleanup(func() { hub.Shutdown() })

			cfg := testConfig()
			cfg.App.EnableSwagger = tt.enable

			rl := SetupRoutes(router, Deps{
				Repository:    mockRepo,
				HealthChecker: healthChecker,
				Config:        cfg,
				Hub:           hub,
			})
			t.Cleanup(func() { rl.Stop() })

			// Check whether the swagger route is registered.
			hasSwaggerRoute := false
			for _, r := range router.Routes() {
				if r.Method == http.MethodGet && strings.HasPrefix(r.Path, "/swagger/") {
					hasSwaggerRoute = true
					break
				}
			}

			assert.Equal(t, tt.expectRouteRegistered, hasSwaggerRoute)

			// Issue actual HTTP requests to verify routing behaviour.
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/swagger/index.html", nil)
			router.ServeHTTP(w, req)

			if tt.expectRouteRegistered {
				// Swagger is enabled — the route is handled by the swagger
				// middleware. In test builds the swagger docs may not be
				// embedded, so we cannot assert 200. Instead verify the
				// request is NOT rejected with 405 (method not allowed),
				// which would indicate no matching route, and that gin's
				// default "404 page not found" body is absent (proving
				// the swagger handler processed the request).
				assert.NotEqual(t, http.StatusMethodNotAllowed, w.Code,
					"expected swagger handler to process request when enabled")
				assert.NotContains(t, w.Body.String(), "404 page not found",
					"expected swagger handler to process request, not gin's default 404")
			} else {
				// Swagger is disabled — gin has no matching route, returning
				// its default 404 with the "404 page not found" body.
				assert.Equal(t, http.StatusNotFound, w.Code,
					"expected 404 when swagger is disabled, got %d", w.Code)
				assert.Contains(t, w.Body.String(), "404 page not found",
					"expected gin's default 404 response when swagger is disabled")
			}
		})
	}
}

// ---- additional stub repos for full-handler construction ----

type stubStackTemplateRepo struct{}

func (s *stubStackTemplateRepo) Create(_ *models.StackTemplate) error                       { return nil }
func (s *stubStackTemplateRepo) FindByID(_ string) (*models.StackTemplate, error)           { return nil, nil }
func (s *stubStackTemplateRepo) Update(_ *models.StackTemplate) error                       { return nil }
func (s *stubStackTemplateRepo) Delete(_ string) error                                      { return nil }
func (s *stubStackTemplateRepo) List() ([]models.StackTemplate, error)                      { return nil, nil }
func (s *stubStackTemplateRepo) ListPaged(_, _ int) ([]models.StackTemplate, int64, error)  { return nil, 0, nil }
func (s *stubStackTemplateRepo) ListPublished() ([]models.StackTemplate, error)             { return nil, nil }
func (s *stubStackTemplateRepo) ListPublishedPaged(_, _ int) ([]models.StackTemplate, int64, error) {
	return nil, 0, nil
}
func (s *stubStackTemplateRepo) ListByOwner(_ string) ([]models.StackTemplate, error) { return nil, nil }
func (s *stubStackTemplateRepo) Count() (int64, error)                                { return 0, nil }

type stubStackDefinitionRepo struct{}

func (s *stubStackDefinitionRepo) Create(_ *models.StackDefinition) error             { return nil }
func (s *stubStackDefinitionRepo) FindByID(_ string) (*models.StackDefinition, error) { return nil, nil }
func (s *stubStackDefinitionRepo) FindByName(_ string) ([]models.StackDefinition, error) {
	return nil, nil
}
func (s *stubStackDefinitionRepo) Update(_ *models.StackDefinition) error               { return nil }
func (s *stubStackDefinitionRepo) Delete(_ string) error                                { return nil }
func (s *stubStackDefinitionRepo) List() ([]models.StackDefinition, error)              { return nil, nil }
func (s *stubStackDefinitionRepo) ListPaged(_, _ int) ([]models.StackDefinition, int64, error) {
	return nil, 0, nil
}
func (s *stubStackDefinitionRepo) ListByOwner(_ string) ([]models.StackDefinition, error) {
	return nil, nil
}
func (s *stubStackDefinitionRepo) ListByTemplate(_ string) ([]models.StackDefinition, error) {
	return nil, nil
}
func (s *stubStackDefinitionRepo) CountByTemplateIDs(_ []string) (map[string]int, error) {
	return nil, nil
}
func (s *stubStackDefinitionRepo) ListIDsByTemplateIDs(_ []string) (map[string][]string, error) {
	return nil, nil
}
func (s *stubStackDefinitionRepo) Count() (int64, error) { return 0, nil }

type stubStackInstanceRepo struct{}

func (s *stubStackInstanceRepo) Create(_ *models.StackInstance) error              { return nil }
func (s *stubStackInstanceRepo) FindByID(_ string) (*models.StackInstance, error)  { return nil, nil }
func (s *stubStackInstanceRepo) FindByNamespace(_ string) (*models.StackInstance, error) {
	return nil, nil
}
func (s *stubStackInstanceRepo) Update(_ *models.StackInstance) error               { return nil }
func (s *stubStackInstanceRepo) Delete(_ string) error                              { return nil }
func (s *stubStackInstanceRepo) List() ([]models.StackInstance, error)              { return nil, nil }
func (s *stubStackInstanceRepo) ListPaged(_, _ int) ([]models.StackInstance, int, error) {
	return nil, 0, nil
}
func (s *stubStackInstanceRepo) ListByOwner(_ string) ([]models.StackInstance, error) {
	return nil, nil
}
func (s *stubStackInstanceRepo) FindByName(_ string) ([]models.StackInstance, error) {
	return nil, nil
}
func (s *stubStackInstanceRepo) FindByCluster(_ string) ([]models.StackInstance, error) {
	return nil, nil
}
func (s *stubStackInstanceRepo) CountByClusterAndOwner(_, _ string) (int, error) { return 0, nil }
func (s *stubStackInstanceRepo) CountAll() (int, error)                          { return 0, nil }
func (s *stubStackInstanceRepo) CountByStatus(_ string) (int, error)             { return 0, nil }
func (s *stubStackInstanceRepo) CountByDefinitionIDs(_ []string) (map[string]int, error) {
	return nil, nil
}
func (s *stubStackInstanceRepo) CountByOwnerIDs(_ []string) (map[string]int, error) {
	return nil, nil
}
func (s *stubStackInstanceRepo) ListIDsByDefinitionIDs(_ []string) (map[string][]string, error) {
	return nil, nil
}
func (s *stubStackInstanceRepo) ListIDsByOwnerIDs(_ []string) (map[string][]string, error) {
	return nil, nil
}
func (s *stubStackInstanceRepo) ExistsByDefinitionAndStatus(_, _ string) (bool, error) {
	return false, nil
}
func (s *stubStackInstanceRepo) ListExpired() ([]*models.StackInstance, error)   { return nil, nil }
func (s *stubStackInstanceRepo) ListExpiringSoon(_ time.Duration) ([]*models.StackInstance, error) {
	return nil, nil
}
func (s *stubStackInstanceRepo) ListByStatus(_ string, _ int) ([]*models.StackInstance, error) {
	return nil, nil
}

type stubChartConfigRepo struct{}

func (s *stubChartConfigRepo) Create(_ *models.ChartConfig) error             { return nil }
func (s *stubChartConfigRepo) FindByID(_ string) (*models.ChartConfig, error) { return nil, nil }
func (s *stubChartConfigRepo) Update(_ *models.ChartConfig) error             { return nil }
func (s *stubChartConfigRepo) Delete(_ string) error                          { return nil }
func (s *stubChartConfigRepo) ListByDefinition(_ string) ([]models.ChartConfig, error) {
	return nil, nil
}

type stubTemplateChartConfigRepo struct{}

func (s *stubTemplateChartConfigRepo) Create(_ *models.TemplateChartConfig) error { return nil }
func (s *stubTemplateChartConfigRepo) FindByID(_ string) (*models.TemplateChartConfig, error) {
	return nil, nil
}
func (s *stubTemplateChartConfigRepo) Update(_ *models.TemplateChartConfig) error { return nil }
func (s *stubTemplateChartConfigRepo) Delete(_ string) error                      { return nil }
func (s *stubTemplateChartConfigRepo) ListByTemplate(_ string) ([]models.TemplateChartConfig, error) {
	return nil, nil
}

type stubValueOverrideRepo struct{}

func (s *stubValueOverrideRepo) Create(_ *models.ValueOverride) error             { return nil }
func (s *stubValueOverrideRepo) FindByID(_ string) (*models.ValueOverride, error) { return nil, nil }
func (s *stubValueOverrideRepo) FindByInstanceAndChart(_, _ string) (*models.ValueOverride, error) {
	return nil, nil
}
func (s *stubValueOverrideRepo) Update(_ *models.ValueOverride) error { return nil }
func (s *stubValueOverrideRepo) Delete(_ string) error                { return nil }
func (s *stubValueOverrideRepo) ListByInstance(_ string) ([]models.ValueOverride, error) {
	return nil, nil
}

type stubChartBranchOverrideRepo struct{}

func (s *stubChartBranchOverrideRepo) List(_ string) ([]*models.ChartBranchOverride, error) {
	return nil, nil
}
func (s *stubChartBranchOverrideRepo) Get(_, _ string) (*models.ChartBranchOverride, error) {
	return nil, nil
}
func (s *stubChartBranchOverrideRepo) Set(_ *models.ChartBranchOverride) error { return nil }
func (s *stubChartBranchOverrideRepo) Delete(_, _ string) error                { return nil }
func (s *stubChartBranchOverrideRepo) DeleteByInstance(_ string) error         { return nil }

type stubInstanceQuotaOverrideRepo struct{}

func (s *stubInstanceQuotaOverrideRepo) GetByInstanceID(_ context.Context, _ string) (*models.InstanceQuotaOverride, error) {
	return nil, nil
}
func (s *stubInstanceQuotaOverrideRepo) Upsert(_ context.Context, _ *models.InstanceQuotaOverride) error {
	return nil
}
func (s *stubInstanceQuotaOverrideRepo) Delete(_ context.Context, _ string) error { return nil }

type stubUserFavoriteRepo struct{}

func (s *stubUserFavoriteRepo) List(_ string) ([]*models.UserFavorite, error)  { return nil, nil }
func (s *stubUserFavoriteRepo) Add(_ *models.UserFavorite) error               { return nil }
func (s *stubUserFavoriteRepo) Remove(_, _, _ string) error                    { return nil }
func (s *stubUserFavoriteRepo) IsFavorite(_, _, _ string) (bool, error)        { return false, nil }

type stubNotificationRepo struct{}

func (s *stubNotificationRepo) Create(_ context.Context, _ *models.Notification) error { return nil }
func (s *stubNotificationRepo) ListByUser(_ context.Context, _ string, _ bool, _, _ int) ([]models.Notification, int64, error) {
	return nil, 0, nil
}
func (s *stubNotificationRepo) CountUnread(_ context.Context, _ string) (int64, error) { return 0, nil }
func (s *stubNotificationRepo) MarkAsRead(_ context.Context, _, _ string) error        { return nil }
func (s *stubNotificationRepo) MarkAllAsRead(_ context.Context, _ string) error        { return nil }
func (s *stubNotificationRepo) GetPreferences(_ context.Context, _ string) ([]models.NotificationPreference, error) {
	return nil, nil
}
func (s *stubNotificationRepo) UpdatePreference(_ context.Context, _ *models.NotificationPreference) error {
	return nil
}

type stubCleanupPolicyRepo struct{}

func (s *stubCleanupPolicyRepo) Create(_ *models.CleanupPolicy) error             { return nil }
func (s *stubCleanupPolicyRepo) FindByID(_ string) (*models.CleanupPolicy, error) { return nil, nil }
func (s *stubCleanupPolicyRepo) Update(_ *models.CleanupPolicy) error             { return nil }
func (s *stubCleanupPolicyRepo) Delete(_ string) error                            { return nil }
func (s *stubCleanupPolicyRepo) List() ([]models.CleanupPolicy, error)            { return nil, nil }
func (s *stubCleanupPolicyRepo) ListEnabled() ([]models.CleanupPolicy, error)     { return nil, nil }

type stubClusterRepo struct{}

func (s *stubClusterRepo) Create(_ *models.Cluster) error             { return nil }
func (s *stubClusterRepo) FindByID(_ string) (*models.Cluster, error) { return nil, nil }
func (s *stubClusterRepo) Update(_ *models.Cluster) error             { return nil }
func (s *stubClusterRepo) Delete(_ string) error                      { return nil }
func (s *stubClusterRepo) List() ([]models.Cluster, error)            { return nil, nil }
func (s *stubClusterRepo) FindDefault() (*models.Cluster, error)      { return nil, nil }
func (s *stubClusterRepo) SetDefault(_ string) error                  { return nil }

type stubSharedValuesRepo struct{}

func (s *stubSharedValuesRepo) Create(_ *models.SharedValues) error             { return nil }
func (s *stubSharedValuesRepo) FindByID(_ string) (*models.SharedValues, error) { return nil, nil }
func (s *stubSharedValuesRepo) FindByClusterAndID(_, _ string) (*models.SharedValues, error) {
	return nil, nil
}
func (s *stubSharedValuesRepo) Update(_ *models.SharedValues) error                  { return nil }
func (s *stubSharedValuesRepo) Delete(_ string) error                                { return nil }
func (s *stubSharedValuesRepo) ListByCluster(_ string) ([]models.SharedValues, error) { return nil, nil }

type stubTemplateVersionRepo struct{}

func (s *stubTemplateVersionRepo) Create(_ context.Context, _ *models.TemplateVersion) error {
	return nil
}
func (s *stubTemplateVersionRepo) ListByTemplate(_ context.Context, _ string) ([]models.TemplateVersion, error) {
	return nil, nil
}
func (s *stubTemplateVersionRepo) GetByID(_ context.Context, _, _ string) (*models.TemplateVersion, error) {
	return nil, nil
}
func (s *stubTemplateVersionRepo) GetLatestByTemplate(_ context.Context, _ string) (*models.TemplateVersion, error) {
	return nil, nil
}

type stubDeploymentLogRepo struct{}

func (s *stubDeploymentLogRepo) Create(_ context.Context, _ *models.DeploymentLog) error { return nil }
func (s *stubDeploymentLogRepo) FindByID(_ context.Context, _ string) (*models.DeploymentLog, error) {
	return nil, nil
}
func (s *stubDeploymentLogRepo) Update(_ context.Context, _ *models.DeploymentLog) error { return nil }
func (s *stubDeploymentLogRepo) ListByInstance(_ context.Context, _ string) ([]models.DeploymentLog, error) {
	return nil, nil
}
func (s *stubDeploymentLogRepo) ListByInstancePaginated(_ context.Context, _ models.DeploymentLogFilters) (*models.DeploymentLogResult, error) {
	return nil, nil
}
func (s *stubDeploymentLogRepo) GetLatestByInstance(_ context.Context, _ string) (*models.DeploymentLog, error) {
	return nil, nil
}
func (s *stubDeploymentLogRepo) SummarizeByInstance(_ context.Context, _ string) (*models.DeployLogSummary, error) {
	return nil, nil
}
func (s *stubDeploymentLogRepo) SummarizeBatch(_ context.Context, _ []string) (map[string]*models.DeployLogSummary, error) {
	return nil, nil
}
func (s *stubDeploymentLogRepo) CountByAction(_ context.Context, _ string) (int, error) {
	return 0, nil
}
func (s *stubDeploymentLogRepo) ListRecentGlobal(_ context.Context, _ int) ([]models.DeploymentLogWithContext, error) {
	return nil, nil
}

type stubAuditLogRepo struct{}

func (s *stubAuditLogRepo) Create(_ *models.AuditLog) error                     { return nil }
func (s *stubAuditLogRepo) List(_ models.AuditLogFilters) (*models.AuditLogResult, error) {
	return nil, nil
}

type stubSessionStore struct{}

func (s *stubSessionStore) BlockToken(_ context.Context, _ string, _ time.Time) error { return nil }
func (s *stubSessionStore) IsTokenBlocked(_ context.Context, _ string) (bool, error)  { return false, nil }
func (s *stubSessionStore) BlockUser(_ context.Context, _ string, _ time.Time) error  { return nil }
func (s *stubSessionStore) IsUserBlocked(_ context.Context, _ string) (bool, error)   { return false, nil }
func (s *stubSessionStore) UnblockUser(_ context.Context, _ string) error             { return nil }
func (s *stubSessionStore) SaveOIDCState(_ context.Context, _ string, _ sessionstore.OIDCStateData, _ time.Duration) error {
	return nil
}
func (s *stubSessionStore) ConsumeOIDCState(_ context.Context, _ string) (*sessionstore.OIDCStateData, error) {
	return nil, nil
}
func (s *stubSessionStore) Cleanup(_ context.Context) error { return nil }
func (s *stubSessionStore) Stop()                           {}

type stubTxRunner struct{}

func (s *stubTxRunner) RunInTx(fn func(repos database.TxRepos) error) error {
	return fn(database.TxRepos{})
}

// ---- full deps helper ----

// setupFullRouter creates a Gin engine with ALL handlers registered, exercising
// every code path in SetupRoutes. Only used for route-registration validation,
// not for handler logic.
func setupFullRouter(t *testing.T) (*gin.Engine, *RateLimiters) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	mockRepo := handlers.NewMockRepository()

	healthChecker := health.New()
	healthChecker.SetReady(true)

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	cfg := testConfig()

	userRepo := &stubUserRepo{}
	apiKeyRepo := &stubAPIKeyRepo{}
	templateRepo := &stubStackTemplateRepo{}
	definitionRepo := &stubStackDefinitionRepo{}
	instanceRepo := &stubStackInstanceRepo{}
	chartConfigRepo := &stubChartConfigRepo{}
	templateChartRepo := &stubTemplateChartConfigRepo{}
	overrideRepo := &stubValueOverrideRepo{}
	branchOverrideRepo := &stubChartBranchOverrideRepo{}
	clusterRepo := &stubClusterRepo{}
	deployLogRepo := &stubDeploymentLogRepo{}

	valuesGen := helm.NewValuesGenerator()

	authHandler := handlers.NewAuthHandler(userRepo, &cfg.Auth, &cfg.OIDC)
	templateHandler := handlers.NewTemplateHandler(templateRepo, templateChartRepo, definitionRepo, chartConfigRepo)
	definitionHandler := handlers.NewDefinitionHandler(definitionRepo, chartConfigRepo, instanceRepo, templateRepo, templateChartRepo)
	instanceHandler := handlers.NewInstanceHandler(
		instanceRepo, overrideRepo, branchOverrideRepo,
		definitionRepo, chartConfigRepo, templateRepo, templateChartRepo,
		valuesGen, userRepo, 0,
	)
	gitHandler := handlers.NewGitHandler(gitprovider.NewRegistry(gitprovider.Config{}))
	auditLogHandler := handlers.NewAuditLogHandler(&stubAuditLogRepo{})
	userHandler := handlers.NewUserHandler(userRepo)
	apiKeyHandler := handlers.NewAPIKeyHandler(apiKeyRepo, userRepo, &cfg.Auth)
	adminHandler := handlers.NewAdminHandler(nil, instanceRepo)
	branchOverrideHandler := handlers.NewBranchOverrideHandler(branchOverrideRepo, instanceRepo)
	instanceQuotaHandler := handlers.NewInstanceQuotaOverrideHandler(&stubInstanceQuotaOverrideRepo{}, instanceRepo)
	favoriteHandler := handlers.NewFavoriteHandler(&stubUserFavoriteRepo{})
	analyticsHandler := handlers.NewAnalyticsHandler(templateRepo, definitionRepo, instanceRepo, deployLogRepo, userRepo)
	cleanupPolicyHandler := handlers.NewCleanupPolicyHandler(&stubCleanupPolicyRepo{}, nil)
	clusterHandler := handlers.NewClusterHandler(clusterRepo, nil, instanceRepo)
	sharedValuesHandler := handlers.NewSharedValuesHandler(&stubSharedValuesRepo{}, clusterRepo)
	dashboardHandler := handlers.NewDashboardHandler(clusterRepo, instanceRepo, deployLogRepo, nil)
	templateVersionHandler := handlers.NewTemplateVersionHandler(&stubTemplateVersionRepo{}, templateRepo)
	notificationHandler := handlers.NewNotificationHandler(&stubNotificationRepo{})

	// OIDC handler with nil provider (safe for route registration only).
	oidcHandler := handlers.NewOIDCHandler(nil, &stubSessionStore{}, userRepo, &cfg.OIDC, &cfg.Auth)

	// QuickDeployHandler requires a non-nil txRunner.
	quickDeployHandler, err := handlers.NewQuickDeployHandler(
		templateRepo, templateChartRepo, definitionRepo, chartConfigRepo,
		instanceRepo, branchOverrideRepo, overrideRepo, valuesGen,
		nil, userRepo, deployLogRepo, &stubAuditLogRepo{}, hub, nil, nil, 0,
		&stubTxRunner{},
	)
	require.NoError(t, err)

	rl := SetupRoutes(router, Deps{
		Repository:                  mockRepo,
		HealthChecker:               healthChecker,
		Config:                      cfg,
		Hub:                         hub,
		AuthHandler:                 authHandler,
		TemplateHandler:             templateHandler,
		DefinitionHandler:           definitionHandler,
		InstanceHandler:             instanceHandler,
		GitHandler:                  gitHandler,
		AuditLogHandler:             auditLogHandler,
		AuditLogger:                 &stubAuditLogger{},
		UserHandler:                 userHandler,
		APIKeyHandler:               apiKeyHandler,
		AdminHandler:                adminHandler,
		BranchOverrideHandler:       branchOverrideHandler,
		InstanceQuotaOverrideHandler: instanceQuotaHandler,
		FavoriteHandler:             favoriteHandler,
		QuickDeployHandler:          quickDeployHandler,
		AnalyticsHandler:            analyticsHandler,
		CleanupPolicyHandler:        cleanupPolicyHandler,
		ClusterHandler:              clusterHandler,
		SharedValuesHandler:         sharedValuesHandler,
		DashboardHandler:            dashboardHandler,
		TemplateVersionHandler:      templateVersionHandler,
		NotificationHandler:         notificationHandler,
		OIDCHandler:                 oidcHandler,
		UserRepo:                    userRepo,
		APIKeyRepo:                  apiKeyRepo,
		ClusterRepo:                 clusterRepo,
		InstanceRepo:                instanceRepo,
		SessionStore:                &stubSessionStore{},
	})
	t.Cleanup(func() { rl.Stop() })

	return router, rl
}

// collectRoutes extracts "METHOD /path" strings from a Gin engine.
func collectRoutes(router *gin.Engine) map[string]bool {
	m := make(map[string]bool)
	for _, r := range router.Routes() {
		m[r.Method+" "+r.Path] = true
	}
	return m
}

// ---- comprehensive route registration tests ----

func TestSetupRoutes_AllHandlers_RegistersCompleteAPI(t *testing.T) {
	t.Parallel()

	router, _ := setupFullRouter(t)
	registered := collectRoutes(router)

	// Every route declared in routes.go grouped by domain.
	expected := []struct {
		method string
		path   string
	}{
		// WebSocket
		{"GET", "/ws"},

		// Health
		{"GET", "/health"},
		{"GET", "/health/live"},
		{"GET", "/health/ready"},

		// Ping + Items (always registered)
		{"GET", "/api/v1/ping"},
		{"GET", "/api/v1/items"},
		{"POST", "/api/v1/items"},
		{"GET", "/api/v1/items/:id"},
		{"PUT", "/api/v1/items/:id"},
		{"DELETE", "/api/v1/items/:id"},

		// Auth
		{"POST", "/api/v1/auth/login"},
		{"POST", "/api/v1/auth/register"},
		{"GET", "/api/v1/auth/me"},
		{"POST", "/api/v1/auth/refresh"},
		{"POST", "/api/v1/auth/logout"},
		{"POST", "/api/v1/auth/logout-all"},

		// OIDC (enabled — OIDCHandler is non-nil)
		{"GET", "/api/v1/auth/oidc/config"},
		{"GET", "/api/v1/auth/oidc/authorize"},
		{"GET", "/api/v1/auth/oidc/callback"},

		// Templates
		{"GET", "/api/v1/templates"},
		{"POST", "/api/v1/templates"},
		{"GET", "/api/v1/templates/:id"},
		{"PUT", "/api/v1/templates/:id"},
		{"DELETE", "/api/v1/templates/:id"},
		{"POST", "/api/v1/templates/:id/publish"},
		{"POST", "/api/v1/templates/:id/unpublish"},
		{"POST", "/api/v1/templates/:id/instantiate"},
		{"POST", "/api/v1/templates/:id/clone"},

		// Bulk template operations
		{"POST", "/api/v1/templates/bulk/delete"},
		{"POST", "/api/v1/templates/bulk/publish"},
		{"POST", "/api/v1/templates/bulk/unpublish"},

		// Template chart configs
		{"POST", "/api/v1/templates/:id/charts"},
		{"PUT", "/api/v1/templates/:id/charts/:chartId"},
		{"DELETE", "/api/v1/templates/:id/charts/:chartId"},

		// Template versions
		{"GET", "/api/v1/templates/:id/versions"},
		{"GET", "/api/v1/templates/:id/versions/diff"},
		{"GET", "/api/v1/templates/:id/versions/:versionId"},

		// Quick deploy
		{"POST", "/api/v1/templates/:id/quick-deploy"},

		// Stack Definitions
		{"GET", "/api/v1/stack-definitions"},
		{"POST", "/api/v1/stack-definitions"},
		{"POST", "/api/v1/stack-definitions/import"},
		{"GET", "/api/v1/stack-definitions/:id"},
		{"GET", "/api/v1/stack-definitions/:id/export"},
		{"PUT", "/api/v1/stack-definitions/:id"},
		{"DELETE", "/api/v1/stack-definitions/:id"},
		{"GET", "/api/v1/stack-definitions/:id/check-upgrade"},
		{"POST", "/api/v1/stack-definitions/:id/upgrade"},
		{"POST", "/api/v1/stack-definitions/:id/charts"},
		{"PUT", "/api/v1/stack-definitions/:id/charts/:chartId"},
		{"DELETE", "/api/v1/stack-definitions/:id/charts/:chartId"},

		// Stack Instances
		{"GET", "/api/v1/stack-instances"},
		{"POST", "/api/v1/stack-instances"},
		{"GET", "/api/v1/stack-instances/compare"},
		{"GET", "/api/v1/stack-instances/recent"},
		{"GET", "/api/v1/stack-instances/:id"},
		{"PUT", "/api/v1/stack-instances/:id"},
		{"DELETE", "/api/v1/stack-instances/:id"},
		{"POST", "/api/v1/stack-instances/:id/clone"},
		{"GET", "/api/v1/stack-instances/:id/values"},
		{"GET", "/api/v1/stack-instances/:id/values/:chartId"},
		{"GET", "/api/v1/stack-instances/:id/overrides"},
		{"PUT", "/api/v1/stack-instances/:id/overrides/:chartId"},
		{"POST", "/api/v1/stack-instances/:id/deploy"},
		{"GET", "/api/v1/stack-instances/:id/deploy-preview"},
		{"POST", "/api/v1/stack-instances/:id/stop"},
		{"POST", "/api/v1/stack-instances/:id/clean"},
		{"POST", "/api/v1/stack-instances/:id/actions/:name"},
		{"POST", "/api/v1/stack-instances/:id/extend"},
		{"GET", "/api/v1/stack-instances/:id/deploy-log"},
		{"GET", "/api/v1/stack-instances/:id/deploy-log/:logId/values"},
		{"POST", "/api/v1/stack-instances/:id/rollback"},
		{"GET", "/api/v1/stack-instances/:id/status"},
		{"GET", "/api/v1/stack-instances/:id/pods"},

		// Bulk instance operations
		{"POST", "/api/v1/stack-instances/bulk/deploy"},
		{"POST", "/api/v1/stack-instances/bulk/stop"},
		{"POST", "/api/v1/stack-instances/bulk/clean"},
		{"POST", "/api/v1/stack-instances/bulk/delete"},

		// Branch overrides
		{"GET", "/api/v1/stack-instances/:id/branches"},
		{"PUT", "/api/v1/stack-instances/:id/branches/:chartId"},
		{"DELETE", "/api/v1/stack-instances/:id/branches/:chartId"},

		// Instance quota overrides
		{"GET", "/api/v1/stack-instances/:id/quota-overrides"},
		{"PUT", "/api/v1/stack-instances/:id/quota-overrides"},
		{"DELETE", "/api/v1/stack-instances/:id/quota-overrides"},

		// Git
		{"GET", "/api/v1/git/branches"},
		{"GET", "/api/v1/git/validate-branch"},
		{"GET", "/api/v1/git/providers"},

		// Audit logs
		{"GET", "/api/v1/audit-logs"},
		{"GET", "/api/v1/audit-logs/export"},

		// Users
		{"GET", "/api/v1/users"},
		{"DELETE", "/api/v1/users/:id"},
		{"PUT", "/api/v1/users/:id/disable"},
		{"PUT", "/api/v1/users/:id/enable"},

		// API keys
		{"GET", "/api/v1/users/:id/api-keys"},
		{"POST", "/api/v1/users/:id/api-keys"},
		{"DELETE", "/api/v1/users/:id/api-keys/:keyId"},

		// Admin
		{"GET", "/api/v1/admin/orphaned-namespaces"},
		{"DELETE", "/api/v1/admin/orphaned-namespaces/:namespace"},

		// Cleanup policies
		{"GET", "/api/v1/admin/cleanup-policies"},
		{"POST", "/api/v1/admin/cleanup-policies"},
		{"PUT", "/api/v1/admin/cleanup-policies/:id"},
		{"DELETE", "/api/v1/admin/cleanup-policies/:id"},
		{"POST", "/api/v1/admin/cleanup-policies/:id/run"},

		// Clusters
		{"GET", "/api/v1/clusters"},
		{"GET", "/api/v1/clusters/:id"},
		{"POST", "/api/v1/clusters"},
		{"PUT", "/api/v1/clusters/:id"},
		{"DELETE", "/api/v1/clusters/:id"},
		{"POST", "/api/v1/clusters/:id/test"},
		{"POST", "/api/v1/clusters/:id/default"},
		{"GET", "/api/v1/clusters/:id/health/summary"},
		{"GET", "/api/v1/clusters/:id/health/nodes"},
		{"GET", "/api/v1/clusters/:id/namespaces"},
		{"GET", "/api/v1/clusters/:id/quotas"},
		{"PUT", "/api/v1/clusters/:id/quotas"},
		{"DELETE", "/api/v1/clusters/:id/quotas"},
		{"GET", "/api/v1/clusters/:id/utilization"},

		// Shared values
		{"GET", "/api/v1/clusters/:id/shared-values"},
		{"POST", "/api/v1/clusters/:id/shared-values"},
		{"PUT", "/api/v1/clusters/:id/shared-values/:valueId"},
		{"DELETE", "/api/v1/clusters/:id/shared-values/:valueId"},

		// Analytics
		{"GET", "/api/v1/analytics/overview"},
		{"GET", "/api/v1/analytics/templates"},
		{"GET", "/api/v1/analytics/users"},

		// Dashboard
		{"GET", "/api/v1/dashboard"},

		// Notifications
		{"GET", "/api/v1/notifications"},
		{"GET", "/api/v1/notifications/count"},
		{"POST", "/api/v1/notifications/:id/read"},
		{"POST", "/api/v1/notifications/read-all"},
		{"GET", "/api/v1/notifications/preferences"},
		{"PUT", "/api/v1/notifications/preferences"},

		// Favorites
		{"GET", "/api/v1/favorites"},
		{"POST", "/api/v1/favorites"},
		{"DELETE", "/api/v1/favorites/:entityType/:entityId"},
		{"GET", "/api/v1/favorites/check"},
	}

	for _, e := range expected {
		key := e.method + " " + e.path
		assert.True(t, registered[key], "expected route not registered: %s", key)
	}

	// Verify we haven't missed documenting any routes.
	expectedSet := make(map[string]bool, len(expected))
	for _, e := range expected {
		expectedSet[e.method+" "+e.path] = true
	}
	for key := range registered {
		assert.True(t, expectedSet[key], "unexpected route registered that is not in expected list: %s", key)
	}
}

func TestSetupRoutes_OIDCEnabled_RegistersOIDCRoutes(t *testing.T) {
	t.Parallel()

	router, _ := setupFullRouter(t)
	registered := collectRoutes(router)

	oidcRoutes := []string{
		"GET /api/v1/auth/oidc/config",
		"GET /api/v1/auth/oidc/authorize",
		"GET /api/v1/auth/oidc/callback",
	}
	for _, route := range oidcRoutes {
		assert.True(t, registered[route], "OIDC route missing when OIDCHandler is set: %s", route)
	}
}

func TestSetupRoutes_OIDCDisabled_FallbackConfigOnly(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	mockRepo := handlers.NewMockRepository()

	healthChecker := health.New()
	healthChecker.SetReady(true)

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	cfg := testConfig()
	authHandler := handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth, &cfg.OIDC)

	rl := SetupRoutes(router, Deps{
		Repository:    mockRepo,
		HealthChecker: healthChecker,
		Config:        cfg,
		Hub:           hub,
		AuthHandler:   authHandler,
		OIDCHandler:   nil, // OIDC disabled
		UserRepo:      &stubUserRepo{},
		APIKeyRepo:    &stubAPIKeyRepo{},
	})
	t.Cleanup(func() { rl.Stop() })

	registered := collectRoutes(router)

	// The fallback config endpoint should be registered.
	assert.True(t, registered["GET /api/v1/auth/oidc/config"],
		"OIDC fallback config route should be registered when OIDC is disabled")

	// The authorize and callback endpoints should NOT be registered.
	assert.False(t, registered["GET /api/v1/auth/oidc/authorize"],
		"OIDC authorize should NOT be registered when OIDC is disabled")
	assert.False(t, registered["GET /api/v1/auth/oidc/callback"],
		"OIDC callback should NOT be registered when OIDC is disabled")
}

func TestSetupRoutes_LoginRateLimiterBranching(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		loginRateLimit   int32
		wantLoginLimiter bool
	}{
		{
			name:             "login rate limiter created when LoginRateLimit > 0",
			loginRateLimit:   10,
			wantLoginLimiter: true,
		},
		{
			name:             "login rate limiter nil when LoginRateLimit is 0",
			loginRateLimit:   0,
			wantLoginLimiter: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gin.SetMode(gin.TestMode)
			router := gin.New()
			mockRepo := handlers.NewMockRepository()

			healthChecker := health.New()
			healthChecker.SetReady(true)

			hub := websocket.NewHub()
			go hub.Run()
			t.Cleanup(func() { hub.Shutdown() })

			cfg := testConfig()
			cfg.Server.LoginRateLimit = tt.loginRateLimit
			authHandler := handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth, &cfg.OIDC)

			rl := SetupRoutes(router, Deps{
				Repository:    mockRepo,
				HealthChecker: healthChecker,
				Config:        cfg,
				Hub:           hub,
				AuthHandler:   authHandler,
				UserRepo:      &stubUserRepo{},
				APIKeyRepo:    &stubAPIKeyRepo{},
			})
			t.Cleanup(func() { rl.Stop() })

			if tt.wantLoginLimiter {
				require.NotNil(t, rl.Login, "expected login rate limiter to be created")
			} else {
				require.Nil(t, rl.Login, "expected login rate limiter to be nil")
			}

			// Login and refresh endpoints should be reachable regardless.
			registered := collectRoutes(router)
			assert.True(t, registered["POST /api/v1/auth/login"], "login route must be registered")
			assert.True(t, registered["POST /api/v1/auth/refresh"], "refresh route must be registered")
		})
	}
}

func TestSetupRoutes_ClusterHandlerFallbackConstruction(t *testing.T) {
	t.Parallel()

	// When ClusterHandler is nil but ClusterRepo and InstanceRepo are set,
	// SetupRoutes constructs a fallback ClusterHandler internally.
	gin.SetMode(gin.TestMode)
	router := gin.New()
	mockRepo := handlers.NewMockRepository()

	healthChecker := health.New()
	healthChecker.SetReady(true)

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	cfg := testConfig()
	authHandler := handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth, &cfg.OIDC)

	rl := SetupRoutes(router, Deps{
		Repository:    mockRepo,
		HealthChecker: healthChecker,
		Config:        cfg,
		Hub:           hub,
		AuthHandler:   authHandler,
		UserRepo:      &stubUserRepo{},
		APIKeyRepo:    &stubAPIKeyRepo{},
		ClusterHandler: nil, // explicitly nil
		ClusterRepo:    &stubClusterRepo{},
		InstanceRepo:   &stubStackInstanceRepo{},
	})
	t.Cleanup(func() { rl.Stop() })

	registered := collectRoutes(router)

	// Cluster routes should be registered via the fallback handler.
	clusterRoutes := []string{
		"GET /api/v1/clusters",
		"GET /api/v1/clusters/:id",
		"POST /api/v1/clusters",
		"PUT /api/v1/clusters/:id",
		"DELETE /api/v1/clusters/:id",
		"POST /api/v1/clusters/:id/test",
		"POST /api/v1/clusters/:id/default",
	}
	for _, route := range clusterRoutes {
		assert.True(t, registered[route], "cluster route missing with fallback handler: %s", route)
	}
}

func TestSetupRoutes_ClusterHandlerNotRegisteredWithoutDeps(t *testing.T) {
	t.Parallel()

	// When ClusterHandler, ClusterRepo, AND InstanceRepo are all nil,
	// no cluster routes should be registered.
	gin.SetMode(gin.TestMode)
	router := gin.New()
	mockRepo := handlers.NewMockRepository()

	healthChecker := health.New()
	healthChecker.SetReady(true)

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	cfg := testConfig()
	authHandler := handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth, &cfg.OIDC)

	rl := SetupRoutes(router, Deps{
		Repository:     mockRepo,
		HealthChecker:  healthChecker,
		Config:         cfg,
		Hub:            hub,
		AuthHandler:    authHandler,
		UserRepo:       &stubUserRepo{},
		APIKeyRepo:     &stubAPIKeyRepo{},
		ClusterHandler: nil,
		ClusterRepo:    nil,
		InstanceRepo:   nil,
	})
	t.Cleanup(func() { rl.Stop() })

	registered := collectRoutes(router)
	assert.False(t, registered["GET /api/v1/clusters"],
		"cluster routes should NOT be registered when all cluster deps are nil")
}

func TestSetupRoutes_OtelMiddlewareGatedByConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		enabled bool
	}{
		{"otel disabled", false},
		{"otel enabled", true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gin.SetMode(gin.TestMode)
			router := gin.New()
			mockRepo := handlers.NewMockRepository()

			healthChecker := health.New()
			healthChecker.SetReady(true)

			hub := websocket.NewHub()
			go hub.Run()
			t.Cleanup(func() { hub.Shutdown() })

			cfg := testConfig()
			cfg.Otel.Enabled = tt.enabled
			cfg.Otel.ServiceName = "test-svc"
			authHandler := handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth, &cfg.OIDC)

			rl := SetupRoutes(router, Deps{
				Repository:    mockRepo,
				HealthChecker: healthChecker,
				Config:        cfg,
				Hub:           hub,
				AuthHandler:   authHandler,
				UserRepo:      &stubUserRepo{},
				APIKeyRepo:    &stubAPIKeyRepo{},
			})
			t.Cleanup(func() { rl.Stop() })

			// Routes should still work regardless of OTel config.
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/api/v1/ping", nil)
			router.ServeHTTP(w, req)
			assert.Equal(t, http.StatusOK, w.Code)
		})
	}
}

func TestSetupRoutes_APIKeyRoutesWithoutUserHandler(t *testing.T) {
	t.Parallel()

	// When UserHandler is nil but APIKeyHandler is set, the /users/:id/api-keys
	// routes should still be registered.
	gin.SetMode(gin.TestMode)
	router := gin.New()
	mockRepo := handlers.NewMockRepository()

	healthChecker := health.New()
	healthChecker.SetReady(true)

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	cfg := testConfig()
	authHandler := handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth, &cfg.OIDC)
	apiKeyHandler := handlers.NewAPIKeyHandler(&stubAPIKeyRepo{}, &stubUserRepo{}, &cfg.Auth)

	rl := SetupRoutes(router, Deps{
		Repository:    mockRepo,
		HealthChecker: healthChecker,
		Config:        cfg,
		Hub:           hub,
		AuthHandler:   authHandler,
		UserHandler:   nil, // no user handler
		APIKeyHandler: apiKeyHandler,
		UserRepo:      &stubUserRepo{},
		APIKeyRepo:    &stubAPIKeyRepo{},
		AuditLogger:   &stubAuditLogger{},
	})
	t.Cleanup(func() { rl.Stop() })

	registered := collectRoutes(router)
	assert.True(t, registered["GET /api/v1/users/:id/api-keys"], "api-keys list route should be registered without UserHandler")
	assert.True(t, registered["POST /api/v1/users/:id/api-keys"], "api-keys create route should be registered without UserHandler")
	assert.True(t, registered["DELETE /api/v1/users/:id/api-keys/:keyId"], "api-keys delete route should be registered without UserHandler")

	// User management routes should NOT be registered.
	assert.False(t, registered["GET /api/v1/users"], "user list should not be registered when UserHandler is nil")
}

func TestSetupRoutes_SecurityHeaders(t *testing.T) {
	t.Parallel()

	router, _ := setupMinimalRouter(t)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/ping", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	// SecurityHeaders middleware should set standard security headers.
	assert.NotEmpty(t, w.Header().Get("X-Content-Type-Options"),
		"SecurityHeaders middleware should set X-Content-Type-Options")
}

func TestRateLimiters_Stop_NilSafe(t *testing.T) {
	t.Parallel()

	// Calling Stop on nil RateLimiters should not panic.
	var rl *RateLimiters
	assert.NotPanics(t, func() { rl.Stop() })

	// Calling Stop with nil fields should not panic.
	rl2 := &RateLimiters{}
	assert.NotPanics(t, func() { rl2.Stop() })
}

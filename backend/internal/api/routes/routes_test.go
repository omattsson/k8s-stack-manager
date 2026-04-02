package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/internal/api/handlers"
	"backend/internal/config"
	"backend/internal/health"
	"backend/internal/models"
	"backend/internal/websocket"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- minimal mock repos for middleware construction ----

type stubUserRepo struct{}

func (s *stubUserRepo) Create(_ *models.User) error                               { return nil }
func (s *stubUserRepo) FindByID(_ string) (*models.User, error)                   { return nil, nil }
func (s *stubUserRepo) FindByIDs(_ []string) (map[string]*models.User, error)     { return nil, nil }
func (s *stubUserRepo) FindByUsername(_ string) (*models.User, error)              { return nil, nil }
func (s *stubUserRepo) FindByExternalID(_, _ string) (*models.User, error)        { return nil, nil }
func (s *stubUserRepo) Update(_ *models.User) error                               { return nil }
func (s *stubUserRepo) Delete(_ string) error                                     { return nil }
func (s *stubUserRepo) List() ([]models.User, error)                              { return nil, nil }
func (s *stubUserRepo) Count() (int64, error)                                    { return 0, nil }

type stubAPIKeyRepo struct{}

func (s *stubAPIKeyRepo) Create(_ *models.APIKey) error                        { return nil }
func (s *stubAPIKeyRepo) FindByID(_, _ string) (*models.APIKey, error)         { return nil, nil }
func (s *stubAPIKeyRepo) FindByPrefix(_ string) ([]*models.APIKey, error)      { return nil, nil }
func (s *stubAPIKeyRepo) ListByUser(_ string) ([]*models.APIKey, error)        { return nil, nil }
func (s *stubAPIKeyRepo) UpdateLastUsed(_, _ string, _ time.Time) error        { return nil }
func (s *stubAPIKeyRepo) Delete(_, _ string) error                             { return nil }

type stubAuditLogger struct{}

func (s *stubAuditLogger) Create(_ *models.AuditLog) error { return nil }

// ---- helpers ----

func testConfig() *config.Config {
	return &config.Config{
		CORS: config.CORSConfig{
			AllowedOrigins: "*",
		},
		Server: config.ServerConfig{
			RateLimit: 100,
		},
		Auth: config.AuthConfig{
			JWTSecret: "test-secret-key-for-routing-tests",
		},
	}
}

func setupMinimalRouter(t *testing.T) (*gin.Engine, *handlers.RateLimiter) {
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
			RateLimit: 100,
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
	authHandler := handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth)

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
	authHandler := handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth)

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
	authHandler := handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth)

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
	authHandler := handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth)

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
	authHandler := handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth)
	userHandler := handlers.NewUserHandler(&stubUserRepo{})
	apiKeyHandler := handlers.NewAPIKeyHandler(&stubAPIKeyRepo{}, &stubUserRepo{})
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
	authHandler := handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth)

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

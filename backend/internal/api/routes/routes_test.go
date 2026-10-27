package routes

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/internal/api/handlers"
	"backend/internal/api/middleware"
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

func (s *stubUserRepo) Create(_ context.Context, _ *models.User) error             { return nil }
func (s *stubUserRepo) FindByID(_ context.Context, _ string) (*models.User, error) { return nil, nil }
func (s *stubUserRepo) FindByIDs(_ context.Context, _ []string) (map[string]*models.User, error) {
	return nil, nil
}
func (s *stubUserRepo) FindByUsername(_ context.Context, _ string) (*models.User, error) {
	return nil, nil
}
func (s *stubUserRepo) FindByExternalID(_ context.Context, _, _ string) (*models.User, error) {
	return nil, nil
}
func (s *stubUserRepo) Update(_ *models.User) error                   { return nil }
func (s *stubUserRepo) Delete(_ context.Context, _ string) error      { return nil }
func (s *stubUserRepo) List(_ context.Context) ([]models.User, error) { return nil, nil }
func (s *stubUserRepo) Count(_ context.Context) (int64, error)        { return 0, nil }
func (s *stubUserRepo) ListByRoles(_ context.Context, _ []string) ([]models.User, error) {
	return nil, nil
}
func (s *stubUserRepo) UpdateRole(_ context.Context, _, _, _ string) (string, error) { return "", nil }
func (s *stubUserRepo) SetDisabled(_ context.Context, _, _ string, _ bool) error     { return nil }
func (s *stubUserRepo) DeleteGuarded(_ context.Context, _, _ string) error           { return nil }
func (s *stubUserRepo) UpdatePassword(_ context.Context, _, _ string) error          { return nil }
func (s *stubUserRepo) UpdateProfile(_ context.Context, _ string, _ models.UserProfileUpdate) error {
	return nil
}

type stubAPIKeyRepo struct{}

func (s *stubAPIKeyRepo) Create(_ context.Context, _ *models.APIKey) error { return nil }
func (s *stubAPIKeyRepo) FindByID(_ context.Context, _, _ string) (*models.APIKey, error) {
	return nil, nil
}
func (s *stubAPIKeyRepo) FindByPrefix(_ context.Context, _ string) ([]*models.APIKey, error) {
	return nil, nil
}
func (s *stubAPIKeyRepo) FindByIDs(_ context.Context, _ []string) ([]*models.APIKey, error) {
	return nil, nil
}
func (s *stubAPIKeyRepo) ListByUser(_ context.Context, _ string) ([]*models.APIKey, error) {
	return nil, nil
}
func (s *stubAPIKeyRepo) UpdateLastUsed(_ context.Context, _, _ string, _ time.Time) error {
	return nil
}
func (s *stubAPIKeyRepo) Delete(_ context.Context, _, _ string) error { return nil }
func (s *stubAPIKeyRepo) DeleteAllForUser(_ context.Context, _ string) (int64, error) {
	return 0, nil
}

type stubAuditLogger struct{}

func (s *stubAuditLogger) Create(ctx context.Context, _ *models.AuditLog) error { return nil }

type routeRoundTripper func(*http.Request) (*http.Response, error)

func (fn routeRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

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

	healthChecker := health.New()
	healthChecker.SetReady(true)

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	rl := SetupRoutes(router, Deps{
		HealthChecker: healthChecker,
		Config:        testConfig(),
		Hub:           hub,
	})
	t.Cleanup(func() { rl.Stop() })

	return router, rl
}

func TestSetupRoutesRedactsGitRepoBeforeRouteHandling(t *testing.T) {
	t.Parallel()
	router, _ := setupMinimalRouter(t)

	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/git/branches?repo=https%3A%2F%2Fuser%3Asecret%40github.com%2Forg%2Frepo%3Ftoken%3Dnested-secret%23main&keep=%2f%2B", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	assert.Equal(t, "keep=%2f%2B", request.URL.RawQuery)
	assert.NotContains(t, request.RequestURI, "user")
	assert.NotContains(t, request.RequestURI, "secret")
	assert.NotContains(t, request.RequestURI, "token")
}

func TestGitRouteAllowsAuthenticatedUserForAllowedRepository(t *testing.T) {
	gin.SetMode(gin.TestMode)

	originalTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	providerRequests := make(chan *http.Request, 1)
	http.DefaultTransport = routeRoundTripper(func(request *http.Request) (*http.Response, error) {
		providerRequests <- request.Clone(request.Context())
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`[{"name":"main"}]`)),
			Request:    request,
		}, nil
	})

	cfg := testConfig()
	healthChecker := health.New()
	healthChecker.SetReady(true)
	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	registry, err := gitprovider.NewRegistry(gitprovider.Config{GitHub: gitprovider.GitHubConfig{
		Token:               "provider-token-marker",
		AllowedRepositories: []string{"octo-org/octo-repo"},
	}})
	require.NoError(t, err)
	authHandler := handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth, &cfg.OIDC)
	router := gin.New()
	rateLimiters := SetupRoutes(router, Deps{
		Config:        cfg,
		HealthChecker: healthChecker,
		Hub:           hub,
		AuthHandler:   authHandler,
		GitHandler:    handlers.NewGitHandler(registry),
	})
	t.Cleanup(rateLimiters.Stop)

	token, err := middleware.GenerateToken("user-1", "alice", "developer", cfg.Auth.JWTSecret, time.Hour)
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/git/branches?repo=https%3A%2F%2Fgithub.com%2Focto-org%2Focto-repo.git", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `[{"name":"main","is_default":false}]`, response.Body.String())
	providerRequest := <-providerRequests
	assert.Equal(t, "api.github.com", providerRequest.URL.Host)
	assert.Equal(t, "/repos/octo-org/octo-repo/branches", providerRequest.URL.Path)
	assert.Equal(t, "Bearer provider-token-marker", providerRequest.Header.Get("Authorization"))
}

// ---- tests ----

func TestSetupRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.Default()

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

	healthChecker := health.New()
	healthChecker.SetReady(true)

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	cfg := testConfig()

	// Create a minimal AuthHandler to trigger Phase 1 route registration.
	authHandler := handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth, &cfg.OIDC)

	rl := SetupRoutes(router, Deps{
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
		{"GET", "/api/v1/ui-config"},
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

	healthChecker := health.New()
	healthChecker.SetReady(true)

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	cfg := testConfig()
	authHandler := handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth, &cfg.OIDC)

	rl := SetupRoutes(router, Deps{
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

	healthChecker := health.New()
	healthChecker.SetReady(true)

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	cfg := testConfig()
	authHandler := handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth, &cfg.OIDC)

	rl := SetupRoutes(router, Deps{
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

	healthChecker := health.New()
	healthChecker.SetReady(true)

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	cfg := testConfig()
	authHandler := handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth, &cfg.OIDC)

	// OIDCHandler is nil (OIDC disabled), so the fallback config endpoint should be registered.
	rl := SetupRoutes(router, Deps{
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

	healthChecker := health.New()
	healthChecker.SetReady(true)

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	rl := SetupRoutes(router, Deps{
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

	healthChecker := health.New()
	healthChecker.SetReady(true)

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	rl := SetupRoutes(router, Deps{
		HealthChecker: healthChecker,
		Config:        testConfig(),
		Hub:           hub,
	})
	t.Cleanup(func() { rl.Stop() })

	// A test route after SetupRoutes gets the global middleware (MaxBodySize).
	router.POST("/api/v1/test-body", func(c *gin.Context) {
		if _, err := io.ReadAll(c.Request.Body); err != nil {
			c.Status(http.StatusRequestEntityTooLarge)
			return
		}
		c.Status(http.StatusOK)
	})

	// Create a body larger than 1 MB.
	largeBody := strings.Repeat("a", 1<<20+1)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/test-body", strings.NewReader(largeBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	// Reading the limited body fails: the MaxBodySize middleware works and
	// the request is NOT processed.
	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code, "oversized body should be rejected")
}

func TestSetupRoutes_WithAllHandlersRegistersFullAPI(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	router := gin.New()

	healthChecker := health.New()
	healthChecker.SetReady(true)

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	cfg := testConfig()
	authHandler := handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth, &cfg.OIDC)
	userHandler := handlers.NewUserHandler(&stubUserRepo{}, nil, &stubAPIKeyRepo{})
	apiKeyHandler := handlers.NewAPIKeyHandler(&stubAPIKeyRepo{}, &stubUserRepo{}, &cfg.Auth)
	auditLogHandler := handlers.NewAuditLogHandler(nil)

	rl := SetupRoutes(router, Deps{
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

	healthChecker := health.New()
	healthChecker.SetReady(true)

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	cfg := testConfig()
	authHandler := handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth, &cfg.OIDC)

	// All domain handlers are nil -- only auth/health should be registered.
	rl := SetupRoutes(router, Deps{
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

func TestSetupRoutes_NoItemsRoutes(t *testing.T) {
	t.Parallel()

	// GitHub issue #534: the items handlers are a reference implementation
	// only. Unauthenticated item routes let any client change data.
	router, _ := setupMinimalRouter(t)
	fullRouter, _ := setupFullRouter(t)

	// Neither the minimal router nor the router with all handlers has items routes.
	for _, rt := range append(router.Routes(), fullRouter.Routes()...) {
		assert.False(t, strings.HasPrefix(rt.Path, "/api/v1/items"), "items route registered: %s %s", rt.Method, rt.Path)
	}

	tests := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/v1/items", ""},
		{http.MethodPost, "/api/v1/items", `{"name":"x","price":1}`},
		{http.MethodPut, "/api/v1/items/1", `{"name":"x","price":1}`},
		{http.MethodDelete, "/api/v1/items/1", ""},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.method, func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)
			assert.Equal(t, http.StatusNotFound, w.Code)
		})
	}
}

func TestSetupRoutes_RecoveryMiddleware(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	router := gin.New()

	healthChecker := health.New()
	healthChecker.SetReady(true)

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	rl := SetupRoutes(router, Deps{
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

			healthChecker := health.New()
			healthChecker.SetReady(true)

			hub := websocket.NewHub()
			go hub.Run()
			t.Cleanup(func() { hub.Shutdown() })

			cfg := testConfig()
			cfg.App.EnableSwagger = tt.enable

			rl := SetupRoutes(router, Deps{
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

func (s *stubStackTemplateRepo) Create(ctx context.Context, _ *models.StackTemplate) error {
	return ctx.Err()
}
func (s *stubStackTemplateRepo) FindByID(ctx context.Context, _ string) (*models.StackTemplate, error) {
	return nil, ctx.Err()
}
func (s *stubStackTemplateRepo) FindByIDForUpdate(ctx context.Context, _ string) (*models.StackTemplate, error) {
	return nil, ctx.Err()
}
func (s *stubStackTemplateRepo) Update(ctx context.Context, _ *models.StackTemplate) error {
	return ctx.Err()
}
func (s *stubStackTemplateRepo) Delete(ctx context.Context, _ string) error { return ctx.Err() }
func (s *stubStackTemplateRepo) List(ctx context.Context) ([]models.StackTemplate, error) {
	return nil, ctx.Err()
}
func (s *stubStackTemplateRepo) ListPaged(ctx context.Context, _, _ int, _ string) ([]models.StackTemplate, int64, error) {
	return nil, 0, ctx.Err()
}
func (s *stubStackTemplateRepo) ListPublished(ctx context.Context) ([]models.StackTemplate, error) {
	return nil, ctx.Err()
}
func (s *stubStackTemplateRepo) ListPublishedPaged(ctx context.Context, _, _ int, _ string) ([]models.StackTemplate, int64, error) {
	return nil, 0, ctx.Err()
}
func (s *stubStackTemplateRepo) ListByOwner(ctx context.Context, _ string) ([]models.StackTemplate, error) {
	return nil, ctx.Err()
}
func (s *stubStackTemplateRepo) Count(ctx context.Context) (int64, error) { return 0, ctx.Err() }

type stubStackDefinitionRepo struct{}

func (s *stubStackDefinitionRepo) Create(ctx context.Context, _ *models.StackDefinition) error {
	return ctx.Err()
}
func (s *stubStackDefinitionRepo) FindByID(ctx context.Context, _ string) (*models.StackDefinition, error) {
	return nil, ctx.Err()
}
func (s *stubStackDefinitionRepo) FindByName(ctx context.Context, _ string) ([]models.StackDefinition, error) {
	return nil, ctx.Err()
}
func (s *stubStackDefinitionRepo) Update(ctx context.Context, _ *models.StackDefinition) error {
	return ctx.Err()
}
func (s *stubStackDefinitionRepo) SetTemplateLink(ctx context.Context, _, _, _ string) error {
	return ctx.Err()
}
func (s *stubStackDefinitionRepo) Delete(ctx context.Context, _ string) error { return ctx.Err() }
func (s *stubStackDefinitionRepo) List(ctx context.Context) ([]models.StackDefinition, error) {
	return nil, ctx.Err()
}
func (s *stubStackDefinitionRepo) ListPaged(ctx context.Context, _ models.StackDefinitionFilter, _, _ int) ([]models.StackDefinition, int64, error) {
	return nil, 0, ctx.Err()
}
func (s *stubStackDefinitionRepo) ListByOwner(ctx context.Context, _ string) ([]models.StackDefinition, error) {
	return nil, ctx.Err()
}
func (s *stubStackDefinitionRepo) ListByTemplate(ctx context.Context, _ string) ([]models.StackDefinition, error) {
	return nil, ctx.Err()
}
func (s *stubStackDefinitionRepo) CountByTemplateIDs(ctx context.Context, _ []string) (map[string]int, error) {
	return nil, ctx.Err()
}
func (s *stubStackDefinitionRepo) FindByIDForUpdate(ctx context.Context, _ string) (*models.StackDefinition, error) {
	return nil, ctx.Err()
}
func (s *stubStackDefinitionRepo) ListIDsByTemplateIDs(ctx context.Context, _ []string) (map[string][]string, error) {
	return nil, ctx.Err()
}
func (s *stubStackDefinitionRepo) Count(ctx context.Context) (int64, error) { return 0, ctx.Err() }
func (s *stubStackDefinitionRepo) NamesByIDs(ctx context.Context, _ []string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return map[string]string{}, nil
}

func (s *stubStackDefinitionRepo) TemplateRefsByIDs(ctx context.Context, _ []string) (map[string]models.DefinitionTemplateRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return map[string]models.DefinitionTemplateRef{}, nil
}

type stubStackInstanceRepo struct{}

func (s *stubStackInstanceRepo) Create(ctx context.Context, _ *models.StackInstance) error {
	return ctx.Err()
}
func (s *stubStackInstanceRepo) FindByID(ctx context.Context, _ string) (*models.StackInstance, error) {
	return nil, ctx.Err()
}
func (s *stubStackInstanceRepo) FindByNamespace(ctx context.Context, _ string) (*models.StackInstance, error) {
	return nil, ctx.Err()
}
func (s *stubStackInstanceRepo) Update(ctx context.Context, _ *models.StackInstance) error {
	return ctx.Err()
}
func (s *stubStackInstanceRepo) Delete(ctx context.Context, _ string) error { return ctx.Err() }
func (s *stubStackInstanceRepo) List(ctx context.Context) ([]models.StackInstance, error) {
	return nil, ctx.Err()
}
func (s *stubStackInstanceRepo) ListPaged(ctx context.Context, _ models.StackInstanceFilter, _, _ int) ([]models.StackInstance, int, error) {
	return nil, 0, ctx.Err()
}
func (s *stubStackInstanceRepo) ListByOwner(ctx context.Context, _ string) ([]models.StackInstance, error) {
	return nil, ctx.Err()
}
func (s *stubStackInstanceRepo) FindByName(ctx context.Context, _ string) ([]models.StackInstance, error) {
	return nil, ctx.Err()
}
func (s *stubStackInstanceRepo) FindByCluster(ctx context.Context, _ string) ([]models.StackInstance, error) {
	return nil, ctx.Err()
}
func (s *stubStackInstanceRepo) CountForInstanceLimit(ctx context.Context, _, _ string, _ bool) (int, error) {
	return 0, ctx.Err()
}
func (s *stubStackInstanceRepo) CountAll(ctx context.Context) (int, error) { return 0, ctx.Err() }
func (s *stubStackInstanceRepo) CountByStatus(ctx context.Context, _ string) (int, error) {
	return 0, ctx.Err()
}
func (s *stubStackInstanceRepo) CountByDefinitionIDs(ctx context.Context, _ []string) (map[string]int, error) {
	return nil, ctx.Err()
}
func (s *stubStackInstanceRepo) CountByOwnerIDs(ctx context.Context, _ []string) (map[string]int, error) {
	return nil, ctx.Err()
}
func (s *stubStackInstanceRepo) ListIDsByDefinitionIDs(ctx context.Context, _ []string) (map[string][]string, error) {
	return nil, ctx.Err()
}
func (s *stubStackInstanceRepo) ListIDsByOwnerIDs(ctx context.Context, _ []string) (map[string][]string, error) {
	return nil, ctx.Err()
}
func (s *stubStackInstanceRepo) ExistsByDefinitionAndStatus(ctx context.Context, _, _ string) (bool, error) {
	return false, ctx.Err()
}
func (s *stubStackInstanceRepo) ListExpired(ctx context.Context) ([]*models.StackInstance, error) {
	return nil, ctx.Err()
}
func (s *stubStackInstanceRepo) ListExpiringSoon(ctx context.Context, _ time.Duration) ([]*models.StackInstance, error) {
	return nil, ctx.Err()
}
func (*stubStackInstanceRepo) MarkExpiryWarned(ctx context.Context, _ string, _, _ time.Time) (bool, error) {
	return false, ctx.Err()
}
func (s *stubStackInstanceRepo) ListByStatus(ctx context.Context, _ string, _ int) ([]*models.StackInstance, error) {
	return nil, ctx.Err()
}

type stubChartConfigRepo struct{}

func (s *stubChartConfigRepo) Create(ctx context.Context, _ *models.ChartConfig) error {
	return ctx.Err()
}
func (s *stubChartConfigRepo) FindByID(ctx context.Context, _ string) (*models.ChartConfig, error) {
	return nil, ctx.Err()
}
func (s *stubChartConfigRepo) Update(ctx context.Context, _ *models.ChartConfig) error {
	return ctx.Err()
}
func (s *stubChartConfigRepo) Delete(ctx context.Context, _ string) error { return ctx.Err() }
func (s *stubChartConfigRepo) DeleteByDefinition(ctx context.Context, _ string) error {
	return ctx.Err()
}
func (s *stubChartConfigRepo) ListByDefinition(ctx context.Context, _ string) ([]models.ChartConfig, error) {
	return nil, ctx.Err()
}
func (s *stubChartConfigRepo) CountByDefinitionIDs(ctx context.Context, _ []string) (map[string]int, error) {
	return nil, ctx.Err()
}

type stubTemplateChartConfigRepo struct{}

func (s *stubTemplateChartConfigRepo) Create(ctx context.Context, _ *models.TemplateChartConfig) error {
	return ctx.Err()
}
func (s *stubTemplateChartConfigRepo) FindByID(ctx context.Context, _ string) (*models.TemplateChartConfig, error) {
	return nil, ctx.Err()
}
func (s *stubTemplateChartConfigRepo) Update(ctx context.Context, _ *models.TemplateChartConfig) error {
	return ctx.Err()
}
func (s *stubTemplateChartConfigRepo) Delete(ctx context.Context, _ string) error { return ctx.Err() }
func (s *stubTemplateChartConfigRepo) ListByTemplate(ctx context.Context, _ string) ([]models.TemplateChartConfig, error) {
	return nil, ctx.Err()
}

type stubValueOverrideRepo struct{}

func (s *stubValueOverrideRepo) Create(ctx context.Context, _ *models.ValueOverride) error {
	return ctx.Err()
}
func (s *stubValueOverrideRepo) FindByID(ctx context.Context, _ string) (*models.ValueOverride, error) {
	return nil, ctx.Err()
}
func (s *stubValueOverrideRepo) DeleteByInstance(ctx context.Context, _ string) error {
	return ctx.Err()
}
func (s *stubValueOverrideRepo) FindByInstanceAndChart(ctx context.Context, _, _ string) (*models.ValueOverride, error) {
	return nil, ctx.Err()
}
func (s *stubValueOverrideRepo) Update(ctx context.Context, _ *models.ValueOverride) error {
	return ctx.Err()
}
func (s *stubValueOverrideRepo) Delete(ctx context.Context, _ string) error { return ctx.Err() }
func (s *stubValueOverrideRepo) ListByInstance(ctx context.Context, _ string) ([]models.ValueOverride, error) {
	return nil, ctx.Err()
}

type stubChartBranchOverrideRepo struct{}

func (s *stubChartBranchOverrideRepo) List(ctx context.Context, _ string) ([]*models.ChartBranchOverride, error) {
	return nil, ctx.Err()
}
func (s *stubChartBranchOverrideRepo) Get(ctx context.Context, _, _ string) (*models.ChartBranchOverride, error) {
	return nil, ctx.Err()
}
func (s *stubChartBranchOverrideRepo) Set(ctx context.Context, _ *models.ChartBranchOverride) error {
	return ctx.Err()
}
func (s *stubChartBranchOverrideRepo) Delete(ctx context.Context, _, _ string) error {
	return ctx.Err()
}
func (s *stubChartBranchOverrideRepo) DeleteByInstance(ctx context.Context, _ string) error {
	return ctx.Err()
}

type stubInstanceQuotaOverrideRepo struct{}

func (s *stubInstanceQuotaOverrideRepo) GetByInstanceID(_ context.Context, _ string) (*models.InstanceQuotaOverride, error) {
	return nil, nil
}
func (s *stubInstanceQuotaOverrideRepo) Upsert(_ context.Context, _ *models.InstanceQuotaOverride) error {
	return nil
}
func (s *stubInstanceQuotaOverrideRepo) Delete(_ context.Context, _ string) error { return nil }
func (s *stubInstanceQuotaOverrideRepo) ListByInstanceIDs(_ context.Context, _ []string) ([]models.InstanceQuotaOverride, error) {
	return nil, nil
}

type stubUserFavoriteRepo struct{}

func (s *stubUserFavoriteRepo) List(_ context.Context, _ string) ([]*models.UserFavorite, error) {
	return nil, nil
}
func (s *stubUserFavoriteRepo) Add(ctx context.Context, _ *models.UserFavorite) error { return nil }
func (s *stubUserFavoriteRepo) Remove(_ context.Context, _, _, _ string) error        { return nil }
func (s *stubUserFavoriteRepo) IsFavorite(_ context.Context, _, _, _ string) (bool, error) {
	return false, nil
}

type stubNotificationRepo struct{}

func (s *stubNotificationRepo) Create(_ context.Context, _ *models.Notification) error { return nil }
func (s *stubNotificationRepo) ListByUser(_ context.Context, _ string, _ bool, _, _ int) ([]models.Notification, int64, bool, error) {
	return nil, 0, false, nil
}
func (s *stubNotificationRepo) CountUnread(_ context.Context, _ string) (int64, bool, error) {
	return 0, false, nil
}
func (s *stubNotificationRepo) MarkAsRead(_ context.Context, _, _ string) error { return nil }
func (s *stubNotificationRepo) MarkAllAsRead(_ context.Context, _ string) error { return nil }
func (s *stubNotificationRepo) GetPreferences(_ context.Context, _ string) ([]models.NotificationPreference, error) {
	return nil, nil
}
func (s *stubNotificationRepo) DisabledUserIDs(_ context.Context, _ string, _ []string) (map[string]bool, error) {
	return map[string]bool{}, nil
}
func (s *stubNotificationRepo) UpdatePreference(_ context.Context, _ *models.NotificationPreference) error {
	return nil
}

type stubCleanupPolicyRepo struct{}

func (s *stubCleanupPolicyRepo) Create(ctx context.Context, _ *models.CleanupPolicy) error {
	return nil
}
func (s *stubCleanupPolicyRepo) FindByID(_ context.Context, _ string) (*models.CleanupPolicy, error) {
	return nil, nil
}
func (s *stubCleanupPolicyRepo) Update(ctx context.Context, _ *models.CleanupPolicy) error {
	return nil
}
func (s *stubCleanupPolicyRepo) Delete(_ context.Context, _ string) error { return nil }
func (s *stubCleanupPolicyRepo) List(ctx context.Context) ([]models.CleanupPolicy, error) {
	return nil, nil
}
func (s *stubCleanupPolicyRepo) ListEnabled(ctx context.Context) ([]models.CleanupPolicy, error) {
	return nil, nil
}

type stubClusterRepo struct{}

func (s *stubClusterRepo) Create(ctx context.Context, _ *models.Cluster) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}
func (s *stubClusterRepo) FindByID(ctx context.Context, _ string) (*models.Cluster, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, nil
}
func (s *stubClusterRepo) Update(ctx context.Context, _ *models.Cluster) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}
func (s *stubClusterRepo) UpdateHealthStatus(ctx context.Context, _, _ string) error {
	return ctx.Err()
}
func (s *stubClusterRepo) Delete(ctx context.Context, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}
func (s *stubClusterRepo) List(ctx context.Context) ([]models.Cluster, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, nil
}
func (s *stubClusterRepo) FindDefault(ctx context.Context) (*models.Cluster, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, nil
}
func (s *stubClusterRepo) SetDefault(ctx context.Context, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

type stubSharedValuesRepo struct{}

func (s *stubSharedValuesRepo) Create(ctx context.Context, _ *models.SharedValues) error {
	return ctx.Err()
}
func (s *stubSharedValuesRepo) FindByID(ctx context.Context, _ string) (*models.SharedValues, error) {
	return nil, ctx.Err()
}
func (s *stubSharedValuesRepo) FindByClusterAndID(ctx context.Context, _, _ string) (*models.SharedValues, error) {
	return nil, ctx.Err()
}
func (s *stubSharedValuesRepo) Update(ctx context.Context, _ *models.SharedValues) error {
	return ctx.Err()
}
func (s *stubSharedValuesRepo) Delete(ctx context.Context, _ string) error { return ctx.Err() }
func (s *stubSharedValuesRepo) ListByCluster(ctx context.Context, _ string) ([]models.SharedValues, error) {
	return nil, ctx.Err()
}

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
func (s *stubDeploymentLogRepo) ListLatestByActions(_ context.Context, _ string, _ []string, _ int) ([]models.DeploymentLog, error) {
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

func (s *stubDeploymentLogRepo) SummarizeByUsers(_ context.Context, _ []string) (map[string]*models.DeployLogSummary, error) {
	return nil, nil
}
func (s *stubDeploymentLogRepo) CountByAction(_ context.Context, _ string) (int, error) {
	return 0, nil
}
func (s *stubDeploymentLogRepo) ListRecentGlobal(_ context.Context, _ int) ([]models.DeploymentLogWithContext, error) {
	return nil, nil
}

type stubAuditLogRepo struct{}

func (s *stubAuditLogRepo) Create(ctx context.Context, _ *models.AuditLog) error { return nil }
func (s *stubAuditLogRepo) List(ctx context.Context, _ models.AuditLogFilters) (*models.AuditLogResult, error) {
	return nil, nil
}

type stubSessionStore struct{}

func (s *stubSessionStore) BlockToken(_ context.Context, _ string, _ time.Time) error { return nil }
func (s *stubSessionStore) IsTokenBlocked(_ context.Context, _ string) (bool, error) {
	return false, nil
}
func (s *stubSessionStore) BlockUser(_ context.Context, _ string, _ time.Time) error { return nil }
func (s *stubSessionStore) IsUserBlocked(_ context.Context, _ string, _ time.Time) (bool, error) {
	return false, nil
}
func (s *stubSessionStore) BlockSession(_ context.Context, _ string, _ time.Time) error { return nil }
func (s *stubSessionStore) BlockSessions(_ context.Context, _ []string, _ time.Time) error {
	return nil
}
func (s *stubSessionStore) IsSessionBlocked(_ context.Context, _ string) (bool, error) {
	return false, nil
}
func (s *stubSessionStore) UnblockUser(_ context.Context, _ string) error { return nil }
func (s *stubSessionStore) SaveOIDCState(_ context.Context, _ string, _ sessionstore.OIDCStateData, _ time.Duration) error {
	return nil
}
func (s *stubSessionStore) ConsumeOIDCState(_ context.Context, _ string) (*sessionstore.OIDCStateData, error) {
	return nil, nil
}
func (s *stubSessionStore) SaveCLIAuth(_ context.Context, _ string, _ sessionstore.CLIAuthData, _ time.Duration) error {
	return nil
}
func (s *stubSessionStore) GetCLIAuth(_ context.Context, _ string) (*sessionstore.CLIAuthData, error) {
	return nil, nil
}
func (s *stubSessionStore) UpdateCLIAuth(_ context.Context, _ string, _ sessionstore.CLIAuthData) error {
	return nil
}
func (s *stubSessionStore) ConsumeCLIAuth(_ context.Context, _ string) (*sessionstore.CLIAuthData, error) {
	return nil, nil
}
func (s *stubSessionStore) Cleanup(_ context.Context) error { return nil }
func (s *stubSessionStore) Stop()                           {}

type stubTxRunner struct{}

func (s *stubTxRunner) RunInTx(ctx context.Context, fn func(repos database.TxRepos) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
	gitRegistry, err := gitprovider.NewRegistry(gitprovider.Config{})
	require.NoError(t, err)
	gitHandler := handlers.NewGitHandler(gitRegistry)
	auditLogHandler := handlers.NewAuditLogHandler(&stubAuditLogRepo{})
	userHandler := handlers.NewUserHandler(userRepo, nil, apiKeyRepo)
	apiKeyHandler := handlers.NewAPIKeyHandler(apiKeyRepo, userRepo, &cfg.Auth)
	adminHandler := handlers.NewAdminHandler(nil, instanceRepo)
	branchOverrideHandler := handlers.NewBranchOverrideHandler(branchOverrideRepo, instanceRepo, chartConfigRepo)
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
		HealthChecker:                healthChecker,
		Config:                       cfg,
		Hub:                          hub,
		AuthHandler:                  authHandler,
		TemplateHandler:              templateHandler,
		DefinitionHandler:            definitionHandler,
		InstanceHandler:              instanceHandler,
		GitHandler:                   gitHandler,
		AuditLogHandler:              auditLogHandler,
		AuditLogger:                  &stubAuditLogger{},
		UserHandler:                  userHandler,
		APIKeyHandler:                apiKeyHandler,
		AdminHandler:                 adminHandler,
		BranchOverrideHandler:        branchOverrideHandler,
		InstanceQuotaOverrideHandler: instanceQuotaHandler,
		FavoriteHandler:              favoriteHandler,
		QuickDeployHandler:           quickDeployHandler,
		AnalyticsHandler:             analyticsHandler,
		CleanupPolicyHandler:         cleanupPolicyHandler,
		ClusterHandler:               clusterHandler,
		SharedValuesHandler:          sharedValuesHandler,
		DashboardHandler:             dashboardHandler,
		TemplateVersionHandler:       templateVersionHandler,
		NotificationHandler:          notificationHandler,
		OIDCHandler:                  oidcHandler,
		UserRepo:                     userRepo,
		APIKeyRepo:                   apiKeyRepo,
		ClusterRepo:                  clusterRepo,
		InstanceRepo:                 instanceRepo,
		SessionStore:                 &stubSessionStore{},
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

		// Ping and UI config (always registered)
		{"GET", "/api/v1/ping"},
		{"GET", "/api/v1/ui-config"},

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
		{"POST", "/api/v1/auth/oidc/cli-auth"},
		{"POST", "/api/v1/auth/oidc/cli-token"},

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
		{"PUT", "/api/v1/stack-definitions/:id/template-link"},
		{"POST", "/api/v1/stack-definitions/:id/charts"},
		{"GET", "/api/v1/stack-definitions/:id/charts/:chartId"},
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
		{"GET", "/api/v1/stack-instances/:id/overrides/:chartId"},
		{"PUT", "/api/v1/stack-instances/:id/overrides/:chartId"},
		{"DELETE", "/api/v1/stack-instances/:id/overrides/:chartId"},
		{"POST", "/api/v1/stack-instances/:id/deploy"},
		{"GET", "/api/v1/stack-instances/:id/deploy-preview"},
		{"POST", "/api/v1/stack-instances/:id/stop"},
		{"POST", "/api/v1/stack-instances/:id/clean"},
		{"GET", "/api/v1/stack-instances/:id/actions"},
		{"POST", "/api/v1/stack-instances/:id/actions/:name"},
		{"GET", "/api/v1/stack-instances/:id/actions/:name/jobs/:job_id/log"},
		{"GET", "/api/v1/stack-instances/:id/action-runs"},
		{"POST", "/api/v1/stack-instances/:id/extend"},
		{"POST", "/api/v1/stack-instances/:id/follow"},
		{"DELETE", "/api/v1/stack-instances/:id/follow"},
		{"GET", "/api/v1/stack-instances/:id/deploy-log"},
		{"GET", "/api/v1/stack-instances/:id/deploy-log/:logId"},
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
		{"GET", "/api/v1/stack-instances/:id/branches/:chartId"},
		{"PUT", "/api/v1/stack-instances/:id/branches/:chartId"},
		{"DELETE", "/api/v1/stack-instances/:id/branches/:chartId"},

		// Instance quota overrides
		{"GET", "/api/v1/stack-instances/:id/quota"},
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
		{"PUT", "/api/v1/users/:id/password"},
		{"PUT", "/api/v1/users/:id/role"},

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
		"POST /api/v1/auth/oidc/cli-auth",
		"POST /api/v1/auth/oidc/cli-token",
	}
	for _, route := range oidcRoutes {
		assert.True(t, registered[route], "OIDC route missing when OIDCHandler is set: %s", route)
	}
}

func TestSetupRoutes_OIDCDisabled_FallbackConfigOnly(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	router := gin.New()

	healthChecker := health.New()
	healthChecker.SetReady(true)

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	cfg := testConfig()
	authHandler := handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth, &cfg.OIDC)

	rl := SetupRoutes(router, Deps{
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
	assert.False(t, registered["POST /api/v1/auth/oidc/cli-auth"],
		"OIDC cli-auth should NOT be registered when OIDC is disabled")
	assert.False(t, registered["POST /api/v1/auth/oidc/cli-token"],
		"OIDC cli-token should NOT be registered when OIDC is disabled")
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

			healthChecker := health.New()
			healthChecker.SetReady(true)

			hub := websocket.NewHub()
			go hub.Run()
			t.Cleanup(func() { hub.Shutdown() })

			cfg := testConfig()
			cfg.Server.LoginRateLimit = tt.loginRateLimit
			authHandler := handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth, &cfg.OIDC)

			rl := SetupRoutes(router, Deps{
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

	healthChecker := health.New()
	healthChecker.SetReady(true)

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	cfg := testConfig()
	authHandler := handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth, &cfg.OIDC)

	rl := SetupRoutes(router, Deps{
		HealthChecker:  healthChecker,
		Config:         cfg,
		Hub:            hub,
		AuthHandler:    authHandler,
		UserRepo:       &stubUserRepo{},
		APIKeyRepo:     &stubAPIKeyRepo{},
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

	healthChecker := health.New()
	healthChecker.SetReady(true)

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	cfg := testConfig()
	authHandler := handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth, &cfg.OIDC)

	rl := SetupRoutes(router, Deps{
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

	healthChecker := health.New()
	healthChecker.SetReady(true)

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	cfg := testConfig()
	authHandler := handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth, &cfg.OIDC)
	apiKeyHandler := handlers.NewAPIKeyHandler(&stubAPIKeyRepo{}, &stubUserRepo{}, &cfg.Auth)

	rl := SetupRoutes(router, Deps{
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

// TestSetupRoutes_UIConfigIsPublic checks that GET /api/v1/ui-config needs no
// token, also when the authenticated routes are registered.
func TestSetupRoutes_UIConfigIsPublic(t *testing.T) {
	t.Parallel()

	router, _ := setupFullRouter(t)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/ui-config", nil)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "public, max-age=300", w.Header().Get("Cache-Control"))
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	assert.ElementsMatch(t, []string{"title", "logo_url", "favicon_url", "default_instance_ttl_minutes", "max_instance_ttl_days"}, keys)
	assert.NotEmpty(t, body["title"])
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

func (*stubStackInstanceRepo) CountByStatuses(ctx context.Context, statuses []string) (int, error) {
	return 0, ctx.Err()
}

func (*stubClusterRepo) CountAll(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return 0, nil
}

func (*stubClusterRepo) NamesByIDs(ctx context.Context, _ []string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return map[string]string{}, nil
}
func (*stubClusterRepo) CountByHealthStatus(ctx context.Context, status string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return 0, nil
}

func (s *stubCleanupPolicyRepo) SetLastRunAt(_ context.Context, _ string, _ time.Time) error {
	return nil
}

package routes

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"backend/internal/api/handlers"
	"backend/internal/health"
	"backend/internal/websocket"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// peerAddr is the TCP peer of every test request (httptest default range).
const peerAddr = "192.0.2.1:40000"

// setupTrustedProxyRouter returns a router with the given RATE_LIMIT,
// LOGIN_RATE_LIMIT and TRUSTED_PROXIES.
func setupTrustedProxyRouter(t *testing.T, apiLimit, loginLimit int32, trusted []string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()

	healthChecker := health.New()
	healthChecker.SetReady(true)
	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	cfg := testConfig()
	cfg.Server.RateLimit = apiLimit
	cfg.Server.LoginRateLimit = loginLimit
	cfg.Server.TrustedProxies = trusted

	rl := SetupRoutes(router, Deps{
		HealthChecker: healthChecker,
		Config:        cfg,
		Hub:           hub,
		AuthHandler:   handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth, &cfg.OIDC),
		UserRepo:      &stubUserRepo{},
		APIKeyRepo:    &stubAPIKeyRepo{},
	})
	t.Cleanup(func() { rl.Stop() })
	return router
}

// limiterCase is one rate limiter under test: the request that reaches it
// and the limits that make only this limiter refuse the third request (the
// login and refresh routes also pass the API limiter, so it gets a high limit).
type limiterCase struct {
	build      func() *http.Request
	apiLimit   int32
	loginLimit int32
}

// TestSetupRoutes_TrustedProxies sends 3 requests from one peer, each with a
// new client IP header, to a limiter that allows 2. An untrusted peer counts
// as one client (the third request gets 429); a trusted proxy passes the
// client IP, so each request counts for another client.
func TestSetupRoutes_TrustedProxies(t *testing.T) {
	t.Parallel()

	limiters := map[string]limiterCase{
		"API limiter": {apiLimit: 2, loginLimit: 100, build: func() *http.Request {
			return httptest.NewRequest(http.MethodGet, "/api/v1/ui-config", nil)
		}},
		"login limiter": {apiLimit: 100, loginLimit: 2, build: func() *http.Request {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			return req
		}},
		"refresh (login limiter)": {apiLimit: 100, loginLimit: 2, build: func() *http.Request {
			return httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
		}},
		"ws API-key limit": {apiLimit: 2, loginLimit: 100, build: func() *http.Request {
			req := httptest.NewRequest(http.MethodGet, "/ws", nil)
			req.Header.Set("X-API-Key", "sk_test_invalid")
			return req
		}},
	}
	tests := []struct {
		name        string
		trusted     []string
		header      string
		wantLast429 bool
	}{
		{name: "no trusted proxy ignores X-Forwarded-For", header: "X-Forwarded-For", wantLast429: true},
		{name: "no trusted proxy ignores X-Real-IP", header: "X-Real-IP", wantLast429: true},
		{name: "untrusted peer ignores X-Forwarded-For", trusted: []string{"10.0.0.0/8"}, header: "X-Forwarded-For", wantLast429: true},
		{name: "invalid list falls back to no trusted proxy", trusted: []string{"192.0.2.0/24", "bogus"}, header: "X-Forwarded-For", wantLast429: true},
		{name: "trusted proxy passes X-Forwarded-For", trusted: []string{"192.0.2.0/24"}, header: "X-Forwarded-For", wantLast429: false},
		{name: "trusted proxy passes X-Real-IP", trusted: []string{"192.0.2.0/24"}, header: "X-Real-IP", wantLast429: false},
	}
	for _, tt := range tests {
		for limiter, lc := range limiters {
			tt, limiter, lc := tt, limiter, lc
			t.Run(tt.name+"/"+limiter, func(t *testing.T) {
				t.Parallel()
				router := setupTrustedProxyRouter(t, lc.apiLimit, lc.loginLimit, tt.trusted)
				var last int
				for i := 1; i <= 3; i++ {
					req := lc.build()
					req.RemoteAddr = peerAddr
					req.Header.Set(tt.header, fmt.Sprintf("203.0.113.%d", i))
					w := httptest.NewRecorder()
					router.ServeHTTP(w, req)
					if i < 3 {
						require.NotEqual(t, http.StatusTooManyRequests, w.Code, "request %d", i)
					}
					last = w.Code
				}
				if tt.wantLast429 {
					assert.Equal(t, http.StatusTooManyRequests, last)
				} else {
					assert.NotEqual(t, http.StatusTooManyRequests, last)
				}
			})
		}
	}
}

// A chain of proxies: Gin walks X-Forwarded-For from the right and skips
// trusted proxies, so a client value that a trusted proxy appended to is the
// client IP, and a value before it from the client itself is not used.
func TestSetupRoutes_TrustedProxyChain(t *testing.T) {
	t.Parallel()
	router := setupTrustedProxyRouter(t, 2, 100, []string{"192.0.2.0/24"})

	var last int
	for i := 1; i <= 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/ui-config", nil)
		req.RemoteAddr = peerAddr
		// The client spoofs the first entry; the proxy appends the real client.
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("198.51.100.%d, 203.0.113.7", i))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		last = w.Code
	}
	assert.Equal(t, http.StatusTooManyRequests, last, "all requests come from the client 203.0.113.7")
}

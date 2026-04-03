package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupRateLimitedRouter(limit int, window time.Duration) (*gin.Engine, *RateLimiter) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	rl := NewRateLimiter(limit, window)
	router.Use(rl.RateLimit())
	router.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	return router, rl
}

func TestRateLimiter_EnforcesLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		limit    int
		requests int
		want429  bool
	}{
		{
			name:     "allows requests within limit",
			limit:    5,
			requests: 5,
			want429:  false,
		},
		{
			name:     "blocks request exceeding limit",
			limit:    5,
			requests: 6,
			want429:  true,
		},
		{
			name:     "limit of 1 blocks second request",
			limit:    1,
			requests: 2,
			want429:  true,
		},
		{
			name:     "limit of 100 blocks 101st request",
			limit:    100,
			requests: 101,
			want429:  true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			router, rl := setupRateLimitedRouter(tt.limit, time.Minute)
			defer rl.Stop()

			var lastCode int
			got429 := false
			for i := 0; i < tt.requests; i++ {
				w := httptest.NewRecorder()
				req, _ := http.NewRequest(http.MethodGet, "/test", nil)
				req.RemoteAddr = "192.0.2.1:12345"
				router.ServeHTTP(w, req)
				lastCode = w.Code
				if w.Code == http.StatusTooManyRequests {
					got429 = true
					break
				}
			}

			if tt.want429 {
				assert.True(t, got429, "expected 429 after %d requests (limit=%d), last code=%d", tt.requests, tt.limit, lastCode)
			} else {
				assert.False(t, got429, "did not expect 429 within %d requests (limit=%d)", tt.requests, tt.limit)
			}
		})
	}
}

func TestRateLimiter_WindowExpiry(t *testing.T) {
	t.Parallel()

	// Use a short window so requests expire quickly.
	limit := 2
	window := 100 * time.Millisecond
	router, rl := setupRateLimitedRouter(limit, window)
	defer rl.Stop()

	// Exhaust the limit.
	for i := 0; i < limit; i++ {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/test", nil)
		req.RemoteAddr = "192.0.2.1:12345"
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	}

	// Next request should be blocked.
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "192.0.2.1:12345"
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusTooManyRequests, w.Code)

	// Wait for the window to expire and retry.
	require.Eventually(t, func() bool {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/test", nil)
		req.RemoteAddr = "192.0.2.1:12345"
		router.ServeHTTP(w, req)
		return w.Code == http.StatusOK
	}, 500*time.Millisecond, 10*time.Millisecond, "request should succeed after window expires")
}

func TestRateLimiter_PerIPIsolation(t *testing.T) {
	t.Parallel()

	limit := 2
	router, rl := setupRateLimitedRouter(limit, time.Minute)
	defer rl.Stop()

	// Exhaust limit for IP1.
	for i := 0; i < limit; i++ {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/test", nil)
		req.RemoteAddr = "192.0.2.1:12345"
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	}

	// IP1 should be blocked.
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "192.0.2.1:12345"
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusTooManyRequests, w.Code)

	// IP2 should still be allowed.
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "192.0.2.2:12345"
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code, "different IP should not be rate limited")
}

func TestRateLimiter_ResponseBody(t *testing.T) {
	t.Parallel()

	router, rl := setupRateLimitedRouter(1, time.Minute)
	defer rl.Stop()

	// First request succeeds.
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "192.0.2.1:12345"
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// Second request returns 429 with error message.
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "192.0.2.1:12345"
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Contains(t, w.Body.String(), "rate limit exceeded")
}

func TestRateLimiter_Stop(t *testing.T) {
	t.Parallel()

	rl := NewRateLimiter(10, time.Minute)
	// Stop should be safe to call multiple times.
	rl.Stop()
	rl.Stop()
}

func TestRateLimiter_LoginThrottle(t *testing.T) {
	t.Parallel()

	// Simulates the login rate limiter: 10 req/min applied to a login endpoint,
	// layered on top of a general API rate limiter (100 req/min).
	gin.SetMode(gin.TestMode)
	router := gin.New()

	apiRL := NewRateLimiter(100, time.Minute)
	defer apiRL.Stop()
	loginRL := NewRateLimiter(10, time.Minute)
	defer loginRL.Stop()

	api := router.Group("/api/v1")
	api.Use(apiRL.RateLimit())

	// Login gets BOTH the API rate limiter (via group) and a stricter per-route limiter.
	api.POST("/auth/login", loginRL.RateLimit(), func(c *gin.Context) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
	})

	// Regular endpoint only has the API rate limiter.
	api.GET("/items", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"items": []string{}})
	})

	// Send 10 login requests — all should pass.
	for i := 0; i < 10; i++ {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
		req.RemoteAddr = "192.0.2.1:12345"
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusUnauthorized, w.Code, "login request %d should pass", i+1)
	}

	// 11th login should be throttled by the login rate limiter.
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	req.RemoteAddr = "192.0.2.1:12345"
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusTooManyRequests, w.Code, "11th login should be rate limited")

	// Regular endpoint should still work (only 11 API requests so far, limit is 100).
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/items", nil)
	req.RemoteAddr = "192.0.2.1:12345"
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code, "regular endpoint should still work")
}

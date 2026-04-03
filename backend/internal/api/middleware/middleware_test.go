package middleware

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

func TestLoggerMiddleware(t *testing.T) {
	// Not parallel: this test mutates the global slog default logger.

	// Create a buffer to capture slog output
	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	origLogger := slog.Default()
	slog.SetDefault(slog.New(handler))
	defer slog.SetDefault(origLogger)

	// Setup router with middleware
	r := gin.New()
	r.Use(Logger())
	r.GET("/test", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	// Create mock request
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "192.0.2.1:1234"

	// Serve request
	r.ServeHTTP(w, req)

	// Assert response status
	assert.Equal(t, http.StatusOK, w.Code)

	// Verify that something was logged
	logOutput := buf.String()
	assert.Contains(t, logOutput, "GET")
	assert.Contains(t, logOutput, "/test")
}

func TestRecoveryMiddleware(t *testing.T) {
	t.Parallel()

	// Setup router with middleware
	r := gin.New()
	r.Use(Recovery())
	r.GET("/panic", func(c *gin.Context) {
		panic("test panic")
	})

	// Create mock request
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/panic", nil)

	// Serve request
	r.ServeHTTP(w, req)

	// Assert that the recovery middleware caught the panic
	assert.Equal(t, http.StatusInternalServerError, w.Code)

	var response map[string]string
	err := json.NewDecoder(w.Body).Decode(&response)
	assert.Nil(t, err)
	assert.Equal(t, "Internal Server Error", response["error"])
}

func TestRequestIDMiddleware(t *testing.T) {
	t.Parallel()

	t.Run("Generates new request ID when none provided", func(t *testing.T) {
		t.Parallel()
		r := gin.New()
		r.Use(RequestID())
		var capturedID string
		r.GET("/test", func(c *gin.Context) {
			if v, ok := c.Get("request_id"); ok {
				capturedID = v.(string)
			}
			c.Status(http.StatusOK)
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/test", nil)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.NotEmpty(t, w.Header().Get("X-Request-ID"))
		assert.NotEmpty(t, capturedID)
		assert.Equal(t, w.Header().Get("X-Request-ID"), capturedID)
	})

	t.Run("Reuses client-provided X-Request-ID", func(t *testing.T) {
		t.Parallel()
		r := gin.New()
		r.Use(RequestID())
		var capturedID string
		r.GET("/test", func(c *gin.Context) {
			if v, ok := c.Get("request_id"); ok {
				capturedID = v.(string)
			}
			c.Status(http.StatusOK)
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/test", nil)
		req.Header.Set("X-Request-ID", "client-id-123")
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "client-id-123", w.Header().Get("X-Request-ID"))
		assert.Equal(t, "client-id-123", capturedID)
	})

	t.Run("Generated IDs are unique", func(t *testing.T) {
		t.Parallel()
		r := gin.New()
		r.Use(RequestID())
		r.GET("/test", func(c *gin.Context) { c.Status(http.StatusOK) })

		ids := make(map[string]bool)
		for i := 0; i < 10; i++ {
			w := httptest.NewRecorder()
			req, _ := http.NewRequest("GET", "/test", nil)
			r.ServeHTTP(w, req)
			id := w.Header().Get("X-Request-ID")
			assert.False(t, ids[id], "duplicate request ID generated")
			ids[id] = true
		}
	})
}

func TestMaxBodySizeMiddleware(t *testing.T) {
	t.Parallel()

	t.Run("Allows request within size limit", func(t *testing.T) {
		t.Parallel()
		r := gin.New()
		r.Use(MaxBodySize(1024)) // 1 KB
		r.POST("/test", func(c *gin.Context) {
			body := make([]byte, 512)
			_, err := c.Request.Body.Read(body)
			if err != nil && !errors.Is(err, io.EOF) {
				c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "body too large"})
				return
			}
			c.Status(http.StatusOK)
		})

		w := httptest.NewRecorder()
		body := strings.NewReader(strings.Repeat("a", 100))
		req, _ := http.NewRequest("POST", "/test", body)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("Rejects request exceeding size limit", func(t *testing.T) {
		t.Parallel()
		r := gin.New()
		r.Use(MaxBodySize(64)) // 64 bytes
		r.POST("/test", func(c *gin.Context) {
			body := make([]byte, 128)
			_, err := c.Request.Body.Read(body)
			if err != nil {
				c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "body too large"})
				return
			}
			c.Status(http.StatusOK)
		})

		w := httptest.NewRecorder()
		body := strings.NewReader(strings.Repeat("a", 128))
		req, _ := http.NewRequest("POST", "/test", body)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	})
}

func TestSecurityHeaders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		forwardedProto string
		expectHSTS     bool
	}{
		{
			name:           "plain HTTP — no HSTS",
			forwardedProto: "",
			expectHSTS:     false,
		},
		{
			name:           "behind TLS proxy — HSTS present",
			forwardedProto: "https",
			expectHSTS:     true,
		},
		{
			name:           "explicit HTTP forwarded proto — no HSTS",
			forwardedProto: "http",
			expectHSTS:     false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := gin.New()
			r.Use(SecurityHeaders())
			r.GET("/test", func(c *gin.Context) {
				c.Status(http.StatusOK)
			})

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("GET", "/test", nil)
			if tt.forwardedProto != "" {
				req.Header.Set("X-Forwarded-Proto", tt.forwardedProto)
			}
			r.ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
			assert.Equal(t, "DENY", w.Header().Get("X-Frame-Options"))
			assert.Equal(t, "strict-origin-when-cross-origin", w.Header().Get("Referrer-Policy"))
			assert.Equal(t, "camera=(), microphone=(), geolocation=()", w.Header().Get("Permissions-Policy"))

			if tt.expectHSTS {
				assert.Equal(t, "max-age=63072000; includeSubDomains", w.Header().Get("Strict-Transport-Security"))
			} else {
				assert.Empty(t, w.Header().Get("Strict-Transport-Security"))
			}
		})
	}
}

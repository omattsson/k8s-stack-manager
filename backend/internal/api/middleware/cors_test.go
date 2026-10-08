package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestCORSMiddleware(t *testing.T) {
	t.Parallel()

	t.Run("Wildcard allows all origins", func(t *testing.T) {
		t.Parallel()
		r := gin.New()
		r.Use(CORS("*"))
		r.Any("/test", func(c *gin.Context) { c.Status(http.StatusOK) })

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/test", nil)
		r.ServeHTTP(w, req)

		assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
		assert.Empty(t, w.Header().Get("Access-Control-Allow-Credentials"))
		assert.Equal(t, "GET, POST, PUT, DELETE, OPTIONS", w.Header().Get("Access-Control-Allow-Methods"))
		assert.Equal(t, "Content-Type, Content-Length, Accept-Encoding, Authorization, X-Request-ID, X-API-Key", w.Header().Get("Access-Control-Allow-Headers"))
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("Wildcard with Origin header still returns star and no credentials", func(t *testing.T) {
		t.Parallel()
		r := gin.New()
		r.Use(CORS("*"))
		r.Any("/test", func(c *gin.Context) { c.Status(http.StatusOK) })

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/test", nil)
		req.Header.Set("Origin", "https://example.com")
		r.ServeHTTP(w, req)

		assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
		assert.Empty(t, w.Header().Get("Access-Control-Allow-Credentials"))
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("Empty string allows all origins", func(t *testing.T) {
		t.Parallel()
		r := gin.New()
		r.Use(CORS(""))
		r.Any("/test", func(c *gin.Context) { c.Status(http.StatusOK) })

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/test", nil)
		r.ServeHTTP(w, req)

		assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
	})

	t.Run("Allowed origin is set from whitelist", func(t *testing.T) {
		t.Parallel()
		r := gin.New()
		r.Use(CORS("https://example.com,https://other.com"))
		r.Any("/test", func(c *gin.Context) { c.Status(http.StatusOK) })

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/test", nil)
		req.Header.Set("Origin", "https://example.com")
		r.ServeHTTP(w, req)

		assert.Equal(t, "https://example.com", w.Header().Get("Access-Control-Allow-Origin"))
		assert.Equal(t, "true", w.Header().Get("Access-Control-Allow-Credentials"))
		assert.Equal(t, "Origin", w.Header().Get("Vary"))
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("Second allowed origin is matched", func(t *testing.T) {
		t.Parallel()
		r := gin.New()
		r.Use(CORS("https://example.com, https://other.com"))
		r.Any("/test", func(c *gin.Context) { c.Status(http.StatusOK) })

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/test", nil)
		req.Header.Set("Origin", "https://other.com")
		r.ServeHTTP(w, req)

		assert.Equal(t, "https://other.com", w.Header().Get("Access-Control-Allow-Origin"))
		assert.Equal(t, "Origin", w.Header().Get("Vary"))
	})

	t.Run("Disallowed origin gets no Access-Control-Allow-Origin", func(t *testing.T) {
		t.Parallel()
		r := gin.New()
		r.Use(CORS("https://example.com"))
		r.Any("/test", func(c *gin.Context) { c.Status(http.StatusOK) })

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/test", nil)
		req.Header.Set("Origin", "https://evil.com")
		r.ServeHTTP(w, req)

		assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
		assert.Empty(t, w.Header().Get("Vary"))
		assert.Empty(t, w.Header().Get("Access-Control-Allow-Methods"))
		assert.Empty(t, w.Header().Get("Access-Control-Allow-Headers"))
		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("No Origin header passes through as non-CORS request", func(t *testing.T) {
		t.Parallel()
		r := gin.New()
		r.Use(CORS("https://example.com"))
		r.Any("/test", func(c *gin.Context) { c.Status(http.StatusOK) })

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/test", nil)
		// No Origin header set — non-browser / same-origin request
		r.ServeHTTP(w, req)

		assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("Disallowed origin OPTIONS preflight returns 403 without CORS headers", func(t *testing.T) {
		t.Parallel()
		r := gin.New()
		r.Use(CORS("https://example.com"))
		r.Any("/test", func(c *gin.Context) { c.Status(http.StatusOK) })

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("OPTIONS", "/test", nil)
		req.Header.Set("Origin", "https://evil.com")
		r.ServeHTTP(w, req)

		assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
		assert.Empty(t, w.Header().Get("Access-Control-Allow-Methods"))
		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("OPTIONS preflight returns 204", func(t *testing.T) {
		t.Parallel()
		r := gin.New()
		r.Use(CORS("*"))
		r.Any("/test", func(c *gin.Context) { c.Status(http.StatusOK) })

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("OPTIONS", "/test", nil)
		r.ServeHTTP(w, req)

		assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
		assert.Equal(t, "GET, POST, PUT, DELETE, OPTIONS", w.Header().Get("Access-Control-Allow-Methods"))
		assert.Equal(t, http.StatusNoContent, w.Code)
	})
}

// TestCORSMiddleware_ExposeHeaders checks that browser code may read
// Content-Disposition (export file name) and X-Request-ID on cross-origin
// responses, for wildcard and explicit origin lists, and on preflight.
func TestCORSMiddleware_ExposeHeaders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		allowed string
		method  string
		origin  string
	}{
		{name: "wildcard GET", allowed: "*", method: http.MethodGet, origin: "https://example.com"},
		{name: "explicit origin GET", allowed: "https://app.example.com", method: http.MethodGet, origin: "https://app.example.com"},
		{name: "explicit origin preflight", allowed: "https://app.example.com", method: http.MethodOptions, origin: "https://app.example.com"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := gin.New()
			r.Use(CORS(tt.allowed))
			r.Any("/test", func(c *gin.Context) {
				c.Header("Content-Disposition", "attachment; filename=stack-values.zip")
				c.Status(http.StatusOK)
			})

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(tt.method, "/test", nil)
			req.Header.Set("Origin", tt.origin)
			r.ServeHTTP(w, req)

			assert.Equal(t, "Content-Disposition, X-Request-ID", w.Header().Get("Access-Control-Expose-Headers"))
			assert.Equal(t, "Content-Type, Content-Length, Accept-Encoding, Authorization, X-Request-ID, X-API-Key", w.Header().Get("Access-Control-Allow-Headers"))
		})
	}
}

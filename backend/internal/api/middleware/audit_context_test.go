package middleware

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

// captureRouter returns a router with RequestID, the given auth keys and a
// handler that stores the captured context in got.
func captureRouter(t *testing.T, proxies []string, auth map[string]string, spanCtx *trace.SpanContext, got *AuditRequestContext) *gin.Engine {
	t.Helper()
	r := gin.New()
	require.NoError(t, r.SetTrustedProxies(proxies))
	r.Use(RequestID())
	r.Use(func(c *gin.Context) {
		for k, v := range auth {
			c.Set(k, v)
		}
		if spanCtx != nil {
			c.Request = c.Request.WithContext(trace.ContextWithSpanContext(c.Request.Context(), *spanCtx))
		}
		c.Next()
	})
	r.POST("/x", func(c *gin.Context) {
		*got = CaptureRequestContext(c)
		c.Status(http.StatusOK)
	})
	return r
}

func TestCaptureRequestContext(t *testing.T) {
	t.Parallel()
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0x4b, 0xf9, 0x2f, 0x35, 0x77, 0xb3, 0x4d, 0xa6, 0xa3, 0xce, 0x92, 0x9d, 0x0e, 0x0e, 0x47, 0x36},
		SpanID:     trace.SpanID{0, 0xf0, 0x67, 0xaa, 0x0b, 0xa9, 0x02, 0xb7},
		TraceFlags: trace.FlagsSampled,
	})
	tests := []struct {
		name       string
		proxies    []string
		remote     string
		xff        string
		auth       map[string]string
		span       *trace.SpanContext
		wantIP     string
		wantMethod string
		wantKey    string
		wantTrace  string
	}{
		{
			name: "jwt", remote: "198.51.100.7:5555",
			auth:   map[string]string{contextKeyAuthMethod: AuthMethodJWT},
			wantIP: "198.51.100.7", wantMethod: "jwt",
		},
		{
			name: "api key", remote: "198.51.100.7:5555",
			auth:   map[string]string{contextKeyAuthMethod: AuthMethodAPIKey, contextKeyAPIKeyID: "key-1"},
			wantIP: "198.51.100.7", wantMethod: "api_key", wantKey: "key-1",
		},
		{
			name: "span", remote: "198.51.100.7:5555", span: &sc,
			wantIP: "198.51.100.7", wantTrace: "4bf92f3577b34da6a3ce929d0e0e4736",
		},
		{
			name: "trusted proxy", proxies: []string{"10.0.0.0/8"}, remote: "10.1.2.3:1234", xff: "203.0.113.9",
			wantIP: "203.0.113.9",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got AuditRequestContext
			r := captureRouter(t, tt.proxies, tt.auth, tt.span, &got)
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/x", nil)
			req.RemoteAddr = tt.remote
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}
			r.ServeHTTP(w, req)
			assert.Equal(t, tt.wantIP, got.ClientIP)
			assert.Equal(t, tt.wantMethod, got.AuthMethod)
			assert.Equal(t, tt.wantKey, got.APIKeyID)
			assert.Equal(t, tt.wantTrace, got.TraceID)
			assert.NotEmpty(t, got.RequestID)
			assert.Equal(t, w.Header().Get("X-Request-ID"), got.RequestID)
		})
	}
}

func TestCaptureRequestContext_UntrustedForwardedFor(t *testing.T) {
	t.Parallel()
	var got AuditRequestContext
	r := captureRouter(t, nil, nil, nil, &got)
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.RemoteAddr = "198.51.100.7:5555"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	req.Header.Set("X-Real-IP", "203.0.113.10")
	r.ServeHTTP(httptest.NewRecorder(), req)
	assert.Equal(t, "198.51.100.7", got.ClientIP)
}

func TestAuditRequestContext_Apply(t *testing.T) {
	t.Parallel()
	rc := AuditRequestContext{ClientIP: "1.2.3.4", RequestID: "r1", TraceID: "t1", AuthMethod: "jwt", APIKeyID: "k1"}
	e := &models.AuditLog{}
	rc.Apply(e)
	assert.Equal(t, models.AuditLog{ClientIP: "1.2.3.4", RequestID: "r1", TraceID: "t1", AuthMethod: "jwt", APIKeyID: "k1"}, *e)

	keep := &models.AuditLog{AuthMethod: models.AuditAuthPassword}
	rc.Apply(keep)
	assert.Equal(t, models.AuditAuthPassword, keep.AuthMethod, "a set auth method stays")
}

func TestRequestID_RejectsBadHeader(t *testing.T) {
	t.Parallel()
	generated := regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	tests := []struct {
		name   string
		header string
		keep   bool
	}{
		{name: "10 KB", header: strings.Repeat("a", 10*1024)},
		{name: "65 bytes", header: strings.Repeat("a", 65)},
		{name: "control characters", header: "abc\r\nX: y"},
		{name: "space", header: "abc def"},
		{name: "unicode", header: "réq"},
		{name: "valid", header: "req-1.a_B", keep: true},
		{name: "64 bytes", header: strings.Repeat("b", 64), keep: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := gin.New()
			r.Use(RequestID())
			var stored string
			r.GET("/x", func(c *gin.Context) {
				stored = c.GetString(RequestIDKey)
				c.Status(http.StatusOK)
			})
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			req.Header["X-Request-Id"] = []string{tt.header}
			r.ServeHTTP(w, req)
			got := w.Header().Get("X-Request-ID")
			assert.Equal(t, got, stored)
			if tt.keep {
				assert.Equal(t, tt.header, got)
				return
			}
			assert.NotEqual(t, tt.header, got)
			assert.Regexp(t, generated, got)
		})
	}
}

func TestTruncateUTF8(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{name: "short", in: "abc", max: 5, want: "abc"},
		{name: "ascii cut", in: "abcdef", max: 3, want: "abc"},
		{name: "cut inside 3-byte rune", in: "ab€", max: 4, want: "ab"},
		{name: "exact rune end", in: "ab€", max: 5, want: "ab€"},
		{name: "invalid utf8", in: "a\xffb\xfe", max: 10, want: "a�b�"},
		{name: "zero", in: "abc", max: 0, want: ""},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := TruncateUTF8(tt.in, tt.max)
			assert.Equal(t, tt.want, got)
			assert.True(t, utf8.ValidString(got))
			assert.LessOrEqual(t, len(got), max(tt.max, 0))
		})
	}
}

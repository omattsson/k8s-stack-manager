package middleware

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockAuditLogger is a thread-safe in-memory audit logger for tests.
type mockAuditLogger struct {
	mu      sync.Mutex
	entries []*models.AuditLog
	err     error
}

func (m *mockAuditLogger) Create(log *models.AuditLog) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.entries = append(m.entries, log)
	return nil
}

func (m *mockAuditLogger) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

func (m *mockAuditLogger) last() *models.AuditLog {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.entries) == 0 {
		return nil
	}
	return m.entries[len(m.entries)-1]
}

func buildAuditRouter(logger *mockAuditLogger, method, path string, status int, responseBody string) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(contextKeyUserID, "test-user-id")
		c.Set(contextKeyUsername, "testuser")
		c.Next()
	})
	r.Use(NewAuditMiddleware(logger))

	handler := func(c *gin.Context) {
		if responseBody != "" {
			c.Data(status, "application/json", []byte(responseBody))
		} else {
			c.Status(status)
		}
	}

	switch method {
	case http.MethodGet:
		r.GET(path, handler)
	case http.MethodPost:
		r.POST(path, handler)
	case http.MethodPut:
		r.PUT(path, handler)
	case http.MethodDelete:
		r.DELETE(path, handler)
	}
	return r
}

// waitForAudit polls until at least 1 audit entry is created or timeout.
func waitForAudit(logger *mockAuditLogger, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if logger.count() > 0 {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func TestAuditMiddleware_SkipsGET(t *testing.T) {
	t.Parallel()
	logger := &mockAuditLogger{}
	router := buildAuditRouter(logger, http.MethodGet, "/api/v1/stack-definitions", http.StatusOK, "")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-definitions", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	// Give goroutine time to run if any — none should be created.
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, 0, logger.count(), "GET requests should not create audit entries")
}

func TestAuditMiddleware_AuditsSuccessfulMutations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method     string
		path       string
		respStatus int
		wantAction string
	}{
		{http.MethodPost, "/api/v1/stack-definitions", http.StatusCreated, "create"},
		{http.MethodPut, "/api/v1/stack-definitions/abc-123", http.StatusOK, "update"},
		{http.MethodDelete, "/api/v1/stack-definitions/abc-123", http.StatusNoContent, "delete"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			t.Parallel()
			logger := &mockAuditLogger{}
			respBody := `{"id":"new-id"}`
			if tt.method != http.MethodPost {
				respBody = ""
			}
			router := buildAuditRouter(logger, tt.method, tt.path, tt.respStatus, respBody)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(tt.method, tt.path, strings.NewReader("{}"))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			require.Equal(t, tt.respStatus, w.Code)
			created := waitForAudit(logger, 200*time.Millisecond)
			require.True(t, created, "audit entry should be created for successful %s", tt.method)

			entry := logger.last()
			require.NotNil(t, entry)
			assert.Equal(t, tt.wantAction, entry.Action)
			assert.Equal(t, "test-user-id", entry.UserID)
			assert.Equal(t, "testuser", entry.Username)
			assert.NotEmpty(t, entry.ID)
		})
	}
}

func TestAuditMiddleware_SkipsFailedRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		method     string
		respStatus int
	}{
		{"POST 400", http.MethodPost, http.StatusBadRequest},
		{"PUT 404", http.MethodPut, http.StatusNotFound},
		{"DELETE 500", http.MethodDelete, http.StatusInternalServerError},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			logger := &mockAuditLogger{}
			router := buildAuditRouter(logger, tt.method, "/api/v1/stack-definitions/x", tt.respStatus, "")

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(tt.method, "/api/v1/stack-definitions/x", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.respStatus, w.Code)
			time.Sleep(20 * time.Millisecond)
			assert.Equal(t, 0, logger.count(), "failed requests should not create audit entries")
		})
	}
}

func TestAuditMiddleware_ExtractsEntityIDFromResponseBody(t *testing.T) {
	t.Parallel()
	logger := &mockAuditLogger{}
	responseBody := `{"id":"generated-uuid-123","name":"my-stack"}`
	router := buildAuditRouter(logger, http.MethodPost, "/api/v1/stack-definitions", http.StatusCreated, responseBody)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-definitions", strings.NewReader("{}"))
	router.ServeHTTP(w, req)

	created := waitForAudit(logger, 200*time.Millisecond)
	require.True(t, created)

	entry := logger.last()
	require.NotNil(t, entry)
	assert.Equal(t, "generated-uuid-123", entry.EntityID)
}

func TestAuditMiddleware_ExtractsEntityIDFromPathParam(t *testing.T) {
	t.Parallel()
	logger := &mockAuditLogger{}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(contextKeyUserID, "u1")
		c.Set(contextKeyUsername, "user1")
		c.Next()
	})
	r.Use(NewAuditMiddleware(logger))
	r.DELETE("/api/v1/stack-definitions/:id", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodDelete, "/api/v1/stack-definitions/def-abc", nil)
	r.ServeHTTP(w, req)

	created := waitForAudit(logger, 200*time.Millisecond)
	require.True(t, created)
	assert.Equal(t, "def-abc", logger.last().EntityID)
}

func TestAuditMiddleware_FireAndForgetDoesNotBlockResponse(t *testing.T) {
	t.Parallel()

	// Logger that blocks for a while — response should still arrive quickly.
	slowLogger := &slowMockLogger{delay: 50 * time.Millisecond}

	r := gin.New()
	r.Use(NewAuditMiddleware(slowLogger))
	r.POST("/test", func(c *gin.Context) {
		c.JSON(http.StatusCreated, gin.H{"id": "1"})
	})

	start := time.Now()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/test", nil)
	r.ServeHTTP(w, req)
	elapsed := time.Since(start)

	assert.Equal(t, http.StatusCreated, w.Code)
	// The response should arrive well before the slow logger's delay (fire-and-forget).
	assert.Less(t, elapsed, 40*time.Millisecond, "audit should not block response")
}

type slowMockLogger struct {
	delay time.Duration
}

func (s *slowMockLogger) Create(_ *models.AuditLog) error {
	time.Sleep(s.delay)
	return nil
}

func TestAuditMiddleware_LogsErrorButDoesNotFail(t *testing.T) {
	t.Parallel()

	logger := &mockAuditLogger{err: errors.New("storage unavailable")}
	router := buildAuditRouter(logger, http.MethodPost, "/api/v1/stack-instances", http.StatusCreated, `{"id":"x"}`)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances", strings.NewReader("{}"))
	router.ServeHTTP(w, req)

	// The response should still be 201 even though the audit logger failed.
	assert.Equal(t, http.StatusCreated, w.Code)
}

func TestExtractEntityType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path string
		want string
	}{
		{"/api/v1/templates", "stack_template"},
		{"/api/v1/templates/:id", "stack_template"},
		{"/api/v1/stack-definitions", "stack_definition"},
		{"/api/v1/stack-definitions/:id", "stack_definition"},
		{"/api/v1/stack-instances", "stack_instance"},
		{"/api/v1/stack-instances/:id", "stack_instance"},
		{"/api/v1/stack-definitions/:id/charts", "chart_config"},
		{"/api/v1/stack-definitions/:id/charts/:chartId", "chart_config"},
		{"/api/v1/stack-instances/:id/overrides", "value_override"},
		{"/api/v1/auth/register", "register"}, // last non-param segment is "register"
		{"/api/v1/auth/:id", "user"},          // last non-param segment is "auth" → "user"
		{"/api/v1/audit-logs", "audit_log"},
		{"/", ""},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, extractEntityType(tt.path))
		})
	}
}

func TestNormalizeEntityType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		segment string
		want    string
	}{
		{"templates", "stack_template"},
		{"stack-definitions", "stack_definition"},
		{"stack-instances", "stack_instance"},
		{"charts", "chart_config"},
		{"overrides", "value_override"},
		{"auth", "user"},
		{"audit-logs", "audit_log"},
		{"unknown-resource", "unknown_resource"},
		{"items", "items"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.segment, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, normalizeEntityType(tt.segment))
		})
	}
}

// Unused import prevention — ensure json is used.
var _ = json.Marshal

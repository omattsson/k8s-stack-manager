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

// TestAuditMiddleware_AuditsUserDelete checks that DELETE /users/:id (issue
// 433) gets an audit entry from the middleware, so the handler writes none.
func TestAuditMiddleware_AuditsUserDelete(t *testing.T) {
	t.Parallel()
	logger := &mockAuditLogger{}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(contextKeyUserID, "admin-1")
		c.Set(contextKeyUsername, "admin")
		c.Next()
	})
	r.Use(NewAuditMiddleware(logger))
	r.DELETE("/api/v1/users/:id", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodDelete, "/api/v1/users/user-target", nil)
	r.ServeHTTP(w, req)

	require.True(t, waitForAudit(logger, 200*time.Millisecond))
	entry := logger.last()
	require.NotNil(t, entry)
	assert.Equal(t, "delete", entry.Action)
	assert.Equal(t, "user", entry.EntityType)
	assert.Equal(t, "user-target", entry.EntityID)
	assert.Equal(t, "admin-1", entry.UserID)
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
		{"/api/v1/stack-instances/:id/overrides/:chartId", "value_override"},
		{"/api/v1/auth/register", "user"}, // normalizeEntityType maps "register" → "user"
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

// TestAuditMiddleware_AuditsValueOverrideDelete checks that DELETE
// /stack-instances/:id/overrides/:chartId (issue 445) gets an audit entry
// with action "delete", entity type "value_override" and the instance ID.
func TestAuditMiddleware_AuditsValueOverrideDelete(t *testing.T) {
	t.Parallel()
	logger := &mockAuditLogger{}
	r := buildAuditRouter(logger, http.MethodDelete, "/api/v1/stack-instances/:id/overrides/:chartId", http.StatusNoContent, "")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodDelete, "/api/v1/stack-instances/inst-1/overrides/chart-1", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusNoContent, w.Code)
	require.True(t, waitForAudit(logger, 200*time.Millisecond))
	entry := logger.last()
	assert.Equal(t, "delete", entry.Action)
	assert.Equal(t, "value_override", entry.EntityType)
	assert.Equal(t, "inst-1", entry.EntityID)
	assert.Equal(t, "test-user-id", entry.UserID)
}

// waitForAuditCount polls until n audit entries exist or the timeout ends.
func waitForAuditCount(logger *mockAuditLogger, n int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if logger.count() >= n {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func (m *mockAuditLogger) all() []*models.AuditLog {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*models.AuditLog, len(m.entries))
	copy(out, m.entries)
	return out
}

// TestAuditRouteFor checks the route table (issue #438): every operation
// that is not plain CRUD gets a real action and entity type, and plain CRUD
// keeps the method-derived action with singular entity names.
func TestAuditRouteFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method     string
		path       string
		wantAction string
		wantEntity string
		wantIDFrom string // path parameter of the entity ID; "" = response or CRUD rule
		wantSkip   bool
		wantBulk   string
	}{
		// Templates.
		{"POST", "/api/v1/templates/:id/publish", "publish", "stack_template", "id", false, ""},
		{"POST", "/api/v1/templates/:id/unpublish", "unpublish", "stack_template", "id", false, ""},
		{"POST", "/api/v1/templates/:id/instantiate", "instantiate", "stack_template", "id", false, ""},
		{"POST", "/api/v1/templates/:id/clone", "clone", "stack_template", "id", false, ""},
		{"POST", "/api/v1/templates/:id/quick-deploy", "", "", "", true, ""},
		{"POST", "/api/v1/templates/bulk/delete", "delete", "stack_template", "", false, "template_id"},
		{"POST", "/api/v1/templates/bulk/publish", "publish", "stack_template", "", false, "template_id"},
		{"POST", "/api/v1/templates/bulk/unpublish", "unpublish", "stack_template", "", false, "template_id"},
		// Definitions.
		{"POST", "/api/v1/stack-definitions/import", "import", "stack_definition", "", false, ""},
		{"POST", "/api/v1/stack-definitions/:id/upgrade", "upgrade", "stack_definition", "id", false, ""},
		// Instances.
		{"POST", "/api/v1/stack-instances/:id/clone", "clone", "stack_instance", "id", false, ""},
		{"POST", "/api/v1/stack-instances/:id/deploy", "deploy", "stack_instance", "id", false, ""},
		{"POST", "/api/v1/stack-instances/:id/stop", "stop", "stack_instance", "id", false, ""},
		{"POST", "/api/v1/stack-instances/:id/clean", "clean", "stack_instance", "id", false, ""},
		{"POST", "/api/v1/stack-instances/:id/rollback", "rollback", "stack_instance", "id", false, ""},
		{"POST", "/api/v1/stack-instances/:id/extend", "extend_ttl", "stack_instance", "id", false, ""},
		{"POST", "/api/v1/stack-instances/:id/follow", "follow", "stack_instance", "id", false, ""},
		{"DELETE", "/api/v1/stack-instances/:id/follow", "unfollow", "stack_instance", "id", false, ""},
		{"POST", "/api/v1/stack-instances/:id/actions/:name", "invoke_action", "stack_instance", "id", false, ""},
		{"POST", "/api/v1/stack-instances/bulk/deploy", "deploy", "stack_instance", "", false, "instance_id"},
		{"POST", "/api/v1/stack-instances/bulk/stop", "stop", "stack_instance", "", false, "instance_id"},
		{"POST", "/api/v1/stack-instances/bulk/clean", "clean", "stack_instance", "", false, "instance_id"},
		{"POST", "/api/v1/stack-instances/bulk/delete", "delete", "stack_instance", "", false, "instance_id"},
		// Users, API keys, admin.
		{"PUT", "/api/v1/users/:id/disable", "disable", "user", "id", false, ""},
		{"PUT", "/api/v1/users/:id/enable", "enable", "user", "id", false, ""},
		{"PUT", "/api/v1/users/:id/password", "reset_password", "user", "id", false, ""},
		{"PUT", "/api/v1/users/:id/role", "change_role", "user", "id", false, ""},
		{"POST", "/api/v1/users/:id/api-keys", "create", "api_key", "", false, ""},
		{"DELETE", "/api/v1/users/:id/api-keys/:keyId", "delete", "api_key", "keyId", false, ""},
		{"DELETE", "/api/v1/admin/orphaned-namespaces/:namespace", "delete", "namespace", "namespace", false, ""},
		{"POST", "/api/v1/admin/cleanup-policies/:id/run", "run", "cleanup_policy", "id", false, ""},
		{"POST", "/api/v1/admin/notification-channels/:id/test", "test", "notification_channel", "id", false, ""},
		{"POST", "/api/v1/clusters/:id/test", "test_connection", "cluster", "id", false, ""},
		{"POST", "/api/v1/clusters/:id/default", "set_default", "cluster", "id", false, ""},
		{"POST", "/api/v1/notifications/:id/read", "", "", "", true, ""},
		{"POST", "/api/v1/notifications/read-all", "", "", "", true, ""},
		{"DELETE", "/api/v1/favorites/:entityType/:entityId", "delete", "favorite", "entityId", false, ""},
		// Plain CRUD keeps the method-derived action; entity names are singular.
		{"POST", "/api/v1/stack-instances", "create", "stack_instance", "", false, ""},
		{"PUT", "/api/v1/stack-instances/:id", "update", "stack_instance", "", false, ""},
		{"DELETE", "/api/v1/templates/:id", "delete", "stack_template", "", false, ""},
		{"PUT", "/api/v1/stack-instances/:id/branches/:chartId", "update", "branch_override", "", false, ""},
		{"PUT", "/api/v1/stack-instances/:id/quota-overrides", "update", "quota_override", "", false, ""},
		{"POST", "/api/v1/clusters", "create", "cluster", "", false, ""},
		{"PUT", "/api/v1/clusters/:id/quotas", "update", "quota", "", false, ""},
		{"POST", "/api/v1/admin/cleanup-policies", "create", "cleanup_policy", "", false, ""},
		{"POST", "/api/v1/admin/notification-channels", "create", "notification_channel", "", false, ""},
		{"PUT", "/api/v1/admin/notification-channels/:id/subscriptions", "update", "notification_subscription", "", false, ""},
		{"PUT", "/api/v1/notifications/preferences", "update", "notification_preference", "", false, ""},
		{"POST", "/api/v1/favorites", "create", "favorite", "", false, ""},
		{"POST", "/api/v1/clusters/:id/shared-values", "create", "shared_values", "", false, ""},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			t.Parallel()
			got := AuditRouteFor(tt.method, tt.path)
			assert.Equal(t, tt.wantSkip, got.Skip)
			if tt.wantSkip {
				return
			}
			assert.Equal(t, tt.wantAction, got.Action)
			assert.Equal(t, tt.wantEntity, got.EntityType)
			assert.Equal(t, tt.wantIDFrom, got.IDParam)
			assert.Equal(t, tt.wantBulk, got.BulkIDField)
			assert.Contains(t, KnownAuditActions, got.Action)
			assert.Contains(t, KnownAuditEntityTypes, got.EntityType)
		})
	}
}

// TestAuditRoutes_AllKnown checks that every route table entry uses a known
// action and entity type (the audit log page lists them as filters).
func TestAuditRoutes_AllKnown(t *testing.T) {
	t.Parallel()
	for key, r := range auditRoutes {
		if r.Skip {
			continue
		}
		assert.Contains(t, KnownAuditActions, r.Action, key)
		assert.Contains(t, KnownAuditEntityTypes, r.EntityType, key)
	}
}

// TestAuditMiddleware_RouteTableEntries runs requests through the middleware
// and checks the stored entry.
func TestAuditMiddleware_RouteTableEntries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		method      string
		route       string
		url         string
		status      int
		body        string
		wantAction  string
		wantEntity  string
		wantID      string
		wantDetails map[string]any
	}{
		{
			name: "deploy", method: http.MethodPost, route: "/api/v1/stack-instances/:id/deploy",
			url: "/api/v1/stack-instances/inst-1/deploy", status: http.StatusAccepted,
			body:       `{"log_id":"log-9","message":"Deployment started"}`,
			wantAction: "deploy", wantEntity: "stack_instance", wantID: "inst-1",
			wantDetails: map[string]any{"log_id": "log-9"},
		},
		{
			name: "invoke action", method: http.MethodPost, route: "/api/v1/stack-instances/:id/actions/:name",
			url: "/api/v1/stack-instances/inst-2/actions/refresh-db", status: http.StatusOK, body: `{"status_code":200}`,
			wantAction: "invoke_action", wantEntity: "stack_instance", wantID: "inst-2",
			wantDetails: map[string]any{"action": "refresh-db"},
		},
		{
			name: "extend", method: http.MethodPost, route: "/api/v1/stack-instances/:id/extend",
			url: "/api/v1/stack-instances/inst-3/extend", status: http.StatusOK, body: `{"id":"inst-3"}`,
			wantAction: "extend_ttl", wantEntity: "stack_instance", wantID: "inst-3",
		},
		{
			name: "template clone records the clone ID", method: http.MethodPost, route: "/api/v1/templates/:id/clone",
			url: "/api/v1/templates/t1/clone", status: http.StatusCreated, body: `{"id":"t2","name":"copy"}`,
			wantAction: "clone", wantEntity: "stack_template", wantID: "t1",
			wantDetails: map[string]any{"clone_id": "t2"},
		},
		{
			name: "definition import takes the ID of the response", method: http.MethodPost, route: "/api/v1/stack-definitions/import",
			url: "/api/v1/stack-definitions/import", status: http.StatusCreated, body: `{"id":"def-7","charts":[]}`,
			wantAction: "import", wantEntity: "stack_definition", wantID: "def-7",
		},
		{
			name: "cleanup policy run records dry_run", method: http.MethodPost, route: "/api/v1/admin/cleanup-policies/:id/run",
			url: "/api/v1/admin/cleanup-policies/p1/run?dry_run=true", status: http.StatusOK, body: `[]`,
			wantAction: "run", wantEntity: "cleanup_policy", wantID: "p1",
			wantDetails: map[string]any{"dry_run": "true"},
		},
		{
			name: "API key delete uses the key ID", method: http.MethodDelete, route: "/api/v1/users/:id/api-keys/:keyId",
			url: "/api/v1/users/u1/api-keys/k9", status: http.StatusNoContent,
			wantAction: "delete", wantEntity: "api_key", wantID: "k9",
			wantDetails: map[string]any{"user_id": "u1"},
		},
		{
			name: "user role change records the old and the new role", method: http.MethodPut, route: "/api/v1/users/:id/role",
			url: "/api/v1/users/u2/role", status: http.StatusOK,
			body:       `{"id":"u2","old_role":"user","new_role":"devops","changed":true,"message":"Role changed"}`,
			wantAction: "change_role", wantEntity: "user", wantID: "u2",
			wantDetails: map[string]any{"old_role": "user", "new_role": "devops"},
		},
		{
			name: "cluster test connection", method: http.MethodPost, route: "/api/v1/clusters/:id/test",
			url: "/api/v1/clusters/c1/test", status: http.StatusOK, body: `{"success":true}`,
			wantAction: "test_connection", wantEntity: "cluster", wantID: "c1",
		},
		{
			name: "orphaned namespace delete uses the namespace as entity ID", method: http.MethodDelete, route: "/api/v1/admin/orphaned-namespaces/:namespace",
			url: "/api/v1/admin/orphaned-namespaces/stack-demo-alice", status: http.StatusOK, body: `{"message":"deleted"}`,
			wantAction: "delete", wantEntity: "namespace", wantID: "stack-demo-alice",
		},
		{
			name: "nested shared values update keeps the cluster ID and records the value ID", method: http.MethodPut, route: "/api/v1/clusters/:id/shared-values/:valueId",
			url: "/api/v1/clusters/c1/shared-values/sv9", status: http.StatusOK, body: `{"id":"sv9"}`,
			wantAction: "update", wantEntity: "shared_values", wantID: "c1",
			wantDetails: map[string]any{"value_id": "sv9"},
		},
		{
			name: "nested template chart delete records the chart ID", method: http.MethodDelete, route: "/api/v1/templates/:id/charts/:chartId",
			url: "/api/v1/templates/t1/charts/ch2", status: http.StatusNoContent,
			wantAction: "delete", wantEntity: "chart_config", wantID: "t1",
			wantDetails: map[string]any{"chart_id": "ch2"},
		},
		{
			name: "nested definition chart update records the chart ID", method: http.MethodPut, route: "/api/v1/stack-definitions/:id/charts/:chartId",
			url: "/api/v1/stack-definitions/d1/charts/ch3", status: http.StatusOK, body: `{"id":"ch3"}`,
			wantAction: "update", wantEntity: "chart_config", wantID: "d1",
			wantDetails: map[string]any{"chart_id": "ch3"},
		},
		{
			name: "nested value override records the chart ID", method: http.MethodPut, route: "/api/v1/stack-instances/:id/overrides/:chartId",
			url: "/api/v1/stack-instances/i1/overrides/ch4", status: http.StatusOK, body: `{"id":"ov1"}`,
			wantAction: "update", wantEntity: "value_override", wantID: "i1",
			wantDetails: map[string]any{"chart_id": "ch4"},
		},
		{
			name: "nested create records the created child ID", method: http.MethodPost, route: "/api/v1/clusters/:id/shared-values",
			url: "/api/v1/clusters/c1/shared-values", status: http.StatusCreated, body: `{"id":"sv10"}`,
			wantAction: "create", wantEntity: "shared_values", wantID: "c1",
			wantDetails: map[string]any{"created_id": "sv10"},
		},
		{
			name: "plain CRUD create keeps the old rule", method: http.MethodPost, route: "/api/v1/clusters",
			url: "/api/v1/clusters", status: http.StatusCreated, body: `{"id":"c5"}`,
			wantAction: "create", wantEntity: "cluster", wantID: "c5",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			logger := &mockAuditLogger{}
			router := buildAuditRouter(logger, tt.method, tt.route, tt.status, tt.body)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(tt.method, tt.url, nil)
			router.ServeHTTP(w, req)
			require.Equal(t, tt.status, w.Code)

			require.True(t, waitForAudit(logger, time.Second), "audit entry expected")
			entry := logger.last()
			assert.Equal(t, tt.wantAction, entry.Action)
			assert.Equal(t, tt.wantEntity, entry.EntityType)
			assert.Equal(t, tt.wantID, entry.EntityID)
			assert.Equal(t, "test-user-id", entry.UserID)
			if tt.wantDetails == nil {
				assert.Empty(t, entry.Details)
				return
			}
			var details map[string]any
			require.NoError(t, json.Unmarshal([]byte(entry.Details), &details))
			assert.Equal(t, tt.wantDetails, details)
		})
	}
}

// TestAuditMiddleware_SkippedRoutes checks that notification read state and
// quick deploy (the handler writes its own entry) get no middleware entry.
func TestAuditMiddleware_SkippedRoutes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		route string
		url   string
	}{
		{"/api/v1/notifications/:id/read", "/api/v1/notifications/n1/read"},
		{"/api/v1/notifications/read-all", "/api/v1/notifications/read-all"},
		{"/api/v1/templates/:id/quick-deploy", "/api/v1/templates/t1/quick-deploy"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.route, func(t *testing.T) {
			t.Parallel()
			logger := &mockAuditLogger{}
			router := buildAuditRouter(logger, http.MethodPost, tt.route, http.StatusOK, `{"id":"x"}`)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, tt.url, nil)
			router.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code)
			time.Sleep(50 * time.Millisecond)
			assert.Zero(t, logger.count())
		})
	}
}

// TestAuditMiddleware_BulkWritesOneEntryPerSuccess checks that a bulk
// operation records each changed instance, so a filter on the instance ID
// finds it. Failed items get no entry.
func TestAuditMiddleware_BulkWritesOneEntryPerSuccess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		route      string
		body       string
		wantAction string
		wantEntity string
		wantIDs    []string
		wantLogIDs map[string]string
	}{
		{
			name:  "instances",
			route: "/api/v1/stack-instances/bulk/deploy",
			body: `{"total":3,"succeeded":2,"failed":1,"results":[` +
				`{"instance_id":"i1","status":"success","log_id":"l1"},` +
				`{"instance_id":"i2","status":"error","error":"Instance not found"},` +
				`{"instance_id":"i3","status":"success","log_id":"l3"}]}`,
			wantAction: "deploy", wantEntity: "stack_instance",
			wantIDs: []string{"i1", "i3"}, wantLogIDs: map[string]string{"i1": "l1", "i3": "l3"},
		},
		{
			name:       "templates",
			route:      "/api/v1/templates/bulk/delete",
			body:       `{"total":1,"succeeded":1,"failed":0,"results":[{"template_id":"t1","status":"success"}]}`,
			wantAction: "delete", wantEntity: "stack_template",
			wantIDs: []string{"t1"},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			logger := &mockAuditLogger{}
			router := buildAuditRouter(logger, http.MethodPost, tt.route, http.StatusOK, tt.body)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, tt.route, nil)
			router.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code)

			require.True(t, waitForAuditCount(logger, len(tt.wantIDs), time.Second))
			time.Sleep(20 * time.Millisecond)
			entries := logger.all()
			require.Len(t, entries, len(tt.wantIDs))
			var ids []string
			for _, e := range entries {
				ids = append(ids, e.EntityID)
				assert.Equal(t, tt.wantAction, e.Action)
				assert.Equal(t, tt.wantEntity, e.EntityType)
				var details map[string]any
				require.NoError(t, json.Unmarshal([]byte(e.Details), &details))
				assert.Equal(t, true, details["bulk"])
				if want, ok := tt.wantLogIDs[e.EntityID]; ok {
					assert.Equal(t, want, details["log_id"])
				}
			}
			assert.ElementsMatch(t, tt.wantIDs, ids)
		})
	}
}

func TestSnakeCase(t *testing.T) {
	t.Parallel()
	tests := map[string]string{"chartId": "chart_id", "valueId": "value_id", "keyId": "key_id", "name": "name", "entityType": "entity_type"}
	for in, want := range tests {
		in, want := in, want
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, want, snakeCase(in))
		})
	}
}

func TestTruncateEntityID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "uuid stays", in: "3f491941-283f-4429-8c6b-b1cd52ab5f6e", want: "3f491941-283f-4429-8c6b-b1cd52ab5f6e"},
		{name: "63 characters stay", in: strings.Repeat("a", 63), want: strings.Repeat("a", 63)},
		{name: "longer value is cut to 63", in: strings.Repeat("a", 80), want: strings.Repeat("a", 63)},
		{name: "cut at a rune start", in: strings.Repeat("a", 62) + "é", want: strings.Repeat("a", 62)},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := truncateEntityID(tt.in)
			assert.Equal(t, tt.want, got)
			assert.LessOrEqual(t, len(got), models.MaxAuditEntityIDLen)
		})
	}
}

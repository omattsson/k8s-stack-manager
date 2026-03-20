package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupAuditLogRouter(auditRepo *MockAuditLogRepository) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewAuditLogHandler(auditRepo)
	r.GET("/api/v1/audit-logs", h.ListAuditLogs)
	return r
}

// seedAuditLog inserts an AuditLog into the mock repo.
func seedAuditLog(t *testing.T, repo *MockAuditLogRepository, id, userID, action, entityType, entityID string) {
	t.Helper()
	require.NoError(t, repo.Create(&models.AuditLog{
		ID:         id,
		UserID:     userID,
		Username:   "testuser",
		Action:     action,
		EntityType: entityType,
		EntityID:   entityID,
		Timestamp:  time.Now().UTC(),
	}))
}

func TestListAuditLogs(t *testing.T) {
	t.Parallel()

	t.Run("returns all logs with no filters", func(t *testing.T) {
		t.Parallel()
		repo := NewMockAuditLogRepository()
		seedAuditLog(t, repo, "log-1", "uid-1", "create", "stack_definition", "def-1")
		seedAuditLog(t, repo, "log-2", "uid-2", "delete", "stack_instance", "inst-1")

		router := setupAuditLogRouter(repo)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/audit-logs", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp models.PaginatedAuditLogs
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Len(t, resp.Data, 2)
		assert.Equal(t, int64(2), resp.Total)
		assert.Equal(t, 25, resp.Limit)
		assert.Equal(t, 0, resp.Offset)
	})

	t.Run("filters by user_id", func(t *testing.T) {
		t.Parallel()
		repo := NewMockAuditLogRepository()
		seedAuditLog(t, repo, "log-1", "uid-1", "create", "stack_definition", "def-1")
		seedAuditLog(t, repo, "log-2", "uid-2", "create", "stack_definition", "def-2")

		router := setupAuditLogRouter(repo)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/audit-logs?user_id=uid-1", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp models.PaginatedAuditLogs
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Len(t, resp.Data, 1)
		assert.Equal(t, int64(1), resp.Total)
		assert.Equal(t, "uid-1", resp.Data[0].UserID)
	})

	t.Run("filters by action", func(t *testing.T) {
		t.Parallel()
		repo := NewMockAuditLogRepository()
		seedAuditLog(t, repo, "log-1", "uid-1", "create", "stack_definition", "def-1")
		seedAuditLog(t, repo, "log-2", "uid-1", "delete", "stack_definition", "def-1")

		router := setupAuditLogRouter(repo)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/audit-logs?action=delete", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp models.PaginatedAuditLogs
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Len(t, resp.Data, 1)
		assert.Equal(t, "delete", resp.Data[0].Action)
	})

	t.Run("filters by entity_type", func(t *testing.T) {
		t.Parallel()
		repo := NewMockAuditLogRepository()
		seedAuditLog(t, repo, "log-1", "uid-1", "create", "stack_definition", "def-1")
		seedAuditLog(t, repo, "log-2", "uid-1", "create", "stack_instance", "inst-1")

		router := setupAuditLogRouter(repo)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/audit-logs?entity_type=stack_instance", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp models.PaginatedAuditLogs
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Len(t, resp.Data, 1)
		assert.Equal(t, "stack_instance", resp.Data[0].EntityType)
	})

	t.Run("invalid start_date returns 400", func(t *testing.T) {
		t.Parallel()
		repo := NewMockAuditLogRepository()
		router := setupAuditLogRouter(repo)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/audit-logs?start_date=not-a-date", nil)
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("invalid end_date returns 400", func(t *testing.T) {
		t.Parallel()
		repo := NewMockAuditLogRepository()
		router := setupAuditLogRouter(repo)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/audit-logs?end_date=2024-01-99", nil)
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("valid RFC3339 date filters are accepted", func(t *testing.T) {
		t.Parallel()
		repo := NewMockAuditLogRepository()
		seedAuditLog(t, repo, "log-1", "uid-1", "create", "stack_definition", "def-1")

		router := setupAuditLogRouter(repo)
		start := "2024-01-01T00:00:00Z"
		end := "2030-12-31T23:59:59Z"
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/audit-logs?start_date=%s&end_date=%s", start, end), nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("repository error returns 500", func(t *testing.T) {
		t.Parallel()
		repo := NewMockAuditLogRepository()
		repo.SetError(errInternal)

		router := setupAuditLogRouter(repo)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/audit-logs", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})

	t.Run("pagination with limit and offset", func(t *testing.T) {
		t.Parallel()
		repo := NewMockAuditLogRepository()
		for i := 0; i < 10; i++ {
			seedAuditLog(t, repo, fmt.Sprintf("log-%d", i), "uid-1", "create", "stack_definition", fmt.Sprintf("def-%d", i))
		}

		router := setupAuditLogRouter(repo)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/audit-logs?limit=3&offset=2", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp models.PaginatedAuditLogs
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Len(t, resp.Data, 3)
		assert.Equal(t, int64(10), resp.Total)
		assert.Equal(t, 3, resp.Limit)
		assert.Equal(t, 2, resp.Offset)
	})

	t.Run("invalid limit returns 400", func(t *testing.T) {
		t.Parallel()
		repo := NewMockAuditLogRepository()
		router := setupAuditLogRouter(repo)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/audit-logs?limit=abc", nil)
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("invalid offset returns 400", func(t *testing.T) {
		t.Parallel()
		repo := NewMockAuditLogRepository()
		router := setupAuditLogRouter(repo)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/audit-logs?offset=-1", nil)
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

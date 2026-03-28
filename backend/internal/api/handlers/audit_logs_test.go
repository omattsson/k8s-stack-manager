package handlers

import (
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
	r.GET("/api/v1/audit-logs/export", h.ExportAuditLogs)
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

	t.Run("cursor-based pagination returns next_cursor when more results", func(t *testing.T) {
		t.Parallel()
		repo := NewMockAuditLogRepository()
		for i := 0; i < 5; i++ {
			seedAuditLog(t, repo, fmt.Sprintf("log-%d", i), "uid-1", "create", "stack_definition", fmt.Sprintf("def-%d", i))
		}

		router := setupAuditLogRouter(repo)
		w := httptest.NewRecorder()
		// Request page of 2 starting from cursor "log-0" (should skip log-0, return log-1 and log-2)
		cursor := base64.StdEncoding.EncodeToString([]byte("mock|log-0"))
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/audit-logs?limit=2&cursor="+cursor, nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp models.PaginatedAuditLogs
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Len(t, resp.Data, 2)
		assert.Equal(t, "log-1", resp.Data[0].ID)
		assert.Equal(t, "log-2", resp.Data[1].ID)
		assert.NotEmpty(t, resp.NextCursor, "expected next_cursor when more results exist")
		assert.Equal(t, int64(-1), resp.Total, "total should be -1 in cursor mode")
	})

	t.Run("cursor-based pagination returns empty next_cursor on last page", func(t *testing.T) {
		t.Parallel()
		repo := NewMockAuditLogRepository()
		for i := 0; i < 3; i++ {
			seedAuditLog(t, repo, fmt.Sprintf("log-%d", i), "uid-1", "create", "stack_definition", fmt.Sprintf("def-%d", i))
		}

		router := setupAuditLogRouter(repo)
		w := httptest.NewRecorder()
		// Request page of 10 starting from cursor "log-0" (only 2 remain: log-1, log-2)
		cursor := base64.StdEncoding.EncodeToString([]byte("mock|log-0"))
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/audit-logs?limit=10&cursor="+cursor, nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp models.PaginatedAuditLogs
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Len(t, resp.Data, 2)
		assert.Empty(t, resp.NextCursor, "expected no next_cursor on last page")
	})

	t.Run("no cursor uses offset/limit pagination", func(t *testing.T) {
		t.Parallel()
		repo := NewMockAuditLogRepository()
		for i := 0; i < 5; i++ {
			seedAuditLog(t, repo, fmt.Sprintf("log-%d", i), "uid-1", "create", "stack_definition", fmt.Sprintf("def-%d", i))
		}

		router := setupAuditLogRouter(repo)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/audit-logs?limit=2&offset=1", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp models.PaginatedAuditLogs
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Len(t, resp.Data, 2)
		assert.Equal(t, int64(5), resp.Total, "total should be known in offset mode")
		assert.Empty(t, resp.NextCursor, "no next_cursor in offset mode")
	})
}

func TestExportAuditLogs(t *testing.T) {
	t.Parallel()

	t.Run("export as JSON returns valid JSON array", func(t *testing.T) {
		t.Parallel()
		repo := NewMockAuditLogRepository()
		seedAuditLog(t, repo, "log-1", "uid-1", "create", "stack_definition", "def-1")
		seedAuditLog(t, repo, "log-2", "uid-2", "delete", "stack_instance", "inst-1")

		router := setupAuditLogRouter(repo)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/audit-logs/export?format=json", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
		assert.Contains(t, w.Header().Get("Content-Disposition"), "attachment; filename=audit-logs-")
		assert.Contains(t, w.Header().Get("Content-Disposition"), ".json")

		var logs []models.AuditLog
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &logs))
		assert.Len(t, logs, 2)
	})

	t.Run("export defaults to JSON when format omitted", func(t *testing.T) {
		t.Parallel()
		repo := NewMockAuditLogRepository()
		seedAuditLog(t, repo, "log-1", "uid-1", "create", "stack_definition", "def-1")

		router := setupAuditLogRouter(repo)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/audit-logs/export", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

		var logs []models.AuditLog
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &logs))
		assert.Len(t, logs, 1)
	})

	t.Run("export as CSV returns valid CSV with headers", func(t *testing.T) {
		t.Parallel()
		repo := NewMockAuditLogRepository()
		seedAuditLog(t, repo, "log-1", "uid-1", "create", "stack_definition", "def-1")

		router := setupAuditLogRouter(repo)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/audit-logs/export?format=csv", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "text/csv", w.Header().Get("Content-Type"))
		assert.Contains(t, w.Header().Get("Content-Disposition"), "attachment; filename=audit-logs-")
		assert.Contains(t, w.Header().Get("Content-Disposition"), ".csv")

		reader := csv.NewReader(strings.NewReader(w.Body.String()))
		records, err := reader.ReadAll()
		require.NoError(t, err)
		assert.Len(t, records, 2) // 1 header + 1 data row
		assert.Equal(t, []string{"ID", "Timestamp", "UserID", "Username", "Action", "EntityType", "EntityID", "Details"}, records[0])
		assert.Equal(t, "log-1", records[1][0])
		assert.Equal(t, "uid-1", records[1][2])
		assert.Equal(t, "create", records[1][4])
	})

	t.Run("export with filters passes filters to repo", func(t *testing.T) {
		t.Parallel()
		repo := NewMockAuditLogRepository()
		seedAuditLog(t, repo, "log-1", "uid-1", "create", "stack_definition", "def-1")
		seedAuditLog(t, repo, "log-2", "uid-2", "delete", "stack_instance", "inst-1")

		router := setupAuditLogRouter(repo)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/audit-logs/export?format=json&user_id=uid-1", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var logs []models.AuditLog
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &logs))
		assert.Len(t, logs, 1)
		assert.Equal(t, "uid-1", logs[0].UserID)
	})

	t.Run("export with invalid format returns 400", func(t *testing.T) {
		t.Parallel()
		repo := NewMockAuditLogRepository()
		router := setupAuditLogRouter(repo)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/audit-logs/export?format=xml", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		var resp map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Contains(t, resp["error"], "Invalid format")
	})

	t.Run("export with invalid start_date returns 400", func(t *testing.T) {
		t.Parallel()
		repo := NewMockAuditLogRepository()
		router := setupAuditLogRouter(repo)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/audit-logs/export?start_date=not-a-date", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("export with invalid end_date returns 400", func(t *testing.T) {
		t.Parallel()
		repo := NewMockAuditLogRepository()
		router := setupAuditLogRouter(repo)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/audit-logs/export?end_date=2024-01-99", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("export with repository error returns 500", func(t *testing.T) {
		t.Parallel()
		repo := NewMockAuditLogRepository()
		repo.SetError(errInternal)

		router := setupAuditLogRouter(repo)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/audit-logs/export", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
		var resp map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "Internal server error", resp["error"])
	})

	t.Run("export respects hard max limit", func(t *testing.T) {
		t.Parallel()
		repo := NewMockAuditLogRepository()
		for i := 0; i < 5; i++ {
			seedAuditLog(t, repo, fmt.Sprintf("log-%d", i), "uid-1", "create", "stack_definition", fmt.Sprintf("def-%d", i))
		}

		router := setupAuditLogRouter(repo)
		w := httptest.NewRecorder()
		// Client-provided limit/offset should be overridden for export
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/audit-logs/export?format=json&limit=2&offset=3", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var logs []models.AuditLog
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &logs))
		// All 5 logs should be returned, not just 2 starting at offset 3
		assert.Len(t, logs, 5)
	})
}

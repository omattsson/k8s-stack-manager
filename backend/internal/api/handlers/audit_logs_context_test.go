package handlers

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedContextAuditLogs(t *testing.T, repo *MockAuditLogRepository) {
	t.Helper()
	now := time.Now().UTC()
	for i, e := range []models.AuditLog{
		{ID: "a1", UserID: "u1", Username: "alice", Action: "create", EntityType: "stack_instance", EntityID: "i1",
			EntityName: "stack-a", ClientIP: "198.51.100.7", RequestID: "r1", TraceID: "t1", AuthMethod: "api_key", APIKeyID: "k1"},
		{ID: "a2", UserID: "u2", Username: "bob", Action: "delete", EntityType: "stack_instance", EntityID: "i2"},
	} {
		e.Timestamp = now.Add(time.Duration(i) * time.Second)
		require.NoError(t, repo.Create(context.Background(), &e))
	}
}

func TestListAuditLogs_ContextFilters(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		query   string
		wantIDs []string
	}{
		{name: "username", query: "?username=alice", wantIDs: []string{"a1"}},
		{name: "api key", query: "?api_key_id=k1", wantIDs: []string{"a1"}},
		{name: "unknown username", query: "?username=mallory"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := NewMockAuditLogRepository()
			seedContextAuditLogs(t, repo)
			w := httptest.NewRecorder()
			setupAuditLogRouter(repo).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/audit-logs"+tt.query, nil))
			require.Equal(t, http.StatusOK, w.Code)
			assert.True(t, validateJSONSchema(t, auditLogListSchema, w.Body.Bytes()))
			var resp models.PaginatedAuditLogs
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			var ids []string
			for _, e := range resp.Data {
				ids = append(ids, e.ID)
			}
			assert.Equal(t, tt.wantIDs, ids)
		})
	}
}

func TestExportAuditLogs_CSVColumns(t *testing.T) {
	t.Parallel()
	repo := NewMockAuditLogRepository()
	seedContextAuditLogs(t, repo)
	w := httptest.NewRecorder()
	setupAuditLogRouter(repo).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/audit-logs/export?format=csv&username=alice", nil))
	require.Equal(t, http.StatusOK, w.Code)
	rows, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, []string{"ID", "Timestamp", "UserID", "Username", "AuthMethod", "APIKeyID", "ClientIP", "Action",
		"EntityType", "EntityID", "EntityName", "RequestID", "TraceID", "Details"}, rows[0])
	assert.Equal(t, []string{"a1", rows[1][1], "u1", "alice", "api_key", "k1", "198.51.100.7", "create",
		"stack_instance", "i1", "stack-a", "r1", "t1", ""}, rows[1])
}

func TestExportAuditLogs_CSVFormulaEscaped(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{`=HYPERLINK("x")`, `'=HYPERLINK("x")`},
		{"+1", "'+1"},
		{"-1", "'-1"},
		{"@SUM(A1)", "'@SUM(A1)"},
		{"\tx", "'\tx"},
		{"\rx", "'\rx"},
		{"plain", "plain"},
		{"", ""},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			repo := NewMockAuditLogRepository()
			require.NoError(t, repo.Create(context.Background(), &models.AuditLog{ID: "a1", Action: "create", EntityName: tt.in, Timestamp: time.Now()}))
			w := httptest.NewRecorder()
			setupAuditLogRouter(repo).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/audit-logs/export?format=csv", nil))
			rows, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
			require.NoError(t, err)
			require.Len(t, rows, 2)
			assert.Equal(t, tt.want, rows[1][10])
		})
	}
}

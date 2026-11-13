package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeleteInstance_AuditNameOnAsyncDelete: a delete that cleans first
// answers 202; its entry must still name the instance (PR 790 review).
func TestDeleteInstance_AuditNameOnAsyncDelete(t *testing.T) {
	t.Parallel()
	f := newWriteCtxFixture(t)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/v1/stack-instances/run-1", nil))
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	var entry models.AuditLog
	require.Eventually(t, func() bool {
		res, _ := f.audit.List(context.Background(), models.AuditLogFilters{Action: "delete", Limit: 10})
		if res == nil || len(res.Data) == 0 {
			return false
		}
		entry = res.Data[0]
		return true
	}, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, "run-1", entry.EntityID)
	assert.Equal(t, "run-1", entry.EntityName)
}

// TestLogout_AuditBearerWithoutSession: a bearer token without a session
// (the SSO CLI token) still gives a logout entry (PR 790 review).
func TestLogout_AuditBearerWithoutSession(t *testing.T) {
	t.Parallel()
	f := newAuthAuditFixture(t, nil)
	token, err := middleware.GenerateTokenWithOpts(middleware.GenerateTokenOptions{
		UserID: "uid-1", Username: "alice", Role: "user",
		Secret: testAuthConfigWithRefresh().JWTSecret, Expiration: time.Hour,
	})
	require.NoError(t, err)
	f.post(t, "/api/v1/auth/logout", "", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) })
	e := f.audit.one(t)
	assert.Equal(t, "logout", e.Action)
	assert.Equal(t, "uid-1", e.UserID)
	assert.Equal(t, "alice", e.Username)
}

// TestOverrideRemoval_AuditKeyPaths: clearing or deleting an override records
// the removed key paths (PR 790 review).
func TestOverrideRemoval_AuditKeyPaths(t *testing.T) {
	t.Parallel()
	build := func(mw gin.HandlerFunc) *gin.Engine {
		insts, overrides := NewMockStackInstanceRepository(), NewMockValueOverrideRepository()
		defs, charts := NewMockStackDefinitionRepository(), NewMockChartConfigRepository()
		_ = defs.Create(context.Background(), &models.StackDefinition{ID: "def-1", Name: "shop", OwnerID: "u-1"})
		_ = charts.Create(context.Background(), &models.ChartConfig{ID: "chart-1", StackDefinitionID: "def-1", ChartName: "app"})
		_ = insts.Create(context.Background(), &models.StackInstance{ID: "i-1", Name: "stack-a", OwnerID: "u-1", StackDefinitionID: "def-1", Status: models.StackStatusDraft})
		_ = overrides.Create(context.Background(), &models.ValueOverride{ID: "o-1", StackInstanceID: "i-1", ChartConfigID: "chart-1", Values: "replicas: 1\nimage:\n  tag: v1"})
		r := setupInstanceRouter(insts, overrides, defs, charts, NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), "u-1", "alice", "user", mw)
		h := NewInstanceHandler(insts, overrides, NewMockChartBranchOverrideRepository(), defs, charts, nil, nil, nil, NewMockUserRepository(), 0)
		r.DELETE("/api/v1/stack-instances/:id/overrides/:chartId", h.DeleteOverride)
		return r
	}
	for _, tt := range []struct{ name, method, body string }{
		{"put empty values", http.MethodPut, `{"values":""}`},
		{"delete", http.MethodDelete, ""},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			details, _, _ := auditedRequest(t, build, tt.method, "/api/v1/stack-instances/i-1/overrides/chart-1", tt.body)
			ch := changeMap(t, details)
			assert.Contains(t, ch, "replicas")
			assert.Contains(t, ch, "image.tag")
		})
	}
}

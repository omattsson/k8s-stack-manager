package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"backend/internal/database"
	"backend/internal/helm"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingRevoker struct {
	mu  sync.Mutex
	ids []string
}

func (r *recordingRevoker) DisconnectInstance(id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids = append(r.ids, id)
	return 1
}

func (r *recordingRevoker) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ids...)
}

func TestInstanceHandler_RevokesStreams(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		method     string
		path       string
		status     string
		role       string
		bulkBody   string
		wantStatus int
		wantRevoke bool
	}{
		{name: "delete draft", method: http.MethodDelete, path: "/api/v1/stack-instances/i1", status: models.StackStatusDraft, role: "user", wantStatus: http.StatusNoContent, wantRevoke: true},
		{name: "delete running starts a clean", method: http.MethodDelete, path: "/api/v1/stack-instances/i1", status: models.StackStatusRunning, role: "user", wantStatus: http.StatusAccepted, wantRevoke: true},
		{name: "delete refused while deploying", method: http.MethodDelete, path: "/api/v1/stack-instances/i1", status: models.StackStatusDeploying, role: "user", wantStatus: http.StatusConflict},
		{name: "stop", method: http.MethodPost, path: "/api/v1/stack-instances/i1/stop", status: models.StackStatusRunning, role: "user", wantStatus: http.StatusAccepted, wantRevoke: true},
		{name: "stop refused for a stopped instance", method: http.MethodPost, path: "/api/v1/stack-instances/i1/stop", status: models.StackStatusStopped, role: "user", wantStatus: http.StatusConflict},
		{name: "clean", method: http.MethodPost, path: "/api/v1/stack-instances/i1/clean", status: models.StackStatusRunning, role: "user", wantStatus: http.StatusAccepted, wantRevoke: true},
		{name: "bulk stop", method: http.MethodPost, path: "/api/v1/stack-instances/bulk/stop", status: models.StackStatusRunning, role: "devops", bulkBody: `{"instance_ids":["i1"]}`, wantStatus: http.StatusOK, wantRevoke: true},
		{name: "bulk clean", method: http.MethodPost, path: "/api/v1/stack-instances/bulk/clean", status: models.StackStatusRunning, role: "devops", bulkBody: `{"instance_ids":["i1"]}`, wantStatus: http.StatusOK, wantRevoke: true},
		{name: "bulk delete draft", method: http.MethodPost, path: "/api/v1/stack-instances/bulk/delete", status: models.StackStatusDraft, role: "devops", bulkBody: `{"instance_ids":["i1"]}`, wantStatus: http.StatusOK, wantRevoke: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			instRepo := NewMockStackInstanceRepository()
			defRepo := NewMockStackDefinitionRepository()
			ccRepo := NewMockChartConfigRepository()
			logRepo := NewMockDeploymentLogRepository()
			seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", tt.status)
			seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
			seedChartConfig(t, ccRepo, "cc1", "d1", "nginx")
			mgr := newTestManager(instRepo, logRepo)

			gin.SetMode(gin.TestMode)
			r := gin.New()
			r.Use(func(c *gin.Context) {
				c.Set("userID", "uid-1")
				c.Set("username", "alice")
				c.Set("role", tt.role)
				c.Next()
			})
			h, err := NewInstanceHandlerWithDeployer(
				instRepo, NewMockValueOverrideRepository(), nil, defRepo, ccRepo,
				NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), helm.NewValuesGenerator(), NewMockUserRepository(),
				mgr, nil, nil, logRepo, nil, 0,
				&mockHandlerTxRunner{repos: database.TxRepos{StackInstance: instRepo}},
			)
			require.NoError(t, err)
			rev := &recordingRevoker{}
			h.WithStreamRevoker(rev)
			g := r.Group("/api/v1/stack-instances")
			g.DELETE("/:id", h.DeleteInstance)
			g.POST("/:id/stop", h.StopInstance)
			g.POST("/:id/clean", h.CleanInstance)
			g.POST("/bulk/stop", h.BulkStop)
			g.POST("/bulk/clean", h.BulkClean)
			g.POST("/bulk/delete", h.BulkDelete)

			w := httptest.NewRecorder()
			var req *http.Request
			if tt.bulkBody != "" {
				req = httptest.NewRequest(tt.method, tt.path, stringsReader(tt.bulkBody))
				req.Header.Set("Content-Type", "application/json")
			} else {
				req = httptest.NewRequest(tt.method, tt.path, nil)
			}
			r.ServeHTTP(w, req)
			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantRevoke {
				assert.Equal(t, []string{"i1"}, rev.got())
			} else {
				assert.Empty(t, rev.got())
			}
		})
	}
}

func TestInstanceHandler_RevokeStreams_NilSafe(t *testing.T) {
	t.Parallel()
	var h *InstanceHandler
	assert.NotPanics(t, func() { h.revokeStreams("i1") })
	assert.NotPanics(t, func() { (&InstanceHandler{}).revokeStreams("i1") })
}

func stringsReader(s string) *strings.Reader { return strings.NewReader(s) }

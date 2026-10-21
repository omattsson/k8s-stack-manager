package handlers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/internal/database"
	"backend/internal/helm"
	"backend/internal/hooks"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupNotifyTriggerRouter returns a router with create, clone and delete of
// an instance and a recording notifier, for a caller with a username.
func setupNotifyTriggerRouter(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, notif *recordingLifecycleNotifier, callerID, username, role string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userID", callerID)
		c.Set("username", username)
		c.Set("role", role)
		c.Next()
	})
	overrideRepo := NewMockValueOverrideRepository()
	boRepo := NewMockChartBranchOverrideRepository()
	h := NewInstanceHandler(instRepo, overrideRepo, boRepo, defRepo, NewMockChartConfigRepository(),
		NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), helm.NewValuesGenerator(), NewMockUserRepository(), 0)
	h.txRunner = &mockHandlerTxRunner{repos: database.TxRepos{
		StackInstance: instRepo, ValueOverride: overrideRepo, BranchOverride: boRepo,
	}}
	h.WithNotifier(notif)
	g := r.Group("/api/v1/stack-instances")
	g.POST("", h.CreateInstance)
	g.POST("/:id/clone", h.CloneInstance)
	g.DELETE("/:id", h.DeleteInstance)
	return r
}

// TestInstanceHandler_NotifyTrigger checks that instance.created and
// instance.deleted carry the user who made the request as the trigger, also
// when it is not the owner (issue #642). A clone sends instance.created too
// (issue #650).
func TestInstanceHandler_NotifyTrigger(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		method   string
		path     string
		body     string
		callerID string
		username string
		role     string
		wantCode int
		wantType string // empty: no notification
		wantOwn  string
	}{
		{name: "create by the owner", method: http.MethodPost, path: "/api/v1/stack-instances",
			body: `{"stack_definition_id":"d1","name":"my-stack"}`, callerID: "uid-1", username: "alice", role: "user",
			wantCode: http.StatusCreated, wantType: "instance.created", wantOwn: "uid-1"},
		{name: "clone with a name by a user of another user's stack", method: http.MethodPost, path: "/api/v1/stack-instances/i1/clone",
			body: `{"name":"my-clone"}`, callerID: "uid-2", username: "bob", role: "user",
			wantCode: http.StatusCreated, wantType: "instance.created", wantOwn: "uid-2"},
		{name: "clone without a body by a devops user", method: http.MethodPost, path: "/api/v1/stack-instances/i1/clone",
			callerID: "devops-1", username: "dan", role: "devops",
			wantCode: http.StatusCreated, wantType: "instance.created", wantOwn: "devops-1"},
		{name: "clone of a missing stack sends nothing", method: http.MethodPost, path: "/api/v1/stack-instances/missing/clone",
			callerID: "uid-2", username: "bob", role: "user",
			wantCode: http.StatusNotFound},
		{name: "clone with an invalid name sends nothing", method: http.MethodPost, path: "/api/v1/stack-instances/i1/clone",
			body: `{"name":"Not_A_Label"}`, callerID: "uid-2", username: "bob", role: "user",
			wantCode: http.StatusBadRequest},
		{name: "delete by a devops user on another user's stack", method: http.MethodDelete, path: "/api/v1/stack-instances/i1",
			callerID: "devops-1", username: "dan", role: "devops",
			wantCode: http.StatusNoContent, wantType: "instance.deleted", wantOwn: "owner"},
		{name: "delete by an admin", method: http.MethodDelete, path: "/api/v1/stack-instances/i1",
			callerID: "admin-1", username: "root", role: "admin",
			wantCode: http.StatusNoContent, wantType: "instance.deleted", wantOwn: "owner"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			instRepo := NewMockStackInstanceRepository()
			seedInstance(t, instRepo, "i1", "shared-stack", "d1", "owner", models.StackStatusDraft)
			defRepo := NewMockStackDefinitionRepository()
			seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
			notif := &recordingLifecycleNotifier{}
			router := setupNotifyTriggerRouter(instRepo, defRepo, notif, tt.callerID, tt.username, tt.role)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, tt.path, bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)
			require.Equal(t, tt.wantCode, w.Code, w.Body.String())

			notif.mu.Lock()
			defer notif.mu.Unlock()
			if tt.wantType == "" {
				assert.Empty(t, notif.types)
				return
			}
			require.Len(t, notif.types, 1)
			assert.Equal(t, tt.wantType, notif.types[0])
			if tt.wantType == "instance.created" {
				// A new instance has no followers: no follower lookup.
				assert.NotNil(t, notif.targets[0].FollowerIDs)
				assert.Empty(t, notif.targets[0].FollowerIDs)
			}
			assert.Equal(t, tt.wantOwn, notif.targets[0].OwnerID)
			assert.NotEmpty(t, notif.targets[0].Namespace)
			assert.NotEmpty(t, notif.targets[0].Branch)
			require.NotNil(t, notif.triggers[0])
			assert.Equal(t, hooks.Trigger{Type: hooks.TriggerUser, ID: tt.callerID, Name: tt.username}, *notif.triggers[0])
		})
	}
}

package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"backend/internal/database"
	"backend/internal/helm"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xeipuuv/gojsonschema"
)

// followStateSchema is the JSON schema of the follow and unfollow responses.
const followStateSchema = `{
"type": "object",
"required": ["following", "follower_count"],
"additionalProperties": false,
"properties": {
"following": {"type": "boolean"},
"follower_count": {"type": "integer", "minimum": 0}
}
}`

// recordingLifecycleNotifier records NotifyInstance calls and returns the
// configured followers from FollowerIDs.
type recordingLifecycleNotifier struct {
	mu        sync.Mutex
	targets   []models.NotificationTarget
	types     []string
	followers *MockInstanceFollowerRepository
}

func (n *recordingLifecycleNotifier) NotifyInstance(_ context.Context, target models.NotificationTarget, notifType, _, _ string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.targets = append(n.targets, target)
	n.types = append(n.types, notifType)
	return nil
}

func (n *recordingLifecycleNotifier) FollowerIDs(ctx context.Context, instanceID string) []string {
	if n.followers == nil {
		return nil
	}
	ids, _ := n.followers.ListUserIDsByInstance(ctx, instanceID)
	return ids
}

// setupFollowRouter returns a router with the follow routes, GET and DELETE
// of an instance. followers may be nil (follow not configured).
func setupFollowRouter(t *testing.T, instanceRepo *MockStackInstanceRepository, followers *MockInstanceFollowerRepository, notif *recordingLifecycleNotifier, callerID, role string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if callerID != "" {
			c.Set("userID", callerID)
		}
		if role != "" {
			c.Set("role", role)
		}
		c.Next()
	})
	h := NewInstanceHandler(instanceRepo, NewMockValueOverrideRepository(), NewMockChartBranchOverrideRepository(),
		NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), NewMockStackTemplateRepository(),
		NewMockTemplateChartConfigRepository(), helm.NewValuesGenerator(), NewMockUserRepository(), 0)
	repos := database.TxRepos{StackInstance: instanceRepo}
	if followers != nil {
		h.WithFollowers(followers)
		repos.InstanceFollower = followers
	}
	h.txRunner = &mockHandlerTxRunner{repos: repos}
	if notif != nil {
		h.WithNotifier(notif)
	}
	g := r.Group("/api/v1/stack-instances")
	g.GET("/:id", h.GetInstance)
	g.PUT("/:id", h.UpdateInstance)
	g.POST("/:id/extend", h.ExtendTTL)
	g.DELETE("/:id", h.DeleteInstance)
	g.POST("/bulk/delete", h.BulkDelete)
	g.POST("/:id/follow", h.FollowInstance)
	g.DELETE("/:id/follow", h.UnfollowInstance)
	return r
}

func TestInstanceHandler_FollowUnfollow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		method        string
		instanceID    string
		callerID      string
		seed          func(*MockInstanceFollowerRepository)
		noRepo        bool
		repoErr       error
		wantCode      int
		wantFollowing bool
		wantCount     int64
	}{
		{name: "viewer follows another user's instance", method: http.MethodPost, instanceID: "i1", callerID: "viewer",
			wantCode: http.StatusOK, wantFollowing: true, wantCount: 1},
		{name: "follow again is idempotent", method: http.MethodPost, instanceID: "i1", callerID: "viewer",
			seed:     func(f *MockInstanceFollowerRepository) { _ = f.Follow(context.Background(), "viewer", "i1") },
			wantCode: http.StatusOK, wantFollowing: true, wantCount: 1},
		{name: "follow counts other followers", method: http.MethodPost, instanceID: "i1", callerID: "viewer",
			seed:     func(f *MockInstanceFollowerRepository) { _ = f.Follow(context.Background(), "other", "i1") },
			wantCode: http.StatusOK, wantFollowing: true, wantCount: 2},
		{name: "unfollow", method: http.MethodDelete, instanceID: "i1", callerID: "viewer",
			seed:     func(f *MockInstanceFollowerRepository) { _ = f.Follow(context.Background(), "viewer", "i1") },
			wantCode: http.StatusOK, wantFollowing: false, wantCount: 0},
		{name: "unfollow without follow is idempotent", method: http.MethodDelete, instanceID: "i1", callerID: "viewer",
			wantCode: http.StatusOK, wantFollowing: false, wantCount: 0},
		{name: "unknown instance gives 404", method: http.MethodPost, instanceID: "missing", callerID: "viewer",
			wantCode: http.StatusNotFound},
		{name: "no user gives 401", method: http.MethodPost, instanceID: "i1", callerID: "",
			wantCode: http.StatusUnauthorized},
		{name: "follow not configured gives 501", method: http.MethodPost, instanceID: "i1", callerID: "viewer",
			noRepo: true, wantCode: http.StatusNotImplemented},
		{name: "repository error gives 500", method: http.MethodPost, instanceID: "i1", callerID: "viewer",
			repoErr: errors.New("db down"), wantCode: http.StatusInternalServerError},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			instRepo := NewMockStackInstanceRepository()
			seedInstance(t, instRepo, "i1", "shared-stack", "d1", "owner", models.StackStatusRunning)
			var followers *MockInstanceFollowerRepository
			if !tt.noRepo {
				followers = NewMockInstanceFollowerRepository()
				if tt.seed != nil {
					tt.seed(followers)
				}
				if tt.repoErr != nil {
					followers.SetError(tt.repoErr)
				}
			}
			router := setupFollowRouter(t, instRepo, followers, nil, tt.callerID, "user")

			w := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, "/api/v1/stack-instances/"+tt.instanceID+"/follow", nil)
			router.ServeHTTP(w, req)

			require.Equal(t, tt.wantCode, w.Code, w.Body.String())
			if tt.wantCode != http.StatusOK {
				var body map[string]string
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
				if tt.wantCode == http.StatusInternalServerError {
					assert.Equal(t, msgInternalServerError, body["error"])
				}
				return
			}
			result, err := gojsonschema.Validate(gojsonschema.NewStringLoader(followStateSchema), gojsonschema.NewBytesLoader(w.Body.Bytes()))
			require.NoError(t, err)
			assert.True(t, result.Valid(), "%v", result.Errors())
			var resp followStateResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, tt.wantFollowing, resp.Following)
			assert.Equal(t, tt.wantCount, resp.FollowerCount)
		})
	}
}

func TestInstanceHandler_GetInstance_FollowState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		callerID      string
		noRepo        bool
		wantFields    bool
		wantFollowing bool
	}{
		{name: "follower sees following true", callerID: "viewer", wantFields: true, wantFollowing: true},
		{name: "other user sees following false", callerID: "someone", wantFields: true, wantFollowing: false},
		{name: "without follower repository the fields are omitted", callerID: "viewer", noRepo: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			instRepo := NewMockStackInstanceRepository()
			seedInstance(t, instRepo, "i1", "shared-stack", "d1", "owner", models.StackStatusRunning)
			var followers *MockInstanceFollowerRepository
			if !tt.noRepo {
				followers = NewMockInstanceFollowerRepository()
				require.NoError(t, followers.Follow(context.Background(), "viewer", "i1"))
				require.NoError(t, followers.Follow(context.Background(), "owner", "i1"))
			}
			router := setupFollowRouter(t, instRepo, followers, nil, tt.callerID, "user")

			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/stack-instances/i1", nil))
			require.Equal(t, http.StatusOK, w.Code)

			var body map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			if !tt.wantFields {
				assert.NotContains(t, body, "following")
				assert.NotContains(t, body, "follower_count")
				return
			}
			assert.Equal(t, tt.wantFollowing, body["following"])
			assert.EqualValues(t, 2, body["follower_count"])
		})
	}
}

// TestInstanceHandler_UpdateAndExtend_KeepFollowState checks that the update
// and extend responses carry the same computed fields as GET, so the detail
// page can replace its state with the response (issue #497).
func TestInstanceHandler_UpdateAndExtend_KeepFollowState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "extend with minutes", method: http.MethodPost, path: "/api/v1/stack-instances/i1/extend", body: `{"minutes":30}`},
		{name: "extend with empty body", method: http.MethodPost, path: "/api/v1/stack-instances/i1/extend"},
		{name: "update ttl", method: http.MethodPut, path: "/api/v1/stack-instances/i1", body: `{"ttl_minutes":120}`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			instRepo := NewMockStackInstanceRepository()
			inst := seedInstance(t, instRepo, "i1", "shared-stack", "d1", "owner", models.StackStatusRunning)
			inst.TTLMinutes = 60
			require.NoError(t, instRepo.Update(inst))
			followers := NewMockInstanceFollowerRepository()
			require.NoError(t, followers.Follow(context.Background(), "viewer", "i1"))
			require.NoError(t, followers.Follow(context.Background(), "owner", "i1"))
			router := setupFollowRouter(t, instRepo, followers, nil, "viewer", "devops")

			req := httptest.NewRequest(tt.method, tt.path, bytes.NewReader([]byte(tt.body)))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())

			var body map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			assert.Equal(t, true, body["following"])
			assert.EqualValues(t, 2, body["follower_count"])
			assert.NotEmpty(t, body["expires_at"])
		})
	}
}

func TestInstanceHandler_DeleteInstance_NotifiesFollowersAndDeletesRows(t *testing.T) {
	t.Parallel()
	instRepo := NewMockStackInstanceRepository()
	seedInstance(t, instRepo, "i1", "shared-stack", "d1", "owner", models.StackStatusDraft)
	followers := NewMockInstanceFollowerRepository()
	require.NoError(t, followers.Follow(context.Background(), "f1", "i1"))
	require.NoError(t, followers.Follow(context.Background(), "f2", "i1"))
	notif := &recordingLifecycleNotifier{followers: followers}
	router := setupFollowRouter(t, instRepo, followers, notif, "owner", "user")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/v1/stack-instances/i1", nil))
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())

	ids, err := followers.ListUserIDsByInstance(context.Background(), "i1")
	require.NoError(t, err)
	assert.Empty(t, ids, "the delete removes the follower rows")

	require.Len(t, notif.targets, 1)
	assert.Equal(t, "instance.deleted", notif.types[0])
	assert.Equal(t, "owner", notif.targets[0].OwnerID)
	assert.Equal(t, []string{"f1", "f2"}, notif.targets[0].FollowerIDs,
		"the followers are read before the delete")
}

func TestInstanceHandler_BulkDelete_NotifiesFollowers(t *testing.T) {
	t.Parallel()
	instRepo := NewMockStackInstanceRepository()
	seedInstance(t, instRepo, "i1", "shared-stack", "d1", "owner", models.StackStatusDraft)
	followers := NewMockInstanceFollowerRepository()
	require.NoError(t, followers.Follow(context.Background(), "f1", "i1"))
	notif := &recordingLifecycleNotifier{followers: followers}
	router := setupFollowRouter(t, instRepo, followers, notif, "owner", "user")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/stack-instances/bulk/delete", bytes.NewBufferString(`{"instance_ids":["i1"]}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	ids, err := followers.ListUserIDsByInstance(context.Background(), "i1")
	require.NoError(t, err)
	assert.Empty(t, ids, "the bulk delete removes the follower rows")

	notif.mu.Lock()
	defer notif.mu.Unlock()
	require.Len(t, notif.targets, 1)
	assert.Equal(t, "instance.deleted", notif.types[0])
	assert.Equal(t, []string{"f1"}, notif.targets[0].FollowerIDs, "the followers are read before the delete")
}

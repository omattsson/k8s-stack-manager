package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xeipuuv/gojsonschema"
)

// channelFiltersResponseSchema checks the filters and warnings of a channel
// create or update response.
const channelFiltersResponseSchema = `{
"type": "object",
"required": ["id", "name", "filters"],
"properties": {
"filters": {
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "instance_name_patterns": {"type": "array", "items": {"type": "string"}},
    "owner_ids": {"type": "array", "items": {"type": "string"}},
    "definition_ids": {"type": "array", "items": {"type": "string"}},
    "cluster_ids": {"type": "array", "items": {"type": "string"}}
  }
},
"warnings": {"type": "array", "items": {"type": "string"}}
}
}`

// setupChannelFilterRouter returns a channel router with filter lookups:
// user u1, definition d1 and cluster c1 exist.
func setupChannelFilterRouter(t *testing.T) (*gin.Engine, *MockNotificationChannelRepository) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	repo := NewMockNotificationChannelRepository()
	users := NewMockUserRepository()
	require.NoError(t, users.Create(&models.User{ID: "u1", Username: "alice", Role: "user"}))
	defs := NewMockStackDefinitionRepository()
	require.NoError(t, defs.Create(&models.StackDefinition{ID: "d1", Name: "full-stack"}))
	clusters := NewMockClusterRepository()
	require.NoError(t, clusters.Create(&models.Cluster{ID: "c1", Name: "dev"}))
	h := NewNotificationChannelHandler(repo).WithFilterLookups(users, defs, clusters)

	r := gin.New()
	g := r.Group("/api/v1/admin/notification-channels")
	g.POST("", h.CreateChannel)
	g.GET("/:id", h.GetChannel)
	g.PUT("/:id", h.UpdateChannel)
	g.GET("", h.ListChannels)
	return r, repo
}

func TestNotificationChannelHandler_Filters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		method       string
		path         string
		body         string
		wantCode     int
		wantFilters  models.NotificationChannelFilters
		wantWarnings []string
		wantErrPart  string
	}{
		{
			name: "create with all filters, normalized", method: http.MethodPost, path: "",
			body: `{"name":"team-a","webhook_url":"https://hooks.example.com/a","filters":{
				"instance_name_patterns":[" RdbTest-* ","rdbtest-*","*-se"],
				"owner_ids":["u1"],"definition_ids":["d1"],"cluster_ids":["c1"]}}`,
			wantCode: http.StatusCreated,
			wantFilters: models.NotificationChannelFilters{
				InstanceNamePatterns: []string{"rdbtest-*", "*-se"},
				OwnerIDs:             []string{"u1"}, DefinitionIDs: []string{"d1"}, ClusterIDs: []string{"c1"},
			},
		},
		{
			name: "create without filters matches all", method: http.MethodPost, path: "",
			body:     `{"name":"all","webhook_url":"https://hooks.example.com/b"}`,
			wantCode: http.StatusCreated,
		},
		{
			name: "unknown IDs give warnings, not errors", method: http.MethodPost, path: "",
			body: `{"name":"team-b","webhook_url":"https://hooks.example.com/c","filters":{
				"owner_ids":["u1","gone-user"],"definition_ids":["gone-def"],"cluster_ids":["gone-cluster"]}}`,
			wantCode: http.StatusCreated,
			wantFilters: models.NotificationChannelFilters{
				OwnerIDs: []string{"u1", "gone-user"}, DefinitionIDs: []string{"gone-def"}, ClusterIDs: []string{"gone-cluster"},
			},
			wantWarnings: []string{
				"owner_ids: user gone-user does not exist",
				"definition_ids: stack definition gone-def does not exist",
				"cluster_ids: cluster gone-cluster does not exist",
			},
		},
		{
			name: "invalid glob gives 400", method: http.MethodPost, path: "",
			body:     `{"name":"bad","webhook_url":"https://hooks.example.com/d","filters":{"instance_name_patterns":["rdb[test"]}}`,
			wantCode: http.StatusBadRequest, wantErrPart: "not a valid pattern",
		},
		{
			name: "too long ID gives 400", method: http.MethodPost, path: "",
			body:     `{"name":"bad","webhook_url":"https://hooks.example.com/d","filters":{"owner_ids":["` + strings.Repeat("x", 37) + `"]}}`,
			wantCode: http.StatusBadRequest, wantErrPart: "owner_ids",
		},
		{
			name: "update replaces filters", method: http.MethodPut, path: "/ch1",
			body:        `{"filters":{"cluster_ids":["c1"]}}`,
			wantCode:    http.StatusOK,
			wantFilters: models.NotificationChannelFilters{ClusterIDs: []string{"c1"}},
		},
		{
			name: "update with empty object removes filters", method: http.MethodPut, path: "/ch1",
			body:     `{"filters":{}}`,
			wantCode: http.StatusOK,
		},
		{
			name: "update without filters keeps them", method: http.MethodPut, path: "/ch1",
			body:        `{"name":"renamed"}`,
			wantCode:    http.StatusOK,
			wantFilters: models.NotificationChannelFilters{InstanceNamePatterns: []string{"old-*"}},
		},
		{
			name: "update with invalid glob gives 400", method: http.MethodPut, path: "/ch1",
			body:     `{"filters":{"instance_name_patterns":["["]}}`,
			wantCode: http.StatusBadRequest, wantErrPart: "not a valid pattern",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router, repo := setupChannelFilterRouter(t)
			seedChannel(t, repo, "ch1", "existing", "https://hooks.example.com/x", true)
			repo.channels["ch1"].Filters = models.NotificationChannelFilters{InstanceNamePatterns: []string{"old-*"}}

			w := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, "/api/v1/admin/notification-channels"+tt.path, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			require.Equal(t, tt.wantCode, w.Code, w.Body.String())
			if tt.wantErrPart != "" {
				var body map[string]string
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
				assert.Contains(t, body["error"], tt.wantErrPart)
				return
			}
			result, err := gojsonschema.Validate(gojsonschema.NewStringLoader(channelFiltersResponseSchema), gojsonschema.NewBytesLoader(w.Body.Bytes()))
			require.NoError(t, err)
			assert.True(t, result.Valid(), "%v", result.Errors())

			var resp notificationChannelResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, tt.wantFilters, resp.Filters)
			assert.Equal(t, tt.wantWarnings, resp.Warnings)

			// GET returns the stored filters.
			w = httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/notification-channels/"+resp.ID, nil))
			require.Equal(t, http.StatusOK, w.Code)
			var got models.NotificationChannel
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
			assert.Equal(t, tt.wantFilters, got.Filters)
		})
	}
}

package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/internal/helm"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xeipuuv/gojsonschema"
)

// instanceListNamesSchema checks the list envelope and the computed name
// fields of each instance.
const instanceListNamesSchema = `{
  "type": "object",
  "required": ["data", "total", "page", "pageSize"],
  "properties": {
    "total": {"type": "integer"},
    "page": {"type": "integer"},
    "pageSize": {"type": "integer"},
    "data": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["id", "owner_id", "stack_definition_id"],
        "properties": {
          "owner_username": {"type": "string", "minLength": 1},
          "definition_name": {"type": "string", "minLength": 1},
          "cluster_name": {"type": "string", "minLength": 1}
        }
      }
    }
  }
}`

// nameTestRepos holds the repositories of the name and filter tests.
type nameTestRepos struct {
	inst    *MockStackInstanceRepository
	def     *MockStackDefinitionRepository
	cluster *MockClusterRepository
	user    *MockUserRepository
}

// newNameTestRepos returns repositories with two users (alice, bob), two
// definitions and two clusters.
func newNameTestRepos(t *testing.T) nameTestRepos {
	t.Helper()
	r := nameTestRepos{
		inst:    NewMockStackInstanceRepository(),
		def:     NewMockStackDefinitionRepository(),
		cluster: NewMockClusterRepository(),
		user:    NewMockUserRepository(),
	}
	require.NoError(t, r.user.Create(&models.User{ID: "uid-alice", Username: "alice"}))
	require.NoError(t, r.user.Create(&models.User{ID: "uid-bob", Username: "bob"}))
	require.NoError(t, r.def.Create(&models.StackDefinition{ID: "def-a", Name: "Definition A", OwnerID: "uid-alice"}))
	require.NoError(t, r.def.Create(&models.StackDefinition{ID: "def-b", Name: "Definition B", OwnerID: "uid-bob"}))
	require.NoError(t, r.cluster.Create(&models.Cluster{ID: "cl-1", Name: "cluster-one"}))
	require.NoError(t, r.cluster.Create(&models.Cluster{ID: "cl-2", Name: "cluster-two"}))
	return r
}

// seedFilterInstance adds an instance; age orders the list (newest first).
func seedFilterInstance(t *testing.T, repo *MockStackInstanceRepository, id, defID, ownerID, clusterID, status string, age time.Duration) {
	t.Helper()
	created := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC).Add(-age)
	require.NoError(t, repo.Create(&models.StackInstance{
		ID:                id,
		Name:              id,
		StackDefinitionID: defID,
		OwnerID:           ownerID,
		ClusterID:         clusterID,
		Status:            status,
		Namespace:         "stack-" + id,
		CreatedAt:         created,
		UpdatedAt:         created,
	}))
}

// setupNameRouter returns a router with the instance list and detail routes.
// The handler has user, definition and cluster repositories.
func setupNameRouter(r nameTestRepos, callerID, callerRole string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(injectAuthContext(callerID, callerRole))
	h := NewInstanceHandler(r.inst, NewMockValueOverrideRepository(), NewMockChartBranchOverrideRepository(),
		r.def, NewMockChartConfigRepository(), NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
		helm.NewValuesGenerator(), r.user, 0)
	h.clusterRepo = r.cluster
	router.GET("/api/v1/stack-instances", h.ListInstances)
	router.GET("/api/v1/stack-instances/recent", h.GetRecentInstances)
	router.GET("/api/v1/stack-instances/:id", h.GetInstance)
	router.PUT("/api/v1/stack-instances/:id", h.UpdateInstance)
	return router
}

// seedFilterSet adds 7 instances:
//
//	i1 def-a alice cl-1 running   (newest)
//	i2 def-a alice cl-2 stopped
//	i3 def-b bob   cl-1 running
//	i4 def-b bob   cl-2 error
//	i5 def-a bob   cl-1 running
//	i6 def-a alice cl-1 running
//	i7 def-b alice cl-1 running   (oldest)
func seedFilterSet(t *testing.T, repo *MockStackInstanceRepository) {
	t.Helper()
	seedFilterInstance(t, repo, "i1", "def-a", "uid-alice", "cl-1", models.StackStatusRunning, 1*time.Minute)
	seedFilterInstance(t, repo, "i2", "def-a", "uid-alice", "cl-2", models.StackStatusStopped, 2*time.Minute)
	seedFilterInstance(t, repo, "i3", "def-b", "uid-bob", "cl-1", models.StackStatusRunning, 3*time.Minute)
	seedFilterInstance(t, repo, "i4", "def-b", "uid-bob", "cl-2", models.StackStatusError, 4*time.Minute)
	seedFilterInstance(t, repo, "i5", "def-a", "uid-bob", "cl-1", models.StackStatusRunning, 5*time.Minute)
	seedFilterInstance(t, repo, "i6", "def-a", "uid-alice", "cl-1", models.StackStatusRunning, 6*time.Minute)
	seedFilterInstance(t, repo, "i7", "def-b", "uid-alice", "cl-1", models.StackStatusRunning, 7*time.Minute)
}

func instanceIDs(data []models.StackInstance) []string {
	ids := make([]string, len(data))
	for i, d := range data {
		ids[i] = d.ID
	}
	return ids
}

func TestListInstances_Filters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		query     string
		caller    string
		wantCode  int
		wantIDs   []string
		wantTotal int
	}{
		{name: "no filter", query: "", caller: "uid-alice", wantCode: http.StatusOK,
			wantIDs: []string{"i1", "i2", "i3", "i4", "i5", "i6", "i7"}, wantTotal: 7},
		{name: "status", query: "status=running", caller: "uid-alice", wantCode: http.StatusOK,
			wantIDs: []string{"i1", "i3", "i5", "i6", "i7"}, wantTotal: 5},
		{name: "cluster_id", query: "cluster_id=cl-2", caller: "uid-alice", wantCode: http.StatusOK,
			wantIDs: []string{"i2", "i4"}, wantTotal: 2},
		{name: "definition_id", query: "definition_id=def-b", caller: "uid-alice", wantCode: http.StatusOK,
			wantIDs: []string{"i3", "i4", "i7"}, wantTotal: 3},
		{name: "owner me", query: "owner=me", caller: "uid-bob", wantCode: http.StatusOK,
			wantIDs: []string{"i3", "i4", "i5"}, wantTotal: 3},
		{name: "owner username", query: "owner=bob", caller: "uid-alice", wantCode: http.StatusOK,
			wantIDs: []string{"i3", "i4", "i5"}, wantTotal: 3},
		{name: "owner user ID", query: "owner=uid-bob", caller: "uid-alice", wantCode: http.StatusOK,
			wantIDs: []string{"i3", "i4", "i5"}, wantTotal: 3},
		{name: "unknown owner gives empty list", query: "owner=nobody", caller: "uid-alice", wantCode: http.StatusOK,
			wantIDs: []string{}, wantTotal: 0},
		{name: "name", query: "name=i4", caller: "uid-alice", wantCode: http.StatusOK,
			wantIDs: []string{"i4"}, wantTotal: 1},
		{name: "combined status cluster owner definition", query: "status=running&cluster_id=cl-1&owner=alice&definition_id=def-a", caller: "uid-bob", wantCode: http.StatusOK,
			wantIDs: []string{"i1", "i6"}, wantTotal: 2},
		{name: "combined with no match", query: "status=error&owner=me", caller: "uid-alice", wantCode: http.StatusOK,
			wantIDs: []string{}, wantTotal: 0},
		{name: "filtered pagination page 2", query: "status=running&page=2&pageSize=2", caller: "uid-alice", wantCode: http.StatusOK,
			wantIDs: []string{"i5", "i6"}, wantTotal: 5},
		{name: "filtered pagination last page", query: "status=running&page=3&pageSize=2", caller: "uid-alice", wantCode: http.StatusOK,
			wantIDs: []string{"i7"}, wantTotal: 5},
		{name: "filtered legacy limit offset", query: "owner=me&limit=2&offset=1", caller: "uid-alice", wantCode: http.StatusOK,
			wantIDs: []string{"i2", "i6"}, wantTotal: 4},
		{name: "unknown status", query: "status=bogus", caller: "uid-alice", wantCode: http.StatusBadRequest},
		{name: "status is case sensitive", query: "status=Running", caller: "uid-alice", wantCode: http.StatusBadRequest},
		{name: "cluster_id too long", query: "cluster_id=" + strings.Repeat("c", 37), caller: "uid-alice", wantCode: http.StatusBadRequest},
		{name: "definition_id too long", query: "definition_id=" + strings.Repeat("d", 37), caller: "uid-alice", wantCode: http.StatusBadRequest},
		{name: "owner too long", query: "owner=" + strings.Repeat("o", 256), caller: "uid-alice", wantCode: http.StatusBadRequest},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repos := newNameTestRepos(t)
			seedFilterSet(t, repos.inst)
			router := setupNameRouter(repos, tt.caller, "user")

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances?"+tt.query, nil)
			router.ServeHTTP(w, req)

			require.Equal(t, tt.wantCode, w.Code, w.Body.String())
			if tt.wantCode != http.StatusOK {
				var body map[string]string
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
				assert.NotEmpty(t, body["error"])
				return
			}
			var resp pagedResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, tt.wantIDs, instanceIDs(resp.Data))
			assert.Equal(t, tt.wantTotal, resp.Total)
		})
	}
}

func TestListInstances_OwnerLookupError(t *testing.T) {
	t.Parallel()
	repos := newNameTestRepos(t)
	repos.user.findErr = errors.New("connection refused")
	router := setupNameRouter(repos, "uid-alice", "user")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances?owner=bob", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.NotContains(t, w.Body.String(), "connection refused")
}

func TestListInstances_Names(t *testing.T) {
	t.Parallel()
	repos := newNameTestRepos(t)
	seedFilterSet(t, repos.inst)
	router := setupNameRouter(repos, "uid-alice", "user")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances", nil)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	result, err := gojsonschema.Validate(gojsonschema.NewStringLoader(instanceListNamesSchema), gojsonschema.NewBytesLoader(w.Body.Bytes()))
	require.NoError(t, err)
	assert.True(t, result.Valid(), "%v", result.Errors())

	var resp pagedResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 7)
	byID := make(map[string]models.StackInstance, len(resp.Data))
	for _, d := range resp.Data {
		byID[d.ID] = d
	}
	assert.Equal(t, "alice", byID["i1"].OwnerUsername)
	assert.Equal(t, "Definition A", byID["i1"].DefinitionName)
	assert.Equal(t, "cluster-one", byID["i1"].ClusterName)
	assert.Equal(t, "bob", byID["i4"].OwnerUsername)
	assert.Equal(t, "Definition B", byID["i4"].DefinitionName)
	assert.Equal(t, "cluster-two", byID["i4"].ClusterName)

	// One batch lookup per kind for the whole page (no N+1).
	assert.Equal(t, int32(1), repos.user.findByIDsCalls.Load())
	assert.Equal(t, int32(1), repos.def.namesCalls.Load())
	assert.Equal(t, int32(1), repos.cluster.namesCalls.Load())
}

func TestInstanceNames_MissingReferences(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
	}{
		{name: "list", path: "/api/v1/stack-instances"},
		{name: "detail", path: "/api/v1/stack-instances/orphan"},
		{name: "recent", path: "/api/v1/stack-instances/recent"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repos := newNameTestRepos(t)
			// Owner, definition and cluster do not exist (deleted).
			seedFilterInstance(t, repos.inst, "orphan", "def-gone", "uid-alice", "cl-gone", models.StackStatusDraft, 0)
			require.NoError(t, repos.user.Delete("uid-alice"))
			router := setupNameRouter(repos, "uid-alice", "user")

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, tt.path, nil)
			router.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code)

			// The fields are empty and omitted from the JSON.
			body := w.Body.String()
			assert.NotContains(t, body, "owner_username")
			assert.NotContains(t, body, "definition_name")
			assert.NotContains(t, body, "cluster_name")
		})
	}
}

func TestInstanceNames_DetailAndUpdate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		body   string
	}{
		{name: "get", method: http.MethodGet},
		{name: "update", method: http.MethodPut, body: `{"branch":"develop"}`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repos := newNameTestRepos(t)
			seedFilterInstance(t, repos.inst, "i1", "def-b", "uid-alice", "cl-2", models.StackStatusDraft, 0)
			router := setupNameRouter(repos, "uid-alice", "user")

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(tt.method, "/api/v1/stack-instances/i1", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())

			var inst models.StackInstance
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &inst))
			assert.Equal(t, "alice", inst.OwnerUsername)
			assert.Equal(t, "Definition B", inst.DefinitionName)
			assert.Equal(t, "cluster-two", inst.ClusterName)
		})
	}
}

func TestInstanceNames_DefaultClusterHasNoName(t *testing.T) {
	t.Parallel()
	repos := newNameTestRepos(t)
	seedFilterInstance(t, repos.inst, "i1", "def-a", "uid-alice", "", models.StackStatusDraft, 0)
	router := setupNameRouter(repos, "uid-alice", "user")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/i1", nil)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var inst models.StackInstance
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &inst))
	assert.Equal(t, "alice", inst.OwnerUsername)
	assert.Equal(t, "Definition A", inst.DefinitionName)
	assert.Empty(t, inst.ClusterName)
	// No cluster IDs: no cluster lookup.
	assert.Equal(t, int32(0), repos.cluster.namesCalls.Load())
}

func TestInstanceNames_LookupErrorsKeepResponse(t *testing.T) {
	t.Parallel()
	repos := newNameTestRepos(t)
	seedFilterInstance(t, repos.inst, "i1", "def-a", "uid-alice", "cl-1", models.StackStatusDraft, 0)
	repos.def.SetError(errors.New("db down"))
	repos.cluster.err = errors.New("db down")
	router := setupNameRouter(repos, "uid-alice", "user")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances", nil)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp pagedResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 1)
	assert.Equal(t, "alice", resp.Data[0].OwnerUsername)
	assert.Empty(t, resp.Data[0].DefinitionName)
	assert.Empty(t, resp.Data[0].ClusterName)
}

// ---- Definitions ----

// setupDefinitionNameRouter returns a router with the definition list and
// detail routes and a user repository.
func setupDefinitionNameRouter(defRepo *MockStackDefinitionRepository, userRepo *MockUserRepository, callerID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(injectAuthContext(callerID, "user"))
	h := NewDefinitionHandler(defRepo, NewMockChartConfigRepository(), NewMockStackInstanceRepository(), nil, nil).
		WithUserRepo(userRepo)
	router.GET("/api/v1/stack-definitions", h.ListDefinitions)
	router.GET("/api/v1/stack-definitions/:id", h.GetDefinition)
	router.POST("/api/v1/stack-definitions", h.CreateDefinition)
	return router
}

// definitionPage is the list envelope of GET /stack-definitions.
type definitionPage struct {
	Data     []models.StackDefinition `json:"data"`
	Total    int                      `json:"total"`
	Page     int                      `json:"page"`
	PageSize int                      `json:"pageSize"`
}

func TestListDefinitions_OwnerFilter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		query     string
		caller    string
		wantCode  int
		wantIDs   []string
		wantTotal int
	}{
		{name: "no filter", caller: "uid-alice", wantCode: http.StatusOK, wantIDs: []string{"d1", "d2", "d3"}, wantTotal: 3},
		{name: "owner me", query: "owner=me", caller: "uid-alice", wantCode: http.StatusOK, wantIDs: []string{"d1", "d3"}, wantTotal: 2},
		{name: "owner username", query: "owner=bob", caller: "uid-alice", wantCode: http.StatusOK, wantIDs: []string{"d2"}, wantTotal: 1},
		{name: "owner user ID", query: "owner=uid-alice", caller: "uid-bob", wantCode: http.StatusOK, wantIDs: []string{"d1", "d3"}, wantTotal: 2},
		{name: "unknown owner", query: "owner=nobody", caller: "uid-alice", wantCode: http.StatusOK, wantIDs: []string{}, wantTotal: 0},
		{name: "owner and name", query: "owner=me&name=shared", caller: "uid-alice", wantCode: http.StatusOK, wantIDs: []string{"d3"}, wantTotal: 1},
		{name: "owner with pagination", query: "owner=me&page=2&pageSize=1", caller: "uid-alice", wantCode: http.StatusOK, wantIDs: []string{"d3"}, wantTotal: 2},
		{name: "owner too long", query: "owner=" + strings.Repeat("o", 256), caller: "uid-alice", wantCode: http.StatusBadRequest},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			defRepo := NewMockStackDefinitionRepository()
			userRepo := NewMockUserRepository()
			require.NoError(t, userRepo.Create(&models.User{ID: "uid-alice", Username: "alice"}))
			require.NoError(t, userRepo.Create(&models.User{ID: "uid-bob", Username: "bob"}))
			base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			for i, d := range []models.StackDefinition{
				{ID: "d1", Name: "first", OwnerID: "uid-alice"},
				{ID: "d2", Name: "shared", OwnerID: "uid-bob"},
				{ID: "d3", Name: "shared", OwnerID: "uid-alice"},
			} {
				d := d
				d.CreatedAt = base.Add(-time.Duration(i) * time.Minute)
				require.NoError(t, defRepo.Create(&d))
			}
			router := setupDefinitionNameRouter(defRepo, userRepo, tt.caller)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-definitions?"+tt.query, nil)
			router.ServeHTTP(w, req)

			require.Equal(t, tt.wantCode, w.Code, w.Body.String())
			if tt.wantCode != http.StatusOK {
				return
			}
			var resp definitionPage
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			ids := make([]string, len(resp.Data))
			for i, d := range resp.Data {
				ids[i] = d.ID
				want := map[string]string{"uid-alice": "alice", "uid-bob": "bob"}[d.OwnerID]
				assert.Equal(t, want, d.OwnerUsername)
			}
			assert.Equal(t, tt.wantIDs, ids)
			assert.Equal(t, tt.wantTotal, resp.Total)
			if len(resp.Data) > 0 {
				assert.Equal(t, int32(1), userRepo.findByIDsCalls.Load())
			}
		})
	}
}

func TestDefinitionOwnerUsername_DetailAndCreate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		method   string
		path     string
		body     string
		wantCode int
		wantName string
	}{
		{name: "detail", method: http.MethodGet, path: "/api/v1/stack-definitions/d1", wantCode: http.StatusOK, wantName: "alice"},
		{name: "detail of deleted owner", method: http.MethodGet, path: "/api/v1/stack-definitions/d-orphan", wantCode: http.StatusOK, wantName: ""},
		{name: "create ignores a client value", method: http.MethodPost, path: "/api/v1/stack-definitions",
			body: `{"name":"new-def","owner_username":"spoofed"}`, wantCode: http.StatusCreated, wantName: "alice"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			defRepo := NewMockStackDefinitionRepository()
			userRepo := NewMockUserRepository()
			require.NoError(t, userRepo.Create(&models.User{ID: "uid-alice", Username: "alice"}))
			require.NoError(t, defRepo.Create(&models.StackDefinition{ID: "d1", Name: "first", OwnerID: "uid-alice"}))
			require.NoError(t, defRepo.Create(&models.StackDefinition{ID: "d-orphan", Name: "orphan", OwnerID: "uid-gone"}))
			router := setupDefinitionNameRouter(defRepo, userRepo, "uid-alice")

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)
			require.Equal(t, tt.wantCode, w.Code, w.Body.String())

			var def models.StackDefinition
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &def))
			assert.Equal(t, tt.wantName, def.OwnerUsername)
		})
	}
}

// ---- Templates ----

func TestTemplateOwnerUsername(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		path     string
		ownerID  string
		wantName string
	}{
		{name: "detail", path: "/api/v1/templates/t1", ownerID: "uid-alice", wantName: "alice"},
		{name: "detail of deleted owner", path: "/api/v1/templates/t1", ownerID: "uid-gone", wantName: ""},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tmplRepo := NewMockStackTemplateRepository()
			chartRepo := NewMockTemplateChartConfigRepository()
			userRepo := NewMockUserRepository()
			require.NoError(t, userRepo.Create(&models.User{ID: "uid-alice", Username: "alice"}))
			seedTemplate(t, tmplRepo, "t1", "Template One", tt.ownerID, true)

			gin.SetMode(gin.TestMode)
			router := gin.New()
			router.Use(injectAuthContext("uid-alice", "admin"))
			h := NewTemplateHandler(tmplRepo, chartRepo, NewMockStackDefinitionRepository(), NewMockChartConfigRepository())
			h.SetUserRepo(userRepo)
			router.GET("/api/v1/templates", h.ListTemplates)
			router.GET("/api/v1/templates/:id", h.GetTemplate)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, tt.path, nil)
			router.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())

			var detail TemplateDetailResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &detail))
			assert.Equal(t, tt.wantName, detail.OwnerUsername)

			// The list sends the same field.
			w = httptest.NewRecorder()
			req, _ = http.NewRequest(http.MethodGet, "/api/v1/templates", nil)
			router.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code)
			var list struct {
				Data []TemplateListItem `json:"data"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
			require.Len(t, list.Data, 1)
			assert.Equal(t, tt.wantName, list.Data[0].OwnerUsername)
		})
	}
}

func TestResolveOwnerFilter(t *testing.T) {
	t.Parallel()

	const (
		uuidAlice = "11111111-1111-4111-8111-111111111111"
		uuidBob   = "22222222-2222-4222-8222-222222222222"
		uuidGone  = "33333333-3333-4333-8333-333333333333"
	)

	tests := []struct {
		name     string
		owner    string
		caller   string
		findErr  error
		wantCode int
		wantID   string
	}{
		{name: "empty is no filter", owner: "", caller: uuidAlice, wantCode: http.StatusOK, wantID: ""},
		{name: "me", owner: "me", caller: uuidAlice, wantCode: http.StatusOK, wantID: uuidAlice},
		{name: "me without a user gives 401", owner: "me", caller: "", wantCode: http.StatusUnauthorized},
		{name: "UUID matches a user ID first", owner: uuidBob, caller: uuidAlice, wantCode: http.StatusOK, wantID: uuidBob},
		// A user whose username has UUID form: the ID match of the other user wins.
		{name: "UUID ID match wins over a UUID username", owner: uuidAlice, caller: uuidBob, wantCode: http.StatusOK, wantID: uuidAlice},
		{name: "UUID without an ID match falls back to the username", owner: uuidGone, caller: uuidAlice, wantCode: http.StatusOK, wantID: "uid-uuid-name"},
		{name: "username", owner: "bob", caller: uuidAlice, wantCode: http.StatusOK, wantID: uuidBob},
		{name: "unknown value is used as an ID", owner: "nobody", caller: uuidAlice, wantCode: http.StatusOK, wantID: "nobody"},
		{name: "lookup error gives 500", owner: uuidBob, caller: uuidAlice, findErr: errors.New("connection refused"), wantCode: http.StatusInternalServerError},
		{name: "too long gives 400", owner: strings.Repeat("o", 256), caller: uuidAlice, wantCode: http.StatusBadRequest},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			userRepo := NewMockUserRepository()
			require.NoError(t, userRepo.Create(&models.User{ID: uuidAlice, Username: "alice"}))
			require.NoError(t, userRepo.Create(&models.User{ID: uuidBob, Username: "bob"}))
			// A user whose username is a UUID that is no user ID.
			require.NoError(t, userRepo.Create(&models.User{ID: "uid-uuid-name", Username: uuidGone}))
			// A user whose username is the ID of alice.
			require.NoError(t, userRepo.Create(&models.User{ID: "uid-shadow", Username: uuidAlice}))
			userRepo.findErr = tt.findErr

			gin.SetMode(gin.TestMode)
			router := gin.New()
			router.Use(injectAuthContext(tt.caller, "user"))
			var gotID string
			router.GET("/owner", func(c *gin.Context) {
				id, ok := resolveOwnerFilter(c, userRepo, c.Query("owner"))
				if !ok {
					return
				}
				gotID = id
				c.Status(http.StatusOK)
			})

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/owner?owner="+tt.owner, nil)
			router.ServeHTTP(w, req)

			require.Equal(t, tt.wantCode, w.Code, w.Body.String())
			if tt.wantCode == http.StatusOK {
				assert.Equal(t, tt.wantID, gotID)
			} else {
				assert.NotContains(t, w.Body.String(), "connection refused")
			}
		})
	}
}

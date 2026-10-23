package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/cluster"
	"backend/internal/database"
	"backend/internal/helm"
	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// uniqueNamesFixture holds the repositories of a UNIQUE_INSTANCE_NAMES test.
type uniqueNamesFixture struct {
	instances *MockStackInstanceRepository
	defs      *MockStackDefinitionRepository
	users     *MockUserRepository
}

// newUniqueNamesFixture seeds the definition d1, the users alice (uid-1) and
// carol (uid-carol), and the given instances.
func newUniqueNamesFixture(t *testing.T, seed ...models.StackInstance) *uniqueNamesFixture {
	t.Helper()
	f := &uniqueNamesFixture{
		instances: NewMockStackInstanceRepository(),
		defs:      NewMockStackDefinitionRepository(),
		users:     NewMockUserRepository(),
	}
	seedDefinition(t, f.defs, "d1", "My Def", "uid-1")
	require.NoError(t, f.users.Create(context.Background(), &models.User{ID: "uid-1", Username: "alice", Role: "user"}))
	require.NoError(t, f.users.Create(context.Background(), &models.User{ID: "uid-carol", Username: "carol", Role: "user"}))
	now := time.Now().UTC()
	for i := range seed {
		inst := seed[i]
		inst.StackDefinitionID = "d1"
		inst.Branch = "master"
		inst.Status = models.StackStatusDraft
		inst.CreatedAt, inst.UpdatedAt = now, now
		require.NoError(t, f.instances.Create(context.Background(), &inst))
	}
	return f
}

// router returns an instance router as alice (role user). registry may be nil.
func (f *uniqueNamesFixture) router(unique bool, registry *cluster.Registry) *gin.Engine {
	return f.routerWith(f.instances, unique, registry)
}

// routerWith is router with another instance repository.
func (f *uniqueNamesFixture) routerWith(instances models.StackInstanceRepository, unique bool, registry *cluster.Registry) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userID", "uid-1")
		c.Set("username", "alice")
		c.Set("role", "user")
		c.Next()
	})
	overrides := NewMockValueOverrideRepository()
	branches := NewMockChartBranchOverrideRepository()
	h := NewInstanceHandler(instances, overrides, branches, f.defs, NewMockChartConfigRepository(),
		NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), helm.NewValuesGenerator(), f.users, 0).
		WithUniqueInstanceNames(unique)
	h.registry = registry
	h.txRunner = &mockHandlerTxRunner{repos: database.TxRepos{
		StackInstance:  instances,
		ValueOverride:  overrides,
		BranchOverride: branches,
	}}
	r.POST("/api/v1/stack-instances", h.CreateInstance)
	r.PUT("/api/v1/stack-instances/:id", h.UpdateInstance)
	r.POST("/api/v1/stack-instances/:id/clone", h.CloneInstance)
	return r
}

func doJSON(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func TestUniqueInstanceNames_Create(t *testing.T) {
	t.Parallel()

	carolDemo := models.StackInstance{ID: "i-carol", Name: "demo", Namespace: "stack-demo-carol", OwnerID: "uid-carol", ClusterID: "c1"}
	tests := []struct {
		name         string
		seed         []models.StackInstance
		unique       bool
		registry     *cluster.Registry
		body         string
		wantStatus   int
		wantMessage  string
		wantSuggests []string
	}{
		{
			name:       "setting off allows the same name on the same cluster",
			seed:       []models.StackInstance{carolDemo},
			body:       `{"stack_definition_id":"d1","name":"demo","cluster_id":"c1"}`,
			wantStatus: http.StatusCreated,
		},
		{
			name: "same name on the same cluster gives 409 with the owner and free suggestions",
			seed: []models.StackInstance{carolDemo,
				{ID: "i-carol-2", Name: "demo-2", Namespace: "stack-demo-2-carol", OwnerID: "uid-carol", ClusterID: "c1"}},
			unique:       true,
			body:         `{"stack_definition_id":"d1","name":"demo","cluster_id":"c1"}`,
			wantStatus:   http.StatusConflict,
			wantMessage:  `A stack named "demo" already exists on this cluster (owner: carol)`,
			wantSuggests: []string{"demo-3", "demo-4"},
		},
		{
			// alice has demo-3 on c2: the name is free on c1, but the
			// namespace stack-demo-3-alice is not (namespaces are global).
			name: "suggestions skip names whose namespace is taken",
			seed: []models.StackInstance{carolDemo,
				{ID: "i-carol-2", Name: "demo-2", Namespace: "stack-demo-2-carol", OwnerID: "uid-carol", ClusterID: "c1"},
				{ID: "i-alice-3", Name: "demo-3", Namespace: "stack-demo-3-alice", OwnerID: "uid-1", ClusterID: "c2"}},
			unique:       true,
			body:         `{"stack_definition_id":"d1","name":"demo","cluster_id":"c1"}`,
			wantStatus:   http.StatusConflict,
			wantMessage:  `A stack named "demo" already exists on this cluster (owner: carol)`,
			wantSuggests: []string{"demo-4"},
		},
		{
			name:       "same name on another cluster is allowed",
			seed:       []models.StackInstance{carolDemo},
			unique:     true,
			body:       `{"stack_definition_id":"d1","name":"demo","cluster_id":"c2"}`,
			wantStatus: http.StatusCreated,
		},
		{
			name:        "the comparison ignores case",
			seed:        []models.StackInstance{{ID: "i-old", Name: "Demo", Namespace: "stack-Demo-carol", OwnerID: "uid-carol", ClusterID: "c1"}},
			unique:      true,
			body:        `{"stack_definition_id":"d1","name":"demo","cluster_id":"c1"}`,
			wantStatus:  http.StatusConflict,
			wantMessage: `A stack named "Demo" already exists on this cluster (owner: carol)`,
		},
		{
			name:        "an instance without a cluster counts on the default cluster",
			seed:        []models.StackInstance{{ID: "i-legacy", Name: "demo", Namespace: "stack-demo-carol", OwnerID: "uid-carol"}},
			unique:      true,
			registry:    cluster.NewRegistryForTest("c1", nil, &noopHelmExecutor{}),
			body:        `{"stack_definition_id":"d1","name":"demo"}`,
			wantStatus:  http.StatusConflict,
			wantMessage: `A stack named "demo" already exists on this cluster (owner: carol)`,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newUniqueNamesFixture(t, tt.seed...)
			w := doJSON(f.router(tt.unique, tt.registry), http.MethodPost, "/api/v1/stack-instances", tt.body)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantStatus != http.StatusConflict {
				return
			}
			var resp NamespaceConflictResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, msgInstanceNameExists, resp.Error)
			assert.Equal(t, tt.wantMessage, resp.Message)
			if tt.wantSuggests != nil {
				assert.Equal(t, tt.wantSuggests, resp.Suggestions)
			}
		})
	}
}

func TestUniqueInstanceNames_Rename(t *testing.T) {
	t.Parallel()

	seed := []models.StackInstance{
		{ID: "i-carol", Name: "demo", Namespace: "stack-demo-carol", OwnerID: "uid-carol", ClusterID: "c1"},
		{ID: "i-other", Name: "api", Namespace: "stack-api-carol", OwnerID: "uid-carol", ClusterID: "c2"},
		{ID: "i-alice", Name: "Mine", Namespace: "stack-mine-alice", OwnerID: "uid-1", ClusterID: "c1"},
	}
	tests := []struct {
		name        string
		unique      bool
		body        string
		wantStatus  int
		wantMessage string
	}{
		{name: "setting off allows a taken name", body: `{"name":"demo"}`, wantStatus: http.StatusOK},
		{name: "a name taken on the same cluster gives 409", unique: true, body: `{"name":"demo"}`, wantStatus: http.StatusConflict,
			wantMessage: `A stack named "demo" already exists on this cluster (owner: carol)`},
		{name: "a name taken on another cluster is allowed", unique: true, body: `{"name":"api"}`, wantStatus: http.StatusOK},
		{name: "a case change of the own name is allowed", unique: true, body: `{"name":"mine"}`, wantStatus: http.StatusOK},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newUniqueNamesFixture(t, seed...)
			w := doJSON(f.router(tt.unique, nil), http.MethodPut, "/api/v1/stack-instances/i-alice", tt.body)
			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantMessage == "" {
				return
			}
			var resp NamespaceConflictResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, msgInstanceNameExists, resp.Error)
			assert.Equal(t, tt.wantMessage, resp.Message)
		})
	}
}

func TestUniqueInstanceNames_Clone(t *testing.T) {
	t.Parallel()

	// carol already uses demo-copy on c1. Its namespace (stack-demo-copy-carol)
	// does not block the namespace of alice.
	seed := []models.StackInstance{
		{ID: "i-src", Name: "demo", Namespace: "stack-demo-alice", OwnerID: "uid-1", ClusterID: "c1"},
		{ID: "i-carol", Name: "demo-copy", Namespace: "stack-demo-copy-carol", OwnerID: "uid-carol", ClusterID: "c1"},
	}
	tests := []struct {
		name       string
		seed       []models.StackInstance
		unique     bool
		body       string
		wantStatus int
		wantName   string
		wantError  string
	}{
		{name: "setting off: the generated name ignores other owners", wantStatus: http.StatusCreated, wantName: "demo-copy"},
		{name: "the generated name skips a name taken on the cluster", unique: true, wantStatus: http.StatusCreated, wantName: "demo-copy-2"},
		{name: "an explicit taken name gives 409", unique: true, body: `{"name":"demo-copy"}`, wantStatus: http.StatusConflict},
		{name: "an explicit free name is used", unique: true, body: `{"name":"fresh"}`, wantStatus: http.StatusCreated, wantName: "fresh"},
		{
			// demo-copy-2: the namespace of alice is taken (her own stack on
			// c2). demo-copy (carol) is taken on c1.
			name: "the generated name skips a taken namespace and a taken name",
			seed: []models.StackInstance{
				{ID: "i-alice-2", Name: "demo-copy-2", Namespace: "stack-demo-copy-2-alice", OwnerID: "uid-1", ClusterID: "c2"},
			},
			unique: true, wantStatus: http.StatusCreated, wantName: "demo-copy-3",
		},
		{
			name:   "all generated names taken gives 409 no free clone name",
			seed:   takenCloneNames(),
			unique: true, wantStatus: http.StatusConflict, wantError: "no free clone name",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newUniqueNamesFixture(t, append(append([]models.StackInstance{}, seed...), tt.seed...)...)
			w := doJSON(f.router(tt.unique, nil), http.MethodPost, "/api/v1/stack-instances/i-src/clone", tt.body)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantError != "" {
				var resp NamespaceConflictResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, tt.wantError, resp.Error)
			}
			if tt.wantName == "" {
				return
			}
			var resp models.StackInstance
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, tt.wantName, resp.Name)
			assert.Equal(t, "c1", resp.ClusterID)
		})
	}
}

func TestUniqueInstanceNames_QuickDeploy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		unique     bool
		instance   string
		wantStatus int
	}{
		{name: "setting off allows a taken name", instance: "demo", wantStatus: http.StatusAccepted},
		{name: "a name taken on the default cluster gives 409", unique: true, instance: "demo", wantStatus: http.StatusConflict},
		{name: "a free name deploys", unique: true, instance: "fresh", wantStatus: http.StatusAccepted},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// carol's demo has no cluster, so it counts on the default cluster.
			f := newUniqueNamesFixture(t, models.StackInstance{ID: "i-carol", Name: "demo", Namespace: "stack-demo-carol", OwnerID: "uid-carol"})
			tmplRepo := NewMockStackTemplateRepository()
			tmplChartRepo := NewMockTemplateChartConfigRepository()
			seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)
			require.NoError(t, tmplChartRepo.Create(context.Background(), &models.TemplateChartConfig{
				ID: "tc1", StackTemplateID: "t1", ChartName: "nginx",
				RepositoryURL: "oci://example.com/charts/nginx", DeployOrder: 1,
			}))
			ccRepo := NewMockChartConfigRepository()
			boRepo := NewMockChartBranchOverrideRepository()
			ovRepo := NewMockValueOverrideRepository()
			registry := cluster.NewRegistryForTest("test-cluster", nil, &noopHelmExecutor{})

			h, err := NewQuickDeployHandler(
				tmplRepo, tmplChartRepo, f.defs, ccRepo,
				f.instances, boRepo, ovRepo, helm.NewValuesGenerator(),
				newTestManager(f.instances, NewMockDeploymentLogRepository()), f.users, nil, NewMockAuditLogRepository(),
				&MockBroadcastSender{}, registry, nil,
				0,
				&mockHandlerTxRunner{repos: database.TxRepos{
					StackDefinition: f.defs,
					ChartConfig:     ccRepo,
					StackInstance:   f.instances,
					ValueOverride:   ovRepo,
					BranchOverride:  boRepo,
				}},
			)
			require.NoError(t, err)
			h.WithTemplateVersions(newWorkingCopyReleaseRepo(tmplRepo, tmplChartRepo)).WithUniqueInstanceNames(tt.unique)

			gin.SetMode(gin.TestMode)
			r := gin.New()
			r.Use(func(c *gin.Context) {
				c.Set("userID", "uid-1")
				c.Set("username", "alice")
				c.Set("role", "user")
				c.Next()
			})
			r.POST("/api/v1/templates/:id/quick-deploy", h.QuickDeploy)

			w := doJSON(r, http.MethodPost, "/api/v1/templates/t1/quick-deploy", `{"instance_name":"`+tt.instance+`"}`)
			assert.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantStatus == http.StatusConflict {
				assert.Contains(t, w.Body.String(), "(owner: carol)")
			}
		})
	}
}

func TestLogDuplicateInstanceNames(t *testing.T) {
	t.Parallel()
	f := newUniqueNamesFixture(t,
		models.StackInstance{ID: "a", Name: "demo", Namespace: "stack-demo-a", OwnerID: "uid-1", ClusterID: "c1"},
		models.StackInstance{ID: "b", Name: "Demo", Namespace: "stack-demo-b", OwnerID: "uid-carol"},
	)
	dups, err := f.instances.DuplicateInstanceNames(context.Background(), "c1")
	require.NoError(t, err)
	assert.Equal(t, []models.DuplicateInstanceName{{ClusterID: "c1", Name: "demo", Count: 2}}, dups)
	// The log helper must not panic on the result or on an error.
	LogDuplicateInstanceNames(context.Background(), f.instances, "c1")
	f.instances.SetError(assert.AnError)
	LogDuplicateInstanceNames(context.Background(), f.instances, "c1")
}

// takenCloneNames returns instances of carol on c1 for every generated clone
// name of demo (demo-copy-2 ... demo-copy-<maxCloneNameAttempts>).
func takenCloneNames() []models.StackInstance {
	out := make([]models.StackInstance, 0, maxCloneNameAttempts)
	for n := 2; n <= maxCloneNameAttempts; n++ {
		name := cloneNameCandidate("demo", n)
		out = append(out, models.StackInstance{
			ID: "i-" + name, Name: name, Namespace: "stack-" + name + "-carol", OwnerID: "uid-carol", ClusterID: "c1",
		})
	}
	return out
}

// noNameLookupRepo hides models.InstanceNameLookup of the mock.
type noNameLookupRepo struct {
	models.StackInstanceRepository
}

func TestUniqueInstanceNames_LookupErrorGives500(t *testing.T) {
	t.Parallel()

	seed := []models.StackInstance{
		{ID: "i-alice", Name: "mine", Namespace: "stack-mine-alice", OwnerID: "uid-1", ClusterID: "c1"},
	}
	tests := []struct {
		name     string
		noLookup bool
		method   string
		path     string
		body     string
	}{
		{name: "create", method: http.MethodPost, path: "/api/v1/stack-instances", body: `{"stack_definition_id":"d1","name":"demo","cluster_id":"c1"}`},
		{name: "rename", method: http.MethodPut, path: "/api/v1/stack-instances/i-alice", body: `{"name":"demo"}`},
		{name: "clone with a given name", method: http.MethodPost, path: "/api/v1/stack-instances/i-alice/clone", body: `{"name":"demo"}`},
		{name: "clone with a generated name", method: http.MethodPost, path: "/api/v1/stack-instances/i-alice/clone"},
		{name: "repository without name lookup", noLookup: true, method: http.MethodPost, path: "/api/v1/stack-instances", body: `{"stack_definition_id":"d1","name":"demo","cluster_id":"c1"}`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newUniqueNamesFixture(t, seed...)
			var repo models.StackInstanceRepository = f.instances
			if tt.noLookup {
				repo = noNameLookupRepo{f.instances}
			} else {
				f.instances.nameLookupErr = assert.AnError
			}
			w := doJSON(f.routerWith(repo, true, nil), tt.method, tt.path, tt.body)
			assert.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
			assert.JSONEq(t, `{"error":"Internal server error"}`, w.Body.String())
		})
	}
}

// resolveTestRegistry returns a registry over clusters c1 (default) and c2.
// defaultErr, when set, fails the default cluster read.
func resolveTestRegistry(t *testing.T, defaultErr error) *cluster.Registry {
	t.Helper()
	clusters := NewMockClusterRepository()
	require.NoError(t, clusters.Create(context.Background(), &models.Cluster{ID: "c1", Name: "one", IsDefault: true}))
	require.NoError(t, clusters.Create(context.Background(), &models.Cluster{ID: "c2", Name: "two"}))
	clusters.defaultErr = defaultErr
	return cluster.NewRegistry(cluster.RegistryOptions{ClusterRepo: clusters, HelmBinary: "helm"})
}

// TestUniqueInstanceNames_DefaultResolveErrorStopsWrite checks that a failed
// default cluster resolve stops the write (#656). Before, the guard used ""
// as the default cluster and skipped the instances without a cluster, so
// carol's "demo" (no cluster, so on the default c1) did not block the name.
func TestUniqueInstanceNames_DefaultResolveErrorStopsWrite(t *testing.T) {
	t.Parallel()

	seed := []models.StackInstance{
		{ID: "i-carol", Name: "demo", Namespace: "stack-demo-carol", OwnerID: "uid-carol"},
		{ID: "i-alice", Name: "mine", Namespace: "stack-mine-alice", OwnerID: "uid-1", ClusterID: "c1"},
	}
	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "create", method: http.MethodPost, path: "/api/v1/stack-instances", body: `{"stack_definition_id":"d1","name":"demo","cluster_id":"c1"}`},
		{name: "rename", method: http.MethodPut, path: "/api/v1/stack-instances/i-alice", body: `{"name":"demo"}`},
		{name: "clone", method: http.MethodPost, path: "/api/v1/stack-instances/i-alice/clone", body: `{"name":"demo"}`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newUniqueNamesFixture(t, seed...)

			w := doJSON(f.router(true, resolveTestRegistry(t, errors.New("db down"))), tt.method, tt.path, tt.body)

			require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
			assert.JSONEq(t, `{"error":"Internal server error"}`, w.Body.String())
			all, err := f.instances.List(context.Background())
			require.NoError(t, err)
			assert.Len(t, all, 2, "no instance is created")
			mine, err := f.instances.FindByID(context.Background(), "i-alice")
			require.NoError(t, err)
			assert.Equal(t, "mine", mine.Name, "the instance is not renamed")
		})
	}

	t.Run("no default cluster lets a free name pass", func(t *testing.T) {
		t.Parallel()
		f := newUniqueNamesFixture(t, seed...)
		registry := resolveTestRegistry(t, dberrors.NewDatabaseError("find_default", dberrors.ErrNotFound))

		w := doJSON(f.router(true, registry), http.MethodPut, "/api/v1/stack-instances/i-alice", `{"name":"fresh"}`)

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	})
}

// TestCreateInstance_DefaultResolveError checks the answer to a failed
// default cluster resolve of a create without cluster_id: 400 only when no
// default cluster exists, else 500 and no instance (#656).
func TestCreateInstance_DefaultResolveError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		err        error
		name       string
		wantStatus int
	}{
		{name: "no default cluster", err: dberrors.NewDatabaseError("find_default", dberrors.ErrNotFound), wantStatus: http.StatusBadRequest},
		{name: "database error", err: errors.New("db down"), wantStatus: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newUniqueNamesFixture(t)

			w := doJSON(f.router(false, resolveTestRegistry(t, tt.err)), http.MethodPost, "/api/v1/stack-instances",
				`{"stack_definition_id":"d1","name":"demo"}`)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantStatus == http.StatusInternalServerError {
				assert.JSONEq(t, `{"error":"Internal server error"}`, w.Body.String())
			}
			all, err := f.instances.List(context.Background())
			require.NoError(t, err)
			assert.Empty(t, all, "no instance is created")
		})
	}
}

// TestQuickDeploy_DefaultResolveError checks the same answers for a quick
// deploy: 400 only when no default cluster exists, else 500; no definition
// and no instance is created.
func TestQuickDeploy_DefaultResolveError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		err        error
		name       string
		wantStatus int
	}{
		{name: "no default cluster", err: dberrors.NewDatabaseError("find_default", dberrors.ErrNotFound), wantStatus: http.StatusBadRequest},
		{name: "database error", err: errors.New("db down"), wantStatus: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newUniqueNamesFixture(t)
			tmplRepo := NewMockStackTemplateRepository()
			tmplChartRepo := NewMockTemplateChartConfigRepository()
			seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)
			require.NoError(t, tmplChartRepo.Create(context.Background(), &models.TemplateChartConfig{
				ID: "tc1", StackTemplateID: "t1", ChartName: "nginx",
				RepositoryURL: "oci://example.com/charts/nginx", DeployOrder: 1,
			}))
			ccRepo := NewMockChartConfigRepository()
			boRepo := NewMockChartBranchOverrideRepository()
			ovRepo := NewMockValueOverrideRepository()
			h, err := NewQuickDeployHandler(
				tmplRepo, tmplChartRepo, f.defs, ccRepo,
				f.instances, boRepo, ovRepo, helm.NewValuesGenerator(),
				newTestManager(f.instances, NewMockDeploymentLogRepository()), f.users, nil, NewMockAuditLogRepository(),
				&MockBroadcastSender{}, resolveTestRegistry(t, tt.err), nil,
				0,
				&mockHandlerTxRunner{repos: database.TxRepos{
					StackDefinition: f.defs,
					ChartConfig:     ccRepo,
					StackInstance:   f.instances,
					ValueOverride:   ovRepo,
					BranchOverride:  boRepo,
				}},
			)
			require.NoError(t, err)
			h.WithTemplateVersions(newWorkingCopyReleaseRepo(tmplRepo, tmplChartRepo))
			gin.SetMode(gin.TestMode)
			r := gin.New()
			r.Use(func(c *gin.Context) {
				c.Set("userID", "uid-1")
				c.Set("username", "alice")
				c.Set("role", "user")
				c.Next()
			})
			r.POST("/api/v1/templates/:id/quick-deploy", h.QuickDeploy)

			w := doJSON(r, http.MethodPost, "/api/v1/templates/t1/quick-deploy", `{"instance_name":"demo"}`)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantStatus == http.StatusInternalServerError {
				assert.JSONEq(t, `{"error":"Internal server error"}`, w.Body.String())
			}
			all, err := f.instances.List(context.Background())
			require.NoError(t, err)
			assert.Empty(t, all, "no instance is created")
			defs, err := f.defs.List(context.Background())
			require.NoError(t, err)
			assert.Len(t, defs, 1, "only the seeded definition exists")
		})
	}
}

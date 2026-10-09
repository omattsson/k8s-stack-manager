package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/internal/database"
	"backend/internal/deployer"
	"backend/internal/helm"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lifecycleEnv bundles the mock repositories for the stack lifecycle tests
// (extend, names, clone, rollback to a target, values drift, owned
// definitions).
type lifecycleEnv struct {
	inst  *MockStackInstanceRepository
	ov    *MockValueOverrideRepository
	bo    *MockChartBranchOverrideRepository
	def   *MockStackDefinitionRepository
	cc    *MockChartConfigRepository
	logs  *MockDeploymentLogRepository
	quota *MockInstanceQuotaOverrideRepository
}

func newLifecycleEnv() *lifecycleEnv {
	return &lifecycleEnv{
		inst:  NewMockStackInstanceRepository(),
		ov:    NewMockValueOverrideRepository(),
		bo:    NewMockChartBranchOverrideRepository(),
		def:   NewMockStackDefinitionRepository(),
		cc:    NewMockChartConfigRepository(),
		logs:  NewMockDeploymentLogRepository(),
		quota: NewMockInstanceQuotaOverrideRepository(),
	}
}

// router builds an InstanceHandler over the env. mgr may be nil.
func (e *lifecycleEnv) router(t *testing.T, mgr *deployer.Manager, callerID, username, role string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userID", callerID)
		c.Set("username", username)
		c.Set("role", role)
		c.Next()
	})
	h, err := NewInstanceHandlerWithDeployer(
		e.inst, e.ov, e.bo, e.def, e.cc,
		NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
		helm.NewValuesGenerator(), NewMockUserRepository(),
		mgr, nil, nil, e.logs, nil, 0,
		&mockHandlerTxRunner{repos: database.TxRepos{
			StackDefinition:       e.def,
			ChartConfig:           e.cc,
			StackInstance:         e.inst,
			ValueOverride:         e.ov,
			BranchOverride:        e.bo,
			DeploymentLog:         e.logs,
			InstanceQuotaOverride: e.quota,
		}},
	)
	require.NoError(t, err)
	g := r.Group("/api/v1/stack-instances")
	g.POST("", h.CreateInstance)
	g.GET("/:id", h.GetInstance)
	g.PUT("/:id", h.UpdateInstance)
	g.DELETE("/:id", h.DeleteInstance)
	g.POST("/:id/clone", h.CloneInstance)
	g.POST("/:id/extend", h.ExtendTTL)
	g.POST("/:id/rollback", h.RollbackInstance)
	g.POST("/:id/deploy", h.DeployInstance)
	g.GET("/:id/deploy-preview", h.DeployPreview)
	return r
}

// serve sends a request with an optional JSON body.
func serve(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req, _ = http.NewRequest(method, path, nil)
	} else {
		req, _ = http.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// ---- #459 extend ----

func TestExtendExpiry(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	future := now.Add(3 * time.Hour)
	past := now.Add(-time.Hour)
	beyondCap := now.Add(time.Duration(MaxTTLMinutes)*time.Minute + time.Hour)
	limit := now.Add(time.Duration(MaxTTLMinutes) * time.Minute)

	tests := []struct {
		name    string
		current *time.Time
		minutes int
		want    time.Time
	}{
		{"adds to a future expiry", &future, 60, future.Add(time.Hour)},
		{"adds to now when expired", &past, 60, now.Add(time.Hour)},
		{"adds to now when no expiry", nil, 30, now.Add(30 * time.Minute)},
		{"caps at now + max TTL", &future, MaxTTLMinutes, limit},
		{"huge minutes are capped", nil, 1 << 40, limit},
		{"never earlier than the current expiry", &beyondCap, 60, beyondCap},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extendExpiry(tt.current, tt.minutes, now)
			require.NotNil(t, got)
			assert.True(t, got.Equal(tt.want), "got %s want %s", got, tt.want)
		})
	}
}

func TestExtendTTL_Semantics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		ttl         int
		expiresIn   *time.Duration // nil: no expiry
		body        string
		wantStatus  int
		wantTTL     int
		wantMinLeft time.Duration // new expiry is at least now + wantMinLeft
		wantMaxLeft time.Duration // and at most now + wantMaxLeft
		wantWarning bool
	}{
		{name: "minutes adds to the current expiry", ttl: 240, expiresIn: durPtr(3 * time.Hour), body: `{"minutes":60}`,
			wantStatus: http.StatusOK, wantTTL: 240, wantMinLeft: 4*time.Hour - time.Minute, wantMaxLeft: 4*time.Hour + time.Minute},
		{name: "minutes on an expired instance adds to now", ttl: 240, expiresIn: durPtr(-time.Hour), body: `{"minutes":60}`,
			wantStatus: http.StatusOK, wantTTL: 240, wantMinLeft: 59 * time.Minute, wantMaxLeft: 61 * time.Minute},
		{name: "minutes never shortens a long expiry", ttl: 240, expiresIn: durPtr(10 * time.Hour), body: `{"minutes":1}`,
			wantStatus: http.StatusOK, wantTTL: 240, wantMinLeft: 10 * time.Hour, wantMaxLeft: 10*time.Hour + 2*time.Minute},
		{name: "minutes is capped at 30 days", ttl: 240, expiresIn: durPtr(29 * 24 * time.Hour), body: `{"minutes":43200}`,
			wantStatus: http.StatusOK, wantTTL: 240, wantMinLeft: 30*24*time.Hour - time.Minute, wantMaxLeft: 30*24*time.Hour + time.Minute},
		{name: "minutes zero returns 400", ttl: 240, expiresIn: durPtr(time.Hour), body: `{"minutes":0}`, wantStatus: http.StatusBadRequest},
		{name: "negative minutes returns 400", ttl: 240, expiresIn: durPtr(time.Hour), body: `{"minutes":-5}`, wantStatus: http.StatusBadRequest},
		{name: "minutes without TTL and expiry returns 400", ttl: 0, body: `{"minutes":60}`, wantStatus: http.StatusBadRequest},
		{name: "empty body adds one TTL to the current expiry", ttl: 120, expiresIn: durPtr(time.Hour), body: "",
			wantStatus: http.StatusOK, wantTTL: 120, wantMinLeft: 3*time.Hour - time.Minute, wantMaxLeft: 3*time.Hour + time.Minute},
		{name: "deprecated ttl_minutes resets expiry and TTL", ttl: 240, expiresIn: durPtr(3 * time.Hour), body: `{"ttl_minutes":60}`,
			wantStatus: http.StatusOK, wantTTL: 60, wantMinLeft: 59 * time.Minute, wantMaxLeft: 61 * time.Minute, wantWarning: true},
		{name: "minutes wins over ttl_minutes", ttl: 240, expiresIn: durPtr(time.Hour), body: `{"minutes":60,"ttl_minutes":5}`,
			wantStatus: http.StatusOK, wantTTL: 240, wantMinLeft: 2*time.Hour - time.Minute, wantMaxLeft: 2*time.Hour + time.Minute},
		{name: "negative ttl_minutes returns 400", ttl: 240, expiresIn: durPtr(time.Hour), body: `{"ttl_minutes":-1}`, wantStatus: http.StatusBadRequest},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newLifecycleEnv()
			inst := &models.StackInstance{
				ID: "i1", StackDefinitionID: "d1", Name: "zz-life", Namespace: "stack-zz-life-alice",
				OwnerID: "uid-1", Branch: "master", Status: models.StackStatusRunning, TTLMinutes: tt.ttl,
			}
			if tt.expiresIn != nil {
				exp := time.Now().UTC().Add(*tt.expiresIn)
				inst.ExpiresAt = &exp
			}
			require.NoError(t, env.inst.Create(inst))

			w := serve(env.router(t, nil, "uid-1", "alice", "user"), http.MethodPost, "/api/v1/stack-instances/i1/extend", tt.body)
			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantStatus != http.StatusOK {
				return
			}
			var got models.StackInstance
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
			assert.Equal(t, tt.wantTTL, got.TTLMinutes)
			require.NotNil(t, got.ExpiresAt)
			left := time.Until(*got.ExpiresAt)
			assert.GreaterOrEqual(t, left, tt.wantMinLeft)
			assert.LessOrEqual(t, left, tt.wantMaxLeft)
			assert.Equal(t, tt.wantWarning, w.Header().Get("Warning") != "")
		})
	}
}

func durPtr(d time.Duration) *time.Duration { return &d }

// ---- #460 names and clone ----

func TestInstanceNameRule(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
	}{
		{"create with space and uppercase returns 400", http.MethodPost, "/api/v1/stack-instances", `{"stack_definition_id":"d1","name":"Zz Life"}`, http.StatusBadRequest},
		{"create with leading dash returns 400", http.MethodPost, "/api/v1/stack-instances", `{"stack_definition_id":"d1","name":"-abc"}`, http.StatusBadRequest},
		{"create with valid name returns 201", http.MethodPost, "/api/v1/stack-instances", `{"stack_definition_id":"d1","name":"zz-life-2"}`, http.StatusCreated},
		{"update keeping a legacy name works", http.MethodPut, "/api/v1/stack-instances/legacy", `{"name":"Legacy (Copy)","branch":"dev"}`, http.StatusOK},
		{"update without name works", http.MethodPut, "/api/v1/stack-instances/legacy", `{"branch":"dev"}`, http.StatusOK},
		{"rename to an invalid name returns 400", http.MethodPut, "/api/v1/stack-instances/legacy", `{"name":"New Name"}`, http.StatusBadRequest},
		{"rename to a valid name works", http.MethodPut, "/api/v1/stack-instances/legacy", `{"name":"new-name"}`, http.StatusOK},
		{"clone with invalid name returns 400", http.MethodPost, "/api/v1/stack-instances/legacy/clone", `{"name":"Bad_Name"}`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newLifecycleEnv()
			seedDefinition(t, env.def, "d1", "def-1", "uid-1")
			require.NoError(t, env.inst.Create(&models.StackInstance{
				ID: "legacy", StackDefinitionID: "d1", Name: "Legacy (Copy)", Namespace: "stack-legacy-copy-alice",
				OwnerID: "uid-1", Branch: "master", Status: models.StackStatusDraft,
			}))
			w := serve(env.router(t, nil, "uid-1", "alice", "user"), tt.method, tt.path, tt.body)
			assert.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantStatus == http.StatusBadRequest && strings.Contains(tt.body, `"name"`) {
				assert.Contains(t, w.Body.String(), "name")
			}
		})
	}
}

func TestCloneInstance_CopiesSettings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		sourceName string
		body       string
		wantName   string
		wantBranch string
		wantTTL    int
	}{
		{name: "defaults copy name, branch and TTL", sourceName: "zz-life", body: "", wantName: "zz-life-copy", wantBranch: "feature-x", wantTTL: 240},
		{name: "legacy source name is sanitized", sourceName: "Zz Life (Copy)", body: "", wantName: "zz-life-copy-copy", wantBranch: "feature-x", wantTTL: 240},
		{name: "body sets name, branch and TTL", sourceName: "zz-life", body: `{"name":"my-clone","branch":"main","ttl_minutes":0}`, wantName: "my-clone", wantBranch: "main", wantTTL: 0},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newLifecycleEnv()
			exp := time.Now().UTC().Add(time.Hour)
			require.NoError(t, env.inst.Create(&models.StackInstance{
				ID: "src", StackDefinitionID: "d1", Name: tt.sourceName, Namespace: "stack-src-alice",
				OwnerID: "uid-1", Branch: "feature-x", ClusterID: "cluster-a", Status: models.StackStatusRunning,
				TTLMinutes: 240, ExpiresAt: &exp,
			}))
			require.NoError(t, env.ov.Create(&models.ValueOverride{ID: "ov1", StackInstanceID: "src", ChartConfigID: "cc1", Values: "replicas: 3"}))
			require.NoError(t, env.bo.Set(&models.ChartBranchOverride{ID: "bo1", StackInstanceID: "src", ChartConfigID: "cc1", Branch: "hotfix"}))
			pods := 7
			require.NoError(t, env.quota.Upsert(context.Background(), &models.InstanceQuotaOverride{
				StackInstanceID: "src", CPULimit: "2", MemoryLimit: "4Gi", PodLimit: &pods,
			}))

			w := serve(env.router(t, nil, "uid-2", "bob", "user"), http.MethodPost, "/api/v1/stack-instances/src/clone", tt.body)
			require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

			var clone models.StackInstance
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &clone))
			assert.Equal(t, tt.wantName, clone.Name)
			assert.NoError(t, models.ValidateInstanceName(clone.Name))
			assert.Equal(t, "stack-"+tt.wantName+"-bob", clone.Namespace)
			assert.Equal(t, tt.wantBranch, clone.Branch)
			assert.Equal(t, tt.wantTTL, clone.TTLMinutes)
			assert.Equal(t, tt.wantTTL > 0, clone.ExpiresAt != nil)
			assert.Equal(t, "cluster-a", clone.ClusterID)
			assert.Equal(t, "uid-2", clone.OwnerID)
			assert.Equal(t, models.StackStatusDraft, clone.Status)

			ovs, err := env.ov.ListByInstance(clone.ID)
			require.NoError(t, err)
			require.Len(t, ovs, 1)
			assert.Equal(t, "replicas: 3", ovs[0].Values)

			bos, err := env.bo.List(clone.ID)
			require.NoError(t, err)
			require.Len(t, bos, 1)
			assert.Equal(t, "hotfix", bos[0].Branch)

			q, err := env.quota.GetByInstanceID(context.Background(), clone.ID)
			require.NoError(t, err)
			assert.Equal(t, "2", q.CPULimit)
			assert.Equal(t, "4Gi", q.MemoryLimit)
			require.NotNil(t, q.PodLimit)
			assert.Equal(t, 7, *q.PodLimit)
		})
	}
}

func TestCloneNameCandidate(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", 50)
	tests := []struct {
		base string
		n    int
		want string
	}{
		{"zz-life", 1, "zz-life-copy"},
		{"zz-life", 2, "zz-life-copy-2"},
		{long, 1, strings.Repeat("a", 45) + "-copy"},
		{long, 12, strings.Repeat("a", 42) + "-copy-12"},
		{"abc-" + strings.Repeat("b", 46), 1, "abc-" + strings.Repeat("b", 41) + "-copy"},
	}
	for _, tt := range tests {
		got := cloneNameCandidate(tt.base, tt.n)
		assert.Equal(t, tt.want, got)
		assert.NoError(t, models.ValidateInstanceName(got))
	}
}

// ---- #454 rollback to a target, values drift ----

// seedRollbackInstance seeds a running instance with one chart (default
// replicas: 1) and a stored override replicas: 2.
func seedRollbackInstance(t *testing.T, env *lifecycleEnv) {
	t.Helper()
	seedDefinition(t, env.def, "d1", "def-1", "uid-1")
	require.NoError(t, env.cc.Create(&models.ChartConfig{ID: "cc1", StackDefinitionID: "d1", ChartName: "web", DefaultValues: "replicas: 1\n"}))
	require.NoError(t, env.inst.Create(&models.StackInstance{
		ID: "i1", StackDefinitionID: "d1", Name: "zz-life", Namespace: "stack-zz-life-alice",
		OwnerID: "uid-1", Branch: "master", Status: models.StackStatusRunning,
	}))
	require.NoError(t, env.ov.Create(&models.ValueOverride{ID: "ov1", StackInstanceID: "i1", ChartConfigID: "cc1", Values: "replicas: 2\n"}))
}

func seedLog(t *testing.T, env *lifecycleEnv, log models.DeploymentLog) {
	t.Helper()
	if log.StartedAt.IsZero() {
		log.StartedAt = time.Now().UTC()
	}
	require.NoError(t, env.logs.Create(context.Background(), &log))
}

func TestRollbackInstance_Target(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		log        *models.DeploymentLog
		body       string
		wantStatus int
		wantDrift  *bool
	}{
		{name: "target with other values reports drift",
			log:  &models.DeploymentLog{ID: "dep-a", StackInstanceID: "i1", Action: models.DeployActionDeploy, Status: models.DeployLogSuccess, ValuesSnapshot: `{"web":"replicas: 1\n"}`, Branch: "old"},
			body: `{"target_log_id":"dep-a"}`, wantStatus: http.StatusAccepted, wantDrift: boolPtr(true)},
		{name: "target equal to stored overrides has no drift",
			log:  &models.DeploymentLog{ID: "dep-b", StackInstanceID: "i1", Action: models.DeployActionDeploy, Status: models.DeployLogSuccess, ValuesSnapshot: `{"web":"replicas:   2"}`},
			body: `{"target_log_id":"dep-b"}`, wantStatus: http.StatusAccepted, wantDrift: boolPtr(false)},
		{name: "no target keeps one revision back",
			body: `{}`, wantStatus: http.StatusAccepted},
		{name: "unknown target returns 404",
			body: `{"target_log_id":"nope"}`, wantStatus: http.StatusNotFound},
		{name: "target of another instance returns 404",
			log:  &models.DeploymentLog{ID: "dep-x", StackInstanceID: "other", Action: models.DeployActionDeploy, Status: models.DeployLogSuccess, ValuesSnapshot: `{"web":"a: 1"}`},
			body: `{"target_log_id":"dep-x"}`, wantStatus: http.StatusNotFound},
		{name: "failed deploy target returns 400",
			log:  &models.DeploymentLog{ID: "dep-e", StackInstanceID: "i1", Action: models.DeployActionDeploy, Status: models.DeployLogError, ValuesSnapshot: `{"web":"a: 1"}`},
			body: `{"target_log_id":"dep-e"}`, wantStatus: http.StatusBadRequest},
		{name: "stop log target returns 400",
			log:  &models.DeploymentLog{ID: "stop-1", StackInstanceID: "i1", Action: models.DeployActionStop, Status: models.DeployLogSuccess},
			body: `{"target_log_id":"stop-1"}`, wantStatus: http.StatusBadRequest},
		{name: "target without values snapshot returns 400",
			log:  &models.DeploymentLog{ID: "dep-n", StackInstanceID: "i1", Action: models.DeployActionDeploy, Status: models.DeployLogSuccess},
			body: `{"target_log_id":"dep-n"}`, wantStatus: http.StatusBadRequest},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newLifecycleEnv()
			seedRollbackInstance(t, env)
			if tt.log != nil {
				seedLog(t, env, *tt.log)
			}
			mgr := newTestManager(env.inst, env.logs)
			w := serve(env.router(t, mgr, "uid-1", "alice", "user"), http.MethodPost, "/api/v1/stack-instances/i1/rollback", tt.body)
			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantStatus != http.StatusAccepted {
				return
			}
			var resp RollbackResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.NotEmpty(t, resp.LogID)
			if tt.wantDrift == nil {
				assert.Nil(t, resp.ValuesDrift)
				assert.Empty(t, resp.Warning)
			} else {
				require.NotNil(t, resp.ValuesDrift)
				assert.Equal(t, *tt.wantDrift, *resp.ValuesDrift)
				assert.Equal(t, *tt.wantDrift, resp.Warning != "")
			}
			// The rollback log records the target and its branch.
			rbLog, err := env.logs.FindByID(context.Background(), resp.LogID)
			require.NoError(t, err)
			if tt.log != nil {
				assert.Equal(t, tt.log.ID, rbLog.TargetLogID)
				if tt.log.Branch != "" {
					assert.Equal(t, tt.log.Branch, rbLog.Branch)
				}
			}
		})
	}
}

func TestValuesDrift_PreviewAndGet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		running      string // LastDeployedValues
		latestAction string
		latestStatus string
		wantDrift    bool
		wantChanges  bool
	}{
		{"rollback with other values drifts", `{"web":"replicas: 1\n"}`, models.DeployActionRollback, models.DeployLogSuccess, true, true},
		{"rollback with equal values does not drift", `{"web":"replicas: 2"}`, models.DeployActionRollback, models.DeployLogSuccess, false, false},
		{"deploy with other values is a plain change", `{"web":"replicas: 1\n"}`, models.DeployActionDeploy, models.DeployLogSuccess, false, true},
		{"failed rollback does not drift", `{"web":"replicas: 1\n"}`, models.DeployActionRollback, models.DeployLogError, false, true},
		{"stop after a rollback still drifts", `{"web":"replicas: 1\n"}`, models.DeployActionStop, models.DeployLogSuccess, true, true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newLifecycleEnv()
			seedRollbackInstance(t, env)
			inst, err := env.inst.FindByID("i1")
			require.NoError(t, err)
			inst.LastDeployedValues = tt.running
			require.NoError(t, env.inst.Update(inst))
			seedLog(t, env, models.DeploymentLog{ID: "old", StackInstanceID: "i1", Action: models.DeployActionDeploy, Status: models.DeployLogSuccess, StartedAt: time.Now().Add(-2 * time.Hour)})
			if tt.latestAction == models.DeployActionStop {
				// A stop or clean log after the rollback is ignored.
				seedLog(t, env, models.DeploymentLog{ID: "rb", StackInstanceID: "i1", Action: models.DeployActionRollback, Status: models.DeployLogSuccess, StartedAt: time.Now().Add(-time.Hour)})
			}
			seedLog(t, env, models.DeploymentLog{ID: "latest", StackInstanceID: "i1", Action: tt.latestAction, Status: tt.latestStatus})
			router := env.router(t, nil, "uid-1", "alice", "user")

			w := serve(router, http.MethodGet, "/api/v1/stack-instances/i1/deploy-preview", "")
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			var preview DeployPreviewResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &preview))
			require.Len(t, preview.Charts, 1)
			assert.Equal(t, tt.wantChanges, preview.Charts[0].HasChanges)
			assert.Equal(t, tt.wantDrift, preview.ValuesDrift)
			assert.Equal(t, tt.wantDrift, preview.Warning != "")

			w = serve(router, http.MethodGet, "/api/v1/stack-instances/i1", "")
			require.Equal(t, http.StatusOK, w.Code)
			var got map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
			drift, _ := got["values_drift"].(bool)
			assert.Equal(t, tt.wantDrift, drift)
		})
	}
}

func TestValuesEqual(t *testing.T) {
	t.Parallel()
	tests := []struct {
		a, b string
		want bool
	}{
		{"a: 1\nb: 2\n", "b: 2\na: 1", true},
		{"a:   1", "a: 1\n", true},
		{"", "{}", true},
		{"a: 1", "a: 2", false},
		{"a: [1, 2]", "a:\n  - 1\n  - 2\n", true},
		{"a: : :", "a: : :", true},
		{"a: : :", "b: 1", false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, valuesEqual(tt.a, tt.b), "%q vs %q", tt.a, tt.b)
	}
}

// ---- #458 owned definitions and unique names ----

func TestDeleteInstance_OwnedDefinition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		ownerInstance string
		otherUser     bool
		wantDefKept   bool
	}{
		{"quick deploy definition is deleted", "i1", false, false},
		{"definition used by another instance is kept", "i1", true, true},
		{"user definition is kept", "", false, true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newLifecycleEnv()
			require.NoError(t, env.def.Create(&models.StackDefinition{ID: "d1", Name: "zz-qd", OwnerID: "uid-1", OwnerInstanceID: tt.ownerInstance}))
			seedChartConfig(t, env.cc, "cc1", "d1", "web")
			seedInstance(t, env.inst, "i1", "zz-qd", "d1", "uid-1", models.StackStatusDraft)
			if tt.otherUser {
				seedInstance(t, env.inst, "i2", "zz-other", "d1", "uid-1", models.StackStatusDraft)
			}

			w := serve(env.router(t, nil, "uid-1", "alice", "user"), http.MethodDelete, "/api/v1/stack-instances/i1", "")
			require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())

			_, err := env.def.FindByID("d1")
			assert.Equal(t, tt.wantDefKept, err == nil)
			charts, err := env.cc.ListByDefinition("d1")
			require.NoError(t, err)
			assert.Equal(t, tt.wantDefKept, len(charts) == 1)
		})
	}
}

func TestDefinitionNames_UniquePerOwner(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
		wantName   string
	}{
		{"create with an existing name returns 409", http.MethodPost, "/api/v1/stack-definitions", `{"name":"zz-test-def"}`, http.StatusConflict, ""},
		{"create with a name of another owner works", http.MethodPost, "/api/v1/stack-definitions", `{"name":"other-def"}`, http.StatusCreated, "other-def"},
		{"rename to an existing name returns 409", http.MethodPut, "/api/v1/stack-definitions/d2", `{"name":"zz-test-def"}`, http.StatusConflict, ""},
		{"update keeping the name works", http.MethodPut, "/api/v1/stack-definitions/d1", `{"name":"zz-test-def","description":"x"}`, http.StatusOK, "zz-test-def"},
		{"import with an existing name is renamed", http.MethodPost, "/api/v1/stack-definitions/import",
			`{"schema_version":"1.0","definition":{"name":"zz-test-def"},"charts":[{"chart_name":"web"}]}`, http.StatusCreated, "zz-test-def (imported)"},
		{"import with a renamed name taken is renamed again", http.MethodPost, "/api/v1/stack-definitions/import",
			`{"schema_version":"1.0","definition":{"name":"dup"},"charts":[]}`, http.StatusCreated, "dup (imported 2)"},
		{"import with a new name keeps it", http.MethodPost, "/api/v1/stack-definitions/import",
			`{"schema_version":"1.0","definition":{"name":"fresh"},"charts":[]}`, http.StatusCreated, "fresh"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			defRepo := NewMockStackDefinitionRepository()
			seedDefinition(t, defRepo, "d1", "zz-test-def", "uid-1")
			seedDefinition(t, defRepo, "d2", "second", "uid-1")
			seedDefinition(t, defRepo, "d3", "other-def", "uid-9")
			seedDefinition(t, defRepo, "d4", "dup", "uid-1")
			seedDefinition(t, defRepo, "d5", "dup (imported)", "uid-1")
			router := setupDefinitionRouter(defRepo, NewMockChartConfigRepository(), NewMockStackInstanceRepository(), "uid-1", "user")

			w := serve(router, tt.method, tt.path, tt.body)
			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantName != "" {
				var got map[string]any
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
				assert.Equal(t, tt.wantName, got["name"])
			}
		})
	}
}

func TestInstantiateTemplate_DuplicateName(t *testing.T) {
	t.Parallel()
	tmplRepo := NewMockStackTemplateRepository()
	seedTemplate(t, tmplRepo, "t1", "tmpl", "uid-1", true)
	defRepo := NewMockStackDefinitionRepository()
	seedDefinition(t, defRepo, "d1", "taken", "uid-1")
	router := setupTemplateRouter(tmplRepo, NewMockTemplateChartConfigRepository(), defRepo, NewMockChartConfigRepository(), "uid-1", "user")

	w := serve(router, http.MethodPost, "/api/v1/templates/t1/instantiate", `{"name":"taken"}`)
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())

	w = serve(router, http.MethodPost, "/api/v1/templates/t1/instantiate", `{"name":"free"}`)
	assert.Equal(t, http.StatusCreated, w.Code, w.Body.String())
}

func TestQuickDeploy_OwnedDefinitionAndNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		instanceName string
		existingDefs []string
		wantStatus   int
		wantDefName  string
	}{
		{"definition is owned by the instance", "zz-qd", nil, http.StatusAccepted, "zz-qd"},
		{"definition name gets a number when taken", "zz-qd", []string{"zz-qd", "zz-qd (2)"}, http.StatusAccepted, "zz-qd (3)"},
		{"invalid instance name returns 400", "ZZ QD", nil, http.StatusBadRequest, ""},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tmplRepo := NewMockStackTemplateRepository()
			tmplChartRepo := NewMockTemplateChartConfigRepository()
			defRepo := NewMockStackDefinitionRepository()
			ccRepo := NewMockChartConfigRepository()
			instRepo := NewMockStackInstanceRepository()
			seedTemplate(t, tmplRepo, "t1", "tmpl", "owner-1", true)
			require.NoError(t, tmplChartRepo.Create(&models.TemplateChartConfig{ID: "tc1", StackTemplateID: "t1", ChartName: "web", DeployOrder: 1}))
			for i, n := range tt.existingDefs {
				seedDefinition(t, defRepo, "old-"+string(rune('a'+i)), n, "uid-1")
			}
			logRepo := NewMockDeploymentLogRepository()
			mgr := newTestManager(instRepo, logRepo)
			router := setupQuickDeployRouter(t, tmplRepo, tmplChartRepo, defRepo, ccRepo, instRepo,
				NewMockChartBranchOverrideRepository(), NewMockValueOverrideRepository(), NewMockAuditLogRepository(),
				mgr, nil, "uid-1", "alice", "user", 0)

			body, _ := json.Marshal(quickDeployRequest{InstanceName: tt.instanceName})
			w := serve(router, http.MethodPost, "/api/v1/templates/t1/quick-deploy", string(body))
			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantStatus != http.StatusAccepted {
				return
			}
			var resp quickDeployResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, tt.wantDefName, resp.Definition.Name)
			assert.Equal(t, resp.Instance.ID, resp.Definition.OwnerInstanceID)
		})
	}
}

func TestDeployExpiry(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	later := now.Add(10 * time.Hour)
	soon := now.Add(time.Hour)
	past := now.Add(-time.Hour)
	tests := []struct {
		name    string
		current *time.Time
		want    time.Time
	}{
		{"first deploy: now + TTL", nil, now.Add(4 * time.Hour)},
		{"expired: now + TTL", &past, now.Add(4 * time.Hour)},
		{"earlier expiry: now + TTL", &soon, now.Add(4 * time.Hour)},
		{"extended expiry is kept", &later, later},
	}
	for _, tt := range tests {
		got := deployExpiry(tt.current, 240, now)
		assert.True(t, got.Equal(tt.want), "%s: got %s want %s", tt.name, got, tt.want)
	}
}

func TestDeployInstance_RedeployKeepsLaterExpiry(t *testing.T) {
	t.Parallel()
	env := newLifecycleEnv()
	seedRollbackInstance(t, env)
	inst, err := env.inst.FindByID("i1")
	require.NoError(t, err)
	extended := time.Now().UTC().Add(48 * time.Hour)
	inst.TTLMinutes = 240
	inst.ExpiresAt = &extended
	require.NoError(t, env.inst.Update(inst))

	mgr := newTestManager(env.inst, env.logs)
	w := serve(env.router(t, mgr, "uid-1", "alice", "user"), http.MethodPost, "/api/v1/stack-instances/i1/deploy", "")
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	var resp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Eventually(t, func() bool {
		l, err := env.logs.FindByID(context.Background(), resp["log_id"])
		return err == nil && l.Status != models.DeployLogRunning
	}, 5*time.Second, 10*time.Millisecond)
	mgr.Shutdown()

	got, err := env.inst.FindByID("i1")
	require.NoError(t, err)
	require.NotNil(t, got.ExpiresAt)
	assert.True(t, got.ExpiresAt.Equal(extended), "a redeploy never makes the expiry earlier")
}

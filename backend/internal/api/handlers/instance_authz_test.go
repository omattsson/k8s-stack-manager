package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/cluster"
	"backend/internal/database"
	"backend/internal/deployer"
	"backend/internal/helm"
	"backend/internal/hooks"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	authzInstanceID = "inst-authz"
	authzChartID    = "chart-authz"
	authzOwnerID    = "uid-owner"
)

// countingHelmExecutor records every Helm call that changes a release.
type countingHelmExecutor struct {
	noopHelmExecutor
	calls atomic.Int64
}

func (e *countingHelmExecutor) Install(ctx context.Context, req deployer.InstallRequest) (string, error) {
	e.calls.Add(1)
	return e.noopHelmExecutor.Install(ctx, req)
}

func (e *countingHelmExecutor) Uninstall(ctx context.Context, req deployer.UninstallRequest) (string, error) {
	e.calls.Add(1)
	return e.noopHelmExecutor.Uninstall(ctx, req)
}

func (e *countingHelmExecutor) Rollback(ctx context.Context, name, ns string, rev int) (string, error) {
	e.calls.Add(1)
	return e.noopHelmExecutor.Rollback(ctx, name, ns, rev)
}

// authzFixture wires every instance endpoint that has an ownership rule
// against mocks that record side effects.
type authzFixture struct {
	router       *gin.Engine
	instRepo     *MockStackInstanceRepository
	overrideRepo *MockValueOverrideRepository
	boRepo       *MockChartBranchOverrideRepository
	quotaRepo    *MockInstanceQuotaOverrideRepository
	logRepo      *MockDeploymentLogRepository
	helmExec     *countingHelmExecutor
	hookRec      *handlerHookRecorder
	actionCalls  *atomic.Int64
}

func newAuthzFixture(t *testing.T, instStatus, callerID, callerRole string) *authzFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)

	f := &authzFixture{
		instRepo:     NewMockStackInstanceRepository(),
		overrideRepo: NewMockValueOverrideRepository(),
		boRepo:       NewMockChartBranchOverrideRepository(),
		quotaRepo:    NewMockInstanceQuotaOverrideRepository(),
		logRepo:      NewMockDeploymentLogRepository(),
		helmExec:     &countingHelmExecutor{},
		hookRec:      newHandlerHookRecorder(t),
		actionCalls:  &atomic.Int64{},
	}
	defRepo := NewMockStackDefinitionRepository()
	ccRepo := NewMockChartConfigRepository()

	// Seed: instance owned by authzOwnerID with a TTL (so a deploy would
	// change ExpiresAt), its definition, one chart and one override of each kind.
	inst := seedInstance(t, f.instRepo, authzInstanceID, "my-stack", "d1", authzOwnerID, instStatus)
	inst.TTLMinutes = 60
	require.NoError(t, f.instRepo.Update(inst))
	seedDefinition(t, defRepo, "d1", "My Def", authzOwnerID)
	require.NoError(t, ccRepo.Create(&models.ChartConfig{
		ID:                authzChartID,
		StackDefinitionID: "d1",
		ChartName:         "app",
		RepositoryURL:     "oci://example.com/charts/app",
		DeployOrder:       1,
	}))
	require.NoError(t, f.overrideRepo.Create(&models.ValueOverride{
		ID: "vo-1", StackInstanceID: authzInstanceID, ChartConfigID: authzChartID, Values: "replicas: 1",
	}))
	require.NoError(t, f.boRepo.Set(&models.ChartBranchOverride{
		ID: "bo-1", StackInstanceID: authzInstanceID, ChartConfigID: authzChartID, Branch: "feature/a",
	}))
	pods := 5
	require.NoError(t, f.quotaRepo.Upsert(context.Background(), &models.InstanceQuotaOverride{
		StackInstanceID: authzInstanceID, CPURequest: "100m", PodLimit: &pods,
	}))

	// Hook dispatcher subscribed to every event the handlers and the deployer fire.
	dispatcher, err := hooks.NewDispatcher(hooks.Config{Subscriptions: []hooks.Subscription{{
		Name: "recorder",
		Events: []string{
			hooks.EventPreDeploy, hooks.EventPostDeploy, hooks.EventDeployFinalized,
			hooks.EventPreInstanceDelete, hooks.EventPostInstanceDelete,
			hooks.EventPreRollback, hooks.EventPostRollback, hooks.EventRollbackCompleted,
			hooks.EventStopCompleted, hooks.EventCleanCompleted, hooks.EventDeleteCompleted,
		},
		URL:           f.hookRec.server.URL,
		FailurePolicy: hooks.FailurePolicyIgnore,
	}}}, f.hookRec.server.Client())
	require.NoError(t, err)

	// Action subscriber that counts invocations.
	actionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f.actionCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	t.Cleanup(actionSrv.Close)
	actions, err := hooks.NewActionRegistry([]hooks.ActionSubscription{{
		Name: "refresh-db", URL: actionSrv.URL, TimeoutSeconds: 5,
	}}, actionSrv.Client())
	require.NoError(t, err)

	mgr := deployer.NewManager(deployer.ManagerConfig{
		Registry:      cluster.NewRegistryForTest("test-cluster", nil, f.helmExec),
		InstanceRepo:  f.instRepo,
		DeployLogRepo: f.logRepo,
		Hub:           &MockBroadcastSender{},
		MaxConcurrent: 2,
		Hooks:         dispatcher,
	})

	h, err := NewInstanceHandlerWithDeployer(
		f.instRepo, f.overrideRepo, f.boRepo, defRepo, ccRepo,
		NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
		helm.NewValuesGenerator(), NewMockUserRepository(),
		mgr, nil, nil, f.logRepo, nil,
		0, &mockHandlerTxRunner{repos: database.TxRepos{
			StackInstance:  f.instRepo,
			ValueOverride:  f.overrideRepo,
			BranchOverride: f.boRepo,
		}},
	)
	require.NoError(t, err)
	h.WithHooks(dispatcher)
	h.WithActions(actions)

	boHandler := NewBranchOverrideHandler(f.boRepo, f.instRepo, ccRepo)
	quotaHandler := NewInstanceQuotaOverrideHandler(f.quotaRepo, f.instRepo)

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userID", callerID)
		c.Set("username", callerID)
		c.Set("role", callerRole)
		c.Next()
	})
	insts := r.Group("/api/v1/stack-instances")
	{
		insts.GET("/compare", h.CompareInstances)
		insts.GET("/:id", h.GetInstance)
		insts.POST("/:id/clone", h.CloneInstance)
		insts.GET("/:id/values", h.ExportAllValues)
		insts.GET("/:id/values/:chartId", h.ExportChartValues)
		insts.GET("/:id/status", h.GetInstanceStatus)
		insts.GET("/:id/pods", h.GetInstancePods)
		insts.PUT("/:id", h.UpdateInstance)
		insts.DELETE("/:id", h.DeleteInstance)
		insts.POST("/:id/deploy", h.DeployInstance)
		insts.POST("/:id/stop", h.StopInstance)
		insts.POST("/:id/clean", h.CleanInstance)
		insts.POST("/:id/rollback", h.RollbackInstance)
		insts.POST("/:id/extend", h.ExtendTTL)
		insts.GET("/:id/deploy-preview", h.DeployPreview)
		insts.GET("/:id/deploy-log", h.GetDeployLog)
		insts.POST("/:id/actions/:name", h.InvokeAction)
		insts.GET("/:id/overrides", h.GetOverrides)
		insts.PUT("/:id/overrides/:chartId", h.SetOverride)
		insts.GET("/:id/branches", boHandler.ListBranchOverrides)
		insts.PUT("/:id/branches/:chartId", boHandler.SetBranchOverride)
		insts.DELETE("/:id/branches/:chartId", boHandler.DeleteBranchOverride)
		insts.GET("/:id/quota-overrides", quotaHandler.GetQuotaOverride)
		insts.PUT("/:id/quota-overrides", quotaHandler.SetQuotaOverride)
		insts.DELETE("/:id/quota-overrides", quotaHandler.DeleteQuotaOverride)
	}
	f.router = r
	return f
}

// assertNoSideEffects checks that a rejected request changed nothing.
func (f *authzFixture) assertNoSideEffects(t *testing.T, before *models.StackInstance) {
	t.Helper()

	after, err := f.instRepo.FindByID(authzInstanceID)
	require.NoError(t, err, "instance must not be deleted")
	assert.Equal(t, before, after, "instance must not change (status, TTL, fields)")

	f.logRepo.mu.RLock()
	logCount := len(f.logRepo.items)
	f.logRepo.mu.RUnlock()
	assert.Zero(t, logCount, "no deploy-log row")
	assert.Zero(t, f.helmExec.calls.Load(), "no Helm call")
	assert.Empty(t, f.hookRec.names(), "no hook fired")
	assert.Zero(t, f.actionCalls.Load(), "no action invoked")

	vo, err := f.overrideRepo.ListByInstance(authzInstanceID)
	require.NoError(t, err)
	require.Len(t, vo, 1, "value overrides unchanged")
	assert.Equal(t, "replicas: 1", vo[0].Values)

	bo, err := f.boRepo.List(authzInstanceID)
	require.NoError(t, err)
	require.Len(t, bo, 1, "branch overrides unchanged")
	assert.Equal(t, "feature/a", bo[0].Branch)

	qo, err := f.quotaRepo.GetByInstanceID(context.Background(), authzInstanceID)
	require.NoError(t, err, "quota override must not be deleted")
	assert.Equal(t, "100m", qo.CPURequest)
}

// TestInstanceAuthorization_ProtectedEndpoints covers every endpoint that
// follows canModifyInstance: the owner, an admin and a devops user are
// allowed; another user gets 403 and nothing changes.
func TestInstanceAuthorization_ProtectedEndpoints(t *testing.T) {
	t.Parallel()

	base := "/api/v1/stack-instances/" + authzInstanceID
	endpoints := []struct {
		name        string
		method      string
		path        string
		body        string
		instStatus  string
		wantAllowed int
	}{
		{"deploy", http.MethodPost, base + "/deploy", "", models.StackStatusDraft, http.StatusAccepted},
		{"stop", http.MethodPost, base + "/stop", "", models.StackStatusRunning, http.StatusAccepted},
		{"clean", http.MethodPost, base + "/clean", "", models.StackStatusRunning, http.StatusAccepted},
		{"delete", http.MethodDelete, base, "", models.StackStatusDraft, http.StatusNoContent},
		{"rollback", http.MethodPost, base + "/rollback", "", models.StackStatusRunning, http.StatusAccepted},
		{"invoke action", http.MethodPost, base + "/actions/refresh-db", `{"parameters":{"k":"v"}}`, models.StackStatusRunning, http.StatusOK},
		{"update", http.MethodPut, base, `{"branch":"feature/b","ttl_minutes":30}`, models.StackStatusRunning, http.StatusOK},
		{"extend TTL", http.MethodPost, base + "/extend", `{"ttl_minutes":120}`, models.StackStatusRunning, http.StatusOK},
		{"deploy preview", http.MethodGet, base + "/deploy-preview", "", models.StackStatusRunning, http.StatusOK},
		{"get value overrides", http.MethodGet, base + "/overrides", "", models.StackStatusRunning, http.StatusOK},
		{"set value override", http.MethodPut, base + "/overrides/" + authzChartID, `{"values":"replicas: 3"}`, models.StackStatusRunning, http.StatusOK},
		{"list branch overrides", http.MethodGet, base + "/branches", "", models.StackStatusRunning, http.StatusOK},
		{"set branch override", http.MethodPut, base + "/branches/" + authzChartID, `{"branch":"feature/b"}`, models.StackStatusRunning, http.StatusOK},
		{"delete branch override", http.MethodDelete, base + "/branches/" + authzChartID, "", models.StackStatusRunning, http.StatusNoContent},
		{"get quota override", http.MethodGet, base + "/quota-overrides", "", models.StackStatusRunning, http.StatusOK},
		{"set quota override", http.MethodPut, base + "/quota-overrides", `{"cpu_request":"200m"}`, models.StackStatusRunning, http.StatusOK},
		{"delete quota override", http.MethodDelete, base + "/quota-overrides", "", models.StackStatusRunning, http.StatusNoContent},
	}
	callers := []struct {
		name    string
		userID  string
		role    string
		allowed bool
	}{
		{"owner", authzOwnerID, "user", true},
		{"admin", "uid-admin", "admin", true},
		{"devops", "uid-devops", "devops", true},
		{"other user", "uid-other", "user", false},
	}

	type testCase struct {
		name        string
		method      string
		path        string
		body        string
		instStatus  string
		callerID    string
		callerRole  string
		allowed     bool
		wantAllowed int
	}
	var tests []testCase
	for _, ep := range endpoints {
		for _, cl := range callers {
			tests = append(tests, testCase{
				name:   ep.name + "/" + cl.name,
				method: ep.method, path: ep.path, body: ep.body, instStatus: ep.instStatus,
				callerID: cl.userID, callerRole: cl.role, allowed: cl.allowed,
				wantAllowed: ep.wantAllowed,
			})
		}
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newAuthzFixture(t, tt.instStatus, tt.callerID, tt.callerRole)
			before, err := f.instRepo.FindByID(authzInstanceID)
			require.NoError(t, err)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(tt.method, tt.path, bytes.NewBufferString(tt.body))
			if tt.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			f.router.ServeHTTP(w, req)

			if tt.allowed {
				assert.Equal(t, tt.wantAllowed, w.Code, w.Body.String())
				return
			}

			require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
			var resp map[string]string
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, msgInstanceModifyForbidden, resp["error"])
			f.assertNoSideEffects(t, before)
		})
	}
}

// TestInstanceAuthorization_ReadEndpointsOpen checks that any authenticated
// user can still read another user's instance: details, deploy log, exported
// values, compare, clone (creates a copy owned by the caller), status and pods.
// The fixture has no cluster client, so status and pods return the no-cluster
// 503 after the instance lookup (a 200 needs a k8s client).
func TestInstanceAuthorization_ReadEndpointsOpen(t *testing.T) {
	t.Parallel()

	base := "/api/v1/stack-instances/" + authzInstanceID
	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{"get instance", http.MethodGet, base, http.StatusOK},
		{"get deploy log", http.MethodGet, base + "/deploy-log", http.StatusOK},
		{"export all values", http.MethodGet, base + "/values", http.StatusOK},
		{"export chart values", http.MethodGet, base + "/values/" + authzChartID, http.StatusOK},
		{"compare", http.MethodGet, "/api/v1/stack-instances/compare?left=" + authzInstanceID + "&right=inst-authz-2", http.StatusOK},
		{"clone", http.MethodPost, base + "/clone", http.StatusCreated},
		// No cluster client in the fixture: 503 "K8s monitoring not configured",
		// reached after the lookup, so no ownership rule blocked the read.
		{"status", http.MethodGet, base + "/status", http.StatusServiceUnavailable},
		{"pods", http.MethodGet, base + "/pods", http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newAuthzFixture(t, models.StackStatusRunning, "uid-other", "user")
			seedInstance(t, f.instRepo, "inst-authz-2", "my-stack-2", "d1", authzOwnerID, models.StackStatusRunning)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(tt.method, tt.path, nil)
			f.router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code, w.Body.String())
		})
	}
}

// TestCanModifyInstance unit-tests the authorization rule.
func TestCanModifyInstance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		userID  string
		role    string
		ownerID string
		nilInst bool
		want    bool
	}{
		{name: "owner", userID: "u1", role: "user", ownerID: "u1", want: true},
		{name: "admin", userID: "u2", role: "admin", ownerID: "u1", want: true},
		{name: "devops", userID: "u2", role: "devops", ownerID: "u1", want: true},
		{name: "other user", userID: "u2", role: "user", ownerID: "u1", want: false},
		{name: "unknown role", userID: "u2", role: "guest", ownerID: "u1", want: false},
		{name: "empty caller and empty owner", userID: "", role: "", ownerID: "", want: false},
		{name: "nil instance", userID: "u1", role: "admin", nilInst: true, want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			if tt.userID != "" {
				c.Set("userID", tt.userID)
			}
			if tt.role != "" {
				c.Set("role", tt.role)
			}
			var inst *models.StackInstance
			if !tt.nilInst {
				inst = &models.StackInstance{ID: "i1", OwnerID: tt.ownerID, CreatedAt: time.Now()}
			}
			assert.Equal(t, tt.want, canModifyInstance(c, inst))
		})
	}
}

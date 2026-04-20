package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"backend/internal/database"
	"backend/internal/helm"
	"backend/internal/hooks"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordedHook captures one event delivery for assertions.
type recordedHook struct {
	event    string
	envelope hooks.EventEnvelope
}

type handlerHookRecorder struct {
	mu     sync.Mutex
	events []recordedHook
	deny   map[string]string // event -> reject message; if set, returns Allowed:false
	server *httptest.Server
}

func newHandlerHookRecorder(t *testing.T) *handlerHookRecorder {
	t.Helper()
	r := &handlerHookRecorder{deny: map[string]string{}}
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var env hooks.EventEnvelope
		if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		r.mu.Lock()
		r.events = append(r.events, recordedHook{event: env.Event, envelope: env})
		denyMsg, deny := r.deny[env.Event]
		r.mu.Unlock()
		if deny {
			_ = json.NewEncoder(w).Encode(hooks.HookResponse{Allowed: false, Message: denyMsg})
			return
		}
		_ = json.NewEncoder(w).Encode(hooks.HookResponse{Allowed: true})
	}))
	t.Cleanup(r.server.Close)
	return r
}

func (r *handlerHookRecorder) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.events))
	for _, e := range r.events {
		out = append(out, e.event)
	}
	return out
}

func (r *handlerHookRecorder) dispatcher(t *testing.T, fp hooks.FailurePolicy) *hooks.Dispatcher {
	t.Helper()
	d, err := hooks.NewDispatcher(hooks.Config{Subscriptions: []hooks.Subscription{{
		Name: "recorder",
		Events: []string{
			hooks.EventPreInstanceCreate,
			hooks.EventPostInstanceCreate,
			hooks.EventPreInstanceDelete,
			hooks.EventPostInstanceDelete,
		},
		URL:           r.server.URL,
		FailurePolicy: fp,
	}}}, r.server.Client())
	require.NoError(t, err)
	return d
}

// setupInstanceRouterWithHooks mirrors setupInstanceRouter but attaches
// a hooks.Dispatcher (and optionally an ActionRegistry) to the InstanceHandler.
func setupInstanceRouterWithHooks(
	instanceRepo *MockStackInstanceRepository,
	defRepo *MockStackDefinitionRepository,
	dispatcher *hooks.Dispatcher,
	callerID, callerUsername string,
) *gin.Engine {
	return setupInstanceRouterWithHooksAndActions(instanceRepo, defRepo, dispatcher, nil, callerID, callerUsername)
}

func setupInstanceRouterWithHooksAndActions(
	instanceRepo *MockStackInstanceRepository,
	defRepo *MockStackDefinitionRepository,
	dispatcher *hooks.Dispatcher,
	registry *hooks.ActionRegistry,
	callerID, callerUsername string,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if callerID != "" {
			c.Set("userID", callerID)
		}
		if callerUsername != "" {
			c.Set("username", callerUsername)
		}
		c.Set("role", "user")
		c.Next()
	})

	overrideRepo := NewMockValueOverrideRepository()
	boRepo := NewMockChartBranchOverrideRepository()
	h := NewInstanceHandler(instanceRepo, overrideRepo, boRepo, defRepo,
		NewMockChartConfigRepository(), NewMockStackTemplateRepository(),
		NewMockTemplateChartConfigRepository(),
		helm.NewValuesGenerator(), NewMockUserRepository(), 0)
	h.txRunner = &mockHandlerTxRunner{repos: database.TxRepos{
		StackInstance:  instanceRepo,
		ValueOverride:  overrideRepo,
		BranchOverride: boRepo,
	}}
	h.WithHooks(dispatcher)
	h.WithActions(registry)

	insts := r.Group("/api/v1/stack-instances")
	{
		insts.POST("", h.CreateInstance)
		insts.DELETE("/:id", h.DeleteInstance)
		insts.POST("/:id/actions/:name", h.InvokeAction)
	}
	return r
}

func TestCreateInstance_FiresPreAndPostHooks(t *testing.T) {
	t.Parallel()
	rec := newHandlerHookRecorder(t)

	instRepo := NewMockStackInstanceRepository()
	defRepo := NewMockStackDefinitionRepository()
	seedDefinition(t, defRepo, "d1", "Demo Def", "uid-1")

	router := setupInstanceRouterWithHooks(instRepo, defRepo,
		rec.dispatcher(t, hooks.FailurePolicyIgnore), "uid-1", "alice")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances",
		bytes.NewBufferString(`{"stack_definition_id":"d1","name":"hooked"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	assert.Equal(t,
		[]string{hooks.EventPreInstanceCreate, hooks.EventPostInstanceCreate},
		rec.names())
}

func TestCreateInstance_PreHookDenialReturns403(t *testing.T) {
	t.Parallel()
	rec := newHandlerHookRecorder(t)
	rec.deny[hooks.EventPreInstanceCreate] = "policy says no"

	instRepo := NewMockStackInstanceRepository()
	defRepo := NewMockStackDefinitionRepository()
	seedDefinition(t, defRepo, "d1", "Demo Def", "uid-1")

	router := setupInstanceRouterWithHooks(instRepo, defRepo,
		rec.dispatcher(t, hooks.FailurePolicyFail), "uid-1", "alice")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances",
		bytes.NewBufferString(`{"stack_definition_id":"d1","name":"hooked"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "pre-instance-create hook rejected the request")
	// Only pre-create was attempted; the create never reached the post hook.
	assert.Equal(t, []string{hooks.EventPreInstanceCreate}, rec.names())
	// And nothing was persisted.
	all, err := instRepo.List()
	require.NoError(t, err)
	assert.Empty(t, all)
}

func TestDeleteInstance_FiresPreAndPostHooks(t *testing.T) {
	t.Parallel()
	rec := newHandlerHookRecorder(t)

	instRepo := NewMockStackInstanceRepository()
	defRepo := NewMockStackDefinitionRepository()
	seedInstance(t, instRepo, "i-1", "to-delete", "d1", "uid-1", "running")

	router := setupInstanceRouterWithHooks(instRepo, defRepo,
		rec.dispatcher(t, hooks.FailurePolicyIgnore), "uid-1", "alice")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodDelete, "/api/v1/stack-instances/i-1", nil)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	assert.Equal(t,
		[]string{hooks.EventPreInstanceDelete, hooks.EventPostInstanceDelete},
		rec.names())
}

func TestInvokeAction_RoutesToSubscriberAndForwardsResult(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body hooks.ActionRequest
		require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
		assert.Equal(t, "refresh-db", body.Action)
		require.NotNil(t, body.Instance)
		assert.Equal(t, "i-1", body.Instance.ID)
		assert.Equal(t, "alpine", body.Parameters["image"])
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "wiped": []string{"mysql-data"}})
	}))
	defer srv.Close()

	registry, err := hooks.NewActionRegistry([]hooks.ActionSubscription{{
		Name: "refresh-db", URL: srv.URL, TimeoutSeconds: 5,
	}}, srv.Client())
	require.NoError(t, err)

	instRepo := NewMockStackInstanceRepository()
	defRepo := NewMockStackDefinitionRepository()
	seedInstance(t, instRepo, "i-1", "demo", "d1", "uid-1", "running")

	router := setupInstanceRouterWithHooksAndActions(instRepo, defRepo, nil, registry, "uid-1", "alice")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost,
		"/api/v1/stack-instances/i-1/actions/refresh-db",
		bytes.NewBufferString(`{"parameters":{"image":"alpine"}}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Action     string         `json:"action"`
		InstanceID string         `json:"instance_id"`
		StatusCode int            `json:"status_code"`
		Result     map[string]any `json:"result"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "refresh-db", resp.Action)
	assert.Equal(t, "i-1", resp.InstanceID)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, true, resp.Result["ok"])
}

func TestInvokeAction_UnknownActionReturns404(t *testing.T) {
	t.Parallel()

	registry, err := hooks.NewActionRegistry(nil, nil)
	require.NoError(t, err)

	instRepo := NewMockStackInstanceRepository()
	seedInstance(t, instRepo, "i-1", "demo", "d1", "uid-1", "running")

	router := setupInstanceRouterWithHooksAndActions(instRepo,
		NewMockStackDefinitionRepository(), nil, registry, "uid-1", "alice")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost,
		"/api/v1/stack-instances/i-1/actions/missing", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "missing")
}

func TestInvokeAction_NoRegistryReturns503(t *testing.T) {
	t.Parallel()

	instRepo := NewMockStackInstanceRepository()
	seedInstance(t, instRepo, "i-1", "demo", "d1", "uid-1", "running")
	router := setupInstanceRouterWithHooksAndActions(instRepo,
		NewMockStackDefinitionRepository(), nil, nil, "uid-1", "alice")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost,
		"/api/v1/stack-instances/i-1/actions/x", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestInvokeAction_UnknownInstanceReturns404(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("subscriber should not be called when instance is unknown")
	}))
	defer srv.Close()

	registry, err := hooks.NewActionRegistry([]hooks.ActionSubscription{{
		Name: "x", URL: srv.URL, TimeoutSeconds: 5,
	}}, srv.Client())
	require.NoError(t, err)

	router := setupInstanceRouterWithHooksAndActions(NewMockStackInstanceRepository(),
		NewMockStackDefinitionRepository(), nil, registry, "uid-1", "alice")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost,
		"/api/v1/stack-instances/missing/actions/x", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestDeleteInstance_PreHookDenialReturns403(t *testing.T) {
	t.Parallel()
	rec := newHandlerHookRecorder(t)
	rec.deny[hooks.EventPreInstanceDelete] = "still in use"

	instRepo := NewMockStackInstanceRepository()
	defRepo := NewMockStackDefinitionRepository()
	seedInstance(t, instRepo, "i-1", "keep-me", "d1", "uid-1", "running")

	router := setupInstanceRouterWithHooks(instRepo, defRepo,
		rec.dispatcher(t, hooks.FailurePolicyFail), "uid-1", "alice")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodDelete, "/api/v1/stack-instances/i-1", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "pre-instance-delete hook rejected the request")
	// Instance must still exist.
	stored, err := instRepo.FindByID("i-1")
	require.NoError(t, err)
	assert.Equal(t, "i-1", stored.ID)
	// Post-delete must not have fired.
	assert.Equal(t, []string{hooks.EventPreInstanceDelete}, rec.names())
}

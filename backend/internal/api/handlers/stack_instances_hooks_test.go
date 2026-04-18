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
// a hooks.Dispatcher to the InstanceHandler.
func setupInstanceRouterWithHooks(
	instanceRepo *MockStackInstanceRepository,
	defRepo *MockStackDefinitionRepository,
	dispatcher *hooks.Dispatcher,
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

	insts := r.Group("/api/v1/stack-instances")
	{
		insts.POST("", h.CreateInstance)
		insts.DELETE("/:id", h.DeleteInstance)
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
	assert.Contains(t, w.Body.String(), "policy says no")
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
	assert.Contains(t, w.Body.String(), "still in use")
	// Instance must still exist.
	stored, err := instRepo.FindByID("i-1")
	require.NoError(t, err)
	assert.Equal(t, "i-1", stored.ID)
	// Post-delete must not have fired.
	assert.Equal(t, []string{hooks.EventPreInstanceDelete}, rec.names())
}

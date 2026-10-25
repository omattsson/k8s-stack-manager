package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"backend/internal/ctxutil"
	"backend/internal/database"
	"backend/internal/deployer"
	"backend/internal/helm"
	"backend/internal/hooks"
	"backend/internal/models"
	"backend/internal/notifier"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureDefaultLog replaces the default slog logger with a debug-level
// text logger for the test and returns its buffer.
func captureDefaultLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// valuesCancelRouter returns a router with the routes that build Helm values
// (export, deploy preview, compare, deploy, bulk deploy) over env. The
// handler reads the instances through inst (a hook over env.inst). mgr may
// be nil.
func valuesCancelRouter(t *testing.T, env *lifecycleEnv, inst *hookInstanceRepo, mgr *deployer.Manager) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userID", "uid-1")
		c.Set("username", "alice")
		c.Set("role", "user")
		c.Next()
	})
	users := NewMockUserRepository()
	seedUser(t, users, "uid-1", "alice", "secret-pass", "user")
	h, err := NewInstanceHandlerWithDeployer(
		inst, env.ov, env.bo, env.def, env.cc,
		NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
		helm.NewValuesGenerator(), users,
		mgr, nil, nil, env.logs, nil, 0,
		&mockHandlerTxRunner{repos: database.TxRepos{
			StackDefinition: env.def,
			ChartConfig:     env.cc,
			StackInstance:   env.inst,
			ValueOverride:   env.ov,
			BranchOverride:  env.bo,
			DeploymentLog:   env.logs,
		}},
	)
	require.NoError(t, err)
	g := r.Group("/api/v1/stack-instances")
	g.GET("/compare", h.CompareInstances)
	g.GET("/:id/values/:chartId", h.ExportChartValues)
	g.GET("/:id/values", h.ExportAllValues)
	g.GET("/:id/deploy-preview", h.DeployPreview)
	g.POST("/:id/deploy", h.DeployInstance)
	g.POST("/bulk/deploy", h.BulkDeploy)
	return r
}

// seedValuesCancelEnv seeds two draft instances of uid-1 with a TTL and no
// expiry yet, on one definition with one chart.
func seedValuesCancelEnv(t *testing.T, env *lifecycleEnv) {
	t.Helper()
	seedDefinition(t, env.def, "d1", "def-1", "uid-1")
	require.NoError(t, env.cc.Create(context.Background(), &models.ChartConfig{ID: "cc1", StackDefinitionID: "d1", ChartName: "web", DefaultValues: "owner: \"{{.Owner}}\"\n"}))
	for _, id := range []string{"i1", "i2"} {
		require.NoError(t, env.inst.Create(context.Background(), &models.StackInstance{
			ID: id, StackDefinitionID: "d1", Name: "zz-" + id, Namespace: "stack-zz-" + id + "-alice",
			OwnerID: "uid-1", Branch: "master", Status: models.StackStatusDraft, TTLMinutes: 60,
		}))
	}
}

// valuesCancelRequest returns a request to path over inst. When cancelled,
// the request is cancelled after instance read number reads, so the values
// build sees the cancel.
func valuesCancelRequest(t *testing.T, inst *hookInstanceRepo, method, path, body string, cancelled bool, reads int32) *http.Request {
	t.Helper()
	req, cancel := liveRequest(t, method, path, body)
	if cancelled {
		inst.setHook(cancelAt("after:FindByID", reads, cancel))
	}
	return req
}

// TestInstanceValuesReads_CancelledRequest checks that the read-only routes
// that build Helm values answer 499 without an error line when the client
// left during the build (#656).
// Not parallel: it replaces the default slog logger.
func TestInstanceValuesReads_CancelledRequest(t *testing.T) {
	logBuf := captureDefaultLog(t)

	tests := []struct {
		name  string
		path  string
		reads int32 // instance reads before the values build
	}{
		{name: "export chart values", path: "/api/v1/stack-instances/i1/values/cc1", reads: 1},
		{name: "export all values", path: "/api/v1/stack-instances/i1/values", reads: 1},
		{name: "deploy preview", path: "/api/v1/stack-instances/i1/deploy-preview", reads: 1},
		{name: "compare", path: "/api/v1/stack-instances/compare?left=i1&right=i2", reads: 2},
	}

	for _, tt := range tests {
		for _, cancelled := range []bool{false, true} {
			name := tt.name
			if cancelled {
				name += ", cancelled"
			}
			t.Run(name, func(t *testing.T) {
				env := newLifecycleEnv()
				seedValuesCancelEnv(t, env)
				logBuf.Reset()

				inst := &hookInstanceRepo{MockStackInstanceRepository: env.inst}
				w := httptest.NewRecorder()
				req := valuesCancelRequest(t, inst, http.MethodGet, tt.path, "", cancelled, tt.reads)
				valuesCancelRouter(t, env, inst, nil).ServeHTTP(w, req)
				assert.Equal(t, cancelled, req.Context().Err() != nil)

				if cancelled {
					assert.Equal(t, statusClientClosedRequest, w.Code, w.Body.String())
					assert.JSONEq(t, `{"error":"Client closed request"}`, w.Body.String())
				} else {
					assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
				}
				assert.NotContains(t, logBuf.String(), "level=ERROR")
			})
		}
	}
}

// TestDeployInstance_CancelledRequest checks that a deploy whose client left
// during the values build changes nothing: the build runs before the first
// write (the TTL expiry), so the instance keeps its expiry and status and no
// deploy log starts (#656). A live request still deploys.
// Not parallel: it replaces the default slog logger.
func TestDeployInstance_CancelledRequest(t *testing.T) {
	logBuf := captureDefaultLog(t)

	tests := []struct {
		name       string
		path       string
		body       string
		bulk       bool
		cancelled  bool
		wantStatus int
	}{
		{name: "deploy", path: "/api/v1/stack-instances/i1/deploy", wantStatus: http.StatusAccepted},
		{name: "deploy, cancelled", path: "/api/v1/stack-instances/i1/deploy", cancelled: true, wantStatus: statusClientClosedRequest},
		{name: "bulk deploy", path: "/api/v1/stack-instances/bulk/deploy", body: `{"instance_ids":["i1"]}`, bulk: true, wantStatus: http.StatusOK},
		{name: "bulk deploy, cancelled", path: "/api/v1/stack-instances/bulk/deploy", body: `{"instance_ids":["i1"]}`, bulk: true, cancelled: true, wantStatus: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newLifecycleEnv()
			seedValuesCancelEnv(t, env)
			mgr := newTestManager(env.inst, env.logs)
			t.Cleanup(mgr.Shutdown)
			logBuf.Reset()

			inst := &hookInstanceRepo{MockStackInstanceRepository: env.inst}
			w := httptest.NewRecorder()
			req := valuesCancelRequest(t, inst, http.MethodPost, tt.path, tt.body, tt.cancelled, 1)
			valuesCancelRouter(t, env, inst, mgr).ServeHTTP(w, req)
			assert.Equal(t, tt.cancelled, req.Context().Err() != nil)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.bulk {
				var resp BulkOperationResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				require.Len(t, resp.Results, 1)
				if tt.cancelled {
					assert.Equal(t, "error", resp.Results[0].Status)
					assert.Equal(t, "request cancelled", resp.Results[0].Error)
				} else {
					assert.Equal(t, "success", resp.Results[0].Status, resp.Results[0].Error)
				}
			}
			mgr.Shutdown()

			got, err := env.inst.FindByID(context.Background(), "i1")
			require.NoError(t, err)
			logs, err := env.logs.ListByInstance(context.Background(), "i1")
			require.NoError(t, err)
			if tt.cancelled {
				assert.Nil(t, got.ExpiresAt, "the TTL expiry is not written")
				assert.Equal(t, models.StackStatusDraft, got.Status)
				assert.Empty(t, logs, "no deploy starts")
				assert.NotContains(t, logBuf.String(), "level=ERROR")
			} else {
				assert.NotNil(t, got.ExpiresAt, "the deploy starts the TTL clock")
				assert.Len(t, logs, 1, "the deploy starts")
			}
		})
	}
}

// ctxNotifier records the context error, the time left until the deadline
// (-1 without one) and the trigger of each NotifyInstance call.
type ctxNotifier struct {
	mu       sync.Mutex
	types    []string
	ctxErrs  []error
	left     []time.Duration
	triggers []*hooks.Trigger
}

func (n *ctxNotifier) NotifyInstance(ctx context.Context, _ models.NotificationTarget, notifType, _, _ string, opts ...notifier.InstanceOption) error {
	left := time.Duration(-1)
	if d, ok := ctx.Deadline(); ok {
		left = time.Until(d)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.types = append(n.types, notifType)
	n.ctxErrs = append(n.ctxErrs, ctx.Err())
	n.left = append(n.left, left)
	n.triggers = append(n.triggers, notifier.ResolveInstanceOptions(opts...).Trigger)
	return nil
}

func (n *ctxNotifier) FollowerIDs(context.Context, string) []string { return nil }

// TestDeleteInstance_CancelledRequestAfterDelete checks that a client that
// left at the row delete does not cancel the delete, post-instance-delete,
// delete-completed and the instance.deleted notification, and that they keep
// the user trigger (#656).
func TestDeleteInstance_CancelledRequestAfterDelete(t *testing.T) {
	t.Parallel()

	wantEvents := []string{hooks.EventPreInstanceDelete, hooks.EventPostInstanceDelete, hooks.EventDeleteCompleted}
	tests := []struct {
		name      string
		cancelled bool
	}{
		{name: "live request"},
		{name: "request cancelled at the row delete", cancelled: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := newHandlerHookRecorder(t)
			d, err := hooks.NewDispatcher(hooks.Config{Subscriptions: []hooks.Subscription{{
				Name:          "recorder",
				Events:        []string{hooks.EventPreInstanceDelete, hooks.EventPostInstanceDelete, hooks.EventDeleteCompleted},
				URL:           rec.server.URL,
				FailurePolicy: hooks.FailurePolicyIgnore,
			}}}, rec.server.Client())
			require.NoError(t, err)

			instRepo := newHookInstanceRepo()
			seedInstance(t, instRepo.MockStackInstanceRepository, "i-1", "to-delete", "d1", "uid-1", models.StackStatusDraft)
			overrideRepo := NewMockValueOverrideRepository()
			boRepo := NewMockChartBranchOverrideRepository()
			h := NewInstanceHandler(instRepo, overrideRepo, boRepo, NewMockStackDefinitionRepository(),
				NewMockChartConfigRepository(), NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
				helm.NewValuesGenerator(), NewMockUserRepository(), 0)
			h.txRunner = &mockHandlerTxRunner{repos: database.TxRepos{
				StackInstance: instRepo, ValueOverride: overrideRepo, BranchOverride: boRepo,
			}}
			h.WithHooks(d)
			notif := &ctxNotifier{}
			h.WithNotifier(notif)
			gin.SetMode(gin.TestMode)
			r := gin.New()
			r.Use(injectAuthContext("uid-1", "user"), func(c *gin.Context) {
				c.Set("username", "alice")
				c.Next()
			})
			r.DELETE("/api/v1/stack-instances/:id", h.DeleteInstance)

			req, cancel := liveRequest(t, http.MethodDelete, "/api/v1/stack-instances/i-1", "")
			if tt.cancelled {
				instRepo.setHook(cancelAt("before:Delete", 1, cancel))
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
			assert.Equal(t, tt.cancelled, req.Context().Err() != nil)
			_, findErr := instRepo.FindByID(context.Background(), "i-1")
			require.Error(t, findErr, "the instance is deleted")
			want := hooks.Trigger{Type: hooks.TriggerUser, ID: "uid-1", Name: "alice"}
			assert.Equal(t, wantEvents, rec.names())
			rec.mu.Lock()
			for _, e := range rec.events {
				if assert.NotNil(t, e.envelope.Trigger, e.event) {
					assert.Equal(t, want, *e.envelope.Trigger, e.event)
				}
			}
			rec.mu.Unlock()

			notif.mu.Lock()
			defer notif.mu.Unlock()
			require.Equal(t, []string{"instance.deleted"}, notif.types)
			assert.NoError(t, notif.ctxErrs[0], "the notification gets a context without the cancel")
			require.NotNil(t, notif.triggers[0])
			assert.Equal(t, want, *notif.triggers[0])
		})
	}
}

// TestDeleteInstance_NotifyHasItsOwnBudget checks that instance.deleted does
// not inherit the write deadline of the delete: the post-delete hooks can
// outlast it, and the notification must still be sent (#674). The row delete
// takes 300 ms; with the delete deadline, less than WriteTimeout - 300 ms
// would be left at the notification.
func TestDeleteInstance_NotifyHasItsOwnBudget(t *testing.T) {
	t.Parallel()

	const slowDelete = 300 * time.Millisecond
	instRepo := newHookInstanceRepo()
	seedInstance(t, instRepo.MockStackInstanceRepository, "i-1", "to-delete", "d1", "uid-1", models.StackStatusDraft)
	overrideRepo := NewMockValueOverrideRepository()
	boRepo := NewMockChartBranchOverrideRepository()
	h := NewInstanceHandler(instRepo, overrideRepo, boRepo, NewMockStackDefinitionRepository(),
		NewMockChartConfigRepository(), NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
		helm.NewValuesGenerator(), NewMockUserRepository(), 0)
	h.txRunner = &mockHandlerTxRunner{repos: database.TxRepos{
		StackInstance: instRepo, ValueOverride: overrideRepo, BranchOverride: boRepo,
	}}
	notif := &ctxNotifier{}
	h.WithNotifier(notif)
	instRepo.setHook(func(point string, _ context.Context) {
		if point == "before:Delete" {
			time.Sleep(slowDelete)
		}
	})
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(injectAuthContext("uid-1", "user"))
	r.DELETE("/api/v1/stack-instances/:id", h.DeleteInstance)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/v1/stack-instances/i-1", nil))

	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	notif.mu.Lock()
	defer notif.mu.Unlock()
	require.Equal(t, []string{"instance.deleted"}, notif.types)
	assert.NoError(t, notif.ctxErrs[0])
	assert.Greater(t, notif.left[0], ctxutil.WriteTimeout-slowDelete/2,
		"the notification starts a fresh WriteTimeout budget after the delete")
}

// TestPostHookCtx checks the budget of a post-commit hook: WriteTimeout plus
// the subscriber timeouts of the event, without the cancel and the deadline
// of the handler context (#674).
func TestPostHookCtx(t *testing.T) {
	t.Parallel()

	d, err := hooks.NewDispatcher(hooks.Config{Subscriptions: []hooks.Subscription{
		{Name: "cmdb", Events: []string{hooks.EventPostInstanceCreate}, URL: "http://127.0.0.1:1/cmdb", TimeoutSeconds: 120},
		{Name: "audit", Events: []string{hooks.EventPostInstanceCreate, hooks.EventPostInstanceDelete}, URL: "http://127.0.0.1:1/audit", TimeoutSeconds: 60},
	}}, nil)
	require.NoError(t, err)
	h := &InstanceHandler{}
	h.WithHooks(d)

	tests := []struct {
		name   string
		event  string
		budget time.Duration
	}{
		{name: "two subscribers", event: hooks.EventPostInstanceCreate, budget: ctxutil.WriteTimeout + 180*time.Second},
		{name: "one subscriber", event: hooks.EventPostInstanceDelete, budget: ctxutil.WriteTimeout + 60*time.Second},
		{name: "no subscriber", event: hooks.EventDeleteCompleted, budget: ctxutil.WriteTimeout},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// The handler context: a write deadline that has nearly passed,
			// and a client that leaves.
			parent, cancelParent := context.WithTimeout(context.Background(), time.Millisecond)
			cancelParent()

			start := time.Now()
			ctx, cancel := h.postHookCtx(parent, tt.event)
			defer cancel()

			assert.NoError(t, ctx.Err(), "the end of the handler context does not end the hook")
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			assert.WithinDuration(t, start.Add(tt.budget), deadline, time.Second)
		})
	}
}

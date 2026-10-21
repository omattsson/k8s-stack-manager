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
// (export, deploy preview, compare, deploy, bulk deploy) over env. mgr may be
// nil.
func valuesCancelRouter(t *testing.T, env *lifecycleEnv, mgr *deployer.Manager) *gin.Engine {
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
		env.inst, env.ov, env.bo, env.def, env.cc,
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
	require.NoError(t, env.cc.Create(&models.ChartConfig{ID: "cc1", StackDefinitionID: "d1", ChartName: "web", DefaultValues: "owner: \"{{.Owner}}\"\n"}))
	for _, id := range []string{"i1", "i2"} {
		require.NoError(t, env.inst.Create(&models.StackInstance{
			ID: id, StackDefinitionID: "d1", Name: "zz-" + id, Namespace: "stack-zz-" + id + "-alice",
			OwnerID: "uid-1", Branch: "master", Status: models.StackStatusDraft, TTLMinutes: 60,
		}))
	}
}

func cancelRequest(t *testing.T, method, path, body string, cancelled bool) *http.Request {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if cancelled {
		cancel()
	}
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
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
		name string
		path string
	}{
		{name: "export chart values", path: "/api/v1/stack-instances/i1/values/cc1"},
		{name: "export all values", path: "/api/v1/stack-instances/i1/values"},
		{name: "deploy preview", path: "/api/v1/stack-instances/i1/deploy-preview"},
		{name: "compare", path: "/api/v1/stack-instances/compare?left=i1&right=i2"},
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

				w := httptest.NewRecorder()
				valuesCancelRouter(t, env, nil).ServeHTTP(w, cancelRequest(t, http.MethodGet, tt.path, "", cancelled))

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

			w := httptest.NewRecorder()
			valuesCancelRouter(t, env, mgr).ServeHTTP(w, cancelRequest(t, http.MethodPost, tt.path, tt.body, tt.cancelled))

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

			got, err := env.inst.FindByID("i1")
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

// ctxNotifier records the context error and the trigger of each
// NotifyInstance call.
type ctxNotifier struct {
	mu       sync.Mutex
	types    []string
	ctxErrs  []error
	triggers []*hooks.Trigger
}

func (n *ctxNotifier) NotifyInstance(ctx context.Context, _ models.NotificationTarget, notifType, _, _ string, opts ...notifier.InstanceOption) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.types = append(n.types, notifType)
	n.ctxErrs = append(n.ctxErrs, ctx.Err())
	n.triggers = append(n.triggers, notifier.ResolveInstanceOptions(opts...).Trigger)
	return nil
}

func (n *ctxNotifier) FollowerIDs(context.Context, string) []string { return nil }

// TestDeleteInstance_CancelledRequestAfterDelete checks that a client that
// left does not cancel post-instance-delete, delete-completed and the
// instance.deleted notification after the row delete, and that they keep
// the user trigger (#656). With a cancelled request the pre-instance-delete
// call cannot reach the subscriber (failure_policy ignore).
func TestDeleteInstance_CancelledRequestAfterDelete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		cancelled  bool
		wantEvents []string
	}{
		{name: "live request",
			wantEvents: []string{hooks.EventPreInstanceDelete, hooks.EventPostInstanceDelete, hooks.EventDeleteCompleted}},
		{name: "cancelled request", cancelled: true,
			wantEvents: []string{hooks.EventPostInstanceDelete, hooks.EventDeleteCompleted}},
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

			instRepo := NewMockStackInstanceRepository()
			seedInstance(t, instRepo, "i-1", "to-delete", "d1", "uid-1", models.StackStatusDraft)
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

			w := httptest.NewRecorder()
			r.ServeHTTP(w, cancelRequest(t, http.MethodDelete, "/api/v1/stack-instances/i-1", "", tt.cancelled))

			require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
			_, findErr := instRepo.FindByID("i-1")
			require.Error(t, findErr, "the instance is deleted")
			want := hooks.Trigger{Type: hooks.TriggerUser, ID: "uid-1", Name: "alice"}
			assert.Equal(t, tt.wantEvents, rec.names())
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

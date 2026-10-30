package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/config"
	"backend/internal/k8s"
	"backend/internal/models"
	"backend/internal/podstream"
	"backend/internal/websocket"

	"github.com/gin-gonic/gin"
	gorilla "github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// fakeExecClient is a k8s.ExecClient. The test sets what the namespace check
// and the target resolution return and what the command does.
type fakeExecClient struct {
	nsErr      error
	resolveErr error
	target     *k8s.ExecTarget
	run        func(ctx context.Context, cmd []string, stdout, stderr io.Writer) (int, error)
	mu         sync.Mutex
	calls      []string
	lastReq    k8s.ExecTargetRequest
	lastNS     string
}

func (f *fakeExecClient) note(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeExecClient) called(s string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == s {
			return true
		}
	}
	return false
}

func (f *fakeExecClient) CheckStreamNamespace(_ context.Context, ns, backendNS string) error {
	f.note("check:" + ns + ":" + backendNS)
	return f.nsErr
}

func (f *fakeExecClient) ResolveExecTarget(_ context.Context, ns string, req k8s.ExecTargetRequest) (*k8s.ExecTarget, error) {
	f.note("resolve")
	f.mu.Lock()
	f.lastReq = req
	f.mu.Unlock()
	if f.resolveErr != nil {
		return nil, f.resolveErr
	}
	if f.target != nil {
		return f.target, nil
	}
	c := req.Container
	if c == "" {
		c = "web"
	}
	return &k8s.ExecTarget{Pod: "web-0", Container: c}, nil
}

func (f *fakeExecClient) ExecCommand(ctx context.Context, ns string, _ k8s.ExecTarget, cmd []string, so, se io.Writer) (int, error) {
	f.note("exec")
	f.mu.Lock()
	f.lastNS = ns
	f.mu.Unlock()
	if f.run != nil {
		return f.run(ctx, cmd, so, se)
	}
	return 0, nil
}

// failingAudit refuses every entry.
type failingAudit struct{}

func (failingAudit) Create(context.Context, *models.AuditLog) error { return errors.New("db down") }

type execFixture struct {
	router   *gin.Engine
	handler  *StreamHandler
	manager  *podstream.Manager
	pfMgr    *podstream.Manager
	instRepo *MockStackInstanceRepository
	charts   *MockChartConfigRepository
	clusters *MockClusterRepository
	exec     *fakeExecClient
	audit    *recordingAudit
	hub      *websocket.Hub
}

type execFixtureOpts struct {
	cfg      config.StreamConfig
	userID   string
	role     string
	authMeth string
	origins  string
	instance *models.StackInstance
	cluster  *models.Cluster
	audit    middleware.AuditLogger
	execCfg  podstream.Config
}

func execStreamCfg() config.StreamConfig {
	c := defaultStreamCfg()
	c.Exec = config.ExecConfig{
		Enabled: true, AllowedContainers: []string{"web", "worker"},
		MaxDuration: time.Minute, MaxCommandLength: 4096, MaxPerUser: 3,
	}
	return c
}

func newExecFixture(t *testing.T, o execFixtureOpts) *execFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	f := &execFixture{
		instRepo: NewMockStackInstanceRepository(), clusters: NewMockClusterRepository(), charts: NewMockChartConfigRepository(),
		exec: &fakeExecClient{}, audit: &recordingAudit{}, hub: websocket.NewHub(),
	}
	go f.hub.Run()
	t.Cleanup(f.hub.Shutdown)
	if o.userID == "" {
		o.userID = "owner-1"
	}
	if o.role == "" {
		o.role = "user"
	}
	if !o.cfg.Exec.Enabled && o.cfg.Exec.MaxPerUser == 0 {
		o.cfg = execStreamCfg()
	}
	if o.instance == nil {
		o.instance = &models.StackInstance{ID: "inst-1", OwnerID: "owner-1", Namespace: "stack-ns", ClusterID: "cl-1", StackDefinitionID: "def-1", Status: models.StackStatusRunning}
	}
	if o.cluster == nil {
		o.cluster = &models.Cluster{ID: "cl-1", AllowExec: true}
	}
	require.NoError(t, f.instRepo.Create(context.Background(), o.instance))
	require.NoError(t, f.clusters.Create(context.Background(), o.cluster))
	seedChartConfig(t, f.charts, "cc-1", "def-1", "web")
	seedChartConfig(t, f.charts, "cc-2", "def-other", "foreign")

	o.execCfg.Kind = podstream.KindExec
	o.execCfg.NoIdleTimeout = true
	o.execCfg.Timings = podstream.Timings{PingInterval: 50 * time.Millisecond}
	if o.execCfg.MaxDuration == 0 {
		o.execCfg.MaxDuration = time.Minute
	}
	f.manager = podstream.NewManager(o.execCfg)
	t.Cleanup(func() { _ = f.manager.Shutdown(context.Background()) })
	f.pfMgr = podstream.NewManager(podstream.Config{})
	t.Cleanup(func() { _ = f.pfMgr.Shutdown(context.Background()) })

	var audit middleware.AuditLogger = f.audit
	if o.audit != nil {
		audit = o.audit
	}
	f.handler = NewStreamHandler(StreamDeps{
		InstanceRepo: f.instRepo, ClusterRepo: f.clusters, Manager: f.pfMgr,
		ExecManager:     f.manager,
		ExecProvider:    func(string) (k8s.ExecClient, error) { return f.exec, nil },
		ChartConfigRepo: f.charts,
		Hub:             f.hub, Audit: audit, AllowedOrigins: o.origins, Config: o.cfg,
	})
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userID", o.userID)
		c.Set("username", "alice")
		c.Set("role", o.role)
		if o.authMeth != "" {
			c.Set("authMethod", o.authMeth)
		} else {
			c.Set("authMethod", "jwt")
		}
		c.Set("jti", "jti-1")
		c.Set("sessionID", "sid-1")
		c.Next()
	})
	r.GET("/api/v1/stack-instances/:id/exec", f.handler.Exec)
	f.router = r
	return f
}

func (f *execFixture) do(t *testing.T, path string, upgrade bool, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if upgrade {
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Sec-WebSocket-Version", "13")
		req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}

func (f *execFixture) server(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(f.router)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

const execPath = "/api/v1/stack-instances/inst-1/exec?chart=web"

func TestExec_PreUpgradeChecks(t *testing.T) {
	t.Parallel()
	disabled := execStreamCfg()
	disabled.Exec.Enabled = false
	withKeys := execStreamCfg()
	withKeys.AllowAPIKeys = true
	noList := execStreamCfg()
	noList.Exec.AllowedContainers = nil

	tests := []struct {
		name       string
		opts       execFixtureOpts
		path       string
		upgrade    bool
		origin     string
		setup      func(*execFixture)
		wantStatus int
		wantCode   string
	}{
		{name: "feature off", opts: execFixtureOpts{cfg: disabled}, path: execPath, wantStatus: 404, wantCode: "feature_disabled"},
		{name: "api key refused by default", opts: execFixtureOpts{authMeth: "api_key"}, path: execPath, wantStatus: 403, wantCode: "api_key_not_allowed"},
		{name: "api key allowed", opts: execFixtureOpts{authMeth: "api_key", cfg: withKeys}, path: execPath, wantStatus: 400, wantCode: "upgrade_required"},
		{name: "chart missing", path: "/api/v1/stack-instances/inst-1/exec", wantStatus: 400, wantCode: "invalid_chart"},
		{name: "chart not a name", path: "/api/v1/stack-instances/inst-1/exec?chart=Bad_Name", wantStatus: 400, wantCode: "invalid_chart"},
		{name: "chart too long", path: "/api/v1/stack-instances/inst-1/exec?chart=" + strings.Repeat("a", 54), wantStatus: 400, wantCode: "invalid_chart"},
		{name: "chart of 53 characters passes the syntax check", path: "/api/v1/stack-instances/inst-1/exec?chart=" + strings.Repeat("a", 53), wantStatus: 404, wantCode: "chart_not_found"},
		{name: "container not a label", path: execPath + "&container=Bad_Name", wantStatus: 400, wantCode: "invalid_container"},
		{name: "container empty", path: execPath + "&container=", wantStatus: 400, wantCode: "invalid_container"},
		{name: "instance not found", path: "/api/v1/stack-instances/nope/exec?chart=web", wantStatus: 404, wantCode: "instance_not_found"},
		{name: "other user", opts: execFixtureOpts{userID: "someone-else"}, path: execPath, wantStatus: 403, wantCode: "forbidden"},
		{name: "owner passes", path: execPath, wantStatus: 400, wantCode: "upgrade_required"},
		{name: "devops passes", opts: execFixtureOpts{userID: "x", role: "devops"}, path: execPath, wantStatus: 400, wantCode: "upgrade_required"},
		{name: "admin passes", opts: execFixtureOpts{userID: "x", role: "admin"}, path: execPath, wantStatus: 400, wantCode: "upgrade_required"},
		{name: "cluster switch off", opts: execFixtureOpts{cluster: &models.Cluster{ID: "cl-1", AllowPortForward: true}}, path: execPath, wantStatus: 403, wantCode: "cluster_exec_disabled"},
		{name: "cluster gone", path: execPath, setup: func(f *execFixture) { _ = f.clusters.Delete(context.Background(), "cl-1") }, wantStatus: 403, wantCode: "cluster_exec_disabled"},
		{name: "draft", opts: execFixtureOpts{instance: &models.StackInstance{ID: "inst-1", OwnerID: "owner-1", Namespace: "ns", ClusterID: "cl-1", Status: models.StackStatusDraft}}, path: execPath, wantStatus: 409, wantCode: "instance_not_deployed"},
		{name: "stopped", opts: execFixtureOpts{instance: &models.StackInstance{ID: "inst-1", OwnerID: "owner-1", Namespace: "ns", ClusterID: "cl-1", Status: models.StackStatusStopped}}, path: execPath, wantStatus: 409, wantCode: "instance_not_deployed"},
		{name: "no namespace", opts: execFixtureOpts{instance: &models.StackInstance{ID: "inst-1", OwnerID: "owner-1", ClusterID: "cl-1", Status: models.StackStatusRunning}}, path: execPath, wantStatus: 409, wantCode: "instance_not_deployed"},
		{name: "namespace shared", path: execPath, setup: func(f *execFixture) {
			require.NoError(t, f.instRepo.Create(context.Background(), &models.StackInstance{ID: "victim", OwnerID: "v", Namespace: "stack-ns", ClusterID: "cl-1", Status: models.StackStatusRunning}))
		}, wantStatus: 403, wantCode: "namespace_not_allowed"},
		{name: "namespace guard refuses", path: execPath, setup: func(f *execFixture) { f.exec.nsErr = k8s.ErrNamespaceNotAllowed }, wantStatus: 403, wantCode: "namespace_not_allowed"},
		{name: "namespace missing", path: execPath, setup: func(f *execFixture) { f.exec.nsErr = k8s.ErrNamespaceNotFound }, wantStatus: 409, wantCode: "instance_not_deployed"},
		{name: "chart of another definition", path: "/api/v1/stack-instances/inst-1/exec?chart=foreign", wantStatus: 404, wantCode: "chart_not_found"},
		{name: "chart lookup error", path: execPath, setup: func(f *execFixture) { f.charts.err = errors.New("db down") }, wantStatus: 500, wantCode: "internal"},
		{name: "container not allowed", path: execPath + "&container=db", wantStatus: 403, wantCode: "container_not_allowed"},
		{name: "empty allowed list refuses everything", opts: execFixtureOpts{cfg: noList}, path: execPath, wantStatus: 403, wantCode: "container_not_allowed"},
		{name: "empty allowed list with a container", opts: execFixtureOpts{cfg: noList}, path: execPath + "&container=web", wantStatus: 403, wantCode: "container_not_allowed"},
		{name: "allowed container passes", path: execPath + "&container=worker", wantStatus: 400, wantCode: "upgrade_required"},
		{name: "origin not allowed", opts: execFixtureOpts{origins: "same-origin"}, path: execPath, upgrade: true, origin: "https://evil.example", wantStatus: 403, wantCode: "origin_not_allowed"},
		{name: "no ready pod", path: execPath, upgrade: true, setup: func(f *execFixture) { f.exec.resolveErr = k8s.ErrNoReadyPod }, wantStatus: 503, wantCode: "no_ready_pod"},
		{name: "container not found", path: execPath + "&container=web", upgrade: true, setup: func(f *execFixture) { f.exec.resolveErr = k8s.ErrContainerNotFound }, wantStatus: 404, wantCode: "container_not_found"},
		{name: "no allowed container in the pods", path: execPath, upgrade: true, setup: func(f *execFixture) { f.exec.resolveErr = k8s.ErrContainerNotAllowed }, wantStatus: 403, wantCode: "container_not_allowed"},
		{name: "service account token", path: execPath, upgrade: true, setup: func(f *execFixture) { f.exec.resolveErr = k8s.ErrServiceAccountToken }, wantStatus: 403, wantCode: "service_account_token"},
		{name: "cluster too slow", path: execPath, upgrade: true, setup: func(f *execFixture) { f.exec.resolveErr = context.DeadlineExceeded }, wantStatus: 503, wantCode: "kube_timeout"},
		{name: "unknown error is a generic 500", path: execPath, upgrade: true, setup: func(f *execFixture) { f.exec.resolveErr = errors.New("secret detail") }, wantStatus: 500, wantCode: "internal"},
		{name: "instance lookup error", path: execPath, setup: func(f *execFixture) { f.instRepo.fetchErr = errors.New("db down") }, wantStatus: 500, wantCode: "internal"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newExecFixture(t, tt.opts)
			if tt.setup != nil {
				tt.setup(f)
			}
			var hdr []string
			if tt.origin != "" {
				hdr = []string{"Origin", tt.origin}
			}
			w := f.do(t, tt.path, tt.upgrade, hdr...)
			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			msg, code := decodeErr(t, w)
			assert.Equal(t, tt.wantCode, code)
			if tt.wantStatus == 500 {
				assert.Equal(t, "Internal server error", msg)
				assert.NotContains(t, w.Body.String(), "secret detail")
			}
			assert.Zero(t, f.manager.Active(), "the slot is released")
			assert.False(t, f.exec.called("exec"))
			assert.Empty(t, f.audit.snapshot(), "no audit entry before the command")
		})
	}
}

// The checks run in the order of the contract: a later check never answers
// before an earlier one, and the cluster is not asked before the cheap checks.
func TestExec_CheckOrder(t *testing.T) {
	t.Parallel()
	t.Run("feature before everything", func(t *testing.T) {
		t.Parallel()
		cfg := execStreamCfg()
		cfg.Exec.Enabled = false
		f := newExecFixture(t, execFixtureOpts{cfg: cfg, authMeth: "api_key", userID: "other"})
		w := f.do(t, "/api/v1/stack-instances/nope/exec", false)
		_, code := decodeErr(t, w)
		assert.Equal(t, "feature_disabled", code)
	})
	t.Run("api key before the query", func(t *testing.T) {
		t.Parallel()
		f := newExecFixture(t, execFixtureOpts{authMeth: "api_key"})
		_, code := decodeErr(t, f.do(t, "/api/v1/stack-instances/inst-1/exec", false))
		assert.Equal(t, "api_key_not_allowed", code)
	})
	t.Run("query before the instance", func(t *testing.T) {
		t.Parallel()
		f := newExecFixture(t, execFixtureOpts{})
		_, code := decodeErr(t, f.do(t, "/api/v1/stack-instances/nope/exec", false))
		assert.Equal(t, "invalid_chart", code)
	})
	t.Run("role before cluster", func(t *testing.T) {
		t.Parallel()
		f := newExecFixture(t, execFixtureOpts{userID: "other", cluster: &models.Cluster{ID: "cl-1"}})
		_, code := decodeErr(t, f.do(t, execPath, false))
		assert.Equal(t, "forbidden", code)
	})
	t.Run("not deployed before the namespace check", func(t *testing.T) {
		t.Parallel()
		f := newExecFixture(t, execFixtureOpts{instance: &models.StackInstance{ID: "inst-1", OwnerID: "owner-1", Namespace: "ns", ClusterID: "cl-1", Status: models.StackStatusStopped}})
		f.exec.nsErr = k8s.ErrNamespaceNotAllowed
		_, code := decodeErr(t, f.do(t, execPath, false))
		assert.Equal(t, "instance_not_deployed", code)
		assert.False(t, f.exec.called("check:ns:manager-ns"))
	})
	t.Run("namespace guard before the chart", func(t *testing.T) {
		t.Parallel()
		f := newExecFixture(t, execFixtureOpts{})
		f.exec.nsErr = k8s.ErrNamespaceNotAllowed
		_, code := decodeErr(t, f.do(t, "/api/v1/stack-instances/inst-1/exec?chart=foreign", false))
		assert.Equal(t, "namespace_not_allowed", code)
	})
	t.Run("chart before the container", func(t *testing.T) {
		t.Parallel()
		f := newExecFixture(t, execFixtureOpts{})
		_, code := decodeErr(t, f.do(t, "/api/v1/stack-instances/inst-1/exec?chart=foreign&container=db", false))
		assert.Equal(t, "chart_not_found", code)
	})
	t.Run("container before the upgrade check, upgrade before the pod", func(t *testing.T) {
		t.Parallel()
		f := newExecFixture(t, execFixtureOpts{})
		_, code := decodeErr(t, f.do(t, execPath+"&container=db", false))
		assert.Equal(t, "container_not_allowed", code)
		_, code = decodeErr(t, f.do(t, execPath, false))
		assert.Equal(t, "upgrade_required", code)
		assert.False(t, f.exec.called("resolve"), "the pod is resolved after the upgrade checks")
	})
	t.Run("limit before the pod", func(t *testing.T) {
		t.Parallel()
		f := newExecFixture(t, execFixtureOpts{execCfg: podstream.Config{MaxPerUser: 1}})
		held, err := f.manager.Acquire("owner-1")
		require.NoError(t, err)
		defer held.Release()
		w := f.do(t, execPath, true)
		_, code := decodeErr(t, w)
		assert.Equal(t, http.StatusTooManyRequests, w.Code)
		assert.Equal(t, "exec_limit", code)
		assert.False(t, f.exec.called("resolve"))
	})
	t.Run("shutting down", func(t *testing.T) {
		t.Parallel()
		f := newExecFixture(t, execFixtureOpts{})
		require.NoError(t, f.manager.Shutdown(context.Background()))
		w := f.do(t, execPath, true)
		_, code := decodeErr(t, w)
		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
		assert.Equal(t, "shutting_down", code)
	})
}

func TestExec_PassesRequestToTheCluster(t *testing.T) {
	t.Parallel()
	cfg := execStreamCfg()
	cfg.Exec.AllowServiceAccountToken = true
	f := newExecFixture(t, execFixtureOpts{cfg: cfg})
	f.exec.resolveErr = k8s.ErrNoReadyPod
	f.do(t, execPath+"&container=worker", true)
	assert.True(t, f.exec.called("check:stack-ns:manager-ns"))
	f.exec.mu.Lock()
	defer f.exec.mu.Unlock()
	assert.Equal(t, "web", f.exec.lastReq.Release)
	assert.Equal(t, "worker", f.exec.lastReq.Container)
	assert.True(t, f.exec.lastReq.AllowServiceAccountToken)
	assert.True(t, f.exec.lastReq.AllowedContainer("web"))
	assert.False(t, f.exec.lastReq.AllowedContainer("db"))
}

type execClientFrames struct {
	text   []map[string]any
	stdout strings.Builder
	stderr strings.Builder
	order  []string
}

func readExecFrames(t *testing.T, c *gorilla.Conn) (*execClientFrames, int, string) {
	t.Helper()
	out := &execClientFrames{}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		mt, data, err := c.ReadMessage()
		if err != nil {
			var ce *gorilla.CloseError
			require.True(t, errors.As(err, &ce), "got %v", err)
			return out, ce.Code, ce.Text
		}
		if mt == gorilla.TextMessage {
			var m map[string]any
			require.NoError(t, json.Unmarshal(data, &m))
			out.text = append(out.text, m)
			out.order = append(out.order, "text:"+m["type"].(string))
			continue
		}
		switch data[0] {
		case podstream.ExecStreamStdout:
			out.stdout.Write(data[1:])
			out.order = append(out.order, "out")
		case podstream.ExecStreamStderr:
			out.stderr.Write(data[1:])
			out.order = append(out.order, "err")
		}
	}
}

func TestExec_EndToEnd(t *testing.T) {
	t.Parallel()
	f := newExecFixture(t, execFixtureOpts{})
	release := make(chan struct{})
	f.exec.run = func(_ context.Context, cmd []string, so, se io.Writer) (int, error) {
		_, _ = so.Write([]byte("one"))
		_, _ = se.Write([]byte("two"))
		<-release
		_, _ = so.Write([]byte("three"))
		return 3, nil
	}
	base := f.server(t)
	c, _, err := gorilla.DefaultDialer.Dial(base+"/api/v1/stack-instances/inst-1/exec?chart=web&container=worker", nil)
	require.NoError(t, err)
	defer c.Close()
	require.NoError(t, c.WriteMessage(gorilla.TextMessage, []byte(`{"type":"start","command":["bin/console","cache:clear","it's a test",""]}`)))

	// Frames arrive while the command still runs.
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := c.ReadMessage()
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"started","pod":"web-0","container":"worker"}`, string(data))
	_, data, err = c.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, append([]byte{podstream.ExecStreamStdout}, "one"...), data)
	_, data, err = c.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, append([]byte{podstream.ExecStreamStderr}, "two"...), data)
	assert.Equal(t, 1, f.hub.RevocableCount(), "registered for revocation")

	close(release)
	rest, code, _ := readExecFrames(t, c)
	assert.Equal(t, 1000, code)
	assert.Equal(t, "three", rest.stdout.String())
	require.Len(t, rest.text, 1)
	assert.Equal(t, map[string]any{"type": "exit", "exit_code": float64(3)}, rest.text[0])

	require.Eventually(t, func() bool { return len(f.audit.snapshot()) == 2 }, 3*time.Second, 10*time.Millisecond)
	start, end := f.audit.snapshot()[0], f.audit.snapshot()[1]
	assert.Equal(t, "exec", start.Action)
	assert.Equal(t, "stack_instance", start.EntityType)
	assert.Equal(t, "inst-1", start.EntityID)
	assert.Equal(t, "owner-1", start.UserID)
	var sd map[string]any
	require.NoError(t, json.Unmarshal([]byte(start.Details), &sd))
	assert.Equal(t, map[string]any{
		"chart": "web", "container": "worker", "pod": "web-0", "auth": "jwt",
		"command": `bin/console cache:clear 'it'\''s a test' ''`,
	}, sd)
	assert.Equal(t, "exec_end", end.Action)
	var ed map[string]any
	require.NoError(t, json.Unmarshal([]byte(end.Details), &ed))
	assert.EqualValues(t, 3, ed["exit_code"])
	assert.Equal(t, "nonzero", ed["result"])
	assert.EqualValues(t, 8, ed["bytes_stdout"])
	assert.EqualValues(t, 3, ed["bytes_stderr"])
	assert.EqualValues(t, 1000, ed["close_code"])
	assert.Equal(t, "web", ed["chart"])
	assert.Equal(t, "web-0", ed["pod"])
	assert.Contains(t, ed, "duration_ms")
	assert.NotContains(t, end.Details, "three", "never the output")
	f.exec.mu.Lock()
	assert.Equal(t, "stack-ns", f.exec.lastNS)
	f.exec.mu.Unlock()
	require.Eventually(t, func() bool { return f.manager.Active() == 0 }, 2*time.Second, 10*time.Millisecond)
}

func TestExec_AuditFailureStopsTheCommand(t *testing.T) {
	t.Parallel()
	f := newExecFixture(t, execFixtureOpts{audit: failingAudit{}})
	base := f.server(t)
	c, _, err := gorilla.DefaultDialer.Dial(base+"/api/v1/stack-instances/inst-1/exec?chart=web", nil)
	require.NoError(t, err)
	defer c.Close()
	require.NoError(t, c.WriteMessage(gorilla.TextMessage, []byte(`{"type":"start","command":["ls"]}`)))
	fr, code, _ := readExecFrames(t, c)
	assert.Equal(t, 1011, code)
	require.Len(t, fr.text, 1)
	assert.Equal(t, map[string]any{"type": "error", "message": "audit unavailable"}, fr.text[0])
	assert.False(t, f.exec.called("exec"), "the command never runs")
}

func TestExec_NoAuditEntryWithoutACommand(t *testing.T) {
	t.Parallel()
	f := newExecFixture(t, execFixtureOpts{})
	base := f.server(t)
	c, _, err := gorilla.DefaultDialer.Dial(base+"/api/v1/stack-instances/inst-1/exec?chart=web", nil)
	require.NoError(t, err)
	defer c.Close()
	require.NoError(t, c.WriteMessage(gorilla.TextMessage, []byte(`{"type":"start","command":[]}`)))
	_, code, _ := readExecFrames(t, c)
	assert.Equal(t, 4400, code)
	require.Eventually(t, func() bool { return f.manager.Active() == 0 }, 2*time.Second, 10*time.Millisecond)
	assert.Empty(t, f.audit.snapshot())
}

func TestExec_StreamErrorsAndRevocation(t *testing.T) {
	t.Parallel()
	t.Run("kubernetes error", func(t *testing.T) {
		t.Parallel()
		f := newExecFixture(t, execFixtureOpts{})
		f.exec.run = func(context.Context, []string, io.Writer, io.Writer) (int, error) {
			return -1, errors.New("container not found")
		}
		base := f.server(t)
		c, _, err := gorilla.DefaultDialer.Dial(base+"/api/v1/stack-instances/inst-1/exec?chart=web", nil)
		require.NoError(t, err)
		defer c.Close()
		require.NoError(t, c.WriteMessage(gorilla.TextMessage, []byte(`{"type":"start","command":["ls"]}`)))
		fr, code, _ := readExecFrames(t, c)
		assert.Equal(t, 4502, code)
		assert.Equal(t, "error", fr.text[len(fr.text)-1]["type"])
		require.Eventually(t, func() bool { return len(f.audit.snapshot()) == 2 }, 3*time.Second, 10*time.Millisecond)
		var ed map[string]any
		require.NoError(t, json.Unmarshal([]byte(f.audit.snapshot()[1].Details), &ed))
		assert.Nil(t, ed["exit_code"])
		assert.Equal(t, "error", ed["result"])
	})
	t.Run("missing rest config is internal", func(t *testing.T) {
		t.Parallel()
		f := newExecFixture(t, execFixtureOpts{})
		f.exec.run = func(context.Context, []string, io.Writer, io.Writer) (int, error) {
			return -1, k8s.ErrRESTConfigUnavailable
		}
		base := f.server(t)
		c, _, err := gorilla.DefaultDialer.Dial(base+"/api/v1/stack-instances/inst-1/exec?chart=web", nil)
		require.NoError(t, err)
		defer c.Close()
		require.NoError(t, c.WriteMessage(gorilla.TextMessage, []byte(`{"type":"start","command":["ls"]}`)))
		fr, code, _ := readExecFrames(t, c)
		assert.Equal(t, 1011, code)
		assert.Equal(t, "Internal server error", fr.text[len(fr.text)-1]["message"])
	})
	t.Run("instance delete closes the session", func(t *testing.T) {
		t.Parallel()
		f := newExecFixture(t, execFixtureOpts{})
		f.exec.run = func(ctx context.Context, _ []string, _, _ io.Writer) (int, error) {
			<-ctx.Done()
			return -1, ctx.Err()
		}
		base := f.server(t)
		c, _, err := gorilla.DefaultDialer.Dial(base+"/api/v1/stack-instances/inst-1/exec?chart=web", nil)
		require.NoError(t, err)
		defer c.Close()
		require.NoError(t, c.WriteMessage(gorilla.TextMessage, []byte(`{"type":"start","command":["sleep","100"]}`)))
		require.Eventually(t, func() bool { return f.exec.called("exec") }, 2*time.Second, 5*time.Millisecond)
		assert.Equal(t, 1, f.hub.DisconnectInstance("inst-1"))
		_, code, _ := readExecFrames(t, c)
		assert.Equal(t, 1008, code)
		require.Eventually(t, func() bool { return len(f.audit.snapshot()) == 2 }, 3*time.Second, 10*time.Millisecond)
		var ed map[string]any
		require.NoError(t, json.Unmarshal([]byte(f.audit.snapshot()[1].Details), &ed))
		assert.Equal(t, "revoked", ed["result"])
	})
}

func TestShellQuoteAndAuditCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cmd  []string
		want string
	}{
		{name: "plain", cmd: []string{"ls", "-la", "/tmp"}, want: "ls -la /tmp"},
		{name: "space", cmd: []string{"echo", "a b"}, want: "echo 'a b'"},
		{name: "quote", cmd: []string{"echo", "it's"}, want: `echo 'it'\''s'`},
		{name: "empty argument", cmd: []string{"echo", ""}, want: "echo ''"},
		{name: "metacharacters", cmd: []string{"sh", "-c", "a; b | c $(d)"}, want: "sh -c 'a; b | c $(d)'"},
		{name: "key=value", cmd: []string{"env", "A=b"}, want: "env A=b"},
		{name: "invalid UTF-8", cmd: []string{"echo", "a\xffb"}, want: "echo 'a?b'"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, auditCommand(tt.cmd))
		})
	}
	t.Run("cut at 1000 characters", func(t *testing.T) {
		t.Parallel()
		got := auditCommand([]string{"echo", strings.Repeat("é", 1500)})
		assert.True(t, strings.HasSuffix(got, " …[truncated]"))
		assert.Equal(t, 1000+len([]rune(" …[truncated]")), len([]rune(got)))
	})
	t.Run("exactly 1000 is kept", func(t *testing.T) {
		t.Parallel()
		got := auditCommand([]string{strings.Repeat("a", 1000)})
		assert.Equal(t, strings.Repeat("a", 1000), got)
	})
}

func TestExec_RejectedMetricHasKindExec(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	podstream.UseMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	t.Cleanup(func() { podstream.UseMeterProvider(otel.GetMeterProvider()) })

	off := execStreamCfg()
	off.Exec.Enabled = false
	f := newExecFixture(t, execFixtureOpts{cfg: off})
	f.do(t, execPath, false)
	f2 := newExecFixture(t, execFixtureOpts{cluster: &models.Cluster{ID: "cl-1"}})
	f2.do(t, execPath, false)
	f3 := newExecFixture(t, execFixtureOpts{execCfg: podstream.Config{MaxPerUser: 1}})
	held, err := f3.manager.Acquire("owner-1")
	require.NoError(t, err)
	f3.do(t, execPath, true)
	held.Release()

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	got := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "stack_stream.rejected_total" {
				continue
			}
			sum := m.Data.(metricdata.Sum[int64])
			for _, dp := range sum.DataPoints {
				kind, _ := dp.Attributes.Value("kind")
				reason, _ := dp.Attributes.Value("reason")
				got[kind.AsString()+"/"+reason.AsString()] += dp.Value
			}
		}
	}
	assert.EqualValues(t, 1, got["exec/feature_disabled"])
	assert.EqualValues(t, 1, got["exec/cluster_exec_disabled"])
	assert.EqualValues(t, 1, got["exec/user_limit"])
	assert.Zero(t, got["port_forward/feature_disabled"])
}

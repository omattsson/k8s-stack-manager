package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/config"
	"backend/internal/k8s"
	"backend/internal/models"
	"backend/internal/podstream"
	"backend/internal/sessionstore"
	"backend/internal/websocket"

	"github.com/gin-gonic/gin"
	gorilla "github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// fakeForwarder is a k8s.PortForwarder. The dial connects to a local TCP
// listener (echo) through a net.Pipe stream.
type fakeForwarder struct {
	nsErr      error
	resolveErr error
	dialErr    error
	target     *k8s.ServiceTarget
	dialTarget func() (io.ReadWriteCloser, error)
	// onDial runs inside DialPodPort (a revoke during the tunnel setup).
	onDial func()
	mu     sync.Mutex
	calls  []string
}

func (f *fakeForwarder) note(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeForwarder) CheckStreamNamespace(_ context.Context, ns, backendNS string) error {
	f.note("check:" + ns + ":" + backendNS)
	return f.nsErr
}

func (f *fakeForwarder) ResolveServiceTarget(_ context.Context, ns, svc string, port int32) (*k8s.ServiceTarget, error) {
	f.note("resolve")
	if f.resolveErr != nil {
		return nil, f.resolveErr
	}
	if f.target != nil {
		return f.target, nil
	}
	return &k8s.ServiceTarget{Service: svc, Pod: "web-0", ServicePort: port, TargetPort: 8080}, nil
}

func (f *fakeForwarder) DialPodPort(_ context.Context, ns, pod string, port int32) (k8s.PodPortStream, error) {
	f.note("dial")
	if f.onDial != nil {
		f.onDial()
	}
	if f.dialErr != nil {
		return nil, f.dialErr
	}
	return newEchoStream(), nil
}

// echoStream echoes everything that the session writes.
type echoStream struct {
	sess, pod net.Conn
	errR      *io.PipeReader
	errW      *io.PipeWriter
	once      sync.Once
}

func newEchoStream() *echoStream {
	a, b := net.Pipe()
	r, w := io.Pipe()
	e := &echoStream{sess: a, pod: b, errR: r, errW: w}
	go func() { _, _ = io.Copy(b, b) }()
	return e
}

func (e *echoStream) Data() io.ReadWriteCloser { return e.sess }
func (e *echoStream) Errors() io.Reader        { return e.errR }
func (e *echoStream) Close() error {
	e.once.Do(func() {
		_ = e.sess.Close()
		_ = e.pod.Close()
		_ = e.errW.Close()
		_ = e.errR.Close()
	})
	return nil
}

type recordingAudit struct {
	mu      sync.Mutex
	entries []models.AuditLog
}

func (r *recordingAudit) Create(_ context.Context, l *models.AuditLog) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, *l)
	return nil
}

func (r *recordingAudit) snapshot() []models.AuditLog {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]models.AuditLog(nil), r.entries...)
}

type streamFixture struct {
	router   *gin.Engine
	handler  *StreamHandler
	manager  *podstream.Manager
	instRepo *MockStackInstanceRepository
	clusters *MockClusterRepository
	fwd      *fakeForwarder
	audit    *recordingAudit
	hub      *websocket.Hub
	store    *sessionstore.MemoryStore
	users    *MockUserRepository
	keys     *MockAPIKeyRepository
}

type fixtureOpts struct {
	cfg       config.StreamConfig
	userID    string
	role      string
	authMeth  string
	origins   string
	instance  *models.StackInstance
	cluster   *models.Cluster
	managerCf podstream.Config
	// withRecheck wires the session store, the user and the API key
	// repositories (the re-check after the registration).
	withRecheck bool
	apiKeyID    string
}

func defaultStreamCfg() config.StreamConfig {
	return config.StreamConfig{
		BackendNamespace: "manager-ns",
		PortForward: config.PortForwardConfig{
			Enabled: true, MaxDuration: time.Minute, IdleTimeout: time.Minute, MaxPerUser: 5,
		},
	}
}

func newStreamFixture(t *testing.T, o fixtureOpts) *streamFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	f := &streamFixture{
		instRepo: NewMockStackInstanceRepository(), clusters: NewMockClusterRepository(),
		fwd: &fakeForwarder{}, audit: &recordingAudit{}, hub: websocket.NewHub(),
	}
	go f.hub.Run()
	t.Cleanup(f.hub.Shutdown)
	if o.userID == "" {
		o.userID = "owner-1"
	}
	if o.role == "" {
		o.role = "user"
	}
	if o.cfg.PortForward.MaxPerUser == 0 {
		o.cfg = defaultStreamCfg()
	}
	if o.instance == nil {
		o.instance = &models.StackInstance{ID: "inst-1", OwnerID: "owner-1", Namespace: "stack-ns", ClusterID: "cl-1", Status: models.StackStatusRunning}
	}
	if o.cluster == nil {
		o.cluster = &models.Cluster{ID: "cl-1", AllowPortForward: true}
	}
	require.NoError(t, f.instRepo.Create(context.Background(), o.instance))
	require.NoError(t, f.clusters.Create(context.Background(), o.cluster))

	o.managerCf.Timings = podstream.Timings{PingInterval: 50 * time.Millisecond}
	if o.managerCf.MaxDuration == 0 {
		o.managerCf.MaxDuration = time.Minute
	}
	f.manager = podstream.NewManager(o.managerCf)
	t.Cleanup(func() { _ = f.manager.Shutdown(context.Background()) })
	deps := StreamDeps{
		InstanceRepo: f.instRepo, ClusterRepo: f.clusters,
		Provider: func(string) (k8s.PortForwarder, error) { return f.fwd, nil },
		Manager:  f.manager, Hub: f.hub, Audit: f.audit,
		AllowedOrigins: o.origins, Config: o.cfg,
	}
	if o.withRecheck {
		f.store = sessionstore.NewMemoryStore()
		t.Cleanup(f.store.Stop)
		f.users, f.keys = NewMockUserRepository(), NewMockAPIKeyRepository()
		require.NoError(t, f.users.Create(context.Background(), &models.User{ID: o.userID, Username: o.userID, Role: o.role}))
		require.NoError(t, f.keys.Create(context.Background(), &models.APIKey{ID: "key-1", UserID: o.userID}))
		deps.SessionStore, deps.UserRepo, deps.APIKeyRepo = f.store, f.users, f.keys
	}
	f.handler = NewStreamHandler(deps)
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
		if o.apiKeyID != "" {
			c.Set("apiKeyID", o.apiKeyID)
		}
		c.Next()
	})
	r.GET("/api/v1/stack-instances/:id/port-forward", f.handler.PortForward)
	f.router = r
	return f
}

func (f *streamFixture) do(t *testing.T, path string, upgrade bool, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if upgrade {
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Sec-WebSocket-Version", "13")
		req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}

const pfPath = "/api/v1/stack-instances/inst-1/port-forward?service=web&port=80"

func decodeErr(t *testing.T, w *httptest.ResponseRecorder) (string, string) {
	t.Helper()
	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
	return body.Error, body.Code
}

func TestPortForward_PreUpgradeChecks(t *testing.T) {
	t.Parallel()
	disabled := defaultStreamCfg()
	disabled.PortForward.Enabled = false
	withKeys := defaultStreamCfg()
	withKeys.AllowAPIKeys = true

	tests := []struct {
		name       string
		opts       fixtureOpts
		path       string
		upgrade    bool
		hdr        map[string]string
		setup      func(*streamFixture)
		wantStatus int
		wantCode   string
	}{
		{name: "feature off", opts: fixtureOpts{cfg: disabled}, path: pfPath, wantStatus: 404, wantCode: "feature_disabled"},
		{name: "api key refused by default", opts: fixtureOpts{authMeth: "api_key"}, path: pfPath, wantStatus: 403, wantCode: "api_key_not_allowed"},
		{name: "api key allowed", opts: fixtureOpts{authMeth: "api_key", cfg: withKeys}, path: pfPath, wantStatus: 400, wantCode: "upgrade_required"},
		{name: "service missing", opts: fixtureOpts{}, path: "/api/v1/stack-instances/inst-1/port-forward?port=80", wantStatus: 400, wantCode: "invalid_service"},
		{name: "service not a DNS-1035 label", opts: fixtureOpts{}, path: "/api/v1/stack-instances/inst-1/port-forward?service=Bad_Name&port=80", wantStatus: 400, wantCode: "invalid_service"},
		{name: "service starts with a digit", opts: fixtureOpts{}, path: "/api/v1/stack-instances/inst-1/port-forward?service=1web&port=80", wantStatus: 400, wantCode: "invalid_service"},
		{name: "port missing", opts: fixtureOpts{}, path: "/api/v1/stack-instances/inst-1/port-forward?service=web", wantStatus: 400, wantCode: "invalid_port"},
		{name: "port zero", opts: fixtureOpts{}, path: "/api/v1/stack-instances/inst-1/port-forward?service=web&port=0", wantStatus: 400, wantCode: "invalid_port"},
		{name: "port too large", opts: fixtureOpts{}, path: "/api/v1/stack-instances/inst-1/port-forward?service=web&port=65536", wantStatus: 400, wantCode: "invalid_port"},
		{name: "port not a number", opts: fixtureOpts{}, path: "/api/v1/stack-instances/inst-1/port-forward?service=web&port=http", wantStatus: 400, wantCode: "invalid_port"},
		{name: "instance not found", opts: fixtureOpts{}, path: "/api/v1/stack-instances/nope/port-forward?service=web&port=80", wantStatus: 404, wantCode: "instance_not_found"},
		{name: "other user", opts: fixtureOpts{userID: "someone-else"}, path: pfPath, wantStatus: 403, wantCode: "forbidden"},
		{name: "owner passes", opts: fixtureOpts{}, path: pfPath, wantStatus: 400, wantCode: "upgrade_required"},
		{name: "devops passes", opts: fixtureOpts{userID: "x", role: "devops"}, path: pfPath, wantStatus: 400, wantCode: "upgrade_required"},
		{name: "admin passes", opts: fixtureOpts{userID: "x", role: "admin"}, path: pfPath, wantStatus: 400, wantCode: "upgrade_required"},
		{name: "cluster switch off", opts: fixtureOpts{cluster: &models.Cluster{ID: "cl-1"}}, path: pfPath, wantStatus: 403, wantCode: "cluster_port_forward_disabled"},
		{name: "cluster gone", opts: fixtureOpts{}, path: pfPath, setup: func(f *streamFixture) {
			_ = f.clusters.Delete(context.Background(), "cl-1")
		}, wantStatus: 403, wantCode: "cluster_port_forward_disabled"},
		{name: "draft", opts: fixtureOpts{instance: &models.StackInstance{ID: "inst-1", OwnerID: "owner-1", Namespace: "ns", ClusterID: "cl-1", Status: models.StackStatusDraft}}, path: pfPath, wantStatus: 409, wantCode: "instance_not_deployed"},
		{name: "stopped", opts: fixtureOpts{instance: &models.StackInstance{ID: "inst-1", OwnerID: "owner-1", Namespace: "ns", ClusterID: "cl-1", Status: models.StackStatusStopped}}, path: pfPath, wantStatus: 409, wantCode: "instance_not_deployed"},
		{name: "no namespace", opts: fixtureOpts{instance: &models.StackInstance{ID: "inst-1", OwnerID: "owner-1", ClusterID: "cl-1", Status: models.StackStatusRunning}}, path: pfPath, wantStatus: 409, wantCode: "instance_not_deployed"},
		{name: "namespace missing in the cluster", opts: fixtureOpts{}, path: pfPath, setup: func(f *streamFixture) { f.fwd.nsErr = k8s.ErrNamespaceNotFound }, wantStatus: 409, wantCode: "instance_not_deployed"},
		{name: "namespace not allowed", opts: fixtureOpts{}, path: pfPath, setup: func(f *streamFixture) { f.fwd.nsErr = k8s.ErrNamespaceNotAllowed }, wantStatus: 403, wantCode: "namespace_not_allowed"},
		// A row from before the namespace became immutable names the namespace
		// of another stack: refused before the cluster is asked.
		{name: "namespace shared with another instance", opts: fixtureOpts{}, path: pfPath, upgrade: true, setup: func(f *streamFixture) {
			require.NoError(t, f.instRepo.Create(context.Background(), &models.StackInstance{
				ID: "victim-1", OwnerID: "victim", Namespace: "stack-ns", ClusterID: "cl-1", Status: models.StackStatusRunning,
			}))
		}, wantStatus: 403, wantCode: "namespace_not_allowed"},
		{name: "namespace owner lookup error", opts: fixtureOpts{}, path: pfPath, setup: func(f *streamFixture) {
			f.instRepo.mu.Lock()
			defer f.instRepo.mu.Unlock()
			f.instRepo.namespaceSharedErr = errors.New("db down")
		}, wantStatus: 500, wantCode: "internal"},
		{name: "not an upgrade request", opts: fixtureOpts{}, path: pfPath, wantStatus: 400, wantCode: "upgrade_required"},
		{name: "origin not allowed", opts: fixtureOpts{origins: "same-origin"}, path: pfPath, upgrade: true, hdr: map[string]string{"Origin": "https://evil.example"}, wantStatus: 403, wantCode: "origin_not_allowed"},
		{name: "service not found", opts: fixtureOpts{}, path: pfPath, upgrade: true, setup: func(f *streamFixture) { f.fwd.resolveErr = k8s.ErrServiceNotFound }, wantStatus: 404, wantCode: "service_not_found"},
		{name: "port not found", opts: fixtureOpts{}, path: pfPath, upgrade: true, setup: func(f *streamFixture) { f.fwd.resolveErr = k8s.ErrPortNotFound }, wantStatus: 404, wantCode: "port_not_found"},
		{name: "external name", opts: fixtureOpts{}, path: pfPath, upgrade: true, setup: func(f *streamFixture) { f.fwd.resolveErr = k8s.ErrServiceExternalName }, wantStatus: 409, wantCode: "service_external_name"},
		{name: "no selector", opts: fixtureOpts{}, path: pfPath, upgrade: true, setup: func(f *streamFixture) { f.fwd.resolveErr = k8s.ErrServiceNoSelector }, wantStatus: 409, wantCode: "service_no_selector"},
		{name: "no ready pod", opts: fixtureOpts{}, path: pfPath, upgrade: true, setup: func(f *streamFixture) { f.fwd.resolveErr = k8s.ErrNoReadyPod }, wantStatus: 503, wantCode: "no_ready_pod"},
		{name: "dial failed", opts: fixtureOpts{}, path: pfPath, upgrade: true, setup: func(f *streamFixture) { f.fwd.dialErr = k8s.ErrDialFailed }, wantStatus: 502, wantCode: "dial_failed"},
		{name: "rest config unavailable", opts: fixtureOpts{}, path: pfPath, upgrade: true, setup: func(f *streamFixture) { f.fwd.dialErr = k8s.ErrRESTConfigUnavailable }, wantStatus: 500, wantCode: "internal"},
		{name: "unknown resolve error", opts: fixtureOpts{}, path: pfPath, upgrade: true, setup: func(f *streamFixture) { f.fwd.resolveErr = errors.New("secret detail") }, wantStatus: 500, wantCode: "internal"},
		{name: "instance lookup error", opts: fixtureOpts{}, path: pfPath, setup: func(f *streamFixture) { f.instRepo.fetchErr = errors.New("db down") }, wantStatus: 500, wantCode: "internal"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newStreamFixture(t, tt.opts)
			if tt.setup != nil {
				tt.setup(f)
			}
			w := f.do(t, tt.path, tt.upgrade, tt.hdr)
			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			msg, code := decodeErr(t, w)
			assert.Equal(t, tt.wantCode, code)
			if tt.wantStatus == 500 {
				assert.Equal(t, "Internal server error", msg)
			}
			assert.NotContains(t, w.Body.String(), "secret detail")
			assert.Equal(t, 0, f.manager.Active(), "slot released")
			assert.Empty(t, f.audit.snapshot(), "no audit entry before an upgrade")
		})
	}
}

func TestPortForward_CheckOrder(t *testing.T) {
	t.Parallel()
	// A request that fails several checks reports the first one: feature
	// off wins over a bad service, a bad service wins over a missing
	// instance, and the forbidden check wins over the cluster switch.
	off := defaultStreamCfg()
	off.PortForward.Enabled = false
	f := newStreamFixture(t, fixtureOpts{cfg: off, authMeth: "api_key"})
	w := f.do(t, "/api/v1/stack-instances/nope/port-forward?service=BAD&port=0", false, nil)
	_, code := decodeErr(t, w)
	assert.Equal(t, "feature_disabled", code)

	f = newStreamFixture(t, fixtureOpts{authMeth: "api_key"})
	w = f.do(t, "/api/v1/stack-instances/nope/port-forward?service=BAD&port=0", false, nil)
	_, code = decodeErr(t, w)
	assert.Equal(t, "api_key_not_allowed", code)

	f = newStreamFixture(t, fixtureOpts{})
	w = f.do(t, "/api/v1/stack-instances/nope/port-forward?service=BAD&port=0", false, nil)
	_, code = decodeErr(t, w)
	assert.Equal(t, "invalid_service", code)

	f = newStreamFixture(t, fixtureOpts{userID: "other", cluster: &models.Cluster{ID: "cl-1"}})
	w = f.do(t, pfPath, false, nil)
	_, code = decodeErr(t, w)
	assert.Equal(t, "forbidden", code)
}

func TestPortForward_TunnelLimit(t *testing.T) {
	t.Parallel()
	f := newStreamFixture(t, fixtureOpts{managerCf: podstream.Config{MaxPerUser: 1}})
	held, err := f.manager.Acquire("owner-1")
	require.NoError(t, err)
	defer held.Release()
	w := f.do(t, pfPath, true, nil)
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	_, code := decodeErr(t, w)
	assert.Equal(t, "tunnel_limit", code)
	assert.NotContains(t, f.fwd.calls, "resolve", "the limit is checked before the pod lookup")
}

func TestPortForward_PassesBackendNamespace(t *testing.T) {
	t.Parallel()
	f := newStreamFixture(t, fixtureOpts{})
	f.do(t, pfPath, false, nil)
	assert.Contains(t, f.fwd.calls, "check:stack-ns:manager-ns")
}

func (f *streamFixture) server(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(f.router)
	t.Cleanup(srv.Close)
	return srv, "ws" + strings.TrimPrefix(srv.URL, "http")
}

func TestPortForward_EndToEnd(t *testing.T) {
	t.Parallel()
	f := newStreamFixture(t, fixtureOpts{})
	_, base := f.server(t)

	c, _, err := gorilla.DefaultDialer.Dial(base+"/api/v1/stack-instances/inst-1/port-forward?service=web&port=80", nil)
	require.NoError(t, err)
	defer c.Close()

	require.NoError(t, c.WriteMessage(gorilla.BinaryMessage, []byte("ping-pong")))
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	mt, data, err := c.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, gorilla.BinaryMessage, mt)
	assert.Equal(t, "ping-pong", string(data))

	// Registered with the hub for revocation, with the identity.
	assert.Equal(t, 1, f.hub.RevocableCount())

	require.Eventually(t, func() bool { return len(f.audit.snapshot()) == 1 }, 2*time.Second, 10*time.Millisecond)
	start := f.audit.snapshot()[0]
	assert.Equal(t, "port_forward", start.Action)
	assert.Equal(t, "stack_instance", start.EntityType)
	assert.Equal(t, "inst-1", start.EntityID)
	assert.Equal(t, "owner-1", start.UserID)
	var sd map[string]any
	require.NoError(t, json.Unmarshal([]byte(start.Details), &sd))
	assert.Equal(t, "web", sd["service"])
	assert.EqualValues(t, 80, sd["port"])
	assert.Equal(t, "web-0", sd["pod"])
	assert.EqualValues(t, 8080, sd["target_port"])
	assert.Equal(t, "jwt", sd["auth"])
	assert.NotContains(t, start.Details, "ping-pong")

	// Closing ends the tunnel and writes the end entry.
	require.NoError(t, c.WriteControl(gorilla.CloseMessage, gorilla.FormatCloseMessage(1000, ""), time.Now().Add(time.Second)))
	require.Eventually(t, func() bool { return len(f.audit.snapshot()) == 2 }, 3*time.Second, 10*time.Millisecond)
	end := f.audit.snapshot()[1]
	assert.Equal(t, "port_forward_end", end.Action)
	var ed map[string]any
	require.NoError(t, json.Unmarshal([]byte(end.Details), &ed))
	assert.EqualValues(t, 9, ed["bytes_in"])
	assert.EqualValues(t, 9, ed["bytes_out"])
	assert.EqualValues(t, 1000, ed["close_code"])
	assert.Contains(t, ed, "duration_ms")
	assert.Equal(t, "web-0", ed["pod"])
	require.Eventually(t, func() bool { return f.manager.Active() == 0 }, 2*time.Second, 10*time.Millisecond)
}

func TestPortForward_RevokedByInstanceAndAPIKeyAuditAuth(t *testing.T) {
	t.Parallel()
	cfg := defaultStreamCfg()
	cfg.AllowAPIKeys = true
	f := newStreamFixture(t, fixtureOpts{cfg: cfg, authMeth: "api_key"})
	_, base := f.server(t)
	c, _, err := gorilla.DefaultDialer.Dial(base+"/api/v1/stack-instances/inst-1/port-forward?service=web&port=80", nil)
	require.NoError(t, err)
	defer c.Close()
	require.Eventually(t, func() bool { return len(f.audit.snapshot()) == 1 && f.hub.RevocableCount() == 1 }, 2*time.Second, 10*time.Millisecond)
	var sd map[string]any
	require.NoError(t, json.Unmarshal([]byte(f.audit.snapshot()[0].Details), &sd))
	assert.Equal(t, "api_key", sd["auth"])

	assert.Equal(t, 1, f.hub.DisconnectInstance("inst-1"))
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err = c.ReadMessage()
	var ce *gorilla.CloseError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, 1008, ce.Code)
}

func TestPortForward_UpgradeKeepsCrossOriginOutAndAllowsNoOrigin(t *testing.T) {
	t.Parallel()
	f := newStreamFixture(t, fixtureOpts{origins: "same-origin"})
	_, base := f.server(t)
	// No Origin header (stackctl): passes.
	c, _, err := gorilla.DefaultDialer.Dial(base+"/api/v1/stack-instances/inst-1/port-forward?service=web&port=80", nil)
	require.NoError(t, err)
	_ = c.Close()
	// A foreign browser origin: refused before the upgrade.
	_, resp, err := gorilla.DefaultDialer.Dial(base+"/api/v1/stack-instances/inst-1/port-forward?service=web&port=80",
		http.Header{"Origin": []string{"https://evil.example"}})
	require.Error(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

// A revoke that runs while the tunnel is being set up (after the checks, during
// the dial) is not seen by the hub: the session is not registered yet. The
// re-check right after the registration closes it with 1008 at once, without
// waiting for the revalidation (which does not run in this test).
func TestPortForward_RevokedDuringSetup(t *testing.T) {
	t.Parallel()
	cfg := defaultStreamCfg()
	cfg.AllowAPIKeys = true
	until := time.Now().Add(time.Hour)
	ctx := context.Background()
	tests := []struct {
		name       string
		opts       fixtureOpts
		revoke     func(*streamFixture)
		wantClosed bool
	}{
		{name: "token logged out", opts: fixtureOpts{withRecheck: true}, wantClosed: true,
			revoke: func(f *streamFixture) { require.NoError(t, f.store.BlockToken(ctx, "jti-1", until)) }},
		{name: "session revoked", opts: fixtureOpts{withRecheck: true}, wantClosed: true,
			revoke: func(f *streamFixture) { require.NoError(t, f.store.BlockSession(ctx, "sid-1", until)) }},
		{name: "api key: user blocked after the authentication", opts: fixtureOpts{withRecheck: true, cfg: cfg, authMeth: "api_key", apiKeyID: "key-1"}, wantClosed: true,
			revoke: func(f *streamFixture) { require.NoError(t, f.store.BlockUser(ctx, "owner-1", until)) }},
		{name: "api key: key deleted", opts: fixtureOpts{withRecheck: true, cfg: cfg, authMeth: "api_key", apiKeyID: "key-1"}, wantClosed: true,
			revoke: func(f *streamFixture) { require.NoError(t, f.keys.Delete(ctx, "owner-1", "key-1")) }},
		{name: "api key: user disabled", opts: fixtureOpts{withRecheck: true, cfg: cfg, authMeth: "api_key", apiKeyID: "key-1"}, wantClosed: true,
			revoke: func(f *streamFixture) {
				u, err := f.users.FindByID(ctx, "owner-1")
				require.NoError(t, err)
				u.Disabled = true
				require.NoError(t, f.users.Update(u))
			}},
		{name: "role lost during the setup", opts: fixtureOpts{withRecheck: true, userID: "dev-1", role: "devops"}, wantClosed: true,
			revoke: func(f *streamFixture) {
				u, err := f.users.FindByID(ctx, "dev-1")
				require.NoError(t, err)
				u.Role = "user"
				require.NoError(t, f.users.Update(u))
			}},
		{name: "instance stopped during the setup", opts: fixtureOpts{withRecheck: true}, wantClosed: true,
			revoke: func(f *streamFixture) {
				i, err := f.instRepo.FindByID(ctx, "inst-1")
				require.NoError(t, err)
				i.Status = models.StackStatusStopped
				require.NoError(t, f.instRepo.Update(ctx, i))
			}},
		{name: "cluster switch turned off during the setup", opts: fixtureOpts{withRecheck: true}, wantClosed: true,
			revoke: func(f *streamFixture) {
				require.NoError(t, f.clusters.Update(ctx, &models.Cluster{ID: "cl-1"}))
			}},
		{name: "no revoke keeps the tunnel", opts: fixtureOpts{withRecheck: true}, revoke: func(*streamFixture) {}},
		{name: "store lookup failure fails open", opts: fixtureOpts{withRecheck: true}, revoke: func(f *streamFixture) {
			f.users.findErr = errors.New("db down")
		}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newStreamFixture(t, tt.opts)
			f.fwd.onDial = func() { tt.revoke(f) }
			_, base := f.server(t)
			c, _, err := gorilla.DefaultDialer.Dial(base+"/api/v1/stack-instances/inst-1/port-forward?service=web&port=80", nil)
			require.NoError(t, err)
			defer c.Close()
			if !tt.wantClosed {
				require.NoError(t, c.WriteMessage(gorilla.BinaryMessage, []byte("still open")))
				_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
				_, data, err := c.ReadMessage()
				require.NoError(t, err)
				assert.Equal(t, "still open", string(data))
				return
			}
			_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
			_, _, err = c.ReadMessage()
			var ce *gorilla.CloseError
			require.ErrorAs(t, err, &ce)
			assert.Equal(t, 1008, ce.Code)
			assert.Equal(t, "session revoked", ce.Text)
			require.Eventually(t, func() bool { return len(f.audit.snapshot()) == 2 }, 3*time.Second, 10*time.Millisecond)
			var ed map[string]any
			require.NoError(t, json.Unmarshal([]byte(f.audit.snapshot()[1].Details), &ed))
			assert.EqualValues(t, 1008, ed["close_code"])
			assert.EqualValues(t, 0, ed["bytes_in"])
		})
	}
}

// The refusals are counted once each, with the code as the reason; the
// tunnel limit is counted by the manager (user_limit), not a second time. A
// 500 is a fault, not a refusal. This test replaces the metric instruments, so
// it does not run in parallel with the others (podstream.UseMeterProvider
// replaces the instruments).
func TestPortForward_RejectedMetricCountsEveryRefusal(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	podstream.UseMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	t.Cleanup(func() { podstream.UseMeterProvider(otel.GetMeterProvider()) })

	collect := func() map[string]int64 {
		var rm metricdata.ResourceMetrics
		require.NoError(t, reader.Collect(context.Background(), &rm))
		out := map[string]int64{}
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name != "stack_stream.rejected_total" {
					continue
				}
				sum, ok := m.Data.(metricdata.Sum[int64])
				require.True(t, ok)
				for _, dp := range sum.DataPoints {
					reason, _ := dp.Attributes.Value("reason")
					out[reason.AsString()] += dp.Value
				}
			}
		}
		return out
	}

	off := defaultStreamCfg()
	off.PortForward.Enabled = false
	run := func(o fixtureOpts, path string, upgrade bool, setup func(*streamFixture)) {
		f := newStreamFixture(t, o)
		if setup != nil {
			setup(f)
		}
		f.do(t, path, upgrade, nil)
	}
	run(fixtureOpts{cfg: off}, pfPath, false, nil)
	run(fixtureOpts{}, "/api/v1/stack-instances/inst-1/port-forward?port=80", false, nil)
	run(fixtureOpts{}, "/api/v1/stack-instances/inst-1/port-forward?service=web", false, nil)
	run(fixtureOpts{}, "/api/v1/stack-instances/nope/port-forward?service=web&port=80", false, nil)
	run(fixtureOpts{userID: "other"}, pfPath, false, nil)
	run(fixtureOpts{cluster: &models.Cluster{ID: "cl-1"}}, pfPath, false, nil)
	run(fixtureOpts{}, pfPath, true, func(f *streamFixture) { f.fwd.resolveErr = k8s.ErrServiceNotFound })
	run(fixtureOpts{}, pfPath, true, func(f *streamFixture) { f.fwd.dialErr = k8s.ErrDialFailed })
	run(fixtureOpts{}, pfPath, true, func(f *streamFixture) { f.fwd.resolveErr = context.DeadlineExceeded })
	run(fixtureOpts{}, pfPath, true, func(f *streamFixture) { f.fwd.resolveErr = errors.New("boom") }) // 500: not counted
	// Tunnel limit.
	f := newStreamFixture(t, fixtureOpts{managerCf: podstream.Config{MaxPerUser: 1}})
	held, err := f.manager.Acquire("owner-1")
	require.NoError(t, err)
	f.do(t, pfPath, true, nil)
	held.Release()

	got := collect()
	for reason, want := range map[string]int64{
		"feature_disabled": 1, "invalid_service": 1, "invalid_port": 1, "instance_not_found": 1,
		"forbidden": 1, "cluster_port_forward_disabled": 1, "service_not_found": 1,
		"dial_failed": 1, "kube_timeout": 1, "user_limit": 1,
	} {
		assert.Equal(t, want, got[reason], reason)
	}
	assert.Zero(t, got["tunnel_limit"], "the manager counts the limit")
	assert.Zero(t, got["internal"], "a 500 is not a refusal")
}

func TestMapStreamError_Timeouts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		err        error
		dial       bool
		wantStatus int
		wantCode   string
	}{
		{"lookup timeout", fmt.Errorf("get namespace: %w", context.DeadlineExceeded), false, 503, "kube_timeout"},
		{"dial timeout", context.DeadlineExceeded, true, 502, "dial_failed"},
		{"dial failure", fmt.Errorf("%w: refused", k8s.ErrDialFailed), true, 502, "dial_failed"},
		{"cancelled", context.Canceled, false, 500, "internal"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			status, code, _ := mapStreamError(tt.err, tt.dial)
			assert.Equal(t, tt.wantStatus, status)
			assert.Equal(t, tt.wantCode, code)
		})
	}
}

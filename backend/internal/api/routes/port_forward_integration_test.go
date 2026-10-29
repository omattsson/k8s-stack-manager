package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/api/handlers"
	"backend/internal/api/middleware"
	"backend/internal/config"
	"backend/internal/health"
	"backend/internal/k8s"
	"backend/internal/models"
	"backend/internal/podstream"
	"backend/internal/sessionstore"
	"backend/internal/websocket"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
	gorilla "github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file is the no-cluster integration test of the port-forward route
// (plan of #722, section 3): a real httptest.Server with the real SetupRoutes
// chain, the memory session store, real JWT / API-key auth, a real AuthHandler
// logout, the real hub revalidation (identity checker and stream access
// checker), and a fake forwarder that dials a local TCP echo listener. It needs
// no database and no cluster, so it runs under -short.

const (
	itSecret     = "integration-secret-for-port-forward"
	itRevalidate = 40 * time.Millisecond
)

// ---- fakes ----

type itInstanceRepo struct {
	models.StackInstanceRepository
	mu   sync.Mutex
	inst *models.StackInstance // nil: deleted
}

func (r *itInstanceRepo) FindByID(_ context.Context, _ string) (*models.StackInstance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inst == nil {
		return nil, dberrors.NewDatabaseError("find", dberrors.ErrNotFound)
	}
	cp := *r.inst
	return &cp, nil
}

func (r *itInstanceRepo) FindByIDs(_ context.Context, ids []string) (map[string]*models.StackInstance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]*models.StackInstance, len(ids))
	for _, id := range ids {
		if r.inst != nil {
			cp := *r.inst
			out[id] = &cp
		}
	}
	return out, nil
}

func (r *itInstanceRepo) mutate(fn func(*models.StackInstance)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fn(r.inst)
}

func (r *itInstanceRepo) remove() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inst = nil
}

type itUserRepo struct {
	models.UserRepository
	mu    sync.Mutex
	users map[string]*models.User
}

func (r *itUserRepo) FindByID(_ context.Context, id string) (*models.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.users[id]
	if !ok {
		return nil, dberrors.NewDatabaseError("find", dberrors.ErrNotFound)
	}
	cp := *u
	return &cp, nil
}

func (r *itUserRepo) FindByIDs(_ context.Context, ids []string) (map[string]*models.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]*models.User{}
	for _, id := range ids {
		if u, ok := r.users[id]; ok {
			cp := *u
			out[id] = &cp
		}
	}
	return out, nil
}

func (r *itUserRepo) setRole(id, role string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.users[id].Role = role
}

type itClusterRepo struct {
	models.ClusterRepository
	mu      sync.Mutex
	cluster models.Cluster
}

func (r *itClusterRepo) FindByID(_ context.Context, id string) (*models.Cluster, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := r.cluster
	return &cp, nil
}

func (r *itClusterRepo) List(_ context.Context) ([]models.Cluster, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return []models.Cluster{r.cluster}, nil
}

func (r *itClusterRepo) StreamFlagsByIDs(_ context.Context, ids []string, includeDefault bool) (map[string]bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = r.cluster.AllowPortForward
	}
	if includeDefault {
		out[models.DefaultClusterKey] = r.cluster.AllowPortForward
	}
	return out, nil
}

func (r *itClusterRepo) setAllow(v bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cluster.AllowPortForward = v
}

type itAPIKeyRepo struct {
	models.APIKeyRepository
	key *models.APIKey
}

func (r *itAPIKeyRepo) FindByPrefix(_ context.Context, prefix string) ([]*models.APIKey, error) {
	if r.key != nil && r.key.Prefix == prefix {
		return []*models.APIKey{r.key}, nil
	}
	return nil, nil
}

func (r *itAPIKeyRepo) FindByID(_ context.Context, userID, keyID string) (*models.APIKey, error) {
	if r.key != nil && r.key.ID == keyID && r.key.UserID == userID {
		return r.key, nil
	}
	return nil, dberrors.NewDatabaseError("find_by_id", dberrors.ErrNotFound)
}

func (r *itAPIKeyRepo) FindByIDs(_ context.Context, ids []string) ([]*models.APIKey, error) {
	var out []*models.APIKey
	for _, id := range ids {
		if r.key != nil && r.key.ID == id {
			out = append(out, r.key)
		}
	}
	return out, nil
}

func (r *itAPIKeyRepo) UpdateLastUsed(context.Context, string, string, time.Time) error { return nil }

type itAudit struct {
	mu      sync.Mutex
	entries []models.AuditLog
}

func (a *itAudit) Create(_ context.Context, l *models.AuditLog) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, *l)
	return nil
}

func (a *itAudit) byAction(action string) []models.AuditLog {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []models.AuditLog
	for _, e := range a.entries {
		if e.Action == action {
			out = append(out, e)
		}
	}
	return out
}

func (a *itAudit) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.entries)
}

type itStream struct {
	data net.Conn
	errR *io.PipeReader
	errW *io.PipeWriter
	once sync.Once
}

func (s *itStream) Data() io.ReadWriteCloser { return s.data }
func (s *itStream) Errors() io.Reader        { return s.errR }
func (s *itStream) Close() error {
	s.once.Do(func() { _ = s.data.Close(); _ = s.errW.Close(); _ = s.errR.Close() })
	return nil
}

// itForwarder dials the local echo listener instead of a pod.
type itForwarder struct {
	addr string
	mu   sync.Mutex
	dial []int32
}

func (f *itForwarder) CheckStreamNamespace(context.Context, string, string) error { return nil }
func (f *itForwarder) ResolveServiceTarget(_ context.Context, _, svc string, port int32) (*k8s.ServiceTarget, error) {
	return &k8s.ServiceTarget{Service: svc, Pod: "web-0", ServicePort: port, TargetPort: 8080}, nil
}
func (f *itForwarder) DialPodPort(ctx context.Context, _, _ string, port int32) (k8s.PodPortStream, error) {
	f.mu.Lock()
	f.dial = append(f.dial, port)
	f.mu.Unlock()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", f.addr)
	if err != nil {
		return nil, k8s.ErrDialFailed
	}
	r, w := io.Pipe()
	return &itStream{data: conn, errR: r, errW: w}, nil
}

func startEchoListener(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().String()
}

// ---- harness ----

type itOpts struct {
	allowAPIKeys bool
}

type itHarness struct {
	srv       *httptest.Server
	hub       *websocket.Hub
	store     *sessionstore.MemoryStore
	instances *itInstanceRepo
	users     *itUserRepo
	clusters  *itClusterRepo
	audit     *itAudit
	fwd       *itForwarder
	mgr       *podstream.Manager
	apiKey    string // raw API key of user "owner"
}

const itPath = "/api/v1/stack-instances/i1/port-forward?service=web&port=80"

func newITHarness(t *testing.T, o itOpts) *itHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := testConfig()
	cfg.Auth.JWTSecret = itSecret
	cfg.Auth.AccessTokenExpiration = time.Hour
	cfg.Stream = config.StreamConfig{
		AllowAPIKeys: o.allowAPIKeys,
		PortForward: config.PortForwardConfig{
			Enabled: true, MaxDuration: time.Minute, IdleTimeout: time.Minute, MaxPerUser: 3,
		},
	}

	h := &itHarness{
		store:     sessionstore.NewMemoryStore(),
		instances: &itInstanceRepo{inst: &models.StackInstance{ID: "i1", OwnerID: "owner", Namespace: "ns", ClusterID: "c1", Status: models.StackStatusRunning}},
		users: &itUserRepo{users: map[string]*models.User{
			"owner": {ID: "owner", Username: "owner", Role: "user"},
			"dev":   {ID: "dev", Username: "dev", Role: "devops"},
			"other": {ID: "other", Username: "other", Role: "user"},
		}},
		clusters: &itClusterRepo{cluster: models.Cluster{ID: "c1", AllowPortForward: true}},
		audit:    &itAudit{},
		fwd:      &itForwarder{addr: startEchoListener(t)},
	}
	t.Cleanup(h.store.Stop)

	h.hub = websocket.NewHub()
	go h.hub.Run()
	t.Cleanup(h.hub.Shutdown)

	h.mgr = podstream.NewManager(podstream.Config{MaxPerUser: 3, Timings: podstream.Timings{PingInterval: 100 * time.Millisecond}})
	t.Cleanup(func() { _ = h.mgr.Shutdown(context.Background()) })

	rawKey, prefix, hash, err := models.GenerateAPIKey()
	require.NoError(t, err)
	h.apiKey = rawKey
	keys := &itAPIKeyRepo{key: &models.APIKey{ID: "key1", UserID: "owner", Name: "ci", KeyHash: hash, Prefix: prefix}}

	sh := handlers.NewStreamHandler(handlers.StreamDeps{
		InstanceRepo: h.instances, ClusterRepo: h.clusters,
		Provider: func(string) (k8s.PortForwarder, error) { return h.fwd, nil },
		Manager:  h.mgr, Hub: h.hub, Audit: h.audit, AllowedOrigins: "*", Config: cfg.Stream,
		SessionStore: h.store, UserRepo: h.users, APIKeyRepo: keys,
	})

	authHandler := handlers.NewAuthHandler(h.users, &cfg.Auth, &cfg.OIDC)
	authHandler.SetSessionStore(h.store)
	authHandler.SetWebSocketRevoker(h.hub)

	router := gin.New()
	health := health.New()
	rl := SetupRoutes(router, Deps{
		HealthChecker: health, Config: cfg, Hub: h.hub,
		AuthHandler: authHandler, UserRepo: h.users, APIKeyRepo: keys, SessionStore: h.store,
		InstanceHandler: &handlers.InstanceHandler{},
		StreamHandler:   sh,
		AuditLogger:     h.audit,
	})
	t.Cleanup(rl.Stop)

	// The wiring of api/main.go: stream access checker plus identity checker.
	h.hub.SetRevocableAccessChecker(handlers.NewStreamAccessChecker(h.instances, h.users, h.clusters, cfg.Stream))
	h.hub.StartRevalidation(itRevalidate, handlers.NewWebSocketRevocationChecker(h.store, h.users, keys))

	h.srv = httptest.NewServer(router)
	t.Cleanup(h.srv.Close)
	return h
}

func (h *itHarness) wsURL(pathAndQuery string) string {
	return "ws" + strings.TrimPrefix(h.srv.URL, "http") + pathAndQuery
}

type itToken struct {
	jwt string
}

func (h *itHarness) token(t *testing.T, userID, role, sid string, ttl time.Duration) itToken {
	t.Helper()
	tok, err := middleware.GenerateTokenWithOpts(middleware.GenerateTokenOptions{
		UserID: userID, Username: userID, Role: role, Secret: itSecret, Expiration: ttl, SessionID: sid,
	})
	require.NoError(t, err)
	return itToken{jwt: tok}
}

func (h *itHarness) dial(t *testing.T, tok itToken) *gorilla.Conn {
	t.Helper()
	c, resp, err := gorilla.DefaultDialer.Dial(h.wsURL(itPath), http.Header{"Authorization": []string{"Bearer " + tok.jwt}})
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	require.NoError(t, err, "dial")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// echo sends msg and waits for the same bytes back.
func echo(t *testing.T, c *gorilla.Conn, msg string) {
	t.Helper()
	require.NoError(t, c.SetWriteDeadline(time.Now().Add(5*time.Second)))
	require.NoError(t, c.WriteMessage(gorilla.BinaryMessage, []byte(msg)))
	var got []byte
	require.NoError(t, c.SetReadDeadline(time.Now().Add(5*time.Second)))
	for len(got) < len(msg) {
		mt, data, err := c.ReadMessage()
		require.NoError(t, err)
		if mt == gorilla.BinaryMessage {
			got = append(got, data...)
		}
	}
	assert.Equal(t, msg, string(got))
}

// closeCode waits for the server to close the tunnel and returns the code.
func closeCode(t *testing.T, c *gorilla.Conn) int {
	t.Helper()
	require.NoError(t, c.SetReadDeadline(time.Now().Add(5*time.Second)))
	for {
		_, _, err := c.ReadMessage()
		if err == nil {
			continue
		}
		var ce *gorilla.CloseError
		require.True(t, errors.As(err, &ce), "want a close frame, got %v", err)
		return ce.Code
	}
}

func (h *itHarness) endEntry(t *testing.T) map[string]any {
	t.Helper()
	require.Eventually(t, func() bool { return len(h.audit.byAction("port_forward_end")) == 1 },
		3*time.Second, 20*time.Millisecond, "port_forward_end entry")
	var d map[string]any
	require.NoError(t, json.Unmarshal([]byte(h.audit.byAction("port_forward_end")[0].Details), &d))
	return d
}

func (h *itHarness) get(t *testing.T, path string, hdr http.Header) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, h.srv.URL+path, nil)
	require.NoError(t, err)
	for k, v := range hdr {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

// ---- tests ----

func TestPortForwardIntegration_RoundTripAndAudit(t *testing.T) {
	t.Parallel()
	h := newITHarness(t, itOpts{})
	c := h.dial(t, h.token(t, "owner", "user", "sid-1", time.Hour))

	echo(t, c, "hello tunnel")
	big := strings.Repeat("0123456789abcdef", 4096) // 64 KiB in one frame
	echo(t, c, big)

	require.NoError(t, c.WriteControl(gorilla.CloseMessage, gorilla.FormatCloseMessage(gorilla.CloseNormalClosure, ""), time.Now().Add(time.Second)))
	assert.Equal(t, gorilla.CloseNormalClosure, closeCode(t, c))

	// Exactly one start and one end entry; the start carries the target.
	start := h.audit.byAction("port_forward")
	require.Len(t, start, 1)
	assert.Equal(t, "stack_instance", start[0].EntityType)
	assert.Equal(t, "i1", start[0].EntityID)
	assert.Equal(t, "owner", start[0].UserID)
	var sd map[string]any
	require.NoError(t, json.Unmarshal([]byte(start[0].Details), &sd))
	assert.Equal(t, "web", sd["service"])
	assert.EqualValues(t, 80, sd["port"])
	assert.Equal(t, "web-0", sd["pod"])
	assert.EqualValues(t, 8080, sd["target_port"])
	assert.Equal(t, "jwt", sd["auth"])

	end := h.endEntry(t)
	assert.EqualValues(t, len("hello tunnel")+len(big), end["bytes_in"])
	assert.EqualValues(t, len("hello tunnel")+len(big), end["bytes_out"])
	assert.EqualValues(t, 1000, end["close_code"])
	// Never payload bytes in any entry.
	h.audit.mu.Lock()
	for _, e := range h.audit.entries {
		assert.NotContains(t, e.Details, "hello tunnel")
	}
	h.audit.mu.Unlock()

	// No second entry appears (the audit middleware ignores GET).
	time.Sleep(150 * time.Millisecond)
	assert.Equal(t, 2, h.audit.count())
	assert.Len(t, h.audit.byAction("port_forward_end"), 1)
}

func TestPortForwardIntegration_LogoutClosesWith1008(t *testing.T) {
	t.Parallel()
	h := newITHarness(t, itOpts{})
	tok := h.token(t, "owner", "user", "sid-1", time.Hour)
	c := h.dial(t, tok)
	echo(t, c, "before logout")

	req, err := http.NewRequest(http.MethodPost, h.srv.URL+"/api/v1/auth/logout", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+tok.jwt)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	assert.Equal(t, gorilla.ClosePolicyViolation, closeCode(t, c))
	end := h.endEntry(t)
	assert.EqualValues(t, 1008, end["close_code"])

	// The token no longer opens a tunnel.
	status, _ := h.get(t, itPath, http.Header{"Authorization": []string{"Bearer " + tok.jwt}})
	assert.Equal(t, http.StatusUnauthorized, status)
}

func TestPortForwardIntegration_SessionBlockClosesAtRevalidation(t *testing.T) {
	t.Parallel()
	h := newITHarness(t, itOpts{})
	tok := h.token(t, "owner", "user", "sid-block", time.Hour)
	c := h.dial(t, tok)
	echo(t, c, "alive")

	// A block written by another replica: no local disconnect, only the
	// revalidation of the hub can see it.
	require.NoError(t, h.store.BlockSession(context.Background(), "sid-block", time.Now().Add(time.Hour)))

	assert.Equal(t, gorilla.ClosePolicyViolation, closeCode(t, c))
	assert.EqualValues(t, 1008, h.endEntry(t)["close_code"])
}

func TestPortForwardIntegration_AccessLostClosesAtRevalidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		userID string
		role   string // role in the JWT at open time
		change func(h *itHarness)
	}{
		{name: "devops demoted to user and not the owner", userID: "dev", role: "devops", change: func(h *itHarness) { h.users.setRole("dev", "user") }},
		{name: "owner changed", userID: "owner", role: "user", change: func(h *itHarness) { h.instances.mutate(func(i *models.StackInstance) { i.OwnerID = "other" }) }},
		{name: "instance deleted", userID: "owner", role: "user", change: func(h *itHarness) { h.instances.remove() }},
		{name: "instance stopped", userID: "owner", role: "user", change: func(h *itHarness) {
			h.instances.mutate(func(i *models.StackInstance) { i.Status = models.StackStatusStopped })
		}},
		{name: "cluster switch turned off", userID: "owner", role: "user", change: func(h *itHarness) { h.clusters.setAllow(false) }},
		{name: "user disabled", userID: "owner", role: "user", change: func(h *itHarness) {
			h.users.mu.Lock()
			h.users.users["owner"].Disabled = true
			h.users.mu.Unlock()
		}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newITHarness(t, itOpts{})
			c := h.dial(t, h.token(t, tt.userID, tt.role, "sid-"+tt.userID, time.Hour))
			echo(t, c, "alive")

			tt.change(h)

			assert.Equal(t, gorilla.ClosePolicyViolation, closeCode(t, c))
			assert.EqualValues(t, 1008, h.endEntry(t)["close_code"])
			assert.Len(t, h.audit.byAction("port_forward"), 1)
		})
	}
}

func TestPortForwardIntegration_InstanceDeleteClosesWith1008(t *testing.T) {
	t.Parallel()
	h := newITHarness(t, itOpts{})
	c := h.dial(t, h.token(t, "owner", "user", "sid-1", time.Hour))
	echo(t, c, "alive")

	// DELETE/stop/clean call this on the hub (see TestInstanceHandler_RevokesStreams
	// for the handler side). It closes at once, without waiting for a revalidation.
	assert.Equal(t, 1, h.hub.DisconnectInstance("i1"))
	assert.Equal(t, gorilla.ClosePolicyViolation, closeCode(t, c))
	assert.EqualValues(t, 1008, h.endEntry(t)["close_code"])
}

func TestPortForwardIntegration_OtherUserAndPreUpgradeStatuses(t *testing.T) {
	t.Parallel()
	h := newITHarness(t, itOpts{})
	other := h.token(t, "other", "user", "sid-o", time.Hour)

	status, body := h.get(t, itPath, http.Header{"Authorization": []string{"Bearer " + other.jwt}})
	assert.Equal(t, http.StatusForbidden, status)
	assert.Equal(t, "forbidden", body["code"])

	// Another user is refused before the upgrade, also with upgrade headers.
	_, resp, err := gorilla.DefaultDialer.Dial(h.wsURL(itPath), http.Header{"Authorization": []string{"Bearer " + other.jwt}})
	require.Error(t, err)
	require.NotNil(t, resp)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	// No credential at all.
	status, _ = h.get(t, itPath, nil)
	assert.Equal(t, http.StatusUnauthorized, status)

	assert.Equal(t, 0, h.audit.count(), "a refused request writes no audit entry")
	h.fwd.mu.Lock()
	assert.Empty(t, h.fwd.dial, "no pod connection for a refused request")
	h.fwd.mu.Unlock()
}

func TestPortForwardIntegration_ClusterSwitchOffRefusedBeforeUpgrade(t *testing.T) {
	t.Parallel()
	h := newITHarness(t, itOpts{})
	h.clusters.setAllow(false)
	tok := h.token(t, "owner", "user", "sid-1", time.Hour)

	_, resp, err := gorilla.DefaultDialer.Dial(h.wsURL(itPath), http.Header{"Authorization": []string{"Bearer " + tok.jwt}})
	require.Error(t, err)
	require.NotNil(t, resp)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "cluster_port_forward_disabled", body["code"])
	assert.Equal(t, 0, h.audit.count())
}

func TestPortForwardIntegration_APIKey(t *testing.T) {
	t.Parallel()
	t.Run("refused by default", func(t *testing.T) {
		t.Parallel()
		h := newITHarness(t, itOpts{})
		for _, hdr := range []http.Header{
			{"X-API-Key": []string{"sk_" + h.apiKey}},
			{"X-API-Key": []string{h.apiKey}},
		} {
			status, body := h.get(t, itPath, hdr)
			assert.Equal(t, http.StatusForbidden, status)
			assert.Equal(t, "api_key_not_allowed", body["code"])
		}
		// A real upgrade request is refused the same way.
		_, resp, err := gorilla.DefaultDialer.Dial(h.wsURL(itPath), http.Header{"X-API-Key": []string{"sk_" + h.apiKey}})
		require.Error(t, err)
		require.NotNil(t, resp)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		assert.Equal(t, 0, h.audit.count())
	})

	t.Run("a wrong key is 401", func(t *testing.T) {
		t.Parallel()
		h := newITHarness(t, itOpts{allowAPIKeys: true})
		status, _ := h.get(t, itPath, http.Header{"X-API-Key": []string{"sk_" + strings.Repeat("0", 64)}})
		assert.Equal(t, http.StatusUnauthorized, status)
	})

	t.Run("allowed with the setting", func(t *testing.T) {
		t.Parallel()
		h := newITHarness(t, itOpts{allowAPIKeys: true})
		c, resp, err := gorilla.DefaultDialer.Dial(h.wsURL(itPath), http.Header{"X-API-Key": []string{"sk_" + h.apiKey}})
		if resp != nil {
			defer resp.Body.Close()
		}
		require.NoError(t, err)
		t.Cleanup(func() { _ = c.Close() })
		echo(t, c, "through an api key")

		start := h.audit.byAction("port_forward")
		require.Len(t, start, 1)
		var sd map[string]any
		require.NoError(t, json.Unmarshal([]byte(start[0].Details), &sd))
		assert.Equal(t, "api_key", sd["auth"])

		require.NoError(t, c.WriteControl(gorilla.CloseMessage, gorilla.FormatCloseMessage(gorilla.CloseNormalClosure, ""), time.Now().Add(time.Second)))
		assert.Equal(t, gorilla.CloseNormalClosure, closeCode(t, c))
	})
}

func TestPortForwardIntegration_APIKeyDeletedClosesTunnel(t *testing.T) {
	t.Parallel()
	h := newITHarness(t, itOpts{allowAPIKeys: true})
	c, resp, err := gorilla.DefaultDialer.Dial(h.wsURL(itPath), http.Header{"X-API-Key": []string{"sk_" + h.apiKey}})
	if resp != nil {
		defer resp.Body.Close()
	}
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	echo(t, c, "alive")

	// Deleting or rotating a key calls DisconnectAPIKey (local and fan-out).
	h.hub.DisconnectAPIKey("key1")
	assert.Equal(t, gorilla.ClosePolicyViolation, closeCode(t, c))
}

func TestPortForwardIntegration_ExpiredAccessTokenKeepsTunnelOpen(t *testing.T) {
	t.Parallel()
	h := newITHarness(t, itOpts{})
	c := h.dial(t, h.token(t, "owner", "user", "sid-short", 3*time.Second))
	echo(t, c, "before expiry")

	// Wait until the token is past its expiry and several revalidation runs
	// have happened. The 3 s lifetime leaves the dial and the first echo far
	// from the expiry, also on a slow -race run.
	time.Sleep(3300 * time.Millisecond)

	// The tunnel still works, and a new request with the token is refused.
	echo(t, c, "after expiry")
	status, _ := h.get(t, itPath, http.Header{"Authorization": []string{"Bearer " + h.token(t, "owner", "user", "sid-short", -time.Second).jwt}})
	assert.Equal(t, http.StatusUnauthorized, status)
	assert.Empty(t, h.audit.byAction("port_forward_end"))
}

func TestPortForwardIntegration_DialFailureIs502(t *testing.T) {
	t.Parallel()
	h := newITHarness(t, itOpts{})
	h.fwd.addr = "127.0.0.1:1" // nothing listens
	tok := h.token(t, "owner", "user", "sid-1", time.Hour)

	_, resp, err := gorilla.DefaultDialer.Dial(h.wsURL(itPath), http.Header{"Authorization": []string{"Bearer " + tok.jwt}})
	require.Error(t, err)
	require.NotNil(t, resp)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
	assert.Equal(t, 0, h.audit.count(), "no start entry without a tunnel")
	// The slot is free again: the next dial can use the limit.
	h.fwd.addr = startEchoListener(t)
	c := h.dial(t, tok)
	echo(t, c, "recovered")
}

func TestPortForwardIntegration_TunnelLimit429(t *testing.T) {
	t.Parallel()
	h := newITHarness(t, itOpts{})
	tok := h.token(t, "owner", "user", "sid-1", time.Hour)
	for i := 0; i < 3; i++ {
		echo(t, h.dial(t, tok), "x")
	}
	_, resp, err := gorilla.DefaultDialer.Dial(h.wsURL(itPath), http.Header{"Authorization": []string{"Bearer " + tok.jwt}})
	require.Error(t, err)
	require.NotNil(t, resp)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
}

func TestPortForwardIntegration_ServerShutdownClosesWith1001(t *testing.T) {
	t.Parallel()
	h := newITHarness(t, itOpts{})
	c := h.dial(t, h.token(t, "owner", "user", "sid-1", time.Hour))
	echo(t, c, "alive")

	require.NoError(t, h.mgr.Shutdown(context.Background()))
	assert.Equal(t, gorilla.CloseGoingAway, closeCode(t, c))
	assert.EqualValues(t, 1001, h.endEntry(t)["close_code"])
}

// TestPortForwardIntegration_TokenQueryNeverAuthenticatesNorLogs is not
// parallel: it swaps the default slog handler to capture every log line of the
// chain (the logger, recovery, handler and auth middleware).
func TestPortForwardIntegration_TokenQueryNeverAuthenticatesNorLogs(t *testing.T) {
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	h := newITHarness(t, itOpts{})
	tok := h.token(t, "owner", "user", "sid-1", time.Hour)

	for _, q := range []string{"&token=" + tok.jwt, "&token=", "&token=" + tok.jwt + "&token=" + tok.jwt} {
		status, _ := h.get(t, itPath+q, nil)
		assert.Equal(t, http.StatusUnauthorized, status, q)

		_, resp, err := gorilla.DefaultDialer.Dial(h.wsURL(itPath+q), nil)
		require.Error(t, err)
		require.NotNil(t, resp)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, q)
	}

	// A valid header plus a stray ?token= opens a tunnel; the query value is
	// ignored and not logged.
	c, resp, err := gorilla.DefaultDialer.Dial(h.wsURL(itPath+"&token=stray-secret-value"), http.Header{"Authorization": []string{"Bearer " + tok.jwt}})
	if resp != nil {
		defer resp.Body.Close()
	}
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	echo(t, c, "ok")

	logs := buf.String()
	assert.NotEmpty(t, logs, "the capture must see the request logs")
	assert.NotContains(t, logs, tok.jwt)
	assert.NotContains(t, logs, "stray-secret-value")
	assert.NotContains(t, logs, "token=")
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

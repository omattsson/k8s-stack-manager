package routes

import (
	"context"
	"io"
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
	"backend/internal/websocket"

	"github.com/gin-gonic/gin"
	gorilla "github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type pfInstanceRepo struct {
	models.StackInstanceRepository
	inst *models.StackInstance
}

func (r *pfInstanceRepo) FindByID(_ context.Context, id string) (*models.StackInstance, error) {
	cp := *r.inst
	return &cp, nil
}

// NamespaceShared: one instance, so its namespace is never shared.
func (r *pfInstanceRepo) NamespaceShared(context.Context, string, string) (bool, error) {
	return false, nil
}

func (r *pfInstanceRepo) FindByIDs(_ context.Context, ids []string) (map[string]*models.StackInstance, error) {
	out := make(map[string]*models.StackInstance, len(ids))
	for _, id := range ids {
		cp := *r.inst
		cp.ID = id
		out[id] = &cp
	}
	return out, nil
}

type pfClusterRepo struct{ models.ClusterRepository }

func (pfClusterRepo) StreamFlagsByIDs(_ context.Context, ids []string, includeDefault bool) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = true
	}
	if includeDefault {
		out[models.DefaultClusterKey] = true
	}
	return out, nil
}

func (pfClusterRepo) FindByID(_ context.Context, id string) (*models.Cluster, error) {
	return &models.Cluster{ID: id, AllowPortForward: true}, nil
}

type pfStream struct {
	sess, pod net.Conn
	errR      *io.PipeReader
	errW      *io.PipeWriter
	once      sync.Once
}

func (s *pfStream) Data() io.ReadWriteCloser { return s.sess }
func (s *pfStream) Errors() io.Reader        { return s.errR }
func (s *pfStream) Close() error {
	s.once.Do(func() { _ = s.sess.Close(); _ = s.pod.Close(); _ = s.errW.Close(); _ = s.errR.Close() })
	return nil
}

type pfForwarder struct{}

func (pfForwarder) CheckStreamNamespace(context.Context, string, string) error { return nil }
func (pfForwarder) ResolveServiceTarget(_ context.Context, _, svc string, port int32) (*k8s.ServiceTarget, error) {
	return &k8s.ServiceTarget{Service: svc, Pod: "p-0", ServicePort: port, TargetPort: port}, nil
}
func (pfForwarder) DialPodPort(context.Context, string, string, int32) (k8s.PodPortStream, error) {
	a, b := net.Pipe()
	r, w := io.Pipe()
	go func() { _, _ = io.Copy(b, b) }()
	return &pfStream{sess: a, pod: b, errR: r, errW: w}, nil
}

type pfAudit struct {
	mu      sync.Mutex
	actions []string
}

func (a *pfAudit) Create(_ context.Context, l *models.AuditLog) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.actions = append(a.actions, l.Action)
	return nil
}
func (a *pfAudit) list() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.actions...)
}

// TestSetupRoutes_PortForwardThroughMiddleware opens a tunnel through the real
// middleware chain (logger, recovery, metrics wrappers, CORS, body limit, rate
// limit, JWT auth, audit) on a server with short read and write timeouts, and
// checks that the tunnel outlives them, that ?token= is refused, and that the
// audit middleware does not double-write.
func TestSetupRoutes_PortForwardThroughMiddleware(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	cfg := testConfig()
	cfg.Stream = config.StreamConfig{PortForward: config.PortForwardConfig{
		Enabled: true, MaxDuration: time.Minute, IdleTimeout: time.Minute, MaxPerUser: 2,
	}}
	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(hub.Shutdown)
	mgr := podstream.NewManager(podstream.Config{MaxPerUser: 2, Timings: podstream.Timings{PingInterval: 100 * time.Millisecond}})
	t.Cleanup(func() { _ = mgr.Shutdown(context.Background()) })
	audit := &pfAudit{}

	sh := handlers.NewStreamHandler(handlers.StreamDeps{
		InstanceRepo: &pfInstanceRepo{inst: &models.StackInstance{ID: "i1", OwnerID: "u1", Namespace: "ns", ClusterID: "c1", Status: models.StackStatusRunning}},
		ClusterRepo:  pfClusterRepo{},
		Provider:     func(string) (k8s.PortForwarder, error) { return pfForwarder{}, nil },
		Manager:      mgr, Hub: hub, Audit: audit, AllowedOrigins: "*", Config: cfg.Stream,
	})

	router := gin.New()
	healthChecker := health.New()
	rl := SetupRoutes(router, Deps{
		HealthChecker: healthChecker, Config: cfg, Hub: hub,
		AuthHandler:     handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth, &cfg.OIDC),
		InstanceHandler: &handlers.InstanceHandler{},
		StreamHandler:   sh,
		AuditLogger:     audit,
	})
	t.Cleanup(rl.Stop)

	srv := httptest.NewUnstartedServer(router)
	srv.Config.ReadTimeout = 200 * time.Millisecond
	srv.Config.WriteTimeout = 200 * time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)
	base := "ws" + strings.TrimPrefix(srv.URL, "http")
	url := base + "/api/v1/stack-instances/i1/port-forward?service=web&port=80"

	token, err := middleware.GenerateToken("u1", "alice", "user", cfg.Auth.JWTSecret, time.Hour)
	require.NoError(t, err)

	// ?token= alone does not authenticate.
	_, resp, err := gorilla.DefaultDialer.Dial(url+"&token="+token, nil)
	require.Error(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// Header auth opens the tunnel and it outlives the server timeouts.
	c, _, err := gorilla.DefaultDialer.Dial(url, http.Header{"Authorization": []string{"Bearer " + token}})
	require.NoError(t, err)
	defer c.Close()
	done := make(chan struct{})
	var got []byte
	go func() {
		defer close(done)
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			mt, data, rerr := c.ReadMessage()
			if rerr != nil {
				return
			}
			if mt == gorilla.BinaryMessage {
				got = data
				return
			}
		}
	}()
	time.Sleep(700 * time.Millisecond)
	require.NoError(t, c.WriteMessage(gorilla.BinaryMessage, []byte("after the timeouts")))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("no echo")
	}
	assert.Equal(t, "after the timeouts", string(got))

	// The instance delete path closes it.
	assert.Equal(t, 1, hub.DisconnectInstance("i1"))
	require.Eventually(t, func() bool {
		a := audit.list()
		return len(a) == 2 && a[0] == "port_forward" && a[1] == "port_forward_end"
	}, 3*time.Second, 20*time.Millisecond, "%v", audit.list())
}

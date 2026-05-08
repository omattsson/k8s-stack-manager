package main

import (
	"backend/internal/api/handlers"
	"backend/internal/cluster"
	"backend/internal/config"
	"backend/internal/database"
	"backend/internal/deployer"
	"backend/internal/gitprovider"
	"backend/internal/health"
	"backend/internal/helm"
	"backend/internal/hooks"
	"backend/internal/k8s"
	"backend/internal/models"
	"backend/internal/notifier"
	"backend/internal/scheduler"
	"backend/internal/sessionstore"
	"backend/internal/ttl"
	"backend/internal/websocket"
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// ---- buildSessionStore tests ----

func TestBuildSessionStore(t *testing.T) {
	t.Parallel()

	// Open an in-memory SQLite DB so NewMySQLStore gets a valid *gorm.DB
	// (it starts a cleanup goroutine that references db).
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)

	// Auto-migrate the session_entries table so the cleanup goroutine
	// won't error when it tries to delete expired rows.
	require.NoError(t, db.AutoMigrate(&sessionstore.SessionEntry{}))

	tests := []struct {
		name        string
		backend     string
		wantType    string
		wantMemory  bool // true → expect *MemoryStore; false → expect *MySQLStore
	}{
		{
			name:       "memory backend returns MemoryStore",
			backend:    "memory",
			wantType:   "*sessionstore.MemoryStore",
			wantMemory: true,
		},
		{
			name:       "mysql backend returns MySQLStore",
			backend:    "mysql",
			wantType:   "*sessionstore.MySQLStore",
			wantMemory: false,
		},
		{
			name:       "empty string (default) returns MySQLStore",
			backend:    "",
			wantType:   "*sessionstore.MySQLStore",
			wantMemory: false,
		},
		{
			name:       "unrecognised backend falls through to MySQLStore",
			backend:    "redis",
			wantType:   "*sessionstore.MySQLStore",
			wantMemory: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var store sessionstore.SessionStore
			if tt.wantMemory {
				store = buildSessionStore(tt.backend, nil) // db not needed for MemoryStore
			} else {
				store = buildSessionStore(tt.backend, db)
			}
			t.Cleanup(func() { store.Stop() })

			require.NotNil(t, store)

			if tt.wantMemory {
				_, ok := store.(*sessionstore.MemoryStore)
				assert.True(t, ok, "expected *sessionstore.MemoryStore, got %T", store)
			} else {
				_, ok := store.(*sessionstore.MySQLStore)
				assert.True(t, ok, "expected *sessionstore.MySQLStore, got %T", store)
			}

			// Verify the returned value satisfies the SessionStore interface.
			var _ sessionstore.SessionStore = store
		})
	}
}

func TestBuildSessionStore_MemoryNilDB(t *testing.T) {
	t.Parallel()

	// Memory backend should work fine even when db is nil.
	store := buildSessionStore("memory", nil)
	t.Cleanup(func() { store.Stop() })

	require.NotNil(t, store)
	_, ok := store.(*sessionstore.MemoryStore)
	assert.True(t, ok)

	// Smoke-test: basic operations should succeed.
	ctx := context.Background()
	err := store.BlockToken(ctx, "test-jti", time.Now().Add(time.Hour))
	assert.NoError(t, err)

	blocked, err := store.IsTokenBlocked(ctx, "test-jti")
	assert.NoError(t, err)
	assert.True(t, blocked)
}

// ---- struct field compile-time type assertions ----
//
// These tests verify that the bootstrap structs expose the expected fields
// with the expected types. A compilation failure here means a field was
// renamed, removed, or had its type changed.

func TestDomainServicesFieldTypes(t *testing.T) {
	t.Parallel()

	var ds domainServices

	// Each assignment is a compile-time check that the field exists and
	// has the expected type. We use pointers so the zero value works.
	var _ *gitprovider.Registry = ds.GitRegistry
	var _ *cluster.Registry = ds.ClusterRegistry
	var _ *cluster.HealthPoller = ds.HealthPoller
	var _ *cluster.SecretRefresher = ds.SecretRefresher
	var _ *k8s.Watcher = ds.K8sWatcher
	var _ *deployer.Manager = ds.DeployManager
	var _ *hooks.Dispatcher = ds.HookDispatcher
	var _ *hooks.ActionRegistry = ds.ActionRegistry
	var _ *notifier.Notifier = ds.LifecycleNotifier
	var _ *deployer.CleanupExecutor = ds.CleanupExecutor
	var _ *scheduler.Scheduler = ds.CleanupScheduler
	var _ *helm.ValuesGenerator = ds.ValuesGen
	var _ context.CancelFunc = ds.WatcherCancel
}

func TestHandlerSetFieldTypes(t *testing.T) {
	t.Parallel()

	var hs handlerSet

	var _ *handlers.AuthHandler = hs.Auth
	var _ *handlers.OIDCHandler = hs.OIDC
	var _ *handlers.TemplateHandler = hs.Template
	var _ *handlers.DefinitionHandler = hs.Definition
	var _ *handlers.TemplateVersionHandler = hs.TemplateVersion
	var _ *handlers.InstanceHandler = hs.Instance
	var _ *handlers.GitHandler = hs.Git
	var _ *handlers.AuditLogHandler = hs.AuditLog
	var _ *handlers.UserHandler = hs.User
	var _ *handlers.APIKeyHandler = hs.APIKey
	var _ *handlers.AdminHandler = hs.Admin
	var _ *handlers.ClusterHandler = hs.Cluster
	var _ *handlers.BranchOverrideHandler = hs.BranchOverride
	var _ *handlers.InstanceQuotaOverrideHandler = hs.InstanceQuotaOverride
	var _ *handlers.SharedValuesHandler = hs.SharedValues
	var _ *handlers.NotificationHandler = hs.Notification
	var _ *handlers.FavoriteHandler = hs.Favorite
	var _ *handlers.QuickDeployHandler = hs.QuickDeploy
	var _ *handlers.AnalyticsHandler = hs.Analytics
	var _ *handlers.DashboardHandler = hs.Dashboard
	var _ *handlers.CleanupPolicyHandler = hs.CleanupPolicy
}

func TestRouterDepsFieldTypes(t *testing.T) {
	t.Parallel()

	var rd routerDeps

	var _ models.Repository = rd.Repo
	var _ *health.HealthChecker = rd.HealthChecker
	var _ *websocket.Hub = rd.Hub
	var _ sessionstore.SessionStore = rd.SessionStore
	var _ *database.RepositorySet = rd.Repos
	var _ *domainServices = rd.Svc
}

func TestBackgroundServicesFieldTypes(t *testing.T) {
	t.Parallel()

	var bg backgroundServices

	var _ *ttl.Reaper = bg.Reaper
	var _ *ttl.Warner = bg.ExpiryWarner
	var _ *cluster.QuotaMonitor = bg.QuotaMonitor
	var _ *cluster.SecretMonitor = bg.SecretMonitor
	var _ context.CancelFunc = bg.RefreshTokenCleanupCancel
}

// ---- startHTTPServer tests ----

func TestStartHTTPServer_ListensOnConfiguredAddress(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/ping", func(c *gin.Context) {
		c.String(http.StatusOK, "pong")
	})

	// Pick a free port.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())

	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:         "127.0.0.1",
			Port:         fmt.Sprintf("%d", port),
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
			IdleTimeout:  30 * time.Second,
		},
	}

	srv := startHTTPServer(router, cfg)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	// Give the server a moment to start listening.
	time.Sleep(50 * time.Millisecond)

	// Verify the server is reachable and serves the expected response.
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/ping", port))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestStartHTTPServer_PprofEnabled(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	router := gin.New()

	// Pick two free ports.
	listener1, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	mainPort := listener1.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener1.Close())

	listener2, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	pprofPort := listener2.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener2.Close())

	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:         "127.0.0.1",
			Port:         fmt.Sprintf("%d", mainPort),
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
			IdleTimeout:  30 * time.Second,
			PprofEnabled: true,
			PprofAddr:    fmt.Sprintf("127.0.0.1:%d", pprofPort),
		},
	}

	srv := startHTTPServer(router, cfg)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	time.Sleep(100 * time.Millisecond)

	// Verify the pprof endpoint is reachable.
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/debug/pprof/", pprofPort))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestStartHTTPServer_PprofDisabled(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	router := gin.New()

	// Pick two free ports — one for main server, one for pprof.
	listener1, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	mainPort := listener1.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener1.Close())

	listener2, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	pprofPort := listener2.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener2.Close())

	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:         "127.0.0.1",
			Port:         fmt.Sprintf("%d", mainPort),
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
			IdleTimeout:  30 * time.Second,
			PprofEnabled: false,
			PprofAddr:    fmt.Sprintf("127.0.0.1:%d", pprofPort),
		},
	}

	srv := startHTTPServer(router, cfg)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	time.Sleep(100 * time.Millisecond)

	// The pprof endpoint should NOT be listening when disabled.
	client := &http.Client{Timeout: 500 * time.Millisecond}
	_, err = client.Get(fmt.Sprintf("http://127.0.0.1:%d/debug/pprof/", pprofPort))
	assert.Error(t, err, "pprof server should not be running when PprofEnabled=false")
}

func TestStartHTTPServer_ReturnsValidServer(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	router := gin.New()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())

	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:         "127.0.0.1",
			Port:         fmt.Sprintf("%d", port),
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 30 * time.Second,
			IdleTimeout:  60 * time.Second,
		},
	}

	srv := startHTTPServer(router, cfg)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	// Verify the returned *http.Server has correct configuration.
	assert.Equal(t, fmt.Sprintf("127.0.0.1:%d", port), srv.Addr)
	assert.Equal(t, 15*time.Second, srv.ReadTimeout)
	assert.Equal(t, 30*time.Second, srv.WriteTimeout)
	assert.Equal(t, 60*time.Second, srv.IdleTimeout)
	assert.Equal(t, router, srv.Handler)
}

// ---- initDatabase / initRepositories ----
//
// These functions require a real MySQL connection, so we skip them in short
// mode. The tests are intentionally present to document the expected signatures
// and serve as a reminder that integration tests exist elsewhere.

func TestInitDatabase_RequiresMySQL(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: initDatabase requires a real MySQL connection")
	}
}

func TestInitRepositories_RequiresMySQL(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: initRepositories requires a real MySQL connection")
	}
}

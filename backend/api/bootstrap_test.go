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
	"backend/internal/leader"
	"backend/internal/models"
	"backend/internal/notifier"
	"backend/internal/scheduler"
	"backend/internal/sessionstore"
	"backend/internal/telemetry"
	"backend/internal/ttl"
	"backend/internal/websocket"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
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
		name       string
		backend    string
		wantMemory bool // true → expect *MemoryStore; false → expect *MySQLStore
	}{
		{
			name:       "memory backend returns MemoryStore",
			backend:    "memory",
			wantMemory: true,
		},
		{
			name:       "mysql backend returns MySQLStore",
			backend:    "mysql",
			wantMemory: false,
		},
		{
			name:       "empty string (default) returns MySQLStore",
			backend:    "",
			wantMemory: false,
		},
		{
			name:       "unrecognised backend falls through to MySQLStore",
			backend:    "redis",
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

	var _ *health.HealthChecker = rd.HealthChecker
	var _ *websocket.Hub = rd.Hub
	var _ sessionstore.SessionStore = rd.SessionStore
	var _ *database.RepositorySet = rd.Repos
	var _ *domainServices = rd.Svc
}

func TestLeaderWorkersFieldTypes(t *testing.T) {
	t.Parallel()

	var lw leaderWorkers

	var _ *leader.Group = lw.Group
	var _ *ttl.Reaper = lw.Reaper
	var _ *ttl.Warner = lw.ExpiryWarner
	var _ *cluster.QuotaMonitor = lw.QuotaMonitor
	var _ *cluster.SecretMonitor = lw.SecretMonitor
}

// ---- startHTTPServer tests ----
//
// The servers bind port 0 and the tests read the bound address from Addr, so
// no test picks a port that another server can take first (#662).

func waitForServer(t *testing.T, addr string) {
	t.Helper()
	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err != nil {
			return false
		}
		conn.Close()
		return true
	}, 2*time.Second, 10*time.Millisecond, "server at %s did not become ready", addr)
}

func shutdownServers(t *testing.T, s *servers) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.Main.Shutdown(ctx)
	if s.Pprof != nil {
		_ = s.Pprof.Shutdown(ctx)
	}
	if s.Metrics != nil {
		_ = s.Metrics.Shutdown(ctx)
	}
}

// testServerConfig returns a server config on 127.0.0.1 port 0.
func testServerConfig() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Host:         "127.0.0.1",
			Port:         "0",
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
			IdleTimeout:  30 * time.Second,
		},
	}
}

// startTestServers starts the servers of cfg and stops them at the end of the test.
func startTestServers(t *testing.T, router *gin.Engine, cfg *config.Config, tel *telemetry.Telemetry) *servers {
	t.Helper()
	s, err := startHTTPServer(router, cfg, tel)
	require.NoError(t, err)
	t.Cleanup(func() { shutdownServers(t, s) })
	return s
}

// boundPort returns the port of a bound server address and checks that it
// is a real port on 127.0.0.1.
func boundPort(t *testing.T, addr string) int {
	t.Helper()
	tcpAddr, err := net.ResolveTCPAddr("tcp", addr)
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", tcpAddr.IP.String())
	assert.NotZero(t, tcpAddr.Port, "Addr holds the bound port")
	return tcpAddr.Port
}

func TestStartHTTPServer_ListensOnConfiguredAddress(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/ping", func(c *gin.Context) {
		c.String(http.StatusOK, "pong")
	})

	s := startTestServers(t, router, testServerConfig(), &telemetry.Telemetry{})
	boundPort(t, s.Main.Addr)

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s/ping", s.Main.Addr))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestStartHTTPServer_PprofEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := testServerConfig()
	cfg.Server.PprofEnabled = true
	cfg.Server.PprofAddr = "127.0.0.1:0"

	s := startTestServers(t, gin.New(), cfg, &telemetry.Telemetry{})

	require.NotNil(t, s.Pprof, "pprof server should be set when enabled")
	assert.NotEqual(t, boundPort(t, s.Main.Addr), boundPort(t, s.Pprof.Addr))
	waitForServer(t, s.Pprof.Addr)

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s/debug/pprof/", s.Pprof.Addr))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestStartHTTPServer_PprofDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)

	s := startTestServers(t, gin.New(), testServerConfig(), &telemetry.Telemetry{})

	assert.Nil(t, s.Pprof, "pprof server should be nil when disabled")
}

func TestStartHTTPServer_ReturnsValidServer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	cfg := testServerConfig()
	cfg.Server.ReadTimeout = 15 * time.Second
	cfg.Server.WriteTimeout = 30 * time.Second
	cfg.Server.IdleTimeout = 60 * time.Second

	s := startTestServers(t, router, cfg, &telemetry.Telemetry{})

	boundPort(t, s.Main.Addr)
	assert.Equal(t, 15*time.Second, s.Main.ReadTimeout)
	assert.Equal(t, 30*time.Second, s.Main.WriteTimeout)
	assert.Equal(t, 60*time.Second, s.Main.IdleTimeout)
	assert.Equal(t, router, s.Main.Handler)
}

func TestStartHTTPServer_MetricsEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := testServerConfig()
	cfg.Otel = config.OtelConfig{
		MetricsEnabled: true,
		MetricsAddr:    "127.0.0.1:0",
	}

	tel, err := telemetry.Init(cfg.Otel)
	require.NoError(t, err)
	t.Cleanup(func() {
		if tel != nil {
			_ = tel.Shutdown(context.Background())
		}
	})

	s := startTestServers(t, gin.New(), cfg, tel)

	require.NotNil(t, s.Metrics, "metrics server should be set when enabled")
	assert.NotEqual(t, boundPort(t, s.Main.Addr), boundPort(t, s.Metrics.Addr))
	waitForServer(t, s.Metrics.Addr)

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s/metrics", s.Metrics.Addr))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestStartHTTPServer_BindFailureReturnsError checks that a main server
// address in use is an error for the caller, not an exit of the process
// (#662).
func TestStartHTTPServer_BindFailureReturnsError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = taken.Close() })
	cfg := testServerConfig()
	cfg.Server.Port = fmt.Sprintf("%d", taken.Addr().(*net.TCPAddr).Port)

	s, err := startHTTPServer(gin.New(), cfg, &telemetry.Telemetry{})
	if s != nil {
		t.Cleanup(func() { shutdownServers(t, s) })
	}

	require.Error(t, err)
	assert.Nil(t, s)
}

// TestStartHTTPServer_OptionalBindFailureKeepsMain checks that a pprof
// address in use is logged only: the main server still serves.
func TestStartHTTPServer_OptionalBindFailureKeepsMain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = taken.Close() })
	cfg := testServerConfig()
	cfg.Server.PprofEnabled = true
	cfg.Server.PprofAddr = taken.Addr().String()

	s := startTestServers(t, gin.New(), cfg, &telemetry.Telemetry{})

	waitForServer(t, s.Main.Addr)
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

// ============================================================================
// Stub repository implementations for bootstrap wiring tests.
// Each stub satisfies its respective interface with no-op methods returning
// zero values. These are only used to satisfy constructors — no handler logic
// is exercised.
// ============================================================================

// ---- stubClusterRepo ----

type stubClusterRepo struct{}

func (s *stubClusterRepo) Create(ctx context.Context, _ *models.Cluster) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}
func (s *stubClusterRepo) FindByID(ctx context.Context, _ string) (*models.Cluster, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, nil
}
func (s *stubClusterRepo) Update(ctx context.Context, _ *models.Cluster) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}
func (s *stubClusterRepo) UpdateHealthStatus(ctx context.Context, _, _ string) error {
	return ctx.Err()
}
func (s *stubClusterRepo) Delete(ctx context.Context, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}
func (s *stubClusterRepo) List(ctx context.Context) ([]models.Cluster, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, nil
}
func (s *stubClusterRepo) FindDefault(ctx context.Context) (*models.Cluster, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, nil
}
func (s *stubClusterRepo) SetDefault(ctx context.Context, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// ---- stubStackInstanceRepo ----

type stubStackInstanceRepo struct{}

func (s *stubStackInstanceRepo) Create(ctx context.Context, _ *models.StackInstance) error {
	return ctx.Err()
}
func (s *stubStackInstanceRepo) FindByID(ctx context.Context, _ string) (*models.StackInstance, error) {
	return nil, ctx.Err()
}
func (s *stubStackInstanceRepo) FindByNamespace(ctx context.Context, _ string) (*models.StackInstance, error) {
	return nil, ctx.Err()
}
func (s *stubStackInstanceRepo) Update(ctx context.Context, _ *models.StackInstance) error {
	return ctx.Err()
}
func (s *stubStackInstanceRepo) Delete(ctx context.Context, _ string) error { return ctx.Err() }
func (s *stubStackInstanceRepo) List(ctx context.Context) ([]models.StackInstance, error) {
	return nil, ctx.Err()
}
func (s *stubStackInstanceRepo) ListPaged(ctx context.Context, _ models.StackInstanceFilter, _, _ int) ([]models.StackInstance, int, error) {
	return nil, 0, ctx.Err()
}
func (s *stubStackInstanceRepo) ListByOwner(ctx context.Context, _ string) ([]models.StackInstance, error) {
	return nil, ctx.Err()
}
func (s *stubStackInstanceRepo) FindByName(ctx context.Context, _ string) ([]models.StackInstance, error) {
	return nil, ctx.Err()
}
func (s *stubStackInstanceRepo) FindByCluster(ctx context.Context, _ string) ([]models.StackInstance, error) {
	return nil, ctx.Err()
}
func (s *stubStackInstanceRepo) CountByClusterAndOwner(ctx context.Context, _, _ string) (int, error) {
	return 0, ctx.Err()
}
func (s *stubStackInstanceRepo) CountAll(ctx context.Context) (int, error) { return 0, ctx.Err() }
func (s *stubStackInstanceRepo) CountByStatus(ctx context.Context, _ string) (int, error) {
	return 0, ctx.Err()
}
func (s *stubStackInstanceRepo) CountByDefinitionIDs(ctx context.Context, _ []string) (map[string]int, error) {
	return nil, ctx.Err()
}
func (s *stubStackInstanceRepo) CountByOwnerIDs(ctx context.Context, _ []string) (map[string]int, error) {
	return nil, ctx.Err()
}
func (s *stubStackInstanceRepo) ListIDsByDefinitionIDs(ctx context.Context, _ []string) (map[string][]string, error) {
	return nil, ctx.Err()
}
func (s *stubStackInstanceRepo) ListIDsByOwnerIDs(ctx context.Context, _ []string) (map[string][]string, error) {
	return nil, ctx.Err()
}
func (s *stubStackInstanceRepo) ExistsByDefinitionAndStatus(ctx context.Context, _, _ string) (bool, error) {
	return false, ctx.Err()
}
func (s *stubStackInstanceRepo) ListExpired(ctx context.Context) ([]*models.StackInstance, error) {
	return nil, ctx.Err()
}
func (s *stubStackInstanceRepo) ListExpiringSoon(ctx context.Context, _ time.Duration) ([]*models.StackInstance, error) {
	return nil, ctx.Err()
}
func (*stubStackInstanceRepo) MarkExpiryWarned(ctx context.Context, _ string, _, _ time.Time) (bool, error) {
	return false, ctx.Err()
}
func (s *stubStackInstanceRepo) ListByStatus(ctx context.Context, _ string, _ int) ([]*models.StackInstance, error) {
	return nil, ctx.Err()
}

// ---- stubStackDefinitionRepo ----

type stubStackDefinitionRepo struct{}

func (s *stubStackDefinitionRepo) Create(ctx context.Context, _ *models.StackDefinition) error {
	return ctx.Err()
}
func (s *stubStackDefinitionRepo) FindByID(ctx context.Context, _ string) (*models.StackDefinition, error) {
	return nil, ctx.Err()
}
func (s *stubStackDefinitionRepo) FindByName(ctx context.Context, _ string) ([]models.StackDefinition, error) {
	return nil, ctx.Err()
}
func (s *stubStackDefinitionRepo) Update(ctx context.Context, _ *models.StackDefinition) error {
	return ctx.Err()
}
func (s *stubStackDefinitionRepo) SetTemplateLink(ctx context.Context, _, _, _ string) error {
	return ctx.Err()
}
func (s *stubStackDefinitionRepo) Delete(ctx context.Context, _ string) error { return ctx.Err() }
func (s *stubStackDefinitionRepo) List(ctx context.Context) ([]models.StackDefinition, error) {
	return nil, ctx.Err()
}
func (s *stubStackDefinitionRepo) ListPaged(ctx context.Context, _ models.StackDefinitionFilter, _, _ int) ([]models.StackDefinition, int64, error) {
	return nil, 0, ctx.Err()
}
func (s *stubStackDefinitionRepo) ListByOwner(ctx context.Context, _ string) ([]models.StackDefinition, error) {
	return nil, ctx.Err()
}
func (s *stubStackDefinitionRepo) ListByTemplate(ctx context.Context, _ string) ([]models.StackDefinition, error) {
	return nil, ctx.Err()
}
func (s *stubStackDefinitionRepo) CountByTemplateIDs(ctx context.Context, _ []string) (map[string]int, error) {
	return nil, ctx.Err()
}
func (s *stubStackDefinitionRepo) FindByIDForUpdate(ctx context.Context, _ string) (*models.StackDefinition, error) {
	return nil, ctx.Err()
}
func (s *stubStackDefinitionRepo) ListIDsByTemplateIDs(ctx context.Context, _ []string) (map[string][]string, error) {
	return nil, ctx.Err()
}
func (s *stubStackDefinitionRepo) Count(ctx context.Context) (int64, error) { return 0, ctx.Err() }
func (s *stubStackDefinitionRepo) NamesByIDs(ctx context.Context, _ []string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return map[string]string{}, nil
}

// ---- stubChartConfigRepo ----

type stubChartConfigRepo struct{}

func (s *stubChartConfigRepo) Create(ctx context.Context, _ *models.ChartConfig) error {
	return ctx.Err()
}
func (s *stubChartConfigRepo) FindByID(ctx context.Context, _ string) (*models.ChartConfig, error) {
	return nil, ctx.Err()
}
func (s *stubChartConfigRepo) Update(ctx context.Context, _ *models.ChartConfig) error {
	return ctx.Err()
}
func (s *stubChartConfigRepo) Delete(ctx context.Context, _ string) error { return ctx.Err() }
func (s *stubChartConfigRepo) DeleteByDefinition(ctx context.Context, _ string) error {
	return ctx.Err()
}
func (s *stubChartConfigRepo) ListByDefinition(ctx context.Context, _ string) ([]models.ChartConfig, error) {
	return nil, ctx.Err()
}
func (s *stubChartConfigRepo) CountByDefinitionIDs(ctx context.Context, _ []string) (map[string]int, error) {
	return nil, ctx.Err()
}

// ---- stubDeploymentLogRepo ----

type stubDeploymentLogRepo struct{}

func (s *stubDeploymentLogRepo) Create(_ context.Context, _ *models.DeploymentLog) error { return nil }
func (s *stubDeploymentLogRepo) FindByID(_ context.Context, _ string) (*models.DeploymentLog, error) {
	return nil, nil
}
func (s *stubDeploymentLogRepo) Update(_ context.Context, _ *models.DeploymentLog) error { return nil }
func (s *stubDeploymentLogRepo) ListByInstance(_ context.Context, _ string) ([]models.DeploymentLog, error) {
	return nil, nil
}
func (s *stubDeploymentLogRepo) ListByInstancePaginated(_ context.Context, _ models.DeploymentLogFilters) (*models.DeploymentLogResult, error) {
	return nil, nil
}
func (s *stubDeploymentLogRepo) ListLatestByActions(_ context.Context, _ string, _ []string, _ int) ([]models.DeploymentLog, error) {
	return nil, nil
}
func (s *stubDeploymentLogRepo) GetLatestByInstance(_ context.Context, _ string) (*models.DeploymentLog, error) {
	return nil, nil
}
func (s *stubDeploymentLogRepo) SummarizeByInstance(_ context.Context, _ string) (*models.DeployLogSummary, error) {
	return nil, nil
}
func (s *stubDeploymentLogRepo) SummarizeBatch(_ context.Context, _ []string) (map[string]*models.DeployLogSummary, error) {
	return nil, nil
}

func (s *stubDeploymentLogRepo) SummarizeByUsers(_ context.Context, _ []string) (map[string]*models.DeployLogSummary, error) {
	return nil, nil
}
func (s *stubDeploymentLogRepo) CountByAction(_ context.Context, _ string) (int, error) {
	return 0, nil
}
func (s *stubDeploymentLogRepo) ListRecentGlobal(_ context.Context, _ int) ([]models.DeploymentLogWithContext, error) {
	return nil, nil
}

// ---- stubAuditLogRepo ----

type stubAuditLogRepo struct{}

func (s *stubAuditLogRepo) Create(ctx context.Context, _ *models.AuditLog) error { return nil }
func (s *stubAuditLogRepo) List(ctx context.Context, _ models.AuditLogFilters) (*models.AuditLogResult, error) {
	return nil, nil
}

// ---- stubResourceQuotaRepo ----

type stubResourceQuotaRepo struct{}

func (s *stubResourceQuotaRepo) GetByClusterID(_ context.Context, _ string) (*models.ResourceQuotaConfig, error) {
	return nil, nil
}
func (s *stubResourceQuotaRepo) Upsert(_ context.Context, _ *models.ResourceQuotaConfig) error {
	return nil
}
func (s *stubResourceQuotaRepo) Delete(_ context.Context, _ string) error { return nil }

// ---- stubInstanceQuotaOverrideRepo ----

type stubInstanceQuotaOverrideRepo struct{}

func (s *stubInstanceQuotaOverrideRepo) GetByInstanceID(_ context.Context, _ string) (*models.InstanceQuotaOverride, error) {
	return nil, nil
}
func (s *stubInstanceQuotaOverrideRepo) Upsert(_ context.Context, _ *models.InstanceQuotaOverride) error {
	return nil
}
func (s *stubInstanceQuotaOverrideRepo) Delete(_ context.Context, _ string) error { return nil }
func (s *stubInstanceQuotaOverrideRepo) ListByInstanceIDs(_ context.Context, _ []string) ([]models.InstanceQuotaOverride, error) {
	return nil, nil
}

// ---- stubCleanupPolicyRepo ----

type stubCleanupPolicyRepo struct{}

func (s *stubCleanupPolicyRepo) Create(ctx context.Context, _ *models.CleanupPolicy) error {
	return nil
}
func (s *stubCleanupPolicyRepo) FindByID(_ context.Context, _ string) (*models.CleanupPolicy, error) {
	return nil, nil
}
func (s *stubCleanupPolicyRepo) Update(ctx context.Context, _ *models.CleanupPolicy) error {
	return nil
}
func (s *stubCleanupPolicyRepo) Delete(_ context.Context, _ string) error { return nil }
func (s *stubCleanupPolicyRepo) List(ctx context.Context) ([]models.CleanupPolicy, error) {
	return nil, nil
}
func (s *stubCleanupPolicyRepo) ListEnabled(ctx context.Context) ([]models.CleanupPolicy, error) {
	return nil, nil
}

// ---- stubNotificationRepo ----

type stubNotificationRepo struct{}

func (s *stubNotificationRepo) Create(_ context.Context, _ *models.Notification) error { return nil }
func (s *stubNotificationRepo) ListByUser(_ context.Context, _ string, _ bool, _, _ int) ([]models.Notification, int64, error) {
	return nil, 0, nil
}
func (s *stubNotificationRepo) CountUnread(_ context.Context, _ string) (int64, error) { return 0, nil }
func (s *stubNotificationRepo) MarkAsRead(_ context.Context, _, _ string) error        { return nil }
func (s *stubNotificationRepo) MarkAllAsRead(_ context.Context, _ string) error        { return nil }
func (s *stubNotificationRepo) GetPreferences(_ context.Context, _ string) ([]models.NotificationPreference, error) {
	return nil, nil
}
func (s *stubNotificationRepo) DisabledUserIDs(_ context.Context, _ string, _ []string) (map[string]bool, error) {
	return map[string]bool{}, nil
}
func (s *stubNotificationRepo) UpdatePreference(_ context.Context, _ *models.NotificationPreference) error {
	return nil
}

// ---- stubUserRepo ----

type stubUserRepo struct{}

func (s *stubUserRepo) Create(_ context.Context, _ *models.User) error             { return nil }
func (s *stubUserRepo) FindByID(_ context.Context, _ string) (*models.User, error) { return nil, nil }
func (s *stubUserRepo) FindByIDs(_ context.Context, _ []string) (map[string]*models.User, error) {
	return nil, nil
}
func (s *stubUserRepo) FindByUsername(_ context.Context, _ string) (*models.User, error) {
	return nil, nil
}
func (s *stubUserRepo) FindByExternalID(_ context.Context, _, _ string) (*models.User, error) {
	return nil, nil
}
func (s *stubUserRepo) Update(_ *models.User) error                   { return nil }
func (s *stubUserRepo) Delete(_ context.Context, _ string) error      { return nil }
func (s *stubUserRepo) List(_ context.Context) ([]models.User, error) { return nil, nil }
func (s *stubUserRepo) Count(_ context.Context) (int64, error)        { return 0, nil }
func (s *stubUserRepo) ListByRoles(_ context.Context, _ []string) ([]models.User, error) {
	return nil, nil
}
func (s *stubUserRepo) UpdateRole(_ context.Context, _, _, _ string) (string, error) { return "", nil }
func (s *stubUserRepo) SetDisabled(_ context.Context, _, _ string, _ bool) error     { return nil }
func (s *stubUserRepo) DeleteGuarded(_ context.Context, _, _ string) error           { return nil }
func (s *stubUserRepo) UpdatePassword(_ context.Context, _, _ string) error          { return nil }
func (s *stubUserRepo) UpdateProfile(_ context.Context, _ string, _ models.UserProfileUpdate) error {
	return nil
}

// ---- stubStackTemplateRepo ----

type stubStackTemplateRepo struct{}

func (s *stubStackTemplateRepo) Create(ctx context.Context, _ *models.StackTemplate) error {
	return ctx.Err()
}
func (s *stubStackTemplateRepo) FindByID(ctx context.Context, _ string) (*models.StackTemplate, error) {
	return nil, ctx.Err()
}
func (s *stubStackTemplateRepo) FindByIDForUpdate(ctx context.Context, _ string) (*models.StackTemplate, error) {
	return nil, ctx.Err()
}
func (s *stubStackTemplateRepo) Update(ctx context.Context, _ *models.StackTemplate) error {
	return ctx.Err()
}
func (s *stubStackTemplateRepo) Delete(ctx context.Context, _ string) error { return ctx.Err() }
func (s *stubStackTemplateRepo) List(ctx context.Context) ([]models.StackTemplate, error) {
	return nil, ctx.Err()
}
func (s *stubStackTemplateRepo) ListPaged(ctx context.Context, _, _ int, _ string) ([]models.StackTemplate, int64, error) {
	return nil, 0, ctx.Err()
}
func (s *stubStackTemplateRepo) ListPublished(ctx context.Context) ([]models.StackTemplate, error) {
	return nil, ctx.Err()
}
func (s *stubStackTemplateRepo) ListPublishedPaged(ctx context.Context, _, _ int, _ string) ([]models.StackTemplate, int64, error) {
	return nil, 0, ctx.Err()
}
func (s *stubStackTemplateRepo) ListByOwner(ctx context.Context, _ string) ([]models.StackTemplate, error) {
	return nil, ctx.Err()
}
func (s *stubStackTemplateRepo) Count(ctx context.Context) (int64, error) { return 0, ctx.Err() }

// ---- stubTemplateChartConfigRepo ----

type stubTemplateChartConfigRepo struct{}

func (s *stubTemplateChartConfigRepo) Create(ctx context.Context, _ *models.TemplateChartConfig) error {
	return ctx.Err()
}
func (s *stubTemplateChartConfigRepo) FindByID(ctx context.Context, _ string) (*models.TemplateChartConfig, error) {
	return nil, ctx.Err()
}
func (s *stubTemplateChartConfigRepo) Update(ctx context.Context, _ *models.TemplateChartConfig) error {
	return ctx.Err()
}
func (s *stubTemplateChartConfigRepo) Delete(ctx context.Context, _ string) error { return ctx.Err() }
func (s *stubTemplateChartConfigRepo) ListByTemplate(ctx context.Context, _ string) ([]models.TemplateChartConfig, error) {
	return nil, ctx.Err()
}

// ---- stubValueOverrideRepo ----

type stubValueOverrideRepo struct{}

func (s *stubValueOverrideRepo) Create(ctx context.Context, _ *models.ValueOverride) error {
	return ctx.Err()
}
func (s *stubValueOverrideRepo) FindByID(ctx context.Context, _ string) (*models.ValueOverride, error) {
	return nil, ctx.Err()
}
func (s *stubValueOverrideRepo) DeleteByInstance(ctx context.Context, _ string) error {
	return ctx.Err()
}
func (s *stubValueOverrideRepo) FindByInstanceAndChart(ctx context.Context, _, _ string) (*models.ValueOverride, error) {
	return nil, ctx.Err()
}
func (s *stubValueOverrideRepo) Update(ctx context.Context, _ *models.ValueOverride) error {
	return ctx.Err()
}
func (s *stubValueOverrideRepo) Delete(ctx context.Context, _ string) error { return ctx.Err() }
func (s *stubValueOverrideRepo) ListByInstance(ctx context.Context, _ string) ([]models.ValueOverride, error) {
	return nil, ctx.Err()
}

// ---- stubChartBranchOverrideRepo ----

type stubChartBranchOverrideRepo struct{}

func (s *stubChartBranchOverrideRepo) List(ctx context.Context, _ string) ([]*models.ChartBranchOverride, error) {
	return nil, ctx.Err()
}
func (s *stubChartBranchOverrideRepo) Get(ctx context.Context, _, _ string) (*models.ChartBranchOverride, error) {
	return nil, ctx.Err()
}
func (s *stubChartBranchOverrideRepo) Set(ctx context.Context, _ *models.ChartBranchOverride) error {
	return ctx.Err()
}
func (s *stubChartBranchOverrideRepo) Delete(ctx context.Context, _, _ string) error {
	return ctx.Err()
}
func (s *stubChartBranchOverrideRepo) DeleteByInstance(ctx context.Context, _ string) error {
	return ctx.Err()
}

// ---- stubAPIKeyRepo ----

type stubAPIKeyRepo struct{}

func (s *stubAPIKeyRepo) Create(_ context.Context, _ *models.APIKey) error { return nil }
func (s *stubAPIKeyRepo) FindByID(_ context.Context, _, _ string) (*models.APIKey, error) {
	return nil, nil
}
func (s *stubAPIKeyRepo) FindByPrefix(_ context.Context, _ string) ([]*models.APIKey, error) {
	return nil, nil
}
func (s *stubAPIKeyRepo) FindByIDs(_ context.Context, _ []string) ([]*models.APIKey, error) {
	return nil, nil
}
func (s *stubAPIKeyRepo) ListByUser(_ context.Context, _ string) ([]*models.APIKey, error) {
	return nil, nil
}
func (s *stubAPIKeyRepo) UpdateLastUsed(_ context.Context, _, _ string, _ time.Time) error {
	return nil
}
func (s *stubAPIKeyRepo) Delete(_ context.Context, _, _ string) error { return nil }
func (s *stubAPIKeyRepo) DeleteAllForUser(_ context.Context, _ string) (int64, error) {
	return 0, nil
}

// ---- stubSharedValuesRepo ----

type stubSharedValuesRepo struct{}

func (s *stubSharedValuesRepo) Create(ctx context.Context, _ *models.SharedValues) error {
	return ctx.Err()
}
func (s *stubSharedValuesRepo) FindByID(ctx context.Context, _ string) (*models.SharedValues, error) {
	return nil, ctx.Err()
}
func (s *stubSharedValuesRepo) FindByClusterAndID(ctx context.Context, _, _ string) (*models.SharedValues, error) {
	return nil, ctx.Err()
}
func (s *stubSharedValuesRepo) Update(ctx context.Context, _ *models.SharedValues) error {
	return ctx.Err()
}
func (s *stubSharedValuesRepo) Delete(ctx context.Context, _ string) error { return ctx.Err() }
func (s *stubSharedValuesRepo) ListByCluster(ctx context.Context, _ string) ([]models.SharedValues, error) {
	return nil, ctx.Err()
}

// ---- stubTemplateVersionRepo ----

type stubTemplateVersionRepo struct{}

func (s *stubTemplateVersionRepo) Create(_ context.Context, _ *models.TemplateVersion) error {
	return nil
}
func (s *stubTemplateVersionRepo) ListByTemplate(_ context.Context, _ string) ([]models.TemplateVersion, error) {
	return nil, nil
}
func (s *stubTemplateVersionRepo) GetByID(_ context.Context, _, _ string) (*models.TemplateVersion, error) {
	return nil, nil
}
func (s *stubTemplateVersionRepo) GetLatestByTemplate(_ context.Context, _ string) (*models.TemplateVersion, error) {
	return nil, nil
}

// ---- stubUserFavoriteRepo ----

type stubUserFavoriteRepo struct{}

func (s *stubUserFavoriteRepo) List(_ context.Context, _ string) ([]*models.UserFavorite, error) {
	return nil, nil
}
func (s *stubUserFavoriteRepo) Add(ctx context.Context, _ *models.UserFavorite) error { return nil }
func (s *stubUserFavoriteRepo) Remove(_ context.Context, _, _, _ string) error        { return nil }
func (s *stubUserFavoriteRepo) IsFavorite(_ context.Context, _, _, _ string) (bool, error) {
	return false, nil
}

// ---- stubRefreshTokenRepo ----

// stubRefreshTokenRepo stores nothing. Every method returns the error of a
// done ctx, as the GORM repository does (#656).
type stubRefreshTokenRepo struct{}

func (s *stubRefreshTokenRepo) Create(ctx context.Context, _ *models.RefreshToken) error {
	return ctx.Err()
}
func (s *stubRefreshTokenRepo) FindByTokenHash(ctx context.Context, _ string) (*models.RefreshToken, error) {
	return nil, ctx.Err()
}
func (s *stubRefreshTokenRepo) RevokeByID(ctx context.Context, _ string) error { return ctx.Err() }
func (s *stubRefreshTokenRepo) RevokeAllForUser(ctx context.Context, _ string) error {
	return ctx.Err()
}
func (s *stubRefreshTokenRepo) RevokeAllForUserExcept(ctx context.Context, _, _ string) error {
	return ctx.Err()
}
func (s *stubRefreshTokenRepo) DeleteExpired(ctx context.Context) (int64, error) {
	return 0, ctx.Err()
}
func (s *stubRefreshTokenRepo) CountActiveForUser(ctx context.Context, _ string) (int64, error) {
	return 0, ctx.Err()
}
func (s *stubRefreshTokenRepo) WithTx(ctx context.Context, fn func(txRepo models.RefreshTokenRepository) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(s)
}
func (s *stubRefreshTokenRepo) MarkRotatedIfActive(ctx context.Context, _ string, _ time.Time) (int64, error) {
	return 0, ctx.Err()
}
func (s *stubRefreshTokenRepo) RevokeFamily(ctx context.Context, _ string) error { return ctx.Err() }
func (s *stubRefreshTokenRepo) CountActiveInFamily(ctx context.Context, _ string) (int64, error) {
	return 0, ctx.Err()
}
func (s *stubRefreshTokenRepo) TouchFamily(ctx context.Context, _ string, _ time.Time) error {
	return ctx.Err()
}
func (s *stubRefreshTokenRepo) RevokeOldestForUser(ctx context.Context, _ string, _ int, _ string) (int64, error) {
	return 0, ctx.Err()
}

// ---- stubTxRunner ----

type stubTxRunner struct{}

func (s *stubTxRunner) RunInTx(ctx context.Context, fn func(repos database.TxRepos) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(database.TxRepos{})
}

// buildTestConfig returns a minimal *config.Config suitable for bootstrap tests.
// Fields are set to their minimum valid values needed for the bootstrap functions.
func buildTestConfig() *config.Config {
	return &config.Config{
		CORS: config.CORSConfig{AllowedOrigins: "*"},
		Server: config.ServerConfig{
			Host:           "127.0.0.1",
			Port:           "0",
			RateLimit:      100,
			LoginRateLimit: 10,
			ReadTimeout:    5 * time.Second,
			WriteTimeout:   5 * time.Second,
			IdleTimeout:    30 * time.Second,
		},
		Auth: config.AuthConfig{
			JWTSecret:              "test-secret-key-for-bootstrap-tests-minimum-length",
			JWTExpiration:          time.Hour,
			AccessTokenExpiration:  15 * time.Minute,
			RefreshTokenExpiration: 24 * time.Hour,
			AdminUsername:          "admin",
			AdminPassword:          "admin-password-for-test",
		},
		Deployment: config.DeploymentConfig{
			HelmBinary:                "helm",
			HooksConfigFile:           "",
			ClusterHealthPollInterval: 60 * time.Second,
			DeploymentTimeout:         5 * time.Minute,
			MaxConcurrentDeploys:      2,
		},
	}
}

// buildTestRepositorySet returns a *database.RepositorySet with all fields
// populated by no-op stub implementations. No database connection is needed.
func buildTestRepositorySet() *database.RepositorySet {
	return &database.RepositorySet{
		Cluster:               &stubClusterRepo{},
		StackInstance:         &stubStackInstanceRepo{},
		StackDefinition:       &stubStackDefinitionRepo{},
		ChartConfig:           &stubChartConfigRepo{},
		DeploymentLog:         &stubDeploymentLogRepo{},
		AuditLog:              &stubAuditLogRepo{},
		ResourceQuota:         &stubResourceQuotaRepo{},
		InstanceQuotaOverride: &stubInstanceQuotaOverrideRepo{},
		CleanupPolicy:         &stubCleanupPolicyRepo{},
		Notification:          &stubNotificationRepo{},
		User:                  &stubUserRepo{},
		TxRunner:              &stubTxRunner{},
		StackTemplate:         &stubStackTemplateRepo{},
		TemplateChartConfig:   &stubTemplateChartConfigRepo{},
		ValueOverride:         &stubValueOverrideRepo{},
		ChartBranchOverride:   &stubChartBranchOverrideRepo{},
		APIKey:                &stubAPIKeyRepo{},
		SharedValues:          &stubSharedValuesRepo{},
		TemplateVersion:       &stubTemplateVersionRepo{},
		UserFavorite:          &stubUserFavoriteRepo{},
		RefreshToken:          &stubRefreshTokenRepo{},
	}
}

// buildTestHub creates a websocket.Hub for testing, starts it, and registers
// cleanup to shut it down.
func buildTestHub(t *testing.T) *websocket.Hub {
	t.Helper()
	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })
	return hub
}

// ============================================================================
// buildDomainServices tests
// ============================================================================

func TestBuildDomainServices_ReturnsAllFields(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	cfg := buildTestConfig()
	repos := buildTestRepositorySet()
	hub := buildTestHub(t)
	healthChecker := health.New()

	svc, err := buildDomainServices(cfg, repos, hub, healthChecker)
	require.NoError(t, err)
	require.NotNil(t, svc)

	// Every non-optional field must be populated.
	assert.NotNil(t, svc.GitRegistry, "GitRegistry")
	assert.NotNil(t, svc.ClusterRegistry, "ClusterRegistry")
	assert.NotNil(t, svc.HealthPoller, "HealthPoller")
	assert.NotNil(t, svc.SecretRefresher, "SecretRefresher")
	assert.NotNil(t, svc.K8sWatcher, "K8sWatcher")
	assert.NotNil(t, svc.DeployManager, "DeployManager")
	assert.NotNil(t, svc.LifecycleNotifier, "LifecycleNotifier")
	assert.NotNil(t, svc.CleanupExecutor, "CleanupExecutor")
	assert.NotNil(t, svc.CleanupScheduler, "CleanupScheduler")
	assert.NotNil(t, svc.ValuesGen, "ValuesGen")
	assert.False(t, svc.CleanupScheduler.Active(), "the scheduler runs only in a leadership term")

	// HookDispatcher and ActionRegistry are nil when HooksConfigFile is empty.
	assert.Nil(t, svc.HookDispatcher, "HookDispatcher should be nil when no hooks config")
	assert.Nil(t, svc.ActionRegistry, "ActionRegistry should be nil when no hooks config")
}

func TestBuildDomainServices_PassesGitHubTokenToRegistry(t *testing.T) {
	t.Parallel()

	cfg := buildTestConfig()
	cfg.GitProvider.GitHubToken = "github-token-marker"
	cfg.GitProvider.GitHubAllowedRepositories = []string{"octo-org/permitted"}
	repos := buildTestRepositorySet()
	hub := buildTestHub(t)

	svc, err := buildDomainServices(cfg, repos, hub, health.New())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.Error(t, svc.GitRegistry.HealthCheck(ctx), "a configured GitHub token must enable the registry health check")
}

func TestBuildDomainServices_RejectsGitHubTokenWithoutAllowlist(t *testing.T) {
	t.Parallel()

	cfg := buildTestConfig()
	cfg.GitProvider.GitHubToken = "github-token-secret-marker"

	svc, err := buildDomainServices(cfg, buildTestRepositorySet(), buildTestHub(t), health.New())
	require.Error(t, err)
	assert.Nil(t, svc)
	assert.Contains(t, err.Error(), "build Git provider registry")
	assert.NotContains(t, err.Error(), "github-token-secret-marker")
}

func TestBuildDomainServices_PassesGitHubAllowedRepositoriesToRegistry(t *testing.T) {
	t.Parallel()

	cfg := buildTestConfig()
	if !setGitHubAllowedRepositories(t, &cfg.GitProvider, []string{"octo-org/permitted"}) {
		return
	}
	repos := buildTestRepositorySet()
	hub := buildTestHub(t)

	svc, err := buildDomainServices(cfg, repos, hub, health.New())
	require.NoError(t, err)

	_, err = svc.GitRegistry.ListBranches(context.Background(), "https://github.com/octo-org/denied-marker")
	assert.ErrorIs(t, err, gitprovider.ErrRepositoryNotAllowed)
	assert.NotContains(t, err.Error(), "denied-marker")
}

func setGitHubAllowedRepositories(t *testing.T, providerConfig *config.GitProviderConfig, allowed []string) bool {
	t.Helper()
	field := reflect.ValueOf(providerConfig).Elem().FieldByName("GitHubAllowedRepositories")
	if !assert.True(t, field.IsValid(), "GitProviderConfig must expose parsed GitHubAllowedRepositories") {
		return false
	}
	if !assert.True(t, field.CanSet(), "GitHubAllowedRepositories must be settable during bootstrap") {
		return false
	}
	field.Set(reflect.ValueOf(allowed))
	return true
}

func TestBuildDomainServices_InvalidHooksConfigFile(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	cfg := buildTestConfig()
	cfg.Deployment.HooksConfigFile = "/nonexistent/hooks.json"
	repos := buildTestRepositorySet()
	hub := buildTestHub(t)
	healthChecker := health.New()

	svc, err := buildDomainServices(cfg, repos, hub, healthChecker)
	assert.Error(t, err, "should fail when hooks config file cannot be read")
	assert.Nil(t, svc)
	assert.Contains(t, err.Error(), "load hooks config")
}

func TestBuildDomainServices_AddsHealthChecks(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	cfg := buildTestConfig()
	repos := buildTestRepositorySet()
	hub := buildTestHub(t)
	healthChecker := health.New()
	healthChecker.SetReady(true)

	_, err := buildDomainServices(cfg, repos, hub, healthChecker)
	require.NoError(t, err)

	// HealthChecker should have at least the three checks added by buildDomainServices:
	// "cluster_registry", "git_provider", "helm". We verify by running readiness
	// and checking for those keys in the result.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	status := healthChecker.CheckReadiness(ctx)
	assert.Contains(t, status.Checks, "cluster_registry")
	assert.Contains(t, status.Checks, "git_provider")
	assert.Contains(t, status.Checks, "helm")
}

// ============================================================================
// buildHandlers tests
// ============================================================================

func TestBuildHandlers_ReturnsAllFields(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	cfg := buildTestConfig()
	repos := buildTestRepositorySet()
	hub := buildTestHub(t)
	healthChecker := health.New()

	svc, err := buildDomainServices(cfg, repos, hub, healthChecker)
	require.NoError(t, err)

	sessStore := sessionstore.NewMemoryStore()
	t.Cleanup(func() { sessStore.Stop() })

	hs, err := buildHandlers(cfg, repos, svc, sessStore, hub)
	require.NoError(t, err)
	require.NotNil(t, hs)

	// All handler fields must be non-nil (except OIDC which is nil when disabled).
	assert.NotNil(t, hs.Auth, "Auth")
	assert.NotNil(t, hs.Template, "Template")
	assert.NotNil(t, hs.Definition, "Definition")
	assert.NotNil(t, hs.TemplateVersion, "TemplateVersion")
	assert.NotNil(t, hs.Instance, "Instance")
	assert.NotNil(t, hs.Git, "Git")
	assert.NotNil(t, hs.AuditLog, "AuditLog")
	assert.NotNil(t, hs.User, "User")
	assert.NotNil(t, hs.APIKey, "APIKey")
	assert.NotNil(t, hs.Admin, "Admin")
	assert.NotNil(t, hs.Cluster, "Cluster")
	assert.NotNil(t, hs.BranchOverride, "BranchOverride")
	assert.NotNil(t, hs.InstanceQuotaOverride, "InstanceQuotaOverride")
	assert.NotNil(t, hs.SharedValues, "SharedValues")
	assert.NotNil(t, hs.Notification, "Notification")
	assert.NotNil(t, hs.Favorite, "Favorite")
	assert.NotNil(t, hs.QuickDeploy, "QuickDeploy")
	assert.NotNil(t, hs.Analytics, "Analytics")
	assert.NotNil(t, hs.Dashboard, "Dashboard")
	assert.NotNil(t, hs.CleanupPolicy, "CleanupPolicy")

	// OIDC handler should be nil when OIDC is not enabled.
	assert.Nil(t, hs.OIDC, "OIDC should be nil when cfg.OIDC.Enabled=false")
}

func TestBuildHandlers_OIDCDisabled(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	cfg := buildTestConfig()
	cfg.OIDC.Enabled = false
	repos := buildTestRepositorySet()
	hub := buildTestHub(t)
	healthChecker := health.New()

	svc, err := buildDomainServices(cfg, repos, hub, healthChecker)
	require.NoError(t, err)

	sessStore := sessionstore.NewMemoryStore()
	t.Cleanup(func() { sessStore.Stop() })

	hs, err := buildHandlers(cfg, repos, svc, sessStore, hub)
	require.NoError(t, err)
	assert.Nil(t, hs.OIDC, "OIDC handler should be nil when disabled")
}

// ============================================================================
// buildRouter tests
// ============================================================================

func TestBuildRouter_ReturnsEngineAndRateLimiters(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	cfg := buildTestConfig()
	repos := buildTestRepositorySet()
	hub := buildTestHub(t)
	healthChecker := health.New()
	healthChecker.SetReady(true)

	svc, err := buildDomainServices(cfg, repos, hub, healthChecker)
	require.NoError(t, err)

	sessStore := sessionstore.NewMemoryStore()
	t.Cleanup(func() { sessStore.Stop() })

	hs, err := buildHandlers(cfg, repos, svc, sessStore, hub)
	require.NoError(t, err)

	router, rateLimiters := buildRouter(cfg, hs, routerDeps{
		HealthChecker: healthChecker,
		Hub:           hub,
		SessionStore:  sessStore,
		Repos:         repos,
		Svc:           svc,
	})

	require.NotNil(t, router, "router should not be nil")
	require.NotNil(t, rateLimiters, "rate limiters should not be nil")
	t.Cleanup(func() { rateLimiters.Stop() })

	// The router should have registered routes.
	routeList := router.Routes()
	assert.Greater(t, len(routeList), 0, "router should have registered routes")
}

func TestBuildRouter_HealthEndpointResponds(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	cfg := buildTestConfig()
	repos := buildTestRepositorySet()
	hub := buildTestHub(t)
	healthChecker := health.New()
	healthChecker.SetReady(true)

	svc, err := buildDomainServices(cfg, repos, hub, healthChecker)
	require.NoError(t, err)

	sessStore := sessionstore.NewMemoryStore()
	t.Cleanup(func() { sessStore.Stop() })

	hs, err := buildHandlers(cfg, repos, svc, sessStore, hub)
	require.NoError(t, err)

	router, rateLimiters := buildRouter(cfg, hs, routerDeps{
		HealthChecker: healthChecker,
		Hub:           hub,
		SessionStore:  sessStore,
		Repos:         repos,
		Svc:           svc,
	})
	t.Cleanup(func() { rateLimiters.Stop() })

	// Smoke-test: the health endpoint should respond 200.
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/health/live", nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code, "health/live should return 200")
}

// ============================================================================
// buildLeaderWorkers / startLeaderElection tests
// ============================================================================

func TestBuildLeaderWorkers_ReturnsAllWorkers(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	cfg := buildTestConfig()
	repos := buildTestRepositorySet()
	hub := buildTestHub(t)

	svc, err := buildDomainServices(cfg, repos, hub, health.New())
	require.NoError(t, err)
	sessStore := sessionstore.NewMemoryStore()
	t.Cleanup(func() { sessStore.Stop() })
	hs, err := buildHandlers(cfg, repos, svc, sessStore, hub)
	require.NoError(t, err)

	lw := buildLeaderWorkers(svc, hs, repos, hub, time.Second)
	require.NotNil(t, lw)
	assert.NotNil(t, lw.Reaper, "Reaper")
	assert.NotNil(t, lw.ExpiryWarner, "ExpiryWarner")
	assert.NotNil(t, lw.QuotaMonitor, "QuotaMonitor")
	assert.NotNil(t, lw.SecretMonitor, "SecretMonitor")
	assert.Equal(t, []string{
		"ttl-reaper", "expiry-warner", "cleanup-scheduler", "quota-monitor", "secret-monitor",
		"secret-refresher", "cluster-health-poller", "k8s-status-watcher", "refresh-token-cleanup",
	}, lw.Group.Names())
	assert.False(t, lw.Group.Running(), "buildLeaderWorkers must not start the workers")
}

func TestStartLeaderElection_DisabledRunsWorkersUntilShutdown(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	cfg := buildTestConfig()
	repos := buildTestRepositorySet()
	hub := buildTestHub(t)

	svc, err := buildDomainServices(cfg, repos, hub, health.New())
	require.NoError(t, err)
	sessStore := sessionstore.NewMemoryStore()
	t.Cleanup(func() { sessStore.Stop() })
	hs, err := buildHandlers(cfg, repos, svc, sessStore, hub)
	require.NoError(t, err)

	elector, err := leader.NewInCluster(leaderConfig(cfg.LeaderElection))
	require.NoError(t, err, "election is disabled by default: no cluster needed")
	lw := buildLeaderWorkers(svc, hs, repos, hub, time.Second)
	lr := startLeaderElection(elector, lw.Group)

	require.Eventually(t, func() bool {
		return elector.IsLeader() && lw.Group.Running() && svc.CleanupScheduler.Active()
	}, 2*time.Second, 10*time.Millisecond)

	lr.Shutdown(2 * time.Second)
	assert.False(t, elector.IsLeader())
	assert.False(t, lw.Group.Running())
	assert.False(t, svc.CleanupScheduler.Active())
}

// ============================================================================
// Full integration: buildDomainServices -> buildHandlers -> buildRouter -> leader workers
// ============================================================================

func TestBootstrapFullPipeline(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	cfg := buildTestConfig()
	repos := buildTestRepositorySet()
	hub := buildTestHub(t)
	healthChecker := health.New()
	healthChecker.SetReady(true)

	// Step 1: Domain services.
	svc, err := buildDomainServices(cfg, repos, hub, healthChecker)
	require.NoError(t, err)

	// Step 2: Handlers.
	sessStore := sessionstore.NewMemoryStore()
	t.Cleanup(func() { sessStore.Stop() })

	hs, err := buildHandlers(cfg, repos, svc, sessStore, hub)
	require.NoError(t, err)

	// Step 3: Router.
	router, rateLimiters := buildRouter(cfg, hs, routerDeps{
		HealthChecker: healthChecker,
		Hub:           hub,
		SessionStore:  sessStore,
		Repos:         repos,
		Svc:           svc,
	})
	require.NotNil(t, router)
	t.Cleanup(func() { rateLimiters.Stop() })

	// Step 4: Leader workers (election disabled: this process leads).
	elector, err := leader.New(leaderConfig(cfg.LeaderElection), nil)
	require.NoError(t, err)
	lr := startLeaderElection(elector, buildLeaderWorkers(svc, hs, repos, hub, time.Second).Group)
	t.Cleanup(func() { lr.Shutdown(2 * time.Second) })

	// Verify full pipeline produced working endpoints.
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/health/live", nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func (*stubStackInstanceRepo) CountByStatuses(ctx context.Context, statuses []string) (int, error) {
	return 0, ctx.Err()
}

func (*stubClusterRepo) CountAll(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return 0, nil
}

func (*stubClusterRepo) CountByHealthStatus(ctx context.Context, status string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return 0, nil
}
func (*stubClusterRepo) NamesByIDs(ctx context.Context, _ []string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return map[string]string{}, nil
}

func TestGinMode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		env   string
		debug bool
		want  string
	}{
		{name: "env wins", env: "debug", debug: false, want: gin.DebugMode},
		{name: "debug config", env: "", debug: true, want: gin.DebugMode},
		{name: "production default", env: "", debug: false, want: gin.ReleaseMode},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ginMode(tt.env, tt.debug))
		})
	}
}

// stubRetentionRepo is a models.RetentionRepository that deletes nothing.
type stubRetentionRepo struct{}

func (stubRetentionRepo) DeleteDeploymentLogsOlderThan(context.Context, time.Duration, int) (int64, error) {
	return 0, nil
}
func (stubRetentionRepo) DeleteAuditLogsOlderThan(context.Context, time.Duration, int) (int64, error) {
	return 0, nil
}
func (stubRetentionRepo) DeleteNotificationsOlderThan(context.Context, time.Duration, int) (int64, error) {
	return 0, nil
}
func (stubRetentionRepo) DeleteDeliveryLogsOlderThan(context.Context, time.Duration, int) (int64, error) {
	return 0, nil
}
func (stubRetentionRepo) DeleteActionRunsOlderThan(context.Context, time.Duration, int) (int64, error) {
	return 0, nil
}

func TestBuildLogRetention(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		retention config.RetentionConfig
		repos     *database.RepositorySet
		want      bool
	}{
		{name: "retention set", retention: config.RetentionConfig{AuditLogDays: 365}, repos: &database.RepositorySet{Retention: stubRetentionRepo{}}, want: true},
		{name: "action runs only", retention: config.RetentionConfig{ActionRunDays: 180}, repos: &database.RepositorySet{Retention: stubRetentionRepo{}}, want: true},
		{name: "all 0 keeps everything", retention: config.RetentionConfig{}, repos: &database.RepositorySet{Retention: stubRetentionRepo{}}},
		{name: "no repository", retention: config.RetentionConfig{AuditLogDays: 365}, repos: &database.RepositorySet{}},
		{name: "no repository set", retention: config.RetentionConfig{AuditLogDays: 365}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := buildLogRetention(&config.Config{Retention: tt.retention}, tt.repos)
			if !tt.want {
				assert.Nil(t, w)
				return
			}
			require.NotNil(t, w)
			assert.Equal(t, "log-retention", w.Name)
			assert.NotNil(t, w.Run)
		})
	}
}

// UNIQUE_INSTANCE_NAMES needs an instance repository with name lookup; the
// test stub has none, so buildHandlers must refuse to start.
func TestBuildHandlers_UniqueInstanceNamesNeedsNameLookup(t *testing.T) {
	t.Parallel()

	cfg := buildTestConfig()
	cfg.App.UniqueInstanceNames = true
	repos := buildTestRepositorySet()
	hub := buildTestHub(t)
	healthChecker := health.New()

	svc, err := buildDomainServices(cfg, repos, hub, healthChecker)
	require.NoError(t, err)

	sessStore := sessionstore.NewMemoryStore()
	t.Cleanup(func() { sessStore.Stop() })

	_, err = buildHandlers(cfg, repos, svc, sessStore, hub)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "UNIQUE_INSTANCE_NAMES")
}

func (s *stubCleanupPolicyRepo) SetLastRunAt(_ context.Context, _ string, _ time.Time) error {
	return nil
}

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
	"backend/internal/telemetry"
	"backend/internal/ttl"
	"backend/internal/websocket"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
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
//
// These tests are NOT parallel because startHTTPServer calls os.Exit(1) on
// bind failure. Running them sequentially eliminates port-race risk.

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return port
}

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
}

func TestStartHTTPServer_ListensOnConfiguredAddress(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/ping", func(c *gin.Context) {
		c.String(http.StatusOK, "pong")
	})

	port := freePort(t)
	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:         "127.0.0.1",
			Port:         fmt.Sprintf("%d", port),
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
			IdleTimeout:  30 * time.Second,
		},
	}

	s := startHTTPServer(router, cfg, &telemetry.Telemetry{})
	t.Cleanup(func() { shutdownServers(t, s) })

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	waitForServer(t, addr)

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s/ping", addr))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestStartHTTPServer_PprofEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	mainPort := freePort(t)
	pprofPort := freePort(t)

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

	s := startHTTPServer(router, cfg, &telemetry.Telemetry{})
	t.Cleanup(func() { shutdownServers(t, s) })

	require.NotNil(t, s.Pprof, "pprof server should be set when enabled")

	pprofAddr := fmt.Sprintf("127.0.0.1:%d", pprofPort)
	waitForServer(t, pprofAddr)

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s/debug/pprof/", pprofAddr))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestStartHTTPServer_PprofDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	mainPort := freePort(t)

	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:         "127.0.0.1",
			Port:         fmt.Sprintf("%d", mainPort),
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
			IdleTimeout:  30 * time.Second,
			PprofEnabled: false,
		},
	}

	s := startHTTPServer(router, cfg, &telemetry.Telemetry{})
	t.Cleanup(func() { shutdownServers(t, s) })

	assert.Nil(t, s.Pprof, "pprof server should be nil when disabled")
}

func TestStartHTTPServer_ReturnsValidServer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	port := freePort(t)
	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:         "127.0.0.1",
			Port:         fmt.Sprintf("%d", port),
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 30 * time.Second,
			IdleTimeout:  60 * time.Second,
		},
	}

	s := startHTTPServer(router, cfg, &telemetry.Telemetry{})
	t.Cleanup(func() { shutdownServers(t, s) })

	assert.Equal(t, fmt.Sprintf("127.0.0.1:%d", port), s.Main.Addr)
	assert.Equal(t, 15*time.Second, s.Main.ReadTimeout)
	assert.Equal(t, 30*time.Second, s.Main.WriteTimeout)
	assert.Equal(t, 60*time.Second, s.Main.IdleTimeout)
	assert.Equal(t, router, s.Main.Handler)
}

func TestStartHTTPServer_MetricsEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	mainPort := freePort(t)
	metricsPort := freePort(t)

	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:         "127.0.0.1",
			Port:         fmt.Sprintf("%d", mainPort),
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
			IdleTimeout:  30 * time.Second,
		},
		Otel: config.OtelConfig{
			MetricsEnabled: true,
			MetricsAddr:    fmt.Sprintf("127.0.0.1:%d", metricsPort),
		},
	}

	tel, err := telemetry.Init(cfg.Otel)
	require.NoError(t, err)
	t.Cleanup(func() {
		if tel != nil {
			_ = tel.Shutdown(context.Background())
		}
	})

	s := startHTTPServer(router, cfg, tel)
	t.Cleanup(func() { shutdownServers(t, s) })

	require.NotNil(t, s.Metrics, "metrics server should be set when enabled")

	metricsAddr := fmt.Sprintf("127.0.0.1:%d", metricsPort)
	waitForServer(t, metricsAddr)

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s/metrics", metricsAddr))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
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

func (s *stubClusterRepo) Create(_ *models.Cluster) error             { return nil }
func (s *stubClusterRepo) FindByID(_ string) (*models.Cluster, error) { return nil, nil }
func (s *stubClusterRepo) Update(_ *models.Cluster) error             { return nil }
func (s *stubClusterRepo) Delete(_ string) error                      { return nil }
func (s *stubClusterRepo) List() ([]models.Cluster, error)            { return nil, nil }
func (s *stubClusterRepo) FindDefault() (*models.Cluster, error)      { return nil, nil }
func (s *stubClusterRepo) SetDefault(_ string) error                  { return nil }

// ---- stubStackInstanceRepo ----

type stubStackInstanceRepo struct{}

func (s *stubStackInstanceRepo) Create(_ *models.StackInstance) error             { return nil }
func (s *stubStackInstanceRepo) FindByID(_ string) (*models.StackInstance, error) { return nil, nil }
func (s *stubStackInstanceRepo) FindByNamespace(_ string) (*models.StackInstance, error) {
	return nil, nil
}
func (s *stubStackInstanceRepo) Update(_ *models.StackInstance) error  { return nil }
func (s *stubStackInstanceRepo) Delete(_ string) error                 { return nil }
func (s *stubStackInstanceRepo) List() ([]models.StackInstance, error) { return nil, nil }
func (s *stubStackInstanceRepo) ListPaged(_, _ int) ([]models.StackInstance, int, error) {
	return nil, 0, nil
}
func (s *stubStackInstanceRepo) ListByOwner(_ string) ([]models.StackInstance, error) {
	return nil, nil
}
func (s *stubStackInstanceRepo) FindByName(_ string) ([]models.StackInstance, error) {
	return nil, nil
}
func (s *stubStackInstanceRepo) FindByCluster(_ string) ([]models.StackInstance, error) {
	return nil, nil
}
func (s *stubStackInstanceRepo) CountByClusterAndOwner(_, _ string) (int, error) { return 0, nil }
func (s *stubStackInstanceRepo) CountAll() (int, error)                          { return 0, nil }
func (s *stubStackInstanceRepo) CountByStatus(_ string) (int, error)             { return 0, nil }
func (s *stubStackInstanceRepo) CountByDefinitionIDs(_ []string) (map[string]int, error) {
	return nil, nil
}
func (s *stubStackInstanceRepo) CountByOwnerIDs(_ []string) (map[string]int, error) {
	return nil, nil
}
func (s *stubStackInstanceRepo) ListIDsByDefinitionIDs(_ []string) (map[string][]string, error) {
	return nil, nil
}
func (s *stubStackInstanceRepo) ListIDsByOwnerIDs(_ []string) (map[string][]string, error) {
	return nil, nil
}
func (s *stubStackInstanceRepo) ExistsByDefinitionAndStatus(_, _ string) (bool, error) {
	return false, nil
}
func (s *stubStackInstanceRepo) ListExpired() ([]*models.StackInstance, error) { return nil, nil }
func (s *stubStackInstanceRepo) ListExpiringSoon(_ time.Duration) ([]*models.StackInstance, error) {
	return nil, nil
}
func (s *stubStackInstanceRepo) ListByStatus(_ string, _ int) ([]*models.StackInstance, error) {
	return nil, nil
}

// ---- stubStackDefinitionRepo ----

type stubStackDefinitionRepo struct{}

func (s *stubStackDefinitionRepo) Create(_ *models.StackDefinition) error { return nil }
func (s *stubStackDefinitionRepo) FindByID(_ string) (*models.StackDefinition, error) {
	return nil, nil
}
func (s *stubStackDefinitionRepo) FindByName(_ string) ([]models.StackDefinition, error) {
	return nil, nil
}
func (s *stubStackDefinitionRepo) Update(_ *models.StackDefinition) error  { return nil }
func (s *stubStackDefinitionRepo) Delete(_ string) error                   { return nil }
func (s *stubStackDefinitionRepo) List() ([]models.StackDefinition, error) { return nil, nil }
func (s *stubStackDefinitionRepo) ListPaged(_, _ int) ([]models.StackDefinition, int64, error) {
	return nil, 0, nil
}
func (s *stubStackDefinitionRepo) ListByOwner(_ string) ([]models.StackDefinition, error) {
	return nil, nil
}
func (s *stubStackDefinitionRepo) ListByTemplate(_ string) ([]models.StackDefinition, error) {
	return nil, nil
}
func (s *stubStackDefinitionRepo) CountByTemplateIDs(_ []string) (map[string]int, error) {
	return nil, nil
}
func (s *stubStackDefinitionRepo) ListIDsByTemplateIDs(_ []string) (map[string][]string, error) {
	return nil, nil
}
func (s *stubStackDefinitionRepo) Count() (int64, error) { return 0, nil }

// ---- stubChartConfigRepo ----

type stubChartConfigRepo struct{}

func (s *stubChartConfigRepo) Create(_ *models.ChartConfig) error             { return nil }
func (s *stubChartConfigRepo) FindByID(_ string) (*models.ChartConfig, error) { return nil, nil }
func (s *stubChartConfigRepo) Update(_ *models.ChartConfig) error             { return nil }
func (s *stubChartConfigRepo) Delete(_ string) error                          { return nil }
func (s *stubChartConfigRepo) ListByDefinition(_ string) ([]models.ChartConfig, error) {
	return nil, nil
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
func (s *stubDeploymentLogRepo) GetLatestByInstance(_ context.Context, _ string) (*models.DeploymentLog, error) {
	return nil, nil
}
func (s *stubDeploymentLogRepo) SummarizeByInstance(_ context.Context, _ string) (*models.DeployLogSummary, error) {
	return nil, nil
}
func (s *stubDeploymentLogRepo) SummarizeBatch(_ context.Context, _ []string) (map[string]*models.DeployLogSummary, error) {
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

func (s *stubAuditLogRepo) Create(_ *models.AuditLog) error { return nil }
func (s *stubAuditLogRepo) List(_ models.AuditLogFilters) (*models.AuditLogResult, error) {
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

// ---- stubCleanupPolicyRepo ----

type stubCleanupPolicyRepo struct{}

func (s *stubCleanupPolicyRepo) Create(_ *models.CleanupPolicy) error             { return nil }
func (s *stubCleanupPolicyRepo) FindByID(_ string) (*models.CleanupPolicy, error) { return nil, nil }
func (s *stubCleanupPolicyRepo) Update(_ *models.CleanupPolicy) error             { return nil }
func (s *stubCleanupPolicyRepo) Delete(_ string) error                            { return nil }
func (s *stubCleanupPolicyRepo) List() ([]models.CleanupPolicy, error)            { return nil, nil }
func (s *stubCleanupPolicyRepo) ListEnabled() ([]models.CleanupPolicy, error)     { return nil, nil }

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
func (s *stubNotificationRepo) UpdatePreference(_ context.Context, _ *models.NotificationPreference) error {
	return nil
}

// ---- stubUserRepo ----

type stubUserRepo struct{}

func (s *stubUserRepo) Create(_ *models.User) error                           { return nil }
func (s *stubUserRepo) FindByID(_ string) (*models.User, error)               { return nil, nil }
func (s *stubUserRepo) FindByIDs(_ []string) (map[string]*models.User, error) { return nil, nil }
func (s *stubUserRepo) FindByUsername(_ string) (*models.User, error)         { return nil, nil }
func (s *stubUserRepo) FindByExternalID(_, _ string) (*models.User, error)    { return nil, nil }
func (s *stubUserRepo) Update(_ *models.User) error                           { return nil }
func (s *stubUserRepo) Delete(_ string) error                                 { return nil }
func (s *stubUserRepo) List() ([]models.User, error)                          { return nil, nil }
func (s *stubUserRepo) Count() (int64, error)                                 { return 0, nil }
func (s *stubUserRepo) ListByRoles(_ []string) ([]models.User, error)         { return nil, nil }

// ---- stubStackTemplateRepo ----

type stubStackTemplateRepo struct{}

func (s *stubStackTemplateRepo) Create(_ *models.StackTemplate) error             { return nil }
func (s *stubStackTemplateRepo) FindByID(_ string) (*models.StackTemplate, error) { return nil, nil }
func (s *stubStackTemplateRepo) Update(_ *models.StackTemplate) error             { return nil }
func (s *stubStackTemplateRepo) Delete(_ string) error                            { return nil }
func (s *stubStackTemplateRepo) List() ([]models.StackTemplate, error)            { return nil, nil }
func (s *stubStackTemplateRepo) ListPaged(_, _ int) ([]models.StackTemplate, int64, error) {
	return nil, 0, nil
}
func (s *stubStackTemplateRepo) ListPublished() ([]models.StackTemplate, error) { return nil, nil }
func (s *stubStackTemplateRepo) ListPublishedPaged(_, _ int) ([]models.StackTemplate, int64, error) {
	return nil, 0, nil
}
func (s *stubStackTemplateRepo) ListByOwner(_ string) ([]models.StackTemplate, error) {
	return nil, nil
}
func (s *stubStackTemplateRepo) Count() (int64, error) { return 0, nil }

// ---- stubTemplateChartConfigRepo ----

type stubTemplateChartConfigRepo struct{}

func (s *stubTemplateChartConfigRepo) Create(_ *models.TemplateChartConfig) error { return nil }
func (s *stubTemplateChartConfigRepo) FindByID(_ string) (*models.TemplateChartConfig, error) {
	return nil, nil
}
func (s *stubTemplateChartConfigRepo) Update(_ *models.TemplateChartConfig) error { return nil }
func (s *stubTemplateChartConfigRepo) Delete(_ string) error                      { return nil }
func (s *stubTemplateChartConfigRepo) ListByTemplate(_ string) ([]models.TemplateChartConfig, error) {
	return nil, nil
}

// ---- stubValueOverrideRepo ----

type stubValueOverrideRepo struct{}

func (s *stubValueOverrideRepo) Create(_ *models.ValueOverride) error             { return nil }
func (s *stubValueOverrideRepo) FindByID(_ string) (*models.ValueOverride, error) { return nil, nil }
func (s *stubValueOverrideRepo) FindByInstanceAndChart(_, _ string) (*models.ValueOverride, error) {
	return nil, nil
}
func (s *stubValueOverrideRepo) Update(_ *models.ValueOverride) error { return nil }
func (s *stubValueOverrideRepo) Delete(_ string) error                { return nil }
func (s *stubValueOverrideRepo) ListByInstance(_ string) ([]models.ValueOverride, error) {
	return nil, nil
}

// ---- stubChartBranchOverrideRepo ----

type stubChartBranchOverrideRepo struct{}

func (s *stubChartBranchOverrideRepo) List(_ string) ([]*models.ChartBranchOverride, error) {
	return nil, nil
}
func (s *stubChartBranchOverrideRepo) Get(_, _ string) (*models.ChartBranchOverride, error) {
	return nil, nil
}
func (s *stubChartBranchOverrideRepo) Set(_ *models.ChartBranchOverride) error { return nil }
func (s *stubChartBranchOverrideRepo) Delete(_, _ string) error                { return nil }
func (s *stubChartBranchOverrideRepo) DeleteByInstance(_ string) error         { return nil }

// ---- stubAPIKeyRepo ----

type stubAPIKeyRepo struct{}

func (s *stubAPIKeyRepo) Create(_ *models.APIKey) error                   { return nil }
func (s *stubAPIKeyRepo) FindByID(_, _ string) (*models.APIKey, error)    { return nil, nil }
func (s *stubAPIKeyRepo) FindByPrefix(_ string) ([]*models.APIKey, error) { return nil, nil }
func (s *stubAPIKeyRepo) ListByUser(_ string) ([]*models.APIKey, error)   { return nil, nil }
func (s *stubAPIKeyRepo) UpdateLastUsed(_, _ string, _ time.Time) error   { return nil }
func (s *stubAPIKeyRepo) Delete(_, _ string) error                        { return nil }

// ---- stubSharedValuesRepo ----

type stubSharedValuesRepo struct{}

func (s *stubSharedValuesRepo) Create(_ *models.SharedValues) error             { return nil }
func (s *stubSharedValuesRepo) FindByID(_ string) (*models.SharedValues, error) { return nil, nil }
func (s *stubSharedValuesRepo) FindByClusterAndID(_, _ string) (*models.SharedValues, error) {
	return nil, nil
}
func (s *stubSharedValuesRepo) Update(_ *models.SharedValues) error { return nil }
func (s *stubSharedValuesRepo) Delete(_ string) error               { return nil }
func (s *stubSharedValuesRepo) ListByCluster(_ string) ([]models.SharedValues, error) {
	return nil, nil
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

func (s *stubUserFavoriteRepo) List(_ string) ([]*models.UserFavorite, error) { return nil, nil }
func (s *stubUserFavoriteRepo) Add(_ *models.UserFavorite) error              { return nil }
func (s *stubUserFavoriteRepo) Remove(_, _, _ string) error                   { return nil }
func (s *stubUserFavoriteRepo) IsFavorite(_, _, _ string) (bool, error)       { return false, nil }

// ---- stubRefreshTokenRepo ----

type stubRefreshTokenRepo struct{}

func (s *stubRefreshTokenRepo) Create(_ *models.RefreshToken) error { return nil }
func (s *stubRefreshTokenRepo) FindByTokenHash(_ string) (*models.RefreshToken, error) {
	return nil, nil
}
func (s *stubRefreshTokenRepo) RevokeByID(_ string) error                  { return nil }
func (s *stubRefreshTokenRepo) RevokeByIDIfActive(_ string) (int64, error) { return 0, nil }
func (s *stubRefreshTokenRepo) RevokeAllForUser(_ string) error            { return nil }
func (s *stubRefreshTokenRepo) RevokeAllForUserExcept(_, _ string) error   { return nil }
func (s *stubRefreshTokenRepo) DeleteExpired() (int64, error)              { return 0, nil }
func (s *stubRefreshTokenRepo) CountActiveForUser(_ string) (int64, error) { return 0, nil }
func (s *stubRefreshTokenRepo) WithTx(fn func(txRepo models.RefreshTokenRepository) error) error {
	return fn(s)
}

// ---- stubTxRunner ----

type stubTxRunner struct{}

func (s *stubTxRunner) RunInTx(fn func(repos database.TxRepos) error) error {
	return fn(database.TxRepos{})
}

// ---- stubGenericRepo (models.Repository) ----

type stubGenericRepo struct{}

func (s *stubGenericRepo) Create(_ context.Context, _ interface{}) error                 { return nil }
func (s *stubGenericRepo) FindByID(_ context.Context, _ uint, _ interface{}) error       { return nil }
func (s *stubGenericRepo) Update(_ context.Context, _ interface{}) error                 { return nil }
func (s *stubGenericRepo) Delete(_ context.Context, _ interface{}) error                 { return nil }
func (s *stubGenericRepo) List(_ context.Context, _ interface{}, _ ...interface{}) error { return nil }
func (s *stubGenericRepo) Ping(_ context.Context) error                                  { return nil }
func (s *stubGenericRepo) Close() error                                                  { return nil }

// ============================================================================
// Test helpers
// ============================================================================

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

	// Clean up background goroutines started by buildDomainServices.
	t.Cleanup(func() {
		svc.WatcherCancel()
		svc.K8sWatcher.Stop()
		svc.HealthPoller.Stop()
		svc.SecretRefresher.Stop()
	})

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
	assert.NotNil(t, svc.WatcherCancel, "WatcherCancel")

	// HookDispatcher and ActionRegistry are nil when HooksConfigFile is empty.
	assert.Nil(t, svc.HookDispatcher, "HookDispatcher should be nil when no hooks config")
	assert.Nil(t, svc.ActionRegistry, "ActionRegistry should be nil when no hooks config")
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

	svc, err := buildDomainServices(cfg, repos, hub, healthChecker)
	require.NoError(t, err)
	t.Cleanup(func() {
		svc.WatcherCancel()
		svc.K8sWatcher.Stop()
		svc.HealthPoller.Stop()
		svc.SecretRefresher.Stop()
	})

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
	t.Cleanup(func() {
		svc.WatcherCancel()
		svc.K8sWatcher.Stop()
		svc.HealthPoller.Stop()
		svc.SecretRefresher.Stop()
	})

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
	t.Cleanup(func() {
		svc.WatcherCancel()
		svc.K8sWatcher.Stop()
		svc.HealthPoller.Stop()
		svc.SecretRefresher.Stop()
	})

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
	t.Cleanup(func() {
		svc.WatcherCancel()
		svc.K8sWatcher.Stop()
		svc.HealthPoller.Stop()
		svc.SecretRefresher.Stop()
	})

	sessStore := sessionstore.NewMemoryStore()
	t.Cleanup(func() { sessStore.Stop() })

	hs, err := buildHandlers(cfg, repos, svc, sessStore, hub)
	require.NoError(t, err)

	router, rateLimiters := buildRouter(cfg, hs, routerDeps{
		Repo:          &stubGenericRepo{},
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
	t.Cleanup(func() {
		svc.WatcherCancel()
		svc.K8sWatcher.Stop()
		svc.HealthPoller.Stop()
		svc.SecretRefresher.Stop()
	})

	sessStore := sessionstore.NewMemoryStore()
	t.Cleanup(func() { sessStore.Stop() })

	hs, err := buildHandlers(cfg, repos, svc, sessStore, hub)
	require.NoError(t, err)

	router, rateLimiters := buildRouter(cfg, hs, routerDeps{
		Repo:          &stubGenericRepo{},
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
// startBackgroundServices tests
// ============================================================================

func TestStartBackgroundServices_ReturnsAllFields(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	cfg := buildTestConfig()
	repos := buildTestRepositorySet()
	hub := buildTestHub(t)
	healthChecker := health.New()

	svc, err := buildDomainServices(cfg, repos, hub, healthChecker)
	require.NoError(t, err)
	t.Cleanup(func() {
		svc.WatcherCancel()
		svc.K8sWatcher.Stop()
		svc.HealthPoller.Stop()
		svc.SecretRefresher.Stop()
	})

	sessStore := sessionstore.NewMemoryStore()
	t.Cleanup(func() { sessStore.Stop() })

	hs, err := buildHandlers(cfg, repos, svc, sessStore, hub)
	require.NoError(t, err)

	bg, err := startBackgroundServices(svc, hs, repos, hub)
	require.NoError(t, err)
	require.NotNil(t, bg)

	// Clean up all background services.
	t.Cleanup(func() {
		bg.RefreshTokenCleanupCancel()
		bg.Reaper.Stop()
		bg.ExpiryWarner.Stop()
		bg.QuotaMonitor.Stop()
		bg.SecretMonitor.Stop()
		svc.CleanupScheduler.Stop()
	})

	assert.NotNil(t, bg.Reaper, "Reaper")
	assert.NotNil(t, bg.ExpiryWarner, "ExpiryWarner")
	assert.NotNil(t, bg.QuotaMonitor, "QuotaMonitor")
	assert.NotNil(t, bg.SecretMonitor, "SecretMonitor")
	assert.NotNil(t, bg.RefreshTokenCleanupCancel, "RefreshTokenCleanupCancel")
}

// ============================================================================
// Full integration: buildDomainServices -> buildHandlers -> buildRouter -> startBackgroundServices
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
	t.Cleanup(func() {
		svc.WatcherCancel()
		svc.K8sWatcher.Stop()
		svc.HealthPoller.Stop()
		svc.SecretRefresher.Stop()
	})

	// Step 2: Handlers.
	sessStore := sessionstore.NewMemoryStore()
	t.Cleanup(func() { sessStore.Stop() })

	hs, err := buildHandlers(cfg, repos, svc, sessStore, hub)
	require.NoError(t, err)

	// Step 3: Router.
	router, rateLimiters := buildRouter(cfg, hs, routerDeps{
		Repo:          &stubGenericRepo{},
		HealthChecker: healthChecker,
		Hub:           hub,
		SessionStore:  sessStore,
		Repos:         repos,
		Svc:           svc,
	})
	require.NotNil(t, router)
	t.Cleanup(func() { rateLimiters.Stop() })

	// Step 4: Background services.
	bg, err := startBackgroundServices(svc, hs, repos, hub)
	require.NoError(t, err)
	t.Cleanup(func() {
		bg.RefreshTokenCleanupCancel()
		bg.Reaper.Stop()
		bg.ExpiryWarner.Stop()
		bg.QuotaMonitor.Stop()
		bg.SecretMonitor.Stop()
		svc.CleanupScheduler.Stop()
	})

	// Verify full pipeline produced working endpoints.
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/health/live", nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

package main

import (
	"backend/internal/cluster"
	"backend/internal/config"
	"backend/internal/deployer"
	"backend/internal/health"
	"backend/internal/models"
	"backend/internal/scheduler"
	"backend/internal/ttl"
	"backend/internal/websocket"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// MockDB is a mock implementation of the database.Database interface
type MockDB struct {
	mock.Mock
}

func (m *MockDB) AutoMigrate() error {
	args := m.Called()
	return args.Error(0)
}

func (m *MockDB) DB() *gorm.DB {
	args := m.Called()
	return args.Get(0).(*gorm.DB)
}

// MockRepository is a mock implementation of the models.Repository interface
type MockRepository struct {
	mock.Mock
}

func (m *MockRepository) Create(ctx context.Context, entity interface{}) error {
	args := m.Called(ctx, entity)
	return args.Error(0)
}

func (m *MockRepository) FindByID(ctx context.Context, id uint, dest interface{}) error {
	args := m.Called(ctx, id, dest)
	return args.Error(0)
}

func (m *MockRepository) Update(ctx context.Context, entity interface{}) error {
	args := m.Called(ctx, entity)
	return args.Error(0)
}

func (m *MockRepository) Delete(ctx context.Context, entity interface{}) error {
	args := m.Called(ctx, entity)
	return args.Error(0)
}

func (m *MockRepository) List(ctx context.Context, dest interface{}, conditions ...interface{}) error {
	args := m.Called(ctx, dest, conditions)
	return args.Error(0)
}

func (m *MockRepository) Ping(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *MockRepository) Close() error {
	args := m.Called()
	return args.Error(0)
}

// MockSQLDB is a mock implementation of the sql.DB interface
type MockSQLDB struct {
	mock.Mock
}

func (m *MockSQLDB) Ping() error {
	args := m.Called()
	return args.Error(0)
}

func (m *MockSQLDB) Close() error {
	args := m.Called()
	return args.Error(0)
}

// Helper function to set up environment variables for testing
func setupTestEnv() {
	os.Setenv("APP_NAME", "testapp")
	os.Setenv("GO_ENV", "testing")
	os.Setenv("APP_DEBUG", "true")
	os.Setenv("DB_HOST", "testhost")
	os.Setenv("DB_PORT", "3306")
	os.Setenv("DB_USER", "testuser")
	os.Setenv("DB_PASSWORD", "testpass")
	os.Setenv("DB_NAME", "testdb")
	os.Setenv("SERVER_HOST", "localhost")
	os.Setenv("SERVER_PORT", "8082") // Use different port for tests
}

// Helper function to clean up environment variables after testing
func cleanupTestEnv() {
	vars := []string{
		"APP_NAME", "GO_ENV", "APP_DEBUG",
		"DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD", "DB_NAME",
		"DB_MAX_OPEN_CONNS", "DB_MAX_IDLE_CONNS", "DB_CONN_MAX_LIFETIME",
		"SERVER_HOST", "SERVER_PORT", "SERVER_READ_TIMEOUT", "SERVER_WRITE_TIMEOUT", "SERVER_SHUTDOWN_TIMEOUT",
		"LOG_LEVEL", "LOG_FILE",
		"USE_AZURE_TABLE", "USE_AZURITE",
		"AZURE_TABLE_ACCOUNT_NAME", "AZURE_TABLE_ACCOUNT_KEY",
		"AZURE_TABLE_ENDPOINT", "AZURE_TABLE_NAME",
	}
	for _, v := range vars {
		os.Unsetenv(v)
	}
}

// Mock functions to replace the actual implementations
func mockLoadConfig() (*config.Config, error) {
	return &config.Config{
		App: config.AppConfig{
			Name:        "testapp",
			Environment: "testing",
			Debug:       true,
		},
		Database: config.DatabaseConfig{
			Host:            "testhost",
			Port:            "3306",
			User:            "testuser",
			Password:        "testpass",
			DBName:          "testdb",
			MaxOpenConns:    5,
			MaxIdleConns:    2,
			ConnMaxLifetime: 5 * time.Minute,
		},
		Server: config.ServerConfig{
			Host:            "localhost",
			Port:            "8082",
			ReadTimeout:     10 * time.Second,
			WriteTimeout:    time.Duration(0),
			ShutdownTimeout: 30 * time.Second,
		},
		Logging: config.LogConfig{
			Level: "debug",
			File:  "",
		},
		AzureTable: config.AzureTableConfig{
			UseAzureTable: false,
		},
	}, nil
}

// Tests for main.go functions

func TestConfigLoading(t *testing.T) {
	t.Parallel()
	setupTestEnv()
	t.Cleanup(cleanupTestEnv)

	cfg, err := config.LoadConfig()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "testapp", cfg.App.Name)
	assert.Equal(t, "testing", cfg.App.Environment)
	assert.True(t, cfg.App.Debug)
	assert.Equal(t, "testhost", cfg.Database.Host)
}

func TestHealthEndpoints(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	r := gin.Default()

	healthChecker := health.New()
	healthChecker.SetReady(true)

	r.GET("/health/live", func(c *gin.Context) {
		status := healthChecker.CheckLiveness(c.Request.Context())
		c.JSON(http.StatusOK, status)
	})

	r.GET("/health/ready", func(c *gin.Context) {
		status := healthChecker.CheckReadiness(c.Request.Context())
		if status.Status == "DOWN" {
			c.JSON(http.StatusServiceUnavailable, status)
			return
		}
		c.JSON(http.StatusOK, status)
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "/health/live", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "UP")

	w = httptest.NewRecorder()
	req, _ = http.NewRequestWithContext(context.Background(), "GET", "/health/ready", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "UP")

	healthChecker.AddCheck("test", func(_ context.Context) error {
		return errors.New("test error")
	})

	w = httptest.NewRecorder()
	req, _ = http.NewRequestWithContext(context.Background(), "GET", "/health/ready", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), "DOWN")
}

func TestDatabaseHealthCheck(t *testing.T) {
	t.Parallel()
	healthChecker := health.New()

	mockRepo := new(MockRepository)
	mockRepo.On("Ping", mock.Anything).Return(nil)

	healthChecker.AddCheck("database", func(_ context.Context) error {
		return mockRepo.Ping(context.Background())
	})

	status := healthChecker.CheckReadiness(context.Background())
	assert.Equal(t, "DOWN", status.Status) // Initially DOWN because we haven't set ready

	healthChecker.SetReady(true)

	status = healthChecker.CheckReadiness(context.Background())
	assert.Equal(t, "UP", status.Status)
	assert.Equal(t, "UP", status.Checks["database"].Status)

	mockRepo = new(MockRepository)
	mockRepo.On("Ping", mock.Anything).Return(errors.New("connection failed"))

	healthChecker = health.New()
	healthChecker.SetReady(true)
	healthChecker.AddCheck("database", func(_ context.Context) error {
		return mockRepo.Ping(context.Background())
	})

	status = healthChecker.CheckReadiness(context.Background())
	assert.Equal(t, "DOWN", status.Status)
	assert.Equal(t, "DOWN", status.Checks["database"].Status)
	assert.Contains(t, status.Checks["database"].Message, "connection failed")
}

func TestServerConfiguration(t *testing.T) {
	t.Parallel()
	cfg, _ := mockLoadConfig()

	addr := cfg.Server.Host + ":" + cfg.Server.Port
	assert.Equal(t, "localhost:8082", addr)

	server := &http.Server{
		Addr:         addr,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
	}

	assert.Equal(t, addr, server.Addr)
	assert.Equal(t, 10*time.Second, server.ReadTimeout)
	assert.Equal(t, time.Duration(0), server.WriteTimeout)
}

// ---- mock repos for ensureDefaultCluster / backfillInstanceClusterIDs ----

type mockClusterRepo struct {
	mu       sync.RWMutex
	clusters []*models.Cluster
	err      error
}

func newMockClusterRepo() *mockClusterRepo {
	return &mockClusterRepo{}
}

func (m *mockClusterRepo) Create(c *models.Cluster) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	cp := *c
	m.clusters = append(m.clusters, &cp)
	return nil
}

func (m *mockClusterRepo) FindByID(id string) (*models.Cluster, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, c := range m.clusters {
		if c.ID == id {
			return c, nil
		}
	}
	return nil, fmt.Errorf("not found")
}

func (m *mockClusterRepo) Update(c *models.Cluster) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, existing := range m.clusters {
		if existing.ID == c.ID {
			cp := *c
			m.clusters[i] = &cp
			return nil
		}
	}
	return fmt.Errorf("not found")
}

func (m *mockClusterRepo) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, c := range m.clusters {
		if c.ID == id {
			m.clusters = append(m.clusters[:i], m.clusters[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("not found")
}

func (m *mockClusterRepo) List() ([]models.Cluster, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	out := make([]models.Cluster, len(m.clusters))
	for i, c := range m.clusters {
		out[i] = *c
	}
	return out, nil
}

func (m *mockClusterRepo) FindDefault() (*models.Cluster, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, c := range m.clusters {
		if c.IsDefault {
			return c, nil
		}
	}
	return nil, fmt.Errorf("not found")
}

func (m *mockClusterRepo) SetDefault(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.clusters {
		c.IsDefault = c.ID == id
	}
	return nil
}

type mockInstanceRepo struct {
	mu    sync.RWMutex
	items map[string]*models.StackInstance
	err   error
}

func newMockInstanceRepo() *mockInstanceRepo {
	return &mockInstanceRepo{items: make(map[string]*models.StackInstance)}
}

func (m *mockInstanceRepo) Create(inst *models.StackInstance) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *inst
	m.items[inst.ID] = &cp
	return nil
}

func (m *mockInstanceRepo) FindByID(id string) (*models.StackInstance, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	inst, ok := m.items[id]
	if !ok {
		return nil, errors.New("not found")
	}
	cp := *inst
	return &cp, nil
}

func (m *mockInstanceRepo) Update(inst *models.StackInstance) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	cp := *inst
	m.items[inst.ID] = &cp
	return nil
}

func (m *mockInstanceRepo) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, id)
	return nil
}

func (m *mockInstanceRepo) List() ([]models.StackInstance, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	out := make([]models.StackInstance, 0, len(m.items))
	for _, inst := range m.items {
		out = append(out, *inst)
	}
	return out, nil
}

func (m *mockInstanceRepo) FindByNamespace(namespace string) (*models.StackInstance, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, inst := range m.items {
		if inst.Namespace == namespace {
			cp := *inst
			return &cp, nil
		}
	}
	return nil, errors.New("not found")
}

func (m *mockInstanceRepo) ListByOwner(_ string) ([]models.StackInstance, error) {
	return m.List()
}

func (m *mockInstanceRepo) FindByCluster(clusterID string) ([]models.StackInstance, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []models.StackInstance
	for _, inst := range m.items {
		if inst.ClusterID == clusterID {
			out = append(out, *inst)
		}
	}
	return out, nil
}

func (m *mockInstanceRepo) CountByClusterAndOwner(_, _ string) (int, error) {
	return 0, nil
}

func (m *mockInstanceRepo) ListExpired() ([]*models.StackInstance, error) {
	return nil, nil
}

// ---- extractAPIServerURL tests ----

func TestExtractAPIServerURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		path     string
		wantHost string
	}{
		{
			name:     "nonexistent file returns empty string",
			path:     "/tmp/nonexistent-kubeconfig-12345",
			wantHost: "",
		},
		{
			name:     "empty path returns empty string",
			path:     "",
			wantHost: "",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractAPIServerURL(tt.path)
			assert.Equal(t, tt.wantHost, got)
		})
	}
}

func TestExtractAPIServerURL_ValidKubeconfig(t *testing.T) {
	t.Parallel()

	// Create a temporary kubeconfig file.
	tmpDir := t.TempDir()
	kubeconfigPath := filepath.Join(tmpDir, "kubeconfig")
	kubeconfigContent := `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://my-k8s-cluster.example.com:6443
  name: test-cluster
contexts:
- context:
    cluster: test-cluster
    user: test-user
  name: test-context
current-context: test-context
users:
- name: test-user
  user:
    token: fake-token
`
	err := os.WriteFile(kubeconfigPath, []byte(kubeconfigContent), 0600)
	require.NoError(t, err)

	got := extractAPIServerURL(kubeconfigPath)
	assert.Equal(t, "https://my-k8s-cluster.example.com:6443", got)
}

func TestExtractAPIServerURL_InvalidKubeconfig(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	kubeconfigPath := filepath.Join(tmpDir, "bad-kubeconfig")
	err := os.WriteFile(kubeconfigPath, []byte("this is not valid kubeconfig yaml: [[["), 0600)
	require.NoError(t, err)

	got := extractAPIServerURL(kubeconfigPath)
	// Invalid kubeconfig should return empty string (no panic).
	assert.Empty(t, got)
}

// ---- ensureDefaultCluster tests ----

func TestEnsureDefaultCluster_NoClustersAndValidKubeconfig(t *testing.T) {
	t.Parallel()

	// Create a temporary kubeconfig file.
	tmpDir := t.TempDir()
	kubeconfigPath := filepath.Join(tmpDir, "kubeconfig")
	kubeconfigContent := `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://k8s.example.com:6443
  name: test-cluster
contexts:
- context:
    cluster: test-cluster
    user: test-user
  name: test-context
current-context: test-context
users:
- name: test-user
  user:
    token: fake-token
`
	err := os.WriteFile(kubeconfigPath, []byte(kubeconfigContent), 0600)
	require.NoError(t, err)

	clusterRepo := newMockClusterRepo()
	instanceRepo := newMockInstanceRepo()
	cfg := &config.Config{
		Deployment: config.DeploymentConfig{
			KubeconfigPath: kubeconfigPath,
		},
	}

	ensureDefaultCluster(clusterRepo, instanceRepo, cfg)

	// Verify a default cluster was created.
	clusters, err := clusterRepo.List()
	require.NoError(t, err)
	require.Len(t, clusters, 1)
	assert.Equal(t, "default", clusters[0].Name)
	assert.Equal(t, "https://k8s.example.com:6443", clusters[0].APIServerURL)
	assert.True(t, clusters[0].IsDefault)
	assert.Equal(t, kubeconfigPath, clusters[0].KubeconfigPath)
	assert.Equal(t, models.ClusterUnreachable, clusters[0].HealthStatus)
}

func TestEnsureDefaultCluster_ClustersAlreadyExist(t *testing.T) {
	t.Parallel()

	clusterRepo := newMockClusterRepo()
	clusterRepo.clusters = append(clusterRepo.clusters, &models.Cluster{
		ID:   "existing-cluster",
		Name: "existing",
	})

	instanceRepo := newMockInstanceRepo()
	cfg := &config.Config{
		Deployment: config.DeploymentConfig{
			KubeconfigPath: "/some/path",
		},
	}

	ensureDefaultCluster(clusterRepo, instanceRepo, cfg)

	// Should not create a new cluster.
	clusters, err := clusterRepo.List()
	require.NoError(t, err)
	assert.Len(t, clusters, 1)
	assert.Equal(t, "existing", clusters[0].Name)
}

func TestEnsureDefaultCluster_NoKubeconfigPath(t *testing.T) {
	t.Parallel()

	clusterRepo := newMockClusterRepo()
	instanceRepo := newMockInstanceRepo()
	cfg := &config.Config{
		Deployment: config.DeploymentConfig{
			KubeconfigPath: "",
		},
	}

	ensureDefaultCluster(clusterRepo, instanceRepo, cfg)

	// Should not create a cluster when no kubeconfig path is set.
	clusters, err := clusterRepo.List()
	require.NoError(t, err)
	assert.Empty(t, clusters)
}

func TestEnsureDefaultCluster_ListError(t *testing.T) {
	t.Parallel()

	clusterRepo := newMockClusterRepo()
	clusterRepo.err = fmt.Errorf("db error")

	instanceRepo := newMockInstanceRepo()
	cfg := &config.Config{
		Deployment: config.DeploymentConfig{
			KubeconfigPath: "/some/path",
		},
	}

	// Should not panic on list error.
	assert.NotPanics(t, func() {
		ensureDefaultCluster(clusterRepo, instanceRepo, cfg)
	})
}

func TestEnsureDefaultCluster_InvalidKubeconfig(t *testing.T) {
	t.Parallel()

	clusterRepo := newMockClusterRepo()
	instanceRepo := newMockInstanceRepo()
	cfg := &config.Config{
		Deployment: config.DeploymentConfig{
			KubeconfigPath: "/nonexistent/kubeconfig",
		},
	}

	ensureDefaultCluster(clusterRepo, instanceRepo, cfg)

	// Should not create a cluster when API server URL cannot be determined.
	clusters, err := clusterRepo.List()
	require.NoError(t, err)
	assert.Empty(t, clusters)
}

func TestEnsureDefaultCluster_CreateError(t *testing.T) {
	t.Parallel()

	// Create a valid kubeconfig so we get past the URL extraction step.
	tmpDir := t.TempDir()
	kubeconfigPath := filepath.Join(tmpDir, "kubeconfig")
	kubeconfigContent := `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://k8s.example.com:6443
  name: test-cluster
contexts:
- context:
    cluster: test-cluster
    user: test-user
  name: test-context
current-context: test-context
users:
- name: test-user
  user:
    token: fake-token
`
	err := os.WriteFile(kubeconfigPath, []byte(kubeconfigContent), 0600)
	require.NoError(t, err)

	// Trick: After List() succeeds (returns empty), set error so Create fails.
	clusterRepo := &failOnCreateClusterRepo{}
	instanceRepo := newMockInstanceRepo()
	cfg := &config.Config{
		Deployment: config.DeploymentConfig{
			KubeconfigPath: kubeconfigPath,
		},
	}

	// Should not panic on create error.
	assert.NotPanics(t, func() {
		ensureDefaultCluster(clusterRepo, instanceRepo, cfg)
	})
}

// failOnCreateClusterRepo returns empty list but errors on Create.
type failOnCreateClusterRepo struct{}

func (f *failOnCreateClusterRepo) Create(_ *models.Cluster) error {
	return fmt.Errorf("create failed")
}
func (f *failOnCreateClusterRepo) FindByID(_ string) (*models.Cluster, error) {
	return nil, fmt.Errorf("not found")
}
func (f *failOnCreateClusterRepo) Update(_ *models.Cluster) error  { return nil }
func (f *failOnCreateClusterRepo) Delete(_ string) error           { return nil }
func (f *failOnCreateClusterRepo) List() ([]models.Cluster, error) { return nil, nil }
func (f *failOnCreateClusterRepo) FindDefault() (*models.Cluster, error) {
	return nil, fmt.Errorf("not found")
}
func (f *failOnCreateClusterRepo) SetDefault(_ string) error { return nil }

// ---- backfillInstanceClusterIDs tests ----

func TestBackfillInstanceClusterIDs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		instances       []*models.StackInstance
		clusterID       string
		wantBackfilled  int
		wantUntouched   int
	}{
		{
			name: "backfills instances with empty cluster ID",
			instances: []*models.StackInstance{
				{ID: "inst-1", Name: "a", ClusterID: ""},
				{ID: "inst-2", Name: "b", ClusterID: ""},
				{ID: "inst-3", Name: "c", ClusterID: "existing-cluster"},
			},
			clusterID:      "new-default",
			wantBackfilled: 2,
			wantUntouched:  1,
		},
		{
			name:            "no instances to backfill",
			instances:       []*models.StackInstance{},
			clusterID:       "new-default",
			wantBackfilled:  0,
			wantUntouched:   0,
		},
		{
			name: "all instances already have cluster ID",
			instances: []*models.StackInstance{
				{ID: "inst-1", Name: "a", ClusterID: "existing"},
				{ID: "inst-2", Name: "b", ClusterID: "other"},
			},
			clusterID:      "new-default",
			wantBackfilled: 0,
			wantUntouched:  2,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := newMockInstanceRepo()
			for _, inst := range tt.instances {
				require.NoError(t, repo.Create(inst))
			}

			backfillInstanceClusterIDs(repo, tt.clusterID)

			// Verify results.
			backfilled := 0
			untouched := 0
			for _, inst := range tt.instances {
				updated, err := repo.FindByID(inst.ID)
				require.NoError(t, err)
				if inst.ClusterID == "" {
					// Was originally empty, should now have the new cluster ID.
					assert.Equal(t, tt.clusterID, updated.ClusterID)
					backfilled++
				} else {
					// Already had a cluster ID, should be unchanged.
					assert.Equal(t, inst.ClusterID, updated.ClusterID)
					untouched++
				}
			}
			assert.Equal(t, tt.wantBackfilled, backfilled)
			assert.Equal(t, tt.wantUntouched, untouched)
		})
	}
}

func TestBackfillInstanceClusterIDs_ListError(t *testing.T) {
	t.Parallel()

	repo := newMockInstanceRepo()
	repo.err = fmt.Errorf("list failed")

	// Should not panic on list error.
	assert.NotPanics(t, func() {
		backfillInstanceClusterIDs(repo, "some-cluster-id")
	})
}

func TestBackfillInstanceClusterIDs_UpdateError(t *testing.T) {
	t.Parallel()

	repo := newMockInstanceRepo()
	require.NoError(t, repo.Create(&models.StackInstance{ID: "inst-1", Name: "a", ClusterID: ""}))

	// Set error after creation so list succeeds but update fails.
	repo.err = fmt.Errorf("update failed")

	// Should not panic on update error (best-effort).
	assert.NotPanics(t, func() {
		backfillInstanceClusterIDs(repo, "some-cluster-id")
	})
}

// ---- mock policy repo for scheduler ----

type stubPolicyRepo struct{}

func (s *stubPolicyRepo) Create(_ *models.CleanupPolicy) error        { return nil }
func (s *stubPolicyRepo) FindByID(_ string) (*models.CleanupPolicy, error) {
	return nil, fmt.Errorf("not found")
}
func (s *stubPolicyRepo) Update(_ *models.CleanupPolicy) error        { return nil }
func (s *stubPolicyRepo) Delete(_ string) error                       { return nil }
func (s *stubPolicyRepo) List() ([]models.CleanupPolicy, error)       { return nil, nil }
func (s *stubPolicyRepo) ListEnabled() ([]models.CleanupPolicy, error) { return nil, nil }

// ---- gracefulShutdown tests ----

func TestGracefulShutdown(t *testing.T) {
	t.Parallel()

	// Create a test HTTP server.
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	srv := &http.Server{
		Addr:    "127.0.0.1:0",
		Handler: router,
	}

	// Start the server so ListenAndServe is active.
	go func() {
		_ = srv.ListenAndServe()
	}()
	time.Sleep(50 * time.Millisecond)

	// Create minimal deps for shutdown.
	hub := websocket.NewHub()
	go hub.Run()

	reaper := ttl.NewReaper(newMockInstanceRepo(), nil, hub, nil, 60*time.Second)
	// Start the reaper so Stop() can signal it to exit.
	go reaper.Start()

	clusterRegistry := cluster.NewRegistry(cluster.RegistryConfig{})
	healthPoller := cluster.NewHealthPoller(cluster.HealthPollerConfig{
		Interval: 1 * time.Hour,
	})
	healthPoller.Start()

	cleanupScheduler := scheduler.NewScheduler(&stubPolicyRepo{}, newMockInstanceRepo(), nil, nil)
	// Start the scheduler so Stop() can signal it to exit.
	_ = cleanupScheduler.Start()

	deployManager := deployer.NewManager(deployer.ManagerConfig{
		MaxConcurrent: 1,
	})

	_, watcherCancel := context.WithCancel(context.Background())

	mockRepo := new(MockRepository)
	mockRepo.On("Close").Return(nil)

	done := make(chan struct{})
	go func() {
		gracefulShutdown(srv, 2*time.Second, shutdownDeps{
			reaper:           reaper,
			cleanupScheduler: cleanupScheduler,
			deployManager:    deployManager,
			healthPoller:     healthPoller,
			k8sWatcher:       nil, // nil k8sWatcher should be handled gracefully
			hub:              hub,
			clusterRegistry:  clusterRegistry,
			watcherCancel:    watcherCancel,
			oidcStateStore:   nil, // nil oidcStateStore should be handled gracefully
			repo:             mockRepo,
		})
		close(done)
	}()

	select {
	case <-done:
		// Shutdown completed.
	case <-time.After(10 * time.Second):
		t.Fatal("gracefulShutdown did not complete in time")
	}

	mockRepo.AssertCalled(t, "Close")
}

func TestGracefulShutdown_RepoCloseError(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	srv := &http.Server{
		Addr:    "127.0.0.1:0",
		Handler: router,
	}

	hub := websocket.NewHub()
	go hub.Run()

	reaper := ttl.NewReaper(newMockInstanceRepo(), nil, hub, nil, 60*time.Second)
	go reaper.Start()

	clusterRegistry := cluster.NewRegistry(cluster.RegistryConfig{})
	healthPoller := cluster.NewHealthPoller(cluster.HealthPollerConfig{
		Interval: 1 * time.Hour,
	})
	healthPoller.Start()

	cleanupScheduler := scheduler.NewScheduler(&stubPolicyRepo{}, newMockInstanceRepo(), nil, nil)
	_ = cleanupScheduler.Start()

	deployManager := deployer.NewManager(deployer.ManagerConfig{MaxConcurrent: 1})
	_, watcherCancel := context.WithCancel(context.Background())

	mockRepo := new(MockRepository)
	mockRepo.On("Close").Return(errors.New("close failed"))

	// Should not panic even if repo.Close() errors.
	done := make(chan struct{})
	go func() {
		gracefulShutdown(srv, 2*time.Second, shutdownDeps{
			reaper:           reaper,
			cleanupScheduler: cleanupScheduler,
			deployManager:    deployManager,
			healthPoller:     healthPoller,
			k8sWatcher:       nil,
			hub:              hub,
			clusterRegistry:  clusterRegistry,
			watcherCancel:    watcherCancel,
			oidcStateStore:   nil,
			repo:             mockRepo,
		})
		close(done)
	}()

	select {
	case <-done:
		// ok
	case <-time.After(10 * time.Second):
		t.Fatal("gracefulShutdown did not complete in time")
	}
}

// ---- must tests ----

// must() calls os.Exit(1) on error, so we cannot test the error path directly
// in-process without exec trickery. We test the no-error path to ensure it
// does not exit.
func TestMust_NoError(t *testing.T) {
	t.Parallel()

	assert.NotPanics(t, func() {
		must("test component", nil)
	})
}

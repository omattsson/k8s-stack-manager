//go:build integration

package handlers

import (
	"context"
	"net/http"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"backend/internal/database"
	"backend/internal/helm"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// handlersTestSchema is a schema of its own: the database package tests
// truncate the tables of the shared schema, and the packages run in parallel.
const handlersTestSchema = "ksm_handlers_it"

// openHandlersMySQL connects to TEST_MYSQL_DSN (default: the docker-compose
// MySQL), creates handlersTestSchema and returns a clean session on it.
func openHandlersMySQL(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		dsn = "root:rootpassword@tcp(localhost:3306)/app?charset=utf8mb4&parseTime=True&loc=Local"
	}
	cfg, err := mysqldriver.ParseDSN(dsn)
	require.NoError(t, err)
	cfg.DBName = ""
	admin, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err, "Failed to connect to MySQL — is the container running?")
	require.NoError(t, admin.Exec("CREATE DATABASE IF NOT EXISTS "+handlersTestSchema).Error)
	if sqlDB, err := admin.DB(); err == nil {
		_ = sqlDB.Close()
	}

	cfg.DBName = handlersTestSchema
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	require.NoError(t, db.AutoMigrate(&models.User{}, &models.Cluster{}, &models.StackDefinition{}, &models.StackInstance{}))
	for _, table := range []string{"stack_instances", "stack_definitions", "clusters", "users"} {
		require.NoError(t, db.Exec("DELETE FROM "+table).Error)
	}
	return db.Session(&gorm.Session{SkipDefaultTransaction: true})
}

// slowCreateTxRunner delays each instance create inside the transaction and
// signals after the first limit count, so a second request can count while
// the first transaction is still open.
type slowCreateTxRunner struct {
	inner   database.TxRunner
	counted chan struct{}
	once    sync.Once
	delay   time.Duration
}

func (r *slowCreateTxRunner) RunInTx(ctx context.Context, fn func(repos database.TxRepos) error) error {
	return r.inner.RunInTx(ctx, func(repos database.TxRepos) error {
		repos.StackInstance = &slowCreateInstances{StackInstanceRepository: repos.StackInstance, runner: r}
		return fn(repos)
	})
}

type slowCreateInstances struct {
	models.StackInstanceRepository
	runner *slowCreateTxRunner
}

func (s *slowCreateInstances) CountForInstanceLimit(ctx context.Context, clusterID, ownerID string, includeUnassigned bool) (int, error) {
	n, err := s.StackInstanceRepository.CountForInstanceLimit(ctx, clusterID, ownerID, includeUnassigned)
	s.runner.once.Do(func() { close(s.runner.counted) })
	return n, err
}

func (s *slowCreateInstances) Create(ctx context.Context, inst *models.StackInstance) error {
	time.Sleep(s.runner.delay)
	return s.StackInstanceRepository.Create(ctx, inst)
}

// TestDatabaseMySQL_InstanceLimitConcurrentCreates checks on MySQL that two
// concurrent creates of one owner at limit-1 give one 201 and one 409: the
// second count waits for the user row lock of the first transaction (#682).
func TestDatabaseMySQL_InstanceLimitConcurrentCreates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openHandlersMySQL(t)
	ctx := context.Background()
	require.NoError(t, db.Create(&models.User{ID: "uid-1", Username: "alice", Role: "user"}).Error)
	clusters := database.NewGORMClusterRepository(db, "test-encryption-key-for-integration")
	require.NoError(t, clusters.Create(ctx, &models.Cluster{
		ID: "cl-1", Name: "one", APIServerURL: "https://one.example.com", KubeconfigPath: "/kc", MaxInstancesPerUser: 2,
	}))
	defs := database.NewGORMStackDefinitionRepository(db)
	require.NoError(t, db.Create(&models.StackDefinition{ID: "d1", Name: "def-1", OwnerID: "uid-1"}).Error)
	insts := database.NewGORMStackInstanceRepository(db)
	require.NoError(t, insts.Create(ctx, &models.StackInstance{
		Name: "existing", StackDefinitionID: "d1", Namespace: "stack-existing-alice", OwnerID: "uid-1",
		ClusterID: "cl-1", Branch: "master", Status: models.StackStatusDraft,
	}))

	runner := &slowCreateTxRunner{inner: database.NewGORMTxRunner(db), counted: make(chan struct{}), delay: 500 * time.Millisecond}
	h := NewInstanceHandler(insts, NewMockValueOverrideRepository(), NewMockChartBranchOverrideRepository(), defs,
		NewMockChartConfigRepository(), NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
		helm.NewValuesGenerator(), NewMockUserRepository(), 0)
	h.clusterRepo = clusters
	h.txRunner = runner
	r := gin.New()
	r.Use(injectAuthContext("uid-1", "user"), func(c *gin.Context) {
		c.Set("username", "alice")
		c.Next()
	})
	r.POST("/api/v1/stack-instances", h.CreateInstance)
	env := &instanceLimitEnv{router: r}

	codes := make([]int, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		codes[0] = env.post("/api/v1/stack-instances", `{"stack_definition_id":"d1","name":"first","cluster_id":"cl-1"}`).Code
	}()
	select {
	case <-runner.counted:
	case <-time.After(10 * time.Second):
		t.Fatal("the first create did not count")
	}
	go func() {
		defer wg.Done()
		codes[1] = env.post("/api/v1/stack-instances", `{"stack_definition_id":"d1","name":"second","cluster_id":"cl-1"}`).Code
	}()
	wg.Wait()

	sort.Ints(codes)
	assert.Equal(t, []int{http.StatusCreated, http.StatusConflict}, codes)
	n, err := insts.CountForInstanceLimit(ctx, "cl-1", "uid-1", false)
	require.NoError(t, err)
	assert.Equal(t, 2, n, "the owner stays at the limit")
}

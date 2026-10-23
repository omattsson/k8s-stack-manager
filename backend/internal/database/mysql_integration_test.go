//go:build integration

package database

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// setupMySQLTestDB connects to a real MySQL instance and creates all tables.
// Requires TEST_MYSQL_DSN env var, or defaults to the docker-compose MySQL.
// Each test gets a clean database via table truncation.
func setupMySQLTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		dsn = "root:rootpassword@tcp(localhost:3306)/app?charset=utf8mb4&parseTime=True&loc=Local"
	}

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "Failed to connect to MySQL — is the container running?")

	// AutoMigrate all models
	require.NoError(t, db.AutoMigrate(
		&models.User{},
		&models.Item{},
		&models.Cluster{},
		&models.Notification{},
		&models.NotificationPreference{},
		&models.ResourceQuotaConfig{},
		&models.TemplateVersion{},
		&models.InstanceQuotaOverride{},
		&models.AuditLog{},
		&models.APIKey{},
		&models.StackDefinition{},
		&models.StackTemplate{},
		&models.StackInstance{},
		&models.ChartConfig{},
		&models.TemplateChartConfig{},
		&models.ValueOverride{},
		&models.ChartBranchOverride{},
		&models.DeploymentLog{},
		&models.SharedValues{},
		&models.CleanupPolicy{},
		&models.UserFavorite{},
		&models.InstanceFollower{},
		&models.ActionRun{},
	))

	// Clean all tables before each test
	tables := []string{
		"action_runs", "instance_followers", "user_favorites", "chart_branch_overrides", "value_overrides",
		"template_chart_configs", "chart_configs", "deployment_logs",
		"stack_instances", "stack_templates", "stack_definitions",
		"shared_values", "cleanup_policies", "api_keys", "audit_logs",
		"notification_preferences", "notifications", "instance_quota_overrides",
		"resource_quota_configs", "template_versions", "clusters",
		"items", "users",
	}
	db.Exec("SET FOREIGN_KEY_CHECKS = 0")
	for _, table := range tables {
		db.Exec(fmt.Sprintf("TRUNCATE TABLE `%s`", table))
	}
	db.Exec("SET FOREIGN_KEY_CHECKS = 1")

	return db
}

func TestMySQLIntegration_UserRepository(t *testing.T) {
	db := setupMySQLTestDB(t)
	repo := NewGORMUserRepository(db)

	user := &models.User{
		Username:     "testuser",
		PasswordHash: "hashedpass",
		Role:         "developer",
	}

	// Create
	err := repo.Create(context.Background(), user)
	require.NoError(t, err)
	assert.NotEmpty(t, user.ID)

	// FindByID
	found, err := repo.FindByID(context.Background(), user.ID)
	require.NoError(t, err)
	assert.Equal(t, "testuser", found.Username)

	// FindByUsername
	found, err = repo.FindByUsername(context.Background(), "testuser")
	require.NoError(t, err)
	assert.Equal(t, user.ID, found.ID)

	// Targeted update (UserRepository has no full-row update).
	err = repo.UpdatePassword(context.Background(), user.ID, "newhash")
	require.NoError(t, err)
	found, err = repo.FindByID(context.Background(), user.ID)
	require.NoError(t, err)
	assert.Equal(t, "newhash", found.PasswordHash)

	// List
	users, err := repo.List(context.Background())
	require.NoError(t, err)
	assert.Len(t, users, 1)

	// Delete
	err = repo.Delete(context.Background(), user.ID)
	require.NoError(t, err)
	_, err = repo.FindByID(context.Background(), user.ID)
	assert.Error(t, err)
}

func TestMySQLIntegration_StackDefinitionRepository(t *testing.T) {
	db := setupMySQLTestDB(t)
	repo := NewGORMStackDefinitionRepository(db)

	def := &models.StackDefinition{
		Name:          "test-stack",
		Description:   "A test stack",
		OwnerID:       "user1",
		DefaultBranch: "main",
	}

	err := repo.Create(context.Background(), def)
	require.NoError(t, err)
	assert.NotEmpty(t, def.ID)

	found, err := repo.FindByID(context.Background(), def.ID)
	require.NoError(t, err)
	assert.Equal(t, "test-stack", found.Name)

	defs, err := repo.List(context.Background())
	require.NoError(t, err)
	assert.Len(t, defs, 1)

	defs, err = repo.ListByOwner(context.Background(), "user1")
	require.NoError(t, err)
	assert.Len(t, defs, 1)

	err = repo.Delete(context.Background(), def.ID)
	require.NoError(t, err)
}

func TestMySQLIntegration_StackInstanceRepository(t *testing.T) {
	db := setupMySQLTestDB(t)
	repo := NewGORMStackInstanceRepository(db)

	instance := &models.StackInstance{
		Name:              "test-instance",
		Namespace:         "stack-test-instance-user1",
		StackDefinitionID: "def1",
		OwnerID:           "user1",
		Status:            "pending",
		ClusterID:         "cluster1",
	}

	// Create
	err := repo.Create(context.Background(), instance)
	require.NoError(t, err)
	assert.NotEmpty(t, instance.ID)

	// FindByID
	found, err := repo.FindByID(context.Background(), instance.ID)
	require.NoError(t, err)
	assert.Equal(t, "test-instance", found.Name)

	// FindByNamespace
	found, err = repo.FindByNamespace(context.Background(), "stack-test-instance-user1")
	require.NoError(t, err)
	assert.Equal(t, instance.ID, found.ID)

	// ListPaged
	instances, total, err := repo.ListPaged(context.Background(), models.StackInstanceFilter{}, 10, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Len(t, instances, 1)

	// FindByCluster
	instances, err = repo.FindByCluster(context.Background(), "cluster1")
	require.NoError(t, err)
	assert.Len(t, instances, 1)

	// CountForInstanceLimit
	count, err := repo.CountForInstanceLimit(context.Background(), "cluster1", "user1", false)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// FindByNameOnCluster and DuplicateInstanceNames (UNIQUE_INSTANCE_NAMES)
	second := &models.StackInstance{
		Name:              "Test-Instance",
		Namespace:         "stack-test-instance-user2",
		StackDefinitionID: "def1",
		OwnerID:           "user2",
		Status:            "pending",
	}
	require.NoError(t, repo.Create(context.Background(), second))
	instances, err = repo.FindByNameOnCluster(context.Background(), "TEST-instance", "cluster1", true)
	require.NoError(t, err)
	assert.Len(t, instances, 2)
	instances, err = repo.FindByNameOnCluster(context.Background(), "test-instance", "cluster1", false)
	require.NoError(t, err)
	assert.Len(t, instances, 1)
	dups, err := repo.DuplicateInstanceNames(context.Background(), "cluster1")
	require.NoError(t, err)
	assert.Equal(t, []models.DuplicateInstanceName{{ClusterID: "cluster1", Name: "test-instance", Count: 2}}, dups)
	require.NoError(t, repo.Delete(context.Background(), second.ID))

	// Update
	instance.Status = "running"
	err = repo.Update(context.Background(), instance)
	require.NoError(t, err)

	// Delete
	err = repo.Delete(context.Background(), instance.ID)
	require.NoError(t, err)
}

func TestMySQLIntegration_ClusterRepository(t *testing.T) {
	db := setupMySQLTestDB(t)
	repo := NewGORMClusterRepository(db, "test-encryption-key-for-integration")

	cluster := &models.Cluster{
		Name:         "test-cluster",
		Description:  "Test cluster",
		APIServerURL: "https://k8s.example.com",
		IsDefault:    true,
	}

	err := repo.Create(context.Background(), cluster)
	require.NoError(t, err)
	assert.NotEmpty(t, cluster.ID)

	found, err := repo.FindDefault(context.Background())
	require.NoError(t, err)
	assert.Equal(t, cluster.ID, found.ID)

	// Create another and switch default
	cluster2 := &models.Cluster{
		Name:         "cluster-2",
		Description:  "Second cluster",
		APIServerURL: "https://k8s2.example.com",
	}
	err = repo.Create(context.Background(), cluster2)
	require.NoError(t, err)

	err = repo.SetDefault(context.Background(), cluster2.ID)
	require.NoError(t, err)

	found, err = repo.FindDefault(context.Background())
	require.NoError(t, err)
	assert.Equal(t, cluster2.ID, found.ID)

	clusters, err := repo.List(context.Background())
	require.NoError(t, err)
	assert.Len(t, clusters, 2)
}

func TestMySQLIntegration_ChartConfigRepository(t *testing.T) {
	db := setupMySQLTestDB(t)
	repo := NewGORMChartConfigRepository(db)

	cfg := &models.ChartConfig{
		StackDefinitionID: "def1",
		ChartName:         "nginx",
		RepositoryURL:     "https://charts.example.com",
		ChartVersion:      "1.0.0",
		DeployOrder:       1,
	}

	err := repo.Create(context.Background(), cfg)
	require.NoError(t, err)
	assert.NotEmpty(t, cfg.ID)

	configs, err := repo.ListByDefinition(context.Background(), "def1")
	require.NoError(t, err)
	assert.Len(t, configs, 1)
	assert.Equal(t, "nginx", configs[0].ChartName)
}

func TestMySQLIntegration_DeploymentLogRepository(t *testing.T) {
	db := setupMySQLTestDB(t)
	repo := NewGORMDeploymentLogRepository(db)
	ctx := context.Background()

	log := &models.DeploymentLog{
		StackInstanceID: "inst1",
		Action:          "deploy",
		Status:          "success",
		StartedAt:       time.Now().Add(-5 * time.Minute),
		CompletedAt:     ptrTime(time.Now()),
	}

	err := repo.Create(ctx, log)
	require.NoError(t, err)

	latest, err := repo.GetLatestByInstance(ctx, "inst1")
	require.NoError(t, err)
	assert.Equal(t, "deploy", latest.Action)

	summary, err := repo.SummarizeByInstance(ctx, "inst1")
	require.NoError(t, err)
	assert.Equal(t, 1, summary.DeployCount)
	assert.Equal(t, 1, summary.SuccessCount)

	// A partial deploy counts as an error in all summaries.
	require.NoError(t, repo.Create(ctx, &models.DeploymentLog{
		StackInstanceID: "inst1",
		Action:          models.DeployActionDeploy,
		Status:          models.DeployLogPartial,
		StartedAt:       time.Now().Add(-time.Minute),
		CompletedAt:     ptrTime(time.Now()),
		UserID:          "user-partial",
	}))
	summary, err = repo.SummarizeByInstance(ctx, "inst1")
	require.NoError(t, err)
	assert.Equal(t, 2, summary.DeployCount)
	assert.Equal(t, 1, summary.SuccessCount)
	assert.Equal(t, 1, summary.ErrorCount)
	batch, err := repo.SummarizeBatch(ctx, []string{"inst1"})
	require.NoError(t, err)
	require.Contains(t, batch, "inst1")
	assert.Equal(t, 1, batch["inst1"].ErrorCount)
	byUser, err := repo.SummarizeByUsers(ctx, []string{"user-partial"})
	require.NoError(t, err)
	require.Contains(t, byUser, "user-partial")
	assert.Equal(t, 1, byUser["user-partial"].ErrorCount)
}

func TestMySQLIntegration_AuditLogRepository(t *testing.T) {
	db := setupMySQLTestDB(t)
	repo := NewGORMAuditLogRepository(db)

	entry := &models.AuditLog{
		UserID:     "user1",
		Username:   "testuser",
		Action:     "create",
		EntityType: "stack_instance",
		EntityID:   "inst1",
		Details:    "Created stack instance",
	}

	err := repo.Create(context.Background(), entry)
	require.NoError(t, err)

	result, err := repo.List(context.Background(), models.AuditLogFilters{Action: "create", Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, int64(1), result.Total)
	assert.Len(t, result.Data, 1)
	assert.Equal(t, "create", result.Data[0].Action)

	// Filter by entity type
	result, err = repo.List(context.Background(), models.AuditLogFilters{EntityType: "stack_instance", Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, int64(1), result.Total)
	assert.Len(t, result.Data, 1)
}

func TestMySQLIntegration_NotificationRepository(t *testing.T) {
	db := setupMySQLTestDB(t)
	repo := NewGORMNotificationRepository(db)
	ctx := context.Background()

	notif := &models.Notification{
		UserID:  "user1",
		Type:    "deploy",
		Title:   "Deployment started",
		Message: "Stack test is deploying",
	}

	err := repo.Create(ctx, notif)
	require.NoError(t, err)

	count, _, err := repo.CountUnread(ctx, "user1")
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	err = repo.MarkAsRead(ctx, notif.ID, "user1")
	require.NoError(t, err)

	count, _, err = repo.CountUnread(ctx, "user1")
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

func TestMySQLIntegration_SharedValuesRepository(t *testing.T) {
	db := setupMySQLTestDB(t)
	repo := NewGORMSharedValuesRepository(db)

	sv := &models.SharedValues{
		ClusterID:   "cluster1",
		Name:        "global-defaults",
		Description: "Global default values",
		Values:      "key: value",
		Priority:    10,
	}

	err := repo.Create(context.Background(), sv)
	require.NoError(t, err)

	list, err := repo.ListByCluster(context.Background(), "cluster1")
	require.NoError(t, err)
	assert.Len(t, list, 1)
	assert.Equal(t, "global-defaults", list[0].Name)
}

func TestMySQLIntegration_CleanupPolicyRepository(t *testing.T) {
	db := setupMySQLTestDB(t)
	repo := NewGORMCleanupPolicyRepository(db)

	policy := &models.CleanupPolicy{
		Name:      "nightly-cleanup",
		Schedule:  "0 2 * * *",
		Action:    "stop",
		Condition: "status == 'running'",
		ClusterID: "cluster1",
		Enabled:   true,
	}

	err := repo.Create(context.Background(), policy)
	require.NoError(t, err)

	enabled, err := repo.ListEnabled(context.Background())
	require.NoError(t, err)
	assert.Len(t, enabled, 1)

	all, err := repo.List(context.Background())
	require.NoError(t, err)
	assert.Len(t, all, 1)
}

func TestMySQLIntegration_UserFavoriteRepository(t *testing.T) {
	db := setupMySQLTestDB(t)
	repo := NewGORMUserFavoriteRepository(db)

	fav := &models.UserFavorite{
		UserID:     "user1",
		EntityType: "template",
		EntityID:   "tmpl1",
	}
	err := repo.Add(context.Background(), fav)
	require.NoError(t, err)

	isFav, err := repo.IsFavorite(context.Background(), "user1", "template", "tmpl1")
	require.NoError(t, err)
	assert.True(t, isFav)

	favorites, err := repo.List(context.Background(), "user1")
	require.NoError(t, err)
	assert.Len(t, favorites, 1)

	err = repo.Remove(context.Background(), "user1", "template", "tmpl1")
	require.NoError(t, err)

	isFav, err = repo.IsFavorite(context.Background(), "user1", "template", "tmpl1")
	require.NoError(t, err)
	assert.False(t, isFav)
}

func TestMySQLIntegration_ValueOverrideRepository(t *testing.T) {
	db := setupMySQLTestDB(t)
	repo := NewGORMValueOverrideRepository(db)

	override := &models.ValueOverride{
		StackInstanceID: "inst1",
		ChartConfigID:   "chart1",
		Values:          "replicas: 3",
	}

	err := repo.Create(context.Background(), override)
	require.NoError(t, err)

	found, err := repo.FindByInstanceAndChart(context.Background(), "inst1", "chart1")
	require.NoError(t, err)
	assert.Equal(t, "replicas: 3", found.Values)

	list, err := repo.ListByInstance(context.Background(), "inst1")
	require.NoError(t, err)
	assert.Len(t, list, 1)
}

func TestMySQLIntegration_ChartBranchOverrideRepository(t *testing.T) {
	db := setupMySQLTestDB(t)
	repo := NewGORMChartBranchOverrideRepository(db)

	err := repo.Set(context.Background(), &models.ChartBranchOverride{
		StackInstanceID: "inst1",
		ChartConfigID:   "chart1",
		Branch:          "feature-branch",
	})
	require.NoError(t, err)

	override, err := repo.Get(context.Background(), "inst1", "chart1")
	require.NoError(t, err)
	assert.Equal(t, "feature-branch", override.Branch)

	overrides, err := repo.List(context.Background(), "inst1")
	require.NoError(t, err)
	assert.Len(t, overrides, 1)

	// Upsert
	err = repo.Set(context.Background(), &models.ChartBranchOverride{
		StackInstanceID: "inst1",
		ChartConfigID:   "chart1",
		Branch:          "develop",
	})
	require.NoError(t, err)
	override, err = repo.Get(context.Background(), "inst1", "chart1")
	require.NoError(t, err)
	assert.Equal(t, "develop", override.Branch)

	err = repo.DeleteByInstance(context.Background(), "inst1")
	require.NoError(t, err)
	overrides, err = repo.List(context.Background(), "inst1")
	require.NoError(t, err)
	assert.Len(t, overrides, 0)
}

func TestMySQLIntegration_APIKeyRepository(t *testing.T) {
	db := setupMySQLTestDB(t)
	repo := NewGORMAPIKeyRepository(db)

	key := &models.APIKey{
		UserID:  "user1",
		Name:    "test-key",
		KeyHash: "hash123",
		Prefix:  "sk_test_abcdef12",
	}

	err := repo.Create(context.Background(), key)
	require.NoError(t, err)
	assert.NotEmpty(t, key.ID)

	byIDs, err := repo.FindByIDs(context.Background(), []string{key.ID, "missing-id"})
	require.NoError(t, err)
	require.Len(t, byIDs, 1)
	assert.Equal(t, key.ID, byIDs[0].ID)

	found, err := repo.FindByPrefix(context.Background(), "sk_test_abcdef12")
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, "test-key", found[0].Name)

	keys, err := repo.ListByUser(context.Background(), "user1")
	require.NoError(t, err)
	assert.Len(t, keys, 1)

	err = repo.Delete(context.Background(), "user1", key.ID)
	require.NoError(t, err)
}

func ptrTime(t time.Time) *time.Time {
	return &t
}

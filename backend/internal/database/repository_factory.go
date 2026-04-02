package database

import (
	"fmt"
	"log/slog"

	"backend/internal/config"
	"backend/internal/database/azure"
	"backend/internal/models"

	"gorm.io/gorm"
)

// RepositorySet holds all domain-specific repository instances.
// Created by NewRepositorySet based on the configured data store.
type RepositorySet struct {
	User                  models.UserRepository
	StackTemplate         models.StackTemplateRepository
	TemplateChartConfig   models.TemplateChartConfigRepository
	StackDefinition       models.StackDefinitionRepository
	ChartConfig           models.ChartConfigRepository
	StackInstance         models.StackInstanceRepository
	ValueOverride         models.ValueOverrideRepository
	ChartBranchOverride   models.ChartBranchOverrideRepository
	AuditLog              models.AuditLogRepository
	APIKey                models.APIKeyRepository
	DeploymentLog         models.DeploymentLogRepository
	SharedValues          models.SharedValuesRepository
	ResourceQuota         models.ResourceQuotaRepository
	InstanceQuotaOverride models.InstanceQuotaOverrideRepository
	TemplateVersion       models.TemplateVersionRepository
	Notification          models.NotificationRepository
	UserFavorite          models.UserFavoriteRepository
	CleanupPolicy         models.CleanupPolicyRepository
	Cluster               models.ClusterRepository
	TxRunner              TxRunner
}

// NewRepositorySet creates all domain-specific repositories based on config.
// When UseAzureTable is true, Azure Table Storage implementations are used.
// When false, GORM implementations are used where available; repositories
// without a GORM implementation yet will cause a fatal error.
func NewRepositorySet(cfg *config.Config, db *gorm.DB) (*RepositorySet, error) {
	if cfg.AzureTable.UseAzureTable {
		return newAzureRepositorySet(cfg)
	}
	return newGORMRepositorySet(cfg, db)
}

func newAzureRepositorySet(cfg *config.Config) (*RepositorySet, error) {
	azCfg := cfg.AzureTable
	an, ak, ep, ua := azCfg.AccountName, azCfg.AccountKey, azCfg.Endpoint, azCfg.UseAzurite

	userRepo, err := azure.NewUserRepository(an, ak, ep, ua)
	if err != nil {
		return nil, fmt.Errorf("user repository: %w", err)
	}
	templateRepo, err := azure.NewStackTemplateRepository(an, ak, ep, ua)
	if err != nil {
		return nil, fmt.Errorf("stack template repository: %w", err)
	}
	templateChartRepo, err := azure.NewTemplateChartConfigRepository(an, ak, ep, ua)
	if err != nil {
		return nil, fmt.Errorf("template chart config repository: %w", err)
	}
	definitionRepo, err := azure.NewStackDefinitionRepository(an, ak, ep, ua)
	if err != nil {
		return nil, fmt.Errorf("stack definition repository: %w", err)
	}
	chartConfigRepo, err := azure.NewChartConfigRepository(an, ak, ep, ua)
	if err != nil {
		return nil, fmt.Errorf("chart config repository: %w", err)
	}
	instanceRepo, err := azure.NewStackInstanceRepository(an, ak, ep, ua)
	if err != nil {
		return nil, fmt.Errorf("stack instance repository: %w", err)
	}
	overrideRepo, err := azure.NewValueOverrideRepository(an, ak, ep, ua)
	if err != nil {
		return nil, fmt.Errorf("value override repository: %w", err)
	}
	branchOverrideRepo, err := azure.NewChartBranchOverrideRepository(an, ak, ep, ua)
	if err != nil {
		return nil, fmt.Errorf("chart branch override repository: %w", err)
	}
	auditRepo, err := azure.NewAuditLogRepository(an, ak, ep, ua)
	if err != nil {
		return nil, fmt.Errorf("audit log repository: %w", err)
	}
	apiKeyRepo, err := azure.NewAPIKeyRepository(an, ak, ep, ua)
	if err != nil {
		return nil, fmt.Errorf("API key repository: %w", err)
	}
	deployLogRepo, err := azure.NewDeploymentLogRepository(an, ak, ep, ua)
	if err != nil {
		return nil, fmt.Errorf("deployment log repository: %w", err)
	}
	sharedValuesRepo, err := azure.NewSharedValuesRepository(an, ak, ep, ua)
	if err != nil {
		return nil, fmt.Errorf("shared values repository: %w", err)
	}
	quotaRepo, err := azure.NewResourceQuotaRepository(an, ak, ep, ua)
	if err != nil {
		return nil, fmt.Errorf("resource quota repository: %w", err)
	}
	quotaOverrideRepo, err := azure.NewInstanceQuotaOverrideRepository(an, ak, ep, ua)
	if err != nil {
		return nil, fmt.Errorf("instance quota override repository: %w", err)
	}
	templateVersionRepo, err := azure.NewTemplateVersionRepository(an, ak, ep, ua)
	if err != nil {
		return nil, fmt.Errorf("template version repository: %w", err)
	}
	notificationRepo, err := azure.NewNotificationRepository(an, ak, ep, ua)
	if err != nil {
		return nil, fmt.Errorf("notification repository: %w", err)
	}
	favoriteRepo, err := azure.NewUserFavoriteRepository(an, ak, ep, ua)
	if err != nil {
		return nil, fmt.Errorf("user favorite repository: %w", err)
	}
	cleanupPolicyRepo, err := azure.NewCleanupPolicyRepository(an, ak, ep, ua)
	if err != nil {
		return nil, fmt.Errorf("cleanup policy repository: %w", err)
	}
	clusterRepo, err := azure.NewClusterRepository(an, ak, ep, ua, cfg.Deployment.KubeconfigEncryptionKey)
	if err != nil {
		return nil, fmt.Errorf("cluster repository: %w", err)
	}

	noOpTx := NewNoOpTxRunner(TxRepos{
		StackDefinition: definitionRepo,
		ChartConfig:     chartConfigRepo,
		StackInstance:   instanceRepo,
		StackTemplate:   templateRepo,
		TemplateChart:   templateChartRepo,
		ValueOverride:   overrideRepo,
		BranchOverride:  branchOverrideRepo,
		DeploymentLog:   deployLogRepo,
	})

	return &RepositorySet{
		User:                  userRepo,
		StackTemplate:         templateRepo,
		TemplateChartConfig:   templateChartRepo,
		StackDefinition:       definitionRepo,
		ChartConfig:           chartConfigRepo,
		StackInstance:         instanceRepo,
		ValueOverride:         overrideRepo,
		ChartBranchOverride:   branchOverrideRepo,
		AuditLog:              auditRepo,
		APIKey:                apiKeyRepo,
		DeploymentLog:         deployLogRepo,
		SharedValues:          sharedValuesRepo,
		ResourceQuota:         quotaRepo,
		InstanceQuotaOverride: quotaOverrideRepo,
		TemplateVersion:       templateVersionRepo,
		Notification:          notificationRepo,
		UserFavorite:          favoriteRepo,
		CleanupPolicy:         cleanupPolicyRepo,
		Cluster:               clusterRepo,
		TxRunner:              noOpTx,
	}, nil
}

func newGORMRepositorySet(cfg *config.Config, db *gorm.DB) (*RepositorySet, error) {
	if db == nil {
		return nil, fmt.Errorf("GORM database connection is required when USE_AZURE_TABLE is false")
	}

	// Repositories with GORM implementations.
	userRepo := NewGORMUserRepository(db)
	auditLogRepo := NewGORMAuditLogRepository(db)
	apiKeyRepo := NewGORMAPIKeyRepository(db)
	notificationRepo := NewGORMNotificationRepository(db)
	templateVersionRepo := NewGORMTemplateVersionRepository(db)
	resourceQuotaRepo := NewGORMResourceQuotaRepository(db)
	instanceQuotaOverrideRepo := NewGORMInstanceQuotaOverrideRepository(db)
	stackInstanceRepo := NewGORMStackInstanceRepository(db)
	stackTemplateRepo := NewGORMStackTemplateRepository(db)
	stackDefinitionRepo := NewGORMStackDefinitionRepository(db)
	chartConfigRepo := NewGORMChartConfigRepository(db)
	templateChartConfigRepo := NewGORMTemplateChartConfigRepository(db)
	valueOverrideRepo := NewGORMValueOverrideRepository(db)
	chartBranchOverrideRepo := NewGORMChartBranchOverrideRepository(db)
	clusterRepo := NewGORMClusterRepository(db, cfg.Deployment.KubeconfigEncryptionKey)
	deploymentLogRepo := NewGORMDeploymentLogRepository(db)
	sharedValuesRepo := NewGORMSharedValuesRepository(db)
	userFavoriteRepo := NewGORMUserFavoriteRepository(db)
	cleanupPolicyRepo := NewGORMCleanupPolicyRepository(db)

	slog.Info("Using GORM repositories for all domain entities")

	return &RepositorySet{
		User:                  userRepo,
		AuditLog:              auditLogRepo,
		APIKey:                apiKeyRepo,
		Notification:          notificationRepo,
		TemplateVersion:       templateVersionRepo,
		ResourceQuota:         resourceQuotaRepo,
		InstanceQuotaOverride: instanceQuotaOverrideRepo,
		StackInstance:         stackInstanceRepo,
		StackTemplate:         stackTemplateRepo,
		StackDefinition:       stackDefinitionRepo,
		ChartConfig:           chartConfigRepo,
		TemplateChartConfig:   templateChartConfigRepo,
		ValueOverride:         valueOverrideRepo,
		ChartBranchOverride:   chartBranchOverrideRepo,
		Cluster:               clusterRepo,
		DeploymentLog:         deploymentLogRepo,
		SharedValues:          sharedValuesRepo,
		UserFavorite:          userFavoriteRepo,
		CleanupPolicy:         cleanupPolicyRepo,
		TxRunner:              NewGORMTxRunner(db),
	}, nil
}

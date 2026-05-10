package database

import (
	"fmt"
	"log/slog"

	"backend/internal/config"
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
	RefreshToken          models.RefreshTokenRepository
	NotificationChannel   models.NotificationChannelRepository
	TxRunner              TxRunner
}

// NewRepositorySet creates all domain-specific GORM repositories.
func NewRepositorySet(cfg *config.Config, db *gorm.DB) (*RepositorySet, error) {
	return newGORMRepositorySet(cfg, db)
}

func newGORMRepositorySet(cfg *config.Config, db *gorm.DB) (*RepositorySet, error) {
	if db == nil {
		return nil, fmt.Errorf("GORM database connection is required")
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
	refreshTokenRepo := NewGORMRefreshTokenRepository(db)
	notificationChannelRepo := NewGORMNotificationChannelRepository(db)

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
		RefreshToken:          refreshTokenRepo,
		NotificationChannel:   notificationChannelRepo,
		TxRunner:              NewGORMTxRunner(db),
	}, nil
}

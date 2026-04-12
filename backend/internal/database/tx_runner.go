package database

import (
	"backend/internal/models"

	"gorm.io/gorm"
)

// TxRunner executes a function within a database transaction.
// All repositories in the callback share the same *gorm.DB transaction.
type TxRunner interface {
	// RunInTx executes fn within a transaction. If fn returns an error, the
	// transaction is rolled back. The TxRepos provides transactional repository
	// instances that share the same underlying connection.
	RunInTx(fn func(repos TxRepos) error) error
}

// TxRepos provides access to repository instances that share a transaction.
// Only include the repositories actually needed for transactional operations.
type TxRepos struct {
	StackDefinition models.StackDefinitionRepository
	ChartConfig     models.ChartConfigRepository
	StackInstance   models.StackInstanceRepository
	StackTemplate   models.StackTemplateRepository
	TemplateChart   models.TemplateChartConfigRepository
	ValueOverride   models.ValueOverrideRepository
	BranchOverride  models.ChartBranchOverrideRepository
	DeploymentLog   models.DeploymentLogRepository
}

// GORMTxRunner implements TxRunner using GORM database transactions.
// Each call to RunInTx creates a new database transaction and constructs
// fresh repository instances that share the transactional *gorm.DB.
type GORMTxRunner struct {
	db *gorm.DB
}

// NewGORMTxRunner creates a GORMTxRunner backed by the given database connection.
func NewGORMTxRunner(db *gorm.DB) *GORMTxRunner {
	return &GORMTxRunner{db: db}
}

// RunInTx executes fn within a GORM transaction. All repositories in the
// TxRepos share the same transactional *gorm.DB, so updates are atomic.
// If fn returns an error, the transaction is rolled back automatically.
func (r *GORMTxRunner) RunInTx(fn func(repos TxRepos) error) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		repos := TxRepos{
			StackDefinition: NewGORMStackDefinitionRepository(tx),
			ChartConfig:     NewGORMChartConfigRepository(tx),
			StackInstance:   NewGORMStackInstanceRepository(tx),
			StackTemplate:   NewGORMStackTemplateRepository(tx),
			TemplateChart:   NewGORMTemplateChartConfigRepository(tx),
			ValueOverride:   NewGORMValueOverrideRepository(tx),
			BranchOverride:  NewGORMChartBranchOverrideRepository(tx),
			DeploymentLog:   NewGORMDeploymentLogRepository(tx),
		}
		return fn(repos)
	})
}

package database

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"backend/internal/models"
	"backend/pkg/crypto"
	"backend/pkg/dberrors"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Compile-time interface check.
var _ models.ClusterRepository = (*GORMClusterRepository)(nil)

// GORMClusterRepository implements models.ClusterRepository using GORM.
type GORMClusterRepository struct {
	db            *gorm.DB
	encryptionKey []byte // nil or empty means encryption disabled
}

// NewGORMClusterRepository creates a new GORM-backed cluster repository.
func NewGORMClusterRepository(db *gorm.DB, encryptionKey string) *GORMClusterRepository {
	repo := &GORMClusterRepository{db: db}
	if encryptionKey != "" {
		repo.encryptionKey = crypto.DeriveKey(encryptionKey)
	} else {
		slog.Warn("KUBECONFIG_ENCRYPTION_KEY is not set — clusters with kubeconfig_data will be rejected; use kubeconfig_path instead")
	}
	return repo
}

// encryptKubeconfig encrypts KubeconfigData in-place before persisting.
// Returns the original plaintext so the caller can restore it after the DB write.
func (r *GORMClusterRepository) encryptKubeconfig(cluster *models.Cluster) (string, error) {
	original := cluster.KubeconfigData
	if original == "" {
		return "", nil
	}
	if len(r.encryptionKey) == 0 {
		return "", dberrors.NewDatabaseError("validation",
			fmt.Errorf("kubeconfig_data cannot be stored without KUBECONFIG_ENCRYPTION_KEY configured; use kubeconfig_path instead: %w", dberrors.ErrValidation))
	}
	encrypted, err := crypto.Encrypt([]byte(original), r.encryptionKey)
	if err != nil {
		return "", dberrors.NewDatabaseError("encrypt", err)
	}
	cluster.KubeconfigData = base64.StdEncoding.EncodeToString(encrypted)
	return original, nil
}

// decryptKubeconfig decrypts KubeconfigData in-place after reading from DB.
func (r *GORMClusterRepository) decryptKubeconfig(cluster *models.Cluster) error {
	if cluster.KubeconfigData == "" || len(r.encryptionKey) == 0 {
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(cluster.KubeconfigData)
	if err != nil {
		// Not base64; assume plaintext (pre-encryption data).
		return nil
	}
	decrypted, err := crypto.Decrypt(decoded, r.encryptionKey)
	if err != nil {
		return dberrors.NewDatabaseError("decrypt kubeconfig data", err)
	}
	cluster.KubeconfigData = string(decrypted)
	return nil
}

// Create inserts a new cluster record.
func (r *GORMClusterRepository) Create(cluster *models.Cluster) error {
	if cluster.ID == "" {
		cluster.ID = uuid.New().String()
	}
	if cluster.HealthStatus == "" {
		cluster.HealthStatus = models.ClusterUnreachable
	}
	now := time.Now().UTC()
	cluster.CreatedAt = now
	cluster.UpdatedAt = now

	original, err := r.encryptKubeconfig(cluster)
	if err != nil {
		return err
	}

	dbErr := r.db.Create(cluster).Error

	// Restore plaintext so the caller's struct is not mutated.
	if original != "" {
		cluster.KubeconfigData = original
	}

	if dbErr != nil {
		if isDuplicateKeyError(dbErr) {
			return dberrors.NewDatabaseError("create", dberrors.ErrDuplicateKey)
		}
		return dberrors.NewDatabaseError("create", dbErr)
	}
	return nil
}

// FindByID returns a cluster by its ID.
func (r *GORMClusterRepository) FindByID(id string) (*models.Cluster, error) {
	var cluster models.Cluster
	if err := r.db.Where("id = ?", id).First(&cluster).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("find_by_id", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("find_by_id", err)
	}
	if err := r.decryptKubeconfig(&cluster); err != nil {
		return nil, err
	}
	return &cluster, nil
}

// Update persists changes to an existing cluster record.
func (r *GORMClusterRepository) Update(cluster *models.Cluster) error {
	cluster.UpdatedAt = time.Now().UTC()

	original, err := r.encryptKubeconfig(cluster)
	if err != nil {
		return err
	}

	dbErr := r.db.Save(cluster).Error

	// Restore plaintext so the caller's struct is not mutated.
	if original != "" {
		cluster.KubeconfigData = original
	}

	if dbErr != nil {
		if isDuplicateKeyError(dbErr) {
			return dberrors.NewDatabaseError("update", dberrors.ErrDuplicateKey)
		}
		return dberrors.NewDatabaseError("update", dbErr)
	}
	return nil
}

// Delete removes a cluster by ID.
func (r *GORMClusterRepository) Delete(id string) error {
	result := r.db.Where("id = ?", id).Delete(&models.Cluster{})
	if result.Error != nil {
		return dberrors.NewDatabaseError("delete", result.Error)
	}
	if result.RowsAffected == 0 {
		return dberrors.NewDatabaseError("delete", dberrors.ErrNotFound)
	}
	return nil
}

// List returns all clusters.
func (r *GORMClusterRepository) List() ([]models.Cluster, error) {
	var clusters []models.Cluster
	if err := r.db.Find(&clusters).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list", err)
	}
	for i := range clusters {
		if err := r.decryptKubeconfig(&clusters[i]); err != nil {
			return nil, err
		}
	}
	return clusters, nil
}

// FindDefault returns the cluster marked as default.
func (r *GORMClusterRepository) FindDefault() (*models.Cluster, error) {
	var cluster models.Cluster
	if err := r.db.Where("is_default = ?", true).First(&cluster).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("find_default", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("find_default", err)
	}
	if err := r.decryptKubeconfig(&cluster); err != nil {
		return nil, err
	}
	return &cluster, nil
}

// SetDefault unsets all existing defaults and marks the given cluster as default.
func (r *GORMClusterRepository) SetDefault(id string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		// Lock existing default rows to prevent concurrent SetDefault races.
		var existing []models.Cluster
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("is_default = ?", true).Find(&existing).Error; err != nil {
			return dberrors.NewDatabaseError("set_default", err)
		}

		// Unset all current defaults.
		if len(existing) > 0 {
			if err := tx.Model(&models.Cluster{}).
				Where("is_default = ?", true).
				Update("is_default", false).Error; err != nil {
				return dberrors.NewDatabaseError("set_default", err)
			}
		}

		// Set the target cluster as default.
		result := tx.Model(&models.Cluster{}).
			Where("id = ?", id).
			Updates(map[string]interface{}{
				"is_default": true,
				"updated_at": time.Now().UTC(),
			})
		if result.Error != nil {
			return dberrors.NewDatabaseError("set_default", result.Error)
		}
		if result.RowsAffected == 0 {
			return dberrors.NewDatabaseError("set_default", dberrors.ErrNotFound)
		}
		return nil
	})
}

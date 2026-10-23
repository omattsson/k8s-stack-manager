package database

import (
	"context"
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

// encryptRegistryPassword encrypts RegistryPassword in-place before persisting.
// Returns the original plaintext so the caller can restore it after the DB write.
func (r *GORMClusterRepository) encryptRegistryPassword(cluster *models.Cluster) (string, error) {
	original := cluster.RegistryPassword
	if original == "" || len(r.encryptionKey) == 0 {
		return original, nil
	}
	encrypted, err := crypto.Encrypt([]byte(original), r.encryptionKey)
	if err != nil {
		return "", dberrors.NewDatabaseError("encrypt registry password", err)
	}
	cluster.RegistryPassword = base64.StdEncoding.EncodeToString(encrypted)
	return original, nil
}

// decryptRegistryPassword decrypts RegistryPassword in-place after reading from DB.
func (r *GORMClusterRepository) decryptRegistryPassword(cluster *models.Cluster) error {
	if cluster.RegistryPassword == "" || len(r.encryptionKey) == 0 {
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(cluster.RegistryPassword)
	if err != nil {
		return nil
	}
	decrypted, err := crypto.Decrypt(decoded, r.encryptionKey)
	if err != nil {
		return dberrors.NewDatabaseError("decrypt registry password", err)
	}
	cluster.RegistryPassword = string(decrypted)
	return nil
}

// Create inserts a new cluster record.
func (r *GORMClusterRepository) Create(ctx context.Context, cluster *models.Cluster) error {
	if cluster.ID == "" {
		cluster.ID = uuid.New().String()
	}
	if cluster.HealthStatus == "" {
		cluster.HealthStatus = models.ClusterUnreachable
	}
	now := time.Now().UTC()
	cluster.CreatedAt = now
	cluster.UpdatedAt = now

	originalKC, err := r.encryptKubeconfig(cluster)
	if err != nil {
		return err
	}
	originalRP, err := r.encryptRegistryPassword(cluster)
	if err != nil {
		if originalKC != "" {
			cluster.KubeconfigData = originalKC
		}
		return err
	}

	dbErr := r.db.WithContext(ctx).Create(cluster).Error

	// Restore plaintext so the caller's struct is not mutated.
	if originalKC != "" {
		cluster.KubeconfigData = originalKC
	}
	cluster.RegistryPassword = originalRP

	if dbErr != nil {
		// Create can run in a default transaction: keep the context error (#656).
		if ctx.Err() != nil {
			return withContextErr(ctx, "create", dbErr)
		}
		if isDuplicateKeyError(dbErr) {
			return dberrors.NewDatabaseError("create", dberrors.ErrDuplicateKey)
		}
		return dberrors.NewDatabaseError("create", dbErr)
	}
	return nil
}

// FindByID returns a cluster by its ID.
func (r *GORMClusterRepository) FindByID(ctx context.Context, id string) (*models.Cluster, error) {
	var cluster models.Cluster
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&cluster).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("find_by_id", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("find_by_id", err)
	}
	if err := r.decryptKubeconfig(&cluster); err != nil {
		return nil, err
	}
	if err := r.decryptRegistryPassword(&cluster); err != nil {
		return nil, err
	}
	return &cluster, nil
}

// Update persists changes to an existing cluster record. It never writes
// is_default or health_status: SetDefault and UpdateHealthStatus own them, so
// a stale full row cannot undo their writes (#678).
func (r *GORMClusterRepository) Update(ctx context.Context, cluster *models.Cluster) error {
	cluster.UpdatedAt = time.Now().UTC()

	originalKC, err := r.encryptKubeconfig(cluster)
	if err != nil {
		return err
	}
	originalRP, err := r.encryptRegistryPassword(cluster)
	if err != nil {
		if originalKC != "" {
			cluster.KubeconfigData = originalKC
		}
		return err
	}

	dbErr := r.db.WithContext(ctx).Omit("is_default", "health_status").Save(cluster).Error

	// Restore plaintext so the caller's struct is not mutated.
	if originalKC != "" {
		cluster.KubeconfigData = originalKC
	}
	cluster.RegistryPassword = originalRP

	if dbErr != nil {
		// Save can run in a default transaction: keep the context error (#656).
		if ctx.Err() != nil {
			return withContextErr(ctx, "update", dbErr)
		}
		if isDuplicateKeyError(dbErr) {
			return dberrors.NewDatabaseError("update", dberrors.ErrDuplicateKey)
		}
		return dberrors.NewDatabaseError("update", dbErr)
	}
	return nil
}

// UpdateHealthStatus writes health_status and updated_at of one cluster.
func (r *GORMClusterRepository) UpdateHealthStatus(ctx context.Context, id, status string) error {
	result := r.db.WithContext(ctx).Model(&models.Cluster{}).Where("id = ?", id).
		UpdateColumns(map[string]any{"health_status": status, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		if ctx.Err() != nil {
			return withContextErr(ctx, "update_health_status", result.Error)
		}
		return dberrors.NewDatabaseError("update_health_status", result.Error)
	}
	if result.RowsAffected == 0 {
		return dberrors.NewDatabaseError("update_health_status", dberrors.ErrNotFound)
	}
	return nil
}

// Delete removes a cluster by ID.
func (r *GORMClusterRepository) Delete(ctx context.Context, id string) error {
	result := r.db.WithContext(ctx).Where("id = ?", id).Delete(&models.Cluster{})
	if result.Error != nil {
		// Delete can run in a default transaction: keep the context error (#656).
		if ctx.Err() != nil {
			return withContextErr(ctx, "delete", result.Error)
		}
		return dberrors.NewDatabaseError("delete", result.Error)
	}
	if result.RowsAffected == 0 {
		return dberrors.NewDatabaseError("delete", dberrors.ErrNotFound)
	}
	return nil
}

// NamesByIDs returns the name of each cluster in ids that exists, keyed by
// ID. It selects only the id and name columns, so it does not decrypt the
// kubeconfig or the registry password.
func (r *GORMClusterRepository) NamesByIDs(ctx context.Context, ids []string) (map[string]string, error) {
	return namesByIDs(r.db.WithContext(ctx), &models.Cluster{}, ids)
}

// List returns all clusters.
func (r *GORMClusterRepository) List(ctx context.Context) ([]models.Cluster, error) {
	var clusters []models.Cluster
	if err := r.db.WithContext(ctx).Find(&clusters).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list", err)
	}
	for i := range clusters {
		if err := r.decryptKubeconfig(&clusters[i]); err != nil {
			return nil, err
		}
		if err := r.decryptRegistryPassword(&clusters[i]); err != nil {
			return nil, err
		}
	}
	return clusters, nil
}

// CountAll returns the total number of clusters using a projected aggregate
// query, so it does not load or decrypt cluster secrets like List() does.
func (r *GORMClusterRepository) CountAll(ctx context.Context) (int, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.Cluster{}).Count(&count).Error; err != nil {
		return 0, dberrors.NewDatabaseError("count_all", err)
	}
	return int(count), nil
}

// CountByHealthStatus returns the number of clusters with the given health
// status using a projected aggregate query (no secret decryption).
func (r *GORMClusterRepository) CountByHealthStatus(ctx context.Context, status string) (int, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.Cluster{}).
		Where("health_status = ?", status).
		Count(&count).Error; err != nil {
		return 0, dberrors.NewDatabaseError("count_by_health_status", err)
	}
	return int(count), nil
}

// FindDefault returns the cluster marked as default.
func (r *GORMClusterRepository) FindDefault(ctx context.Context) (*models.Cluster, error) {
	var cluster models.Cluster
	if err := r.db.WithContext(ctx).Where("is_default = ?", true).First(&cluster).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("find_default", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("find_default", err)
	}
	if err := r.decryptKubeconfig(&cluster); err != nil {
		return nil, err
	}
	if err := r.decryptRegistryPassword(&cluster); err != nil {
		return nil, err
	}
	return &cluster, nil
}

// SetDefault unsets all existing defaults and marks the given cluster as default.
func (r *GORMClusterRepository) SetDefault(ctx context.Context, id string) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
	return withContextErr(ctx, "set_default", err)
}

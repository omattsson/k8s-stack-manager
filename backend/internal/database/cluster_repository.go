package database

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
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
	// warnedUnreadable holds the cluster IDs whose unreadable password was
	// already logged by this process. A repairing save removes the entry.
	warnedUnreadable sync.Map
}

// NewGORMClusterRepository creates a new GORM-backed cluster repository.
func NewGORMClusterRepository(db *gorm.DB, encryptionKey string) *GORMClusterRepository {
	repo := &GORMClusterRepository{db: db}
	if encryptionKey != "" {
		repo.encryptionKey = crypto.DeriveKey(encryptionKey)
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
		return "", errKubeconfigNoKey()
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
	if original == "" {
		return original, nil
	}
	if len(r.encryptionKey) == 0 {
		return "", errRegistryPasswordNoKey()
	}
	encrypted, err := crypto.Encrypt([]byte(original), r.encryptionKey)
	if err != nil {
		return "", dberrors.NewDatabaseError("encrypt registry password", err)
	}
	cluster.RegistryPassword = encryptedPrefix + base64.StdEncoding.EncodeToString(encrypted)
	return original, nil
}

func errKubeconfigNoKey() error {
	return dberrors.NewDatabaseError("validation",
		fmt.Errorf("kubeconfig_data cannot be stored without KUBECONFIG_ENCRYPTION_KEY configured; use kubeconfig_path instead: %w", dberrors.ErrValidation))
}

func errRegistryPasswordNoKey() error {
	return dberrors.NewDatabaseError("validation",
		fmt.Errorf("registry_password cannot be stored without KUBECONFIG_ENCRYPTION_KEY configured: %w", dberrors.ErrValidation))
}

// encryptedPrefix marks a registry password that this version encrypted.
// An unprefixed value is legacy plaintext, or ciphertext of an older version.
const encryptedPrefix = "enc:v1:"

// minCiphertextLen is the AES-GCM nonce plus the tag: a shorter value is not ciphertext.
const minCiphertextLen = 12 + 16

// storedRegistryPassword reads the stored (not decrypted) registry password.
func (r *GORMClusterRepository) storedRegistryPassword(ctx context.Context, id string) (string, error) {
	var stored string
	err := r.db.WithContext(ctx).Model(&models.Cluster{}).Where("id = ?", id).
		Select("COALESCE(registry_password, '')").Limit(1).Scan(&stored).Error
	if err != nil {
		return "", dberrors.NewDatabaseError("find_by_id", err)
	}
	return stored, nil
}

// keepLegacyPassword reports whether an update without a key keeps the stored
// legacy plaintext password unchanged, so other edits still work.
func (r *GORMClusterRepository) keepLegacyPassword(ctx context.Context, cluster *models.Cluster) (bool, error) {
	if cluster.RegistryPassword == "" || len(r.encryptionKey) != 0 {
		return false, nil
	}
	stored, err := r.storedRegistryPassword(ctx, cluster.ID)
	if err != nil {
		return false, err
	}
	return stored == cluster.RegistryPassword, nil
}

// decryptRegistryPassword decrypts RegistryPassword in-place after reading from DB.
// A value that cannot be decrypted never fails the read and is never returned
// as a password: the password is empty and RegistryPasswordUnreadable is set.
func (r *GORMClusterRepository) decryptRegistryPassword(cluster *models.Cluster) error {
	stored := cluster.RegistryPassword
	if stored == "" {
		return nil
	}
	prefixed := strings.HasPrefix(stored, encryptedPrefix)
	if len(r.encryptionKey) == 0 {
		if prefixed {
			r.markUnreadable(cluster, "no encryption key")
		}
		// Unprefixed without a key: legacy plaintext.
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, encryptedPrefix))
	if err != nil || (!prefixed && len(decoded) < minCiphertextLen) {
		if prefixed {
			r.markUnreadable(cluster, "invalid encoding")
		}
		// Not ciphertext; legacy plaintext from before the key was required.
		return nil
	}
	decrypted, err := crypto.Decrypt(decoded, r.encryptionKey)
	if err != nil {
		r.markUnreadable(cluster, "decryption failed")
		return nil
	}
	cluster.RegistryPassword = string(decrypted)
	return nil
}

func (r *GORMClusterRepository) markUnreadable(cluster *models.Cluster, reason string) {
	cluster.RegistryPassword = ""
	cluster.RegistryPasswordUnreadable = true
	if _, seen := r.warnedUnreadable.LoadOrStore(cluster.ID, struct{}{}); !seen {
		slog.Warn("registry password cannot be decrypted; set it again, or remove the registry",
			"cluster_id", cluster.ID, "reason", reason)
	}
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
	keep, err := r.keepLegacyPassword(ctx, cluster)
	if err != nil {
		if originalKC != "" {
			cluster.KubeconfigData = originalKC
		}
		return err
	}
	originalRP := cluster.RegistryPassword
	if !keep {
		originalRP, err = r.encryptRegistryPassword(cluster)
		if err != nil {
			if originalKC != "" {
				cluster.KubeconfigData = originalKC
			}
			return err
		}
	}

	omit := []string{"is_default", "health_status"}
	if cluster.RegistryPasswordUnreadable && cluster.RegistryPassword == "" && cluster.RegistryURL != "" {
		// Defense in depth: UpdateCluster answers 400 first for an unreadable row.
		// This keeps the stored value that could not be read.
		omit = append(omit, "registry_password")
	}
	dbErr := r.db.WithContext(ctx).Omit(omit...).Save(cluster).Error

	// Restore plaintext so the caller's struct is not mutated.
	if originalKC != "" {
		cluster.KubeconfigData = originalKC
	}
	cluster.RegistryPassword = originalRP
	if dbErr == nil && !slices.Contains(omit, "registry_password") {
		r.warnedUnreadable.Delete(cluster.ID)
	}

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

// CountRegistryPasswordsWithoutKey counts the clusters that store a registry
// password when no key is set: cleartext (or an older unprefixed format) and
// encrypted by this version. With a key it returns zero.
func (r *GORMClusterRepository) CountRegistryPasswordsWithoutKey(ctx context.Context) (cleartext, encrypted int, err error) {
	if len(r.encryptionKey) != 0 {
		return 0, 0, nil
	}
	base := func() *gorm.DB {
		return r.db.WithContext(ctx).Model(&models.Cluster{}).
			Where("registry_password IS NOT NULL AND registry_password <> ''")
	}
	var total, enc int64
	if err = base().Count(&total).Error; err != nil {
		return 0, 0, dberrors.NewDatabaseError("count_registry_passwords", err)
	}
	if err = base().Where("registry_password LIKE ?", encryptedPrefix+"%").Count(&enc).Error; err != nil {
		return 0, 0, dberrors.NewDatabaseError("count_registry_passwords", err)
	}
	return int(total - enc), int(enc), nil
}

// CheckCredentialsStorable refuses kubeconfig_data and a registry_password
// that the repository cannot encrypt. An unchanged legacy password passes.
func (r *GORMClusterRepository) CheckCredentialsStorable(ctx context.Context, cluster *models.Cluster) error {
	if len(r.encryptionKey) != 0 {
		return nil
	}
	if cluster.KubeconfigData != "" {
		return errKubeconfigNoKey()
	}
	if cluster.RegistryPassword == "" {
		return nil
	}
	if cluster.ID != "" {
		keep, err := r.keepLegacyPassword(ctx, cluster)
		if err != nil {
			return err
		}
		if keep {
			return nil
		}
	}
	return errRegistryPasswordNoKey()
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

// clusterIDsPerQuery bounds the IN list of one StreamFlagsByIDs query.
const clusterIDsPerQuery = 500

// StreamFlagsByIDs returns allow_port_forward of the clusters in ids that
// exist, keyed by ID. It selects only id, is_default and allow_port_forward,
// so it never reads or decrypts the kubeconfig or the registry password and a
// damaged secret column cannot fail it. With includeDefault the flag of the
// default cluster is stored under models.DefaultClusterKey.
func (r *GORMClusterRepository) StreamFlagsByIDs(ctx context.Context, ids []string, includeDefault bool) (map[string]bool, error) {
	out := make(map[string]bool, len(ids)+1)
	type row struct {
		ID               string
		IsDefault        bool
		AllowPortForward bool
	}
	query := func(db *gorm.DB) ([]row, error) {
		var rows []row
		if err := db.Model(&models.Cluster{}).Select("id, is_default, allow_port_forward").Find(&rows).Error; err != nil {
			return nil, dberrors.NewDatabaseError("stream_flags_by_ids", err)
		}
		return rows, nil
	}
	for start := 0; start < len(ids); start += clusterIDsPerQuery {
		end := min(start+clusterIDsPerQuery, len(ids))
		rows, err := query(r.db.WithContext(ctx).Where("id IN ?", ids[start:end]))
		if err != nil {
			return nil, err
		}
		for _, rw := range rows {
			out[rw.ID] = rw.AllowPortForward
		}
	}
	if includeDefault {
		rows, err := query(r.db.WithContext(ctx).Where("is_default = ?", true).Limit(1))
		if err != nil {
			return nil, err
		}
		if len(rows) > 0 {
			out[models.DefaultClusterKey] = rows[0].AllowPortForward
		}
	}
	return out, nil
}

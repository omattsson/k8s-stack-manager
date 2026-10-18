package database

import (
	"context"
	"time"

	"backend/internal/models"

	"gorm.io/gorm"
)

// GORMRefreshTokenRepository implements models.RefreshTokenRepository using GORM.
type GORMRefreshTokenRepository struct {
	db *gorm.DB
}

// NewGORMRefreshTokenRepository creates a new GORM-backed refresh token repository.
func NewGORMRefreshTokenRepository(db *gorm.DB) *GORMRefreshTokenRepository {
	return &GORMRefreshTokenRepository{db: db}
}

func (r *GORMRefreshTokenRepository) Create(token *models.RefreshToken) error {
	return r.db.Create(token).Error
}

func (r *GORMRefreshTokenRepository) FindByTokenHash(hash string) (*models.RefreshToken, error) {
	var token models.RefreshToken
	if err := r.db.Where("token_hash = ?", hash).First(&token).Error; err != nil {
		return nil, err
	}
	return &token, nil
}

func (r *GORMRefreshTokenRepository) RevokeByID(id string) error {
	return r.db.Model(&models.RefreshToken{}).Where("id = ?", id).Update("revoked", true).Error
}

func (r *GORMRefreshTokenRepository) MarkRotatedIfActive(id string, rotatedAt time.Time) (int64, error) {
	tx := r.db.Model(&models.RefreshToken{}).
		Where("id = ? AND revoked = ?", id, false).
		Updates(map[string]interface{}{"revoked": true, "rotated_at": rotatedAt})
	return tx.RowsAffected, tx.Error
}

// RevokeFamily revokes the active tokens of a family. "id = ?" also matches a
// legacy token (empty family_id) that started the family.
func (r *GORMRefreshTokenRepository) RevokeFamily(familyID string) error {
	return r.db.Model(&models.RefreshToken{}).
		Where("(family_id = ? OR id = ?) AND revoked = ?", familyID, familyID, false).
		Update("revoked", true).Error
}

func (r *GORMRefreshTokenRepository) CountActiveInFamily(familyID string) (int64, error) {
	var count int64
	err := r.db.Model(&models.RefreshToken{}).
		Where("(family_id = ? OR id = ?) AND revoked = ? AND expires_at > ?", familyID, familyID, false, time.Now().UTC()).
		Count(&count).Error
	return count, err
}

// TouchFamily updates last_activity of the active tokens of a family. The
// "last_activity < ?" guard keeps a late write from moving the time back.
func (r *GORMRefreshTokenRepository) TouchFamily(ctx context.Context, familyID string, at time.Time) error {
	return r.db.WithContext(ctx).Model(&models.RefreshToken{}).
		Where("family_id = ? AND revoked = ? AND expires_at > ? AND last_activity < ?", familyID, false, at, at).
		Update("last_activity", at).Error
}

func (r *GORMRefreshTokenRepository) RevokeAllForUser(userID string) error {
	return r.db.Model(&models.RefreshToken{}).Where("user_id = ? AND revoked = ?", userID, false).Update("revoked", true).Error
}

func (r *GORMRefreshTokenRepository) RevokeAllForUserExcept(userID string, excludeID string) error {
	return r.db.Model(&models.RefreshToken{}).
		Where("user_id = ? AND revoked = ? AND id != ?", userID, false, excludeID).
		Update("revoked", true).Error
}

func (r *GORMRefreshTokenRepository) DeleteExpired() (int64, error) {
	tx := r.db.Where("expires_at < ?", time.Now().UTC()).Delete(&models.RefreshToken{})
	return tx.RowsAffected, tx.Error
}

func (r *GORMRefreshTokenRepository) CountActiveForUser(userID string) (int64, error) {
	var count int64
	err := r.db.Model(&models.RefreshToken{}).
		Where("user_id = ? AND revoked = ? AND expires_at > ?", userID, false, time.Now().UTC()).
		Count(&count).Error
	return count, err
}

func (r *GORMRefreshTokenRepository) WithTx(fn func(txRepo models.RefreshTokenRepository) error) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		return fn(NewGORMRefreshTokenRepository(tx))
	})
}

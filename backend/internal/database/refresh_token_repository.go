package database

import (
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

func (r *GORMRefreshTokenRepository) RevokeByIDIfActive(id string) (int64, error) {
	tx := r.db.Model(&models.RefreshToken{}).Where("id = ? AND revoked = ?", id, false).Update("revoked", true)
	return tx.RowsAffected, tx.Error
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
	tx := r.db.Where("expires_at < ?", time.Now()).Delete(&models.RefreshToken{})
	return tx.RowsAffected, tx.Error
}

func (r *GORMRefreshTokenRepository) CountActiveForUser(userID string) (int64, error) {
	var count int64
	err := r.db.Model(&models.RefreshToken{}).
		Where("user_id = ? AND revoked = ? AND expires_at > ?", userID, false, time.Now()).
		Count(&count).Error
	return count, err
}

func (r *GORMRefreshTokenRepository) WithTx(fn func(txRepo models.RefreshTokenRepository) error) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		return fn(NewGORMRefreshTokenRepository(tx))
	})
}

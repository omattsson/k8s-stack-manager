package database

import (
	"time"

	"backend/internal/models"

	"gorm.io/gorm"
)

// GormRefreshTokenRepository implements models.RefreshTokenRepository using GORM.
type GormRefreshTokenRepository struct {
	db *gorm.DB
}

// NewGormRefreshTokenRepository creates a new GORM-backed refresh token repository.
func NewGormRefreshTokenRepository(db *gorm.DB) *GormRefreshTokenRepository {
	return &GormRefreshTokenRepository{db: db}
}

func (r *GormRefreshTokenRepository) Create(token *models.RefreshToken) error {
	return r.db.Create(token).Error
}

func (r *GormRefreshTokenRepository) FindByTokenHash(hash string) (*models.RefreshToken, error) {
	var token models.RefreshToken
	if err := r.db.Where("token_hash = ?", hash).First(&token).Error; err != nil {
		return nil, err
	}
	return &token, nil
}

func (r *GormRefreshTokenRepository) RevokeByID(id string) error {
	return r.db.Model(&models.RefreshToken{}).Where("id = ?", id).Update("revoked", true).Error
}

func (r *GormRefreshTokenRepository) RevokeAllForUser(userID string) error {
	return r.db.Model(&models.RefreshToken{}).Where("user_id = ? AND revoked = ?", userID, false).Update("revoked", true).Error
}

func (r *GormRefreshTokenRepository) DeleteExpired() (int64, error) {
	tx := r.db.Where("expires_at < ?", time.Now()).Delete(&models.RefreshToken{})
	return tx.RowsAffected, tx.Error
}

func (r *GormRefreshTokenRepository) CountActiveForUser(userID string) (int64, error) {
	var count int64
	err := r.db.Model(&models.RefreshToken{}).
		Where("user_id = ? AND revoked = ? AND expires_at > ?", userID, false, time.Now()).
		Count(&count).Error
	return count, err
}

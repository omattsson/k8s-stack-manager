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

func (r *GORMRefreshTokenRepository) Create(ctx context.Context, token *models.RefreshToken) error {
	return r.db.WithContext(ctx).Create(token).Error
}

func (r *GORMRefreshTokenRepository) FindByTokenHash(ctx context.Context, hash string) (*models.RefreshToken, error) {
	var token models.RefreshToken
	if err := r.db.WithContext(ctx).Where("token_hash = ?", hash).First(&token).Error; err != nil {
		return nil, err
	}
	return &token, nil
}

func (r *GORMRefreshTokenRepository) RevokeByID(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Model(&models.RefreshToken{}).Where("id = ?", id).Update("revoked", true).Error
}

func (r *GORMRefreshTokenRepository) MarkRotatedIfActive(ctx context.Context, id string, rotatedAt time.Time) (int64, error) {
	tx := r.db.WithContext(ctx).Model(&models.RefreshToken{}).
		Where("id = ? AND revoked = ?", id, false).
		Updates(map[string]interface{}{"revoked": true, "rotated_at": rotatedAt})
	return tx.RowsAffected, tx.Error
}

// RevokeFamily revokes the active tokens of a family. "id = ?" also matches a
// legacy token (empty family_id) that started the family.
func (r *GORMRefreshTokenRepository) RevokeFamily(ctx context.Context, familyID string) error {
	return r.db.WithContext(ctx).Model(&models.RefreshToken{}).
		Where("(family_id = ? OR id = ?) AND revoked = ?", familyID, familyID, false).
		Update("revoked", true).Error
}

func (r *GORMRefreshTokenRepository) CountActiveInFamily(ctx context.Context, familyID string) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&models.RefreshToken{}).
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

func (r *GORMRefreshTokenRepository) RevokeAllForUser(ctx context.Context, userID string) error {
	return r.db.WithContext(ctx).Model(&models.RefreshToken{}).Where("user_id = ? AND revoked = ?", userID, false).Update("revoked", true).Error
}

func (r *GORMRefreshTokenRepository) RevokeAllForUserExcept(ctx context.Context, userID string, excludeID string) error {
	return r.db.WithContext(ctx).Model(&models.RefreshToken{}).
		Where("user_id = ? AND revoked = ? AND id != ?", userID, false, excludeID).
		Update("revoked", true).Error
}

// RevokeOldestForUser revokes the active tokens of the user except the keep
// newest ones and excludeID. It reads the IDs first (MySQL refuses LIMIT in an
// IN subquery); a token created after the read is newer than every revoked one
// and stays active.
func (r *GORMRefreshTokenRepository) RevokeOldestForUser(ctx context.Context, userID string, keep int, excludeID string) (int64, error) {
	if keep < 0 {
		keep = 0
	}
	db := r.db.WithContext(ctx)
	q := db.Model(&models.RefreshToken{}).
		Where("user_id = ? AND revoked = ? AND expires_at > ?", userID, false, time.Now().UTC())
	if excludeID != "" {
		q = q.Where("id <> ?", excludeID)
	}
	var ids []string
	if err := q.Order("created_at DESC").Order("id DESC").Offset(keep).Limit(maxRevokeBatch).Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	tx := db.Model(&models.RefreshToken{}).
		Where("id IN ? AND revoked = ?", ids, false).
		Update("revoked", true)
	return tx.RowsAffected, tx.Error
}

// maxRevokeBatch bounds the IDs of one RevokeOldestForUser call. A user has at
// most MAX_REFRESH_TOKENS_PER_USER active tokens plus a few concurrent ones.
const maxRevokeBatch = 1000

func (r *GORMRefreshTokenRepository) DeleteExpired(ctx context.Context) (int64, error) {
	tx := r.db.WithContext(ctx).Where("expires_at < ?", time.Now().UTC()).Delete(&models.RefreshToken{})
	return tx.RowsAffected, tx.Error
}

func (r *GORMRefreshTokenRepository) CountActiveForUser(ctx context.Context, userID string) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&models.RefreshToken{}).
		Where("user_id = ? AND revoked = ? AND expires_at > ?", userID, false, time.Now().UTC()).
		Count(&count).Error
	return count, err
}

// WithTx runs fn in a transaction with ctx. A cancel inside the transaction
// can surface as sql.ErrTxDone; withContextErr keeps the context error (499).
func (r *GORMRefreshTokenRepository) WithTx(ctx context.Context, fn func(txRepo models.RefreshTokenRepository) error) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(NewGORMRefreshTokenRepository(tx))
	})
	return withContextErr(ctx, "refresh token transaction", err)
}

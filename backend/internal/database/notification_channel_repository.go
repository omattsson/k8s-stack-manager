package database

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"backend/internal/models"
	"backend/pkg/crypto"
	"backend/pkg/dberrors"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Compile-time interface check.
var _ models.NotificationChannelRepository = (*GORMNotificationChannelRepository)(nil)

// GORMNotificationChannelRepository implements NotificationChannelRepository using GORM.
type GORMNotificationChannelRepository struct {
	db            *gorm.DB
	encryptionKey []byte
	// warnedUnreadable holds "<channel ID>/<field>" keys that this process
	// already logged. A repairing save removes the entry.
	warnedUnreadable sync.Map
}

// NewGORMNotificationChannelRepository creates a new GORM-backed notification channel repository.
func NewGORMNotificationChannelRepository(db *gorm.DB, encryptionKey string) *GORMNotificationChannelRepository {
	repo := &GORMNotificationChannelRepository{db: db}
	if encryptionKey != "" {
		repo.encryptionKey = crypto.DeriveKey(encryptionKey)
	}
	return repo
}

func errChannelSecretNoKey() error {
	return dberrors.NewDatabaseError("validation",
		fmt.Errorf("secret cannot be stored without KUBECONFIG_ENCRYPTION_KEY configured; remove the secret to send unsigned requests: %w", dberrors.ErrValidation))
}

// storedSecret reads the stored (not decrypted) secret.
func (r *GORMNotificationChannelRepository) storedSecret(ctx context.Context, id string) (string, error) {
	var stored string
	err := r.db.WithContext(ctx).Model(&models.NotificationChannel{}).Where("id = ?", id).
		Select("COALESCE(secret, '')").Limit(1).Scan(&stored).Error
	if err != nil {
		return "", dberrors.NewDatabaseError("get_channel", err)
	}
	return stored, nil
}

// storableSecret returns the value to write for a new secret. keep is true
// when the secret is an unchanged legacy plaintext value that an update
// without a key leaves in place.
func (r *GORMNotificationChannelRepository) storableSecret(ctx context.Context, ch *models.NotificationChannel, existing bool) (value string, keep bool, err error) {
	if ch.Secret == "" {
		return "", false, nil
	}
	if len(r.encryptionKey) == 0 {
		if existing {
			stored, err := r.storedSecret(ctx, ch.ID)
			if err != nil {
				return "", false, err
			}
			if stored == ch.Secret {
				return "", true, nil
			}
		}
		return "", false, errChannelSecretNoKey()
	}
	sealed, err := sealValue(ch.Secret, r.encryptionKey)
	if err != nil {
		return "", false, dberrors.NewDatabaseError("encrypt_secret", err)
	}
	return sealed, false, nil
}

// storableURL encrypts the webhook URL when a key is set. Without a key the
// URL stays plaintext, because a channel needs it.
func (r *GORMNotificationChannelRepository) storableURL(url string) (string, error) {
	if len(r.encryptionKey) == 0 {
		return url, nil
	}
	sealed, err := sealValue(url, r.encryptionKey)
	if err != nil {
		return "", dberrors.NewDatabaseError("encrypt_webhook_url", err)
	}
	return sealed, nil
}

// decryptChannel decrypts the secret and the webhook URL in place. A value
// that cannot be read never fails the read and is never returned: it is empty
// and the matching Unreadable flag is set.
func (r *GORMNotificationChannelRepository) decryptChannel(ch *models.NotificationChannel) {
	plain, unreadable := openValue(ch.Secret, r.encryptionKey, true)
	if unreadable != "" {
		ch.Secret, ch.SecretUnreadable = "", true
		r.warnUnreadable(ch.ID, "secret", unreadable)
	} else {
		ch.Secret = plain
	}
	plain, unreadable = openValue(ch.WebhookURL, r.encryptionKey, false)
	if unreadable != "" {
		ch.WebhookURL, ch.WebhookURLUnreadable = "", true
		r.warnUnreadable(ch.ID, "webhook_url", unreadable)
	} else {
		ch.WebhookURL = plain
	}
}

func (r *GORMNotificationChannelRepository) warnUnreadable(id, field, reason string) {
	if _, seen := r.warnedUnreadable.LoadOrStore(id+"/"+field, struct{}{}); !seen {
		slog.Warn("channel value cannot be decrypted; the channel sends nothing until it is set again",
			"channel_id", id, "field", field, "reason", reason)
	}
}

// CountSecretsWithoutKey counts the channels that store a secret when no key
// is set: plaintext (or an older unprefixed format) and encrypted by this
// version. With a key it returns zero.
func (r *GORMNotificationChannelRepository) CountSecretsWithoutKey(ctx context.Context) (cleartext, encrypted int, err error) {
	if len(r.encryptionKey) != 0 {
		return 0, 0, nil
	}
	base := func() *gorm.DB {
		return r.db.WithContext(ctx).Model(&models.NotificationChannel{}).
			Where("secret IS NOT NULL AND secret <> ''")
	}
	var total, enc int64
	if err = base().Count(&total).Error; err != nil {
		return 0, 0, dberrors.NewDatabaseError("count_channel_secrets", err)
	}
	if err = base().Where("secret LIKE ?", encryptedPrefix+"%").Count(&enc).Error; err != nil {
		return 0, 0, dberrors.NewDatabaseError("count_channel_secrets", err)
	}
	return int(total - enc), int(enc), nil
}

// CreateChannel inserts a new notification channel.
func (r *GORMNotificationChannelRepository) CreateChannel(ctx context.Context, channel *models.NotificationChannel) error {
	if channel.Format == "" {
		channel.Format = models.NotificationChannelFormatGeneric
	}
	secret, _, err := r.storableSecret(ctx, channel, false)
	if err != nil {
		return err
	}
	url, err := r.storableURL(channel.WebhookURL)
	if err != nil {
		return err
	}
	origSecret, origURL := channel.Secret, channel.WebhookURL
	channel.Secret, channel.WebhookURL = secret, url
	defer func() { channel.Secret, channel.WebhookURL = origSecret, origURL }()
	if err := r.db.WithContext(ctx).Create(channel).Error; err != nil {
		if isDuplicateKeyError(err) {
			return dberrors.NewDatabaseError("create_channel", fmt.Errorf("%w", dberrors.ErrDuplicateKey))
		}
		return dberrors.NewDatabaseError("create_channel", err)
	}
	return nil
}

// GetChannel returns a single notification channel by ID.
func (r *GORMNotificationChannelRepository) GetChannel(ctx context.Context, id string) (*models.NotificationChannel, error) {
	var channel models.NotificationChannel
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&channel).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("get_channel", fmt.Errorf("%w", dberrors.ErrNotFound))
		}
		return nil, dberrors.NewDatabaseError("get_channel", err)
	}
	r.decryptChannel(&channel)
	return &channel, nil
}

// channelFormat returns the format to store: an empty format is generic.
func channelFormat(format string) string {
	if format == "" {
		return models.NotificationChannelFormatGeneric
	}
	return format
}

// UpdateChannel updates an existing notification channel. If secretChanged is
// true, the secret field is encrypted and persisted; otherwise it is left untouched
// to avoid double-encryption when re-saving a decrypted record. An empty
// WebhookURL (an unreadable stored URL that the caller did not set again) is
// left untouched too.
func (r *GORMNotificationChannelRepository) UpdateChannel(ctx context.Context, channel *models.NotificationChannel, secretChanged bool) error {
	updates := map[string]interface{}{
		"name":       channel.Name,
		"enabled":    channel.Enabled,
		"filters":    channel.Filters,
		"format":     channelFormat(channel.Format),
		"updated_at": time.Now().UTC(),
	}
	if channel.WebhookURL != "" {
		url, err := r.storableURL(channel.WebhookURL)
		if err != nil {
			return err
		}
		updates["webhook_url"] = url
	}
	if secretChanged {
		secret, keep, err := r.storableSecret(ctx, channel, true)
		if err != nil {
			return err
		}
		if !keep {
			updates["secret"] = secret
		}
	}
	result := r.db.WithContext(ctx).Model(&models.NotificationChannel{}).Where("id = ?", channel.ID).Updates(updates)
	if result.Error != nil {
		if isDuplicateKeyError(result.Error) {
			return dberrors.NewDatabaseError("update_channel", fmt.Errorf("%w", dberrors.ErrDuplicateKey))
		}
		return dberrors.NewDatabaseError("update_channel", result.Error)
	}
	if result.RowsAffected == 0 {
		return dberrors.NewDatabaseError("update_channel", fmt.Errorf("%w", dberrors.ErrNotFound))
	}
	if _, ok := updates["secret"]; ok {
		r.warnedUnreadable.Delete(channel.ID + "/secret")
	}
	if _, ok := updates["webhook_url"]; ok {
		r.warnedUnreadable.Delete(channel.ID + "/webhook_url")
	}
	return nil
}

// DeleteChannel removes a notification channel and its subscriptions by ID.
func (r *GORMNotificationChannelRepository) DeleteChannel(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Delete delivery logs, subscriptions, then channel.
		if err := tx.Where("channel_id = ?", id).Delete(&models.NotificationDeliveryLog{}).Error; err != nil {
			return dberrors.NewDatabaseError("delete_channel_logs", err)
		}
		if err := tx.Where("channel_id = ?", id).Delete(&models.NotificationChannelSubscription{}).Error; err != nil {
			return dberrors.NewDatabaseError("delete_channel_subscriptions", err)
		}
		result := tx.Where("id = ?", id).Delete(&models.NotificationChannel{})
		if result.Error != nil {
			return dberrors.NewDatabaseError("delete_channel", result.Error)
		}
		if result.RowsAffected == 0 {
			return dberrors.NewDatabaseError("delete_channel", fmt.Errorf("%w", dberrors.ErrNotFound))
		}
		return nil
	})
}

// ListChannels returns all notification channels with the webhook URL
// decrypted. The secret is json:"-" and never sent to API clients.
func (r *GORMNotificationChannelRepository) ListChannels(ctx context.Context) ([]models.NotificationChannel, error) {
	var channels []models.NotificationChannel
	if err := r.db.WithContext(ctx).Order("name ASC").Find(&channels).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_channels", err)
	}
	for i := range channels {
		r.decryptChannel(&channels[i])
	}
	return channels, nil
}

// ListEnabledChannels returns all enabled notification channels.
func (r *GORMNotificationChannelRepository) ListEnabledChannels(ctx context.Context) ([]models.NotificationChannel, error) {
	var channels []models.NotificationChannel
	if err := r.db.WithContext(ctx).Where("enabled = ?", true).Order("name ASC").Find(&channels).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_enabled_channels", err)
	}
	for i := range channels {
		r.decryptChannel(&channels[i])
	}
	return channels, nil
}

// SetSubscriptions replaces all subscriptions for a channel with the given event types.
func (r *GORMNotificationChannelRepository) SetSubscriptions(ctx context.Context, channelID string, eventTypes []string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Delete existing subscriptions.
		if err := tx.Where("channel_id = ?", channelID).Delete(&models.NotificationChannelSubscription{}).Error; err != nil {
			return dberrors.NewDatabaseError("delete_subscriptions", err)
		}
		// Bulk insert new subscriptions.
		if len(eventTypes) > 0 {
			subs := make([]models.NotificationChannelSubscription, len(eventTypes))
			for i, et := range eventTypes {
				subs[i] = models.NotificationChannelSubscription{
					ID:        uuid.New().String(),
					ChannelID: channelID,
					EventType: et,
				}
			}
			if err := tx.Create(&subs).Error; err != nil {
				return dberrors.NewDatabaseError("create_subscriptions", err)
			}
		}
		return nil
	})
}

// GetSubscriptions returns all subscriptions for a channel.
func (r *GORMNotificationChannelRepository) GetSubscriptions(ctx context.Context, channelID string) ([]models.NotificationChannelSubscription, error) {
	var subs []models.NotificationChannelSubscription
	if err := r.db.WithContext(ctx).Where("channel_id = ?", channelID).Find(&subs).Error; err != nil {
		return nil, dberrors.NewDatabaseError("get_subscriptions", err)
	}
	return subs, nil
}

// CountSubscriptionsByChannel returns subscription counts for all channels in one query.
func (r *GORMNotificationChannelRepository) CountSubscriptionsByChannel(ctx context.Context) (map[string]int, error) {
	type result struct {
		ChannelID string
		Count     int
	}
	var rows []result
	if err := r.db.WithContext(ctx).
		Model(&models.NotificationChannelSubscription{}).
		Select("channel_id, COUNT(*) as count").
		Group("channel_id").
		Scan(&rows).Error; err != nil {
		return nil, dberrors.NewDatabaseError("count_subscriptions", err)
	}
	counts := make(map[string]int, len(rows))
	for _, row := range rows {
		counts[row.ChannelID] = row.Count
	}
	return counts, nil
}

// FindChannelsByEvent returns all enabled channels subscribed to the given event type.
func (r *GORMNotificationChannelRepository) FindChannelsByEvent(ctx context.Context, eventType string) ([]models.NotificationChannel, error) {
	var channels []models.NotificationChannel
	err := r.db.WithContext(ctx).
		Joins("JOIN notification_channel_subscriptions ON notification_channel_subscriptions.channel_id = notification_channels.id").
		Where("notification_channel_subscriptions.event_type = ? AND notification_channels.enabled = ?", eventType, true).
		Find(&channels).Error
	if err != nil {
		return nil, dberrors.NewDatabaseError("find_channels_by_event", err)
	}
	for i := range channels {
		r.decryptChannel(&channels[i])
	}
	return channels, nil
}

// CreateDeliveryLog inserts a new delivery log record.
func (r *GORMNotificationChannelRepository) CreateDeliveryLog(ctx context.Context, log *models.NotificationDeliveryLog) error {
	if err := r.db.WithContext(ctx).Create(log).Error; err != nil {
		return dberrors.NewDatabaseError("create_delivery_log", err)
	}
	return nil
}

// ListDeliveryLogs returns paginated delivery logs for a channel, ordered by created_at DESC.
func (r *GORMNotificationChannelRepository) ListDeliveryLogs(ctx context.Context, channelID string, limit, offset int) ([]models.NotificationDeliveryLog, int64, error) {
	query := r.db.WithContext(ctx).Where("channel_id = ?", channelID)

	var total int64
	if err := query.Model(&models.NotificationDeliveryLog{}).Count(&total).Error; err != nil {
		return nil, 0, dberrors.NewDatabaseError("count_delivery_logs", err)
	}

	var logs []models.NotificationDeliveryLog
	if err := query.Order("created_at DESC").Limit(limit).Offset(offset).Find(&logs).Error; err != nil {
		return nil, 0, dberrors.NewDatabaseError("list_delivery_logs", err)
	}

	return logs, total, nil
}

package database

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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
	// warned holds "secret:<id>" and "webhook_url:<id>" of the unreadable
	// values that this process already logged.
	warned sync.Map
}

// NewGORMNotificationChannelRepository creates a new GORM-backed notification channel repository.
func NewGORMNotificationChannelRepository(db *gorm.DB, encryptionKey string) *GORMNotificationChannelRepository {
	repo := &GORMNotificationChannelRepository{db: db}
	if encryptionKey != "" {
		repo.encryptionKey = crypto.DeriveKey(encryptionKey)
	}
	return repo
}

// errChannelSecretNoKey is the 400 for a secret that cannot be stored without
// a key. The handler shows models.ErrChannelSecretNoKey as is.
func errChannelSecretNoKey() error {
	return dberrors.NewDatabaseError("validation",
		fmt.Errorf("%w: %w", models.ErrChannelSecretNoKey, dberrors.ErrValidation))
}

// storedState is the result of reading a stored (encrypted) value.
type storedState int

const (
	storedEmpty      storedState = iota
	storedDecrypted              // ciphertext of this or an older version, decrypted
	storedPlaintext              // legacy plaintext, used as is
	storedUnreadable             // prefixed or undecryptable: never used
)

// decodeStored reads a stored value. A value with encryptedPrefix is
// ciphertext; without a key, with bad encoding or with a failed decrypt it is
// unreadable. An unprefixed value is legacy plaintext, except that
// unprefixedMayBeCipher (the channel secret: older versions wrote unprefixed
// base64 ciphertext) makes the repository try to decrypt it when a key is set.
// A reason (never the value) is returned for an unreadable value.
func decodeStored(stored string, key []byte, unprefixedMayBeCipher bool) (string, storedState, string) {
	if stored == "" {
		return "", storedEmpty, ""
	}
	prefixed := strings.HasPrefix(stored, encryptedPrefix)
	if !prefixed && (!unprefixedMayBeCipher || len(key) == 0) {
		return stored, storedPlaintext, ""
	}
	if len(key) == 0 {
		return "", storedUnreadable, "no encryption key"
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, encryptedPrefix))
	if err != nil || (!prefixed && len(decoded) < minCiphertextLen) {
		if prefixed {
			return "", storedUnreadable, "invalid encoding"
		}
		return stored, storedPlaintext, ""
	}
	decrypted, err := crypto.Decrypt(decoded, key)
	if err != nil {
		return "", storedUnreadable, "decryption failed"
	}
	return string(decrypted), storedDecrypted, ""
}

func (r *GORMNotificationChannelRepository) encryptValue(plain string) (string, error) {
	encrypted, err := crypto.Encrypt([]byte(plain), r.encryptionKey)
	if err != nil {
		return "", err
	}
	return encryptedPrefix + base64.StdEncoding.EncodeToString(encrypted), nil
}

// warnOnce logs an unreadable value at most once per channel and field in
// this process. A save that writes the field resets it.
func (r *GORMNotificationChannelRepository) warnOnce(field, id, reason string) {
	if _, seen := r.warned.LoadOrStore(field+":"+id, struct{}{}); !seen {
		slog.Warn("channel "+field+" cannot be decrypted; set it again",
			"channel_id", id, "reason", reason)
	}
}

// decryptFields decrypts Secret and WebhookURL in place after a read. A value
// that cannot be decrypted never fails the read and is never returned: the
// field is empty and the matching Unreadable flag is set.
func (r *GORMNotificationChannelRepository) decryptFields(ch *models.NotificationChannel) {
	ch.SecretUnreadable, ch.WebhookURLUnreadable = false, false
	ch.StoredSecretRaw = ch.Secret
	secret, st, reason := decodeStored(ch.Secret, r.encryptionKey, true)
	ch.Secret = secret
	if st == storedUnreadable {
		ch.SecretUnreadable = true
		r.warnOnce("secret", ch.ID, reason)
	}
	// webhook_url: URLs were never encrypted before the prefix existed, so an
	// unprefixed value is always a plaintext URL.
	url, st, reason := decodeStored(ch.WebhookURL, r.encryptionKey, false)
	ch.WebhookURL = url
	if st == storedUnreadable {
		ch.WebhookURLUnreadable = true
		r.warnOnce("webhook_url", ch.ID, reason)
	}
}

// storedSecret reads the stored (not decrypted) secret of a channel.
func (r *GORMNotificationChannelRepository) storedSecret(ctx context.Context, id string) (string, error) {
	var stored string
	err := r.db.WithContext(ctx).Model(&models.NotificationChannel{}).Where("id = ?", id).
		Select("COALESCE(secret, '')").Limit(1).Scan(&stored).Error
	if err != nil {
		return "", dberrors.NewDatabaseError("get_channel", err)
	}
	return stored, nil
}

// secretToStore returns the value for the secret column. keep is true when
// the column stays as it is: without a key, a secret equal to the stored
// legacy plaintext is no change. An empty secret clears the column. A new
// secret without a key is refused (nothing is written yet).
func (r *GORMNotificationChannelRepository) secretToStore(ctx context.Context, ch *models.NotificationChannel, existing bool) (value string, keep bool, err error) {
	if ch.Secret == "" {
		return "", false, nil
	}
	if len(r.encryptionKey) == 0 {
		if existing {
			stored, serr := r.storedSecret(ctx, ch.ID)
			if serr != nil {
				return "", false, serr
			}
			if stored == ch.Secret && !strings.HasPrefix(stored, encryptedPrefix) {
				return "", true, nil
			}
		}
		return "", false, errChannelSecretNoKey()
	}
	enc, eerr := r.encryptValue(ch.Secret)
	if eerr != nil {
		return "", false, dberrors.NewDatabaseError("encrypt_secret", eerr)
	}
	return enc, false, nil
}

// urlToStore returns the value for the webhook_url column. With a key the URL
// is encrypted; without a key it is stored in plaintext. The URL is required
// for every channel (the secret is optional) and the Helm default key is
// empty, so refusing it would disable channels on installs without a key.
// Many URLs hold no token.
func (r *GORMNotificationChannelRepository) urlToStore(url string) (string, error) {
	if len(r.encryptionKey) == 0 || url == "" {
		return url, nil
	}
	enc, err := r.encryptValue(url)
	if err != nil {
		return "", dberrors.NewDatabaseError("encrypt_webhook_url", err)
	}
	return enc, nil
}

// CreateChannel inserts a new notification channel.
func (r *GORMNotificationChannelRepository) CreateChannel(ctx context.Context, channel *models.NotificationChannel) error {
	if channel.Format == "" {
		channel.Format = models.NotificationChannelFormatGeneric
	}
	secretValue, _, err := r.secretToStore(ctx, channel, false)
	if err != nil {
		return err
	}
	urlValue, err := r.urlToStore(channel.WebhookURL)
	if err != nil {
		return err
	}
	origSecret, origURL := channel.Secret, channel.WebhookURL
	channel.Secret, channel.WebhookURL = secretValue, urlValue
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
	r.decryptFields(&channel)
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
// true, the secret field is encrypted and persisted. Otherwise, with a key, a
// readable secret (decryptFields returns plaintext or "") is encrypted again
// on every save, so a legacy plaintext or old-format secret moves to the
// "enc:v1:" format with the next save (as the URL does). An unreadable
// secret, an empty secret and a save without a key leave the column as it is.
func (r *GORMNotificationChannelRepository) UpdateChannel(ctx context.Context, channel *models.NotificationChannel, secretChanged bool) error {
	updates := map[string]interface{}{
		"name":       channel.Name,
		"enabled":    channel.Enabled,
		"filters":    channel.Filters,
		"format":     channelFormat(channel.Format),
		"updated_at": time.Now().UTC(),
	}
	// Checks first: a refused request changes nothing.
	writeSecret := false
	if secretChanged {
		value, keep, err := r.secretToStore(ctx, channel, true)
		if err != nil {
			return err
		}
		if !keep {
			updates["secret"] = value
			writeSecret = true
		}
	} else if raw := channel.StoredSecretRaw; len(r.encryptionKey) > 0 && !channel.SecretUnreadable &&
		channel.Secret != "" && raw != "" && !strings.HasPrefix(raw, encryptedPrefix) {
		// Lazy migration of a legacy plaintext or old-format secret. It is
		// conditional on the value that was read, so a concurrent change of
		// the secret wins, and a prefixed secret is never rewritten.
		value, err := r.encryptValue(channel.Secret)
		if err != nil {
			return dberrors.NewDatabaseError("encrypt_secret", err)
		}
		updates["secret"] = gorm.Expr("CASE WHEN secret = ? THEN ? ELSE secret END", raw, value)
		writeSecret = true
	}
	// An unreadable URL that the caller did not replace stays as it is, so a
	// broken channel can still be renamed or disabled.
	writeURL := !channel.WebhookURLUnreadable || channel.WebhookURL != ""
	if writeURL {
		value, err := r.urlToStore(channel.WebhookURL)
		if err != nil {
			return err
		}
		updates["webhook_url"] = value
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
	if writeSecret {
		r.warned.Delete("secret:" + channel.ID)
		channel.SecretUnreadable = false
	}
	if writeURL {
		r.warned.Delete("webhook_url:" + channel.ID)
		channel.WebhookURLUnreadable = false
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

// ListChannels returns all notification channels. The webhook URL is
// decrypted for the API. The secret is decrypted too, to set SecretUnreadable.
func (r *GORMNotificationChannelRepository) ListChannels(ctx context.Context) ([]models.NotificationChannel, error) {
	var channels []models.NotificationChannel
	if err := r.db.WithContext(ctx).Order("name ASC").Find(&channels).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_channels", err)
	}
	for i := range channels {
		r.decryptFields(&channels[i])
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
		r.decryptFields(&channels[i])
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
		r.decryptFields(&channels[i])
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

// ChannelSecretStats counts the stored secrets and webhook URLs by state.
// It never logs or returns the values. PlaintextSecrets are secrets in
// plaintext. Without a key, an unprefixed secret that is valid base64 of at
// least minCiphertextLen bytes cannot be told apart from ciphertext of an
// older version (key removed later); it counts in MaybeCipherSecrets, not in
// PlaintextSecrets. UnreadableSecrets and UnreadableURLs cannot be decrypted
// with the current key. PlaintextURLs are unprefixed URLs.
func (r *GORMNotificationChannelRepository) ChannelSecretStats(ctx context.Context) (models.ChannelSecretStats, error) {
	var stats models.ChannelSecretStats
	var rows []struct{ Secret, WebhookURL string }
	if err := r.db.WithContext(ctx).Model(&models.NotificationChannel{}).
		Select("COALESCE(secret, '') AS secret, webhook_url").Scan(&rows).Error; err != nil {
		return stats, dberrors.NewDatabaseError("channel_secret_stats", err)
	}
	for _, row := range rows {
		switch _, st, _ := decodeStored(row.Secret, r.encryptionKey, true); st {
		case storedPlaintext:
			if len(r.encryptionKey) == 0 && looksLikeCiphertext(row.Secret) {
				stats.MaybeCipherSecrets++
			} else {
				stats.PlaintextSecrets++
			}
		case storedUnreadable:
			stats.UnreadableSecrets++
		}
		switch _, st, _ := decodeStored(row.WebhookURL, r.encryptionKey, false); st {
		case storedPlaintext:
			stats.PlaintextURLs++
		case storedUnreadable:
			stats.UnreadableURLs++
		}
	}
	return stats, nil
}

// looksLikeCiphertext reports whether an unprefixed value is valid base64 of
// at least minCiphertextLen bytes.
func looksLikeCiphertext(stored string) bool {
	if strings.HasPrefix(stored, encryptedPrefix) {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(stored)
	return err == nil && len(decoded) >= minCiphertextLen
}

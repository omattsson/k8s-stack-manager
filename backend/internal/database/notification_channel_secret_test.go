package database

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"backend/internal/models"
	"backend/pkg/crypto"
	"backend/pkg/dberrors"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const (
	channelKeyA = "channel-test-key-a-0123456789abcdef"
	channelKeyB = "channel-test-key-b-0123456789abcdef"
	channelURL  = "https://hooks.example.com/services/T000/B000/token-value"
)

func channelTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupTestDBWithAllTables(t)
	require.NoError(t, db.AutoMigrate(&models.NotificationChannel{}, &models.NotificationChannelSubscription{}, &models.NotificationDeliveryLog{}))
	return db
}

func storedChannelColumn(t *testing.T, db *gorm.DB, id, column string) string {
	t.Helper()
	var raw string
	require.NoError(t, db.Model(&models.NotificationChannel{}).Where("id = ?", id).Select("COALESCE("+column+", '')").Scan(&raw).Error)
	return raw
}

func seedRawChannel(t *testing.T, db *gorm.DB, id, url, secret string) {
	t.Helper()
	require.NoError(t, db.Create(&models.NotificationChannel{ID: id, Name: id, WebhookURL: url, Secret: secret, Enabled: true, Format: "generic"}).Error)
}

func legacyCiphertext(t *testing.T, plain, key string) string {
	t.Helper()
	enc, err := crypto.Encrypt([]byte(plain), crypto.DeriveKey(key))
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(enc)
}

func TestChannelRepository_SecretWithoutKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("create with a secret is refused and stores nothing", func(t *testing.T) {
		t.Parallel()
		db := channelTestDB(t)
		repo := NewGORMNotificationChannelRepository(db, "")
		ch := &models.NotificationChannel{ID: "c1", Name: "c1", WebhookURL: channelURL, Secret: "hmac-secret", Enabled: true}
		err := repo.CreateChannel(ctx, ch)
		require.Error(t, err)
		assert.True(t, errors.Is(err, dberrors.ErrValidation))
		assert.Contains(t, err.Error(), "KUBECONFIG_ENCRYPTION_KEY")
		assert.Equal(t, "hmac-secret", ch.Secret, "the caller struct is not changed")
		var n int64
		require.NoError(t, db.Model(&models.NotificationChannel{}).Count(&n).Error)
		assert.Zero(t, n)
	})

	t.Run("create without a secret works and the URL stays plaintext", func(t *testing.T) {
		t.Parallel()
		db := channelTestDB(t)
		repo := NewGORMNotificationChannelRepository(db, "")
		require.NoError(t, repo.CreateChannel(ctx, &models.NotificationChannel{ID: "c1", Name: "c1", WebhookURL: channelURL, Enabled: true}))
		assert.Equal(t, channelURL, storedChannelColumn(t, db, "c1", "webhook_url"))
	})

	tests := []struct {
		name       string
		newSecret  string
		wantErr    bool
		wantStored string
	}{
		{"unchanged legacy secret is kept", "legacy-plain", false, "legacy-plain"},
		{"changed secret is refused", "other", true, "legacy-plain"},
		{"cleared secret is stored empty", "", false, ""},
	}
	for _, tt := range tests {
		tt := tt
		t.Run("update: "+tt.name, func(t *testing.T) {
			t.Parallel()
			db := channelTestDB(t)
			seedRawChannel(t, db, "c1", channelURL, "legacy-plain")
			repo := NewGORMNotificationChannelRepository(db, "")
			ch, err := repo.GetChannel(ctx, "c1")
			require.NoError(t, err)
			assert.Equal(t, "legacy-plain", ch.Secret)
			ch.Secret = tt.newSecret
			ch.Name = "renamed"
			err = repo.UpdateChannel(ctx, ch, true)
			if tt.wantErr {
				require.Error(t, err)
				assert.True(t, errors.Is(err, dberrors.ErrValidation))
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantStored, storedChannelColumn(t, db, "c1", "secret"))
		})
	}

	t.Run("update without a secret change keeps the legacy secret", func(t *testing.T) {
		t.Parallel()
		db := channelTestDB(t)
		seedRawChannel(t, db, "c1", channelURL, "legacy-plain")
		repo := NewGORMNotificationChannelRepository(db, "")
		ch, err := repo.GetChannel(ctx, "c1")
		require.NoError(t, err)
		ch.Name = "renamed"
		require.NoError(t, repo.UpdateChannel(ctx, ch, false))
		assert.Equal(t, "legacy-plain", storedChannelColumn(t, db, "c1", "secret"))
	})
}

func TestChannelRepository_EncryptsWithKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("secret and URL are stored with the prefix and round trip", func(t *testing.T) {
		t.Parallel()
		db := channelTestDB(t)
		repo := NewGORMNotificationChannelRepository(db, channelKeyA)
		ch := &models.NotificationChannel{ID: "c1", Name: "c1", WebhookURL: channelURL, Secret: "hmac-secret", Enabled: true}
		require.NoError(t, repo.CreateChannel(ctx, ch))
		assert.Equal(t, channelURL, ch.WebhookURL, "the caller struct is not changed")
		assert.Equal(t, "hmac-secret", ch.Secret)

		for _, col := range []string{"secret", "webhook_url"} {
			raw := storedChannelColumn(t, db, "c1", col)
			assert.True(t, strings.HasPrefix(raw, encryptedPrefix), col)
			assert.NotContains(t, raw, "hmac-secret")
			assert.NotContains(t, raw, "token-value")
		}

		got, err := repo.GetChannel(ctx, "c1")
		require.NoError(t, err)
		assert.Equal(t, channelURL, got.WebhookURL)
		assert.Equal(t, "hmac-secret", got.Secret)
		assert.False(t, got.SecretUnreadable || got.WebhookURLUnreadable)

		list, err := repo.ListChannels(ctx)
		require.NoError(t, err)
		require.Len(t, list, 1)
		assert.Equal(t, channelURL, list[0].WebhookURL, "the list returns the decrypted URL")
		enabled, err := repo.ListEnabledChannels(ctx)
		require.NoError(t, err)
		require.Len(t, enabled, 1)
		assert.Equal(t, channelURL, enabled[0].WebhookURL)

		require.NoError(t, repo.SetSubscriptions(ctx, "c1", []string{"deployment.success"}))
		found, err := repo.FindChannelsByEvent(ctx, "deployment.success")
		require.NoError(t, err)
		require.Len(t, found, 1)
		assert.Equal(t, channelURL, found[0].WebhookURL)
		assert.Equal(t, "hmac-secret", found[0].Secret)
	})

	t.Run("update encrypts the URL and a legacy plaintext row at its next save", func(t *testing.T) {
		t.Parallel()
		db := channelTestDB(t)
		seedRawChannel(t, db, "c1", channelURL, "legacy-plain")
		repo := NewGORMNotificationChannelRepository(db, channelKeyA)
		ch, err := repo.GetChannel(ctx, "c1")
		require.NoError(t, err)
		assert.Equal(t, channelURL, ch.WebhookURL, "an unprefixed URL is plaintext")
		assert.Equal(t, "legacy-plain", ch.Secret, "a short legacy value is plaintext")
		assert.False(t, ch.SecretUnreadable)

		ch.Name = "renamed"
		require.NoError(t, repo.UpdateChannel(ctx, ch, false))
		assert.True(t, strings.HasPrefix(storedChannelColumn(t, db, "c1", "webhook_url"), encryptedPrefix))
		assert.Equal(t, "legacy-plain", storedChannelColumn(t, db, "c1", "secret"), "the secret changes only when the caller sets it")

		ch.Secret = "new-secret"
		require.NoError(t, repo.UpdateChannel(ctx, ch, true))
		assert.True(t, strings.HasPrefix(storedChannelColumn(t, db, "c1", "secret"), encryptedPrefix))
		got, err := repo.GetChannel(ctx, "c1")
		require.NoError(t, err)
		assert.Equal(t, "new-secret", got.Secret)
		assert.Equal(t, channelURL, got.WebhookURL)
	})

	t.Run("unprefixed ciphertext of an older version still decrypts", func(t *testing.T) {
		t.Parallel()
		db := channelTestDB(t)
		seedRawChannel(t, db, "c1", channelURL, legacyCiphertext(t, "old-secret", channelKeyA))
		got, err := NewGORMNotificationChannelRepository(db, channelKeyA).GetChannel(ctx, "c1")
		require.NoError(t, err)
		assert.Equal(t, "old-secret", got.Secret)
		assert.False(t, got.SecretUnreadable)
	})

	t.Run("a plaintext secret that is valid base64 is read as plaintext when short", func(t *testing.T) {
		t.Parallel()
		db := channelTestDB(t)
		seedRawChannel(t, db, "c1", channelURL, "c2VjcmV0")
		got, err := NewGORMNotificationChannelRepository(db, channelKeyA).GetChannel(ctx, "c1")
		require.NoError(t, err)
		assert.Equal(t, "c2VjcmV0", got.Secret)
		assert.False(t, got.SecretUnreadable)
	})
}

func TestChannelRepository_UnreadableValues(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// A 40 character plaintext that is valid base64 and long enough to look like ciphertext.
	b64Plain := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 30)))

	tests := []struct {
		name       string
		key        string
		secret     string
		url        string
		wantSecret bool
		wantURL    bool
	}{
		{"wrong key", channelKeyB, "ENC_A_SECRET", "ENC_A_URL", true, true},
		{"no key and prefixed values", "", "ENC_A_SECRET", "ENC_A_URL", true, true},
		{"legacy plaintext that looks like ciphertext", channelKeyA, b64Plain, channelURL, true, false},
		{"prefixed but not base64", channelKeyA, encryptedPrefix + "!!!", channelURL, true, false},
		{"readable values", channelKeyA, "ENC_A_SECRET", "ENC_A_URL", false, false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			db := channelTestDB(t)
			writer := NewGORMNotificationChannelRepository(db, channelKeyA)
			require.NoError(t, writer.CreateChannel(ctx, &models.NotificationChannel{ID: "c1", Name: "c1", WebhookURL: channelURL, Secret: "hmac-secret", Enabled: true}))
			secret, url := tt.secret, tt.url
			if secret == "ENC_A_SECRET" {
				secret = storedChannelColumn(t, db, "c1", "secret")
			}
			if url == "ENC_A_URL" {
				url = storedChannelColumn(t, db, "c1", "webhook_url")
			}
			require.NoError(t, db.Model(&models.NotificationChannel{}).Where("id = ?", "c1").Updates(map[string]any{"secret": secret, "webhook_url": url}).Error)

			repo := NewGORMNotificationChannelRepository(db, tt.key)
			got, err := repo.GetChannel(ctx, "c1")
			require.NoError(t, err, "an unreadable value never fails the read")
			assert.Equal(t, tt.wantSecret, got.SecretUnreadable)
			assert.Equal(t, tt.wantURL, got.WebhookURLUnreadable)
			if tt.wantSecret {
				assert.Empty(t, got.Secret, "ciphertext is never returned as the secret")
			}
			if tt.wantURL {
				assert.Empty(t, got.WebhookURL)
			}

			list, err := repo.ListChannels(ctx)
			require.NoError(t, err)
			require.Len(t, list, 1)
			assert.Equal(t, tt.wantSecret, list[0].SecretUnreadable)
			assert.Equal(t, tt.wantURL, list[0].WebhookURLUnreadable)

			require.NoError(t, repo.SetSubscriptions(ctx, "c1", []string{"deployment.success"}))
			found, err := repo.FindChannelsByEvent(ctx, "deployment.success")
			require.NoError(t, err)
			require.Len(t, found, 1)
			assert.Equal(t, tt.wantSecret, found[0].SecretUnreadable)
			if tt.wantSecret {
				assert.Empty(t, found[0].Secret)
			}
		})
	}
}

func TestChannelRepository_SaveKeepsUnreadableValues(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := channelTestDB(t)
	require.NoError(t, NewGORMNotificationChannelRepository(db, channelKeyA).CreateChannel(ctx,
		&models.NotificationChannel{ID: "c1", Name: "c1", WebhookURL: channelURL, Secret: "hmac-secret", Enabled: true}))
	storedSecret := storedChannelColumn(t, db, "c1", "secret")
	storedURL := storedChannelColumn(t, db, "c1", "webhook_url")

	repo := NewGORMNotificationChannelRepository(db, channelKeyB)
	ch, err := repo.GetChannel(ctx, "c1")
	require.NoError(t, err)
	require.True(t, ch.SecretUnreadable && ch.WebhookURLUnreadable)

	ch.Name = "renamed"
	ch.Enabled = false
	require.NoError(t, repo.UpdateChannel(ctx, ch, false))
	assert.Equal(t, storedSecret, storedChannelColumn(t, db, "c1", "secret"), "the empty in-memory secret is not written")
	assert.Equal(t, storedURL, storedChannelColumn(t, db, "c1", "webhook_url"), "the empty in-memory URL is not written")
	assert.Equal(t, "renamed", storedChannelColumn(t, db, "c1", "name"))

	t.Run("the caller sets both again and repairs the row", func(t *testing.T) {
		ch.Secret, ch.WebhookURL = "new-secret", "https://hooks.example.com/new"
		require.NoError(t, repo.UpdateChannel(ctx, ch, true))
		got, err := repo.GetChannel(ctx, "c1")
		require.NoError(t, err)
		assert.False(t, got.SecretUnreadable || got.WebhookURLUnreadable)
		assert.Equal(t, "new-secret", got.Secret)
		assert.Equal(t, "https://hooks.example.com/new", got.WebhookURL)
	})
}

func TestChannelRepository_ClearingAnUnreadableSecretWorksWithoutKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := channelTestDB(t)
	require.NoError(t, NewGORMNotificationChannelRepository(db, channelKeyA).CreateChannel(ctx,
		&models.NotificationChannel{ID: "c1", Name: "c1", WebhookURL: channelURL, Secret: "hmac-secret", Enabled: true}))
	repo := NewGORMNotificationChannelRepository(db, "")
	ch, err := repo.GetChannel(ctx, "c1")
	require.NoError(t, err)
	require.True(t, ch.SecretUnreadable)

	ch.Secret = ""
	require.NoError(t, repo.UpdateChannel(ctx, ch, true))
	assert.Empty(t, storedChannelColumn(t, db, "c1", "secret"))
}

// Not parallel: it replaces the default slog logger.
func TestChannelRepository_UnreadableWarningOncePerChannel(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(old)

	ctx := context.Background()
	db := channelTestDB(t)
	require.NoError(t, NewGORMNotificationChannelRepository(db, channelKeyA).CreateChannel(ctx,
		&models.NotificationChannel{ID: "c1", Name: "c1", WebhookURL: channelURL, Secret: "hmac-secret", Enabled: true}))
	require.NoError(t, NewGORMNotificationChannelRepository(db, channelKeyA).CreateChannel(ctx,
		&models.NotificationChannel{ID: "c2", Name: "c2", WebhookURL: channelURL, Enabled: true}))
	repo := NewGORMNotificationChannelRepository(db, channelKeyB)

	for i := 0; i < 5; i++ {
		_, err := repo.ListChannels(ctx)
		require.NoError(t, err)
		_, err = repo.GetChannel(ctx, "c1")
		require.NoError(t, err)
	}
	out := buf.String()
	assert.Equal(t, 1, strings.Count(out, "field=secret"), "one warning for the secret of c1")
	assert.Equal(t, 2, strings.Count(out, "field=webhook_url"), "one warning for the URL of each channel")
	assert.Contains(t, out, "channel_id=c1")
	assert.NotContains(t, out, "token-value")
	assert.NotContains(t, out, "hmac-secret")
	assert.NotContains(t, out, encryptedPrefix)

	ch, err := repo.GetChannel(ctx, "c1")
	require.NoError(t, err)
	ch.Secret = "again"
	require.NoError(t, repo.UpdateChannel(ctx, ch, true))
	_, err = repo.GetChannel(ctx, "c1")
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(buf.String(), "field=secret"), "a readable secret logs nothing")
}

func TestChannelRepository_CountSecretsWithoutKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := channelTestDB(t)
	seedRawChannel(t, db, "plain", channelURL, "legacy-plain")
	seedRawChannel(t, db, "empty", channelURL, "")
	require.NoError(t, NewGORMNotificationChannelRepository(db, channelKeyA).CreateChannel(ctx,
		&models.NotificationChannel{ID: "enc", Name: "enc", WebhookURL: channelURL, Secret: "hmac-secret", Enabled: true}))

	cleartext, encrypted, err := NewGORMNotificationChannelRepository(db, "").CountSecretsWithoutKey(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, cleartext)
	assert.Equal(t, 1, encrypted)

	cleartext, encrypted, err = NewGORMNotificationChannelRepository(db, channelKeyA).CountSecretsWithoutKey(ctx)
	require.NoError(t, err)
	assert.Zero(t, cleartext+encrypted, "with a key nothing is counted")
}

func TestNotificationChannelURLColumnMigration(t *testing.T) {
	t.Parallel()
	mig := notificationChannelURLColumnMigration()
	assert.Equal(t, "20261023000065", mig.Version)

	t.Run("up and down are idempotent", func(t *testing.T) {
		t.Parallel()
		db := channelTestDB(t)
		require.NoError(t, mig.Up(db))
		require.NoError(t, mig.Up(db))
		require.NoError(t, mig.Down(db))
		require.NoError(t, mig.Down(db))
	})
	t.Run("no table", func(t *testing.T) {
		t.Parallel()
		db := setupMigrationTestDB(t)
		require.NoError(t, mig.Up(db.DB))
		require.NoError(t, mig.Down(db.DB))
	})
	t.Run("the model column is TEXT", func(t *testing.T) {
		t.Parallel()
		db := channelTestDB(t)
		types, err := db.Migrator().ColumnTypes(&models.NotificationChannel{})
		require.NoError(t, err)
		for _, col := range types {
			if col.Name() == "webhook_url" {
				assert.Equal(t, "TEXT", strings.ToUpper(col.DatabaseTypeName()))
				return
			}
		}
		t.Fatal("webhook_url column not found")
	})
}

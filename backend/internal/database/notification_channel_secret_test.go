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
	channelKey      = "channel-test-key-0123456789abcdef"
	channelOtherKey = "another-test-key-0123456789abcde"
)

func channelDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupTestDBWithAllTables(t)
	require.NoError(t, db.AutoMigrate(&models.NotificationChannel{}, &models.NotificationChannelSubscription{}, &models.NotificationDeliveryLog{}))
	return db
}

func newChannel(name, secret string) *models.NotificationChannel {
	return &models.NotificationChannel{
		ID: "ch-" + name, Name: name, WebhookURL: "https://hooks.example.com/" + name + "?sig=tok", Secret: secret, Enabled: true,
	}
}

func storedChannelColumn(t *testing.T, db *gorm.DB, id, col string) string {
	t.Helper()
	var raw string
	require.NoError(t, db.Model(&models.NotificationChannel{}).Where("id = ?", id).Select("COALESCE("+col+", '')").Scan(&raw).Error)
	return raw
}

func setStored(t *testing.T, db *gorm.DB, id, col, value string) {
	t.Helper()
	require.NoError(t, db.Model(&models.NotificationChannel{}).Where("id = ?", id).Update(col, value).Error)
}

// oldCipher is the format of older versions: base64 AES-GCM without a prefix.
func oldCipher(t *testing.T, plain string) string {
	t.Helper()
	enc, err := crypto.Encrypt([]byte(plain), crypto.DeriveKey(channelKey))
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(enc)
}

// captureSlog replaces the default logger; tests that use it are not parallel.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

func TestChannelRepository_SecretEncryption(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tests := []struct {
		name   string
		stored string // raw column value written after the create
		key    string
		want   string
		wantUn bool
	}{
		{"unprefixed old ciphertext decrypts", "OLD", channelKey, "s3cret", false},
		{"short base64 plaintext is used as is", "abcd", channelKey, "abcd", false},
		{"non base64 plaintext is used as is", "not base64 !!", channelKey, "not base64 !!", false},
		{"unprefixed without a key is plaintext", "legacy-plain", "", "legacy-plain", false},
		{"prefixed without a key is unreadable", "enc:v1:AAAA", "", "", true},
		{"prefixed with bad encoding is unreadable", "enc:v1:%%%", channelKey, "", true},
		{"prefixed with a failed decrypt is unreadable", "enc:v1:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 40)), channelKey, "", true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			db := channelDB(t)
			require.NoError(t, NewGORMNotificationChannelRepository(db, channelKey).CreateChannel(ctx, newChannel("a", "x")))
			stored := tt.stored
			if stored == "OLD" {
				stored = oldCipher(t, "s3cret")
			}
			setStored(t, db, "ch-a", "secret", stored)

			repo := NewGORMNotificationChannelRepository(db, tt.key)
			got, err := repo.GetChannel(ctx, "ch-a")
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.Secret)
			assert.Equal(t, tt.wantUn, got.SecretUnreadable)

			list, err := repo.ListChannels(ctx)
			require.NoError(t, err)
			require.Len(t, list, 1)
			assert.Equal(t, tt.wantUn, list[0].SecretUnreadable)
			enabled, err := repo.ListEnabledChannels(ctx)
			require.NoError(t, err)
			assert.Equal(t, tt.wantUn, enabled[0].SecretUnreadable)
		})
	}

	t.Run("a new secret has the prefix and reads back", func(t *testing.T) {
		t.Parallel()
		db := channelDB(t)
		repo := NewGORMNotificationChannelRepository(db, channelKey)
		ch := newChannel("a", "s3cret")
		require.NoError(t, repo.CreateChannel(ctx, ch))
		assert.Equal(t, "s3cret", ch.Secret, "caller struct is not changed")
		assert.True(t, strings.HasPrefix(storedChannelColumn(t, db, ch.ID, "secret"), "enc:v1:"))
		got, err := repo.GetChannel(ctx, ch.ID)
		require.NoError(t, err)
		assert.Equal(t, "s3cret", got.Secret)
		assert.False(t, got.SecretUnreadable)
	})

}

// Not parallel: it replaces the default slog logger.
func TestChannelRepository_WrongKeyLoggedOnce(t *testing.T) {
	ctx := context.Background()
	{
		db := channelDB(t)
		require.NoError(t, NewGORMNotificationChannelRepository(db, channelKey).CreateChannel(ctx, newChannel("a", "s3cret")))
		buf := captureSlog(t)
		repo := NewGORMNotificationChannelRepository(db, channelOtherKey)
		for i := 0; i < 3; i++ {
			got, err := repo.GetChannel(ctx, "ch-a")
			require.NoError(t, err)
			assert.True(t, got.SecretUnreadable)
			assert.Empty(t, got.Secret)
		}
		assert.Equal(t, 1, strings.Count(buf.String(), "secret cannot be decrypted"))
		assert.Contains(t, buf.String(), "channel_id=ch-a")
		assert.NotContains(t, buf.String(), "s3cret")

		// A save that writes the secret repairs the row and resets the log.
		fixed := &models.NotificationChannel{ID: "ch-a", Name: "a", WebhookURL: "https://hooks.example.com/a", Secret: "new", Enabled: true}
		require.NoError(t, repo.UpdateChannel(ctx, fixed, true))
		got, err := repo.GetChannel(ctx, "ch-a")
		require.NoError(t, err)
		assert.Equal(t, "new", got.Secret)
		assert.False(t, got.SecretUnreadable)
	}
}

func TestChannelRepository_SecretWithoutKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("create with a secret is refused and stores nothing", func(t *testing.T) {
		t.Parallel()
		db := channelDB(t)
		err := NewGORMNotificationChannelRepository(db, "").CreateChannel(ctx, newChannel("a", "s3cret"))
		require.Error(t, err)
		assert.True(t, errors.Is(err, dberrors.ErrValidation))
		assert.True(t, errors.Is(err, models.ErrChannelSecretNoKey))
		var n int64
		require.NoError(t, db.Model(&models.NotificationChannel{}).Count(&n).Error)
		assert.Zero(t, n)
	})

	t.Run("create without a secret stores the URL in plaintext", func(t *testing.T) {
		t.Parallel()
		db := channelDB(t)
		ch := newChannel("a", "")
		require.NoError(t, NewGORMNotificationChannelRepository(db, "").CreateChannel(ctx, ch))
		assert.Equal(t, ch.WebhookURL, storedChannelColumn(t, db, ch.ID, "webhook_url"))
	})

	t.Run("update with a new secret is refused and changes nothing", func(t *testing.T) {
		t.Parallel()
		db := channelDB(t)
		repo := NewGORMNotificationChannelRepository(db, "")
		require.NoError(t, repo.CreateChannel(ctx, newChannel("a", "")))
		require.NoError(t, repo.SetSubscriptions(ctx, "ch-a", []string{"deployment.success"}))

		upd := newChannel("a", "new-secret")
		upd.Name = "renamed"
		err := repo.UpdateChannel(ctx, upd, true)
		require.Error(t, err)
		assert.True(t, errors.Is(err, models.ErrChannelSecretNoKey))
		got, gerr := repo.GetChannel(ctx, "ch-a")
		require.NoError(t, gerr)
		assert.Equal(t, "a", got.Name)
		assert.Empty(t, storedChannelColumn(t, db, "ch-a", "secret"))
		subs, serr := repo.GetSubscriptions(ctx, "ch-a")
		require.NoError(t, serr)
		assert.Len(t, subs, 1)
	})

	t.Run("removing the secret works and clears the column", func(t *testing.T) {
		t.Parallel()
		db := channelDB(t)
		repo := NewGORMNotificationChannelRepository(db, "")
		require.NoError(t, repo.CreateChannel(ctx, newChannel("a", "")))
		setStored(t, db, "ch-a", "secret", "legacy-plain")
		upd := newChannel("a", "")
		require.NoError(t, repo.UpdateChannel(ctx, upd, true))
		assert.Empty(t, storedChannelColumn(t, db, "ch-a", "secret"))
	})

	t.Run("an update without secretChanged keeps a legacy secret", func(t *testing.T) {
		t.Parallel()
		db := channelDB(t)
		repo := NewGORMNotificationChannelRepository(db, "")
		require.NoError(t, repo.CreateChannel(ctx, newChannel("a", "")))
		setStored(t, db, "ch-a", "secret", "legacy-plain")
		existing, err := repo.GetChannel(ctx, "ch-a")
		require.NoError(t, err)
		existing.Name = "renamed"
		require.NoError(t, repo.UpdateChannel(ctx, existing, false))
		assert.Equal(t, "legacy-plain", storedChannelColumn(t, db, "ch-a", "secret"))
	})

	t.Run("the same legacy secret passes, a different one is refused", func(t *testing.T) {
		t.Parallel()
		db := channelDB(t)
		repo := NewGORMNotificationChannelRepository(db, "")
		require.NoError(t, repo.CreateChannel(ctx, newChannel("a", "")))
		setStored(t, db, "ch-a", "secret", "legacy-plain")
		same := newChannel("a", "legacy-plain")
		require.NoError(t, repo.UpdateChannel(ctx, same, true))
		assert.Equal(t, "legacy-plain", storedChannelColumn(t, db, "ch-a", "secret"))
		other := newChannel("a", "other")
		require.Error(t, repo.UpdateChannel(ctx, other, true))
		assert.Equal(t, "legacy-plain", storedChannelColumn(t, db, "ch-a", "secret"))
	})
}

func TestChannelRepository_WebhookURL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	longURL := "https://hooks.example.com/" + strings.Repeat("a", 2048-len("https://hooks.example.com/"))

	t.Run("encrypted with a key and read back", func(t *testing.T) {
		t.Parallel()
		db := channelDB(t)
		repo := NewGORMNotificationChannelRepository(db, channelKey)
		ch := newChannel("a", "")
		ch.WebhookURL = longURL
		require.NoError(t, repo.CreateChannel(ctx, ch))
		raw := storedChannelColumn(t, db, ch.ID, "webhook_url")
		assert.True(t, strings.HasPrefix(raw, "enc:v1:"))
		assert.Greater(t, len(raw), 2048, "ciphertext is longer than the plain URL")
		assert.NotContains(t, raw, "hooks.example.com")
		assert.Equal(t, longURL, ch.WebhookURL, "caller struct is not changed")
		got, err := repo.GetChannel(ctx, ch.ID)
		require.NoError(t, err)
		assert.Equal(t, longURL, got.WebhookURL)
		list, err := repo.ListChannels(ctx)
		require.NoError(t, err)
		assert.Equal(t, longURL, list[0].WebhookURL)
	})

	t.Run("legacy plaintext is read with a key and encrypted by a save", func(t *testing.T) {
		t.Parallel()
		db := channelDB(t)
		require.NoError(t, NewGORMNotificationChannelRepository(db, "").CreateChannel(ctx, newChannel("a", "")))
		repo := NewGORMNotificationChannelRepository(db, channelKey)
		got, err := repo.GetChannel(ctx, "ch-a")
		require.NoError(t, err)
		assert.Equal(t, "https://hooks.example.com/a?sig=tok", got.WebhookURL)
		assert.False(t, got.WebhookURLUnreadable)
		got.Name = "renamed"
		require.NoError(t, repo.UpdateChannel(ctx, got, false))
		assert.True(t, strings.HasPrefix(storedChannelColumn(t, db, "ch-a", "webhook_url"), "enc:v1:"))
		again, err := repo.GetChannel(ctx, "ch-a")
		require.NoError(t, err)
		assert.Equal(t, "https://hooks.example.com/a?sig=tok", again.WebhookURL)
	})

	t.Run("FindChannelsByEvent decrypts and flags", func(t *testing.T) {
		t.Parallel()
		db := channelDB(t)
		repo := NewGORMNotificationChannelRepository(db, channelKey)
		require.NoError(t, repo.CreateChannel(ctx, newChannel("a", "s3cret")))
		require.NoError(t, repo.CreateChannel(ctx, newChannel("b", "s3cret")))
		for _, id := range []string{"ch-a", "ch-b"} {
			require.NoError(t, repo.SetSubscriptions(ctx, id, []string{"deployment.success"}))
		}
		setStored(t, db, "ch-b", "webhook_url", "enc:v1:%%%")
		found, err := repo.FindChannelsByEvent(ctx, "deployment.success")
		require.NoError(t, err)
		require.Len(t, found, 2)
		for _, ch := range found {
			switch ch.ID {
			case "ch-a":
				assert.Equal(t, "s3cret", ch.Secret)
				assert.False(t, ch.WebhookURLUnreadable)
			case "ch-b":
				assert.True(t, ch.WebhookURLUnreadable)
				assert.Empty(t, ch.WebhookURL)
			}
		}
	})
}

// Not parallel: it replaces the default slog logger.
func TestChannelRepository_UnreadableURL(t *testing.T) {
	ctx := context.Background()
	{
		db := channelDB(t)
		require.NoError(t, NewGORMNotificationChannelRepository(db, channelKey).CreateChannel(ctx, newChannel("a", "")))
		stored := storedChannelColumn(t, db, "ch-a", "webhook_url")
		buf := captureSlog(t)

		for _, key := range []string{"", channelOtherKey} {
			repo := NewGORMNotificationChannelRepository(db, key)
			got, err := repo.GetChannel(ctx, "ch-a")
			require.NoError(t, err)
			assert.True(t, got.WebhookURLUnreadable)
			assert.Empty(t, got.WebhookURL)
			assert.Equal(t, models.ChannelWebhookURLUnreadableMessage, got.UnreadableMessage())

			got.Enabled = false
			require.NoError(t, repo.UpdateChannel(ctx, got, false))
			assert.Equal(t, stored, storedChannelColumn(t, db, "ch-a", "webhook_url"), "the stored value stays")
			again, err := repo.GetChannel(ctx, "ch-a")
			require.NoError(t, err)
			assert.False(t, again.Enabled, "the other fields are saved")
		}
		assert.NotContains(t, buf.String(), "hooks.example.com")

		// Repair without a key: plaintext. Repair with a key: encrypted.
		repo := NewGORMNotificationChannelRepository(db, channelKey)
		got, err := repo.GetChannel(ctx, "ch-a")
		require.NoError(t, err)
		assert.False(t, got.WebhookURLUnreadable, "the right key reads it")
		other := NewGORMNotificationChannelRepository(db, channelOtherKey)
		broken, err := other.GetChannel(ctx, "ch-a")
		require.NoError(t, err)
		broken.WebhookURL = "https://new.example.com/x"
		require.NoError(t, other.UpdateChannel(ctx, broken, false))
		assert.False(t, broken.WebhookURLUnreadable)
		fixed, err := other.GetChannel(ctx, "ch-a")
		require.NoError(t, err)
		assert.Equal(t, "https://new.example.com/x", fixed.WebhookURL)
		assert.False(t, fixed.WebhookURLUnreadable)

		keyless := channelDB(t)
		kl := NewGORMNotificationChannelRepository(keyless, "")
		require.NoError(t, NewGORMNotificationChannelRepository(keyless, channelKey).CreateChannel(ctx, newChannel("a", "")))
		b2, err := kl.GetChannel(ctx, "ch-a")
		require.NoError(t, err)
		b2.WebhookURL = "https://new.example.com/y"
		require.NoError(t, kl.UpdateChannel(ctx, b2, false))
		assert.Equal(t, "https://new.example.com/y", storedChannelColumn(t, keyless, "ch-a", "webhook_url"))
	}
}

func TestChannelRepository_ChannelSecretStats(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	setup := func(t *testing.T) *gorm.DB {
		db := channelDB(t)
		keyed := NewGORMNotificationChannelRepository(db, channelKey)
		require.NoError(t, keyed.CreateChannel(ctx, newChannel("ok", "s3cret"))) // encrypted secret and URL
		require.NoError(t, keyed.CreateChannel(ctx, newChannel("plain", "")))    // URL encrypted, no secret
		require.NoError(t, keyed.CreateChannel(ctx, newChannel("legacy", "")))   // plaintext secret and URL
		setStored(t, db, "ch-legacy", "secret", "legacy-plain")
		setStored(t, db, "ch-legacy", "webhook_url", "https://legacy.example.com/h")
		require.NoError(t, keyed.CreateChannel(ctx, newChannel("bad", "s3cret")))
		setStored(t, db, "ch-bad", "secret", "enc:v1:%%%")
		setStored(t, db, "ch-bad", "webhook_url", "enc:v1:%%%")
		return db
	}

	tests := []struct {
		name                                         string
		key                                          string
		plainSecrets, badSecrets, plainURLs, badURLs int
	}{
		{"with the key", channelKey, 1, 1, 1, 1},
		{"without a key", "", 1, 2, 1, 3},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			db := setup(t)
			ps, bs, pu, bu, err := NewGORMNotificationChannelRepository(db, tt.key).ChannelSecretStats(ctx)
			require.NoError(t, err)
			assert.Equal(t, tt.plainSecrets, ps, "plaintext secrets")
			assert.Equal(t, tt.badSecrets, bs, "unreadable secrets")
			assert.Equal(t, tt.plainURLs, pu, "plaintext URLs")
			assert.Equal(t, tt.badURLs, bu, "unreadable URLs")
		})
	}
}

func TestNotificationChannelURLMigration(t *testing.T) {
	t.Parallel()
	mig := notificationChannelURLMigration()
	assert.Equal(t, "20261024000065", mig.Version)

	db := channelDB(t)
	// SQLite: both steps are no-ops and idempotent.
	require.NoError(t, mig.Up(db))
	require.NoError(t, mig.Up(db))
	require.NoError(t, mig.Down(db))

}

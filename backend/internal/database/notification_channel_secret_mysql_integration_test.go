//go:build integration

package database

import (
	"context"
	"strings"
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// setupChannelMySQL creates the notification channel tables and empties them,
// so a rerun on a used database works.
func setupChannelMySQL(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupMySQLTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.NotificationChannel{}, &models.NotificationChannelSubscription{}, &models.NotificationDeliveryLog{}))
	for _, table := range []string{"notification_delivery_logs", "notification_channel_subscriptions", "notification_channels"} {
		require.NoError(t, db.Exec("DELETE FROM "+table).Error) // #nosec G202 -- constant table names
	}
	return db
}

// TestDatabaseMySQL_ChannelWebhookURLColumn checks on MySQL that migration 65
// widens webhook_url from VARCHAR(2048) to TEXT, so a 2048 character URL fits
// as ciphertext, and that Up and Down are idempotent.
func TestDatabaseMySQL_ChannelWebhookURLColumn(t *testing.T) {
	db := setupChannelMySQL(t)
	ctx := context.Background()
	mig := notificationChannelURLMigration()

	require.NoError(t, db.Exec("ALTER TABLE notification_channels MODIFY webhook_url VARCHAR(2048) NOT NULL").Error)
	require.NoError(t, mig.Up(db))
	require.NoError(t, mig.Up(db), "Up is idempotent")
	columnType, found, err := channelURLColumnType(db)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "text", columnType)

	repo := NewGORMNotificationChannelRepository(db, "test-encryption-key-for-integration")
	longURL := "https://hooks.example.com/" + strings.Repeat("a", 2048-len("https://hooks.example.com/"))
	ch := &models.NotificationChannel{ID: "ch-long", Name: "long", WebhookURL: longURL, Secret: "s3cret", Enabled: true}
	require.NoError(t, repo.CreateChannel(ctx, ch))
	raw := storedChannelColumn(t, db, ch.ID, "webhook_url")
	assert.Greater(t, len(raw), 2048)
	assert.True(t, strings.HasPrefix(raw, "enc:v1:"))
	got, err := repo.GetChannel(ctx, ch.ID)
	require.NoError(t, err)
	assert.Equal(t, longURL, got.WebhookURL)
	assert.Equal(t, "s3cret", got.Secret)

	require.NoError(t, repo.DeleteChannel(ctx, ch.ID))
	require.NoError(t, mig.Down(db))
	require.NoError(t, mig.Down(db), "Down is idempotent")
	columnType, _, err = channelURLColumnType(db)
	require.NoError(t, err)
	assert.Equal(t, "varchar(2048)", columnType)
	require.NoError(t, mig.Up(db))
}

// TestDatabaseMySQL_ChannelURLMigrationDown checks that Down refuses to shrink
// the column while a stored URL is longer than 2048 characters.
func TestDatabaseMySQL_ChannelURLMigrationDown(t *testing.T) {
	db := setupChannelMySQL(t)
	mig := notificationChannelURLMigration()
	require.NoError(t, mig.Up(db))

	long := "https://hooks.example.com/" + strings.Repeat("a", 2100)
	require.NoError(t, db.Exec("INSERT INTO notification_channels (id, name, webhook_url, enabled) VALUES (?, ?, ?, ?)", "ch-long", "long", long, true).Error)
	err := mig.Down(db)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "longer than 2048")
	columnType, _, cerr := channelURLColumnType(db)
	require.NoError(t, cerr)
	assert.Equal(t, "text", columnType, "the column is unchanged")

	require.NoError(t, db.Exec("DELETE FROM notification_channels WHERE id = ?", "ch-long").Error)
	require.NoError(t, mig.Down(db))
	require.NoError(t, mig.Up(db))
}

// TestDatabaseMySQL_ChannelSecretWithoutKey checks on MySQL that a keyless
// repository refuses a new secret, keeps a legacy plaintext or NULL secret on
// other edits, clears it on request, and counts the stored values.
func TestDatabaseMySQL_ChannelSecretWithoutKey(t *testing.T) {
	db := setupChannelMySQL(t)
	ctx := context.Background()
	repo := NewGORMNotificationChannelRepository(db, "")

	require.NoError(t, repo.CreateChannel(ctx, &models.NotificationChannel{ID: "ch-null", Name: "null", WebhookURL: "https://a.example.com/x", Enabled: true}))
	require.NoError(t, db.Exec("UPDATE notification_channels SET secret = NULL WHERE id = ?", "ch-null").Error)
	c, err := repo.GetChannel(ctx, "ch-null")
	require.NoError(t, err)
	c.Name = "null-renamed"
	require.NoError(t, repo.UpdateChannel(ctx, c, false), "an update of a channel with a NULL secret works")

	require.NoError(t, repo.CreateChannel(ctx, &models.NotificationChannel{ID: "ch-legacy", Name: "legacy", WebhookURL: "https://b.example.com/x", Enabled: true}))
	require.NoError(t, db.Exec("UPDATE notification_channels SET secret = 'plain' WHERE id = ?", "ch-legacy").Error)
	legacy, err := repo.GetChannel(ctx, "ch-legacy")
	require.NoError(t, err)
	assert.Equal(t, "plain", legacy.Secret)
	legacy.Name = "legacy-renamed"
	require.NoError(t, repo.UpdateChannel(ctx, legacy, false))
	require.NoError(t, repo.UpdateChannel(ctx, legacy, true), "the unchanged legacy secret passes")
	assert.Equal(t, "plain", storedChannelColumn(t, db, "ch-legacy", "secret"))

	legacy.Secret = "changed"
	require.Error(t, repo.UpdateChannel(ctx, legacy, true))
	assert.Equal(t, "plain", storedChannelColumn(t, db, "ch-legacy", "secret"))

	stats, err := repo.ChannelSecretStats(ctx)
	require.NoError(t, err)
	assert.Equal(t, models.ChannelSecretStats{PlaintextSecrets: 1, PlaintextURLs: 2}, stats)

	legacy.Secret = ""
	require.NoError(t, repo.UpdateChannel(ctx, legacy, true))
	assert.Empty(t, storedChannelColumn(t, db, "ch-legacy", "secret"))

	require.Error(t, repo.CreateChannel(ctx, &models.NotificationChannel{Name: "new", WebhookURL: "https://c.example.com/x", Secret: "pw", Enabled: true}))
}

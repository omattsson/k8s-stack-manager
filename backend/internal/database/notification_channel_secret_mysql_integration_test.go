//go:build integration

package database

import (
	"context"
	"strings"
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDatabaseMySQL_NotificationChannelEncryption checks on MySQL that the
// webhook_url column is TEXT (migration 65), that a 2048 byte URL is stored
// encrypted and read back, and that a keyless repository refuses a secret.
func TestDatabaseMySQL_NotificationChannelEncryption(t *testing.T) {
	db, _ := setupClusterContextMySQL(t, 5)
	ctx := context.Background()

	// The table of migration 40, then migration 65.
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS notification_channels").Error)
	require.NoError(t, db.Exec(`CREATE TABLE notification_channels (
		id VARCHAR(36) NOT NULL PRIMARY KEY, name VARCHAR(255) NOT NULL UNIQUE,
		webhook_url VARCHAR(2048) NOT NULL, secret TEXT, enabled TINYINT(1) NOT NULL DEFAULT 1,
		format VARCHAR(20) NOT NULL DEFAULT 'generic', filters TEXT, created_at DATETIME NULL, updated_at DATETIME NULL)`).Error)
	var colType string
	require.NoError(t, db.Raw("SELECT DATA_TYPE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'notification_channels' AND COLUMN_NAME = 'webhook_url'").Scan(&colType).Error)
	require.Equal(t, "varchar", strings.ToLower(colType), "the old column")
	longEnc := "x" + strings.Repeat("a", 3000)
	require.Error(t, db.Exec("INSERT INTO notification_channels (id, name, webhook_url) VALUES ('big', 'big', ?)", longEnc).Error, "the old column is too small")
	mig := notificationChannelURLColumnMigration()
	require.NoError(t, mig.Up(db))
	require.NoError(t, mig.Up(db), "Up is idempotent")

	require.NoError(t, db.Raw("SELECT DATA_TYPE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'notification_channels' AND COLUMN_NAME = 'webhook_url'").Scan(&colType).Error)
	assert.Equal(t, "text", strings.ToLower(colType))

	longURL := "https://hooks.example.com/" + strings.Repeat("a", 2048-len("https://hooks.example.com/"))
	require.Len(t, longURL, 2048)

	repo := NewGORMNotificationChannelRepository(db, "test-encryption-key-for-integration")
	require.NoError(t, repo.CreateChannel(ctx, &models.NotificationChannel{ID: "enc1", Name: "enc1", WebhookURL: longURL, Secret: "hmac", Enabled: true}))
	got, err := repo.GetChannel(ctx, "enc1")
	require.NoError(t, err)
	assert.Equal(t, longURL, got.WebhookURL)
	assert.Equal(t, "hmac", got.Secret)
	assert.Greater(t, len(storedChannelColumn(t, db, "enc1", "webhook_url")), 2048)

	keyless := NewGORMNotificationChannelRepository(db, "")
	require.Error(t, keyless.CreateChannel(ctx, &models.NotificationChannel{ID: "enc2", Name: "enc2", WebhookURL: longURL, Secret: "hmac", Enabled: true}))
	cleartext, encrypted, err := keyless.CountSecretsWithoutKey(ctx)
	require.NoError(t, err)
	assert.Zero(t, cleartext)
	assert.Equal(t, 1, encrypted)

	unreadable, err := keyless.GetChannel(ctx, "enc1")
	require.NoError(t, err)
	assert.True(t, unreadable.SecretUnreadable)
	assert.True(t, unreadable.WebhookURLUnreadable)
}

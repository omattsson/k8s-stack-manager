package database

import (
	"backend/internal/database/schema"
	"backend/internal/models"

	"gorm.io/gorm"
)

// notificationChannelURLColumnMigration is migration 65. It widens
// notification_channels.webhook_url from VARCHAR(2048) to TEXT: the encrypted
// URL (prefix, nonce, tag, base64) is longer than a 2048 byte URL (#692). The
// column has no index. Up and Down are idempotent. Down needs that no stored
// value is longer than 2048 characters.
func notificationChannelURLColumnMigration() schema.Migration {
	return schema.Migration{
		Version:     "20261023000065",
		Name:        "widen_notification_channels_webhook_url",
		Description: "Widen notification_channels.webhook_url to TEXT (encrypted URLs are longer)",
		Up: func(tx *gorm.DB) error {
			return modifyChannelURLColumn(tx, "TEXT")
		},
		Down: func(tx *gorm.DB) error {
			return modifyChannelURLColumn(tx, "VARCHAR(2048)")
		},
	}
}

// modifyChannelURLColumn changes the column type on MySQL. SQLite does not
// enforce a VARCHAR length, so it needs no change.
func modifyChannelURLColumn(tx *gorm.DB, columnType string) error {
	if tx.Dialector.Name() != "mysql" || !tx.Migrator().HasTable(&models.NotificationChannel{}) {
		return nil
	}
	return tx.Exec("ALTER TABLE notification_channels MODIFY webhook_url " + columnType + " NOT NULL").Error // #nosec G202 -- constant type
}

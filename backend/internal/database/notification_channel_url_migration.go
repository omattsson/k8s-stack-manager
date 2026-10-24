package database

import (
	"database/sql"
	"errors"
	"fmt"

	"backend/internal/database/schema"

	"gorm.io/gorm"
)

// notificationChannelURLMigration is migration 65 (#692). With
// KUBECONFIG_ENCRYPTION_KEY the backend stores notification_channels.webhook_url
// as "enc:v1:" + base64 ciphertext: a URL of 2048 characters gives about 2775.
// The migration widens the column from VARCHAR(2048) to TEXT NOT NULL. Only
// MySQL needs it (a new database gets TEXT from AutoMigrate in migration 4;
// SQLite does not limit the length). Up and Down are idempotent. Down goes
// back to VARCHAR(2048); MySQL refuses it in strict mode when a stored value
// is longer, so Down never cuts a URL.
func notificationChannelURLMigration() schema.Migration {
	return schema.Migration{
		Version:     "20261024000065",
		Name:        "widen_notification_channel_webhook_url",
		Description: "Widen notification_channels.webhook_url to TEXT for the encrypted URL",
		Up: func(tx *gorm.DB) error {
			if tx.Dialector.Name() != "mysql" {
				return nil
			}
			columnType, found, err := channelURLColumnType(tx)
			if err != nil || !found || columnType == "text" {
				return err
			}
			return tx.Exec("ALTER TABLE notification_channels MODIFY webhook_url TEXT NOT NULL").Error // #nosec G202 -- constant statement
		},
		Down: func(tx *gorm.DB) error {
			if tx.Dialector.Name() != "mysql" {
				return nil
			}
			columnType, found, err := channelURLColumnType(tx)
			if err != nil || !found || columnType == "varchar(2048)" {
				return err
			}
			return tx.Exec("ALTER TABLE notification_channels MODIFY webhook_url VARCHAR(2048) NOT NULL").Error // #nosec G202 -- constant statement
		},
	}
}

func channelURLColumnType(tx *gorm.DB) (columnType string, found bool, err error) {
	row := tx.Raw("SELECT COLUMN_TYPE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'notification_channels' AND COLUMN_NAME = 'webhook_url'").Row()
	if scanErr := row.Scan(&columnType); scanErr != nil {
		if errors.Is(scanErr, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("failed to check column type: %w", scanErr)
	}
	return columnType, true, nil
}

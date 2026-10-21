package database

import (
	"backend/internal/database/schema"
	"backend/internal/models"

	"gorm.io/gorm"
)

// notificationListIndexName is the index of the notification list query.
const notificationListIndexName = "idx_notifications_user_created"

// notificationListIndexMigration is migration 63 (#654). It adds an index on
// notifications (user_id, created_at DESC): the list query (WHERE user_id = ?
// ORDER BY created_at DESC LIMIT n) then reads only the rows of the page, not
// all rows of the user with a filesort. Up and Down are idempotent.
func notificationListIndexMigration() schema.Migration {
	return schema.Migration{
		Version:     "20261021000063",
		Name:        "add_notifications_user_created_index",
		Description: "Add an index on notifications (user_id, created_at DESC) for the notification list",
		Up: func(tx *gorm.DB) error {
			m := tx.Migrator()
			if !m.HasTable(&models.Notification{}) || m.HasIndex(&models.Notification{}, notificationListIndexName) {
				return nil
			}
			return tx.Exec("CREATE INDEX " + notificationListIndexName + " ON notifications (user_id, created_at DESC)").Error // #nosec G202 -- index name is a constant
		},
		Down: func(tx *gorm.DB) error {
			m := tx.Migrator()
			if m.HasIndex(&models.Notification{}, notificationListIndexName) {
				return m.DropIndex(&models.Notification{}, notificationListIndexName)
			}
			return nil
		},
	}
}

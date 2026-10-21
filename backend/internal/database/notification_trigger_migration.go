package database

import (
	"backend/internal/database/schema"
	"backend/internal/models"

	"gorm.io/gorm"
)

// notificationTriggerColumns are the columns of models.Trigger.
var notificationTriggerColumns = []string{"trigger_type", "trigger_id", "trigger_name"}

// notificationTriggerMigration is migration 64: the nullable trigger columns
// of notifications (#652). Old rows keep NULL. Up and Down are idempotent.
func notificationTriggerMigration() schema.Migration {
	return schema.Migration{
		Version:     "20261021000064",
		Name:        "add_notification_trigger",
		Description: "Add notifications.trigger_type, trigger_id and trigger_name (who or what started the action)",
		Up: func(tx *gorm.DB) error {
			m := tx.Migrator()
			if !m.HasTable(&models.Notification{}) {
				return nil
			}
			// Migration 4 runs AutoMigrate on the current model, so a new
			// database has the columns already.
			for _, col := range notificationTriggerColumns {
				if m.HasColumn(&models.Notification{}, col) {
					continue
				}
				if err := m.AddColumn(&models.Notification{}, col); err != nil {
					return err
				}
			}
			return nil
		},
		Down: func(tx *gorm.DB) error {
			m := tx.Migrator()
			if !m.HasTable(&models.Notification{}) {
				return nil
			}
			for _, col := range notificationTriggerColumns {
				if !m.HasColumn(&models.Notification{}, col) {
					continue
				}
				if err := m.DropColumn(&models.Notification{}, col); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

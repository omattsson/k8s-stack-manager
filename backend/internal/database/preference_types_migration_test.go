package database

import (
	"testing"

	"backend/internal/models"
	"backend/internal/notifier"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMigration_UnknownPreferenceTypes checks migration 56: it deletes the
// preferences of unknown event types, keeps the known ones, and can run
// again (issue #498).
func TestMigration_UnknownPreferenceTypes(t *testing.T) {
	t.Parallel()
	db := setupMigrationTestDB(t)
	require.NoError(t, db.DB.AutoMigrate(&models.NotificationPreference{}))

	rows := []models.NotificationPreference{
		{ID: "p1", UserID: "u1", EventType: "deployment.success", Enabled: false, Channel: "in_app"},
		{ID: "p2", UserID: "u1", EventType: "bogus.event", Enabled: true, Channel: "in_app"},
		{ID: "p3", UserID: "u2", EventType: "stack.expired", Enabled: true, Channel: "in_app"},
		{ID: "p4", UserID: "u2", EventType: "quota.warning", Enabled: true, Channel: "in_app"},
	}
	require.NoError(t, db.DB.Create(&rows).Error)

	mig := unknownPreferenceTypesMigration()
	require.NoError(t, mig.Up(db.DB))
	require.NoError(t, mig.Up(db.DB), "Up again")
	require.NoError(t, mig.Down(db.DB))

	var left []models.NotificationPreference
	require.NoError(t, db.DB.Order("id").Find(&left).Error)
	ids := make([]string, 0, len(left))
	for _, p := range left {
		ids = append(ids, p.ID)
	}
	assert.Equal(t, []string{"p1", "p4"}, ids)
}

// TestMigration_UnknownPreferenceTypes_NoTable checks that Up does nothing
// without the table.
func TestMigration_UnknownPreferenceTypes_NoTable(t *testing.T) {
	t.Parallel()
	db := setupMigrationTestDB(t)
	require.NoError(t, unknownPreferenceTypesMigration().Up(db.DB))
}

// TestMigration56PreferenceEventTypes checks that the frozen list of
// migration 56 has every current preference type, so the migration deletes
// no valid preference. A type added after migration 56 cannot have rows
// before it ran: then add it to the expected exceptions here, not to the
// migration list.
func TestMigration56PreferenceEventTypes(t *testing.T) {
	t.Parallel()
	addedAfterMigration56 := map[string]bool{
		// The TTL reaper sends stack.expired since #500. Migration 56 ran
		// when the API still refused it, so no stored row is lost.
		"stack.expired": true,
	}
	frozen := map[string]bool{}
	for _, et := range migration56PreferenceEventTypes {
		frozen[et] = true
	}
	for _, et := range notifier.PreferenceEventTypes() {
		if addedAfterMigration56[et] {
			continue
		}
		assert.Truef(t, frozen[et], "preference type %q is missing from migration56PreferenceEventTypes", et)
	}
}

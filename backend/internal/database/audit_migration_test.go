package database

import (
	"strings"
	"testing"

	"backend/internal/api/middleware"
	"backend/internal/database/schema"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMigration_AuditEntityTypes checks migration 47: plural CRUD entity
// types get the singular names, old operation entries keep their values, and
// entity_id takes a namespace name longer than a UUID.
func TestMigration_AuditEntityTypes(t *testing.T) {
	t.Parallel()
	db := setupMigrationTestDB(t)
	require.NoError(t, db.AutoMigrate())
	require.NoError(t, db.DB.Unscoped().Where("version = ?", "20261009000047").Delete(&schema.SchemaVersion{}).Error)

	rows := []models.AuditLog{
		{ID: "a1", Action: "create", EntityType: "clusters", EntityID: "c1"},
		{ID: "a2", Action: "update", EntityType: "quotas", EntityID: "c1"},
		{ID: "a3", Action: "create", EntityType: "api_keys", EntityID: "u1"},
		{ID: "a4", Action: "update", EntityType: "subscriptions", EntityID: "ch1"},
		{ID: "a5", Action: "delete", EntityType: "orphaned_namespaces"},
		{ID: "a6", Action: "create", EntityType: "deploy", EntityID: "i1"},         // old operation entry: unchanged
		{ID: "a7", Action: "update", EntityType: "stack_instance", EntityID: "i1"}, // already singular
	}
	for i := range rows {
		require.NoError(t, db.DB.Create(&rows[i]).Error)
	}

	require.NoError(t, db.AutoMigrate())

	want := map[string]string{
		"a1": "cluster", "a2": "quota", "a3": "api_key", "a4": "notification_subscription",
		"a5": "namespace", "a6": "deploy", "a7": "stack_instance",
	}
	var got []models.AuditLog
	require.NoError(t, db.DB.Find(&got).Error)
	require.Len(t, got, len(want))
	for _, r := range got {
		assert.Equal(t, want[r.ID], r.EntityType, r.ID)
	}

	long := models.AuditLog{ID: "a8", Action: "delete", EntityType: "namespace", EntityID: "stack-" + strings.Repeat("x", 57)}
	require.NoError(t, db.DB.Create(&long).Error)

	require.NoError(t, auditEntityTypesMigration().Down(db.DB))
}

// TestAuditEntityTypeRenames_TargetsAreKnown checks that the migration only
// renames to entity types the audit middleware writes.
func TestAuditEntityTypeRenames_TargetsAreKnown(t *testing.T) {
	t.Parallel()
	for from, to := range auditEntityTypeRenames {
		assert.Contains(t, middleware.KnownAuditEntityTypes, to, from)
	}
}

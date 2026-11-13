//go:build integration

package database

import (
	"context"
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDatabaseAuditLogListLegacyRows checks that a row written before
// migration 68 (NULL in the new columns) lists without an error.
func TestDatabaseAuditLogListLegacyRows(t *testing.T) {
	db := setupMySQLTestDB(t)
	const id = "legacy-audit-row-68"
	t.Cleanup(func() { db.Exec("DELETE FROM audit_logs WHERE id = ?", id) })
	require.NoError(t, db.Exec(
		"INSERT INTO audit_logs (id, user_id, username, action, entity_type, entity_id, details, timestamp) VALUES (?, 'u1', 'alice', 'create', 'stack_instance', ?, '', NOW())",
		id, id).Error)
	var nulls int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM audit_logs WHERE id = ? AND entity_name IS NULL AND client_ip IS NULL AND api_key_id IS NULL", id).Scan(&nulls).Error)
	require.Equal(t, int64(1), nulls, "the new columns must be NULL on a legacy row")

	res, err := NewGORMAuditLogRepository(db).List(context.Background(), models.AuditLogFilters{EntityID: id, Limit: 10})
	require.NoError(t, err)
	require.Len(t, res.Data, 1)
	assert.Equal(t, "", res.Data[0].EntityName)
	assert.Equal(t, "", res.Data[0].ClientIP)
	assert.Equal(t, int64(1), res.Total)
}

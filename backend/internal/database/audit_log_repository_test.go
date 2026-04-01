package database

import (
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupAuditLogRepo(t *testing.T) *GORMAuditLogRepository {
	t.Helper()
	db := setupTestDBWithAllTables(t)
	return NewGORMAuditLogRepository(db)
}

func TestGORMAuditLogRepository_Create(t *testing.T) {
	t.Parallel()

	repo := setupAuditLogRepo(t)
	log := &models.AuditLog{
		UserID:     "u1",
		Username:   "alice",
		Action:     "create",
		EntityType: "stack_instance",
		EntityID:   "si-1",
		Details:    "created instance",
	}
	err := repo.Create(log)
	require.NoError(t, err)
	assert.NotEmpty(t, log.ID)
	assert.False(t, log.Timestamp.IsZero())
}

func TestGORMAuditLogRepository_List(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		filters   models.AuditLogFilters
		seedLogs  []models.AuditLog
		wantCount int
		wantTotal int64
	}{
		{
			name:    "no filters returns all",
			filters: models.AuditLogFilters{Limit: 10},
			seedLogs: []models.AuditLog{
				{UserID: "u1", Username: "alice", Action: "create", EntityType: "instance", EntityID: "e1"},
				{UserID: "u2", Username: "bob", Action: "delete", EntityType: "instance", EntityID: "e2"},
			},
			wantCount: 2,
			wantTotal: 2,
		},
		{
			name:    "filter by action",
			filters: models.AuditLogFilters{Action: "create", Limit: 10},
			seedLogs: []models.AuditLog{
				{UserID: "u1", Username: "alice", Action: "create", EntityType: "instance", EntityID: "e1"},
				{UserID: "u2", Username: "bob", Action: "delete", EntityType: "instance", EntityID: "e2"},
			},
			wantCount: 1,
			wantTotal: 1,
		},
		{
			name:    "filter by entity type",
			filters: models.AuditLogFilters{EntityType: "template", Limit: 10},
			seedLogs: []models.AuditLog{
				{UserID: "u1", Username: "alice", Action: "create", EntityType: "instance", EntityID: "e1"},
				{UserID: "u1", Username: "alice", Action: "update", EntityType: "template", EntityID: "t1"},
			},
			wantCount: 1,
			wantTotal: 1,
		},
		{
			name:    "filter by user ID",
			filters: models.AuditLogFilters{UserID: "u2", Limit: 10},
			seedLogs: []models.AuditLog{
				{UserID: "u1", Username: "alice", Action: "create", EntityType: "instance", EntityID: "e1"},
				{UserID: "u2", Username: "bob", Action: "delete", EntityType: "instance", EntityID: "e2"},
			},
			wantCount: 1,
			wantTotal: 1,
		},
		{
			name: "pagination with offset",
			filters: models.AuditLogFilters{
				Limit:  1,
				Offset: 1,
			},
			seedLogs: []models.AuditLog{
				{UserID: "u1", Username: "alice", Action: "create", EntityType: "instance", EntityID: "e1"},
				{UserID: "u2", Username: "bob", Action: "delete", EntityType: "instance", EntityID: "e2"},
			},
			wantCount: 1,
			wantTotal: 2,
		},
		{
			name:    "filter by entity ID",
			filters: models.AuditLogFilters{EntityID: "e1", Limit: 10},
			seedLogs: []models.AuditLog{
				{UserID: "u1", Username: "alice", Action: "create", EntityType: "instance", EntityID: "e1"},
				{UserID: "u2", Username: "bob", Action: "delete", EntityType: "instance", EntityID: "e2"},
			},
			wantCount: 1,
			wantTotal: 1,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupAuditLogRepo(t)
			for i := range tt.seedLogs {
				require.NoError(t, repo.Create(&tt.seedLogs[i]))
			}

			result, err := repo.List(tt.filters)
			require.NoError(t, err)
			assert.Len(t, result.Data, tt.wantCount)
			assert.Equal(t, tt.wantTotal, result.Total)
		})
	}
}

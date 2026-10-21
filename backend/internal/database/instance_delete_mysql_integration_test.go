//go:build integration

package database

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestDatabaseMySQL_CleanupOwnedDefinitionLock captures the SQL of the
// owned-definition cleanup on MySQL (#654): no SELECT ... FOR UPDATE for a
// definition without an owner instance, one for a quick deploy definition.
func TestDatabaseMySQL_CleanupOwnedDefinitionLock(t *testing.T) {
	tests := []struct {
		name        string
		owner       string
		wantLocks   int
		wantDefKept bool
	}{
		{name: "definition without an owner", owner: "", wantLocks: 0, wantDefKept: true},
		{name: "quick deploy definition", owner: "i1", wantLocks: 1, wantDefKept: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			db := setupMySQLTestDB(t)
			defRepo := NewGORMStackDefinitionRepository(db)
			require.NoError(t, defRepo.Create(&models.StackDefinition{ID: "d1", Name: "qd", OwnerID: "u1", OwnerInstanceID: tt.owner}))
			inst := &models.StackInstance{ID: "i1", StackDefinitionID: "d1", Name: "qd", Namespace: "stack-qd-u1", OwnerID: "u1", Status: models.StackStatusDraft}
			require.NoError(t, NewGORMStackInstanceRepository(db).Create(context.Background(), inst))

			log, h := newRecordingLogger(slog.LevelDebug)
			runner := NewGORMTxRunner(db.Session(&gorm.Session{Logger: newSlogGormLogger(log, true)}))
			require.NoError(t, DeleteInstanceWithOwnedDefinition(context.Background(), runner, inst))

			locks := 0
			for _, r := range h.all() {
				s := recordAttrs(r)["sql"].String()
				if strings.Contains(s, "stack_definitions") && strings.Contains(s, "FOR UPDATE") {
					locks++
				}
			}
			assert.Equal(t, tt.wantLocks, locks)
			_, err := defRepo.FindByID("d1")
			assert.Equal(t, tt.wantDefKept, err == nil)
		})
	}
}

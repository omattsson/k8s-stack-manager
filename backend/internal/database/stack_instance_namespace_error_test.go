package database

import (
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMarkNamespaceError(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	future := now.Add(10 * time.Minute)
	past := now.Add(-time.Minute)
	tests := []struct {
		name        string
		status      string
		hookUntil   *time.Time
		wantChanged bool
	}{
		{name: "running gets error", status: models.StackStatusRunning, wantChanged: true},
		{name: "partial gets error", status: models.StackStatusPartial, wantChanged: true},
		{name: "stabilizing without blocking hooks gets error", status: models.StackStatusStabilizing, wantChanged: true},
		{name: "stabilizing with an expired marker gets error", status: models.StackStatusStabilizing, hookUntil: &past, wantChanged: true},
		{name: "stabilizing during blocking hooks keeps its status", status: models.StackStatusStabilizing, hookUntil: &future},
		{name: "deploying keeps its status", status: models.StackStatusDeploying},
		{name: "stopping keeps its status", status: models.StackStatusStopping},
		{name: "cleaning keeps its status", status: models.StackStatusCleaning},
		{name: "draft keeps its status", status: models.StackStatusDraft},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupStackInstanceRepo(t)
			inst := &models.StackInstance{
				Name: "ns-err", StackDefinitionID: "d1", Namespace: "stack-ns-err", OwnerID: "o1",
				Branch: "main", Status: tt.status, PostDeployHookUntil: tt.hookUntil,
			}
			require.NoError(t, repo.Create(inst))

			changed, err := repo.MarkNamespaceError(inst.ID, "namespace resources in error state", now)
			require.NoError(t, err)
			assert.Equal(t, tt.wantChanged, changed)

			stored, err := repo.FindByID(inst.ID)
			require.NoError(t, err)
			if tt.wantChanged {
				assert.Equal(t, models.StackStatusError, stored.Status)
				assert.Equal(t, "namespace resources in error state", stored.ErrorMessage)
			} else {
				assert.Equal(t, tt.status, stored.Status)
				assert.Empty(t, stored.ErrorMessage)
			}
		})
	}
}

package database

import (
	"context"
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// markStatusCases are the cases of MarkStatus to stabilizing from deploying
// or stabilizing (the use of the deploy manager).
var markStatusCases = []struct {
	name       string
	status     string
	wantMarked bool
	wantStatus string
}{
	{name: "deploying becomes stabilizing", status: models.StackStatusDeploying, wantMarked: true, wantStatus: models.StackStatusStabilizing},
	{name: "stabilizing stays (no changed values)", status: models.StackStatusStabilizing, wantMarked: true, wantStatus: models.StackStatusStabilizing},
	{name: "stopping of a stop stays", status: models.StackStatusStopping, wantStatus: models.StackStatusStopping},
	{name: "stopped stays", status: models.StackStatusStopped, wantStatus: models.StackStatusStopped},
}

func runMarkStatusCase(t *testing.T, repo *GORMStackInstanceRepository, status string, wantMarked bool, wantStatus string) {
	t.Helper()
	inst := &models.StackInstance{
		Name: "mark", StackDefinitionID: "d1", Namespace: "stack-mark", OwnerID: "o1",
		Branch: "main", Status: status,
	}
	require.NoError(t, repo.Create(context.Background(), inst))

	marked, err := repo.MarkStatus(context.Background(), inst.ID, models.StackStatusStabilizing,
		[]string{models.StackStatusDeploying, models.StackStatusStabilizing})
	require.NoError(t, err)
	assert.Equal(t, wantMarked, marked)
	stored, err := repo.FindByID(context.Background(), inst.ID)
	require.NoError(t, err)
	assert.Equal(t, wantStatus, stored.Status)
}

func TestMarkStatus(t *testing.T) {
	t.Parallel()
	for _, tt := range markStatusCases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runMarkStatusCase(t, setupStackInstanceRepo(t), tt.status, tt.wantMarked, tt.wantStatus)
		})
	}

	t.Run("unknown instance", func(t *testing.T) {
		t.Parallel()
		marked, err := setupStackInstanceRepo(t).MarkStatus(context.Background(), "missing", models.StackStatusStabilizing,
			[]string{models.StackStatusDeploying})
		require.NoError(t, err)
		assert.False(t, marked)
	})
}

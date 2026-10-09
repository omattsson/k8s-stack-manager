package database

import (
	"context"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStackLifecycleColumnsMigration(t *testing.T) {
	t.Parallel()
	db := setupTestDBWithAllTables(t)
	m := db.Migrator()
	mig := stackLifecycleColumnsMigration()

	// Down removes the columns; Up adds them again.
	require.NoError(t, mig.Down(db))
	assert.False(t, m.HasColumn(&models.StackInstance{}, "StoppedAt"))
	assert.False(t, m.HasColumn(&models.StackDefinition{}, "OwnerInstanceID"))
	assert.False(t, m.HasColumn(&models.DeploymentLog{}, "ChartVersions"))
	assert.False(t, m.HasColumn(&models.DeploymentLog{}, "Branch"))
	require.NoError(t, mig.Down(db), "Down must be idempotent")

	// Seed rows without the new columns (raw SQL, the model has them).
	updated := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	stopLogDone := time.Date(2026, 9, 5, 8, 0, 0, 0, time.UTC)
	for _, row := range []struct{ id, status string }{
		{"stopped-with-log", models.StackStatusStopped},
		{"stopped-no-log", models.StackStatusStopped},
		{"running", models.StackStatusRunning},
	} {
		require.NoError(t, db.Exec(
			"INSERT INTO stack_instances (id, name, namespace, owner_id, stack_definition_id, status, created_at, updated_at, ttl_minutes) VALUES (?, ?, ?, 'u1', 'd1', ?, ?, ?, 0)",
			row.id, row.id, "stack-"+row.id, row.status, updated, updated,
		).Error)
	}
	require.NoError(t, db.Exec(
		"INSERT INTO deployment_logs (id, stack_instance_id, action, status, started_at, completed_at) VALUES ('l1', 'stopped-with-log', 'stop', 'success', ?, ?), ('l2', 'stopped-with-log', 'stop', 'error', ?, ?)",
		stopLogDone.Add(-time.Minute), stopLogDone, stopLogDone, stopLogDone.Add(time.Hour),
	).Error)

	require.NoError(t, mig.Up(db))
	require.NoError(t, mig.Up(db), "Up must be idempotent")
	assert.True(t, m.HasColumn(&models.StackInstance{}, "StoppedAt"))
	assert.True(t, m.HasColumn(&models.StackDefinition{}, "OwnerInstanceID"))
	assert.True(t, m.HasIndex(&models.StackDefinition{}, "OwnerInstanceID"))
	assert.True(t, m.HasColumn(&models.DeploymentLog{}, "ChartVersions"))
	assert.True(t, m.HasColumn(&models.DeploymentLog{}, "Branch"))

	repo := NewGORMStackInstanceRepository(db)
	withLog, err := repo.FindByID("stopped-with-log")
	require.NoError(t, err)
	require.NotNil(t, withLog.StoppedAt)
	assert.True(t, withLog.StoppedAt.Equal(stopLogDone), "backfill uses the last successful stop log: %s", withLog.StoppedAt)

	noLog, err := repo.FindByID("stopped-no-log")
	require.NoError(t, err)
	require.NotNil(t, noLog.StoppedAt)
	assert.True(t, noLog.StoppedAt.Equal(updated), "backfill falls back to updated_at")

	running, err := repo.FindByID("running")
	require.NoError(t, err)
	assert.Nil(t, running.StoppedAt)
}

func TestDeleteInstanceWithOwnedDefinition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		owner       string // OwnerInstanceID of the definition
		otherInst   bool   // instance i2 also uses the definition
		ownerAlive  bool   // instance "i-owner" exists (and does not use d1)
		wantDefKept bool
	}{
		{name: "owned definition without other instances is deleted", owner: "i1", wantDefKept: false},
		{name: "owned definition with another instance is kept", owner: "i1", otherInst: true, wantDefKept: true},
		{name: "user definition is kept", owner: "", wantDefKept: true},
		{name: "definition of a living owner instance is kept", owner: "i-owner", ownerAlive: true, wantDefKept: true},
		{name: "definition of a deleted owner (clone of quick deploy) is deleted", owner: "i-gone", wantDefKept: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			db := setupTestDBWithAllTables(t)
			runner := NewGORMTxRunner(db)
			defRepo := NewGORMStackDefinitionRepository(db)
			ccRepo := NewGORMChartConfigRepository(db)
			instRepo := NewGORMStackInstanceRepository(db)
			boRepo := NewGORMChartBranchOverrideRepository(db)
			ovRepo := NewGORMValueOverrideRepository(db)
			quotaRepo := NewGORMInstanceQuotaOverrideRepository(db)
			ctx := context.Background()

			require.NoError(t, defRepo.Create(&models.StackDefinition{ID: "d1", Name: "qd", OwnerID: "u1", OwnerInstanceID: tt.owner}))
			require.NoError(t, ccRepo.Create(&models.ChartConfig{ID: "c1", StackDefinitionID: "d1", ChartName: "web"}))
			inst := &models.StackInstance{ID: "i1", StackDefinitionID: "d1", Name: "qd", Namespace: "stack-qd-u1", OwnerID: "u1", Status: models.StackStatusDraft}
			require.NoError(t, instRepo.Create(inst))
			require.NoError(t, boRepo.Set(&models.ChartBranchOverride{ID: "b1", StackInstanceID: "i1", ChartConfigID: "c1", Branch: "x"}))
			require.NoError(t, ovRepo.Create(&models.ValueOverride{ID: "v1", StackInstanceID: "i1", ChartConfigID: "c1", Values: "a: 1"}))
			require.NoError(t, quotaRepo.Upsert(ctx, &models.InstanceQuotaOverride{StackInstanceID: "i1", CPULimit: "1"}))
			if tt.otherInst {
				require.NoError(t, instRepo.Create(&models.StackInstance{ID: "i2", StackDefinitionID: "d1", Name: "qd2", Namespace: "stack-qd2-u1", OwnerID: "u1", Status: models.StackStatusDraft}))
			}
			if tt.ownerAlive {
				require.NoError(t, instRepo.Create(&models.StackInstance{ID: "i-owner", StackDefinitionID: "d-other", Name: "own", Namespace: "stack-own-u1", OwnerID: "u1", Status: models.StackStatusDraft}))
			}

			require.NoError(t, DeleteInstanceWithOwnedDefinition(runner, inst))

			_, err := instRepo.FindByID("i1")
			assert.Error(t, err, "instance is deleted")
			bos, err := boRepo.List("i1")
			require.NoError(t, err)
			assert.Empty(t, bos, "branch overrides are deleted")
			ovs, err := ovRepo.ListByInstance("i1")
			require.NoError(t, err)
			assert.Empty(t, ovs, "value overrides are deleted")
			_, err = quotaRepo.GetByInstanceID(ctx, "i1")
			assert.Error(t, err, "quota override is deleted")

			_, err = defRepo.FindByID("d1")
			assert.Equal(t, tt.wantDefKept, err == nil)
			charts, err := ccRepo.ListByDefinition("d1")
			require.NoError(t, err)
			assert.Equal(t, tt.wantDefKept, len(charts) == 1)
		})
	}
}

// failingTxRunner runs the first transaction and fails every later one, to
// show that a failed definition cleanup does not undo the instance delete.
type failingTxRunner struct {
	inner TxRunner
	calls int
}

func (f *failingTxRunner) RunInTx(fn func(repos TxRepos) error) error {
	f.calls++
	if f.calls > 1 {
		return assert.AnError
	}
	return f.inner.RunInTx(fn)
}

func TestDeleteInstanceWithOwnedDefinition_CleanupErrorKeepsDelete(t *testing.T) {
	t.Parallel()
	db := setupTestDBWithAllTables(t)
	instRepo := NewGORMStackInstanceRepository(db)
	defRepo := NewGORMStackDefinitionRepository(db)
	require.NoError(t, defRepo.Create(&models.StackDefinition{ID: "d1", Name: "qd", OwnerID: "u1", OwnerInstanceID: "i1"}))
	inst := &models.StackInstance{ID: "i1", StackDefinitionID: "d1", Name: "qd", Namespace: "stack-qd-u1", OwnerID: "u1", Status: models.StackStatusDraft}
	require.NoError(t, instRepo.Create(inst))

	runner := &failingTxRunner{inner: NewGORMTxRunner(db)}
	require.NoError(t, DeleteInstanceWithOwnedDefinition(runner, inst))
	assert.Equal(t, 2, runner.calls)
	_, err := instRepo.FindByID("i1")
	assert.Error(t, err, "instance stays deleted")
	_, err = defRepo.FindByID("d1")
	assert.NoError(t, err, "definition cleanup failed and is left for a later delete")
}

func TestInstanceQuotaOverrideInTxRepos(t *testing.T) {
	t.Parallel()
	db := setupTestDBWithAllTables(t)
	runner := NewGORMTxRunner(db)
	require.NoError(t, runner.RunInTx(func(repos TxRepos) error {
		require.NotNil(t, repos.InstanceQuotaOverride)
		return repos.InstanceQuotaOverride.Upsert(context.Background(), &models.InstanceQuotaOverride{StackInstanceID: "i1", CPULimit: "1"})
	}))
	got, err := NewGORMInstanceQuotaOverrideRepository(db).GetByInstanceID(context.Background(), "i1")
	require.NoError(t, err)
	assert.Equal(t, "1", got.CPULimit)
}

func TestListLatestByActions(t *testing.T) {
	t.Parallel()
	db := setupTestDBWithAllTables(t)
	repo := NewGORMDeploymentLogRepository(db)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)
	for i, l := range []models.DeploymentLog{
		{ID: "a", Action: models.DeployActionDeploy, Status: models.DeployLogSuccess, ValuesSnapshot: "big"},
		{ID: "b", Action: models.DeployActionRollback, Status: models.DeployLogSuccess, ValuesSnapshot: "big", Output: "out", Branch: "main"},
		{ID: "c", Action: models.DeployActionStop, Status: models.DeployLogSuccess},
	} {
		l.StackInstanceID = "i1"
		l.StartedAt = base.Add(time.Duration(i) * time.Minute)
		require.NoError(t, repo.Create(ctx, &l))
	}

	logs, err := repo.ListLatestByActions(ctx, "i1", []string{models.DeployActionDeploy, models.DeployActionRollback}, 5)
	require.NoError(t, err)
	require.Len(t, logs, 2, "stop logs are ignored")
	assert.Equal(t, "a", logs[1].ID)
	got := logs[0]
	assert.Equal(t, "b", got.ID)
	assert.Equal(t, models.DeployActionRollback, got.Action)
	assert.Equal(t, "main", got.Branch)
	assert.Empty(t, got.ValuesSnapshot, "large columns are not loaded")
	assert.Empty(t, got.Output)

	one, err := repo.ListLatestByActions(ctx, "i1", []string{models.DeployActionDeploy, models.DeployActionRollback}, 1)
	require.NoError(t, err)
	require.Len(t, one, 1)
	assert.Equal(t, "b", one[0].ID)

	none, err := repo.ListLatestByActions(ctx, "other", []string{models.DeployActionDeploy}, 1)
	require.NoError(t, err)
	assert.Empty(t, none)
}

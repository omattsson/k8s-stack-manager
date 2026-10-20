package database

import (
	"fmt"
	"testing"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// defReadSpy records each definition read as "tx<n>:find" (plain) or
// "tx<n>:lock" (FOR UPDATE), n being the transaction number of spyTxRunner.
// lockedOwner, when set, replaces OwnerInstanceID in the locked read (a
// change between the reads); lockedGone makes the locked read not find it.
type defReadSpy struct {
	findErr     error
	lockedOwner *string
	events      []string
	tx          int
	lockedGone  bool
}

type lockSpyDefinitionRepo struct {
	models.StackDefinitionRepository
	spy *defReadSpy
}

func (r *lockSpyDefinitionRepo) FindByID(id string) (*models.StackDefinition, error) {
	r.spy.events = append(r.spy.events, fmt.Sprintf("tx%d:find", r.spy.tx))
	if r.spy.findErr != nil {
		return nil, r.spy.findErr
	}
	return r.StackDefinitionRepository.FindByID(id)
}

func (r *lockSpyDefinitionRepo) FindByIDForUpdate(id string) (*models.StackDefinition, error) {
	r.spy.events = append(r.spy.events, fmt.Sprintf("tx%d:lock", r.spy.tx))
	if r.spy.lockedGone {
		return nil, dberrors.NewDatabaseError("find_by_id_for_update", dberrors.ErrNotFound)
	}
	def, err := r.StackDefinitionRepository.FindByIDForUpdate(id)
	if err == nil && r.spy.lockedOwner != nil {
		def.OwnerInstanceID = *r.spy.lockedOwner
	}
	return def, err
}

// spyTxRunner numbers the transactions and puts the spy in front of the
// definition repository of each one.
type spyTxRunner struct {
	inner TxRunner
	spy   *defReadSpy
}

func (r *spyTxRunner) RunInTx(fn func(repos TxRepos) error) error {
	r.spy.tx++
	return r.inner.RunInTx(func(repos TxRepos) error {
		repos.StackDefinition = &lockSpyDefinitionRepo{StackDefinitionRepository: repos.StackDefinition, spy: r.spy}
		return fn(repos)
	})
}

// TestDeleteInstanceWithOwnedDefinition_LocksOnlyOwnedDefinitions checks
// #654: the owner check is a plain read in its own transaction (tx2); only a
// definition with an owner gets the cleanup transaction (tx3), whose first
// read locks the row.
func TestDeleteInstanceWithOwnedDefinition_LocksOnlyOwnedDefinitions(t *testing.T) {
	t.Parallel()

	cleared := ""
	tests := []struct {
		name        string
		owner       string // OwnerInstanceID of d1; "-" means no definition
		findErr     error
		lockedOwner *string
		lockedGone  bool
		wantEvents  []string
		wantDefKept bool
	}{
		{name: "definition without an owner gets no cleanup transaction", owner: "", wantEvents: []string{"tx2:find"}, wantDefKept: true},
		{name: "missing definition gets no cleanup transaction", owner: "-", wantEvents: []string{"tx2:find"}},
		{name: "owner check error skips the cleanup", owner: "i1", findErr: errDefinitionRead, wantEvents: []string{"tx2:find"}, wantDefKept: true},
		{name: "owned definition is locked first and deleted", owner: "i1", wantEvents: []string{"tx2:find", "tx3:lock"}},
		{name: "owner removed before the lock keeps the definition", owner: "i1", lockedOwner: &cleared, wantEvents: []string{"tx2:find", "tx3:lock"}, wantDefKept: true},
		{name: "definition deleted before the lock is no error", owner: "i1", lockedGone: true, wantEvents: []string{"tx2:find", "tx3:lock"}, wantDefKept: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			db := setupTestDBWithAllTables(t)
			defRepo := NewGORMStackDefinitionRepository(db)
			ccRepo := NewGORMChartConfigRepository(db)
			instRepo := NewGORMStackInstanceRepository(db)
			if tt.owner != "-" {
				require.NoError(t, defRepo.Create(&models.StackDefinition{ID: "d1", Name: "qd", OwnerID: "u1", OwnerInstanceID: tt.owner}))
				require.NoError(t, ccRepo.Create(&models.ChartConfig{ID: "c1", StackDefinitionID: "d1", ChartName: "web"}))
			}
			inst := &models.StackInstance{ID: "i1", StackDefinitionID: "d1", Name: "qd", Namespace: "stack-qd-u1", OwnerID: "u1", Status: models.StackStatusDraft}
			require.NoError(t, instRepo.Create(inst))

			spy := &defReadSpy{findErr: tt.findErr, lockedOwner: tt.lockedOwner, lockedGone: tt.lockedGone}
			require.NoError(t, DeleteInstanceWithOwnedDefinition(&spyTxRunner{inner: NewGORMTxRunner(db), spy: spy}, inst))

			assert.Equal(t, tt.wantEvents, spy.events)
			_, err := instRepo.FindByID("i1")
			assert.Error(t, err, "instance is deleted")
			if tt.owner == "-" {
				return
			}
			_, err = defRepo.FindByID("d1")
			assert.Equal(t, tt.wantDefKept, err == nil)
			charts, err := ccRepo.ListByDefinition("d1")
			require.NoError(t, err)
			assert.Equal(t, tt.wantDefKept, len(charts) == 1)
		})
	}
}

// TestCleanupOwnedDefinition_LocksFirst checks that a direct caller of
// CleanupOwnedDefinition keeps the lock as the first definition read.
func TestCleanupOwnedDefinition_LocksFirst(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		owner       string
		wantDefKept bool
	}{
		{name: "definition without an owner", owner: "", wantDefKept: true},
		{name: "owned definition", owner: "i1", wantDefKept: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			db := setupTestDBWithAllTables(t)
			defRepo := NewGORMStackDefinitionRepository(db)
			require.NoError(t, defRepo.Create(&models.StackDefinition{ID: "d1", Name: "qd", OwnerID: "u1", OwnerInstanceID: tt.owner}))
			inst := &models.StackInstance{ID: "i1", StackDefinitionID: "d1"}

			spy := &defReadSpy{}
			runner := &spyTxRunner{inner: NewGORMTxRunner(db), spy: spy}
			require.NoError(t, runner.RunInTx(func(repos TxRepos) error {
				return CleanupOwnedDefinition(repos, inst)
			}))

			assert.Equal(t, []string{"tx1:lock"}, spy.events)
			_, err := defRepo.FindByID("d1")
			assert.Equal(t, tt.wantDefKept, err == nil)
		})
	}
}

var errDefinitionRead = dberrors.NewDatabaseError("find_by_id", dberrors.ErrConnectionFailed)

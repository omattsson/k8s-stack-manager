//go:build integration

package database

import (
	"errors"
	"sync"
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type guardOpKind int

const (
	guardDemote guardOpKind = iota
	guardDisable
	guardDelete
)

type guardOp struct {
	kind             guardOpKind
	caller, targetID string
}

func (o guardOp) run(r *GORMUserRepository) error {
	switch o.kind {
	case guardDemote:
		_, err := r.UpdateRole(o.caller, o.targetID, models.RoleUser)
		return err
	case guardDisable:
		return r.SetDisabled(o.caller, o.targetID, true)
	default:
		return r.DeleteGuarded(o.caller, o.targetID)
	}
}

// TestDatabaseUserGuardedChangesConcurrent runs admins who demote, disable or
// delete each other at the same time on MySQL. The admin-row locks serialize
// the transactions: a change whose caller lost the admin role or was
// disabled by an earlier change fails with ErrCallerNotAdmin, and at least
// one enabled admin always remains.
func TestDatabaseUserGuardedChangesConcurrent(t *testing.T) {
	tests := []struct {
		name          string
		admins        []string
		ops           []guardOp
		wantSucceeded int
		wantAdmins    int
	}{
		{name: "demote vs demote", admins: []string{"admin-a", "admin-b"},
			ops: []guardOp{{guardDemote, "admin-a", "admin-b"}, {guardDemote, "admin-b", "admin-a"}}, wantSucceeded: 1, wantAdmins: 1},
		{name: "disable vs disable", admins: []string{"admin-a", "admin-b"},
			ops: []guardOp{{guardDisable, "admin-a", "admin-b"}, {guardDisable, "admin-b", "admin-a"}}, wantSucceeded: 1, wantAdmins: 1},
		{name: "delete vs delete", admins: []string{"admin-a", "admin-b"},
			ops: []guardOp{{guardDelete, "admin-a", "admin-b"}, {guardDelete, "admin-b", "admin-a"}}, wantSucceeded: 1, wantAdmins: 1},
		{name: "demote vs disable", admins: []string{"admin-a", "admin-b"},
			ops: []guardOp{{guardDemote, "admin-a", "admin-b"}, {guardDisable, "admin-b", "admin-a"}}, wantSucceeded: 1, wantAdmins: 1},
		{name: "delete vs demote", admins: []string{"admin-a", "admin-b"},
			ops: []guardOp{{guardDelete, "admin-a", "admin-b"}, {guardDemote, "admin-b", "admin-a"}}, wantSucceeded: 1, wantAdmins: 1},
		// A cycle of three: the first change removes one admin, the change of
		// that admin fails, the third change succeeds. One admin remains.
		{name: "three admins demote in a cycle", admins: []string{"admin-a", "admin-b", "admin-c"},
			ops:           []guardOp{{guardDemote, "admin-a", "admin-b"}, {guardDemote, "admin-b", "admin-c"}, {guardDemote, "admin-c", "admin-a"}},
			wantSucceeded: 2, wantAdmins: 1},
		{name: "three admins, mixed operations in a cycle", admins: []string{"admin-a", "admin-b", "admin-c"},
			ops:           []guardOp{{guardDisable, "admin-a", "admin-b"}, {guardDelete, "admin-b", "admin-c"}, {guardDemote, "admin-c", "admin-a"}},
			wantSucceeded: 2, wantAdmins: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := setupMySQLTestDB(t)
			repo := NewGORMUserRepository(db)

			for round := 0; round < 10; round++ {
				require.NoError(t, db.Exec("DELETE FROM users").Error)
				for _, id := range tt.admins {
					require.NoError(t, repo.Create(&models.User{ID: id, Username: id, Role: models.RoleAdmin}))
				}

				var wg sync.WaitGroup
				start := make(chan struct{})
				errs := make([]error, len(tt.ops))
				for i, op := range tt.ops {
					wg.Add(1)
					go func(i int, op guardOp) {
						defer wg.Done()
						<-start
						errs[i] = op.run(repo)
					}(i, op)
				}
				close(start)
				wg.Wait()

				succeeded := 0
				for _, err := range errs {
					if err == nil {
						succeeded++
						continue
					}
					assert.True(t, errors.Is(err, models.ErrCallerNotAdmin),
						"round %d: unexpected error %v", round, err)
				}
				assert.Equal(t, tt.wantSucceeded, succeeded, "round %d: errors %v", round, errs)

				admins, err := repo.ListByRoles([]string{models.RoleAdmin})
				require.NoError(t, err)
				enabled := 0
				for _, a := range admins {
					if !a.Disabled {
						enabled++
					}
				}
				assert.Equal(t, tt.wantAdmins, enabled, "round %d: enabled admins", round)
			}
		})
	}
}

// TestDatabaseUserIDCaseInsensitive documents why the handlers use the
// stored ID after FindByID: MySQL compares IDs without case.
func TestDatabaseUserIDCaseInsensitive(t *testing.T) {
	db := setupMySQLTestDB(t)
	repo := NewGORMUserRepository(db)
	require.NoError(t, repo.Create(&models.User{ID: "user-abc", Username: "abc", Role: models.RoleUser}))

	u, err := repo.FindByID("USER-ABC")
	require.NoError(t, err)
	assert.Equal(t, "user-abc", u.ID)
}

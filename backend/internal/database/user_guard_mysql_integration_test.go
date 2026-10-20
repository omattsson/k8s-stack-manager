//go:build integration

package database

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"backend/internal/models"

	"github.com/google/uuid"
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

// TestDatabaseUserGuardedChangesNoDeadlock runs concurrent guarded deletes and
// role changes, SSO role syncs and admin creates on different users: no call
// may fail with a deadlock, and the deadlocks that happen are retried (#639).
func TestDatabaseUserGuardedChangesNoDeadlock(t *testing.T) {
	const (
		caller     = "admin-root"
		goroutines = 8 // more goroutines can use up the 3 attempts of one call
		minRounds  = 10
		maxRounds  = 50
	)
	admin, user := models.RoleAdmin, models.RoleUser
	ops := []struct {
		run      func(r *GORMUserRepository, target string) error
		name     string
		oldRole  string // "": run creates the user
		provider string
	}{
		{name: "delete", oldRole: user, provider: "local", run: func(r *GORMUserRepository, target string) error {
			return r.DeleteGuarded(caller, target)
		}},
		{name: "promote", oldRole: user, provider: "local", run: func(r *GORMUserRepository, target string) error {
			_, err := r.UpdateRole(caller, target, admin)
			return err
		}},
		{name: "demote", oldRole: admin, provider: "local", run: func(r *GORMUserRepository, target string) error {
			_, err := r.UpdateRole(caller, target, user)
			return err
		}},
		{name: "sso-promote", oldRole: user, provider: "oidc", run: func(r *GORMUserRepository, target string) error {
			return r.UpdateProfile(target, models.UserProfileUpdate{Role: &admin})
		}},
		{name: "sso-demote", oldRole: admin, provider: "oidc", run: func(r *GORMUserRepository, target string) error {
			return r.UpdateProfile(target, models.UserProfileUpdate{Role: &user})
		}},
		{name: "create-admin", run: func(r *GORMUserRepository, target string) error {
			return r.Create(&models.User{ID: target, Username: "new-" + target, Role: admin})
		}},
	}

	db := setupMySQLTestDB(t)
	repo := NewGORMUserRepository(db)
	retriesBefore := deadlockRetries.Load()

	// Run until at least one deadlock was retried, so the test proves the retry path.
	for round := 0; round < maxRounds && (round < minRounds || deadlockRetries.Load() == retriesBefore); round++ {
		require.NoError(t, db.Exec("DELETE FROM users").Error)
		require.NoError(t, repo.Create(&models.User{ID: caller, Username: caller, Role: admin}))
		// Random IDs as in production: the deadlock needs a promoted user whose ID sorts before the admin rows.
		targets := make([]string, goroutines)
		for i := range targets {
			op := ops[i%len(ops)]
			targets[i] = uuid.New().String()
			if op.oldRole != "" {
				require.NoError(t, repo.Create(&models.User{ID: targets[i], Username: fmt.Sprintf("u-%02d-%s", i, op.name),
					Role: op.oldRole, AuthProvider: op.provider}))
			}
		}

		var wg sync.WaitGroup
		start := make(chan struct{})
		errs := make([]error, goroutines)
		for i := range targets {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				errs[i] = ops[i%len(ops)].run(repo, targets[i])
			}(i)
		}
		close(start)
		wg.Wait()

		for i, err := range errs {
			assert.False(t, isDeadlockError(err), "round %d, %s: deadlock after the retries: %v", round, ops[i%len(ops)].name, err)
			assert.NoError(t, err, "round %d, %s", round, ops[i%len(ops)].name)
		}
	}
	// MySQL decides when it deadlocks, so zero retries is not a failure.
	// TestGuardedAdminChange_RetriesTransaction proves the retry path.
	t.Logf("deadlock retries: %d", deadlockRetries.Load()-retriesBefore)
}

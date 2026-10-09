package database

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createExpiringInstance creates a running instance that expires in
// expiresIn and returns the stored copy.
func createExpiringInstance(t *testing.T, repo *GORMStackInstanceRepository, name string, expiresIn time.Duration) *models.StackInstance {
	t.Helper()
	exp := time.Now().UTC().Add(expiresIn)
	inst := &models.StackInstance{
		Name: name, StackDefinitionID: "d1", Namespace: "stack-" + name,
		OwnerID: "o1", Branch: "main", Status: models.StackStatusRunning, ExpiresAt: &exp,
	}
	require.NoError(t, repo.Create(inst))
	stored, err := repo.FindByID(inst.ID)
	require.NoError(t, err)
	return stored
}

func isWarned(t *testing.T, repo *GORMStackInstanceRepository, id string) bool {
	t.Helper()
	inst, err := repo.FindByID(id)
	require.NoError(t, err)
	return inst.ExpiryWarnedAt != nil
}

func TestExpiryWarnedAtMigration(t *testing.T) {
	t.Parallel()
	db := setupTestDBWithAllTables(t)
	m := db.Migrator()
	mig := expiryWarnedAtMigration()

	require.NoError(t, mig.Down(db))
	assert.False(t, m.HasColumn(&models.StackInstance{}, "ExpiryWarnedAt"))
	require.NoError(t, mig.Down(db), "Down must be idempotent")

	require.NoError(t, mig.Up(db))
	require.NoError(t, mig.Up(db), "Up must be idempotent")
	assert.True(t, m.HasColumn(&models.StackInstance{}, "ExpiryWarnedAt"))
}

func TestMarkExpiryWarned(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		expiresAt func(stored time.Time) time.Time // value passed to MarkExpiryWarned
		premark   bool
		want      bool
	}{
		{name: "first mark wins", expiresAt: func(s time.Time) time.Time { return s }, want: true},
		{name: "already marked", expiresAt: func(s time.Time) time.Time { return s }, premark: true, want: false},
		{name: "expiry changed since the list", expiresAt: func(s time.Time) time.Time { return s.Add(-time.Hour) }, want: false},
		{name: "sub-second precision difference matches", expiresAt: func(s time.Time) time.Time { return s.Add(300 * time.Millisecond) }, want: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupStackInstanceRepo(t)
			inst := createExpiringInstance(t, repo, "mark", 10*time.Minute)
			if tt.premark {
				ok, err := repo.MarkExpiryWarned(inst.ID, *inst.ExpiresAt, time.Now())
				require.NoError(t, err)
				require.True(t, ok)
			}

			ok, err := repo.MarkExpiryWarned(inst.ID, tt.expiresAt(*inst.ExpiresAt), time.Now())
			require.NoError(t, err)
			assert.Equal(t, tt.want, ok)
			assert.Equal(t, tt.want || tt.premark, isWarned(t, repo, inst.ID))
		})
	}
}

func TestMarkExpiryWarned_ConcurrentCallersOneWins(t *testing.T) {
	t.Parallel()
	repo := setupStackInstanceRepo(t)
	inst := createExpiringInstance(t, repo, "race", 10*time.Minute)

	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := repo.MarkExpiryWarned(inst.ID, *inst.ExpiresAt, time.Now())
			assert.NoError(t, err)
			if ok {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(1), wins.Load())
}

func TestListExpiringSoon_SkipsWarnedInstances(t *testing.T) {
	t.Parallel()
	repo := setupStackInstanceRepo(t)
	warned := createExpiringInstance(t, repo, "warned", 10*time.Minute)
	createExpiringInstance(t, repo, "not-warned", 10*time.Minute)
	createExpiringInstance(t, repo, "later", 2*time.Hour)

	ok, err := repo.MarkExpiryWarned(warned.ID, *warned.ExpiresAt, time.Now())
	require.NoError(t, err)
	require.True(t, ok)

	list, err := repo.ListExpiringSoon(30 * time.Minute)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "not-warned", list[0].Name)
}

func TestUpdate_ExpiryWarningMark(t *testing.T) {
	t.Parallel()

	tests := []struct {
		change     func(inst *models.StackInstance)
		name       string
		wantWarned bool
	}{
		{
			name:       "status change of a stale copy keeps the mark",
			change:     func(inst *models.StackInstance) { inst.Status = models.StackStatusPartial },
			wantWarned: true,
		},
		{
			name: "same expiry with sub-second difference keeps the mark",
			change: func(inst *models.StackInstance) {
				exp := inst.ExpiresAt.Add(400 * time.Millisecond)
				inst.ExpiresAt = &exp
			},
			wantWarned: true,
		},
		{
			name: "extend clears the mark",
			change: func(inst *models.StackInstance) {
				exp := inst.ExpiresAt.Add(time.Hour)
				inst.ExpiresAt = &exp
			},
			wantWarned: false,
		},
		{
			name: "earlier expiry (TTL reset) clears the mark",
			change: func(inst *models.StackInstance) {
				exp := inst.ExpiresAt.Add(-5 * time.Minute)
				inst.ExpiresAt = &exp
			},
			wantWarned: false,
		},
		{
			name:       "removing the expiry clears the mark",
			change:     func(inst *models.StackInstance) { inst.ExpiresAt = nil },
			wantWarned: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupStackInstanceRepo(t)
			inst := createExpiringInstance(t, repo, "upd", 10*time.Minute)

			// The copy was read before the warning (ExpiryWarnedAt is nil).
			stale := *inst
			ok, err := repo.MarkExpiryWarned(inst.ID, *inst.ExpiresAt, time.Now())
			require.NoError(t, err)
			require.True(t, ok)

			tt.change(&stale)
			require.NoError(t, repo.Update(&stale))
			assert.Equal(t, tt.wantWarned, isWarned(t, repo, inst.ID))

			// The other columns are written as before.
			stored, err := repo.FindByID(inst.ID)
			require.NoError(t, err)
			assert.Equal(t, stale.Status, stored.Status)
			if stale.ExpiresAt == nil {
				assert.Nil(t, stored.ExpiresAt)
			} else {
				require.NotNil(t, stored.ExpiresAt)
				assert.WithinDuration(t, *stale.ExpiresAt, *stored.ExpiresAt, time.Millisecond)
			}
		})
	}
}

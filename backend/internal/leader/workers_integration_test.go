package leader

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/database"
	"backend/internal/models"
	"backend/internal/scheduler"
	"backend/internal/ttl"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"k8s.io/client-go/kubernetes/fake"
)

// countingNotifier counts expiry warnings.
type countingNotifier struct {
	calls atomic.Int32
}

func (n *countingNotifier) NotifyInstance(context.Context, models.NotificationTarget, string, string, string) error {
	n.calls.Add(1)
	return nil
}

// countingExecutor counts cleanup actions.
type countingExecutor struct {
	calls atomic.Int32
}

func (e *countingExecutor) StopInstance(context.Context, *models.StackInstance) error {
	e.calls.Add(1)
	return nil
}

func (e *countingExecutor) CleanInstance(context.Context, *models.StackInstance) error {
	e.calls.Add(1)
	return nil
}

func (e *countingExecutor) DeleteInstance(context.Context, *models.StackInstance) error {
	e.calls.Add(1)
	return nil
}

// replica is one backend replica with an expiry warner and a cleanup
// scheduler as leader workers.
type replica struct {
	elector *Elector
	exec    *countingExecutor
	sched   *scheduler.Scheduler
	cancel  context.CancelFunc
	done    chan struct{}
}

func startReplica(t *testing.T, identity string, client *fake.Clientset, db *gorm.DB, notifier *countingNotifier) *replica {
	t.Helper()
	instances := database.NewGORMStackInstanceRepository(db)
	policies := database.NewGORMCleanupPolicyRepository(db)

	exec := &countingExecutor{}
	sched := scheduler.NewScheduler(policies, instances, nil, exec, nil).WithReloadInterval(100 * time.Millisecond)
	warner := ttl.NewWarner(instances, notifier, 30*time.Minute, 100*time.Millisecond)
	group := NewGroup(5*time.Second,
		Worker{Name: "expiry-warner", Run: warner.Run},
		Worker{Name: "cleanup-scheduler", Run: sched.Run},
	)

	e, err := New(testConfig(identity), client)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	r := &replica{elector: e, exec: exec, sched: sched, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(r.done)
		e.Run(ctx, group.Start, group.Stop)
	}()
	t.Cleanup(func() {
		r.cancel()
		<-r.done
	})
	return r
}

// TestTwoReplicasOneDatabase runs two worker sets against one database and
// one fake Lease: one expiry warning in total, and cleanup policy runs only
// on the current leader, also after a leader change.
func TestTwoReplicasOneDatabase(t *testing.T) {
	t.Parallel()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&models.StackInstance{}, &models.CleanupPolicy{}))

	exp := time.Now().UTC().Add(10 * time.Minute)
	require.NoError(t, database.NewGORMStackInstanceRepository(db).Create(&models.StackInstance{
		Name: "expiring", StackDefinitionID: "d1", Namespace: "stack-expiring", OwnerID: "u1",
		Branch: "main", Status: models.StackStatusRunning, ExpiresAt: &exp,
	}))
	require.NoError(t, database.NewGORMCleanupPolicyRepository(db).Create(&models.CleanupPolicy{
		Name: "every-second", ClusterID: "all", Action: "stop", Condition: "status:running",
		Schedule: "@every 1s", Enabled: true,
	}))

	var blocked atomic.Bool
	clientA, clientB := sharedClients(&blocked)
	notifier := &countingNotifier{}

	a := startReplica(t, "pod-a", clientA, db, notifier)
	require.Eventually(t, a.elector.IsLeader, 5*time.Second, 20*time.Millisecond)
	b := startReplica(t, "pod-b", clientB, db, notifier)

	// The leader warns once and runs the policy; the follower does neither.
	require.Eventually(t, func() bool { return notifier.calls.Load() == 1 && a.exec.calls.Load() >= 1 },
		5*time.Second, 20*time.Millisecond)
	assert.False(t, b.sched.Active())
	assert.Equal(t, int32(0), b.exec.calls.Load())

	// a loses the API server; b takes over.
	blocked.Store(true)
	require.Eventually(t, func() bool { return b.elector.IsLeader() && b.sched.Active() },
		10*time.Second, 20*time.Millisecond)
	assert.False(t, a.sched.Active())
	aCalls := a.exec.calls.Load()

	require.Eventually(t, func() bool { return b.exec.calls.Load() >= 1 }, 5*time.Second, 20*time.Millisecond)
	time.Sleep(300 * time.Millisecond) // a few warner cycles on b
	assert.Equal(t, aCalls, a.exec.calls.Load(), "the old leader must not run the policy any more")
	assert.Equal(t, int32(1), notifier.calls.Load(), "the new leader must not warn again")
}

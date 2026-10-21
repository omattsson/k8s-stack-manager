package deployer

import (
	"context"
	"sync"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type callerCtxKey struct{}

func markedCtx() context.Context {
	return context.WithValue(context.Background(), callerCtxKey{}, "caller")
}

// ctxRecordingInstanceRepo records the marker and the context of the first
// FindByID, Update and Delete.
type ctxRecordingInstanceRepo struct {
	*mockInstanceRepo
	mu   sync.Mutex
	seen map[string]any
	ctxs map[string]context.Context
}

func newCtxRecordingInstanceRepo() *ctxRecordingInstanceRepo {
	return &ctxRecordingInstanceRepo{mockInstanceRepo: newMockInstanceRepo(), seen: map[string]any{}, ctxs: map[string]context.Context{}}
}

func (r *ctxRecordingInstanceRepo) record(ctx context.Context, method string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.seen[method]; !ok {
		r.seen[method] = ctx.Value(callerCtxKey{})
		r.ctxs[method] = ctx
	}
}

func (r *ctxRecordingInstanceRepo) ctxOf(method string) context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ctxs[method]
}

// ctxRecordingNotifier records the context of FollowerIDs.
type ctxRecordingNotifier struct {
	*mockNotifier
	mu  sync.Mutex
	ctx context.Context
}

func (n *ctxRecordingNotifier) FollowerIDs(ctx context.Context, instanceID string) []string {
	n.mu.Lock()
	n.ctx = ctx
	n.mu.Unlock()
	return n.mockNotifier.FollowerIDs(ctx, instanceID)
}

func (n *ctxRecordingNotifier) followersCtx() context.Context {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.ctx
}

func (r *ctxRecordingInstanceRepo) get(method string) (any, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.seen[method]
	return v, ok
}

func (r *ctxRecordingInstanceRepo) FindByID(ctx context.Context, id string) (*models.StackInstance, error) {
	r.record(ctx, "FindByID")
	return r.mockInstanceRepo.FindByID(ctx, id)
}

func (r *ctxRecordingInstanceRepo) Update(ctx context.Context, inst *models.StackInstance) error {
	r.record(ctx, "Update")
	return r.mockInstanceRepo.Update(ctx, inst)
}

func (r *ctxRecordingInstanceRepo) Delete(ctx context.Context, id string) error {
	r.record(ctx, "Delete")
	return r.mockInstanceRepo.Delete(ctx, id)
}

// TestInterruptRecovery_UsesWorkerContext checks that the interrupted
// operation recovery reads the instance with the context of the worker (#656).
func TestInterruptRecovery_UsesWorkerContext(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	instances := newCtxRecordingInstanceRepo()
	logs := newMockDeployLogRepo()
	require.NoError(t, instances.Create(context.Background(), &models.StackInstance{
		ID: "inst-1", Name: "demo", OwnerID: "owner-1", Status: models.StackStatusDeploying, UpdatedAt: now.Add(-time.Hour),
	}))
	deadline := now.Add(-time.Hour)
	require.NoError(t, logs.Create(context.Background(), &models.DeploymentLog{
		ID: "log-1", StackInstanceID: "inst-1", Action: models.DeployActionDeploy, Status: models.DeployLogRunning,
		StartedAt: deadline.Add(-30 * time.Minute), ReplicaID: "lost-pod", DeadlineAt: &deadline,
	}))
	heartbeats := newFakeHeartbeats()
	heartbeats.dbNow = now
	heartbeats.seen["self-pod"] = now
	rec := NewInterruptRecovery(InterruptRecoveryConfig{
		Operations: &fakeInterruptOps{logs: logs, instances: instances.mockInstanceRepo, heartbeats: heartbeats},
		Heartbeats: heartbeats, Instances: instances, SelfID: "self-pod",
	})
	require.NotNil(t, rec)
	rec.now = func() time.Time { return now }

	assert.Equal(t, 1, rec.RunOnce(markedCtx()))

	v, ok := instances.get("FindByID")
	require.True(t, ok)
	assert.Equal(t, "caller", v, "FindByID runs with the worker context")
}

// TestCleanupExecutor_DeleteUsesCallerContext checks that the direct delete
// of a cleanup policy and its follower read run with the context of the
// policy run without its cancel: the end of the job must not stop a delete
// that afterDelete reports (#656).
func TestCleanupExecutor_DeleteUsesCallerContext(t *testing.T) {
	t.Parallel()

	for _, withTx := range []bool{true, false} {
		withTx := withTx
		name := "without a transaction runner"
		if withTx {
			name = "with a transaction runner"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			instances := newCtxRecordingInstanceRepo()
			logs := newMockDeployLogRepo()
			inst := &models.StackInstance{ID: "inst-del", Name: "draft", StackDefinitionID: "def-1", Status: models.StackStatusDraft}
			require.NoError(t, instances.Create(context.Background(), inst))
			notif := &ctxRecordingNotifier{mockNotifier: &mockNotifier{}}
			cfg := ManagerConfig{InstanceRepo: instances, DeployLogRepo: logs, Notifier: notif}
			if withTx {
				cfg.TxRunner = &mockTxRunner{instanceRepo: instances, logRepo: logs}
			}
			mgr := NewManager(cfg)
			t.Cleanup(mgr.Shutdown)
			exec := NewCleanupExecutor(mgr, newMockDefinitionRepo(), newMockChartConfigRepo(), instances)
			jobCtx, cancelJob := context.WithCancel(markedCtx())
			defer cancelJob()

			require.NoError(t, exec.DeleteInstance(jobCtx, inst))
			cancelJob()

			v, ok := instances.get("Delete")
			require.True(t, ok)
			assert.Equal(t, "caller", v, "Delete runs with the context of the policy run")
			assert.NoError(t, instances.ctxOf("Delete").Err(), "the end of the job does not cancel the delete")
			followersCtx := notif.followersCtx()
			require.NotNil(t, followersCtx, "the followers are read before the delete")
			assert.Equal(t, "caller", followersCtx.Value(callerCtxKey{}), "FollowerIDs runs with the context of the policy run")
			assert.NoError(t, followersCtx.Err(), "the end of the job does not cancel the follower read")
		})
	}
}

// TestExpiryStopper_UsesWorkerContext checks that the stop start of the TTL
// reaper writes the instance with the context of the worker (#656).
func TestExpiryStopper_UsesWorkerContext(t *testing.T) {
	t.Parallel()
	instances := newCtxRecordingInstanceRepo()
	logs := newMockDeployLogRepo()
	inst := &models.StackInstance{ID: "inst-exp", Name: "exp", StackDefinitionID: "def-1", Namespace: "stack-exp", Status: models.StackStatusRunning}
	require.NoError(t, instances.Create(context.Background(), inst))
	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: &mockHelmExecutor{}, noK8sClient: true},
		InstanceRepo:  instances,
		DeployLogRepo: logs,
		TxRunner:      &mockTxRunner{instanceRepo: instances, logRepo: logs},
	})
	t.Cleanup(mgr.Shutdown)
	defRepo := newMockDefinitionRepo()
	defRepo.items["def-1"] = &models.StackDefinition{ID: "def-1", Name: "def"}
	ccRepo := newMockChartConfigRepo()
	ccRepo.items["def-1"] = []models.ChartConfig{{ID: "cc-1", StackDefinitionID: "def-1", ChartName: "app"}}

	require.NoError(t, NewExpiryStopper(mgr, defRepo, ccRepo).StopInstance(markedCtx(), inst))

	v, ok := instances.get("Update")
	require.True(t, ok)
	assert.Equal(t, "caller", v, "the stop start writes with the worker context")
}

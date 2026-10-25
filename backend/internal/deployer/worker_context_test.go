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

// ctxCall is the state of a context at a call, read at the call: a detached
// context is cancelled when the function that made it returns (#674).
type ctxCall struct {
	value       any // callerCtxKey{}
	err         error
	hasDeadline bool
}

func ctxCallOf(ctx context.Context) ctxCall {
	_, hasDeadline := ctx.Deadline()
	return ctxCall{value: ctx.Value(callerCtxKey{}), err: ctx.Err(), hasDeadline: hasDeadline}
}

// ctxRecordingInstanceRepo records the marker and the context state of the
// first FindByID, Update and Delete.
type ctxRecordingInstanceRepo struct {
	*mockInstanceRepo
	mu    sync.Mutex
	seen  map[string]any
	calls map[string]ctxCall
}

func newCtxRecordingInstanceRepo() *ctxRecordingInstanceRepo {
	return &ctxRecordingInstanceRepo{mockInstanceRepo: newMockInstanceRepo(), seen: map[string]any{}, calls: map[string]ctxCall{}}
}

func (r *ctxRecordingInstanceRepo) record(ctx context.Context, method string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.seen[method]; !ok {
		r.seen[method] = ctx.Value(callerCtxKey{})
		r.calls[method] = ctxCallOf(ctx)
	}
}

func (r *ctxRecordingInstanceRepo) callOf(method string) ctxCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[method]
}

// ctxRecordingNotifier records the context state of FollowerIDs. onFollowers,
// when set, runs at the start of FollowerIDs.
type ctxRecordingNotifier struct {
	*mockNotifier
	onFollowers func()
	mu          sync.Mutex
	call        *ctxCall
}

func (n *ctxRecordingNotifier) FollowerIDs(ctx context.Context, instanceID string) []string {
	if n.onFollowers != nil {
		n.onFollowers()
	}
	call := ctxCallOf(ctx)
	n.mu.Lock()
	n.call = &call
	n.mu.Unlock()
	return n.mockNotifier.FollowerIDs(ctx, instanceID)
}

func (n *ctxRecordingNotifier) followersCall() *ctxCall {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.call
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
// that afterDelete reports (#656). The job ends during the follower read; the
// detached context has a deadline (#674).
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
			jobCtx, cancelJob := context.WithCancel(markedCtx())
			defer cancelJob()
			notif := &ctxRecordingNotifier{mockNotifier: &mockNotifier{}, onFollowers: cancelJob}
			cfg := ManagerConfig{InstanceRepo: instances, DeployLogRepo: logs, Notifier: notif}
			if withTx {
				cfg.TxRunner = &mockTxRunner{instanceRepo: instances, logRepo: logs}
			}
			mgr := NewManager(cfg)
			t.Cleanup(mgr.Shutdown)
			exec := NewCleanupExecutor(mgr, newMockDefinitionRepo(), newMockChartConfigRepo(), instances)

			require.NoError(t, exec.DeleteInstance(jobCtx, inst))
			require.Error(t, jobCtx.Err(), "the job ended during the delete")

			v, ok := instances.get("Delete")
			require.True(t, ok)
			assert.Equal(t, "caller", v, "Delete runs with the context of the policy run")
			del := instances.callOf("Delete")
			assert.NoError(t, del.err, "the end of the job does not cancel the delete")
			assert.True(t, del.hasDeadline, "the delete runs with a deadline")
			followers := notif.followersCall()
			require.NotNil(t, followers, "the followers are read before the delete")
			assert.Equal(t, "caller", followers.value, "FollowerIDs runs with the context of the policy run")
			assert.NoError(t, followers.err, "the end of the job does not cancel the follower read")
			assert.True(t, followers.hasDeadline, "the follower read runs with a deadline")
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

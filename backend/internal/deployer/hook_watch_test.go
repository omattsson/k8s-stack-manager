package deployer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/hooks"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// preHookPoll is the watcher poll interval of the pre-hook tests.
const preHookPoll = 10 * time.Millisecond

// preHookTestTimeout is the subscriber timeout of the pre-hook tests. It is
// much longer than waitForLogDone, so only a watcher cancel ends a waiting
// subscriber in time.
const preHookTestTimeout = 60

// hookWaitLimit is how long a waiting subscriber waits for the cancel of its
// request before it answers allowed.
const hookWaitLimit = 10 * time.Second

// takeInstance simulates another operation that takes the instance: it sets
// status and, when action is not empty, adds a newer running deployment log
// of that action.
func takeInstance(t *testing.T, instanceRepo *mockInstanceRepo, logRepo *mockDeployLogRepo, instanceID, status, action string) {
	t.Helper()
	cur, err := instanceRepo.FindByID(instanceID)
	if err != nil {
		t.Errorf("find instance: %v", err)
		return
	}
	cur.Status = status
	if err := instanceRepo.Update(cur); err != nil {
		t.Errorf("update instance: %v", err)
		return
	}
	if action == "" {
		return
	}
	if err := logRepo.Create(context.Background(), &models.DeploymentLog{
		ID: "other-op-" + instanceID, StackInstanceID: instanceID, Action: action,
		Status: models.DeployLogRunning, StartedAt: time.Now().UTC().Add(time.Second),
	}); err != nil {
		t.Errorf("create log: %v", err)
	}
}

// preHookCase is one case of the pre-deploy and pre-rollback watcher tests.
type preHookCase struct {
	name string
	fp   hooks.FailurePolicy
	// takeStatus and takeAction describe the other operation that takes the
	// instance while the subscriber waits. Empty takeStatus: no other
	// operation.
	takeStatus string
	takeAction string
	// deny makes the subscriber deny at once.
	deny bool
	// dbBlip makes instance reads fail for a while during the hook.
	dbBlip bool
	// wantCancelled: the watcher cancels the hook call.
	wantCancelled bool
	// wantStatus is the instance status at the end.
	wantStatus string
	// wantLogStatus is the status of the deploy or rollback log.
	wantLogStatus string
	// wantOutput and wantNotOutput are parts of the log output.
	wantOutput    []string
	wantNotOutput []string
	// wantHelm: Helm installs the chart.
	wantHelm bool
}

// gateHandler returns the subscriber handler of a preHookCase. cancelled is
// closed when the request context of a waiting subscriber ends. elapsed is
// the time from the change of the instance to that cancel.
func gateHandler(t *testing.T, tt preHookCase, instanceRepo *mockInstanceRepo, logRepo *mockDeployLogRepo, instanceID string, cancelled chan struct{}, elapsed *atomic.Int64) func(http.ResponseWriter, *http.Request, hooks.EventEnvelope) {
	return func(w http.ResponseWriter, r *http.Request, _ hooks.EventEnvelope) {
		switch {
		case tt.deny:
			_ = json.NewEncoder(w).Encode(hooks.HookResponse{Allowed: false, Message: "builds are not ready"})
			return
		case tt.dbBlip:
			instanceRepo.mu.Lock()
			instanceRepo.err = errors.New("database unavailable")
			instanceRepo.mu.Unlock()
			time.Sleep(10 * preHookPoll) // several watcher polls fail
			instanceRepo.mu.Lock()
			instanceRepo.err = nil
			instanceRepo.mu.Unlock()
		case tt.takeStatus != "":
			takeInstance(t, instanceRepo, logRepo, instanceID, tt.takeStatus, tt.takeAction)
			changedAt := time.Now()
			select {
			case <-r.Context().Done():
				elapsed.Store(int64(time.Since(changedAt)))
				close(cancelled)
				return
			case <-time.After(hookWaitLimit):
			}
		default:
			time.Sleep(10 * preHookPoll) // several watcher polls see no change
		}
		_ = json.NewEncoder(w).Encode(hooks.HookResponse{Allowed: true})
	}
}

// assertWatcherCancel checks that the watcher cancelled the subscriber call
// soon after the change of the instance.
func assertWatcherCancel(t *testing.T, cancelled chan struct{}, elapsed *atomic.Int64) {
	t.Helper()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher must cancel the hook call")
	}
	// The poll interval is 10ms. The bound is wide for slow CI runners.
	assert.Less(t, time.Duration(elapsed.Load()), 2*time.Second)
}

func TestManager_Deploy_PreDeployHookWatch(t *testing.T) {
	t.Parallel()

	const cancelStopping = "WARNING: deploy cancelled: another operation changed the instance (status stopping) during the pre-deploy hook"
	tests := []preHookCase{
		{
			name: "stop during the hook cancels the call", fp: hooks.FailurePolicyFail,
			takeStatus: models.StackStatusStopping, takeAction: models.DeployActionStop,
			wantCancelled: true, wantStatus: models.StackStatusStopping, wantLogStatus: models.DeployLogError,
			wantOutput: []string{cancelStopping}, wantNotOutput: []string{"ERROR:", "denied"},
		},
		{
			name: "stop during the hook cancels the call (failure_policy ignore)", fp: hooks.FailurePolicyIgnore,
			takeStatus: models.StackStatusStopping, takeAction: models.DeployActionStop,
			wantCancelled: true, wantStatus: models.StackStatusStopping, wantLogStatus: models.DeployLogError,
			wantOutput: []string{cancelStopping}, wantNotOutput: []string{"ERROR:"},
		},
		{
			name: "status change alone cancels the call", fp: hooks.FailurePolicyFail,
			takeStatus:    models.StackStatusStopped,
			wantCancelled: true, wantStatus: models.StackStatusStopped, wantLogStatus: models.DeployLogError,
			wantOutput:    []string{"WARNING: deploy cancelled: another operation changed the instance (status stopped) during the pre-deploy hook"},
			wantNotOutput: []string{"ERROR:"},
		},
		{
			name: "new deploy during the hook cancels the call", fp: hooks.FailurePolicyFail,
			takeStatus: models.StackStatusDeploying, takeAction: models.DeployActionDeploy,
			wantCancelled: true, wantStatus: models.StackStatusDeploying, wantLogStatus: models.DeployLogError,
			wantOutput:    []string{"WARNING: deploy cancelled: another operation changed the instance (status deploying) during the pre-deploy hook"},
			wantNotOutput: []string{"ERROR:"},
		},
		{
			name: "denied hook keeps the denied path", fp: hooks.FailurePolicyFail,
			deny:       true,
			wantStatus: models.StackStatusError, wantLogStatus: models.DeployLogError,
			wantOutput:    []string{`ERROR: pre-deploy hook "gate" denied the deployment: builds are not ready`},
			wantNotOutput: []string{"deploy cancelled"},
		},
		{
			name: "no change during the hook: the deploy goes on", fp: hooks.FailurePolicyFail,
			wantStatus: models.StackStatusRunning, wantLogStatus: models.DeployLogSuccess,
			wantNotOutput: []string{"deploy cancelled", "ERROR:"},
			wantHelm:      true,
		},
		{
			name: "instance read errors during the hook do not cancel", fp: hooks.FailurePolicyFail,
			dbBlip:     true,
			wantStatus: models.StackStatusRunning, wantLogStatus: models.DeployLogSuccess,
			wantNotOutput: []string{"deploy cancelled", "ERROR:"},
			wantHelm:      true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := newSubscriberServer(t)
			instanceRepo := newMockInstanceRepo()
			logRepo := newMockDeployLogRepo()
			inst := seedInstance(t, instanceRepo, "inst-pre-deploy-watch", "pre-deploy-watch", "owner-1")
			cancelled := make(chan struct{})
			var elapsed atomic.Int64
			srv.handlers["gate"] = gateHandler(t, tt, instanceRepo, logRepo, inst.ID, cancelled, &elapsed)
			helmExec := &mockHelmExecutor{}
			mgr := NewManager(ManagerConfig{
				Registry:              &mockClusterResolver{helm: helmExec},
				InstanceRepo:          instanceRepo,
				DeployLogRepo:         logRepo,
				TxRunner:              &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
				Hub:                   &mockBroadcaster{},
				MaxConcurrent:         1,
				StabilizePollInterval: preHookPoll,
				Hooks: srv.dispatcher(t, hooks.Subscription{
					Name: "gate", Events: []string{hooks.EventPreDeploy}, FailurePolicy: tt.fp, TimeoutSeconds: preHookTestTimeout,
				}),
			})

			deployLog := waitForLogDone(t, logRepo, deployOneChart(t, mgr, inst))

			if tt.wantCancelled {
				assertWatcherCancel(t, cancelled, &elapsed)
			}
			assert.Equal(t, tt.wantLogStatus, deployLog.Status)
			for _, want := range tt.wantOutput {
				assert.Contains(t, deployLog.Output, want)
			}
			for _, notWant := range tt.wantNotOutput {
				assert.NotContains(t, deployLog.Output, notWant)
			}
			stored, err := instanceRepo.FindByID(inst.ID)
			require.NoError(t, err)
			assert.Equal(t, tt.wantStatus, stored.Status)
			helmExec.mu.Lock()
			installs := len(helmExec.installCalls)
			helmExec.mu.Unlock()
			if tt.wantHelm {
				assert.Equal(t, 1, installs)
			} else {
				assert.Zero(t, installs, "Helm must not run")
			}
		})
	}
}

func TestRollback_PreRollbackHookWatch(t *testing.T) {
	t.Parallel()

	const cancelStopping = "WARNING: rollback cancelled: another operation changed the instance (status stopping) during the pre-rollback hook"
	tests := []preHookCase{
		{
			name: "stop during the hook cancels the call", fp: hooks.FailurePolicyFail,
			takeStatus: models.StackStatusStopping, takeAction: models.DeployActionStop,
			wantCancelled: true, wantStatus: models.StackStatusStopping, wantLogStatus: models.DeployLogError,
			wantOutput: []string{cancelStopping}, wantNotOutput: []string{"ERROR:", "rejected"},
		},
		{
			name: "stop during the hook cancels the call (failure_policy ignore)", fp: hooks.FailurePolicyIgnore,
			takeStatus: models.StackStatusStopping, takeAction: models.DeployActionStop,
			wantCancelled: true, wantStatus: models.StackStatusStopping, wantLogStatus: models.DeployLogError,
			wantOutput: []string{cancelStopping}, wantNotOutput: []string{"ERROR:"},
		},
		{
			name: "new deploy during the hook cancels the call", fp: hooks.FailurePolicyFail,
			takeStatus: models.StackStatusDeploying, takeAction: models.DeployActionDeploy,
			wantCancelled: true, wantStatus: models.StackStatusDeploying, wantLogStatus: models.DeployLogError,
			wantOutput:    []string{"WARNING: rollback cancelled: another operation changed the instance (status deploying) during the pre-rollback hook"},
			wantNotOutput: []string{"ERROR:"},
		},
		{
			name: "denied hook keeps the rejected path", fp: hooks.FailurePolicyFail,
			deny: true,
			// finalizeRollbackRejected restores the status from before the rollback.
			wantStatus: models.StackStatusRunning, wantLogStatus: models.DeployLogError,
			wantOutput:    []string{`ERROR: pre-rollback hook "gate" denied the rollback: builds are not ready`},
			wantNotOutput: []string{"rollback cancelled"},
		},
		{
			name: "no change during the hook: the rollback goes on", fp: hooks.FailurePolicyFail,
			wantStatus: models.StackStatusRunning, wantLogStatus: models.DeployLogSuccess,
			wantNotOutput: []string{"rollback cancelled", "ERROR:"},
			wantHelm:      true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := newSubscriberServer(t)
			instanceRepo := newMockInstanceRepo()
			logRepo := newMockDeployLogRepo()
			inst := &models.StackInstance{ID: "inst-pre-rollback-watch", Name: "pre-rollback-watch", Namespace: "stack-pre-rollback-watch", OwnerID: "owner-1", Branch: "main", Status: models.StackStatusRunning}
			require.NoError(t, instanceRepo.Create(inst))
			cancelled := make(chan struct{})
			var elapsed atomic.Int64
			srv.handlers["gate"] = gateHandler(t, tt, instanceRepo, logRepo, inst.ID, cancelled, &elapsed)
			var helmMu sync.Mutex
			helmCalls := 0
			countHelm := func() {
				helmMu.Lock()
				helmCalls++
				helmMu.Unlock()
			}
			helmExec := &mockHelmExecutor{
				installFunc: func(_ context.Context, req InstallRequest) (string, error) {
					countHelm()
					return "installed " + req.ReleaseName, nil
				},
				rollbackFunc: func(_ context.Context, releaseName, _ string, _ int) (string, error) {
					countHelm()
					return "rolled back " + releaseName, nil
				},
			}
			mgr := NewManager(ManagerConfig{
				Registry:              &mockClusterResolver{helm: helmExec},
				InstanceRepo:          instanceRepo,
				DeployLogRepo:         logRepo,
				TxRunner:              &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
				Hub:                   &mockBroadcaster{},
				MaxConcurrent:         1,
				StabilizePollInterval: preHookPoll,
				Hooks: srv.dispatcher(t, hooks.Subscription{
					Name: "gate", Events: []string{hooks.EventPreRollback}, FailurePolicy: tt.fp, TimeoutSeconds: preHookTestTimeout,
				}),
			})
			logID, err := mgr.Rollback(context.Background(), RollbackRequest{
				Instance:     inst,
				Charts:       []ChartDeployInfo{{ChartConfig: models.ChartConfig{ChartName: "web"}}},
				TargetLogID:  "dep-a",
				TargetValues: map[string]string{"web": "a: 1\n"},
			})
			require.NoError(t, err)

			rbLog := waitForLogDone(t, logRepo, logID)

			if tt.wantCancelled {
				assertWatcherCancel(t, cancelled, &elapsed)
			}
			assert.Equal(t, tt.wantLogStatus, rbLog.Status)
			for _, want := range tt.wantOutput {
				assert.Contains(t, rbLog.Output, want)
			}
			for _, notWant := range tt.wantNotOutput {
				assert.NotContains(t, rbLog.Output, notWant)
			}
			// finalizeRollbackRejected writes the status after the log.
			require.Eventually(t, func() bool {
				got, err := instanceRepo.FindByID(inst.ID)
				return err == nil && got.Status == tt.wantStatus
			}, 5*time.Second, 10*time.Millisecond, "instance status must be %s", tt.wantStatus)
			helmMu.Lock()
			calls := helmCalls
			helmMu.Unlock()
			if tt.wantHelm {
				assert.Positive(t, calls)
			} else {
				assert.Zero(t, calls, "Helm must not run")
			}
		})
	}
}

// TestManager_Deploy_PreDeployHookShutdown checks that a shutdown during the
// pre-deploy hook keeps its behavior: the watcher does not report it as
// another operation, and the deploy ends with the hook failure.
func TestManager_Deploy_PreDeployHookShutdown(t *testing.T) {
	t.Parallel()
	srv := newSubscriberServer(t)
	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	inst := seedInstance(t, instanceRepo, "inst-pre-deploy-shutdown", "pre-deploy-shutdown", "owner-1")
	started := make(chan struct{})
	srv.handlers["gate"] = func(w http.ResponseWriter, r *http.Request, _ hooks.EventEnvelope) {
		close(started)
		select {
		case <-r.Context().Done():
			return
		case <-time.After(hookWaitLimit):
		}
		_ = json.NewEncoder(w).Encode(hooks.HookResponse{Allowed: true})
	}
	helmExec := &mockHelmExecutor{}
	mgr := NewManager(ManagerConfig{
		Registry:              &mockClusterResolver{helm: helmExec},
		InstanceRepo:          instanceRepo,
		DeployLogRepo:         logRepo,
		TxRunner:              &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:                   &mockBroadcaster{},
		MaxConcurrent:         1,
		StabilizePollInterval: preHookPoll,
		Hooks: srv.dispatcher(t, hooks.Subscription{
			Name: "gate", Events: []string{hooks.EventPreDeploy}, FailurePolicy: hooks.FailurePolicyFail, TimeoutSeconds: preHookTestTimeout,
		}),
	})
	logID := deployOneChart(t, mgr, inst)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("pre-deploy subscriber not called")
	}
	mgr.Shutdown()

	deployLog := waitForLogDone(t, logRepo, logID)
	assert.Equal(t, models.DeployLogError, deployLog.Status)
	assert.Contains(t, deployLog.Output, `ERROR: pre-deploy hook "gate" failed`)
	assert.NotContains(t, deployLog.Output, "deploy cancelled")
	stored, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusError, stored.Status)
	helmExec.mu.Lock()
	assert.Empty(t, helmExec.installCalls)
	helmExec.mu.Unlock()
}

func TestWatchOwnership(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		owned      bool
		wantLost   bool
		wantStatus string
	}{
		{name: "owned: stop reports no change", owned: true},
		{name: "lost: the watcher cancels the context", owned: false, wantLost: true, wantStatus: models.StackStatusStopping},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var checks atomic.Int32
			w := watchOwnership(ctx, cancel, time.Millisecond, func() (bool, string) {
				checks.Add(1)
				if tt.owned {
					return true, models.StackStatusDeploying
				}
				return false, models.StackStatusStopping
			})
			if tt.wantLost {
				select {
				case <-ctx.Done():
				case <-time.After(5 * time.Second):
					t.Fatal("the watcher must cancel the context")
				}
			} else {
				require.Eventually(t, func() bool { return checks.Load() >= 3 }, 5*time.Second, time.Millisecond)
				assert.NoError(t, ctx.Err(), "an owned instance must not cancel the context")
			}
			status, lost := w.stop()
			assert.Equal(t, tt.wantLost, lost)
			assert.Equal(t, tt.wantStatus, status)
			assert.Error(t, ctx.Err(), "stop cancels the context")
		})
	}
}

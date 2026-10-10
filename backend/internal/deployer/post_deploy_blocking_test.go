package deployer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/hooks"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// subCall is one call of a subscriber of the subscriberServer.
type subCall struct {
	sub      string
	envelope hooks.EventEnvelope
}

// subscriberServer serves several subscribers, one per URL path. handlers
// maps a subscriber name to its handler; other subscribers answer allowed.
type subscriberServer struct {
	mu       sync.Mutex
	calls    []subCall
	handlers map[string]func(w http.ResponseWriter, r *http.Request, env hooks.EventEnvelope)
	server   *httptest.Server
}

func newSubscriberServer(t *testing.T) *subscriberServer {
	t.Helper()
	s := &subscriberServer{handlers: map[string]func(http.ResponseWriter, *http.Request, hooks.EventEnvelope){}}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var env hooks.EventEnvelope
		_ = json.NewDecoder(r.Body).Decode(&env)
		name := strings.TrimPrefix(r.URL.Path, "/")
		s.mu.Lock()
		s.calls = append(s.calls, subCall{sub: name, envelope: env})
		h := s.handlers[name]
		s.mu.Unlock()
		if h != nil {
			h(w, r, env)
			return
		}
		_ = json.NewEncoder(w).Encode(hooks.HookResponse{Allowed: true})
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *subscriberServer) snapshot() []subCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]subCall(nil), s.calls...)
}

// callsFor returns "<subscriber>:<event>" of each call in order.
func (s *subscriberServer) callNames() []string {
	var out []string
	for _, c := range s.snapshot() {
		out = append(out, c.sub+":"+c.envelope.Event)
	}
	return out
}

func (s *subscriberServer) dispatcher(t *testing.T, subs ...hooks.Subscription) *hooks.Dispatcher {
	t.Helper()
	for i := range subs {
		subs[i].URL = s.server.URL + "/" + subs[i].Name
	}
	d, err := hooks.NewDispatcher(hooks.Config{Subscriptions: subs}, s.server.Client())
	require.NoError(t, err)
	return d
}

func notifTypes(calls []notifyCall) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.Type)
	}
	return out
}

func TestManager_Deploy_BlockingPostDeploy(t *testing.T) {
	t.Parallel()

	type observed struct {
		status        string
		notifications []string
		markerFuture  bool
	}

	tests := []struct {
		name          string
		failurePolicy hooks.FailurePolicy
		timeout       int
		// handler of the blocking subscriber "restore"; it can use the
		// instance repo and the observed state.
		handler            func(w http.ResponseWriter, r *http.Request, repo *mockInstanceRepo, instID string)
		expectStatus       string
		expectErrorMessage string
		expectNotifs       []string
		expectNotifyHook   bool // non-blocking post-deploy subscriber called
		expectOutput       []string
		expectWarningTitle string
		expectFinalizedMD  map[string]string
	}{
		{
			name:          "success keeps stabilizing until the subscriber returns",
			failurePolicy: hooks.FailurePolicyFail,
			handler: func(w http.ResponseWriter, _ *http.Request, _ *mockInstanceRepo, _ string) {
				_, _ = w.Write([]byte("LOG: restoring snapshot\nLOG: warming cache\n"))
				_ = json.NewEncoder(w).Encode(hooks.HookResponse{Allowed: true})
			},
			expectStatus:     models.StackStatusRunning,
			expectNotifs:     []string{"deployment.success"},
			expectNotifyHook: true,
			expectOutput:     []string{`Running post-deploy step "restore"`, "restoring snapshot", "warming cache", "Post-deploy steps finished"},
		},
		{
			name:          "failure_policy fail denial sets error with the hook reason",
			failurePolicy: hooks.FailurePolicyFail,
			handler: func(w http.ResponseWriter, _ *http.Request, _ *mockInstanceRepo, _ string) {
				_, _ = w.Write([]byte("LOG: restore started\n"))
				_ = json.NewEncoder(w).Encode(hooks.HookResponse{Allowed: false, Message: "snapshot not found"})
			},
			expectStatus:       models.StackStatusError,
			expectErrorMessage: `post-deploy hook "restore" denied the deployment: snapshot not found`,
			expectNotifs:       []string{"deployment.error"},
			expectOutput:       []string{"restore started", `ERROR: post-deploy hook "restore" denied the deployment: snapshot not found`},
		},
		{
			name:          "failure_policy fail timeout is a hook failure, not a deploy timeout",
			failurePolicy: hooks.FailurePolicyFail,
			timeout:       1,
			handler: func(_ http.ResponseWriter, r *http.Request, _ *mockInstanceRepo, _ string) {
				select {
				case <-r.Context().Done():
				case <-time.After(5 * time.Second):
				}
			},
			expectStatus:       models.StackStatusError,
			expectErrorMessage: `post-deploy hook "restore" failed (unreachable or timed out)`,
			expectNotifs:       []string{"deployment.error"},
			expectOutput:       []string{`ERROR: post-deploy hook "restore" failed (unreachable or timed out)`},
		},
		{
			name:          "failure_policy ignore sets running and warns the owner",
			failurePolicy: hooks.FailurePolicyIgnore,
			handler: func(w http.ResponseWriter, _ *http.Request, _ *mockInstanceRepo, _ string) {
				http.Error(w, "boom", http.StatusInternalServerError)
			},
			expectStatus:       models.StackStatusRunning,
			expectNotifs:       []string{"deployment.warning", "deployment.success"},
			expectNotifyHook:   true,
			expectOutput:       []string{`WARNING: post-deploy hook "restore" failed (unreachable or timed out)`},
			expectWarningTitle: "Post-deploy step restore failed",
			expectFinalizedMD:  map[string]string{"post_deploy_failed": "restore"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := newSubscriberServer(t)
			instanceRepo := newMockInstanceRepo()
			logRepo := newMockDeployLogRepo()
			notif := &mockNotifier{}
			inst := seedInstance(t, instanceRepo, "inst-blocking", "blocking-demo", "owner-1")

			var obsMu sync.Mutex
			var obs observed
			srv.handlers["restore"] = func(w http.ResponseWriter, r *http.Request, env hooks.EventEnvelope) {
				if env.Event == hooks.EventPostDeploy {
					cur, err := instanceRepo.FindByID(inst.ID)
					require.NoError(t, err)
					obsMu.Lock()
					obs = observed{
						status:        cur.Status,
						notifications: notifTypes(notif.getCalls()),
						markerFuture:  cur.PostDeployHookUntil != nil && cur.PostDeployHookUntil.After(time.Now()),
					}
					obsMu.Unlock()
				}
				tt.handler(w, r, instanceRepo, inst.ID)
			}
			timeout := tt.timeout
			if timeout == 0 {
				timeout = 10
			}
			mgr := NewManager(ManagerConfig{
				Registry:      &mockClusterResolver{helm: &mockHelmExecutor{}},
				InstanceRepo:  instanceRepo,
				DeployLogRepo: logRepo,
				TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
				Hub:           &mockBroadcaster{},
				MaxConcurrent: 1,
				Notifier:      notif,
				Hooks: srv.dispatcher(t,
					hooks.Subscription{Name: "restore", Events: []string{hooks.EventPostDeploy}, Blocking: true, FailurePolicy: tt.failurePolicy, TimeoutSeconds: timeout},
					hooks.Subscription{Name: "notify", Events: []string{hooks.EventPostDeploy, hooks.EventDeployFinalized, hooks.EventDeployTimeout}},
				),
			})

			logID, err := mgr.Deploy(context.Background(), DeployRequest{
				Instance:   inst,
				Definition: &models.StackDefinition{ID: "def-1", Name: "def"},
				Charts:     []ChartDeployInfo{{ChartConfig: models.ChartConfig{ChartName: "app"}}},
			})
			require.NoError(t, err)
			deployLog := waitForLogDone(t, logRepo, logID)
			notif.waitForCalls(t, len(tt.expectNotifs))
			require.Eventually(t, func() bool {
				for _, n := range srv.callNames() {
					if n == "notify:deploy-finalized" {
						return true
					}
				}
				return false
			}, 5*time.Second, 10*time.Millisecond)

			obsMu.Lock()
			assert.Equal(t, models.StackStatusStabilizing, obs.status, "status during the blocking subscriber")
			assert.Empty(t, obs.notifications, "no notification before the blocking subscriber returns")
			assert.True(t, obs.markerFuture, "the watcher marker is set during the wait")
			obsMu.Unlock()

			stored, err := instanceRepo.FindByID(inst.ID)
			require.NoError(t, err)
			assert.Equal(t, tt.expectStatus, stored.Status)
			assert.Equal(t, tt.expectErrorMessage, stored.ErrorMessage)
			assert.Nil(t, stored.PostDeployHookUntil, "the marker is cleared at the end")
			if tt.expectErrorMessage != "" {
				assert.Equal(t, tt.expectErrorMessage, deployLog.ErrorMessage)
			}
			assert.NotContains(t, deployLog.Output, srv.server.URL, "the deploy log must not contain the subscriber URL")
			assert.NotContains(t, stored.ErrorMessage, srv.server.URL)
			for _, want := range tt.expectOutput {
				assert.Contains(t, deployLog.Output, want)
			}

			calls := notif.getCalls()
			assert.Equal(t, tt.expectNotifs, notifTypes(calls))
			if tt.expectWarningTitle != "" {
				assert.Equal(t, tt.expectWarningTitle, calls[0].Title)
				assert.NotContains(t, calls[0].Message, srv.server.URL)
			}

			names := srv.callNames()
			assert.Equal(t, "restore:post-deploy", names[0], "the blocking subscriber runs first")
			assert.Contains(t, names, "notify:deploy-finalized")
			assert.NotContains(t, names, "notify:deploy-timeout")
			if tt.expectNotifyHook {
				assert.Contains(t, names, "notify:post-deploy")
			} else {
				assert.NotContains(t, names, "notify:post-deploy")
			}
			assert.NotContains(t, names, "restore:deploy-finalized")

			for _, c := range srv.snapshot() {
				require.NotNil(t, c.envelope.Trigger, "%s:%s has a trigger", c.sub, c.envelope.Event)
				assert.Equal(t, hooks.TriggerUser, c.envelope.Trigger.Type)
				if c.sub == "restore" {
					assert.Equal(t, "true", c.envelope.Metadata["blocking"])
					require.Len(t, c.envelope.Charts, 1)
					assert.Equal(t, "app", c.envelope.Charts[0].Name)
				}
				if c.envelope.Event == hooks.EventDeployFinalized {
					for k, v := range tt.expectFinalizedMD {
						assert.Equal(t, v, c.envelope.Metadata[k])
					}
				}
			}
		})
	}
}

func TestManager_Deploy_BlockingPostDeployCancelledByStop(t *testing.T) {
	t.Parallel()

	srv := newSubscriberServer(t)
	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	notif := &mockNotifier{}
	inst := seedInstance(t, instanceRepo, "inst-blocking-stop", "blocking-stop", "owner-1")

	cancelled := make(chan struct{})
	srv.handlers["restore"] = func(_ http.ResponseWriter, r *http.Request, _ hooks.EventEnvelope) {
		// A stop starts while the subscriber works.
		cur, err := instanceRepo.FindByID(inst.ID)
		require.NoError(t, err)
		cur.Status = models.StackStatusStopping
		require.NoError(t, instanceRepo.Update(cur))
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-time.After(5 * time.Second):
		}
	}
	mgr := NewManager(ManagerConfig{
		Registry:              &mockClusterResolver{helm: &mockHelmExecutor{}},
		InstanceRepo:          instanceRepo,
		DeployLogRepo:         logRepo,
		TxRunner:              &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:                   &mockBroadcaster{},
		MaxConcurrent:         1,
		Notifier:              notif,
		StabilizePollInterval: 20 * time.Millisecond,
		Hooks: srv.dispatcher(t,
			hooks.Subscription{Name: "restore", Events: []string{hooks.EventPostDeploy}, Blocking: true, FailurePolicy: hooks.FailurePolicyFail, TimeoutSeconds: 60},
		),
	})

	logID, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "def"},
		Charts:     []ChartDeployInfo{{ChartConfig: models.ChartConfig{ChartName: "app"}}},
	})
	require.NoError(t, err)
	deployLog := waitForLogDone(t, logRepo, logID)

	select {
	case <-cancelled:
	default:
		t.Fatal("the stop must cancel the wait for the blocking subscriber")
	}
	stored, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusStopping, stored.Status, "the status of the stop stays")
	assert.Equal(t, models.DeployLogError, deployLog.Status)
	assert.Contains(t, deployLog.Output, "WARNING: deploy cancelled: another operation changed the instance (status stopping) during the post-deploy hook")
	assert.NotContains(t, notifTypes(notif.getCalls()), "deployment.success")
}

func TestManager_Deploy_NonBlockingPostDeployUnchanged(t *testing.T) {
	t.Parallel()

	srv := newSubscriberServer(t)
	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	notif := &mockNotifier{}
	inst := seedInstance(t, instanceRepo, "inst-nonblocking", "nonblocking", "owner-1")

	var statusAtPostDeploy string
	var mu sync.Mutex
	srv.handlers["notify"] = func(w http.ResponseWriter, _ *http.Request, env hooks.EventEnvelope) {
		if env.Event == hooks.EventPostDeploy {
			cur, _ := instanceRepo.FindByID(inst.ID)
			mu.Lock()
			statusAtPostDeploy = cur.Status
			mu.Unlock()
		}
		_ = json.NewEncoder(w).Encode(hooks.HookResponse{Allowed: true})
	}
	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: &mockHelmExecutor{}},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 1,
		Notifier:      notif,
		Hooks: srv.dispatcher(t,
			hooks.Subscription{Name: "notify", Events: []string{hooks.EventPostDeploy, hooks.EventDeployFinalized}},
		),
	})
	logID, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "def"},
	})
	require.NoError(t, err)
	deployLog := waitForLogDone(t, logRepo, logID)
	require.Eventually(t, func() bool { return len(srv.callNames()) == 2 }, 5*time.Second, 10*time.Millisecond)

	assert.Equal(t, []string{"notify:post-deploy", "notify:deploy-finalized"}, srv.callNames())
	mu.Lock()
	assert.Equal(t, models.StackStatusRunning, statusAtPostDeploy, "post-deploy fires after the status update, as before")
	mu.Unlock()
	assert.NotContains(t, deployLog.Output, "post-deploy step")
}

func TestManager_HookTriggers(t *testing.T) {
	t.Parallel()

	policyTrigger := hooks.Trigger{Type: hooks.TriggerCleanupPolicy, ID: "pol-1", Name: "nightly"}
	tests := []struct {
		name   string
		ctx    context.Context
		expect hooks.Trigger
	}{
		{name: "no trigger in context defaults to user", ctx: context.Background(), expect: hooks.Trigger{Type: hooks.TriggerUser}},
		{name: "user trigger with id and name", ctx: hooks.WithTrigger(context.Background(), hooks.Trigger{Type: hooks.TriggerUser, ID: "u1", Name: "alice"}), expect: hooks.Trigger{Type: hooks.TriggerUser, ID: "u1", Name: "alice"}},
		{name: "cleanup policy trigger", ctx: hooks.WithTrigger(context.Background(), policyTrigger), expect: policyTrigger},
		{name: "ttl trigger", ctx: hooks.WithTrigger(context.Background(), hooks.Trigger{Type: hooks.TriggerTTL}), expect: hooks.Trigger{Type: hooks.TriggerTTL}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := newSubscriberServer(t)
			instanceRepo := newMockInstanceRepo()
			logRepo := newMockDeployLogRepo()
			inst := seedInstance(t, instanceRepo, "inst-trigger", "trigger", "owner-1")
			inst.Status = models.StackStatusRunning
			require.NoError(t, instanceRepo.Update(inst))
			mgr := NewManager(ManagerConfig{
				Registry:      &mockClusterResolver{helm: &mockHelmExecutor{}},
				InstanceRepo:  instanceRepo,
				DeployLogRepo: logRepo,
				TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
				Hub:           &mockBroadcaster{},
				MaxConcurrent: 1,
				Hooks:         srv.dispatcher(t, hooks.Subscription{Name: "rec", Events: []string{hooks.EventStopCompleted}}),
			})
			logID, err := mgr.StopWithCharts(tt.ctx, inst, []ChartDeployInfo{{ChartConfig: models.ChartConfig{ChartName: "app"}}})
			require.NoError(t, err)
			require.Eventually(t, func() bool { return len(srv.snapshot()) == 1 }, 5*time.Second, 10*time.Millisecond)
			env := srv.snapshot()[0].envelope
			require.NotNil(t, env.Trigger)
			assert.Equal(t, tt.expect, *env.Trigger)
			// The entry is removed when the operation ends.
			require.Eventually(t, func() bool { return mgr.triggerFor(logID) == nil }, 5*time.Second, 10*time.Millisecond)
		})
	}
}

func TestCleanupExecutor_DeleteInstance_FiresDeleteEvents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		ctx           context.Context
		expectTrigger *hooks.Trigger
		expectMessage string
	}{
		{
			name:          "policy delete names the policy",
			ctx:           hooks.WithTrigger(context.Background(), hooks.Trigger{Type: hooks.TriggerCleanupPolicy, ID: "pol-1", Name: "old-stacks"}),
			expectTrigger: &hooks.Trigger{Type: hooks.TriggerCleanupPolicy, ID: "pol-1", Name: "old-stacks"},
			expectMessage: `Stack del-demo has been deleted by cleanup policy "old-stacks"`,
		},
		{
			name:          "delete without trigger",
			ctx:           context.Background(),
			expectMessage: "Stack del-demo has been deleted",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := newSubscriberServer(t)
			instanceRepo := newMockInstanceRepo()
			notif := &mockNotifier{}
			inst := seedInstance(t, instanceRepo, "inst-del", "del-demo", "owner-9")
			inst.Status = models.StackStatusStopped
			require.NoError(t, instanceRepo.Update(inst))
			mgr := NewManager(ManagerConfig{
				Registry:      &mockClusterResolver{helm: &mockHelmExecutor{}},
				InstanceRepo:  instanceRepo,
				DeployLogRepo: newMockDeployLogRepo(),
				Notifier:      notif,
				Hooks: srv.dispatcher(t, hooks.Subscription{Name: "rec", Events: []string{
					hooks.EventPostInstanceDelete, hooks.EventDeleteCompleted,
				}}),
			})
			exec := NewCleanupExecutor(mgr, newMockDefinitionRepo(), newMockChartConfigRepo(), instanceRepo)

			require.NoError(t, exec.DeleteInstance(tt.ctx, inst))

			_, err := instanceRepo.FindByID(inst.ID)
			assert.Error(t, err, "the instance is deleted")
			assert.Equal(t, []string{"rec:post-instance-delete", "rec:delete-completed"}, srv.callNames())
			for _, c := range srv.snapshot() {
				require.NotNil(t, c.envelope.InstanceRef)
				assert.Equal(t, inst.ID, c.envelope.InstanceRef.ID)
				assert.Equal(t, tt.expectTrigger, c.envelope.Trigger)
			}
			calls := notif.getCalls()
			require.Len(t, calls, 1)
			assert.Equal(t, "instance.deleted", calls[0].Type)
			assert.Equal(t, "owner-9", calls[0].UserID)
			assert.Equal(t, tt.expectMessage, calls[0].Message)
		})
	}
}

func TestCleanupExecutor_DeleteInstance_RefusedFiresNothing(t *testing.T) {
	t.Parallel()
	srv := newSubscriberServer(t)
	instanceRepo := newMockInstanceRepo()
	notif := &mockNotifier{}
	inst := seedInstance(t, instanceRepo, "inst-del-running", "running", "owner-1")
	inst.Status = models.StackStatusRunning
	mgr := NewManager(ManagerConfig{
		InstanceRepo: instanceRepo,
		Notifier:     notif,
		Hooks:        srv.dispatcher(t, hooks.Subscription{Name: "rec", Events: []string{hooks.EventDeleteCompleted}}),
	})
	exec := NewCleanupExecutor(mgr, newMockDefinitionRepo(), newMockChartConfigRepo(), instanceRepo)

	require.Error(t, exec.DeleteInstance(context.Background(), inst))
	assert.Empty(t, srv.snapshot())
	assert.Empty(t, notif.getCalls())
}

func TestCleanupExecutor_DeleteInstance_PreDeleteHookStopsDelete(t *testing.T) {
	t.Parallel()
	srv := newSubscriberServer(t)
	srv.handlers["gate"] = func(w http.ResponseWriter, _ *http.Request, _ hooks.EventEnvelope) {
		_ = json.NewEncoder(w).Encode(hooks.HookResponse{Allowed: false, Message: "stack is pinned"})
	}
	instanceRepo := newMockInstanceRepo()
	notif := &mockNotifier{}
	inst := seedInstance(t, instanceRepo, "inst-pinned", "pinned", "owner-1")
	inst.Status = models.StackStatusStopped
	require.NoError(t, instanceRepo.Update(inst))
	mgr := NewManager(ManagerConfig{
		InstanceRepo: instanceRepo,
		Notifier:     notif,
		Hooks: srv.dispatcher(t,
			hooks.Subscription{Name: "gate", Events: []string{hooks.EventPreInstanceDelete}, FailurePolicy: hooks.FailurePolicyFail},
			hooks.Subscription{Name: "rec", Events: []string{hooks.EventPostInstanceDelete, hooks.EventDeleteCompleted}},
		),
	})
	exec := NewCleanupExecutor(mgr, newMockDefinitionRepo(), newMockChartConfigRepo(), instanceRepo)
	ctx := hooks.WithTrigger(context.Background(), hooks.Trigger{Type: hooks.TriggerCleanupPolicy, ID: "pol-1", Name: "old"})

	err := exec.DeleteInstance(ctx, inst)
	require.Error(t, err)
	assert.Equal(t, `pre-instance-delete hook "gate" denied the delete: stack is pinned`, err.Error())
	assert.NotContains(t, err.Error(), srv.server.URL)
	_, findErr := instanceRepo.FindByID(inst.ID)
	assert.NoError(t, findErr, "the instance stays")
	assert.Equal(t, []string{"gate:pre-instance-delete"}, srv.callNames())
	assert.Equal(t, "cleanup-policy", srv.snapshot()[0].envelope.Trigger.Type)
	assert.Empty(t, notif.getCalls())
}

func TestManager_DeleteAfterClean_FiresDeleteEvents(t *testing.T) {
	t.Parallel()
	srv := newSubscriberServer(t)
	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: &mockHelmExecutor{}},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner: &txRunnerWithBranchOverride{
			instanceRepo: instanceRepo,
			logRepo:      logRepo,
			boRepo:       noopBranchOverrideRepo{},
		},
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 1,
		Hooks: srv.dispatcher(t, hooks.Subscription{Name: "rec", Events: []string{
			hooks.EventCleanCompleted, hooks.EventPostInstanceDelete, hooks.EventDeleteCompleted,
		}}),
	})
	inst := seedInstance(t, instanceRepo, "inst-del-clean", "del-clean", "owner-1")
	inst.Status = models.StackStatusRunning
	require.NoError(t, instanceRepo.Update(inst))
	mgr.ScheduleDeleteAfterClean(inst.ID)

	ctx := hooks.WithTrigger(context.Background(), hooks.Trigger{Type: hooks.TriggerUser, ID: "u1", Name: "alice"})
	_, err := mgr.Clean(ctx, inst, []models.ChartConfig{{ChartName: "app"}})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(srv.snapshot()) == 3 }, 5*time.Second, 10*time.Millisecond)

	assert.Equal(t, []string{"rec:clean-completed", "rec:post-instance-delete", "rec:delete-completed"}, srv.callNames())
	calls := srv.snapshot()
	assert.Equal(t, "delete", calls[0].envelope.Metadata["operation"], "clean-completed marks the clean of a delete")
	for _, c := range calls {
		require.NotNil(t, c.envelope.Trigger)
		assert.Equal(t, "alice", c.envelope.Trigger.Name)
	}
}

func TestManager_CleanWithoutDelete_HasNoDeleteMark(t *testing.T) {
	t.Parallel()
	srv := newSubscriberServer(t)
	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: &mockHelmExecutor{}},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 1,
		Hooks:         srv.dispatcher(t, hooks.Subscription{Name: "rec", Events: []string{hooks.EventCleanCompleted, hooks.EventDeleteCompleted}}),
	})
	inst := seedInstance(t, instanceRepo, "inst-clean-only", "clean-only", "owner-1")
	inst.Status = models.StackStatusRunning
	require.NoError(t, instanceRepo.Update(inst))

	_, err := mgr.Clean(context.Background(), inst, []models.ChartConfig{{ChartName: "app"}})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(srv.snapshot()) == 1 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(50 * time.Millisecond)

	assert.Equal(t, []string{"rec:clean-completed"}, srv.callNames())
	assert.Empty(t, srv.snapshot()[0].envelope.Metadata["operation"])
}

// blockingManager returns a manager with one blocking post-deploy subscriber
// "restore" of the given policy on srv.
func blockingManager(t *testing.T, srv *subscriberServer, instanceRepo *mockInstanceRepo, logRepo *mockDeployLogRepo, notif *mockNotifier, fp hooks.FailurePolicy, maxConcurrent int) *Manager {
	t.Helper()
	return NewManager(ManagerConfig{
		Registry:              &mockClusterResolver{helm: &mockHelmExecutor{}},
		InstanceRepo:          instanceRepo,
		DeployLogRepo:         logRepo,
		TxRunner:              &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:                   &mockBroadcaster{},
		MaxConcurrent:         maxConcurrent,
		Notifier:              notif,
		StabilizePollInterval: 10 * time.Millisecond,
		Hooks: srv.dispatcher(t,
			hooks.Subscription{Name: "restore", Events: []string{hooks.EventPostDeploy}, Blocking: true, FailurePolicy: fp, TimeoutSeconds: 60},
		),
	})
}

func deployOneChart(t *testing.T, mgr *Manager, inst *models.StackInstance) string {
	t.Helper()
	logID, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "def"},
		Charts:     []ChartDeployInfo{{ChartConfig: models.ChartConfig{ChartName: "app"}}},
	})
	require.NoError(t, err)
	return logID
}

func TestManager_BlockingPostDeploy_TransientDBErrorKeepsWaiting(t *testing.T) {
	t.Parallel()
	srv := newSubscriberServer(t)
	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	inst := seedInstance(t, instanceRepo, "inst-db-blip", "db-blip", "owner-1")
	srv.handlers["restore"] = func(w http.ResponseWriter, _ *http.Request, _ hooks.EventEnvelope) {
		instanceRepo.mu.Lock()
		instanceRepo.err = errors.New("database unavailable")
		instanceRepo.mu.Unlock()
		time.Sleep(100 * time.Millisecond) // several watcher polls fail
		instanceRepo.mu.Lock()
		instanceRepo.err = nil
		instanceRepo.mu.Unlock()
		_ = json.NewEncoder(w).Encode(hooks.HookResponse{Allowed: true})
	}
	mgr := blockingManager(t, srv, instanceRepo, logRepo, &mockNotifier{}, hooks.FailurePolicyFail, 1)

	deployLog := waitForLogDone(t, logRepo, deployOneChart(t, mgr, inst))

	assert.Equal(t, models.DeployLogSuccess, deployLog.Status)
	assert.NotContains(t, deployLog.Output, "deploy cancelled")
	stored, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusRunning, stored.Status)
}

func TestManager_BlockingPostDeploy_ShutdownInterrupts(t *testing.T) {
	t.Parallel()
	for _, fp := range []hooks.FailurePolicy{hooks.FailurePolicyFail, hooks.FailurePolicyIgnore} {
		fp := fp
		t.Run(string(fp), func(t *testing.T) {
			t.Parallel()
			srv := newSubscriberServer(t)
			instanceRepo := newMockInstanceRepo()
			logRepo := newMockDeployLogRepo()
			notif := &mockNotifier{}
			inst := seedInstance(t, instanceRepo, "inst-shutdown", "shutdown", "owner-1")
			started := make(chan struct{})
			srv.handlers["restore"] = func(_ http.ResponseWriter, r *http.Request, _ hooks.EventEnvelope) {
				close(started)
				select {
				case <-r.Context().Done():
				case <-time.After(5 * time.Second):
				}
			}
			mgr := blockingManager(t, srv, instanceRepo, logRepo, notif, fp, 1)
			logID := deployOneChart(t, mgr, inst)

			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("blocking subscriber not called")
			}
			mgr.Shutdown()

			deployLog := waitForLogDone(t, logRepo, logID)
			stored, err := instanceRepo.FindByID(inst.ID)
			require.NoError(t, err)
			assert.Equal(t, models.StackStatusError, stored.Status)
			assert.Equal(t, "Interrupted by a server restart. Deploy again.", stored.ErrorMessage)
			assert.Equal(t, models.DeployLogError, deployLog.Status)
			assert.Contains(t, deployLog.Output, "ERROR: Interrupted by a server restart. Deploy again.")
			assert.NotContains(t, notifTypes(notif.getCalls()), "deployment.success")
			assert.NotContains(t, notifTypes(notif.getCalls()), "deployment.warning")
		})
	}
}

func TestManager_BlockingPostDeploy_FreesDeploySlot(t *testing.T) {
	t.Parallel()
	srv := newSubscriberServer(t)
	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	release := make(chan struct{})
	srv.handlers["restore"] = func(w http.ResponseWriter, _ *http.Request, env hooks.EventEnvelope) {
		if env.InstanceRef != nil && env.InstanceRef.Name == "slow" {
			<-release
		}
		_ = json.NewEncoder(w).Encode(hooks.HookResponse{Allowed: true})
	}
	// One concurrency slot: the second deploy can only run when the first
	// gives its slot free during the blocking wait.
	mgr := blockingManager(t, srv, instanceRepo, logRepo, &mockNotifier{}, hooks.FailurePolicyFail, 1)
	slow := seedInstance(t, instanceRepo, "inst-slow", "slow", "owner-1")
	fast := seedInstance(t, instanceRepo, "inst-fast", "fast", "owner-1")

	slowLog := deployOneChart(t, mgr, slow)
	require.Eventually(t, func() bool {
		cur, err := instanceRepo.FindByID(slow.ID)
		return err == nil && cur.Status == models.StackStatusStabilizing
	}, 5*time.Second, 10*time.Millisecond)

	fastLog := waitForLogDone(t, logRepo, deployOneChart(t, mgr, fast))
	assert.Equal(t, models.DeployLogSuccess, fastLog.Status, "the second deploy finished while the first waits")
	running, err := logRepo.FindByID(context.Background(), slowLog)
	require.NoError(t, err)
	assert.Equal(t, models.DeployLogRunning, running.Status)

	close(release)
	assert.Equal(t, models.DeployLogSuccess, waitForLogDone(t, logRepo, slowLog).Status)
}

func TestManager_BlockingPostDeploy_StopClearsMarker(t *testing.T) {
	t.Parallel()
	srv := newSubscriberServer(t)
	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	inst := seedInstance(t, instanceRepo, "inst-stop-marker", "stop-marker", "owner-1")
	started := make(chan struct{})
	srv.handlers["restore"] = func(_ http.ResponseWriter, r *http.Request, _ hooks.EventEnvelope) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}
	mgr := blockingManager(t, srv, instanceRepo, logRepo, &mockNotifier{}, hooks.FailurePolicyFail, 2)
	deployLogID := deployOneChart(t, mgr, inst)

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("blocking subscriber not called")
	}
	cur, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	require.NotNil(t, cur.PostDeployHookUntil, "marker set during the wait")

	stopLogID, err := mgr.StopWithCharts(context.Background(), cur, []ChartDeployInfo{{ChartConfig: models.ChartConfig{ChartName: "app"}}})
	require.NoError(t, err)
	assert.Equal(t, models.DeployLogSuccess, waitForLogDone(t, logRepo, stopLogID).Status)
	assert.Equal(t, models.DeployLogError, waitForLogDone(t, logRepo, deployLogID).Status)

	stored, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusStopped, stored.Status)
	assert.Nil(t, stored.PostDeployHookUntil, "the stop clears the marker")
}

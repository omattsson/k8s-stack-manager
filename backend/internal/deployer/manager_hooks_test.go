package deployer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"backend/internal/hooks"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordedHook captures one event delivery for assertions.
type recordedHook struct {
	event    string
	envelope hooks.EventEnvelope
}

// hookRecorder is an httptest server that captures every event posted to it.
// Tests inspect recorder.events after the deploy goroutine completes.
type hookRecorder struct {
	t       *testing.T
	mu      sync.Mutex
	events  []recordedHook
	deny    map[string]string // event -> message; if set, respond Allowed:false
	onEvent map[string]func() // event -> callback run before the response
	server  *httptest.Server
}

func newHookRecorder(t *testing.T) *hookRecorder {
	t.Helper()
	r := &hookRecorder{t: t, deny: map[string]string{}, onEvent: map[string]func(){}}
	r.server = httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.server.Close)
	return r
}

func (r *hookRecorder) serve(w http.ResponseWriter, req *http.Request) {
	var env hooks.EventEnvelope
	if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
		r.t.Errorf("recorder: decode envelope: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	r.events = append(r.events, recordedHook{event: env.Event, envelope: env})
	denyMsg, deny := r.deny[env.Event]
	callback := r.onEvent[env.Event]
	r.mu.Unlock()

	if callback != nil {
		callback()
	}
	if deny {
		_ = json.NewEncoder(w).Encode(hooks.HookResponse{Allowed: false, Message: denyMsg})
		return
	}
	_ = json.NewEncoder(w).Encode(hooks.HookResponse{Allowed: true})
}

func (r *hookRecorder) snapshot() []recordedHook {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedHook, len(r.events))
	copy(out, r.events)
	return out
}

func (r *hookRecorder) eventNames() []string {
	snap := r.snapshot()
	out := make([]string, 0, len(snap))
	for _, e := range snap {
		out = append(out, e.event)
	}
	return out
}

// dispatcherFor constructs a real Dispatcher subscribed to all events,
// pointed at the recorder's URL, with the given failure policy.
func (r *hookRecorder) dispatcherFor(t *testing.T, fp hooks.FailurePolicy) *hooks.Dispatcher {
	t.Helper()
	d, err := hooks.NewDispatcher(hooks.Config{Subscriptions: []hooks.Subscription{{
		Name: "recorder",
		Events: []string{
			hooks.EventPreDeploy,
			hooks.EventPostDeploy,
			hooks.EventDeployFinalized,
		},
		URL:           r.server.URL,
		FailurePolicy: fp,
	}}}, r.server.Client())
	require.NoError(t, err)
	return d
}

func TestManager_Deploy_FiresLifecycleHooks(t *testing.T) {
	t.Parallel()

	rec := newHookRecorder(t)
	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	inst := &models.StackInstance{
		ID:                "inst-hooks-1",
		StackDefinitionID: "def-1",
		Name:              "demo",
		Namespace:         "stack-demo-alice",
		OwnerID:           "user-1",
		Branch:            "main",
		Status:            models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
		Hooks:         rec.dispatcherFor(t, hooks.FailurePolicyIgnore),
	})

	logID, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "test-def"},
		Charts:     nil,
	})
	require.NoError(t, err)
	require.NotEmpty(t, logID)

	// Wait for the async deploy goroutine to finish.
	require.Eventually(t, func() bool {
		names := rec.eventNames()
		return len(names) >= 3
	}, 2*time.Second, 20*time.Millisecond, "expected pre-deploy, post-deploy, deploy-finalized")

	names := rec.eventNames()
	assert.Equal(t, []string{
		hooks.EventPreDeploy,
		hooks.EventPostDeploy,
		hooks.EventDeployFinalized,
	}, names, "events fire in lifecycle order")

	for _, evt := range rec.snapshot() {
		require.NotNil(t, evt.envelope.InstanceRef, "envelope must include instance ref for %s", evt.event)
		assert.Equal(t, inst.ID, evt.envelope.InstanceRef.ID)
		assert.Equal(t, inst.Namespace, evt.envelope.InstanceRef.Namespace)
		require.NotNil(t, evt.envelope.Deployment, "envelope must include deployment ref for %s", evt.event)
		assert.Equal(t, logID, evt.envelope.Deployment.ID)
	}
}

func TestManager_Deploy_PreDeployHookIncludesChartData(t *testing.T) {
	t.Parallel()

	rec := newHookRecorder(t)
	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	inst := &models.StackInstance{
		ID:                "inst-charts-1",
		StackDefinitionID: "def-1",
		Name:              "chart-test",
		Namespace:         "stack-chart-test",
		OwnerID:           "user-1",
		Branch:            "feature/foo",
		Status:            models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
		Hooks:         rec.dispatcherFor(t, hooks.FailurePolicyIgnore),
	})

	charts := []ChartDeployInfo{{
		ChartConfig: models.ChartConfig{
			ChartName:       "app-api",
			ChartVersion:    "1.0.0",
			SourceRepoURL:   "https://dev.azure.com/org/proj/_git/app-api",
			BuildPipelineID: "42",
		},
	}, {
		ChartConfig: models.ChartConfig{
			ChartName:    "redis",
			ChartVersion: "7.0.0",
		},
	}, {
		ChartConfig: models.ChartConfig{
			ChartName:       "app-web",
			ChartVersion:    "1.0.0",
			BuildPipelineID: "43",
		},
		Branch: "Feature/Web_Fix",
	}}

	logID, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "test-def"},
		Charts:     charts,
	})
	require.NoError(t, err)
	require.NotEmpty(t, logID)

	// Wait for the pre-deploy event to be recorded.
	require.Eventually(t, func() bool {
		return len(rec.eventNames()) >= 1
	}, 2*time.Second, 20*time.Millisecond)

	preDeployEvt := rec.snapshot()[0]
	assert.Equal(t, hooks.EventPreDeploy, preDeployEvt.event)
	require.Len(t, preDeployEvt.envelope.Charts, 3, "pre-deploy must include all charts")

	assert.Equal(t, "app-api", preDeployEvt.envelope.Charts[0].Name)
	assert.Equal(t, "42", preDeployEvt.envelope.Charts[0].BuildPipelineID)
	assert.Equal(t, "https://dev.azure.com/org/proj/_git/app-api", preDeployEvt.envelope.Charts[0].SourceRepoURL)

	assert.Equal(t, "feature/foo", preDeployEvt.envelope.Charts[0].Branch)
	assert.Equal(t, "feature-foo", preDeployEvt.envelope.Charts[0].ImageTag, "image_tag must match {{.ImageTag}}")

	assert.Equal(t, "redis", preDeployEvt.envelope.Charts[1].Name)
	assert.Empty(t, preDeployEvt.envelope.Charts[1].BuildPipelineID)

	// A per-chart branch override gives the chart its own branch and tag.
	assert.Equal(t, "app-web", preDeployEvt.envelope.Charts[2].Name)
	assert.Equal(t, "Feature/Web_Fix", preDeployEvt.envelope.Charts[2].Branch)
	assert.Equal(t, "feature-web-fix", preDeployEvt.envelope.Charts[2].ImageTag)
}

func TestManager_Deploy_PreHookAbortFinalizesAsError(t *testing.T) {
	t.Parallel()

	rec := newHookRecorder(t)
	rec.deny[hooks.EventPreDeploy] = "policy says no"

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	inst := &models.StackInstance{
		ID:                "inst-blocked-1",
		StackDefinitionID: "def-1",
		Name:              "blocked",
		Namespace:         "stack-blocked-bob",
		OwnerID:           "user-2",
		Branch:            "main",
		Status:            models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
		Hooks:         rec.dispatcherFor(t, hooks.FailurePolicyFail),
	})

	// Deploy returns a log ID immediately — the pre-deploy hook fires
	// asynchronously inside the deploy goroutine.
	logID, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "test-def"},
		Charts:     nil,
	})
	require.NoError(t, err)
	require.NotEmpty(t, logID)

	// Wait for the async goroutine to finalize.
	require.Eventually(t, func() bool {
		logs, _ := logRepo.ListByInstance(context.Background(), inst.ID)
		return len(logs) > 0 && logs[0].Status != models.DeployLogRunning
	}, 2*time.Second, 20*time.Millisecond, "deployment log should be finalized")

	// Instance must be set to error status by finalizeDeploy.
	stored, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusError, stored.Status, "instance must be error when pre-deploy hook denies")
	assert.Contains(t, stored.ErrorMessage, "pre-deploy hook")

	// Deployment log should be marked as error.
	logs, listErr := logRepo.ListByInstance(context.Background(), inst.ID)
	require.NoError(t, listErr)
	require.Len(t, logs, 1)
	assert.Equal(t, models.DeployLogError, logs[0].Status)
	assert.Contains(t, logs[0].ErrorMessage, "pre-deploy hook")
	// The subscriber's deny reason is visible (issue #442), with the hook name.
	assert.Equal(t, `pre-deploy hook "recorder" denied the deployment: policy says no`, logs[0].ErrorMessage)
	assert.Contains(t, stored.ErrorMessage, "policy says no")

	// Only the pre-deploy + deploy-finalized events fire (no post-deploy).
	require.Eventually(t, func() bool {
		return len(rec.eventNames()) >= 2
	}, 2*time.Second, 20*time.Millisecond)
	names := rec.eventNames()
	assert.Contains(t, names, hooks.EventPreDeploy)
	assert.Contains(t, names, hooks.EventDeployFinalized)
	assert.NotContains(t, names, hooks.EventPostDeploy)
}

func TestManager_Deploy_NoDispatcherIsNoOp(t *testing.T) {
	t.Parallel()
	// Regression test: when Hooks is nil, Deploy proceeds normally and no
	// state in the manager references hooks.
	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	inst := &models.StackInstance{
		ID:                "inst-nohooks-1",
		StackDefinitionID: "def-1",
		Name:              "no-hooks",
		Namespace:         "stack-nohooks-carol",
		OwnerID:           "user-3",
		Branch:            "main",
		Status:            models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
		// Hooks: omitted intentionally
	})

	logID, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "test-def"},
		Charts:     nil,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, logID)
}

// TestManager_Deploy_StatusChangedDuringPreDeployHookSkipsInstall covers a
// stop or clean during a long pre-deploy hook (for example a CI gate that
// waits for image builds): the deploy must not install the charts, and it must
// keep the status that the stop or clean set.
func TestManager_Deploy_StatusChangedDuringPreDeployHookSkipsInstall(t *testing.T) {
	t.Parallel()

	rec := newHookRecorder(t)
	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	inst := &models.StackInstance{
		ID:                "inst-stopped-during-gate",
		StackDefinitionID: "def-1",
		Name:              "gate-stop",
		Namespace:         "stack-gate-stop",
		OwnerID:           "user-1",
		Branch:            "feature/foo",
		Status:            models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	// While the pre-deploy hook runs, a stop moves the instance to stopped.
	rec.onEvent[hooks.EventPreDeploy] = func() {
		current, err := instanceRepo.FindByID(inst.ID)
		if err != nil {
			t.Errorf("find instance: %v", err)
			return
		}
		current.Status = models.StackStatusStopped
		if err := instanceRepo.Update(current); err != nil {
			t.Errorf("update instance: %v", err)
		}
	}

	helmExec := &mockHelmExecutor{}
	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: helmExec},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
		Hooks:         rec.dispatcherFor(t, hooks.FailurePolicyFail),
	})

	logID, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "test-def"},
		Charts: []ChartDeployInfo{{
			ChartConfig: models.ChartConfig{ChartName: "app", ChartVersion: "1.0.0"},
		}},
	})
	require.NoError(t, err)
	require.NotEmpty(t, logID)

	require.Eventually(t, func() bool {
		logs, _ := logRepo.ListByInstance(context.Background(), inst.ID)
		return len(logs) > 0 && logs[0].Status != models.DeployLogRunning
	}, 2*time.Second, 20*time.Millisecond, "deployment log should be finalized")

	helmExec.mu.Lock()
	installs := len(helmExec.installCalls)
	helmExec.mu.Unlock()
	assert.Zero(t, installs, "no chart may be installed after the status changed")

	stored, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusStopped, stored.Status, "the status set by the stop must stay")

	logs, err := logRepo.ListByInstance(context.Background(), inst.ID)
	require.NoError(t, err)
	require.Len(t, logs, 1)
	assert.Equal(t, models.DeployLogError, logs[0].Status)
	// sanitizeDeployError makes the stored message generic; the specific
	// "deploy cancelled" text goes to the live deploy log.
	assert.NotEmpty(t, logs[0].ErrorMessage)
}

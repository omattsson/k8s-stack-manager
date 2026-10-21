package deployer

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/hooks"
	"backend/internal/k8s"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

// TestManager_FinalWriteCtx checks that the final-write context outlives a
// shutdown (#656): Shutdown cancels shutdownCtx before it waits for the
// operations, so the final status write must not end with it. The context
// still has a deadline.
func TestManager_FinalWriteCtx(t *testing.T) {
	t.Parallel()
	mgr := NewManager(ManagerConfig{})
	mgr.Shutdown()
	require.Error(t, mgr.shutdownCtx.Err())

	ctx, cancel := mgr.finalWriteCtx()
	defer cancel()

	assert.NoError(t, ctx.Err(), "a shutdown does not cancel the final write")
	deadline, ok := ctx.Deadline()
	require.True(t, ok, "the final write has a deadline")
	assert.WithinDuration(t, time.Now().Add(finalWriteTimeout), deadline, 5*time.Second)
	cancel()
	assert.ErrorIs(t, ctx.Err(), context.Canceled)
}

// blockUntilDone returns a function that signals started once and then
// blocks until ctx ends (a Helm call that a shutdown stops).
func blockUntilDone(started chan<- struct{}) func(ctx context.Context) error {
	var once sync.Once
	return func(ctx context.Context) error {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return ctx.Err()
	}
}

// TestManager_ShutdownWritesFinalStatus starts each operation, shuts the
// manager down while Helm runs, and checks that the final status is written
// (#656). The repository mocks fail on a done context, so a final write with
// the cancelled shutdown context would leave the log running and the
// instance in its in-progress status.
func TestManager_ShutdownWritesFinalStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		noTx       bool
		start      func(ctx context.Context, mgr *Manager, inst *models.StackInstance, charts []ChartDeployInfo) (string, error)
		wantStatus string // "" means the instance row is deleted
		wantLog    string
	}{
		{name: "deploy", wantStatus: models.StackStatusError, wantLog: models.DeployLogError,
			start: func(ctx context.Context, mgr *Manager, inst *models.StackInstance, charts []ChartDeployInfo) (string, error) {
				return mgr.Deploy(ctx, DeployRequest{Instance: inst, Definition: &models.StackDefinition{ID: "def-1"}, Charts: charts})
			}},
		{name: "deploy without a transaction runner", noTx: true, wantStatus: models.StackStatusError, wantLog: models.DeployLogError,
			start: func(ctx context.Context, mgr *Manager, inst *models.StackInstance, charts []ChartDeployInfo) (string, error) {
				return mgr.Deploy(ctx, DeployRequest{Instance: inst, Definition: &models.StackDefinition{ID: "def-1"}, Charts: charts})
			}},
		{name: "stop", wantStatus: models.StackStatusError, wantLog: models.DeployLogError,
			start: func(ctx context.Context, mgr *Manager, inst *models.StackInstance, charts []ChartDeployInfo) (string, error) {
				return mgr.StopWithCharts(ctx, inst, charts)
			}},
		// A clean goes on after a failed uninstall; without a k8s client it
		// succeeds and the instance returns to draft.
		{name: "clean", wantStatus: models.StackStatusDraft, wantLog: models.DeployLogSuccess,
			start: func(ctx context.Context, mgr *Manager, inst *models.StackInstance, _ []ChartDeployInfo) (string, error) {
				return mgr.Clean(ctx, inst, []models.ChartConfig{{ChartName: "app"}})
			}},
		{name: "clean for delete", wantStatus: "", wantLog: models.DeployLogSuccess,
			start: func(ctx context.Context, mgr *Manager, inst *models.StackInstance, _ []ChartDeployInfo) (string, error) {
				return mgr.CleanForDelete(ctx, inst, []models.ChartConfig{{ChartName: "app"}})
			}},
		{name: "rollback", wantStatus: models.StackStatusError, wantLog: models.DeployLogError,
			start: func(ctx context.Context, mgr *Manager, inst *models.StackInstance, charts []ChartDeployInfo) (string, error) {
				return mgr.Rollback(ctx, RollbackRequest{Instance: inst, Charts: charts})
			}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			instanceRepo := newMockInstanceRepo()
			logRepo := newMockDeployLogRepo()
			started := make(chan struct{})
			block := blockUntilDone(started)
			helmMock := &mockHelmExecutor{
				installFunc: func(ctx context.Context, _ InstallRequest) (string, error) { return "", block(ctx) },
				uninstallFunc: func(ctx context.Context, _ UninstallRequest) (string, error) {
					return "", block(ctx)
				},
				historyFunc: func(ctx context.Context, _, _ string, _ int) ([]ReleaseRevision, error) {
					return nil, block(ctx)
				},
			}
			cfg := ManagerConfig{
				Registry:      &mockClusterResolver{helm: helmMock, noK8sClient: true},
				InstanceRepo:  instanceRepo,
				DeployLogRepo: logRepo,
			}
			if !tt.noTx {
				cfg.TxRunner = &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo}
			}
			mgr := NewManager(cfg)
			inst := seedInstance(t, instanceRepo, "inst-"+tt.name, "shutdown", "owner-1")
			inst.Status = models.StackStatusRunning
			require.NoError(t, instanceRepo.Update(context.Background(), inst))
			charts := []ChartDeployInfo{{ChartConfig: models.ChartConfig{ChartName: "app"}}}

			logID, err := tt.start(context.Background(), mgr, inst, charts)
			require.NoError(t, err)
			select {
			case <-started:
			case <-time.After(10 * time.Second):
				t.Fatal("helm was not called")
			}
			mgr.Shutdown()

			gotLog, err := logRepo.FindByID(context.Background(), logID)
			require.NoError(t, err)
			assert.Equal(t, tt.wantLog, gotLog.Status, "the log is finalized after the shutdown")
			stored, err := instanceRepo.FindByID(context.Background(), inst.ID)
			if tt.wantStatus == "" {
				assert.Error(t, err, "the instance is deleted after the clean")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantStatus, stored.Status, "the instance gets its final status after the shutdown")
		})
	}
}

// TestManager_ShutdownDuringPreRollbackHook shuts the manager down while a
// pre-rollback subscriber (failure_policy ignore) runs. The ownership check
// after the hook must not count the shutdown as a lost instance: the cancel
// path keeps the instance status, and the instance would stay deploying.
func TestManager_ShutdownDuringPreRollbackHook(t *testing.T) {
	t.Parallel()
	hookStarted := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		// The server sees a client cancel only after the body is read.
		_, _ = io.Copy(io.Discard, r.Body)
		once.Do(func() { close(hookStarted) })
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	d, err := hooks.NewDispatcher(hooks.Config{Subscriptions: []hooks.Subscription{{
		Name: "gate", URL: srv.URL, Events: []string{hooks.EventPreRollback}, FailurePolicy: hooks.FailurePolicyIgnore,
	}}}, srv.Client())
	require.NoError(t, err)

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	helmMock := &mockHelmExecutor{
		historyFunc: func(ctx context.Context, _, _ string, _ int) ([]ReleaseRevision, error) {
			return nil, ctx.Err()
		},
	}
	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: helmMock, noK8sClient: true},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hooks:         d,
	})
	inst := seedInstance(t, instanceRepo, "inst-hook", "hook", "owner-1")
	inst.Status = models.StackStatusRunning
	require.NoError(t, instanceRepo.Update(context.Background(), inst))

	logID, err := mgr.Rollback(context.Background(), RollbackRequest{
		Instance: inst, Charts: []ChartDeployInfo{{ChartConfig: models.ChartConfig{ChartName: "app"}}},
	})
	require.NoError(t, err)
	select {
	case <-hookStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("the pre-rollback hook was not called")
	}
	mgr.Shutdown()

	gotLog, err := logRepo.FindByID(context.Background(), logID)
	require.NoError(t, err)
	assert.Equal(t, models.DeployLogError, gotLog.Status)
	assert.NotContains(t, gotLog.ErrorMessage, "another operation", "a shutdown is not a lost instance")
	stored, err := instanceRepo.FindByID(context.Background(), inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusError, stored.Status, "the instance does not stay deploying")
}

// TestAwaitReadiness_ShutdownInterrupts checks that a shutdown before or
// during the readiness wait ends the deploy as interrupted, not as a success
// with a readiness warning, unless one last status read finds the pods
// healthy (#656). A pod event does not turn the interrupt into a timeout.
func TestAwaitReadiness_ShutdownInterrupts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// inHelm shuts down while Helm runs; atRead at the first instance
		// read of the wait; otherwise during the wait.
		inHelm bool
		atRead bool
		// failedMount adds a Warning event whose text contains "timed out".
		failedMount bool
		// healthyAtShutdown removes the pending pod just before the
		// shutdown; the wait itself never polls.
		healthyAtShutdown bool
	}{
		{name: "shutdown during the readiness wait"},
		{name: "shutdown during the Helm install", inHelm: true},
		{name: "shutdown at the first read of the wait", atRead: true},
		{name: "shutdown with a FailedMount timeout event", failedMount: true},
		{name: "pods healthy at the shutdown", healthyAtShutdown: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			instanceRepo := newMockInstanceRepo()
			logRepo := newMockDeployLogRepo()
			notif := &mockNotifier{}
			inst := seedInstance(t, instanceRepo, "inst-ready-shutdown", "ready-shutdown", "owner-1")
			// A pending pod: the namespace is not healthy.
			objects := []runtime.Object{
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: inst.Namespace}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}},
				&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "stuck", Namespace: inst.Namespace}, Status: corev1.PodStatus{Phase: corev1.PodPending}},
			}
			if tt.failedMount {
				objects = append(objects, &corev1.Event{
					ObjectMeta: metav1.ObjectMeta{Name: "mount", Namespace: inst.Namespace},
					Type:       corev1.EventTypeWarning, Reason: "FailedMount", LastTimestamp: metav1.Now(),
					Message:        "Unable to attach or mount volumes: timed out waiting for the condition",
					InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "stuck", Namespace: inst.Namespace},
				})
			}
			cs := fake.NewSimpleClientset(objects...)
			inHelm := make(chan struct{})
			release := make(chan struct{})
			readRepo := &shutdownAtReadRepo{mockInstanceRepo: instanceRepo}
			hub := &mockBroadcaster{}
			helmMock := &mockHelmExecutor{installFunc: func(_ context.Context, req InstallRequest) (string, error) {
				if tt.inHelm {
					close(inHelm)
					<-release
				}
				readRepo.armed.Store(tt.atRead)
				return "installed " + req.ReleaseName, nil
			}}
			poll := 10 * time.Millisecond
			if tt.healthyAtShutdown {
				poll = time.Hour
			}
			mgr := NewManager(ManagerConfig{
				Registry:              &mockClusterResolver{helm: helmMock, k8sClient: k8s.NewClientFromInterface(cs)},
				InstanceRepo:          readRepo,
				DeployLogRepo:         logRepo,
				TxRunner:              &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
				Hub:                   hub,
				Notifier:              notif,
				StabilizeTimeout:      time.Minute,
				StabilizePollInterval: poll,
				PodEventPollInterval:  10 * time.Millisecond,
			})
			readRepo.cancel = mgr.shutdownCancel
			t.Cleanup(mgr.Shutdown)
			logID, err := mgr.Deploy(context.Background(), DeployRequest{
				Instance:   inst,
				Definition: &models.StackDefinition{ID: "def-1", Name: "def"},
				Charts:     []ChartDeployInfo{{ChartConfig: models.ChartConfig{ChartName: "app"}}},
			})
			require.NoError(t, err)

			switch {
			case tt.inHelm:
				<-inHelm
				done := make(chan struct{})
				go func() { mgr.Shutdown(); close(done) }()
				require.Eventually(t, func() bool { return mgr.shutdownCtx.Err() != nil }, 5*time.Second, time.Millisecond)
				close(release)
				<-done
			case tt.atRead:
			default:
				require.Eventually(t, func() bool {
					cur, err := instanceRepo.FindByID(context.Background(), inst.ID)
					return err == nil && cur.Status == models.StackStatusStabilizing
				}, 5*time.Second, 5*time.Millisecond)
				if tt.failedMount {
					require.Eventually(t, func() bool { return hubHas(hub, "FailedMount") }, 5*time.Second, 5*time.Millisecond)
				}
				if tt.healthyAtShutdown {
					require.NoError(t, cs.CoreV1().Pods(inst.Namespace).Delete(context.Background(), "stuck", metav1.DeleteOptions{}))
				}
				mgr.Shutdown()
			}

			deployLog := waitForLogDone(t, logRepo, logID)
			stored, err := instanceRepo.FindByID(context.Background(), inst.ID)
			require.NoError(t, err)
			types := notifTypes(notif.getCalls())
			if tt.healthyAtShutdown {
				assert.Equal(t, models.StackStatusRunning, stored.Status, stored.ErrorMessage)
				assert.Equal(t, models.DeployLogSuccess, deployLog.Status)
				assert.NotContains(t, deployLog.Output, ErrDeployInterrupted.Error())
				return
			}
			assert.Equal(t, models.StackStatusError, stored.Status)
			assert.Equal(t, ErrDeployInterrupted.Error(), stored.ErrorMessage)
			assert.Equal(t, models.DeployLogError, deployLog.Status)
			assert.Equal(t, ErrDeployInterrupted.Error(), deployLog.ErrorMessage)
			assert.Contains(t, deployLog.Output, "ERROR: "+ErrDeployInterrupted.Error())
			assert.NotContains(t, deployLog.Output, "readiness timeout")
			assert.NotContains(t, types, "deployment.success")
			assert.NotContains(t, types, "deploy.timeout", "an interrupt is not a timeout")
			assert.Contains(t, types, "deployment.error")
			if tt.failedMount {
				assert.Contains(t, deployLog.Output, "FailedMount", "the pod event stays in the log output")
			}
			if tt.inHelm {
				for _, msg := range hub.getMessages() {
					assert.NotContains(t, string(msg), "waiting for pods", "the wait does not start after a shutdown")
				}
			}
		})
	}
}

// hubHas reports whether a broadcast message contains text.
func hubHas(hub *mockBroadcaster, text string) bool {
	for _, msg := range hub.getMessages() {
		if strings.Contains(string(msg), text) {
			return true
		}
	}
	return false
}

// TestFinalizeDeploy_InterruptIsNotATimeout checks that an interrupted
// deploy gives "deployment.error", also when a pod event with a timeout text
// is attached to it. A real timeout still gives "deploy.timeout".
func TestFinalizeDeploy_InterruptIsNotATimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		wantType string
		notType  string
	}{
		{name: "interrupt", err: ErrDeployInterrupted, wantType: "deployment.error", notType: "deploy.timeout"},
		{name: "interrupt with a timed out pod event",
			err:      withPodEvent(ErrDeployInterrupted, "FailedMount: timed out waiting for the condition"),
			wantType: "deployment.error", notType: "deploy.timeout"},
		{name: "interrupt with a deadline pod event",
			err:      withPodEvent(ErrDeployInterrupted, "FailedCreatePodSandBox: context deadline exceeded"),
			wantType: "deployment.error", notType: "deploy.timeout"},
		{name: "real timeout", err: errors.New("deploying chart app: timed out waiting for the condition"),
			wantType: "deploy.timeout", notType: "deployment.error"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			instanceRepo := newMockInstanceRepo()
			logRepo := newMockDeployLogRepo()
			notif := &mockNotifier{}
			inst := seedInstance(t, instanceRepo, "inst-finalize-interrupt", "finalize-interrupt", "owner-1")
			inst.Status = models.StackStatusDeploying
			require.NoError(t, instanceRepo.Update(context.Background(), inst))
			deployLog := &models.DeploymentLog{
				ID: "log-finalize-interrupt", StackInstanceID: inst.ID, Action: models.DeployActionDeploy,
				Status: models.DeployLogRunning, StartedAt: time.Now().UTC(),
			}
			require.NoError(t, logRepo.Create(context.Background(), deployLog))
			mgr := NewManager(ManagerConfig{
				Registry:      &mockClusterResolver{helm: &mockHelmExecutor{}},
				InstanceRepo:  instanceRepo,
				DeployLogRepo: logRepo,
				TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
				Notifier:      notif,
			})
			t.Cleanup(mgr.Shutdown)

			mgr.finalizeDeploy(inst.ID, deployLog, "", tt.err, false, "", "")

			stored, err := instanceRepo.FindByID(context.Background(), inst.ID)
			require.NoError(t, err)
			assert.Equal(t, models.StackStatusError, stored.Status)
			types := notifTypes(notif.getCalls())
			assert.Contains(t, types, tt.wantType)
			assert.NotContains(t, types, tt.notType)
		})
	}
}

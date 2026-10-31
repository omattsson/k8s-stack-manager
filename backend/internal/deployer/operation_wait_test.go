package deployer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"backend/internal/k8s"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// A stop during a chart install cancels the deploy, waits for its log to
// end and then uninstalls: the instance ends stopped, never stabilizing,
// and the second chart is never installed. One concurrency slot: the stop
// must not hold it while it waits.
func TestManager_StopDuringChartInstall(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	inst := seedInstance(t, instanceRepo, "inst-stop-install", "stop-install", "owner-1")

	var deployLogID string
	var mu sync.Mutex
	var logStatusAtUninstall []string
	helm := &mockHelmExecutor{installGate: make(chan struct{}), installStarted: make(chan struct{})}
	helm.uninstallFunc = func(_ context.Context, req UninstallRequest) (string, error) {
		// Not require: this runs on the goroutine of the stop.
		status := "unreadable"
		if l, err := logRepo.FindByID(context.Background(), deployLogID); err == nil {
			status = l.Status
		}
		mu.Lock()
		logStatusAtUninstall = append(logStatusAtUninstall, status)
		mu.Unlock()
		return "uninstalled " + req.ReleaseName, nil
	}
	mgr := NewManager(ManagerConfig{
		Registry:              &mockClusterResolver{helm: helm},
		InstanceRepo:          instanceRepo,
		DeployLogRepo:         logRepo,
		TxRunner:              &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:                   &mockBroadcaster{},
		MaxConcurrent:         1,
		StabilizeTimeout:      time.Second,
		StabilizePollInterval: 20 * time.Millisecond,
	})
	charts := []ChartDeployInfo{
		{ChartConfig: models.ChartConfig{ChartName: "first", DeployOrder: 1}},
		{ChartConfig: models.ChartConfig{ChartName: "second", DeployOrder: 2}},
	}

	var err error
	deployLogID, err = mgr.Deploy(context.Background(), DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "def"},
		Charts:     charts,
	})
	require.NoError(t, err)
	select {
	case <-helm.installStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("the first install did not start")
	}

	cur, err := instanceRepo.FindByID(context.Background(), inst.ID)
	require.NoError(t, err)
	stopLogID, err := mgr.StopWithCharts(context.Background(), cur, charts)
	require.NoError(t, err)

	deployLog := waitForLogDone(t, logRepo, deployLogID)
	stopLog := waitForLogDone(t, logRepo, stopLogID)

	assert.Equal(t, models.DeployLogError, deployLog.Status)
	assert.Contains(t, deployLog.ErrorMessage, "deploy cancelled: another operation changed the instance (status stopping) during the chart installs")
	helm.mu.Lock()
	installs := len(helm.installCalls)
	helm.mu.Unlock()
	assert.Equal(t, 1, installs, "no chart is installed after the stop")

	assert.Equal(t, models.DeployLogSuccess, stopLog.Status)
	assert.Contains(t, stopLog.Output, "Waiting for the running deploy (log "+deployLogID+") to end before the uninstall")
	mu.Lock()
	assert.Equal(t, []string{models.DeployLogError, models.DeployLogError}, logStatusAtUninstall, "the uninstalls start after the deploy log ended")
	mu.Unlock()

	final, err := instanceRepo.FindByID(context.Background(), inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusStopped, final.Status)
}

// stopAtMarkRepo sets the status stopping right before MarkStatus.
type stopAtMarkRepo struct{ *mockInstanceRepo }

func (r *stopAtMarkRepo) MarkStatus(ctx context.Context, id, to string, from []string) (bool, error) {
	inst, err := r.FindByID(ctx, id)
	if err != nil {
		return false, err
	}
	inst.Status = models.StackStatusStopping
	if err := r.Update(ctx, inst); err != nil {
		return false, err
	}
	return r.mockInstanceRepo.MarkStatus(ctx, id, to, from)
}

func TestManager_MarkStabilizing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		status   string
		newerLog bool
		// stopAtWrite sets the status stopping just before the conditional
		// write (a stop that commits after the ownership check).
		stopAtWrite bool
		wantTaken   string // status in *instanceTakenError, "" for no error
		wantStatus  string
	}{
		{name: "deploying, newest log", status: models.StackStatusDeploying, wantStatus: models.StackStatusStabilizing},
		{name: "already stabilizing (rollback)", status: models.StackStatusStabilizing, wantStatus: models.StackStatusStabilizing},
		{name: "a stop ended first", status: models.StackStatusStopped, newerLog: true, wantTaken: models.StackStatusStopped, wantStatus: models.StackStatusStopped},
		{name: "a stop is running", status: models.StackStatusStopping, newerLog: true, wantTaken: models.StackStatusStopping, wantStatus: models.StackStatusStopping},
		{name: "status deploying, but a newer log", status: models.StackStatusDeploying, newerLog: true, wantTaken: models.StackStatusDeploying, wantStatus: models.StackStatusDeploying},
		{name: "a stop commits between the check and the write", status: models.StackStatusDeploying, stopAtWrite: true, wantTaken: models.StackStatusStopping, wantStatus: models.StackStatusStopping},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			instanceRepo := newMockInstanceRepo()
			logRepo := newMockDeployLogRepo()
			inst := seedInstance(t, instanceRepo, "inst-mark", "mark", "owner-1")
			inst.Status = tt.status
			require.NoError(t, instanceRepo.Update(context.Background(), inst))
			start := time.Now().UTC()
			require.NoError(t, logRepo.Create(context.Background(), &models.DeploymentLog{
				ID: "log-own", StackInstanceID: inst.ID, Action: models.DeployActionDeploy,
				Status: models.DeployLogRunning, StartedAt: start,
			}))
			if tt.newerLog {
				require.NoError(t, logRepo.Create(context.Background(), &models.DeploymentLog{
					ID: "log-stop", StackInstanceID: inst.ID, Action: models.DeployActionStop,
					Status: models.DeployLogRunning, StartedAt: start.Add(time.Second),
				}))
			}
			var repo models.StackInstanceRepository = instanceRepo
			if tt.stopAtWrite {
				repo = &stopAtMarkRepo{mockInstanceRepo: instanceRepo}
			}
			mgr := NewManager(ManagerConfig{InstanceRepo: repo, DeployLogRepo: logRepo, MaxConcurrent: 1})

			err := mgr.markStabilizing(inst.ID, "log-own")
			if tt.wantTaken == "" {
				require.NoError(t, err)
			} else {
				var taken *instanceTakenError
				require.True(t, errors.As(err, &taken), "error %v", err)
				assert.Equal(t, tt.wantTaken, taken.status)
			}
			stored, err := instanceRepo.FindByID(context.Background(), inst.ID)
			require.NoError(t, err)
			assert.Equal(t, tt.wantStatus, stored.Status)
		})
	}
}

// A stop that starts just before markStabilizing leaves the status
// stabilizing and a newer log: the readiness wait ends at its next poll.
func TestAwaitReadiness_NewerOperationEndsWait(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	inst := seedInstance(t, instanceRepo, "inst-ready-taken", "ready-taken", "owner-1")
	inst.Status = models.StackStatusDeploying
	require.NoError(t, instanceRepo.Update(context.Background(), inst))
	start := time.Now().UTC()
	require.NoError(t, logRepo.Create(context.Background(), &models.DeploymentLog{
		ID: "log-own", StackInstanceID: inst.ID, Action: models.DeployActionDeploy,
		Status: models.DeployLogRunning, StartedAt: start,
	}))
	// A pending pod: the namespace never becomes healthy.
	k8sClient := k8s.NewClientFromInterface(fake.NewSimpleClientset(
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: inst.Namespace},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "pending", Namespace: inst.Namespace},
			Status:     corev1.PodStatus{Phase: corev1.PodPending},
		},
	))
	mgr := NewManager(ManagerConfig{
		InstanceRepo:          instanceRepo,
		DeployLogRepo:         logRepo,
		Hub:                   &mockBroadcaster{},
		MaxConcurrent:         1,
		StabilizeTimeout:      time.Minute,
		StabilizePollInterval: 20 * time.Millisecond,
	})

	done := make(chan error, 1)
	go func() {
		_, err := mgr.awaitReadiness(k8sClient, inst.ID, inst.Namespace, "log-own")
		done <- err
	}()
	require.Eventually(t, func() bool {
		cur, err := instanceRepo.FindByID(context.Background(), inst.ID)
		return err == nil && cur.Status == models.StackStatusStabilizing
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, logRepo.Create(context.Background(), &models.DeploymentLog{
		ID: "log-stop", StackInstanceID: inst.ID, Action: models.DeployActionStop,
		Status: models.DeployLogRunning, StartedAt: start.Add(time.Second),
	}))

	select {
	case err := <-done:
		var taken *instanceTakenError
		require.True(t, errors.As(err, &taken), "error %v", err)
		assert.Equal(t, models.StackStatusStabilizing, taken.status)
		assert.Equal(t, "stabilization aborted: another operation started", err.Error())
	case <-time.After(5 * time.Second):
		t.Fatal("the readiness wait did not end after a newer operation started")
	}
}

func TestRunningEarlierLog(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	later := now.Add(time.Minute)
	earlier := now.Add(-time.Minute)
	stop := &models.DeploymentLog{ID: "own", StartedAt: now}
	before, after := now.Add(-time.Second), now.Add(time.Second)
	tests := []struct {
		name string
		logs []models.DeploymentLog
		want string
	}{
		{name: "no logs"},
		{name: "ended deploy", logs: []models.DeploymentLog{{ID: "d", Status: models.DeployLogSuccess, StartedAt: before, DeadlineAt: &later}}},
		{name: "running deploy", logs: []models.DeploymentLog{{ID: "d", Status: models.DeployLogRunning, StartedAt: before, DeadlineAt: &later}}, want: "d"},
		{name: "same start time", logs: []models.DeploymentLog{{ID: "d", Status: models.DeployLogRunning, StartedAt: now, DeadlineAt: &later}}, want: "d"},
		{name: "started after the stop", logs: []models.DeploymentLog{{ID: "d", Status: models.DeployLogRunning, StartedAt: after, DeadlineAt: &later}}},
		{name: "own log is skipped", logs: []models.DeploymentLog{{ID: "own", Status: models.DeployLogRunning, StartedAt: before, DeadlineAt: &later}}},
		{name: "past deadline", logs: []models.DeploymentLog{{ID: "d", Status: models.DeployLogRunning, StartedAt: before, DeadlineAt: &earlier}}},
		{name: "no deadline", logs: []models.DeploymentLog{{ID: "d", Status: models.DeployLogRunning, StartedAt: before}}},
		{name: "second log runs", logs: []models.DeploymentLog{
			{ID: "d1", Status: models.DeployLogError, StartedAt: before, DeadlineAt: &later},
			{ID: "r1", Status: models.DeployLogRunning, StartedAt: before, DeadlineAt: &later},
		}, want: "r1"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := runningEarlierLog(tt.logs, stop, now)
			if tt.want == "" {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tt.want, got.ID)
		})
	}
}

func TestManager_WaitForEarlierOperations(t *testing.T) {
	t.Parallel()

	logRepo := newMockDeployLogRepo()
	deadline := time.Now().UTC().Add(time.Minute)
	running := &models.DeploymentLog{
		ID: "log-deploy", StackInstanceID: "inst-wait", Action: models.DeployActionDeploy,
		Status: models.DeployLogRunning, StartedAt: time.Now().UTC(), DeadlineAt: &deadline,
	}
	require.NoError(t, logRepo.Create(context.Background(), running))
	stop := &models.DeploymentLog{ID: "log-stop", StartedAt: time.Now().UTC().Add(time.Second)}
	mgr := NewManager(ManagerConfig{
		DeployLogRepo:         logRepo,
		Hub:                   &mockBroadcaster{},
		MaxConcurrent:         1,
		StabilizePollInterval: 10 * time.Millisecond,
	})

	go func() {
		time.Sleep(100 * time.Millisecond)
		ended := *running
		ended.Status = models.DeployLogError
		_ = logRepo.Update(context.Background(), &ended)
	}()
	start := time.Now()
	out := mgr.waitForEarlierOperations("inst-wait", stop)
	assert.GreaterOrEqual(t, time.Since(start), 100*time.Millisecond)
	assert.Equal(t, "Waiting for the running deploy (log log-deploy) to end before the uninstall\n", out)

	// Nothing runs any more: no wait, no line.
	assert.Empty(t, mgr.waitForEarlierOperations("inst-wait", stop))
}

// A running deploy of a replica without a heartbeat is not awaited: the
// interrupted operation recovery ends it later.
func TestManager_WaitForEarlierOperations_StaleReplica(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		beat     bool
		wantWait bool
	}{
		{name: "replica alive", beat: true, wantWait: true},
		{name: "replica without heartbeat"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			logRepo := newMockDeployLogRepo()
			deadline := time.Now().UTC().Add(time.Minute)
			running := &models.DeploymentLog{
				ID: "log-deploy", StackInstanceID: "inst-wait", Action: models.DeployActionDeploy, ReplicaID: "replica-b",
				Status: models.DeployLogRunning, StartedAt: time.Now().UTC(), DeadlineAt: &deadline,
			}
			require.NoError(t, logRepo.Create(context.Background(), running))
			beats := newFakeHeartbeats()
			if tt.beat {
				require.NoError(t, beats.Beat(context.Background(), "replica-b"))
			}
			mgr := NewManager(ManagerConfig{
				DeployLogRepo:         logRepo,
				Hub:                   &mockBroadcaster{},
				MaxConcurrent:         1,
				StabilizePollInterval: 10 * time.Millisecond,
				ReplicaID:             "replica-a",
				Heartbeats:            beats,
			})
			if tt.wantWait {
				go func() {
					time.Sleep(50 * time.Millisecond)
					ended := *running
					ended.Status = models.DeployLogSuccess
					_ = logRepo.Update(context.Background(), &ended)
				}()
			}
			out := mgr.waitForEarlierOperations("inst-wait", &models.DeploymentLog{ID: "log-stop", StartedAt: time.Now().UTC().Add(time.Second)})
			assert.Equal(t, tt.wantWait, out != "", "output %q", out)
		})
	}
}

// newStopTestManager returns a manager, an instance with a running deploy
// log of another replica (so this process cannot end it) and the charts.
func newStopTestManager(t *testing.T, helm *mockHelmExecutor) (*Manager, *mockInstanceRepo, *mockDeployLogRepo, *models.StackInstance, *models.DeploymentLog) {
	t.Helper()
	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	inst := seedInstance(t, instanceRepo, "inst-stop-wait", "stop-wait", "owner-1")
	inst.Status = models.StackStatusDeploying
	require.NoError(t, instanceRepo.Update(context.Background(), inst))
	deadline := time.Now().UTC().Add(time.Minute)
	deployLog := &models.DeploymentLog{
		ID: "log-deploy", StackInstanceID: inst.ID, Action: models.DeployActionDeploy, ReplicaID: "replica-b",
		Status: models.DeployLogRunning, StartedAt: time.Now().UTC().Add(-time.Second), DeadlineAt: &deadline,
	}
	require.NoError(t, logRepo.Create(context.Background(), deployLog))
	beats := newFakeHeartbeats()
	require.NoError(t, beats.Beat(context.Background(), "replica-b"))
	mgr := NewManager(ManagerConfig{
		Registry:              &mockClusterResolver{helm: helm},
		InstanceRepo:          instanceRepo,
		DeployLogRepo:         logRepo,
		TxRunner:              &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:                   &mockBroadcaster{},
		MaxConcurrent:         1,
		StabilizePollInterval: 10 * time.Millisecond,
		ReplicaID:             "replica-a",
		Heartbeats:            beats,
	})
	return mgr, instanceRepo, logRepo, inst, deployLog
}

// A deploy whose request read the status before the stop starts after it:
// the stop ends without an uninstall and keeps the status of the deploy.
func TestManager_StopCancelledByNewerOperation(t *testing.T) {
	t.Parallel()

	helm := &mockHelmExecutor{}
	mgr, instanceRepo, logRepo, inst, deployLog := newStopTestManager(t, helm)
	stopLogID, err := mgr.StopWithCharts(context.Background(), inst, []ChartDeployInfo{{ChartConfig: models.ChartConfig{ChartName: "app"}}})
	require.NoError(t, err)

	// The newer deploy: a log after the stop, status deploying.
	time.Sleep(20 * time.Millisecond)
	cur, err := instanceRepo.FindByID(context.Background(), inst.ID)
	require.NoError(t, err)
	cur.Status = models.StackStatusDeploying
	require.NoError(t, instanceRepo.Update(context.Background(), cur))
	deadline := time.Now().UTC().Add(time.Minute)
	require.NoError(t, logRepo.Create(context.Background(), &models.DeploymentLog{
		ID: "log-newer", StackInstanceID: inst.ID, Action: models.DeployActionDeploy, ReplicaID: "replica-b",
		Status: models.DeployLogRunning, StartedAt: time.Now().UTC(), DeadlineAt: &deadline,
	}))
	// The earlier deploy ends; the stop stops waiting.
	ended := *deployLog
	ended.Status = models.DeployLogError
	require.NoError(t, logRepo.Update(context.Background(), &ended))

	stopLog := waitForLogDone(t, logRepo, stopLogID)
	assert.Equal(t, models.DeployLogError, stopLog.Status)
	assert.Equal(t, "stop cancelled: a newer operation started on the instance while the stop waited", stopLog.ErrorMessage)
	helm.mu.Lock()
	assert.Empty(t, helm.uninstallCalls, "the releases of the newer deploy stay")
	helm.mu.Unlock()
	final, err := instanceRepo.FindByID(context.Background(), inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusDeploying, final.Status)
}

// A shutdown while the stop waits ends the stop without an uninstall on a
// cancelled context: "Stop again."
func TestManager_StopInterruptedWhileWaiting(t *testing.T) {
	t.Parallel()

	helm := &mockHelmExecutor{}
	mgr, instanceRepo, logRepo, inst, _ := newStopTestManager(t, helm)
	stopLogID, err := mgr.StopWithCharts(context.Background(), inst, []ChartDeployInfo{{ChartConfig: models.ChartConfig{ChartName: "app"}}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		l, err := logRepo.FindByID(context.Background(), stopLogID)
		return err == nil && l.Status == models.DeployLogRunning
	}, 5*time.Second, 10*time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	mgr.shutdownCancel()

	stopLog := waitForLogDone(t, logRepo, stopLogID)
	assert.Equal(t, models.DeployLogError, stopLog.Status)
	assert.Equal(t, "Interrupted by a server restart. Stop again.", stopLog.ErrorMessage)
	helm.mu.Lock()
	assert.Empty(t, helm.uninstallCalls)
	helm.mu.Unlock()
	final, err := instanceRepo.FindByID(context.Background(), inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusError, final.Status)
	assert.Equal(t, "Interrupted by a server restart. Stop again.", final.ErrorMessage)
}

// A stop during the readiness wait: the deploy ends cancelled (not success)
// and the stop uninstalls after it.
func TestManager_StopDuringReadinessWait(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	inst := seedInstance(t, instanceRepo, "inst-stop-ready", "stop-ready", "owner-1")
	// A pending pod: the namespace never becomes healthy.
	k8sClient := k8s.NewClientFromInterface(fake.NewSimpleClientset(
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: inst.Namespace},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "pending", Namespace: inst.Namespace},
			Status:     corev1.PodStatus{Phase: corev1.PodPending},
		},
	))
	helm := &mockHelmExecutor{}
	mgr := NewManager(ManagerConfig{
		Registry:              &mockClusterResolver{helm: helm, k8sClient: k8sClient},
		InstanceRepo:          instanceRepo,
		DeployLogRepo:         logRepo,
		TxRunner:              &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:                   &mockBroadcaster{},
		MaxConcurrent:         1,
		StabilizeTimeout:      time.Minute,
		StabilizePollInterval: 20 * time.Millisecond,
	})
	charts := []ChartDeployInfo{{ChartConfig: models.ChartConfig{ChartName: "app"}}}
	deployLogID, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "def"},
		Charts:     charts,
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		cur, err := instanceRepo.FindByID(context.Background(), inst.ID)
		return err == nil && cur.Status == models.StackStatusStabilizing
	}, 5*time.Second, 10*time.Millisecond)

	cur, err := instanceRepo.FindByID(context.Background(), inst.ID)
	require.NoError(t, err)
	stopLogID, err := mgr.StopWithCharts(context.Background(), cur, charts)
	require.NoError(t, err)
	deployLog := waitForLogDone(t, logRepo, deployLogID)
	stopLog := waitForLogDone(t, logRepo, stopLogID)

	assert.Equal(t, models.DeployLogError, deployLog.Status)
	assert.Equal(t, "deploy cancelled: another operation changed the instance (status stopping) during the readiness wait", deployLog.ErrorMessage)
	assert.Equal(t, models.DeployLogSuccess, stopLog.Status)
	final, err := instanceRepo.FindByID(context.Background(), inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusStopped, final.Status)
}

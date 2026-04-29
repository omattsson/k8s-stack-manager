package deployer

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"backend/internal/database"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// noopBranchOverrideRepo satisfies ChartBranchOverrideRepository with no-ops.
type noopBranchOverrideRepo struct{}

func (noopBranchOverrideRepo) List(string) ([]*models.ChartBranchOverride, error) { return nil, nil }
func (noopBranchOverrideRepo) Get(string, string) (*models.ChartBranchOverride, error) {
	return nil, fmt.Errorf("not found")
}
func (noopBranchOverrideRepo) Set(*models.ChartBranchOverride) error { return nil }
func (noopBranchOverrideRepo) Delete(string, string) error           { return nil }
func (noopBranchOverrideRepo) DeleteByInstance(string) error         { return nil }

// txRunnerWithBranchOverride extends mockTxRunner to include a BranchOverride repo,
// needed for tests that exercise the delete-after-clean path.
type txRunnerWithBranchOverride struct {
	instanceRepo models.StackInstanceRepository
	logRepo      models.DeploymentLogRepository
	boRepo       models.ChartBranchOverrideRepository
}

func (m *txRunnerWithBranchOverride) RunInTx(fn func(repos database.TxRepos) error) error {
	return fn(database.TxRepos{
		StackInstance:  m.instanceRepo,
		DeploymentLog:  m.logRepo,
		BranchOverride: m.boRepo,
	})
}

// mockNotifier records all Notify calls for assertion.
type mockNotifier struct {
	mu    sync.Mutex
	calls []notifyCall
}

type notifyCall struct {
	UserID     string
	Type       string
	Title      string
	Message    string
	EntityType string
	EntityID   string
}

func (m *mockNotifier) Notify(_ context.Context, userID, notifType, title, message, entityType, entityID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, notifyCall{
		UserID:     userID,
		Type:       notifType,
		Title:      title,
		Message:    message,
		EntityType: entityType,
		EntityID:   entityID,
	})
	return nil
}

func (m *mockNotifier) getCalls() []notifyCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]notifyCall, len(m.calls))
	copy(cp, m.calls)
	return cp
}

// waitForNotification polls until the notifier has at least n calls.
func (m *mockNotifier) waitForCalls(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		count := len(m.calls)
		m.mu.Unlock()
		if count >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d notification calls, got %d", n, len(m.getCalls()))
}

func newNotificationTestManager(notif *mockNotifier) (*Manager, *mockInstanceRepo, *mockDeployLogRepo) {
	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
		Notifier:      notif,
	})

	return mgr, instanceRepo, logRepo
}

func seedInstance(t *testing.T, repo *mockInstanceRepo, id, name, ownerID string) *models.StackInstance {
	t.Helper()
	inst := &models.StackInstance{
		ID:                id,
		StackDefinitionID: "def-1",
		Name:              name,
		Namespace:         "ns-" + id,
		OwnerID:           ownerID,
		Branch:            "main",
		Status:            models.StackStatusDraft,
	}
	require.NoError(t, repo.Create(inst))
	return inst
}

func TestNotification_DeploySuccess(t *testing.T) {
	t.Parallel()
	notif := &mockNotifier{}
	mgr, instanceRepo, _ := newNotificationTestManager(notif)

	inst := seedInstance(t, instanceRepo, "inst-notif-deploy", "my-stack", "user-42")

	_, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "test-def"},
		Charts:     []ChartDeployInfo{},
	})
	require.NoError(t, err)

	notif.waitForCalls(t, 1)
	calls := notif.getCalls()
	assert.Equal(t, "user-42", calls[0].UserID)
	assert.Equal(t, "deployment.success", calls[0].Type)
	assert.Contains(t, calls[0].Message, "my-stack")
	assert.Equal(t, "stack_instance", calls[0].EntityType)
	assert.Equal(t, "inst-notif-deploy", calls[0].EntityID)
}

func TestNotification_DeployError(t *testing.T) {
	t.Parallel()
	notif := &mockNotifier{}
	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	failHelm := &failingHelmExecutor{err: "helm install failed: timeout"}

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: failHelm},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
		Notifier:      notif,
	})

	inst := seedInstance(t, instanceRepo, "inst-notif-err", "err-stack", "user-99")

	_, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "test-def"},
		Charts: []ChartDeployInfo{
			{ChartConfig: models.ChartConfig{ChartName: "failing-chart", DeployOrder: 1}},
		},
	})
	require.NoError(t, err)

	notif.waitForCalls(t, 1)
	calls := notif.getCalls()
	assert.Equal(t, "deployment.error", calls[0].Type)
	assert.Equal(t, "user-99", calls[0].UserID)
}

func TestNotification_StopCompleted(t *testing.T) {
	t.Parallel()
	notif := &mockNotifier{}
	mgr, instanceRepo, _ := newNotificationTestManager(notif)

	inst := seedInstance(t, instanceRepo, "inst-stop", "stop-stack", "user-10")
	inst.Status = models.StackStatusRunning
	require.NoError(t, instanceRepo.Update(inst))

	_, err := mgr.StopWithCharts(context.Background(), inst, []ChartDeployInfo{})
	require.NoError(t, err)

	notif.waitForCalls(t, 1)
	calls := notif.getCalls()
	assert.Equal(t, "deployment.stopped", calls[0].Type)
	assert.Equal(t, "user-10", calls[0].UserID)
	assert.Contains(t, calls[0].Message, "stop-stack")
}

func TestNotification_CleanCompleted(t *testing.T) {
	t.Parallel()
	notif := &mockNotifier{}
	mgr, instanceRepo, _ := newNotificationTestManager(notif)

	inst := seedInstance(t, instanceRepo, "inst-clean", "clean-stack", "user-20")
	inst.Status = models.StackStatusRunning
	require.NoError(t, instanceRepo.Update(inst))

	_, err := mgr.Clean(context.Background(), inst, []models.ChartConfig{})
	require.NoError(t, err)

	notif.waitForCalls(t, 1)
	calls := notif.getCalls()
	assert.Equal(t, "clean.completed", calls[0].Type)
	assert.Equal(t, "user-20", calls[0].UserID)
	assert.Contains(t, calls[0].Message, "clean-stack")
}

func TestNotification_DeleteAfterClean(t *testing.T) {
	t.Parallel()
	notif := &mockNotifier{}
	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner: &txRunnerWithBranchOverride{
			instanceRepo: instanceRepo,
			logRepo:      logRepo,
			boRepo:       noopBranchOverrideRepo{},
		},
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
		Notifier:      notif,
	})

	inst := seedInstance(t, instanceRepo, "inst-del", "del-stack", "user-30")
	inst.Status = models.StackStatusRunning
	require.NoError(t, instanceRepo.Update(inst))

	mgr.ScheduleDeleteAfterClean("inst-del")

	_, err := mgr.Clean(context.Background(), inst, []models.ChartConfig{})
	require.NoError(t, err)

	notif.waitForCalls(t, 1)
	calls := notif.getCalls()
	assert.Equal(t, "instance.deleted", calls[0].Type)
	assert.Equal(t, "user-30", calls[0].UserID)
	assert.Contains(t, calls[0].Message, "del-stack")
}

func TestNotification_RollbackCompleted(t *testing.T) {
	t.Parallel()
	notif := &mockNotifier{}
	mgr, instanceRepo, _ := newNotificationTestManager(notif)

	inst := seedInstance(t, instanceRepo, "inst-rb", "rb-stack", "user-40")
	inst.Status = models.StackStatusRunning
	require.NoError(t, instanceRepo.Update(inst))

	_, err := mgr.Rollback(context.Background(), RollbackRequest{
		Instance: inst,
		Charts:   []ChartDeployInfo{},
	})
	require.NoError(t, err)

	notif.waitForCalls(t, 1)
	calls := notif.getCalls()
	assert.Equal(t, "rollback.completed", calls[0].Type)
	assert.Equal(t, "user-40", calls[0].UserID)
	assert.Contains(t, calls[0].Message, "rb-stack")
}

func TestNotification_RollbackError(t *testing.T) {
	t.Parallel()
	notif := &mockNotifier{}
	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	failHelm := &failingHelmExecutor{err: "rollback failed: timeout"}

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: failHelm},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
		Notifier:      notif,
	})

	inst := seedInstance(t, instanceRepo, "inst-rb-err", "rb-err-stack", "user-41")
	inst.Status = models.StackStatusRunning
	require.NoError(t, instanceRepo.Update(inst))

	_, err := mgr.Rollback(context.Background(), RollbackRequest{
		Instance: inst,
		Charts: []ChartDeployInfo{
			{ChartConfig: models.ChartConfig{ChartName: "fail-chart", DeployOrder: 1}},
		},
	})
	require.NoError(t, err)

	notif.waitForCalls(t, 1)
	calls := notif.getCalls()
	assert.Equal(t, "rollback.error", calls[0].Type)
	assert.Equal(t, "user-41", calls[0].UserID)
}

func TestNotification_NilNotifier(t *testing.T) {
	t.Parallel()
	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: NewHelmClient("/nonexistent/helm", "", 1*time.Second)},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 2,
	})

	inst := seedInstance(t, instanceRepo, "inst-nil-notif", "nil-stack", "user-50")

	_, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "test-def"},
		Charts:     []ChartDeployInfo{},
	})
	require.NoError(t, err)

	waitForTerminalStatus(t, instanceRepo, inst.ID)
	// No panic, no error — nil notifier is silently skipped.
}

// failingHelmExecutor returns errors for all operations.
type failingHelmExecutor struct {
	err string
}

func (f *failingHelmExecutor) Install(_ context.Context, _ InstallRequest) (string, error) {
	return "", fmt.Errorf("%s", f.err)
}

func (f *failingHelmExecutor) Uninstall(_ context.Context, _ UninstallRequest) (string, error) {
	return "", fmt.Errorf("%s", f.err)
}

func (f *failingHelmExecutor) Status(_ context.Context, _, _ string) (*ReleaseStatus, error) {
	return nil, fmt.Errorf("%s", f.err)
}

func (f *failingHelmExecutor) ListReleases(_ context.Context, _ string) ([]string, error) {
	return nil, fmt.Errorf("%s", f.err)
}

func (f *failingHelmExecutor) History(_ context.Context, _ string, _ string, _ int) ([]ReleaseRevision, error) {
	return nil, fmt.Errorf("%s", f.err)
}

func (f *failingHelmExecutor) Rollback(_ context.Context, _, _ string, _ int) (string, error) {
	return "", fmt.Errorf("%s", f.err)
}

func (f *failingHelmExecutor) GetValues(_ context.Context, _, _ string, _ int) (string, error) {
	return "", fmt.Errorf("%s", f.err)
}

func (f *failingHelmExecutor) RegistryLogin(_ context.Context, _, _, _ string) error {
	return nil
}

func (f *failingHelmExecutor) Timeout() time.Duration {
	return 30 * time.Second
}

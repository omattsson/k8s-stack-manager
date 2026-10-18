package deployer

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"backend/internal/hooks"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeHeartbeats is an in-memory models.ReplicaHeartbeatRepository. dbNow
// is the database clock; it can differ from the clock of the recovery.
type fakeHeartbeats struct {
	mu      sync.Mutex
	seen    map[string]time.Time
	dbNow   time.Time
	err     error
	deleted []time.Duration
}

func newFakeHeartbeats() *fakeHeartbeats {
	return &fakeHeartbeats{seen: map[string]time.Time{}, dbNow: time.Now()}
}

func (f *fakeHeartbeats) Beat(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen[id] = f.dbNow
	return nil
}

func (f *fakeHeartbeats) SeenWithin(_ context.Context, ids []string, maxAge time.Duration) (map[string]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]bool{}
	for _, id := range ids {
		if at, ok := f.seen[id]; ok && !at.Before(f.dbNow.Add(-maxAge)) {
			out[id] = true
		}
	}
	return out, nil
}

func (f *fakeHeartbeats) Remove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.seen, id)
	return nil
}

func (f *fakeHeartbeats) DeleteOlderThan(_ context.Context, maxAge time.Duration) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, maxAge)
	return 0, nil
}

// fakeInterruptOps implements models.InterruptedOperationRepository on top
// of the deployer mocks, with the same rules as the GORM repository.
type fakeInterruptOps struct {
	logs       *mockDeployLogRepo
	instances  *mockInstanceRepo
	heartbeats *fakeHeartbeats
	listErr    error
	// beforeInterrupt runs at the start of InterruptOperation (a change by
	// another process between the read and the update).
	beforeInterrupt func()
}

// staleReplica reports whether id has no heartbeat within staleAfter
// (database time of the heartbeat fake).
func (f *fakeInterruptOps) staleReplica(id string, staleAfter time.Duration) bool {
	if f.heartbeats == nil {
		return true
	}
	f.heartbeats.mu.Lock()
	defer f.heartbeats.mu.Unlock()
	at, ok := f.heartbeats.seen[id]
	return !ok || at.Before(f.heartbeats.dbNow.Add(-staleAfter))
}

func (f *fakeInterruptOps) ListInterruptCandidates(_ context.Context, now time.Time, selfID string, staleAfter time.Duration, limit int) ([]models.DeploymentLog, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	f.logs.mu.RLock()
	var out []models.DeploymentLog
	for _, l := range f.logs.items {
		if l.Status == models.DeployLogRunning && l.DeadlineAt != nil && l.DeadlineAt.Before(now) &&
			l.ReplicaID != "" && l.ReplicaID != selfID {
			out = append(out, *l)
		}
	}
	f.logs.mu.RUnlock()
	filtered := out[:0]
	for _, l := range out {
		if f.staleReplica(l.ReplicaID, staleAfter) {
			filtered = append(filtered, l)
		}
	}
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

func (f *fakeInterruptOps) InterruptOperation(_ context.Context, req models.InterruptRequest) (models.InterruptResult, error) {
	if f.beforeInterrupt != nil {
		f.beforeInterrupt()
	}
	if !f.staleReplica(req.ReplicaID, req.StaleAfter) {
		return models.InterruptResult{}, nil
	}
	f.logs.mu.Lock()
	defer f.logs.mu.Unlock()
	f.instances.mu.Lock()
	defer f.instances.mu.Unlock()

	l, ok := f.logs.items[req.LogID]
	if !ok || l.Status != models.DeployLogRunning || l.ReplicaID != req.ReplicaID {
		return models.InterruptResult{}, nil
	}
	res := models.InterruptResult{LogClosed: true}
	updateInstance := false
	if req.InstanceStatus != "" {
		newer := false
		for _, other := range f.logs.items {
			if other.StackInstanceID == req.InstanceID && other.ID != req.LogID && other.StartedAt.After(req.LogStartedAt) {
				newer = true
			}
		}
		if !newer {
			inst, ok := f.instances.items[req.InstanceID]
			if !ok || inst.Status != req.InstanceStatus || !inst.UpdatedAt.Equal(req.InstanceUpdatedAt) {
				return models.InterruptResult{}, models.ErrInterruptConflict
			}
			updateInstance = true
		}
	}
	now := req.Now
	l.Status = models.DeployLogError
	l.ErrorMessage = req.Message
	l.CompletedAt = &now
	if updateInstance {
		inst := f.instances.items[req.InstanceID]
		inst.Status = models.StackStatusError
		inst.ErrorMessage = req.Message
		inst.PostDeployHookUntil = nil
		inst.UpdatedAt = now
		res.InstanceUpdated = true
	}
	return res, nil
}

// fakeAuditRepo records audit entries.
type fakeAuditRepo struct {
	mu      sync.Mutex
	entries []models.AuditLog
}

func (f *fakeAuditRepo) Create(l *models.AuditLog) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, *l)
	return nil
}

func (f *fakeAuditRepo) List(models.AuditLogFilters) (*models.AuditLogResult, error) {
	return &models.AuditLogResult{}, nil
}

func (f *fakeAuditRepo) all() []models.AuditLog {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]models.AuditLog(nil), f.entries...)
}

func TestInterruptRecovery_RunOnce(t *testing.T) {
	t.Parallel()

	const (
		self     = "pod-b-22222222"
		lost     = "pod-a-11111111"
		livePeer = "pod-c-33333333"
	)

	tests := []struct {
		name           string
		action         string
		instanceStatus string
		replicaID      string
		// overdue is now - deadline_at; negative: the deadline is not reached.
		overdue    time.Duration
		noDeadline bool
		// heartbeat is the age of the last heartbeat of replicaID; < 0: no row.
		heartbeat time.Duration
		listErr   error
		// lateHeartbeat: the replica sends a heartbeat after the listing.
		lateHeartbeat bool
		// leaderSkew moves the clock of the recovery against the database
		// clock (the heartbeat check uses the database clock only).
		leaderSkew    time.Duration
		concurrent    bool // another process changes the instance during the check
		newerLog      bool
		wantClosed    int
		wantStatus    string
		wantLogStatus string
		wantNotif     string
		wantMessage   string
	}{
		{
			name: "hard kill: no heartbeat after the threshold sets error", action: models.DeployActionDeploy,
			instanceStatus: models.StackStatusDeploying, replicaID: lost, overdue: time.Minute, heartbeat: -1,
			wantClosed: 1, wantStatus: models.StackStatusError, wantLogStatus: models.DeployLogError,
			wantNotif:   "deployment.error",
			wantMessage: "Interrupted: the server that ran this operation stopped. Deploy again.",
		},
		{
			name: "stale heartbeat during a blocking hook (stabilizing) sets error", action: models.DeployActionDeploy,
			instanceStatus: models.StackStatusStabilizing, replicaID: lost, overdue: time.Hour, heartbeat: 3 * time.Minute,
			wantClosed: 1, wantStatus: models.StackStatusError, wantLogStatus: models.DeployLogError,
			wantNotif:   "deployment.error",
			wantMessage: "Interrupted: the server that ran this operation stopped. Deploy again.",
		},
		{
			name: "interrupted rollback", action: models.DeployActionRollback,
			instanceStatus: models.StackStatusDeploying, replicaID: lost, overdue: time.Hour, heartbeat: -1,
			wantClosed: 1, wantStatus: models.StackStatusError, wantLogStatus: models.DeployLogError,
			wantNotif:   "rollback.error",
			wantMessage: "Interrupted: the server that ran this operation stopped. Deploy again.",
		},
		{
			name: "interrupted stop", action: models.DeployActionStop,
			instanceStatus: models.StackStatusStopping, replicaID: lost, overdue: time.Hour, heartbeat: -1,
			wantClosed: 1, wantStatus: models.StackStatusError, wantLogStatus: models.DeployLogError,
			wantNotif:   "stop.error",
			wantMessage: "Interrupted: the server that ran this operation stopped. Stop again.",
		},
		{
			name: "interrupted clean", action: models.DeployActionClean,
			instanceStatus: models.StackStatusCleaning, replicaID: lost, overdue: time.Hour, heartbeat: -1,
			wantClosed: 1, wantStatus: models.StackStatusError, wantLogStatus: models.DeployLogError,
			wantNotif:   "clean.error",
			wantMessage: "Interrupted: the server that ran this operation stopped. Clean again.",
		},
		{
			name: "live operation on another replica (fresh heartbeat) is untouched", action: models.DeployActionDeploy,
			instanceStatus: models.StackStatusStabilizing, replicaID: livePeer, overdue: 2 * time.Hour, heartbeat: 10 * time.Second,
			wantStatus: models.StackStatusStabilizing, wantLogStatus: models.DeployLogRunning,
		},
		{
			name: "leader clock 10 minutes ahead: fresh heartbeat (database time) is untouched", action: models.DeployActionDeploy,
			instanceStatus: models.StackStatusDeploying, replicaID: livePeer, overdue: time.Hour, heartbeat: 10 * time.Second,
			leaderSkew: 10 * time.Minute,
			wantStatus: models.StackStatusDeploying, wantLogStatus: models.DeployLogRunning,
		},
		{
			name: "leader clock 10 minutes behind: stale heartbeat (database time) is recovered", action: models.DeployActionDeploy,
			instanceStatus: models.StackStatusDeploying, replicaID: lost, overdue: time.Hour, heartbeat: 3 * time.Minute,
			leaderSkew: -10 * time.Minute,
			wantClosed: 1, wantStatus: models.StackStatusError, wantLogStatus: models.DeployLogError,
			wantNotif:   "deployment.error",
			wantMessage: "Interrupted: the server that ran this operation stopped. Deploy again.",
		},
		{
			name: "threshold not reached is untouched", action: models.DeployActionDeploy,
			instanceStatus: models.StackStatusDeploying, replicaID: lost, overdue: -time.Minute, heartbeat: -1,
			wantStatus: models.StackStatusDeploying, wantLogStatus: models.DeployLogRunning,
		},
		{
			name: "own operation is untouched, also without a heartbeat row", action: models.DeployActionDeploy,
			instanceStatus: models.StackStatusDeploying, replicaID: self, overdue: time.Hour, heartbeat: -1,
			wantStatus: models.StackStatusDeploying, wantLogStatus: models.DeployLogRunning,
		},
		{
			name: "log without replica ID (before the upgrade) is untouched", action: models.DeployActionDeploy,
			instanceStatus: models.StackStatusDeploying, replicaID: "", overdue: time.Hour, heartbeat: -1,
			wantStatus: models.StackStatusDeploying, wantLogStatus: models.DeployLogRunning,
		},
		{
			name: "listing error changes nothing", action: models.DeployActionDeploy,
			instanceStatus: models.StackStatusDeploying, replicaID: lost, overdue: time.Hour, heartbeat: -1,
			listErr:    errors.New("db down"),
			wantStatus: models.StackStatusDeploying, wantLogStatus: models.DeployLogRunning,
		},
		{
			name: "log without deadline (before the upgrade) is untouched", action: models.DeployActionDeploy,
			instanceStatus: models.StackStatusDeploying, replicaID: lost, overdue: time.Hour, heartbeat: -1,
			noDeadline: true,
			wantStatus: models.StackStatusDeploying, wantLogStatus: models.DeployLogRunning,
		},
		{
			name: "heartbeat after the listing stops the interrupt", action: models.DeployActionDeploy,
			instanceStatus: models.StackStatusDeploying, replicaID: lost, overdue: time.Hour, heartbeat: 5 * time.Minute,
			lateHeartbeat: true,
			wantStatus:    models.StackStatusDeploying, wantLogStatus: models.DeployLogRunning,
		},
		{
			name: "version conflict (instance changed during the check) is skipped", action: models.DeployActionDeploy,
			instanceStatus: models.StackStatusDeploying, replicaID: lost, overdue: time.Hour, heartbeat: -1,
			concurrent: true,
			wantStatus: models.StackStatusStopping, wantLogStatus: models.DeployLogRunning,
		},
		{
			name: "newer operation owns the instance: only the old log is closed", action: models.DeployActionDeploy,
			instanceStatus: models.StackStatusStopping, replicaID: lost, overdue: time.Hour, heartbeat: -1,
			newerLog:   true,
			wantClosed: 1, wantStatus: models.StackStatusStopping, wantLogStatus: models.DeployLogError,
		},
		{
			name: "instance no longer in progress: only the log is closed", action: models.DeployActionDeploy,
			instanceStatus: models.StackStatusRunning, replicaID: lost, overdue: time.Hour, heartbeat: -1,
			wantClosed: 1, wantStatus: models.StackStatusRunning, wantLogStatus: models.DeployLogError,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

			instances := newMockInstanceRepo()
			logs := newMockDeployLogRepo()
			marker := now.Add(time.Hour)
			inst := &models.StackInstance{
				ID: "inst-1", Name: "demo", OwnerID: "owner-1", StackDefinitionID: "def-1",
				Status: tt.instanceStatus, UpdatedAt: now.Add(-time.Hour), PostDeployHookUntil: &marker,
			}
			require.NoError(t, instances.Create(inst))
			deadline := now.Add(-tt.overdue)
			opLog := &models.DeploymentLog{
				ID: "log-1", StackInstanceID: inst.ID, Action: tt.action, Status: models.DeployLogRunning,
				StartedAt: deadline.Add(-30 * time.Minute), ReplicaID: tt.replicaID, DeadlineAt: &deadline,
			}
			if tt.noDeadline {
				opLog.DeadlineAt = nil
			}
			require.NoError(t, logs.Create(context.Background(), opLog))
			if tt.newerLog {
				require.NoError(t, logs.Create(context.Background(), &models.DeploymentLog{
					ID: "log-2", StackInstanceID: inst.ID, Action: models.DeployActionStop, Status: models.DeployLogRunning,
					StartedAt: now.Add(-time.Minute), ReplicaID: livePeer,
				}))
			}

			heartbeats := newFakeHeartbeats()
			heartbeats.dbNow = now
			heartbeats.seen[self] = now
			heartbeats.seen[livePeer] = now
			if tt.heartbeat >= 0 && tt.replicaID != "" {
				heartbeats.seen[tt.replicaID] = now.Add(-tt.heartbeat)
			}

			ops := &fakeInterruptOps{logs: logs, instances: instances, heartbeats: heartbeats, listErr: tt.listErr}
			if tt.lateHeartbeat {
				ops.beforeInterrupt = func() { require.NoError(t, heartbeats.Beat(context.Background(), tt.replicaID)) }
			}
			if tt.concurrent {
				ops.beforeInterrupt = func() {
					cur, err := instances.FindByID(inst.ID)
					require.NoError(t, err)
					cur.Status = models.StackStatusStopping
					cur.UpdatedAt = now
					require.NoError(t, instances.Update(cur))
				}
			}
			hub := &mockBroadcaster{}
			notif := &mockNotifier{}
			audit := &fakeAuditRepo{}
			rec := NewInterruptRecovery(InterruptRecoveryConfig{
				Operations: ops, Heartbeats: heartbeats, Instances: instances,
				AuditLog: audit, Hub: hub, Notifier: notif,
				SelfID: self,
			})
			require.NotNil(t, rec)
			rec.now = func() time.Time { return now.Add(tt.leaderSkew) }

			closed := rec.RunOnce(context.Background())
			assert.Equal(t, tt.wantClosed, closed)

			stored, err := instances.FindByID(inst.ID)
			require.NoError(t, err)
			assert.Equal(t, tt.wantStatus, stored.Status)
			gotLog, err := logs.FindByID(context.Background(), "log-1")
			require.NoError(t, err)
			assert.Equal(t, tt.wantLogStatus, gotLog.Status)

			calls := notif.getCalls()
			msgs := hub.getMessages()
			if tt.wantNotif == "" {
				assert.Empty(t, calls, "no notification")
				assert.Empty(t, msgs, "no status broadcast")
				assert.NotNil(t, stored.PostDeployHookUntil, "an untouched instance keeps its marker")
			} else {
				assert.Equal(t, tt.wantMessage, stored.ErrorMessage)
				assert.Nil(t, stored.PostDeployHookUntil, "the hook marker is cleared")
				require.Len(t, calls, 1)
				assert.Equal(t, tt.wantNotif, calls[0].Type)
				assert.Equal(t, "owner-1", calls[0].UserID)
				assert.Contains(t, calls[0].Message, tt.wantMessage)

				require.Len(t, msgs, 1)
				var env struct {
					Type    string                  `json:"type"`
					Payload deploymentStatusPayload `json:"payload"`
				}
				require.NoError(t, json.Unmarshal(msgs[0], &env))
				assert.Equal(t, "deployment.status", env.Type)
				assert.Equal(t, deploymentStatusPayload{
					InstanceID: inst.ID, Status: models.StackStatusError, LogID: "log-1",
					ErrorMessage: tt.wantMessage, Action: tt.action,
				}, env.Payload)
			}

			entries := audit.all()
			if tt.wantClosed == 0 {
				assert.Empty(t, entries)
			} else {
				require.Len(t, entries, 1)
				assert.Equal(t, "interrupted", entries[0].Action)
				assert.Equal(t, "stack_instance", entries[0].EntityType)
				assert.Equal(t, inst.ID, entries[0].EntityID)
				assert.Equal(t, "system", entries[0].UserID)
				assert.Contains(t, entries[0].Details, tt.replicaID)
			}
		})
	}
}

// TestInterruptRecovery_LeaderElectionDisabled covers a single process
// (leader election disabled: the process is always the leader) that
// restarts after a hard kill. The old process left a deploy running under
// its own identity; the new process has the same host name but another
// suffix, so the old identity has no fresh heartbeat. The new process ends
// the old deploy at the start of its term (Run) and keeps its own deploy.
func TestInterruptRecovery_LeaderElectionDisabled(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	oldProcess, newProcess := "backend-aaaaaaaa", "backend-bbbbbbbb"

	instances := newMockInstanceRepo()
	logs := newMockDeployLogRepo()
	for _, s := range []struct{ inst, log, replica string }{
		{"inst-old", "log-old", oldProcess},
		{"inst-new", "log-new", newProcess},
	} {
		require.NoError(t, instances.Create(&models.StackInstance{ID: s.inst, Name: s.inst, OwnerID: "u1", Status: models.StackStatusDeploying}))
		deadline := now.Add(-time.Minute)
		require.NoError(t, logs.Create(context.Background(), &models.DeploymentLog{
			ID: s.log, StackInstanceID: s.inst, Action: models.DeployActionDeploy,
			Status: models.DeployLogRunning, StartedAt: now.Add(-time.Hour), ReplicaID: s.replica,
			DeadlineAt: &deadline,
		}))
	}
	heartbeats := newFakeHeartbeats()
	heartbeats.dbNow = now
	// The old process wrote its last heartbeat before the kill.
	heartbeats.seen[oldProcess] = now.Add(-30 * time.Minute)
	heartbeats.seen[newProcess] = now

	rec := NewInterruptRecovery(InterruptRecoveryConfig{
		Operations: &fakeInterruptOps{logs: logs, instances: instances, heartbeats: heartbeats},
		Heartbeats: heartbeats, Instances: instances,
		SelfID:   newProcess,
		Interval: time.Hour,
	})
	require.NotNil(t, rec)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); rec.Run(ctx) }()
	require.Eventually(t, func() bool {
		inst, err := instances.FindByID("inst-old")
		return err == nil && inst.Status == models.StackStatusError
	}, 2*time.Second, 10*time.Millisecond, "the term start check ends the old deploy")
	cancel()
	<-done

	own, err := instances.FindByID("inst-new")
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusDeploying, own.Status, "the own deploy is untouched")
	heartbeats.mu.Lock()
	assert.NotEmpty(t, heartbeats.deleted, "the leader prunes old heartbeat rows")
	heartbeats.mu.Unlock()
}

func TestInterruptRecovery_ConfigDefaults(t *testing.T) {
	t.Parallel()
	assert.Nil(t, NewInterruptRecovery(InterruptRecoveryConfig{}), "missing repositories")
	var nilRec *InterruptRecovery
	nilRec.Run(context.Background())
	assert.Zero(t, nilRec.RunOnce(context.Background()))

	rec := NewInterruptRecovery(InterruptRecoveryConfig{
		Operations: &fakeInterruptOps{logs: newMockDeployLogRepo(), instances: newMockInstanceRepo()},
		Heartbeats: newFakeHeartbeats(), Instances: newMockInstanceRepo(),
	})
	require.NotNil(t, rec)
	assert.Equal(t, DefaultInterruptCheckInterval, rec.cfg.Interval)
	assert.Equal(t, 2*time.Minute, rec.cfg.StaleAfter)
}

// TestManager_DeployLogsCarryReplicaID checks that the operations store
// the process identity on their deploy log.
func TestManager_DeployLogsCarryReplicaID(t *testing.T) {
	t.Parallel()
	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: &mockHelmExecutor{}},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           &mockBroadcaster{},
		MaxConcurrent: 1,
		ReplicaID:     "pod-a-12345678",
	})
	inst := seedInstance(t, instanceRepo, "inst-r", "replica-demo", "owner-1")

	deployID, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "def"},
		Charts:     []ChartDeployInfo{{ChartConfig: models.ChartConfig{ChartName: "app"}}},
	})
	require.NoError(t, err)
	deployLog := waitForLogDone(t, logRepo, deployID)
	assert.Equal(t, "pod-a-12345678", deployLog.ReplicaID, "the final log update keeps the replica ID")
	require.NotNil(t, deployLog.DeadlineAt, "the deploy log has a deadline")
	assert.True(t, deployLog.DeadlineAt.After(deployLog.StartedAt.Add(DeadlineMargin)))

	cur, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	stopID, err := mgr.StopWithCharts(context.Background(), cur, nil)
	require.NoError(t, err)
	stopLog := waitForLogDone(t, logRepo, stopID)
	assert.Equal(t, "pod-a-12345678", stopLog.ReplicaID)
	mgr.Shutdown()
}

// TestManager_LateFinalizeAfterRecovery checks that an operation goroutine
// that ends after the leader recovered its log (the replica was only slow,
// for example a saturated database pool stopped its heartbeat) does not
// overwrite the recovered log or the instance, and sends no notification.
func TestManager_LateFinalizeAfterRecovery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		action string
	}{
		{name: "deploy", action: models.DeployActionDeploy},
		{name: "stop", action: models.DeployActionStop},
		{name: "clean", action: models.DeployActionClean},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			started := make(chan struct{})
			release := make(chan struct{})
			block := func() {
				close(started)
				<-release
			}
			helm := &mockHelmExecutor{
				installFunc:   func(context.Context, InstallRequest) (string, error) { block(); return "installed", nil },
				uninstallFunc: func(context.Context, UninstallRequest) (string, error) { block(); return "uninstalled", nil },
			}
			instanceRepo := newMockInstanceRepo()
			logRepo := newMockDeployLogRepo()
			notif := &mockNotifier{}
			mgr := NewManager(ManagerConfig{
				Registry:      &mockClusterResolver{helm: helm, noK8sClient: true},
				InstanceRepo:  instanceRepo,
				DeployLogRepo: logRepo,
				TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
				Hub:           &mockBroadcaster{},
				MaxConcurrent: 1,
				Notifier:      notif,
				ReplicaID:     "pod-a-12345678",
			})
			inst := seedInstance(t, instanceRepo, "inst-late", "late-demo", "owner-1")
			charts := []ChartDeployInfo{{ChartConfig: models.ChartConfig{ChartName: "app"}}}

			var logID string
			var err error
			switch tt.action {
			case models.DeployActionDeploy:
				logID, err = mgr.Deploy(context.Background(), DeployRequest{
					Instance: inst, Definition: &models.StackDefinition{ID: "def-1", Name: "def"}, Charts: charts,
				})
			case models.DeployActionStop:
				logID, err = mgr.StopWithCharts(context.Background(), inst, charts)
			case models.DeployActionClean:
				// A clean needs an instance with cluster resources.
				inst.Status = models.StackStatusRunning
				require.NoError(t, instanceRepo.Update(inst))
				logID, err = mgr.Clean(context.Background(), inst, []models.ChartConfig{{ChartName: "app"}})
			}
			require.NoError(t, err)
			<-started

			// The leader recovers the operation while the goroutine runs.
			msg := models.InterruptedOperationMessage(tt.action)
			logRepo.mu.Lock()
			l := logRepo.items[logID]
			l.Status = models.DeployLogError
			l.ErrorMessage = msg
			logRepo.mu.Unlock()
			cur, err := instanceRepo.FindByID(inst.ID)
			require.NoError(t, err)
			cur.Status = models.StackStatusError
			cur.ErrorMessage = msg
			require.NoError(t, instanceRepo.Update(cur))

			close(release)
			mgr.Shutdown() // waits for the goroutine

			gotLog, err := logRepo.FindByID(context.Background(), logID)
			require.NoError(t, err)
			assert.Equal(t, models.DeployLogError, gotLog.Status)
			assert.Equal(t, msg, gotLog.ErrorMessage, "the recovered log is not overwritten")
			assert.Empty(t, gotLog.Output, "the late output is not written")
			stored, err := instanceRepo.FindByID(inst.ID)
			require.NoError(t, err)
			assert.Equal(t, models.StackStatusError, stored.Status)
			assert.Equal(t, msg, stored.ErrorMessage, "the instance keeps the recovered error")
			assert.Empty(t, notif.getCalls(), "no success or failure notification from the late goroutine")
		})
	}
}

func TestManager_OperationDeadline(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	helm := &mockHelmExecutor{timeout: 10 * time.Minute}
	d, err := hooks.NewDispatcher(hooks.Config{Subscriptions: []hooks.Subscription{
		{Name: "gate", URL: "http://gate.invalid/hook", Events: []string{hooks.EventPreDeploy, hooks.EventPreRollback}, TimeoutSeconds: 600},
		{Name: "restore", URL: "http://restore.invalid/hook", Events: []string{hooks.EventPostDeploy}, Blocking: true, TimeoutSeconds: 900},
	}}, nil)
	require.NoError(t, err)
	mgr := NewManager(ManagerConfig{StabilizeTimeout: 5 * time.Minute, Hooks: d})
	t.Cleanup(mgr.Shutdown)

	tests := []struct {
		name   string
		action string
		charts int
		want   time.Duration
	}{
		// pre-deploy 10m + 3 charts x 10m + 1m + stabilize 5m + blocking 15m + 1m + margin 5m
		{name: "deploy", action: models.DeployActionDeploy, charts: 3, want: 10*time.Minute + 31*time.Minute + 5*time.Minute + 16*time.Minute + DeadlineMargin},
		// pre-rollback 10m + 2 x 10m + 1m + stabilize 5m + margin
		{name: "rollback", action: models.DeployActionRollback, charts: 2, want: 10*time.Minute + 21*time.Minute + 5*time.Minute + DeadlineMargin},
		// one helm timeout + chart margin + margin
		{name: "stop", action: models.DeployActionStop, charts: 5, want: 11*time.Minute + DeadlineMargin},
		{name: "clean", action: models.DeployActionClean, charts: 5, want: 11*time.Minute + DeadlineMargin},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := mgr.operationDeadline(start, tt.action, helm, tt.charts)
			require.NotNil(t, got)
			assert.Equal(t, tt.want, got.Sub(start))
		})
	}
}

// TestManager_QueuedOperationCancelledAfterRecovery checks that an
// operation that waited for a concurrency slot stops before any helm call
// when the leader ended its log in the meantime.
func TestManager_QueuedOperationCancelledAfterRecovery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		action string
	}{
		{name: "deploy", action: models.DeployActionDeploy},
		{name: "stop", action: models.DeployActionStop},
		{name: "clean", action: models.DeployActionClean},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			started := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			helm := &mockHelmExecutor{
				installFunc: func(_ context.Context, req InstallRequest) (string, error) {
					if req.Namespace == "ns-inst-first" {
						once.Do(func() { close(started) })
						<-release
					}
					return "installed", nil
				},
			}
			instanceRepo := newMockInstanceRepo()
			logRepo := newMockDeployLogRepo()
			notif := &mockNotifier{}
			mgr := NewManager(ManagerConfig{
				Registry:      &mockClusterResolver{helm: helm, noK8sClient: true},
				InstanceRepo:  instanceRepo,
				DeployLogRepo: logRepo,
				TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
				Hub:           &mockBroadcaster{},
				MaxConcurrent: 1,
				Notifier:      notif,
				ReplicaID:     "pod-a-12345678",
			})
			charts := []ChartDeployInfo{{ChartConfig: models.ChartConfig{ChartName: "app"}}}
			def := &models.StackDefinition{ID: "def-1", Name: "def"}

			// The first deploy holds the only slot.
			first := seedInstance(t, instanceRepo, "inst-first", "first", "owner-1")
			_, err := mgr.Deploy(context.Background(), DeployRequest{Instance: first, Definition: def, Charts: charts})
			require.NoError(t, err)
			<-started

			queued := seedInstance(t, instanceRepo, "inst-queued", "queued", "owner-2")
			var logID string
			switch tt.action {
			case models.DeployActionDeploy:
				logID, err = mgr.Deploy(context.Background(), DeployRequest{Instance: queued, Definition: def, Charts: charts})
			case models.DeployActionStop:
				logID, err = mgr.StopWithCharts(context.Background(), queued, charts)
			case models.DeployActionClean:
				queued.Status = models.StackStatusRunning
				require.NoError(t, instanceRepo.Update(queued))
				logID, err = mgr.CleanForDelete(context.Background(), queued, []models.ChartConfig{{ChartName: "app"}})
			}
			require.NoError(t, err)

			// The leader ends the queued log. The instance is left as it is,
			// so only the check at the slot can stop the operation.
			msg := models.InterruptedOperationMessage(tt.action)
			logRepo.mu.Lock()
			logRepo.items[logID].Status = models.DeployLogError
			logRepo.items[logID].ErrorMessage = msg
			logRepo.mu.Unlock()
			before, err := instanceRepo.FindByID(queued.ID)
			require.NoError(t, err)

			close(release)
			mgr.Shutdown()

			helm.mu.Lock()
			for _, c := range helm.installCalls {
				assert.NotEqual(t, "ns-inst-queued", c.Namespace, "no helm install for the cancelled operation")
			}
			assert.Empty(t, helm.uninstallCalls, "no helm uninstall for the cancelled operation")
			helm.mu.Unlock()

			gotLog, err := logRepo.FindByID(context.Background(), logID)
			require.NoError(t, err)
			assert.Equal(t, models.DeployLogError, gotLog.Status)
			assert.Equal(t, msg, gotLog.ErrorMessage)
			stored, err := instanceRepo.FindByID(queued.ID)
			require.NoError(t, err, "the instance is not deleted after a cancelled clean")
			assert.Equal(t, before.Status, stored.Status, "no instance write from the cancelled operation")
			assert.Equal(t, before.ErrorMessage, stored.ErrorMessage)
			for _, c := range notif.getCalls() {
				assert.NotEqual(t, queued.ID, c.EntityID, "no notification for the cancelled operation")
			}
		})
	}
}

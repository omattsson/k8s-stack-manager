package deployer

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/k8s"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func newCleanForDeleteManager(instanceRepo *mockInstanceRepo, logRepo *mockDeployLogRepo, hub *mockBroadcaster, helm *mockHelmExecutor, k8sClient *k8s.Client) *Manager {
	return NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: helm, k8sClient: k8sClient, noK8sClient: k8sClient == nil},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:           hub,
		MaxConcurrent: 2,
	})
}

func TestManager_CleanForDelete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		status       string
		staleMark    bool // the instance has delete_after_clean from an earlier crash
		plainClean   bool
		failNSDelete bool
		wantStartOK  bool
		wantDeleted  bool
		wantStatus   string
		wantMessage  string
	}{
		{name: "success deletes the row and broadcasts instance.deleted", status: models.StackStatusStopped, wantStartOK: true, wantDeleted: true},
		{name: "failed clean keeps the instance and clears the mark", status: models.StackStatusRunning, failNSDelete: true,
			wantStartOK: true, wantStatus: models.StackStatusError, wantMessage: models.DeleteCleanFailedMessage},
		{name: "draft instance is a conflict", status: models.StackStatusDraft},
		{name: "instance in progress is a conflict", status: models.StackStatusDeploying},
		{name: "plain clean does not overwrite a delete mark", status: models.StackStatusStopped, staleMark: true, plainClean: true},
		{name: "plain clean without a mark keeps the instance", status: models.StackStatusStopped, plainClean: true,
			wantStartOK: true, wantStatus: models.StackStatusDraft},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			instanceRepo := newMockInstanceRepo()
			logRepo := newMockDeployLogRepo()
			hub := &mockBroadcaster{}
			var k8sClient *k8s.Client
			if tt.failNSDelete {
				cs := fake.NewSimpleClientset()
				cs.PrependReactor("delete", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, errors.New("namespace delete refused")
				})
				k8sClient = k8s.NewClientFromInterface(cs)
			}
			mgr := newCleanForDeleteManager(instanceRepo, logRepo, hub, &mockHelmExecutor{}, k8sClient)

			inst := seedInstance(t, instanceRepo, "inst-cfd", "cfd", "owner-1")
			inst.Status = tt.status
			inst.DeleteAfterClean = tt.staleMark
			require.NoError(t, instanceRepo.Update(inst))

			var logID string
			var err error
			if tt.plainClean {
				logID, err = mgr.Clean(context.Background(), inst, []models.ChartConfig{{ChartName: "app"}})
			} else {
				logID, err = mgr.CleanForDelete(context.Background(), inst, []models.ChartConfig{{ChartName: "app"}})
			}
			if !tt.wantStartOK {
				wantErr := models.ErrDeleteConflict
				if tt.plainClean {
					wantErr = models.ErrCleanConflict
				}
				require.ErrorIs(t, err, wantErr)
				assert.Empty(t, logID)
				got, findErr := instanceRepo.FindByID(inst.ID)
				require.NoError(t, findErr)
				assert.Equal(t, tt.status, got.Status, "a conflict changes nothing")
				logRepo.mu.RLock()
				assert.Empty(t, logRepo.items, "a conflict creates no clean log")
				logRepo.mu.RUnlock()
				return
			}
			require.NoError(t, err)
			require.NotEmpty(t, logID)

			if tt.wantDeleted {
				require.Eventually(t, func() bool {
					_, findErr := instanceRepo.FindByID(inst.ID)
					return findErr != nil
				}, 5*time.Second, 10*time.Millisecond)
				require.Eventually(t, func() bool {
					for _, msg := range hub.getMessages() {
						if parseBroadcastMessageType(msg) == "instance.deleted" && strings.Contains(string(msg), inst.ID) {
							return true
						}
					}
					return false
				}, 5*time.Second, 10*time.Millisecond)
				return
			}

			waitForTerminalStatus(t, instanceRepo, inst.ID)
			mgr.Shutdown()
			got, findErr := instanceRepo.FindByID(inst.ID)
			require.NoError(t, findErr, "the instance must stay")
			assert.Equal(t, tt.wantStatus, got.Status)
			assert.False(t, got.DeleteAfterClean, "the delete mark must be cleared")
			if tt.wantMessage != "" {
				assert.True(t, strings.HasPrefix(got.ErrorMessage, tt.wantMessage), got.ErrorMessage)
			}
		})
	}
}

// TestManager_CleanForDelete_Concurrent: of two concurrent deletes, one
// starts the clean and the other gets ErrDeleteConflict.
func TestManager_CleanForDelete_Concurrent(t *testing.T) {
	t.Parallel()
	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	mgr := newCleanForDeleteManager(instanceRepo, logRepo, &mockBroadcaster{}, &mockHelmExecutor{}, nil)

	inst := seedInstance(t, instanceRepo, "inst-cfd-conc", "cfd-conc", "owner-1")
	inst.Status = models.StackStatusRunning
	require.NoError(t, instanceRepo.Update(inst))

	const callers = 5
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for n := 0; n < callers; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			cp := *inst // each request read the instance before the start
			_, errs[n] = mgr.CleanForDelete(context.Background(), &cp, []models.ChartConfig{{ChartName: "app"}})
		}(n)
	}
	wg.Wait()

	started := 0
	for _, err := range errs {
		if err == nil {
			started++
		} else {
			assert.ErrorIs(t, err, models.ErrDeleteConflict)
		}
	}
	assert.Equal(t, 1, started)
	require.Eventually(t, func() bool {
		_, err := instanceRepo.FindByID(inst.ID)
		return err != nil
	}, 5*time.Second, 10*time.Millisecond)
}

// TestManager_CleanVersusDelete_Concurrent: a plain clean and a delete of
// the same instance at the same time start one operation. A plain clean
// never clears the delete mark of a delete that started first.
func TestManager_CleanVersusDelete_Concurrent(t *testing.T) {
	t.Parallel()
	for round := 0; round < 20; round++ {
		instanceRepo := newMockInstanceRepo()
		logRepo := newMockDeployLogRepo()
		mgr := newCleanForDeleteManager(instanceRepo, logRepo, &mockBroadcaster{}, &mockHelmExecutor{}, nil)

		inst := seedInstance(t, instanceRepo, "inst-cvd", "cvd", "owner-1")
		inst.Status = models.StackStatusStopped
		require.NoError(t, instanceRepo.Update(inst))

		var wg sync.WaitGroup
		var cleanErr, deleteErr error
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			cp := *inst
			_, cleanErr = mgr.Clean(context.Background(), &cp, []models.ChartConfig{{ChartName: "app"}})
		}()
		go func() {
			defer wg.Done()
			<-start
			cp := *inst
			_, deleteErr = mgr.CleanForDelete(context.Background(), &cp, []models.ChartConfig{{ChartName: "app"}})
		}()
		close(start)
		wg.Wait()

		require.True(t, (cleanErr == nil) != (deleteErr == nil), "exactly one starts: clean=%v delete=%v", cleanErr, deleteErr)
		if cleanErr != nil {
			require.ErrorIs(t, cleanErr, models.ErrCleanConflict)
			require.Eventually(t, func() bool {
				_, err := instanceRepo.FindByID(inst.ID)
				return err != nil
			}, 5*time.Second, 5*time.Millisecond, "the delete that won deletes the row")
		} else {
			require.ErrorIs(t, deleteErr, models.ErrDeleteConflict)
			waitForTerminalStatus(t, instanceRepo, inst.ID)
			got, err := instanceRepo.FindByID(inst.ID)
			require.NoError(t, err, "the clean that won keeps the row")
			assert.Equal(t, models.StackStatusDraft, got.Status)
		}
		mgr.Shutdown()
		logRepo.mu.RLock()
		assert.Len(t, logRepo.items, 1, "the loser creates no log")
		logRepo.mu.RUnlock()
	}
}

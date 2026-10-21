package deployer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"backend/internal/hooks"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ctxRecordingDefinitionRepo records the marker of each FindByID context.
type ctxRecordingDefinitionRepo struct {
	*mockDefinitionRepo
	mu    sync.Mutex
	marks []any
}

func (r *ctxRecordingDefinitionRepo) FindByID(ctx context.Context, id string) (*models.StackDefinition, error) {
	r.mu.Lock()
	r.marks = append(r.marks, ctx.Value(callerCtxKey{}))
	r.mu.Unlock()
	return r.mockDefinitionRepo.FindByID(ctx, id)
}

func (r *ctxRecordingDefinitionRepo) findMarks() []any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]any(nil), r.marks...)
}

func newRecordingDefinitionRepo() *ctxRecordingDefinitionRepo {
	repo := &ctxRecordingDefinitionRepo{mockDefinitionRepo: newMockDefinitionRepo()}
	repo.items["def-1"] = &models.StackDefinition{ID: "def-1", Name: "def"}
	return repo
}

// TestCleanupExecutor_DefinitionReadUsesCallerContext checks that the
// cleanup policy actions read the definition with the context of the policy
// run (#656), and that a cancelled run stops before the chart lookup.
func TestCleanupExecutor_DefinitionReadUsesCallerContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		call func(ctx context.Context, e *CleanupExecutor, inst *models.StackInstance) error
		name string
	}{
		{name: "stop", call: func(ctx context.Context, e *CleanupExecutor, inst *models.StackInstance) error {
			return e.StopInstance(ctx, inst)
		}},
		{name: "clean", call: func(ctx context.Context, e *CleanupExecutor, inst *models.StackInstance) error {
			return e.CleanInstance(ctx, inst)
		}},
		{name: "delete with a clean", call: func(ctx context.Context, e *CleanupExecutor, inst *models.StackInstance) error {
			return e.DeleteInstance(ctx, inst)
		}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			instances := newMockInstanceRepo()
			mgr := NewManager(ManagerConfig{InstanceRepo: instances, DeployLogRepo: newMockDeployLogRepo()})
			t.Cleanup(mgr.Shutdown)
			defs := newRecordingDefinitionRepo()
			charts := newMockChartConfigRepo()
			// The chart lookup ends the call after the definition read.
			charts.err = errors.New("chart lookup reached")
			exec := NewCleanupExecutor(mgr, defs, charts, instances)
			inst := &models.StackInstance{ID: "inst-1", StackDefinitionID: "def-1", Status: models.StackStatusStopped}

			err := tt.call(markedCtx(), exec, inst)

			require.ErrorContains(t, err, "listing charts")
			assert.Equal(t, []any{"caller"}, defs.findMarks(), "FindByID runs with the context of the policy run")
		})
	}

	// A run that ends after pre-instance-delete still deletes: no "pre" event
	// without the delete, as for the API delete.
	t.Run("delete with a clean, run ends at pre-instance-delete", func(t *testing.T) {
		t.Parallel()
		ctx, endRun := context.WithCancel(markedCtx())
		t.Cleanup(endRun)
		srv := newSubscriberServer(t)
		srv.handlers["end-run"] = func(w http.ResponseWriter, _ *http.Request, _ hooks.EventEnvelope) {
			endRun()
			_ = json.NewEncoder(w).Encode(hooks.HookResponse{Allowed: true})
		}
		instances := newMockInstanceRepo()
		inst := seedInstance(t, instances, "inst-end", "end-demo", "owner-1")
		inst.Status = models.StackStatusStopped
		require.NoError(t, instances.Update(context.Background(), inst))
		charts := newMockChartConfigRepo()
		require.NoError(t, charts.Create(&models.ChartConfig{ID: "c1", StackDefinitionID: "def-1", ChartName: "app"}))
		mgr := NewManager(ManagerConfig{
			Registry:      &mockClusterResolver{helm: &mockHelmExecutor{}},
			InstanceRepo:  instances,
			DeployLogRepo: newMockDeployLogRepo(),
			Hooks: srv.dispatcher(t,
				hooks.Subscription{Name: "end-run", Events: []string{hooks.EventPreInstanceDelete}, FailurePolicy: hooks.FailurePolicyIgnore},
				hooks.Subscription{Name: "rec", Events: []string{hooks.EventDeleteCompleted}},
			),
		})
		t.Cleanup(mgr.Shutdown)
		exec := NewCleanupExecutor(mgr, newRecordingDefinitionRepo(), charts, instances)

		require.NoError(t, exec.DeleteInstance(ctx, inst))
		require.ErrorIs(t, ctx.Err(), context.Canceled, "the run ended during pre-instance-delete")
		require.Eventually(t, func() bool {
			_, err := instances.FindByID(context.Background(), inst.ID)
			return err != nil && len(srv.snapshot()) == 2
		}, 5*time.Second, 10*time.Millisecond, "the instance is deleted")
		assert.Equal(t, []string{"end-run:pre-instance-delete", "rec:delete-completed"}, srv.callNames())
	})

	t.Run("cancelled run", func(t *testing.T) {
		t.Parallel()
		defs := newRecordingDefinitionRepo()
		charts := newMockChartConfigRepo()
		charts.items["def-1"] = []models.ChartConfig{{ID: "cc-1", StackDefinitionID: "def-1", ChartName: "app"}}
		exec := &CleanupExecutor{definitionRepo: defs, chartConfigRepo: charts}
		ctx, cancel := context.WithCancel(markedCtx())
		cancel()

		err := exec.StopInstance(ctx, &models.StackInstance{ID: "inst-1", StackDefinitionID: "def-1", Status: models.StackStatusRunning})

		require.ErrorIs(t, err, context.Canceled)
		assert.ErrorContains(t, err, "finding definition")
	})
}

// TestExpiryStopper_DefinitionReadUsesCallerContext checks that the TTL
// reaper reads the definition with its worker context (#656): a worker that
// stops (the end of a leadership term) starts no stop.
func TestExpiryStopper_DefinitionReadUsesCallerContext(t *testing.T) {
	t.Parallel()

	t.Run("worker context", func(t *testing.T) {
		t.Parallel()
		defs := newRecordingDefinitionRepo()
		charts := newMockChartConfigRepo()
		charts.err = errors.New("chart lookup reached")

		err := NewExpiryStopper(nil, defs, charts).StopInstance(markedCtx(), &models.StackInstance{ID: "inst-1", StackDefinitionID: "def-1"})

		require.ErrorContains(t, err, "listing charts")
		assert.Equal(t, []any{"caller"}, defs.findMarks())
	})

	t.Run("stopped worker", func(t *testing.T) {
		t.Parallel()
		instances := newMockInstanceRepo()
		logs := newMockDeployLogRepo()
		inst := &models.StackInstance{ID: "inst-1", Name: "exp", StackDefinitionID: "def-1", Namespace: "stack-exp", Status: models.StackStatusRunning}
		require.NoError(t, instances.Create(context.Background(), inst))
		mgr := NewManager(ManagerConfig{
			Registry:      &mockClusterResolver{helm: &mockHelmExecutor{}, noK8sClient: true},
			InstanceRepo:  instances,
			DeployLogRepo: logs,
		})
		t.Cleanup(mgr.Shutdown)
		charts := newMockChartConfigRepo()
		charts.items["def-1"] = []models.ChartConfig{{ID: "cc-1", StackDefinitionID: "def-1", ChartName: "app"}}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := NewExpiryStopper(mgr, newRecordingDefinitionRepo(), charts).StopInstance(ctx, inst)

		require.ErrorIs(t, err, context.Canceled)
		stored, findErr := instances.FindByID(context.Background(), "inst-1")
		require.NoError(t, findErr)
		assert.Equal(t, models.StackStatusRunning, stored.Status, "no stop started")
		logList, listErr := logs.ListByInstance(context.Background(), "inst-1")
		require.NoError(t, listErr)
		assert.Empty(t, logList, "no deploy log")
	})
}

package deployer

import (
	"context"
	"errors"
	"sync"
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ctxRecordingChartRepo records the marker and the context error of each
// ListByDefinition call. The embedded mock fails on a done context.
type ctxRecordingChartRepo struct {
	*mockChartConfigRepo
	mu    sync.Mutex
	marks []any
	errs  []error
}

func (r *ctxRecordingChartRepo) ListByDefinition(ctx context.Context, defID string) ([]models.ChartConfig, error) {
	r.mu.Lock()
	r.marks = append(r.marks, ctx.Value(callerCtxKey{}))
	r.errs = append(r.errs, ctx.Err())
	r.mu.Unlock()
	return r.mockChartConfigRepo.ListByDefinition(ctx, defID)
}

func (r *ctxRecordingChartRepo) calls() ([]any, []error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]any(nil), r.marks...), append([]error(nil), r.errs...)
}

func newRecordingChartRepo() *ctxRecordingChartRepo {
	return &ctxRecordingChartRepo{mockChartConfigRepo: newMockChartConfigRepo()}
}

// cancelAfterDefinitionRead cancels the run after the definition read, so the
// chart read is the first read with a done context.
type cancelAfterDefinitionRead struct {
	*ctxRecordingDefinitionRepo
	cancel context.CancelFunc
}

func (r *cancelAfterDefinitionRead) FindByID(ctx context.Context, id string) (*models.StackDefinition, error) {
	def, err := r.ctxRecordingDefinitionRepo.FindByID(ctx, id)
	r.cancel()
	return def, err
}

// TestCleanupExecutor_ChartReadUsesCallerContext checks that the cleanup
// policy actions read the charts with the context of the policy run (#656):
// a run that ends before the chart read starts no operation.
func TestCleanupExecutor_ChartReadUsesCallerContext(t *testing.T) {
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
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name+", caller context", func(t *testing.T) {
			t.Parallel()
			charts := newRecordingChartRepo()
			// The chart lookup ends the call.
			charts.err = errors.New("chart lookup reached")
			exec := NewCleanupExecutor(nil, newRecordingDefinitionRepo(), charts, newMockInstanceRepo())

			err := tt.call(markedCtx(), exec, &models.StackInstance{ID: "inst-1", StackDefinitionID: "def-1", Status: models.StackStatusRunning})

			require.ErrorContains(t, err, "listing charts")
			marks, _ := charts.calls()
			assert.Equal(t, []any{"caller"}, marks, "ListByDefinition runs with the context of the policy run")
		})

		t.Run(tt.name+", run ends before the chart read", func(t *testing.T) {
			t.Parallel()
			instances := newMockInstanceRepo()
			logs := newMockDeployLogRepo()
			inst := &models.StackInstance{ID: "inst-1", Name: "end", StackDefinitionID: "def-1", Namespace: "stack-end", Status: models.StackStatusRunning}
			require.NoError(t, instances.Create(context.Background(), inst))
			mgr := NewManager(ManagerConfig{
				Registry:      &mockClusterResolver{helm: &mockHelmExecutor{}, noK8sClient: true},
				InstanceRepo:  instances,
				DeployLogRepo: logs,
			})
			t.Cleanup(mgr.Shutdown)
			charts := newRecordingChartRepo()
			charts.items["def-1"] = []models.ChartConfig{{ID: "cc-1", StackDefinitionID: "def-1", ChartName: "app"}}
			ctx, cancel := context.WithCancel(markedCtx())
			t.Cleanup(cancel)
			defs := &cancelAfterDefinitionRead{ctxRecordingDefinitionRepo: newRecordingDefinitionRepo(), cancel: cancel}
			exec := NewCleanupExecutor(mgr, defs, charts, instances)

			err := tt.call(ctx, exec, inst)

			require.ErrorIs(t, err, context.Canceled)
			assert.ErrorContains(t, err, "listing charts")
			stored, findErr := instances.FindByID(context.Background(), "inst-1")
			require.NoError(t, findErr)
			assert.Equal(t, models.StackStatusRunning, stored.Status, "no operation started")
			logList, listErr := logs.ListByInstance(context.Background(), "inst-1")
			require.NoError(t, listErr)
			assert.Empty(t, logList, "no deploy log")
		})
	}

	t.Run("delete with a clean reads the charts detached", func(t *testing.T) {
		t.Parallel()
		instances := newMockInstanceRepo()
		inst := &models.StackInstance{ID: "inst-del", Name: "del", StackDefinitionID: "def-1", Namespace: "stack-del", Status: models.StackStatusStopped}
		require.NoError(t, instances.Create(context.Background(), inst))
		mgr := NewManager(ManagerConfig{
			Registry:      &mockClusterResolver{helm: &mockHelmExecutor{}, noK8sClient: true},
			InstanceRepo:  instances,
			DeployLogRepo: newMockDeployLogRepo(),
		})
		t.Cleanup(mgr.Shutdown)
		charts := newRecordingChartRepo()
		charts.items["def-1"] = []models.ChartConfig{{ID: "cc-1", StackDefinitionID: "def-1", ChartName: "app"}}
		ctx, cancel := context.WithCancel(markedCtx())
		t.Cleanup(cancel)
		// The run ends after pre-instance-delete: the definition read is the
		// first read after it.
		defs := &cancelAfterDefinitionRead{ctxRecordingDefinitionRepo: newRecordingDefinitionRepo(), cancel: cancel}
		exec := NewCleanupExecutor(mgr, defs, charts, instances)

		require.NoError(t, exec.DeleteInstance(ctx, inst))

		marks, errs := charts.calls()
		assert.Equal(t, []any{"caller"}, marks, "the detached context keeps the values of the run")
		require.Len(t, errs, 1)
		assert.NoError(t, errs[0], "the chart read runs detached from the run")
	})
}

// TestExpiryStopper_ChartReadUsesCallerContext checks that the TTL reaper
// reads the charts with its worker context (#656).
func TestExpiryStopper_ChartReadUsesCallerContext(t *testing.T) {
	t.Parallel()

	t.Run("worker context", func(t *testing.T) {
		t.Parallel()
		charts := newRecordingChartRepo()
		charts.err = errors.New("chart lookup reached")

		err := NewExpiryStopper(nil, newRecordingDefinitionRepo(), charts).StopInstance(markedCtx(), &models.StackInstance{ID: "inst-1", StackDefinitionID: "def-1"})

		require.ErrorContains(t, err, "listing charts")
		marks, _ := charts.calls()
		assert.Equal(t, []any{"caller"}, marks)
	})

	t.Run("worker stops before the chart read", func(t *testing.T) {
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
		charts := newRecordingChartRepo()
		charts.items["def-1"] = []models.ChartConfig{{ID: "cc-1", StackDefinitionID: "def-1", ChartName: "app"}}
		ctx, cancel := context.WithCancel(markedCtx())
		t.Cleanup(cancel)
		defs := &cancelAfterDefinitionRead{ctxRecordingDefinitionRepo: newRecordingDefinitionRepo(), cancel: cancel}

		err := NewExpiryStopper(mgr, defs, charts).StopInstance(ctx, inst)

		require.ErrorIs(t, err, context.Canceled)
		assert.ErrorContains(t, err, "listing charts")
		stored, findErr := instances.FindByID(context.Background(), "inst-1")
		require.NoError(t, findErr)
		assert.Equal(t, models.StackStatusRunning, stored.Status, "no stop started")
		logList, listErr := logs.ListByInstance(context.Background(), "inst-1")
		require.NoError(t, listErr)
		assert.Empty(t, logList, "no deploy log")
	})
}

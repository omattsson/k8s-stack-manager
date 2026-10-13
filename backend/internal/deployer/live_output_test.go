package deployer

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// outputWriterLogRepo adds models.DeploymentLogOutputWriter (with the rules
// of the GORM repository) to mockDeployLogRepo. failFirst makes the first
// write fail with a database error.
type outputWriterLogRepo struct {
	*mockDeployLogRepo
	mu        sync.Mutex
	writes    int
	failFirst bool
}

func (r *outputWriterLogRepo) UpdateRunningOutput(_ context.Context, id, output string) error {
	r.mu.Lock()
	r.writes++
	fail := r.failFirst && r.writes == 1
	r.mu.Unlock()
	if fail {
		return errors.New("connection refused")
	}
	r.mockDeployLogRepo.mu.Lock()
	defer r.mockDeployLogRepo.mu.Unlock()
	cur, ok := r.items[id]
	if !ok || cur.Status != models.DeployLogRunning {
		return models.ErrDeployLogNotRunning
	}
	cur.Output = output
	return nil
}

func (r *outputWriterLogRepo) writeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.writes
}

func (r *outputWriterLogRepo) output(t *testing.T, id string) string {
	t.Helper()
	log, err := r.FindByID(context.Background(), id)
	require.NoError(t, err)
	return log.Output
}

func newLiveOutputManager(t *testing.T, repo models.DeploymentLogRepository) *Manager {
	t.Helper()
	mgr := NewManager(ManagerConfig{DeployLogRepo: repo, MaxConcurrent: 1})
	mgr.liveOutputInterval = 10 * time.Millisecond
	t.Cleanup(mgr.Shutdown)
	return mgr
}

func addLog(t *testing.T, repo *mockDeployLogRepo, id, status, output string) {
	t.Helper()
	require.NoError(t, repo.Create(context.Background(), &models.DeploymentLog{
		ID: id, StackInstanceID: "inst-1", Action: models.DeployActionDeploy, Status: status, Output: output,
	}))
}

func (m *Manager) liveOutputFlushing() bool {
	m.liveOut.mu.Lock()
	defer m.liveOut.mu.Unlock()
	return m.liveOut.flushing
}

func (m *Manager) hasLiveOutput(logID string) bool {
	m.liveOut.mu.Lock()
	defer m.liveOut.mu.Unlock()
	_, ok := m.liveOut.byLog[logID]
	return ok
}

func TestLiveOutput(t *testing.T) {
	t.Parallel()

	const wait, tick = 2 * time.Second, 5 * time.Millisecond

	t.Run("streamed lines reach the running log", func(t *testing.T) {
		t.Parallel()
		repo := &outputWriterLogRepo{mockDeployLogRepo: newMockDeployLogRepo()}
		addLog(t, repo.mockDeployLogRepo, "log-1", models.DeployLogRunning, "")
		mgr := newLiveOutputManager(t, repo)

		mgr.broadcastLog("inst-1", "log-1", "Installing chart a")
		mgr.broadcastLog("inst-1", "log-1", "Installing chart b")

		assert.Eventually(t, func() bool {
			return repo.output(t, "log-1") == "Installing chart a\nInstalling chart b\n"
		}, wait, tick)
	})

	t.Run("a log that is no longer running keeps its final output", func(t *testing.T) {
		t.Parallel()
		repo := &outputWriterLogRepo{mockDeployLogRepo: newMockDeployLogRepo()}
		addLog(t, repo.mockDeployLogRepo, "log-1", models.DeployLogSuccess, "final output\n")
		mgr := newLiveOutputManager(t, repo)

		mgr.broadcastLog("inst-1", "log-1", "late line")

		assert.Eventually(t, func() bool { return !mgr.liveOutputFlushing() }, wait, tick,
			"the flusher stops when no live output is left")
		assert.False(t, mgr.hasLiveOutput("log-1"))
		assert.Equal(t, "final output\n", repo.output(t, "log-1"))
	})

	t.Run("the final write drops the live output", func(t *testing.T) {
		t.Parallel()
		repo := &outputWriterLogRepo{mockDeployLogRepo: newMockDeployLogRepo()}
		addLog(t, repo.mockDeployLogRepo, "log-1", models.DeployLogRunning, "")
		mgr := newLiveOutputManager(t, repo)

		mgr.broadcastLog("inst-1", "log-1", "line")
		require.True(t, mgr.hasLiveOutput("log-1"))
		final := &models.DeploymentLog{ID: "log-1", StackInstanceID: "inst-1", Status: models.DeployLogSuccess, Output: "full output\n"}
		assert.True(t, mgr.writeFinal(nil, final, "deploy"))

		assert.False(t, mgr.hasLiveOutput("log-1"))
		assert.Eventually(t, func() bool { return !mgr.liveOutputFlushing() }, wait, tick)
		assert.Equal(t, "full output\n", repo.output(t, "log-1"))
	})

	t.Run("a failed write is tried again", func(t *testing.T) {
		t.Parallel()
		repo := &outputWriterLogRepo{mockDeployLogRepo: newMockDeployLogRepo(), failFirst: true}
		addLog(t, repo.mockDeployLogRepo, "log-1", models.DeployLogRunning, "")
		mgr := newLiveOutputManager(t, repo)

		mgr.broadcastLog("inst-1", "log-1", "line")

		assert.Eventually(t, func() bool { return repo.output(t, "log-1") == "line\n" }, wait, tick)
		assert.GreaterOrEqual(t, repo.writeCount(), 2)
	})

	t.Run("the live output stops at maxOutputLen", func(t *testing.T) {
		t.Parallel()
		repo := &outputWriterLogRepo{mockDeployLogRepo: newMockDeployLogRepo()}
		addLog(t, repo.mockDeployLogRepo, "log-1", models.DeployLogRunning, "")
		mgr := newLiveOutputManager(t, repo)

		line := strings.Repeat("x", 1023)
		for i := 0; i < 70; i++ { // 70 KiB of lines
			mgr.broadcastLog("inst-1", "log-1", line)
		}

		assert.Eventually(t, func() bool { return len(repo.output(t, "log-1")) == 64*1024 }, wait, tick,
			"64 whole lines of 1 KiB")
	})

	t.Run("a repository without the output writer keeps nothing", func(t *testing.T) {
		t.Parallel()
		repo := newMockDeployLogRepo()
		addLog(t, repo, "log-1", models.DeployLogRunning, "")
		mgr := newLiveOutputManager(t, repo)

		mgr.broadcastLog("inst-1", "log-1", "line")

		assert.False(t, mgr.hasLiveOutput("log-1"))
		assert.False(t, mgr.liveOutputFlushing())
	})

	t.Run("a line without a log ID is ignored", func(t *testing.T) {
		t.Parallel()
		repo := &outputWriterLogRepo{mockDeployLogRepo: newMockDeployLogRepo()}
		mgr := newLiveOutputManager(t, repo)

		mgr.broadcastLog("inst-1", "", "line")

		assert.False(t, mgr.liveOutputFlushing())
	})
}

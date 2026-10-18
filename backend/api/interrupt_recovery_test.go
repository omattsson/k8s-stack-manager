package main

import (
	"context"
	"regexp"
	"testing"
	"time"

	"backend/internal/health"
	"backend/internal/models"
	"backend/internal/sessionstore"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubHeartbeatRepo struct{}

func (stubHeartbeatRepo) Beat(context.Context, string) error { return nil }
func (stubHeartbeatRepo) SeenWithin(context.Context, []string, time.Duration) (map[string]bool, error) {
	return map[string]bool{}, nil
}
func (stubHeartbeatRepo) Remove(context.Context, string) error { return nil }
func (stubHeartbeatRepo) DeleteOlderThan(context.Context, time.Duration) (int64, error) {
	return 0, nil
}

type stubInterruptRepo struct{}

func (stubInterruptRepo) ListInterruptCandidates(context.Context, time.Time, string, time.Duration, int) ([]models.DeploymentLog, error) {
	return nil, nil
}
func (stubInterruptRepo) InterruptOperation(context.Context, models.InterruptRequest) (models.InterruptResult, error) {
	return models.InterruptResult{}, nil
}

func TestBuildLeaderWorkers_InterruptRecovery(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		withRepos  bool
		wantWorker bool
	}{
		{name: "repositories present: recovery worker and heartbeat", withRepos: true, wantWorker: true},
		{name: "repositories missing: no recovery worker", withRepos: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := buildTestConfig()
			cfg.LeaderElection.Identity = "pod-a"
			repos := buildTestRepositorySet()
			if tt.withRepos {
				repos.ReplicaHeartbeat = stubHeartbeatRepo{}
				repos.InterruptedOperation = stubInterruptRepo{}
			}
			hub := buildTestHub(t)

			svc, err := buildDomainServices(cfg, repos, hub, health.New())
			require.NoError(t, err)
			assert.Regexp(t, regexp.MustCompile(`^pod-a-[0-9a-f]{8}$`), svc.ReplicaID, "identity plus a per-process suffix")
			sessStore := sessionstore.NewMemoryStore()
			t.Cleanup(func() { sessStore.Stop() })
			hs, err := buildHandlers(cfg, repos, svc, sessStore, hub)
			require.NoError(t, err)

			lw := buildLeaderWorkers(svc, hs, repos, hub, time.Second)
			if !tt.wantWorker {
				assert.Nil(t, svc.Heartbeat)
				assert.Nil(t, svc.InterruptRecovery)
				assert.NotContains(t, lw.Group.Names(), "interrupted-operation-recovery")
				return
			}
			require.NotNil(t, svc.Heartbeat)
			assert.Equal(t, svc.ReplicaID, svc.Heartbeat.ID(), "deploy logs and the heartbeat use one identity")
			require.NotNil(t, svc.InterruptRecovery)
			assert.Contains(t, lw.Group.Names(), "interrupted-operation-recovery")
		})
	}
}

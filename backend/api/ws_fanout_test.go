package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/config"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingWSEventRepo counts the calls of each method.
type countingWSEventRepo struct {
	calls map[string]int
	mu    sync.Mutex
}

func (r *countingWSEventRepo) inc(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.calls == nil {
		r.calls = map[string]int{}
	}
	r.calls[name]++
}

func (r *countingWSEventRepo) total() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.calls {
		n += c
	}
	return n
}

func (r *countingWSEventRepo) count(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[name]
}

func (r *countingWSEventRepo) Insert(context.Context, []*models.WSEvent) error {
	r.inc("Insert")
	return nil
}

func (r *countingWSEventRepo) MaxID(context.Context) (int64, error) {
	r.inc("MaxID")
	return 0, nil
}

func (r *countingWSEventRepo) ListAfter(context.Context, int64, int, string) ([]models.WSEvent, error) {
	r.inc("ListAfter")
	return nil, nil
}

func (r *countingWSEventRepo) ListByIDs(context.Context, []int64, string) ([]models.WSEvent, error) {
	r.inc("ListByIDs")
	return nil, nil
}

func (r *countingWSEventRepo) IDsAfter(context.Context, int64, int) ([]int64, error) {
	r.inc("IDsAfter")
	return nil, nil
}

func (r *countingWSEventRepo) DeleteOlderThan(context.Context, time.Time, int) (int64, error) {
	r.inc("DeleteOlderThan")
	return 0, nil
}

func TestWSFanoutOrigin(t *testing.T) {
	t.Parallel()
	a, b := wsFanoutOrigin("pod-a"), wsFanoutOrigin("pod-a")
	assert.NotEqual(t, a, b, "two processes with one host name get different origins")
	long := wsFanoutOrigin(strings.Repeat("x", 300))
	assert.LessOrEqual(t, len(long), 253, "fits the origin column")
}

func TestBuildWSFanout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		fanout     config.WSFanoutConfig
		identity   string
		wantFanout bool
	}{
		{name: "disabled: no fan-out, no writes", fanout: config.WSFanoutConfig{}, identity: "pod-a"},
		{
			name:       "enabled",
			fanout:     config.WSFanoutConfig{Enabled: true, PollInterval: 10 * time.Millisecond, Retention: 5 * time.Minute},
			identity:   "pod-a",
			wantFanout: true,
		},
		{
			name:     "enabled without identity: no fan-out",
			fanout:   config.WSFanoutConfig{Enabled: true, PollInterval: 10 * time.Millisecond, Retention: 5 * time.Minute},
			identity: "",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := buildTestConfig()
			cfg.WSFanout = tt.fanout
			cfg.LeaderElection.Identity = tt.identity
			hub := buildTestHub(t)
			repo := &countingWSEventRepo{}

			fanout, worker := buildWSFanout(cfg, hub, repo)
			fanout.Start()
			t.Cleanup(fanout.Stop)

			hub.Broadcast([]byte("all"))
			hub.BroadcastToInstance("inst-1", []byte("inst"))
			hub.BroadcastToUser("u1", []byte("user"))

			if !tt.wantFanout {
				assert.Nil(t, fanout)
				assert.Nil(t, worker, "no cleanup worker")
				time.Sleep(50 * time.Millisecond)
				assert.Zero(t, repo.total(), "disabled fan-out must not touch ws_events")
				return
			}
			require.NotNil(t, fanout)
			require.NotNil(t, worker)
			assert.Equal(t, "ws-event-cleanup", worker.Name)
			assert.Regexp(t, "^"+tt.identity+"-[0-9a-f]{8}$", fanout.Origin(), "identity plus a per-process suffix")
			assert.Equal(t, tt.identity, cfg.LeaderElection.Identity, "the leader identity does not change")
			require.Eventually(t, func() bool { return fanout.Stats().Written == 3 }, 2*time.Second, 5*time.Millisecond)
			require.Eventually(t, func() bool { return repo.count("ListAfter") > 0 }, 2*time.Second, 5*time.Millisecond)

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); worker.Run(ctx) }()
			require.Eventually(t, func() bool { return repo.count("DeleteOlderThan") == 1 }, time.Second, 5*time.Millisecond)
			cancel()
			<-done
		})
	}
}

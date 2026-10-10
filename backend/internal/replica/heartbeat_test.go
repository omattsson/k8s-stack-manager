package replica

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeRepo struct {
	mu      sync.Mutex
	beats   map[string][]time.Time
	removed []string
	failN   int // the first failN beats fail
}

func newFakeRepo() *fakeRepo { return &fakeRepo{beats: map[string][]time.Time{}} }

func (f *fakeRepo) Beat(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failN > 0 {
		f.failN--
		return errors.New("db down")
	}
	f.beats[id] = append(f.beats[id], time.Now())
	return nil
}

func (f *fakeRepo) SeenWithin(context.Context, []string, time.Duration) (map[string]bool, error) {
	return nil, nil
}

func (f *fakeRepo) Remove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, id)
	return nil
}

func (f *fakeRepo) DeleteOlderThan(context.Context, time.Duration) (int64, error) { return 0, nil }

func (f *fakeRepo) count(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.beats[id])
}

func TestProcessID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		identity string
		prefix   string
	}{
		{name: "pod name", identity: "backend-7d9f-abc12", prefix: "backend-7d9f-abc12-"},
		{name: "empty identity uses a fallback", identity: "", prefix: "backend-"},
		{name: "long identity is cut", identity: strings.Repeat("x", 300), prefix: strings.Repeat("x", 244) + "-"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, b := ProcessID(tt.identity), ProcessID(tt.identity)
			assert.NotEqual(t, a, b, "two processes with one host name get different IDs")
			assert.True(t, strings.HasPrefix(a, tt.prefix), a)
			assert.Len(t, a, len(tt.prefix)+8)
			assert.LessOrEqual(t, len(a), 253)
		})
	}
}

func TestHeartbeat_StartBeatsAndStopRemovesRow(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	repo.failN = 1 // a failed write does not stop the heartbeat
	h := NewHeartbeat(repo, "pod-a-1", 10*time.Millisecond)
	require.NotNil(t, h)
	assert.Equal(t, "pod-a-1", h.ID())

	h.Start()
	h.Start() // a second start does nothing
	require.Eventually(t, func() bool { return repo.count("pod-a-1") >= 3 }, 2*time.Second, 5*time.Millisecond)

	h.Stop(time.Second)
	h.Stop(time.Second) // a second stop does nothing
	n := repo.count("pod-a-1")
	time.Sleep(30 * time.Millisecond)
	assert.Equal(t, n, repo.count("pod-a-1"), "no beats after Stop")
	repo.mu.Lock()
	assert.Equal(t, []string{"pod-a-1"}, repo.removed, "Stop deletes the row once")
	repo.mu.Unlock()
}

func TestHeartbeat_NilIsSafe(t *testing.T) {
	t.Parallel()
	assert.Nil(t, NewHeartbeat(nil, "id", 0))
	assert.Nil(t, NewHeartbeat(newFakeRepo(), "", 0))
	var h *Heartbeat
	h.Start()
	h.Stop(time.Second)
	assert.Empty(t, h.ID())

	d := NewHeartbeat(newFakeRepo(), "id", 0)
	require.NotNil(t, d)
	assert.Equal(t, HeartbeatInterval, d.interval)
}

package leader

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// Short timings for the tests. The Lease stores the duration in whole
// seconds, so the lease duration must be at least 1s.
const (
	testLeaseDuration = 2 * time.Second
	testRenewDeadline = 1 * time.Second
	testRetryPeriod   = 200 * time.Millisecond
)

func testConfig(identity string) Config {
	return Config{
		Enabled:       true,
		LeaseName:     "test-workers",
		Namespace:     "test-ns",
		Identity:      identity,
		LeaseDuration: testLeaseDuration,
		RenewDeadline: testRenewDeadline,
		RetryPeriod:   testRetryPeriod,
	}
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	valid := testConfig("pod-a")
	tests := []struct {
		name    string
		mutate  func(c *Config)
		wantErr string
	}{
		{name: "valid", mutate: func(*Config) {}},
		{name: "disabled skips checks", mutate: func(c *Config) { *c = Config{} }},
		{name: "missing lease name", mutate: func(c *Config) { c.LeaseName = "" }, wantErr: "lease name"},
		{name: "missing namespace", mutate: func(c *Config) { c.Namespace = "" }, wantErr: "namespace"},
		{name: "missing identity", mutate: func(c *Config) { c.Identity = "" }, wantErr: "identity"},
		{name: "lease below one second", mutate: func(c *Config) { c.LeaseDuration = 500 * time.Millisecond }, wantErr: "at least 1s"},
		{name: "zero retry period", mutate: func(c *Config) { c.RetryPeriod = 0 }, wantErr: "greater than zero"},
		{name: "lease not above renew", mutate: func(c *Config) { c.RenewDeadline = c.LeaseDuration }, wantErr: "greater than renew deadline"},
		{name: "renew not above retry x jitter", mutate: func(c *Config) { c.RetryPeriod = c.RenewDeadline }, wantErr: "retry period"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := valid
			tt.mutate(&c)
			err := c.Validate()
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		client  *fake.Clientset
		name    string
		cfg     Config
		wantErr bool
	}{
		{name: "disabled without client", cfg: Config{}},
		{name: "enabled with client", cfg: testConfig("pod-a"), client: fake.NewSimpleClientset()},
		{name: "enabled without client", cfg: testConfig("pod-a"), wantErr: true},
		{name: "enabled with invalid config", cfg: Config{Enabled: true}, client: fake.NewSimpleClientset(), wantErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var err error
			if tt.client != nil {
				_, err = New(tt.cfg, tt.client)
			} else {
				_, err = New(tt.cfg, nil)
			}
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestNewInCluster_EnabledOutsideCluster(t *testing.T) {
	// Not parallel: it changes the environment.
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")

	_, err := NewInCluster(testConfig("pod-a"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "LEADER_ELECTION_ENABLED=true")

	e, err := NewInCluster(Config{})
	require.NoError(t, err)
	assert.False(t, e.Enabled())
}

// recorder counts terms and records start and stop times.
type recorder struct {
	mu      sync.Mutex
	starts  []time.Time
	stops   []time.Time
	running atomic.Int32
}

func (r *recorder) worker() Worker {
	return Worker{Name: "test", Run: func(ctx context.Context) {
		r.running.Add(1)
		r.mu.Lock()
		r.starts = append(r.starts, time.Now())
		r.mu.Unlock()
		<-ctx.Done()
		r.mu.Lock()
		r.stops = append(r.stops, time.Now())
		r.mu.Unlock()
		r.running.Add(-1)
	}}
}

func (r *recorder) counts() (starts, stops int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.starts), len(r.stops)
}

func (r *recorder) lastStop() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stops[len(r.stops)-1]
}

func (r *recorder) firstStart() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts[0]
}

// candidate is one replica in a test: an Elector, a worker group and a
// recorder.
type candidate struct {
	elector *Elector
	group   *Group
	rec     *recorder
	cancel  context.CancelFunc
	done    chan struct{}
}

func startCandidate(t *testing.T, identity string, client *fake.Clientset) *candidate {
	t.Helper()
	e, err := New(testConfig(identity), client)
	require.NoError(t, err)
	rec := &recorder{}
	g := NewGroup(5*time.Second, rec.worker())
	ctx, cancel := context.WithCancel(context.Background())
	c := &candidate{elector: e, group: g, rec: rec, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(c.done)
		e.Run(ctx, g.Start, g.Stop)
	}()
	t.Cleanup(c.shutdown)
	return c
}

func (c *candidate) shutdown() {
	c.cancel()
	<-c.done
}

// sharedClients returns two fake clientsets on one object store. The writes
// of the first one fail while *blocked is true (an API server outage seen
// by that replica only).
func sharedClients(blocked *atomic.Bool) (*fake.Clientset, *fake.Clientset) {
	a := fake.NewSimpleClientset()
	failWrites := func(k8stesting.Action) (bool, runtime.Object, error) {
		if blocked.Load() {
			return true, nil, errors.New("api server unavailable")
		}
		return false, nil, nil
	}
	a.PrependReactor("update", "leases", failWrites)
	a.PrependReactor("create", "leases", failWrites)
	a.PrependReactor("get", "leases", failWrites)

	b := &fake.Clientset{}
	b.AddReactor("*", "*", k8stesting.ObjectReaction(a.Tracker()))
	return a, b
}

func TestElectorDisabled_AlwaysLeader(t *testing.T) {
	t.Parallel()

	e, err := New(Config{}, nil)
	require.NoError(t, err)
	rec := &recorder{}
	g := NewGroup(time.Second, rec.worker())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.Run(ctx, g.Start, g.Stop)
	}()

	require.Eventually(t, func() bool { return e.IsLeader() && rec.running.Load() == 1 },
		2*time.Second, 10*time.Millisecond)

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	assert.False(t, e.IsLeader())
	assert.Equal(t, int32(0), rec.running.Load(), "workers must be stopped when Run returns")
	starts, stops := rec.counts()
	assert.Equal(t, 1, starts)
	assert.Equal(t, 1, stops)
}

func TestElector_TwoCandidatesOneLeader(t *testing.T) {
	t.Parallel()

	var blocked atomic.Bool
	clientA, clientB := sharedClients(&blocked)
	a := startCandidate(t, "pod-a", clientA)
	b := startCandidate(t, "pod-b", clientB)

	require.Eventually(t, func() bool {
		return a.elector.IsLeader() != b.elector.IsLeader()
	}, 5*time.Second, 20*time.Millisecond, "one candidate must become the leader")

	// For a few renew periods, exactly one candidate leads and runs workers.
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		require.False(t, a.elector.IsLeader() && b.elector.IsLeader(), "two leaders at the same time")
		require.LessOrEqual(t, a.rec.running.Load()+b.rec.running.Load(), int32(1), "workers run in two replicas")
		time.Sleep(20 * time.Millisecond)
	}
	assert.Equal(t, int32(1), a.rec.running.Load()+b.rec.running.Load())
}

func TestElector_LeaseLossStopsWorkersAndOtherTakesOver(t *testing.T) {
	t.Parallel()

	var blocked atomic.Bool
	clientA, clientB := sharedClients(&blocked)

	a := startCandidate(t, "pod-a", clientA)
	require.Eventually(t, func() bool { return a.elector.IsLeader() && a.rec.running.Load() == 1 },
		5*time.Second, 20*time.Millisecond)

	b := startCandidate(t, "pod-b", clientB)
	// b sees a valid lease held by a and stays a follower.
	time.Sleep(500 * time.Millisecond)
	require.False(t, b.elector.IsLeader())

	// a cannot reach the API server any more.
	blocked.Store(true)

	require.Eventually(t, func() bool { return !a.elector.IsLeader() && a.rec.running.Load() == 0 },
		5*time.Second, 20*time.Millisecond, "a must stop its workers after the renew deadline")
	require.Eventually(t, func() bool { return b.elector.IsLeader() && b.rec.running.Load() == 1 },
		5*time.Second, 20*time.Millisecond, "b must take over")

	// No overlap: a stopped its workers before b started its workers.
	assert.True(t, a.rec.lastStop().Before(b.rec.firstStart()),
		"a stopped at %v, b started at %v", a.rec.lastStop(), b.rec.firstStart())

	// a campaigns again: when the API server is back and b goes away, a
	// becomes the leader again and restarts its workers.
	blocked.Store(false)
	b.shutdown()
	require.Eventually(t, func() bool { return a.elector.IsLeader() && a.rec.running.Load() == 1 },
		5*time.Second, 20*time.Millisecond, "a must lead again")
	starts, stops := a.rec.counts()
	assert.Equal(t, 2, starts, "a must start its workers a second time")
	assert.Equal(t, 1, stops)
}

func TestElector_ReleaseOnCancelHandsOverFast(t *testing.T) {
	t.Parallel()

	var blocked atomic.Bool
	clientA, clientB := sharedClients(&blocked)

	a := startCandidate(t, "pod-a", clientA)
	require.Eventually(t, a.elector.IsLeader, 5*time.Second, 20*time.Millisecond)
	b := startCandidate(t, "pod-b", clientB)
	time.Sleep(300 * time.Millisecond)
	require.False(t, b.elector.IsLeader())

	// Graceful shutdown of a: stop the workers first, then release the lease.
	require.True(t, a.group.Close(time.Second))
	released := time.Now()
	a.shutdown()
	assert.Equal(t, int32(0), a.rec.running.Load())

	require.Eventually(t, b.elector.IsLeader, 5*time.Second, 20*time.Millisecond)
	// Without release, b waits a full lease duration after the last renewal.
	assert.Less(t, time.Since(released), testLeaseDuration,
		"b must take over before the lease expires")
}

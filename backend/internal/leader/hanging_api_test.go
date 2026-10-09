package leader

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	coordinationv1client "k8s.io/client-go/kubernetes/typed/coordination/v1"
	k8stesting "k8s.io/client-go/testing"
)

// hangingClient is a fake clientset whose lease renewals by one identity
// hang until the request context is done, like an API server that does not
// answer. Other lease writes (for example the release) work.
type hangingClient struct {
	*fake.Clientset
	hang     *atomic.Bool
	identity string
}

func (c *hangingClient) CoordinationV1() coordinationv1client.CoordinationV1Interface {
	return &hangingCoordination{CoordinationV1Interface: c.Clientset.CoordinationV1(), c: c}
}

type hangingCoordination struct {
	coordinationv1client.CoordinationV1Interface
	c *hangingClient
}

func (h *hangingCoordination) Leases(namespace string) coordinationv1client.LeaseInterface {
	return &hangingLeases{LeaseInterface: h.CoordinationV1Interface.Leases(namespace), c: h.c}
}

type hangingLeases struct {
	coordinationv1client.LeaseInterface
	c *hangingClient
}

func (l *hangingLeases) Update(ctx context.Context, lease *coordinationv1.Lease, opts metav1.UpdateOptions) (*coordinationv1.Lease, error) {
	if l.c.hang.Load() && lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity == l.c.identity {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return l.LeaseInterface.Update(ctx, lease, opts)
}

// slowRecorder records term starts and stops; a worker needs stopDelay to
// stop after its context is done.
type slowRecorder struct {
	recorder
	stopDelay time.Duration
}

func (r *slowRecorder) worker() Worker {
	return Worker{Name: "slow", Run: func(ctx context.Context) {
		r.running.Add(1)
		r.mu.Lock()
		r.starts = append(r.starts, time.Now())
		r.mu.Unlock()
		<-ctx.Done()
		time.Sleep(r.stopDelay)
		r.mu.Lock()
		r.stops = append(r.stops, time.Now())
		r.mu.Unlock()
		r.running.Add(-1)
	}}
}

func startSlowCandidate(t *testing.T, identity string, client kubernetes.Interface, stopDelay time.Duration) (*Elector, *slowRecorder) {
	t.Helper()
	e, err := New(testConfig(identity), client)
	require.NoError(t, err)
	rec := &slowRecorder{stopDelay: stopDelay}
	g := NewGroup(5*time.Second, rec.worker())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.Run(ctx, g.Start, g.Stop)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return e, rec
}

// TestElector_HangingAPIReleasesOnlyAfterWorkersStopped: the renewals of the
// leader hang, its workers need time to stop. The lease must be released
// only after the workers stopped, so the next leader never overlaps.
func TestElector_HangingAPIReleasesOnlyAfterWorkersStopped(t *testing.T) {
	t.Parallel()

	var hang atomic.Bool
	base := fake.NewSimpleClientset()
	clientA := &hangingClient{Clientset: base, hang: &hang, identity: "pod-a"}
	clientB := &fake.Clientset{}
	clientB.AddReactor("*", "*", k8stesting.ObjectReaction(base.Tracker()))

	// The workers need less than lease duration - renew deadline to stop
	// (the margin the watchdog leaves), but more than one retry period.
	const stopDelay = 500 * time.Millisecond
	a, recA := startSlowCandidate(t, "pod-a", clientA, stopDelay)
	require.Eventually(t, func() bool { return a.IsLeader() && recA.running.Load() == 1 },
		5*time.Second, 20*time.Millisecond)
	b, recB := startSlowCandidate(t, "pod-b", clientB, 0)
	time.Sleep(300 * time.Millisecond)
	require.False(t, b.IsLeader())

	hang.Store(true)

	require.Eventually(t, func() bool { return b.IsLeader() && recB.running.Load() == 1 },
		10*time.Second, 20*time.Millisecond, "b must take over")
	require.Eventually(t, func() bool { _, stops := recA.counts(); return stops == 1 },
		5*time.Second, 20*time.Millisecond)
	assert.True(t, recA.lastStop().Before(recB.firstStart()),
		"a stopped its workers at %v, after b started at %v", recA.lastStop(), recB.firstStart())
}

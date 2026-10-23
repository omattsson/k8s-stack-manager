package handlers

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/singleflight"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"backend/internal/k8s"
)

// blockingStatusClient returns a client whose namespace GET waits for release
// and counts the calls.
func blockingStatusClient(t *testing.T, gets *atomic.Int32, release <-chan struct{}) *k8s.Client {
	t.Helper()
	cs := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-a"}})
	cs.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		gets.Add(1)
		<-release
		return false, nil, nil
	})
	return k8s.NewClientFromInterface(cs)
}

func TestNamespaceStatus_ConcurrentReadsShareOneRead(t *testing.T) {
	t.Parallel()

	var gets atomic.Int32
	release := make(chan struct{})
	client := blockingStatusClient(t, &gets, release)
	h := &InstanceHandler{}
	opts := k8s.StatusOptions{}

	first := make(chan error, 1)
	go func() {
		_, err := h.namespaceStatus(context.Background(), client, "cl-1", "stack-a", opts)
		first <- err
	}()
	require.Eventually(t, func() bool { return gets.Load() == 1 }, 5*time.Second, 5*time.Millisecond)

	// DoChan registers a waiter before it returns, so these join the read in
	// flight without timing assumptions.
	key := namespaceStatusKey("cl-1", "stack-a", opts)
	joiners := make([]<-chan singleflight.Result, 2)
	for i := range joiners {
		joiners[i] = h.statusFlight.DoChan(key, func() (interface{}, error) {
			t.Error("a joiner must not start a second read")
			return nil, nil
		})
	}
	close(release)

	require.NoError(t, <-first)
	for _, ch := range joiners {
		res := <-ch
		require.NoError(t, res.Err)
		assert.True(t, res.Shared)
		assert.Equal(t, "stack-a", res.Val.(*k8s.NamespaceStatus).Namespace)
	}
	assert.Equal(t, int32(1), gets.Load())
}

func TestNamespaceStatus_PanicBecomesError(t *testing.T) {
	t.Parallel()

	cs := fake.NewSimpleClientset()
	cs.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		panic("boom")
	})
	h := &InstanceHandler{}

	_, err := h.namespaceStatus(context.Background(), k8s.NewClientFromInterface(cs), "cl-1", "stack-a", k8s.StatusOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "panic")
}

func TestNamespaceStatus_CancelledCallerStartsNoRead(t *testing.T) {
	t.Parallel()

	var gets atomic.Int32
	release := make(chan struct{})
	close(release)
	client := blockingStatusClient(t, &gets, release)
	h := &InstanceHandler{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := h.namespaceStatus(ctx, client, "cl-1", "stack-a", k8s.StatusOptions{})
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, int32(0), gets.Load())
}

func TestNamespaceStatus_CallerDeadlineDoesNotFailOthers(t *testing.T) {
	t.Parallel()

	var gets atomic.Int32
	release := make(chan struct{})
	client := blockingStatusClient(t, &gets, release)
	h := &InstanceHandler{}

	done := make(chan error, 1)
	var other *k8s.NamespaceStatus
	go func() {
		var err error
		other, err = h.namespaceStatus(context.Background(), client, "cl-1", "stack-a", k8s.StatusOptions{})
		done <- err
	}()
	require.Eventually(t, func() bool { return gets.Load() == 1 }, 5*time.Second, 5*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := h.namespaceStatus(ctx, client, "cl-1", "stack-a", k8s.StatusOptions{})
	assert.ErrorIs(t, err, context.Canceled)

	close(release)
	require.NoError(t, <-done)
	assert.Equal(t, "stack-a", other.Namespace)
}

// Each key gets its own client: a read shared across keys would leave one of
// the clients without a GET. (The fake clientset runs reactors one at a time,
// so one client cannot show parallel reads.)
func TestNamespaceStatus_KeySeparatesClusterAndEvents(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	h := &InstanceHandler{}
	cases := []struct {
		cluster string
		opts    k8s.StatusOptions
		gets    atomic.Int32
	}{{cluster: "cl-1"}, {cluster: "cl-2"}, {cluster: "cl-1", opts: k8s.StatusOptions{IncludeEvents: true}}}

	var wg sync.WaitGroup
	for i := range cases {
		tc := &cases[i]
		client := blockingStatusClient(t, &tc.gets, release)
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := h.namespaceStatus(context.Background(), client, tc.cluster, "stack-a", tc.opts)
			assert.NoError(t, err)
		}()
	}
	require.Eventually(t, func() bool {
		for i := range cases {
			if cases[i].gets.Load() != 1 {
				return false
			}
		}
		return true
	}, 5*time.Second, 5*time.Millisecond)
	close(release)
	wg.Wait()
}

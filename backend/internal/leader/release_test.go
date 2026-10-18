package leader

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

func leaseHolder(t *testing.T, client *fake.Clientset) string {
	t.Helper()
	lease, err := client.CoordinationV1().Leases("test-ns").Get(context.Background(), "test-workers", metav1.GetOptions{})
	require.NoError(t, err)
	if lease.Spec.HolderIdentity == nil {
		return ""
	}
	return *lease.Spec.HolderIdentity
}

func TestElector_NoReleaseWhenWorkersDoNotStop(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		stuck      bool
		wantHolder string
	}{
		{name: "workers stop: lease released", stuck: false, wantHolder: ""},
		{name: "workers do not stop in time: lease kept", stuck: true, wantHolder: "pod-a"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := fake.NewSimpleClientset()
			e, err := New(testConfig("pod-a"), client)
			require.NoError(t, err)

			release := make(chan struct{})
			defer close(release)
			worker := Worker{Name: "w", Run: func(ctx context.Context) {
				<-ctx.Done()
				if tt.stuck {
					<-release
				}
			}}
			g := NewGroup(100*time.Millisecond, worker)

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				defer close(done)
				e.Run(ctx, g.Start, g.Stop)
			}()
			require.Eventually(t, e.IsLeader, 5*time.Second, 20*time.Millisecond)

			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("Run did not return")
			}
			assert.Equal(t, tt.wantHolder, leaseHolder(t, client))
		})
	}
}

func TestElector_ReleaseConflict(t *testing.T) {
	t.Parallel()

	client := fake.NewSimpleClientset()
	e, err := New(testConfig("pod-a"), client)
	require.NoError(t, err)
	lock := &resourcelock.LeaseLock{
		LeaseMeta:  metav1.ObjectMeta{Name: "test-workers", Namespace: "test-ns"},
		Client:     client.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{Identity: "pod-a"},
	}
	now := metav1.NewTime(time.Now())
	require.NoError(t, lock.Create(context.Background(), resourcelock.LeaderElectionRecord{
		HolderIdentity: "pod-a", LeaseDurationSeconds: 2, AcquireTime: now, RenewTime: now,
	}))

	// Another replica takes the lease between the read and the release
	// update: the API server answers Conflict.
	client.PrependReactor("update", "leases", func(action k8stesting.Action) (bool, runtime.Object, error) {
		lease := action.(k8stesting.UpdateAction).GetObject().(*coordinationv1.Lease)
		if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity == "" {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "coordination.k8s.io", Resource: "leases"},
				"test-workers", nil)
		}
		return false, nil, nil
	})

	assert.Equal(t, releaseConflict, e.release(lock))
	assert.Equal(t, "pod-a", leaseHolder(t, client), "a conflicting release must not change the lease")
}

func TestElector_ReleaseOutcomes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		holder string
		want   releaseOutcome
	}{
		{name: "holder releases", holder: "pod-a", want: releaseDone},
		{name: "other holder is kept", holder: "pod-b", want: releaseNotHolder},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := fake.NewSimpleClientset()
			e, err := New(testConfig("pod-a"), client)
			require.NoError(t, err)
			lock := &resourcelock.LeaseLock{
				LeaseMeta:  metav1.ObjectMeta{Name: "test-workers", Namespace: "test-ns"},
				Client:     client.CoordinationV1(),
				LockConfig: resourcelock.ResourceLockConfig{Identity: "pod-a"},
			}
			now := metav1.NewTime(time.Now())
			require.NoError(t, lock.Create(context.Background(), resourcelock.LeaderElectionRecord{
				HolderIdentity: tt.holder, LeaseDurationSeconds: 2, AcquireTime: now, RenewTime: now,
			}))
			assert.Equal(t, tt.want, e.release(lock))
		})
	}
}

package k8s

import (
	"context"
	"sync"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

type pollCtxKey struct{}

// ctxMarks records the marker of the context of each repository call.
type ctxMarks struct {
	mu   sync.Mutex
	seen map[string]any
}

func (c *ctxMarks) record(ctx context.Context, method string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen == nil {
		c.seen = map[string]any{}
	}
	c.seen[method] = ctx.Value(pollCtxKey{})
}

func (c *ctxMarks) get(method string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.seen[method]
	return v, ok
}

// recordingRepo is mockInstanceRepo without InstanceErrorMarker.
type recordingRepo struct {
	*mockInstanceRepo
	marks ctxMarks
}

func (r *recordingRepo) List(ctx context.Context) ([]models.StackInstance, error) {
	r.marks.record(ctx, "List")
	return r.mockInstanceRepo.List(ctx)
}

func (r *recordingRepo) Update(ctx context.Context, inst *models.StackInstance) error {
	r.marks.record(ctx, "Update")
	return r.mockInstanceRepo.Update(ctx, inst)
}

// recordingMarkerRepo is markerInstanceRepo (with InstanceErrorMarker).
type recordingMarkerRepo struct {
	*markerInstanceRepo
	marks ctxMarks
}

func (r *recordingMarkerRepo) List(ctx context.Context) ([]models.StackInstance, error) {
	r.marks.record(ctx, "List")
	return r.markerInstanceRepo.List(ctx)
}

func (r *recordingMarkerRepo) MarkNamespaceError(ctx context.Context, id, message string, now time.Time) (bool, error) {
	r.marks.record(ctx, "MarkNamespaceError")
	return r.markerInstanceRepo.MarkNamespaceError(ctx, id, message, now)
}

// TestWatcherPoll_UsesPollContext checks that the status watcher lists the
// instances and writes the error status with the context of the poll (the
// leadership term of the worker), not a background context (#656).
func TestWatcherPoll_UsesPollContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		write string
		repo  func() (models.StackInstanceRepository, *ctxMarks, func() string)
	}{
		{name: "plain update", write: "Update", repo: func() (models.StackInstanceRepository, *ctxMarks, func() string) {
			r := &recordingRepo{mockInstanceRepo: newMockInstanceRepo()}
			return r, &r.marks, func() string { return r.getStatus("inst-poll") }
		}},
		{name: "conditional update", write: "MarkNamespaceError", repo: func() (models.StackInstanceRepository, *ctxMarks, func() string) {
			r := &recordingMarkerRepo{markerInstanceRepo: &markerInstanceRepo{newMockInstanceRepo()}}
			return r, &r.marks, func() string { return r.getStatus("inst-poll") }
		}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ns := "ns-poll"
			cs := fake.NewSimpleClientset(
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "failed-pod", Namespace: ns, Labels: map[string]string{"app.kubernetes.io/instance": "app"}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "img:v1"}}},
					Status:     corev1.PodStatus{Phase: corev1.PodFailed},
				},
			)
			repo, marks, status := tt.repo()
			require.NoError(t, repo.Create(context.Background(), &models.StackInstance{ID: "inst-poll", Namespace: ns, Status: models.StackStatusRunning}))
			w := NewWatcher(&mockClientProvider{client: NewClientFromInterface(cs)}, repo, &mockBroadcaster{}, time.Second)

			w.poll(context.WithValue(context.Background(), pollCtxKey{}, "poll"))

			assert.Equal(t, models.StackStatusError, status())
			for _, method := range []string{"List", tt.write} {
				v, ok := marks.get(method)
				require.True(t, ok, method)
				assert.Equal(t, "poll", v, "%s runs with the poll context", method)
			}
		})
	}
}

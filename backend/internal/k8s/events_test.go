package k8s

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func warningEvent(name, reason, kind, object, message string, last time.Time) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: name, Namespace: "ns"},
		Type:           corev1.EventTypeWarning,
		Reason:         reason,
		Message:        message,
		InvolvedObject: corev1.ObjectReference{Kind: kind, Name: object},
		LastTimestamp:  metav1.NewTime(last),
	}
}

func TestListPodProblems(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	since := now.Add(-time.Minute)
	quotaMsg := "Error creating: pods \"sync-abc\" is forbidden: exceeded quota: stack-manager-quota, requested: limits.cpu=200m, used: limits.cpu=3990m, limited: limits.cpu=4"

	normal := warningEvent("normal", "Scheduled", "Pod", "web-1", "Successfully assigned", now)
	normal.Type = corev1.EventTypeNormal

	newer := warningEvent("newer", "Other", "Pod", "web-2", "", time.Time{})
	newer.EventTime = metav1.NewMicroTime(now)

	tests := []struct {
		name       string
		objects    []runtime.Object
		wantKeys   []string
		wantReason []string
	}{
		{
			name:    "no events and no pods",
			objects: nil,
		},
		{
			name: "quota FailedCreate event is reported",
			objects: []runtime.Object{
				warningEvent("e1", "FailedCreate", "Job", "sync", quotaMsg, now),
			},
			wantReason: []string{"FailedCreate"},
			wantKeys:   []string{"event|FailedCreate|job/sync|" + quotaMsg},
		},
		{
			name: "event older than since is skipped",
			objects: []runtime.Object{
				warningEvent("old", "FailedScheduling", "Pod", "web-1", "0/3 nodes are available", now.Add(-time.Hour)),
			},
		},
		{
			name: "normal event and unlisted warning reason are skipped",
			objects: []runtime.Object{
				normal,
				warningEvent("probe", "Unhealthy", "Pod", "web-1", "Readiness probe failed", now),
			},
		},
		{
			name: "unlisted reason with a quota message is reported",
			objects: []runtime.Object{
				warningEvent("rs", "SomethingElse", "ReplicaSet", "web-abc", "pods is forbidden: maximum cpu usage per Container is 1, but limit is 2", now),
			},
			wantReason: []string{"SomethingElse"},
		},
		{
			name: "event time of newer components counts as last seen",
			objects: []runtime.Object{
				func() *corev1.Event { e := newer.DeepCopy(); e.Reason = "FailedScheduling"; return e }(),
			},
			wantReason: []string{"FailedScheduling"},
		},
		{
			name: "waiting container with a failure reason is reported",
			objects: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "ns"},
					Status: corev1.PodStatus{
						InitContainerStatuses: []corev1.ContainerStatus{{
							Name:  "init",
							State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}},
						}},
						ContainerStatuses: []corev1.ContainerStatus{{
							Name:  "app",
							State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "Back-off pulling image \"x\""}},
						}},
					},
				},
			},
			wantReason: []string{"ImagePullBackOff"},
			wantKeys:   []string{"container|pod/web-1 container app|ImagePullBackOff"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := NewClientFromInterface(fake.NewSimpleClientset(tt.objects...))
			problems, err := client.ListPodProblems(context.Background(), "ns", since)
			require.NoError(t, err)

			reasons := make([]string, 0, len(problems))
			keys := make([]string, 0, len(problems))
			for _, p := range problems {
				reasons = append(reasons, p.Reason)
				keys = append(keys, p.Key)
			}
			if len(tt.wantReason) == 0 {
				assert.Empty(t, problems)
				return
			}
			assert.Equal(t, tt.wantReason, reasons)
			if tt.wantKeys != nil {
				assert.Equal(t, tt.wantKeys, keys)
			}
		})
	}
}

func TestIsQuotaProblem(t *testing.T) {
	t.Parallel()

	tests := []struct {
		msg  string
		want bool
	}{
		{"exceeded quota: stack-manager-quota", true},
		{"pods \"x\" is forbidden: minimum memory usage per Container is 64Mi", true},
		{"pods \"x\" is forbidden: maximum cpu usage per Container is 1, but limit is 2", true},
		{"pods \"x\" is forbidden: error looking up service account ns/app: serviceaccount \"app\" not found", false},
		{"0/3 nodes are available: 3 Insufficient cpu", false},
		{"", false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.msg, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, IsQuotaProblem(tt.msg))
		})
	}
}

// TestListPodProblems_Pages: the events are read with the continue token,
// and at most maxPodProblemEvents are processed.
func TestListPodProblems_Pages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		pages     int
		perPage   int
		wantCalls int
		wantFound int
	}{
		{name: "three pages are all read", pages: 3, perPage: 2, wantCalls: 3, wantFound: 6},
		{name: "the cap stops the paging", pages: 10, perPage: 400, wantCalls: 3, wantFound: maxPodProblemEvents},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			now := time.Now().UTC()
			cs := fake.NewSimpleClientset()
			calls := 0
			cs.PrependReactor("list", "events", func(action k8stesting.Action) (bool, runtime.Object, error) {
				page := calls
				calls++
				list := &corev1.EventList{}
				for i := 0; i < tt.perPage; i++ {
					list.Items = append(list.Items, *warningEvent(fmt.Sprintf("e-%d-%d", page, i), "FailedScheduling", "Pod",
						fmt.Sprintf("web-%d-%d", page, i), "0/3 nodes are available", now))
				}
				if page+1 < tt.pages {
					list.Continue = fmt.Sprintf("token-%d", page+1)
				}
				return true, list, nil
			})
			client := NewClientFromInterface(cs)
			problems, err := client.ListPodProblems(context.Background(), "ns", now.Add(-time.Minute))
			require.NoError(t, err)
			assert.Equal(t, tt.wantCalls, calls)
			assert.Len(t, problems, tt.wantFound)
		})
	}
}

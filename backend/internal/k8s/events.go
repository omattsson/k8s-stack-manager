package k8s

import (
	"context"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
)

// PodProblem is one reason why pods of a namespace do not start: a Warning
// event (for example FailedCreate with a ResourceQuota message, or
// FailedScheduling), or a container that waits with a failure reason (for
// example ImagePullBackOff or CrashLoopBackOff).
type PodProblem struct {
	// LastSeen is the last time the problem was observed. It is zero for a
	// container state.
	LastSeen time.Time
	// Key identifies the problem for de-duplication. A repeated event (a
	// higher count) has the same key.
	Key string
	// Reason is the event reason or the container waiting reason.
	Reason string
	// Object is "kind/name" of the object (for a container state:
	// "pod/<name> container <name>").
	Object string
	// Message is the event or container state message.
	Message string
}

// podProblemEventReasons are the Warning event reasons that tell why pods are
// not created, scheduled or started. Other Warning events (for example
// readiness probe failures while a pod starts) are not reported.
var podProblemEventReasons = map[string]bool{
	"FailedCreate":           true,
	"FailedScheduling":       true,
	"FailedMount":            true,
	"FailedAttachVolume":     true,
	"FailedCreatePodSandBox": true,
	"Failed":                 true, // kubelet: ErrImagePull, InvalidImageName, CreateContainerConfigError
	"InspectFailed":          true,
	"ErrImageNeverPull":      true,
	"ProvisioningFailed":     true,
	"BackoffLimitExceeded":   true,
	"DeadlineExceeded":       true,
}

// podProblemWaitingReasons are the container waiting reasons that tell why a
// container does not start.
var podProblemWaitingReasons = map[string]bool{
	"ImagePullBackOff":           true,
	"ErrImagePull":               true,
	"InvalidImageName":           true,
	"CrashLoopBackOff":           true,
	"CreateContainerConfigError": true,
	"CreateContainerError":       true,
}

const (
	// podProblemEventPage is the page size of one events list request.
	podProblemEventPage int64 = 200
	// maxPodProblemEvents limits the Warning events read in one call (all
	// pages), so a namespace with very many events does not load the API
	// server or the backend.
	maxPodProblemEvents = 1000
)

// IsQuotaProblem reports whether the message is a ResourceQuota or
// LimitRange rejection: "exceeded quota", "LimitRange", or "usage per"
// (the LimitRange messages "maximum cpu usage per Container is ...").
func IsQuotaProblem(message string) bool {
	msg := strings.ToLower(message)
	return strings.Contains(msg, "exceeded quota") ||
		strings.Contains(msg, "limitrange") ||
		strings.Contains(msg, "usage per")
}

// ListPodProblems returns the pod problems of the namespace: Warning events
// with a reason in podProblemEventReasons (or a quota or LimitRange message)
// that were last seen at or after since, and containers that wait with a
// reason in podProblemWaitingReasons. The events are read in pages of 200
// (field selector type=Warning, continue token), at most 1000 per call; then
// the pods are listed once. The service account needs "list" on events and
// pods in the namespace; a missing permission returns the Forbidden error.
//
// The container states have no since filter: they are the current state of
// the pods, so a container that has waited (for example in
// CrashLoopBackOff) since before the operation also shows.
func (c *Client) ListPodProblems(ctx context.Context, namespace string, since time.Time) ([]PodProblem, error) {
	var problems []PodProblem

	var events []corev1.Event
	opts := metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("type", corev1.EventTypeWarning).String(),
		Limit:         podProblemEventPage,
	}
	for {
		eventList, err := c.clientset.CoreV1().Events(namespace).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		events = append(events, eventList.Items...)
		if eventList.Continue == "" || len(events) >= maxPodProblemEvents {
			break
		}
		opts.Continue = eventList.Continue
	}
	if len(events) > maxPodProblemEvents {
		events = events[:maxPodProblemEvents]
	}
	for i := range events {
		e := &events[i]
		// The fake clientset ignores field selectors: check the type again.
		if e.Type != corev1.EventTypeWarning {
			continue
		}
		if !podProblemEventReasons[e.Reason] && !IsQuotaProblem(e.Message) {
			continue
		}
		lastSeen := eventLastSeen(e)
		if lastSeen.Before(since) {
			continue
		}
		object := strings.ToLower(e.InvolvedObject.Kind) + "/" + e.InvolvedObject.Name
		problems = append(problems, PodProblem{
			LastSeen: lastSeen,
			Key:      "event|" + e.Reason + "|" + object + "|" + e.Message,
			Reason:   e.Reason,
			Object:   object,
			Message:  e.Message,
		})
	}

	podList, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i := range podList.Items {
		pod := &podList.Items[i]
		statuses := append(append([]corev1.ContainerStatus{}, pod.Status.InitContainerStatuses...), pod.Status.ContainerStatuses...)
		for _, cs := range statuses {
			if cs.State.Waiting == nil || !podProblemWaitingReasons[cs.State.Waiting.Reason] {
				continue
			}
			object := "pod/" + pod.Name + " container " + cs.Name
			problems = append(problems, PodProblem{
				// The reason is the key: a CrashLoopBackOff message changes
				// with every restart.
				Key:     "container|" + object + "|" + cs.State.Waiting.Reason,
				Reason:  cs.State.Waiting.Reason,
				Object:  object,
				Message: cs.State.Waiting.Message,
			})
		}
	}

	return problems, nil
}

// eventLastSeen returns the last time the event was observed. Events of
// newer components set EventTime and Series instead of LastTimestamp.
func eventLastSeen(e *corev1.Event) time.Time {
	last := e.LastTimestamp.Time
	if e.Series != nil && e.Series.LastObservedTime.After(last) {
		last = e.Series.LastObservedTime.Time
	}
	if e.EventTime.After(last) {
		last = e.EventTime.Time
	}
	if last.IsZero() {
		last = e.CreationTimestamp.Time
	}
	return last
}

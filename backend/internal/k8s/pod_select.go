package k8s

import (
	"sort"

	corev1 "k8s.io/api/core/v1"
)

// PodReady reports whether the pod is Running, Ready and not terminating.
func PodReady(p *corev1.Pod) bool {
	if p == nil || p.DeletionTimestamp != nil || p.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, cond := range p.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

// SelectReadyPod returns the first pod by name that is Running and Ready and
// has no deletion timestamp, or nil when there is none. The result points into
// pods; the slice is not changed. Port-forward and exec share it, so both
// choose the same pod.
func SelectReadyPod(pods []corev1.Pod) *corev1.Pod {
	var best *corev1.Pod
	for i := range pods {
		p := &pods[i]
		if !PodReady(p) {
			continue
		}
		if best == nil || p.Name < best.Name {
			best = p
		}
	}
	return best
}

// SortPodsByName sorts pods by name in place (helper for callers that list
// candidates).
func SortPodsByName(pods []corev1.Pod) {
	sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
}

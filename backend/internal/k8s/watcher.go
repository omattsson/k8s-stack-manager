package k8s

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"backend/internal/models"
	"backend/internal/websocket"
)

// ClientProvider resolves a k8s Client for a given cluster ID.
// When clusterID is empty, it should return the default cluster's client.
type ClientProvider interface {
	GetK8sClient(clusterID string) (*Client, error)
}

// statusPayload is the WebSocket message payload for status broadcasts.
type statusPayload struct {
	InstanceID      string           `json:"instance_id"`
	Status          string           `json:"status"`
	NamespaceStatus *NamespaceStatus `json:"namespace_status"`
}

// Watcher periodically polls Kubernetes for namespace status and broadcasts
// changes via WebSocket.
type Watcher struct {
	clientProvider ClientProvider
	instanceRepo   models.StackInstanceRepository
	hub            websocket.BroadcastSender
	interval       time.Duration
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	started        atomic.Bool

	// mu protects lastStatus.
	mu         sync.RWMutex
	lastStatus map[string]*NamespaceStatus // instanceID -> last known status
}

// NewWatcher creates a new status watcher.
func NewWatcher(
	provider ClientProvider,
	instanceRepo models.StackInstanceRepository,
	hub websocket.BroadcastSender,
	interval time.Duration,
) *Watcher {
	return &Watcher{
		clientProvider: provider,
		instanceRepo:   instanceRepo,
		hub:            hub,
		interval:       interval,
		lastStatus:     make(map[string]*NamespaceStatus),
	}
}

// Start begins the polling loop in a goroutine.
func (w *Watcher) Start(ctx context.Context) {
	if !w.started.CompareAndSwap(false, true) {
		slog.Warn("K8s status watcher already started, ignoring duplicate Start call")
		return
	}
	ctx, w.cancel = context.WithCancel(ctx)
	w.wg.Add(1)
	go w.run(ctx)
	slog.Info("K8s status watcher started", "interval", w.interval)
}

// Stop gracefully stops the watcher and waits for the polling goroutine to exit.
func (w *Watcher) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
	w.wg.Wait()
	slog.Info("K8s status watcher stopped")
}

// GetStatus returns the last known status for an instance.
func (w *Watcher) GetStatus(instanceID string) (*NamespaceStatus, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	s, ok := w.lastStatus[instanceID]
	return s, ok
}

// run is the main polling loop.
func (w *Watcher) run(ctx context.Context) {
	defer w.wg.Done()

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	// Run an initial check immediately.
	w.poll(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.poll(ctx)
		}
	}
}

// poll checks the status of all active instances.
func (w *Watcher) poll(ctx context.Context) {
	instances, err := w.instanceRepo.List()
	if err != nil {
		slog.Error("Failed to list stack instances", "error", err)
		return
	}

	for i := range instances {
		inst := &instances[i]

		// Only monitor instances that are actively deployed.
		if inst.Status != models.StackStatusRunning &&
			inst.Status != models.StackStatusDeploying {
			continue
		}

		if inst.Namespace == "" {
			continue
		}

		// Resolve the k8s client for this instance's cluster.
		client, clientErr := w.clientProvider.GetK8sClient(inst.ClusterID)
		if clientErr != nil {
			slog.Warn("Failed to get k8s client for instance",
				"instance_id", inst.ID,
				"cluster_id", inst.ClusterID,
				"error", clientErr,
			)
			continue
		}

		// Use a per-namespace timeout to avoid one slow check blocking others.
		checkCtx, checkCancel := context.WithTimeout(ctx, 5*time.Second)
		nsStatus, err := client.GetNamespaceStatus(checkCtx, inst.Namespace, StatusOptions{})
		checkCancel()

		if err != nil {
			slog.Error("Failed to get namespace status",
				"instance_id", inst.ID,
				"namespace", inst.Namespace,
				"error", err,
			)
			continue
		}

		w.mu.RLock()
		prev := w.lastStatus[inst.ID]
		w.mu.RUnlock()

		changed := prev == nil || prev.Status != nsStatus.Status || statusDetailsChanged(prev, nsStatus)
		if changed {
			w.broadcast(inst.ID, nsStatus)
			w.handleStatusTransition(inst, nsStatus)
		}

		w.mu.Lock()
		w.lastStatus[inst.ID] = nsStatus
		w.mu.Unlock()
	}
}

// broadcast sends a status update over WebSocket.
func (w *Watcher) broadcast(instanceID string, nsStatus *NamespaceStatus) {
	if w.hub == nil {
		return
	}

	msg, err := websocket.NewMessage("instance.status", statusPayload{
		InstanceID:      instanceID,
		Status:          nsStatus.Status,
		NamespaceStatus: nsStatus,
	})
	if err != nil {
		slog.Error("Failed to create status message", "error", err)
		return
	}

	data, err := msg.Bytes()
	if err != nil {
		slog.Error("Failed to serialize status message", "error", err)
		return
	}

	w.hub.Broadcast(data)
	slog.Debug("Broadcast status update",
		"instance_id", instanceID,
		"status", nsStatus.Status,
	)
}

// statusDetailsChanged returns true if pod-level details have changed between
// two status snapshots, even when the overall status string is the same.
// This catches transitions like pods going from Pending to Running while the
// namespace overall status remains "progressing" or "degraded".
func statusDetailsChanged(prev, curr *NamespaceStatus) bool {
	if len(prev.Charts) != len(curr.Charts) {
		return true
	}

	prevPods := countPodStates(prev)
	currPods := countPodStates(curr)

	if len(prevPods) != len(currPods) {
		return true
	}
	for k, v := range prevPods {
		if currPods[k] != v {
			return true
		}
	}

	// Check total restart counts — catches CrashLoopBackOff increments
	// even when phase/ready remain unchanged.
	if totalRestarts(prev) != totalRestarts(curr) {
		return true
	}

	// Check container state digests — catches reason transitions like
	// ErrImagePull → ImagePullBackOff where phase/ready/restarts stay the same.
	if containerStateDigest(prev) != containerStateDigest(curr) {
		return true
	}

	// Check ready replica counts on deployments.
	prevReady := countReadyReplicas(prev)
	currReady := countReadyReplicas(curr)
	return prevReady != currReady
}

// countPodStates builds a map of "phase:ready" -> count for quick comparison.
func countPodStates(ns *NamespaceStatus) map[string]int {
	counts := make(map[string]int)
	for _, chart := range ns.Charts {
		for _, pod := range chart.Pods {
			key := pod.Phase
			if pod.Ready {
				key += ":ready"
			}
			counts[key]++
		}
	}
	return counts
}

// totalRestarts sums restart counts across all pods.
func totalRestarts(ns *NamespaceStatus) int32 {
	var total int32
	for _, chart := range ns.Charts {
		for _, pod := range chart.Pods {
			total += pod.RestartCount
		}
	}
	return total
}

// containerStateDigest builds a deterministic, order-independent string from
// all container states so that state/reason/exit-code transitions trigger
// broadcasts even when phase, ready count, and restart count remain unchanged.
// Entries are sorted by pod name + container name for stability.
func containerStateDigest(ns *NamespaceStatus) string {
	var entries []string
	for _, chart := range ns.Charts {
		for _, pod := range chart.Pods {
			for _, cs := range pod.ContainerStates {
				exitCode := int32(0)
				if cs.ExitCode != nil {
					exitCode = *cs.ExitCode
				}
				entries = append(entries, fmt.Sprintf("%s/%s:%s:%s:%d:%d",
					pod.Name, cs.Name, cs.State, cs.Reason, exitCode, cs.RestartCount))
			}
		}
	}
	sort.Strings(entries)
	return strings.Join(entries, ";")
}

// countReadyReplicas sums ready replicas across all deployments.
func countReadyReplicas(ns *NamespaceStatus) int32 {
	var total int32
	for _, chart := range ns.Charts {
		for _, d := range chart.Deployments {
			total += d.ReadyReplicas
		}
	}
	return total
}

// handleStatusTransition updates the instance status in the repository when
// the Kubernetes namespace status degrades.
func (w *Watcher) handleStatusTransition(inst *models.StackInstance, nsStatus *NamespaceStatus) {
	switch nsStatus.Status {
	case StatusError:
		if inst.Status != models.StackStatusError {
			inst.Status = models.StackStatusError
			inst.ErrorMessage = "namespace resources in error state"
			if err := w.instanceRepo.Update(inst); err != nil {
				slog.Error("Failed to update instance status",
					"instance_id", inst.ID,
					"error", err,
				)
			}
		}
	case StatusDegraded:
		slog.Warn("Instance namespace degraded",
			"instance_id", inst.ID,
			"namespace", inst.Namespace,
		)
		// Degraded is logged but does not change the instance status in the
		// repository — only full errors trigger a status transition.
	}
}

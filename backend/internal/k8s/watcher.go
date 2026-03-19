package k8s

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"backend/internal/models"
	"backend/internal/websocket"
)

// statusPayload is the WebSocket message payload for status broadcasts.
type statusPayload struct {
	InstanceID      string           `json:"instance_id"`
	Status          string           `json:"status"`
	NamespaceStatus *NamespaceStatus `json:"namespace_status"`
}

// Watcher periodically polls Kubernetes for namespace status and broadcasts
// changes via WebSocket.
type Watcher struct {
	client       *Client
	instanceRepo models.StackInstanceRepository
	hub          websocket.BroadcastSender
	interval     time.Duration
	cancel       context.CancelFunc
	wg           sync.WaitGroup

	// mu protects lastStatus.
	mu         sync.RWMutex
	lastStatus map[string]*NamespaceStatus // instanceID -> last known status
}

// NewWatcher creates a new status watcher.
func NewWatcher(
	client *Client,
	instanceRepo models.StackInstanceRepository,
	hub websocket.BroadcastSender,
	interval time.Duration,
) *Watcher {
	return &Watcher{
		client:       client,
		instanceRepo: instanceRepo,
		hub:          hub,
		interval:     interval,
		lastStatus:   make(map[string]*NamespaceStatus),
	}
}

// Start begins the polling loop in a goroutine.
func (w *Watcher) Start(ctx context.Context) {
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

		// Use a per-namespace timeout to avoid one slow check blocking others.
		checkCtx, checkCancel := context.WithTimeout(ctx, 5*time.Second)
		nsStatus, err := w.client.GetNamespaceStatus(checkCtx, inst.Namespace)
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

		changed := prev == nil || prev.Status != nsStatus.Status
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

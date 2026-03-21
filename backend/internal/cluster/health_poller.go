package cluster

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"backend/internal/models"
	"backend/internal/websocket"
)

const defaultPollInterval = 60 * time.Second
const healthCheckTimeout = 10 * time.Second

// HealthPollerConfig holds constructor dependencies for HealthPoller.
type HealthPollerConfig struct {
	ClusterRepo models.ClusterRepository
	Registry    *Registry
	Interval    time.Duration
	Hub         websocket.BroadcastSender
}

// HealthPoller periodically checks cluster connectivity and updates health status.
type HealthPoller struct {
	clusterRepo models.ClusterRepository
	registry    *Registry
	interval    time.Duration
	hub         websocket.BroadcastSender
	stopCh      chan struct{}
	done        chan struct{}
	stopOnce    sync.Once
	started     atomic.Bool
}

// NewHealthPoller creates a HealthPoller with the given configuration.
func NewHealthPoller(cfg HealthPollerConfig) *HealthPoller {
	interval := cfg.Interval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	return &HealthPoller{
		clusterRepo: cfg.ClusterRepo,
		registry:    cfg.Registry,
		interval:    interval,
		hub:         cfg.Hub,
		stopCh:      make(chan struct{}),
		done:        make(chan struct{}),
	}
}

// Start begins the background polling goroutine.
// It is safe to call Start multiple times; only the first call launches the goroutine.
func (p *HealthPoller) Start() {
	if !p.started.CompareAndSwap(false, true) {
		return
	}
	go p.run()
}

// Stop requests a graceful shutdown and waits for the poller goroutine to exit.
// It is safe to call Stop multiple times or without calling Start.
func (p *HealthPoller) Stop() {
	p.stopOnce.Do(func() {
		close(p.stopCh)
	})
	if p.started.Load() {
		<-p.done
	}
}

func (p *HealthPoller) run() {
	defer close(p.done)

	// Run an initial poll immediately.
	p.poll()

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			p.poll()
		}
	}
}

func (p *HealthPoller) poll() {
	if p.registry == nil {
		slog.Debug("health poller: registry is nil, skipping poll")
		return
	}

	clusters, err := p.clusterRepo.List()
	if err != nil {
		slog.Error("health poller: failed to list clusters", "error", err)
		return
	}

	for i := range clusters {
		cl := &clusters[i]
		newStatus := p.checkCluster(cl)

		slog.Debug("health poller: checked cluster",
			"cluster_id", cl.ID,
			"cluster_name", cl.Name,
			"status", newStatus,
		)

		if newStatus == cl.HealthStatus {
			continue
		}

		slog.Info("health poller: cluster status changed",
			"cluster_id", cl.ID,
			"cluster_name", cl.Name,
			"old_status", cl.HealthStatus,
			"new_status", newStatus,
		)

		cl.HealthStatus = newStatus
		if updateErr := p.clusterRepo.Update(cl); updateErr != nil {
			slog.Error("health poller: failed to update cluster health",
				"cluster_id", cl.ID,
				"error", updateErr,
			)
			continue
		}

		p.broadcastChange(cl)
	}
}

func (p *HealthPoller) checkCluster(cl *models.Cluster) string {
	client, err := p.registry.GetK8sClient(cl.ID)
	if err != nil {
		slog.Debug("health poller: failed to get k8s client",
			"cluster_id", cl.ID,
			"error", err,
		)
		return models.ClusterUnreachable
	}

	ctx, cancel := context.WithTimeout(context.Background(), healthCheckTimeout)
	defer cancel()

	// Use a context-aware discovery request so the timeout actually cancels
	// the underlying HTTP call, preventing stuck goroutines on hung networks.
	// RESTClient() may be nil for fake/test clientsets, so fall back to ServerVersion().
	if restClient := client.Clientset().Discovery().RESTClient(); restClient != nil {
		result := restClient.Get().AbsPath("/version").Do(ctx)
		if versionErr := result.Error(); versionErr != nil {
			slog.Debug("health poller: cluster ping failed",
				"cluster_id", cl.ID,
				"error", versionErr,
			)
			return models.ClusterUnreachable
		}
	} else {
		if _, versionErr := client.Clientset().Discovery().ServerVersion(); versionErr != nil {
			slog.Debug("health poller: cluster ping failed",
				"cluster_id", cl.ID,
				"error", versionErr,
			)
			return models.ClusterUnreachable
		}
	}

	return models.ClusterHealthy
}

func (p *HealthPoller) broadcastChange(cl *models.Cluster) {
	if p.hub == nil {
		return
	}

	msg, err := websocket.NewMessage("cluster.health_changed", map[string]string{
		"id":            cl.ID,
		"name":          cl.Name,
		"health_status": cl.HealthStatus,
	})
	if err != nil {
		slog.Error("health poller: failed to create broadcast message", "error", err)
		return
	}

	data, err := json.Marshal(msg)
	if err != nil {
		slog.Error("health poller: failed to marshal broadcast message", "error", err)
		return
	}

	p.hub.Broadcast(data)
}

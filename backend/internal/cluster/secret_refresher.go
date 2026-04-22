package cluster

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"backend/internal/k8s"
	"backend/internal/models"
)

const defaultRefreshInterval = 4 * time.Hour

// SecretRefresherConfig holds constructor dependencies for SecretRefresher.
type SecretRefresherConfig struct {
	ClusterRepo  models.ClusterRepository
	InstanceRepo models.StackInstanceRepository
	Registry     *Registry
	Interval     time.Duration
}

// SecretRefresher periodically refreshes image pull secrets in namespaces
// of running stack instances. This handles the case where short-lived registry
// tokens (e.g. ACR tokens) expire while stacks are still running.
type SecretRefresher struct {
	clusterRepo  models.ClusterRepository
	instanceRepo models.StackInstanceRepository
	registry     *Registry
	interval     time.Duration
	stopCh       chan struct{}
	done         chan struct{}
	stopOnce     sync.Once
	started      atomic.Bool
}

// NewSecretRefresher creates a SecretRefresher with the given configuration.
func NewSecretRefresher(cfg SecretRefresherConfig) *SecretRefresher {
	interval := cfg.Interval
	if interval <= 0 {
		interval = defaultRefreshInterval
	}
	return &SecretRefresher{
		clusterRepo:  cfg.ClusterRepo,
		instanceRepo: cfg.InstanceRepo,
		registry:     cfg.Registry,
		interval:     interval,
		stopCh:       make(chan struct{}),
		done:         make(chan struct{}),
	}
}

// Start begins the background refresh goroutine.
// Safe to call multiple times; only the first call launches the goroutine.
func (r *SecretRefresher) Start() {
	if !r.started.CompareAndSwap(false, true) {
		return
	}
	go r.run()
}

// Stop requests a graceful shutdown and waits for the goroutine to exit.
func (r *SecretRefresher) Stop() {
	r.stopOnce.Do(func() {
		close(r.stopCh)
	})
	if r.started.Load() {
		<-r.done
	}
}

func (r *SecretRefresher) run() {
	defer close(r.done)

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-r.stopCh:
			return
		case <-ticker.C:
			r.refresh()
		}
	}
}

func (r *SecretRefresher) refresh() {
	if r.registry == nil {
		return
	}

	clusters, err := r.clusterRepo.List()
	if err != nil {
		slog.Error("secret refresher: failed to list clusters", "error", err)
		return
	}

	for i := range clusters {
		cl := &clusters[i]
		regCfg := cl.RegistryConfig()
		if regCfg == nil {
			continue
		}

		r.refreshClusterSecrets(cl, regCfg)
	}
}

const secretRefreshTimeout = 30 * time.Second

func (r *SecretRefresher) refreshClusterSecrets(cl *models.Cluster, regCfg *models.RegistryConfig) {
	instances, err := r.instanceRepo.FindByCluster(cl.ID)
	if err != nil {
		slog.Error("secret refresher: failed to list instances",
			"cluster_id", cl.ID, "error", err)
		return
	}

	k8sClient, err := r.registry.GetK8sClient(cl.ID)
	if err != nil {
		slog.Error("secret refresher: failed to get k8s client",
			"cluster_id", cl.ID, "error", err)
		return
	}

	var refreshed, failed int
	for j := range instances {
		inst := &instances[j]
		if inst.Status != models.StackStatusRunning && inst.Status != models.StackStatusDeploying {
			continue
		}
		if inst.Namespace == "" {
			continue
		}

		if err := r.refreshSecret(k8sClient, inst.Namespace, regCfg); err != nil {
			slog.Warn("secret refresher: failed to refresh pull secret",
				"cluster_id", cl.ID,
				"namespace", inst.Namespace,
				"instance_id", inst.ID,
				"error", err,
			)
			failed++
		} else {
			refreshed++
		}
	}

	if refreshed > 0 || failed > 0 {
		slog.Info("secret refresher: completed cluster refresh",
			"cluster_id", cl.ID,
			"registry", regCfg.URL,
			"refreshed", refreshed,
			"failed", failed,
		)
	}
}

func (r *SecretRefresher) refreshSecret(k8sClient *k8s.Client, namespace string, regCfg *models.RegistryConfig) error {
	ctx, cancel := context.WithTimeout(context.Background(), secretRefreshTimeout)
	defer cancel()

	return k8sClient.EnsureDockerRegistrySecret(
		ctx, namespace, regCfg.SecretName,
		regCfg.URL, regCfg.Username, regCfg.Password,
	)
}

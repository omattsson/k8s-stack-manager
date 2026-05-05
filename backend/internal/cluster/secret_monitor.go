package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"backend/internal/k8s"
	"backend/internal/models"
)

// SecretNotifier sends system-wide notifications for secret expiry warnings.
type SecretNotifier interface {
	NotifySystem(ctx context.Context, notifType, title, message, entityType, entityID string) error
}

// SecretMonitorConfig holds constructor dependencies.
type SecretMonitorConfig struct {
	ClusterRepo  models.ClusterRepository
	InstanceRepo models.StackInstanceRepository
	Registry     *Registry
	Notifier     SecretNotifier
	Interval     time.Duration
	Threshold    time.Duration // warn this far before expiry (default 7 days)
}

// SecretMonitor periodically scans managed K8s secrets (ACR pull secrets, TLS
// certs) and emits system notifications when they are approaching expiry.
type SecretMonitor struct {
	clusterRepo  models.ClusterRepository
	instanceRepo models.StackInstanceRepository
	registry     *Registry
	notifier     SecretNotifier
	interval     time.Duration
	threshold    time.Duration
	stopCh       chan struct{}
	done         chan struct{}
	stopOnce     sync.Once
	started      atomic.Bool

	mu     sync.Mutex
	warned map[string]time.Time // key: "cluster/ns/secret" → last warned
}

const (
	defaultSecretMonitorInterval  = 1 * time.Hour
	defaultSecretExpiryThreshold  = 7 * 24 * time.Hour // 7 days
	secretWarnCooldown            = 24 * time.Hour
)

// NewSecretMonitor creates a new secret expiry monitor.
func NewSecretMonitor(cfg SecretMonitorConfig) *SecretMonitor {
	interval := cfg.Interval
	if interval <= 0 {
		interval = defaultSecretMonitorInterval
	}
	threshold := cfg.Threshold
	if threshold <= 0 {
		threshold = defaultSecretExpiryThreshold
	}
	return &SecretMonitor{
		clusterRepo:  cfg.ClusterRepo,
		instanceRepo: cfg.InstanceRepo,
		registry:     cfg.Registry,
		notifier:     cfg.Notifier,
		interval:     interval,
		threshold:    threshold,
		stopCh:       make(chan struct{}),
		done:         make(chan struct{}),
		warned:       make(map[string]time.Time),
	}
}

// Start begins the background monitoring goroutine.
func (m *SecretMonitor) Start() {
	if !m.started.CompareAndSwap(false, true) {
		return
	}
	go m.run()
}

// Stop requests a graceful shutdown and waits for the goroutine to exit.
func (m *SecretMonitor) Stop() {
	m.stopOnce.Do(func() { close(m.stopCh) })
	if m.started.Load() {
		<-m.done
	}
}

func (m *SecretMonitor) run() {
	defer close(m.done)

	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	slog.Info("Secret monitor started", "interval", m.interval, "threshold", m.threshold)

	m.scan()

	for {
		select {
		case <-m.stopCh:
			slog.Info("Secret monitor stopped")
			return
		case <-ticker.C:
			m.scan()
		}
	}
}

func (m *SecretMonitor) scan() {
	if m.notifier == nil {
		return
	}

	m.pruneWarned()

	clusters, err := m.clusterRepo.List()
	if err != nil {
		slog.Error("secret monitor: failed to list clusters", "error", err)
		return
	}

	for i := range clusters {
		cl := &clusters[i]
		m.scanCluster(cl)
	}
}

func (m *SecretMonitor) pruneWarned() {
	cutoff := time.Now().Add(-2 * secretWarnCooldown)
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, t := range m.warned {
		if t.Before(cutoff) {
			delete(m.warned, k)
		}
	}
}

func (m *SecretMonitor) scanCluster(cl *models.Cluster) {
	instances, err := m.instanceRepo.FindByCluster(cl.ID)
	if err != nil {
		return
	}

	k8sClient, err := m.registry.GetK8sClient(cl.ID)
	if err != nil {
		return
	}

	now := time.Now()
	deadline := now.Add(m.threshold)

	for j := range instances {
		inst := &instances[j]
		if inst.Namespace == "" {
			continue
		}
		if inst.Status != models.StackStatusRunning && inst.Status != models.StackStatusPartial && inst.Status != models.StackStatusDeploying && inst.Status != models.StackStatusStabilizing {
			continue
		}

		m.scanNamespace(k8sClient, cl, inst.Namespace, now, deadline)
	}
}

func (m *SecretMonitor) scanNamespace(k8sClient *k8s.Client, cl *models.Cluster, namespace string, now, deadline time.Time) {
	secrets, err := k8sClient.ListManagedSecrets(context.Background(), namespace)
	if err != nil {
		slog.Warn("secret monitor: failed to list secrets",
			"cluster_id", cl.ID, "namespace", namespace, "error", err)
		return
	}

	for _, s := range secrets {
		if s.ExpiresAt == nil || s.ExpiresAt.After(deadline) {
			continue
		}

		key := fmt.Sprintf("%s/%s/%s", cl.ID, s.Namespace, s.Name)

		m.mu.Lock()
		lastWarned, alreadyWarned := m.warned[key]
		if alreadyWarned && now.Sub(lastWarned) < secretWarnCooldown {
			m.mu.Unlock()
			continue
		}
		m.warned[key] = now
		m.mu.Unlock()

		remaining := time.Until(*s.ExpiresAt).Truncate(time.Hour)
		_ = m.notifier.NotifySystem(context.Background(),
			"secret.expiring",
			fmt.Sprintf("%s secret expiring on %s", s.Type, cl.Name),
			fmt.Sprintf("%s secret %q in namespace %s expires in %s",
				s.Type, s.Name, s.Namespace, remaining),
			"cluster", cl.ID,
		)
		slog.Warn("secret monitor: secret expiring soon",
			"cluster_id", cl.ID, "namespace", s.Namespace,
			"secret", s.Name, "type", s.Type, "expires_at", s.ExpiresAt)
	}
}

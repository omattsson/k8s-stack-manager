package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"backend/internal/models"

	"k8s.io/apimachinery/pkg/api/resource"
)

// QuotaNotifier sends system-wide notifications for quota warnings.
type QuotaNotifier interface {
	NotifySystem(ctx context.Context, notifType, title, message, entityType, entityID string) error
}

// QuotaMonitorConfig holds constructor dependencies.
type QuotaMonitorConfig struct {
	ClusterRepo  models.ClusterRepository
	InstanceRepo models.StackInstanceRepository
	QuotaRepo    models.ResourceQuotaRepository
	Registry     *Registry
	Notifier     QuotaNotifier
	Interval     time.Duration
	Threshold    float64 // 0.0–1.0, default 0.8
}

// QuotaMonitor periodically checks cluster resource usage against configured
// quotas and emits system notifications when usage exceeds a threshold.
type QuotaMonitor struct {
	clusterRepo  models.ClusterRepository
	instanceRepo models.StackInstanceRepository
	quotaRepo    models.ResourceQuotaRepository
	registry     *Registry
	notifier     QuotaNotifier
	interval     time.Duration
	threshold    float64
	stopCh       chan struct{}
	done         chan struct{}
	stopOnce     sync.Once
	started      atomic.Bool

	// cooldown tracks recently alerted clusters to avoid spam.
	mu       sync.Mutex
	cooldown map[string]time.Time
}

const (
	defaultQuotaMonitorInterval = 5 * time.Minute
	defaultQuotaThreshold       = 0.8
	quotaCooldownDuration       = 1 * time.Hour
)

// NewQuotaMonitor creates a new quota monitor.
func NewQuotaMonitor(cfg QuotaMonitorConfig) *QuotaMonitor {
	interval := cfg.Interval
	if interval <= 0 {
		interval = defaultQuotaMonitorInterval
	}
	threshold := cfg.Threshold
	if threshold <= 0 {
		threshold = defaultQuotaThreshold
	}
	return &QuotaMonitor{
		clusterRepo:  cfg.ClusterRepo,
		instanceRepo: cfg.InstanceRepo,
		quotaRepo:    cfg.QuotaRepo,
		registry:     cfg.Registry,
		notifier:     cfg.Notifier,
		interval:     interval,
		threshold:    threshold,
		stopCh:       make(chan struct{}),
		done:         make(chan struct{}),
		cooldown:     make(map[string]time.Time),
	}
}

// Start begins the background monitoring goroutine.
func (m *QuotaMonitor) Start() {
	if !m.started.CompareAndSwap(false, true) {
		return
	}
	go m.run()
}

// Stop requests a graceful shutdown and waits for the goroutine to exit.
func (m *QuotaMonitor) Stop() {
	m.stopOnce.Do(func() { close(m.stopCh) })
	if m.started.Load() {
		<-m.done
	}
}

func (m *QuotaMonitor) run() {
	defer close(m.done)

	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	slog.Info("Quota monitor started", "interval", m.interval, "threshold", m.threshold)

	m.check()

	for {
		select {
		case <-m.stopCh:
			slog.Info("Quota monitor stopped")
			return
		case <-ticker.C:
			m.check()
		}
	}
}

func (m *QuotaMonitor) check() {
	if m.quotaRepo == nil || m.notifier == nil {
		return
	}

	m.pruneCooldowns()

	clusters, err := m.clusterRepo.List()
	if err != nil {
		slog.Error("quota monitor: failed to list clusters", "error", err)
		return
	}

	for i := range clusters {
		cl := &clusters[i]
		m.checkCluster(cl)
	}
}

func (m *QuotaMonitor) pruneCooldowns() {
	cutoff := time.Now().Add(-2 * quotaCooldownDuration)
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, t := range m.cooldown {
		if t.Before(cutoff) {
			delete(m.cooldown, k)
		}
	}
}

func (m *QuotaMonitor) checkCluster(cl *models.Cluster) {
	quotaCfg, err := m.quotaRepo.GetByClusterID(context.Background(), cl.ID)
	if err != nil || quotaCfg == nil {
		return
	}

	instances, err := m.instanceRepo.FindByCluster(cl.ID)
	if err != nil {
		slog.Error("quota monitor: failed to list instances", "cluster_id", cl.ID, "error", err)
		return
	}

	var running int
	var namespaces []string
	for j := range instances {
		if instances[j].Status == models.StackStatusRunning || instances[j].Status == models.StackStatusDeploying {
			running++
			if instances[j].Namespace != "" {
				namespaces = append(namespaces, instances[j].Namespace)
			}
		}
	}

	k8sClient, err := m.registry.GetK8sClient(cl.ID)
	if err != nil {
		slog.Warn("quota monitor: failed to get k8s client", "cluster_id", cl.ID, "error", err)
		return
	}

	var warnings []string

	// Check aggregate resource usage across managed namespaces.
	var totalCPUUsed, totalCPULimit, totalMemUsed, totalMemLimit int64
	var totalPods int
	for _, ns := range namespaces {
		usage, usageErr := k8sClient.GetNamespaceResourceUsage(context.Background(), ns)
		if usageErr != nil {
			continue
		}
		totalCPUUsed += parseMilliCPU(usage.CPUUsed)
		totalMemUsed += parseBytes(usage.MemoryUsed)
		totalPods += usage.PodCount
	}

	totalCPULimit = parseMilliCPU(quotaCfg.CPULimit)
	totalMemLimit = parseBytes(quotaCfg.MemoryLimit)

	// Quotas are per-namespace; the effective cluster limit is quota × namespace count.
	nsCount := int64(max(len(namespaces), 1))

	if totalCPULimit > 0 {
		ratio := float64(totalCPUUsed) / float64(totalCPULimit*nsCount)
		if ratio >= m.threshold {
			warnings = append(warnings, fmt.Sprintf("CPU at %.0f%% of quota", ratio*100))
		}
	}

	if totalMemLimit > 0 {
		ratio := float64(totalMemUsed) / float64(totalMemLimit*nsCount)
		if ratio >= m.threshold {
			warnings = append(warnings, fmt.Sprintf("Memory at %.0f%% of quota", ratio*100))
		}
	}

	if quotaCfg.PodLimit > 0 {
		podLimit := quotaCfg.PodLimit * int(nsCount)
		ratio := float64(totalPods) / float64(podLimit)
		if ratio >= m.threshold {
			warnings = append(warnings, fmt.Sprintf("Pods at %.0f%% of limit (%d/%d)", ratio*100, totalPods, podLimit))
		}
	}

	if len(warnings) == 0 {
		return
	}

	// Debounce: skip if we alerted this cluster recently.
	m.mu.Lock()
	if last, ok := m.cooldown[cl.ID]; ok && time.Since(last) < quotaCooldownDuration {
		m.mu.Unlock()
		return
	}
	m.cooldown[cl.ID] = time.Now()
	m.mu.Unlock()

	msg := fmt.Sprintf("Cluster %q: %s (%d running stacks)", cl.Name, joinWarnings(warnings), running)
	_ = m.notifier.NotifySystem(context.Background(),
		"quota.warning",
		"Cluster resource quota warning",
		msg,
		"cluster", cl.ID,
	)
	slog.Warn("quota monitor: threshold exceeded", "cluster_id", cl.ID, "warnings", warnings)
}

func parseMilliCPU(s string) int64 {
	if s == "" {
		return 0
	}
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return 0
	}
	return q.MilliValue()
}

func parseBytes(s string) int64 {
	if s == "" {
		return 0
	}
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return 0
	}
	return q.Value()
}

func joinWarnings(w []string) string {
	if len(w) == 1 {
		return w[0]
	}
	result := w[0]
	for i := 1; i < len(w); i++ {
		result += "; " + w[i]
	}
	return result
}

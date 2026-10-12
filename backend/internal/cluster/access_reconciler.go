package cluster

import (
	"context"
	"log/slog"
	"time"

	"backend/internal/models"
)

// defaultAccessReconcileInterval is how often the leader checks the access
// RoleBinding of every stack namespace.
const defaultAccessReconcileInterval = 10 * time.Minute

// AccessReconciler applies the access RoleBinding (namespace-scoped access,
// k8s.AccessBinding) to every stack namespace of the in-cluster clusters.
// New namespaces get it from EnsureNamespace at deploy time; the reconciler
// covers namespaces that existed before the namespace-scoped mode was turned
// on, and RoleBindings that someone deleted. It runs on the leader replica
// only: once at the start of each term, then every interval.
type AccessReconciler struct {
	clusterRepo models.ClusterRepository
	registry    *Registry
	interval    time.Duration
}

// NewAccessReconciler creates the reconciler. interval <= 0 uses 10 minutes.
func NewAccessReconciler(clusterRepo models.ClusterRepository, registry *Registry, interval time.Duration) *AccessReconciler {
	if interval <= 0 {
		interval = defaultAccessReconcileInterval
	}
	return &AccessReconciler{clusterRepo: clusterRepo, registry: registry, interval: interval}
}

// Run reconciles once, then every interval until ctx is done. It can run
// again after it returned (one call per leadership term).
func (a *AccessReconciler) Run(ctx context.Context) {
	a.reconcile(ctx)
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.reconcile(ctx)
		}
	}
}

func (a *AccessReconciler) reconcile(ctx context.Context) {
	clusters, err := a.clusterRepo.List()
	if err != nil {
		slog.Warn("access reconcile: list clusters failed", "error", err)
		return
	}
	for _, c := range clusters {
		if !c.UseInCluster || ctx.Err() != nil {
			continue
		}
		k8sClient, err := a.registry.GetK8sClient(c.ID)
		if err != nil || k8sClient == nil || !k8sClient.HasAccessBinding() {
			if err != nil {
				slog.Warn("access reconcile: no client for cluster", "cluster_id", c.ID, "error", err)
			}
			continue
		}
		written, err := k8sClient.EnsureAccessBindings(ctx)
		if err != nil {
			slog.Warn("access reconcile: some namespaces failed", "cluster_id", c.ID, "written", written, "error", err)
			continue
		}
		if written > 0 {
			slog.Info("access reconcile: RoleBindings applied", "cluster_id", c.ID, "written", written)
		}
	}
}

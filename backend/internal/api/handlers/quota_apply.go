package handlers

import (
	"context"
	"fmt"
	"sync"

	"backend/internal/cluster"
	"backend/internal/k8s"
	"backend/internal/models"
)

// namespaceQuotaClient is the part of k8s.Client that the quota handlers use
// to write the ResourceQuota and the LimitRange of a stack namespace.
type namespaceQuotaClient interface {
	ListStackNamespaces(ctx context.Context) ([]k8s.NamespaceInfo, error)
	NamespaceExists(ctx context.Context, name string) (bool, error)
	EnsureResourceQuota(ctx context.Context, namespace string, config *models.ResourceQuotaConfig) error
	EnsureLimitRange(ctx context.Context, namespace string, config *models.ResourceQuotaConfig) error
}

// quotaClientGetter returns the quota client of a cluster. An empty cluster
// ID means the default cluster.
type quotaClientGetter func(clusterID string) (namespaceQuotaClient, error)

// registryQuotaClients returns a quotaClientGetter backed by the cluster
// registry, or nil when registry is nil.
func registryQuotaClients(registry *cluster.Registry) quotaClientGetter {
	if registry == nil {
		return nil
	}
	return func(clusterID string) (namespaceQuotaClient, error) {
		c, err := registry.GetK8sClient(clusterID)
		if err != nil {
			return nil, err
		}
		if c == nil {
			return nil, fmt.Errorf("no k8s client for cluster %q", clusterID)
		}
		return c, nil
	}
}

// applyNamespaceQuota writes the ResourceQuota and the LimitRange of quota to
// the namespace. An empty quota writes nothing.
func applyNamespaceQuota(ctx context.Context, client namespaceQuotaClient, namespace string, quota *models.ResourceQuotaConfig) error {
	if quota == nil || quota.IsEmpty() {
		return nil
	}
	if err := client.EnsureResourceQuota(ctx, namespace, quota); err != nil {
		return fmt.Errorf("ensure resource quota: %w", err)
	}
	if err := client.EnsureLimitRange(ctx, namespace, quota); err != nil {
		return fmt.Errorf("ensure limit range: %w", err)
	}
	return nil
}

// keyedMutex holds one mutex for each key. An entry is removed when no caller
// holds or waits for it.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*keyedLock
}

type keyedLock struct {
	mu   sync.Mutex
	refs int
}

// lock locks key and returns the unlock function.
func (k *keyedMutex) lock(key string) func() {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = map[string]*keyedLock{}
	}
	l, ok := k.locks[key]
	if !ok {
		l = &keyedLock{}
		k.locks[key] = l
	}
	l.refs++
	k.mu.Unlock()

	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		k.mu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}

// namespaceQuotaLocks serializes the quota writes of this replica to one
// namespace ("<cluster ID>/<namespace>"). Each write reads the stored quotas
// while it holds the lock, so the last write has the latest state. Across
// replicas, and against a concurrent deploy, the last writer wins.
var namespaceQuotaLocks keyedMutex

// quotaSources holds the repositories that writeNamespaceQuota reads. A nil
// repository means no quota of that kind.
type quotaSources struct {
	clusterQuotas models.ResourceQuotaRepository
	overrides     models.InstanceQuotaOverrideRepository
}

// writeNamespaceQuota reads the cluster quota of clusterID and the quota
// override of instanceID ("" means no instance) while it holds the namespace
// lock. Then it writes the merged quota to the namespace. It returns false
// when the merged quota is empty (nothing is written), and an error when a
// read fails or the merged quota is not valid (nothing is written).
func writeNamespaceQuota(ctx context.Context, client namespaceQuotaClient, src quotaSources, clusterID, instanceID, namespace string) (bool, error) {
	unlock := namespaceQuotaLocks.lock(clusterID + "/" + namespace)
	defer unlock()

	var clusterQuota *models.ResourceQuotaConfig
	if src.clusterQuotas != nil {
		q, err := src.clusterQuotas.GetByClusterID(ctx, clusterID)
		if err != nil && !isNotFoundError(err) {
			return false, fmt.Errorf("get cluster quota: %w", err)
		}
		if err == nil {
			clusterQuota = q
		}
	}
	var override *models.InstanceQuotaOverride
	if instanceID != "" && src.overrides != nil {
		ov, err := src.overrides.GetByInstanceID(ctx, instanceID)
		if err != nil && !isNotFoundError(err) {
			return false, fmt.Errorf("get quota override: %w", err)
		}
		if err == nil {
			override = ov
		}
	}
	quota, err := effectiveInstanceQuota(clusterQuota, override)
	if err != nil {
		return false, fmt.Errorf("effective quota: %w", err)
	}
	if quota.IsEmpty() {
		return false, nil
	}
	if err := applyNamespaceQuota(ctx, client, namespace, quota); err != nil {
		return false, err
	}
	return true, nil
}

// effectiveInstanceQuota returns the quota that applies to the namespace of
// an instance: the cluster quota (nil means none) merged with the instance
// override (nil means none). It returns an error when the merged quota is not
// valid, as the deployer does (models.ValidateEffectiveQuota).
func effectiveInstanceQuota(clusterQuota *models.ResourceQuotaConfig, override *models.InstanceQuotaOverride) (*models.ResourceQuotaConfig, error) {
	if clusterQuota == nil {
		clusterQuota = &models.ResourceQuotaConfig{}
	}
	if err := models.ValidateEffectiveQuota(clusterQuota, override); err != nil {
		return nil, err
	}
	return models.MergeQuotaOverride(clusterQuota, override), nil
}

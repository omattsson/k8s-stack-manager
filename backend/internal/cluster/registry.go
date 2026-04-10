// Package cluster manages per-cluster Kubernetes and Helm client pools with
// lazy initialization and thread-safe caching.
package cluster

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"backend/internal/deployer"
	"backend/internal/k8s"
	"backend/internal/models"
	"backend/pkg/dberrors"
)

// K8sClientFactory creates a k8s.Client from a kubeconfig path.
// Defaults to k8s.NewClient; override in tests.
type K8sClientFactory func(kubeconfigPath string) (*k8s.Client, error)

// HelmClientFactory creates a HelmExecutor from binary path, kubeconfig, and timeout.
// Defaults to deployer.NewHelmClient; override in tests.
type HelmClientFactory func(binaryPath, kubeconfig string, timeout time.Duration) deployer.HelmExecutor

// ClusterClients holds the k8s + helm clients for a single cluster.
type ClusterClients struct {
	K8s            *k8s.Client
	Helm           deployer.HelmExecutor
	kubeconfigPath string // temp file path if created from kubeconfig data (for cleanup)
}

// RegistryConfig holds constructor dependencies.
type RegistryConfig struct {
	ClusterRepo models.ClusterRepository
	HelmBinary  string
	HelmTimeout time.Duration
}

// Registry manages per-cluster Kubernetes and Helm clients.
// Clients are created lazily on first access and cached.
// Thread-safe via sync.RWMutex.
type Registry struct {
	mu              sync.RWMutex
	clients         map[string]*ClusterClients // cluster ID → clients
	clusterRepo     models.ClusterRepository
	helmBinary      string
	helmTimeout     time.Duration
	defaultID       string // cached default cluster ID (empty = not resolved yet)
	defaultResolved bool   // whether we've attempted to resolve default

	// Factories for testability.
	k8sFactory  K8sClientFactory
	helmFactory HelmClientFactory
}

// NewRegistry creates a Registry with the given configuration.
func NewRegistry(cfg RegistryConfig) *Registry {
	return &Registry{
		clients:     make(map[string]*ClusterClients),
		clusterRepo: cfg.ClusterRepo,
		helmBinary:  cfg.HelmBinary,
		helmTimeout: cfg.HelmTimeout,
		k8sFactory:  k8s.NewClient,
		helmFactory: func(binaryPath, kubeconfig string, timeout time.Duration) deployer.HelmExecutor {
			return deployer.NewHelmClient(binaryPath, kubeconfig, timeout)
		},
	}
}

// GetClients returns cached clients for the given cluster ID, creating them on first access.
func (r *Registry) GetClients(clusterID string) (*ClusterClients, error) {
	// Fast path: read lock for cache hit.
	r.mu.RLock()
	if cc, ok := r.clients[clusterID]; ok {
		r.mu.RUnlock()
		return cc, nil
	}
	r.mu.RUnlock()

	// Fetch cluster metadata and build clients outside the lock to avoid
	// blocking concurrent readers/writers during potentially slow I/O
	// (DB fetch, kubeconfig parsing, temp-file creation).
	clusterModel, err := r.clusterRepo.FindByID(clusterID)
	if err != nil {
		return nil, fmt.Errorf("cluster %s: %w", clusterID, err)
	}

	cc, err := r.buildClients(clusterModel)
	if err != nil {
		return nil, fmt.Errorf("cluster %s: build clients: %w", clusterID, err)
	}

	// Brief write lock to populate the cache. Re-check in case another
	// goroutine built the same clients concurrently — use the first one
	// and clean up our duplicate.
	r.mu.Lock()
	if existing, ok := r.clients[clusterID]; ok {
		r.mu.Unlock()
		cleanupTempKubeconfig(cc)
		return existing, nil
	}
	r.clients[clusterID] = cc
	r.mu.Unlock()

	return cc, nil
}

// GetDefaultClients returns clients for the default cluster.
func (r *Registry) GetDefaultClients() (*ClusterClients, error) {
	r.mu.RLock()
	resolved := r.defaultResolved
	id := r.defaultID
	r.mu.RUnlock()

	if resolved {
		if id == "" {
			return nil, fmt.Errorf("no default cluster configured")
		}
		return r.GetClients(id)
	}

	// Resolve the default cluster.
	r.mu.Lock()
	// Double-check after acquiring write lock.
	if r.defaultResolved {
		id = r.defaultID
		r.mu.Unlock()
		if id == "" {
			return nil, fmt.Errorf("no default cluster configured")
		}
		return r.GetClients(id)
	}

	cluster, err := r.clusterRepo.FindDefault()
	if err != nil {
		// Only cache the "no default" state for definitive not-found errors.
		// Transient failures (DB down, network) should allow retries.
		if isNotFoundError(err) {
			r.defaultResolved = true
			r.defaultID = ""
		}
		r.mu.Unlock()
		return nil, fmt.Errorf("no default cluster configured: %w", err)
	}

	r.defaultResolved = true
	r.defaultID = cluster.ID
	r.mu.Unlock()

	return r.GetClients(cluster.ID)
}

// ResolveClusterID returns clusterID if non-empty, otherwise returns the default cluster's ID.
func (r *Registry) ResolveClusterID(clusterID string) (string, error) {
	if clusterID != "" {
		return clusterID, nil
	}

	r.mu.RLock()
	resolved := r.defaultResolved
	id := r.defaultID
	r.mu.RUnlock()

	if resolved {
		if id == "" {
			return "", fmt.Errorf("no default cluster configured")
		}
		return id, nil
	}

	r.mu.Lock()
	if r.defaultResolved {
		id = r.defaultID
		r.mu.Unlock()
		if id == "" {
			return "", fmt.Errorf("no default cluster configured")
		}
		return id, nil
	}

	cluster, err := r.clusterRepo.FindDefault()
	if err != nil {
		if isNotFoundError(err) {
			r.defaultResolved = true
			r.defaultID = ""
		}
		r.mu.Unlock()
		return "", fmt.Errorf("no default cluster configured: %w", err)
	}

	r.defaultResolved = true
	r.defaultID = cluster.ID
	r.mu.Unlock()

	return cluster.ID, nil
}

// GetK8sClient returns the k8s Client for the given cluster ID.
// If clusterID is empty, returns the default cluster's client.
// This method satisfies the k8s.ClientProvider interface.
func (r *Registry) GetK8sClient(clusterID string) (*k8s.Client, error) {
	if clusterID == "" {
		clients, err := r.GetDefaultClients()
		if err != nil {
			return nil, err
		}
		return clients.K8s, nil
	}
	clients, err := r.GetClients(clusterID)
	if err != nil {
		return nil, err
	}
	return clients.K8s, nil
}

// GetHelmExecutor returns the Helm executor for the given cluster ID.
// If clusterID is empty, returns the default cluster's executor.
// This method satisfies the deployer.ClusterResolver interface.
func (r *Registry) GetHelmExecutor(clusterID string) (deployer.HelmExecutor, error) {
	if clusterID == "" {
		clients, err := r.GetDefaultClients()
		if err != nil {
			return nil, err
		}
		return clients.Helm, nil
	}
	clients, err := r.GetClients(clusterID)
	if err != nil {
		return nil, err
	}
	return clients.Helm, nil
}

// InvalidateClient removes cached clients for the given cluster ID and cleans up temp files.
func (r *Registry) InvalidateClient(clusterID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if cc, ok := r.clients[clusterID]; ok {
		cleanupTempKubeconfig(cc)
		delete(r.clients, clusterID)
	}
}

// InvalidateDefault resets the cached default cluster resolution.
func (r *Registry) InvalidateDefault() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.defaultResolved = false
	r.defaultID = ""
}

// NewRegistryForTest creates a Registry with pre-populated clients for testing.
// The given clients are cached under clusterID and set as the default cluster.
func NewRegistryForTest(clusterID string, k8sClient *k8s.Client, helmExec deployer.HelmExecutor) *Registry {
	return &Registry{
		clients: map[string]*ClusterClients{
			clusterID: {K8s: k8sClient, Helm: helmExec},
		},
		defaultID:       clusterID,
		defaultResolved: true,
	}
}

// Close cleans up all cached clients and temp kubeconfig files.
func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for id, cc := range r.clients {
		cleanupTempKubeconfig(cc)
		delete(r.clients, id)
	}
	return nil
}

const (
	healthCheckPerClusterTimeout = 3 * time.Second
	healthCheckOverallTimeout    = 4 * time.Second
)

// HealthCheck verifies that at least one registered cluster is reachable.
// Returns nil if no clusters are registered (valid for fresh installs) or if
// at least one cluster responds to a version ping. Returns an error only when
// all registered clusters are unreachable.
func (r *Registry) HealthCheck(ctx context.Context) error {
	// Read clusterRepo under lock, then release before I/O.
	r.mu.RLock()
	repo := r.clusterRepo
	r.mu.RUnlock()

	if repo == nil {
		return nil
	}

	clusters, err := repo.List()
	if err != nil {
		slog.Error("health check: failed to list clusters", "error", err)
		return fmt.Errorf("cluster registry unavailable")
	}
	if len(clusters) == 0 {
		return nil
	}

	// Overall budget so that readiness latency stays bounded regardless of
	// the number of registered clusters.
	overallCtx, overallCancel := context.WithTimeout(ctx, healthCheckOverallTimeout)
	defer overallCancel()

	type result struct {
		clusterID string
		err       error
	}

	ch := make(chan result, len(clusters))

	for i := range clusters {
		cl := &clusters[i]
		go func(clusterID string) {
			client, clientErr := r.GetK8sClient(clusterID)
			if clientErr != nil {
				slog.Debug("health check: failed to get k8s client", "cluster_id", clusterID, "error", clientErr)
				ch <- result{clusterID: clusterID, err: clientErr}
				return
			}
			clusterCtx, cancel := context.WithTimeout(overallCtx, healthCheckPerClusterTimeout)
			defer cancel()
			if pingErr := pingCluster(clusterCtx, client); pingErr != nil {
				slog.Debug("health check: cluster ping failed", "cluster_id", clusterID, "error", pingErr)
				ch <- result{clusterID: clusterID, err: pingErr}
				return
			}
			ch <- result{clusterID: clusterID, err: nil}
		}(cl.ID)
	}

	var lastErr error
	for range clusters {
		select {
		case res := <-ch:
			if res.err == nil {
				// At least one cluster is reachable.
				return nil
			}
			lastErr = res.err
		case <-overallCtx.Done():
			slog.Warn("all registered clusters unreachable", "count", len(clusters), "last_error", lastErr)
			return fmt.Errorf("all %d registered clusters are unreachable", len(clusters))
		}
	}

	slog.Warn("all registered clusters unreachable", "count", len(clusters), "last_error", lastErr)
	return fmt.Errorf("all %d registered clusters are unreachable", len(clusters))
}

// pingCluster performs a lightweight version ping against a k8s cluster.
// Uses RESTClient discovery with fallback to ServerVersion for test fakes.
func pingCluster(ctx context.Context, client *k8s.Client) error {
	if restClient := client.Clientset().Discovery().RESTClient(); restClient != nil {
		result := restClient.Get().AbsPath("/version").Do(ctx)
		return result.Error()
	}
	_, err := client.Clientset().Discovery().ServerVersion()
	return err
}

// ClusterExists checks whether a cluster with the given ID exists in the
// underlying repository, without building any k8s/helm clients.
// Only a definitive not-found returns false; transient backend failures
// conservatively report true to avoid misclassifying existing clusters.
func (r *Registry) ClusterExists(clusterID string) bool {
	_, err := r.clusterRepo.FindByID(clusterID)
	if err == nil {
		return true
	}
	if isNotFoundError(err) {
		return false
	}
	// Transient error — conservatively assume exists.
	return true
}

// buildClients creates k8s + helm clients for a cluster.
// It does not perform any locking itself; callers are responsible for
// holding r.mu when mutating r.clients, but this function may be invoked
// without the registry lock to avoid holding the mutex during I/O.
func (r *Registry) buildClients(cluster *models.Cluster) (*ClusterClients, error) {
	var kubeconfigPath string
	var tempPath string

	switch {
	case cluster.KubeconfigPath != "":
		kubeconfigPath = cluster.KubeconfigPath

	case cluster.KubeconfigData != "":
		// KubeconfigData is already decrypted by the repository layer.
		// Write it as-is to a temp file for client construction.
		f, err := os.CreateTemp("", "k8s-stack-kubeconfig-*")
		if err != nil {
			return nil, fmt.Errorf("create temp kubeconfig file: %w", err)
		}

		if err := f.Chmod(0600); err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}

		if _, err := f.Write([]byte(cluster.KubeconfigData)); err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}

		if err := f.Close(); err != nil {
			_ = os.Remove(f.Name())
		}

		kubeconfigPath = f.Name()
		tempPath = f.Name()

	default:
		return nil, fmt.Errorf("cluster has neither kubeconfig path nor kubeconfig data")
	}

	k8sClient, err := r.k8sFactory(kubeconfigPath)
	if err != nil {
		if tempPath != "" {
			_ = os.Remove(tempPath)
		}
		return nil, fmt.Errorf("create k8s client: %w", err)
	}

	helmClient := r.helmFactory(r.helmBinary, kubeconfigPath, r.helmTimeout)

	return &ClusterClients{
		K8s:            k8sClient,
		Helm:           helmClient,
		kubeconfigPath: tempPath,
	}, nil
}

// cleanupTempKubeconfig removes the temp kubeconfig file if one was created.
func cleanupTempKubeconfig(cc *ClusterClients) {
	if cc.kubeconfigPath != "" {
		_ = os.Remove(cc.kubeconfigPath)
	}
}

// isNotFoundError returns true if the error wraps dberrors.ErrNotFound,
// indicating the record definitively does not exist (vs a transient failure).
func isNotFoundError(err error) bool {
	return errors.Is(err, dberrors.ErrNotFound)
}

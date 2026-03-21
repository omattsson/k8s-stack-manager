// Package cluster manages per-cluster Kubernetes and Helm client pools with
// lazy initialization and thread-safe caching.
package cluster

import (
	"errors"
	"fmt"
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

	// Slow path: write lock for cache miss.
	r.mu.Lock()
	defer r.mu.Unlock()

	// Double-check after acquiring write lock.
	if cc, ok := r.clients[clusterID]; ok {
		return cc, nil
	}

	cluster, err := r.clusterRepo.FindByID(clusterID)
	if err != nil {
		return nil, fmt.Errorf("cluster %s: %w", clusterID, err)
	}

	cc, err := r.buildClients(cluster)
	if err != nil {
		return nil, fmt.Errorf("cluster %s: build clients: %w", clusterID, err)
	}

	r.clients[clusterID] = cc
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

// buildClients creates k8s + helm clients for a cluster. Must be called under write lock.
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
			f.Close()
			os.Remove(f.Name())
			return nil, fmt.Errorf("set temp kubeconfig permissions: %w", err)
		}

		if _, err := f.Write([]byte(cluster.KubeconfigData)); err != nil {
			f.Close()
			os.Remove(f.Name())
			return nil, fmt.Errorf("write temp kubeconfig: %w", err)
		}

		if err := f.Close(); err != nil {
			os.Remove(f.Name())
			return nil, fmt.Errorf("close temp kubeconfig: %w", err)
		}

		kubeconfigPath = f.Name()
		tempPath = f.Name()

	default:
		return nil, fmt.Errorf("cluster has neither kubeconfig path nor kubeconfig data")
	}

	k8sClient, err := r.k8sFactory(kubeconfigPath)
	if err != nil {
		if tempPath != "" {
			os.Remove(tempPath)
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
		os.Remove(cc.kubeconfigPath)
	}
}

// isNotFoundError returns true if the error wraps dberrors.ErrNotFound,
// indicating the record definitively does not exist (vs a transient failure).
func isNotFoundError(err error) bool {
	return errors.Is(err, dberrors.ErrNotFound)
}

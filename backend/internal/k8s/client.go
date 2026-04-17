// Package k8s provides a Kubernetes client wrapper for namespace management
// and resource status monitoring used by the stack deployment pipeline.
package k8s

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Client wraps the Kubernetes clientset for namespace and resource operations.
type Client struct {
	clientset kubernetes.Interface
}

// NewClient creates a Kubernetes client.
// When kubeconfigPath is provided, it is used directly (no in-cluster fallback)
// so that multi-cluster routing always targets the intended cluster.
// When kubeconfigPath is empty, it tries in-cluster config first, then the
// default kubeconfig path (~/.kube/config).
func NewClient(kubeconfigPath string) (*Client, error) {
	var config *rest.Config
	var err error

	if kubeconfigPath != "" {
		// Explicit kubeconfig — use it directly without in-cluster fallback.
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			return nil, fmt.Errorf("build kubeconfig from %s: %w", kubeconfigPath, err)
		}
	} else {
		// No kubeconfig path — try in-cluster config first, then default file.
		config, err = rest.InClusterConfig()
		if err != nil {
			home, _ := os.UserHomeDir()
			defaultPath := filepath.Join(home, ".kube", "config")
			config, err = clientcmd.BuildConfigFromFlags("", defaultPath)
			if err != nil {
				return nil, fmt.Errorf("build kubeconfig: %w", err)
			}
		}
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create kubernetes client: %w", err)
	}

	slog.Info("Kubernetes client initialized", "host", config.Host)
	return &Client{clientset: clientset}, nil
}

// NewClientFromInterface creates a Client from an existing kubernetes.Interface.
// This is primarily useful for testing with a fake clientset.
func NewClientFromInterface(cs kubernetes.Interface) *Client {
	return &Client{clientset: cs}
}

// EnsureNamespace creates a namespace if it doesn't exist.
func (c *Client) EnsureNamespace(ctx context.Context, name string) error {
	exists, err := c.NamespaceExists(ctx, name)
	if err != nil {
		return fmt.Errorf("check namespace %q: %w", name, err)
	}
	if exists {
		slog.Debug("Namespace already exists", "namespace", name)
		return nil
	}

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"managed-by": "k8s-stack-manager",
			},
		},
	}

	_, err = c.clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil {
		if k8serrors.IsAlreadyExists(err) {
			// Race condition: another caller created it between our check and create.
			return nil
		}
		return fmt.Errorf("create namespace %q: %w", name, err)
	}

	slog.Info("Namespace created", "namespace", name)
	return nil
}

// DeleteNamespace deletes a namespace.
func (c *Client) DeleteNamespace(ctx context.Context, name string) error {
	err := c.clientset.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			slog.Debug("Namespace not found, nothing to delete", "namespace", name)
			return nil
		}
		return fmt.Errorf("delete namespace %q: %w", name, err)
	}

	slog.Info("Namespace deleted", "namespace", name)
	return nil
}

// NamespaceExists checks if a namespace exists.
func (c *Client) NamespaceExists(ctx context.Context, name string) (bool, error) {
	_, err := c.clientset.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("get namespace %q: %w", name, err)
	}
	return true, nil
}

// Clientset returns the underlying kubernetes.Interface for advanced operations.
func (c *Client) Clientset() kubernetes.Interface {
	return c.clientset
}

// CopySecret copies a secret from a source namespace to a target namespace.
// Used for replicating shared TLS certs (e.g. a klaradocker-issued wildcard cert
// held in kvk-system) into each stack namespace so ingresses can reference it.
// If the target secret already exists, its data and type are updated to match
// the source. Source secret must exist; this function returns an error otherwise.
func (c *Client) CopySecret(ctx context.Context, sourceNS, sourceName, targetNS, targetName string) error {
	src, err := c.clientset.CoreV1().Secrets(sourceNS).Get(ctx, sourceName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get source secret %s/%s: %w", sourceNS, sourceName, err)
	}

	target := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      targetName,
			Namespace: targetNS,
			Labels: map[string]string{
				"managed-by":           "k8s-stack-manager",
				"klaravik.se/copied-from-namespace": sourceNS,
				"klaravik.se/copied-from-secret":    sourceName,
			},
		},
		Type: src.Type,
		Data: src.Data,
	}

	existing, err := c.clientset.CoreV1().Secrets(targetNS).Get(ctx, targetName, metav1.GetOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("get target secret %s/%s: %w", targetNS, targetName, err)
	}

	if k8serrors.IsNotFound(err) {
		_, err = c.clientset.CoreV1().Secrets(targetNS).Create(ctx, target, metav1.CreateOptions{})
		if err != nil && !k8serrors.IsAlreadyExists(err) {
			return fmt.Errorf("create target secret %s/%s: %w", targetNS, targetName, err)
		}
		slog.Info("Copied secret", "from", sourceNS+"/"+sourceName, "to", targetNS+"/"+targetName)
		return nil
	}

	existing.Data = src.Data
	existing.Type = src.Type
	if existing.Labels == nil {
		existing.Labels = map[string]string{}
	}
	for k, v := range target.Labels {
		existing.Labels[k] = v
	}
	_, err = c.clientset.CoreV1().Secrets(targetNS).Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("update target secret %s/%s: %w", targetNS, targetName, err)
	}
	slog.Debug("Updated copied secret", "from", sourceNS+"/"+sourceName, "to", targetNS+"/"+targetName)
	return nil
}

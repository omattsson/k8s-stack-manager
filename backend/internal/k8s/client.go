// Package k8s provides a Kubernetes client wrapper for namespace management
// and resource status monitoring used by the stack deployment pipeline.
package k8s

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
	clientset  kubernetes.Interface
	restConfig *rest.Config
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
	return &Client{clientset: clientset, restConfig: config}, nil
}

// NewClientFromInterface creates a Client from an existing kubernetes.Interface.
// This is primarily useful for testing with a fake clientset. The restConfig
// is left nil; operations that require it (e.g. pod exec) will error when called.
func NewClientFromInterface(cs kubernetes.Interface) *Client {
	return &Client{clientset: cs}
}

// RESTConfig returns the underlying *rest.Config, or nil when the client was
// constructed from a fake (e.g. in tests). Helpers that need a REST config
// (such as pod exec via SPDY) must handle the nil case by returning a clear error.
func (c *Client) RESTConfig() *rest.Config {
	return c.restConfig
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

type dockerConfigJSON struct {
	Auths map[string]dockerConfigEntry `json:"auths"`
}

type dockerConfigEntry struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Auth     string `json:"auth"`
}

// EnsureDockerRegistrySecret creates or updates a docker-registry type secret
// in the given namespace. Used for automatic image pull secret provisioning
// so that pods can pull images from private registries like ACR.
func (c *Client) EnsureDockerRegistrySecret(ctx context.Context, namespace, secretName, server, username, password string) error {
	cfg := dockerConfigJSON{
		Auths: map[string]dockerConfigEntry{
			server: {
				Username: username,
				Password: password,
				Auth:     base64.StdEncoding.EncodeToString([]byte(username + ":" + password)),
			},
		},
	}
	cfgBytes, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal docker config: %w", err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: namespace,
			Labels: map[string]string{
				"managed-by":                             "k8s-stack-manager",
				"k8s-stack-manager.io/image-pull-secret": "true",
			},
			Annotations: map[string]string{
				"k8s-stack-manager.io/registry": server,
			},
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: cfgBytes,
		},
	}

	existing, err := c.clientset.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		if !k8serrors.IsNotFound(err) {
			return fmt.Errorf("get existing secret %s/%s: %w", namespace, secretName, err)
		}
		// Secret doesn't exist — create it.
		if _, err := c.clientset.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
			if !k8serrors.IsAlreadyExists(err) {
				return fmt.Errorf("create secret %s/%s: %w", namespace, secretName, err)
			}
			// Race: another caller created it between Get and Create — fall through to update.
			existing, err = c.clientset.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("get secret after create race %s/%s: %w", namespace, secretName, err)
			}
		} else {
			slog.Info("Created image pull secret", "namespace", namespace, "secret", secretName, "registry", server)
			return nil
		}
	}

	// Update existing secret with fresh credentials.
	existing.Data = secret.Data
	existing.Type = secret.Type
	if existing.Labels == nil {
		existing.Labels = map[string]string{}
	}
	for k, v := range secret.Labels {
		existing.Labels[k] = v
	}
	if existing.Annotations == nil {
		existing.Annotations = map[string]string{}
	}
	for k, v := range secret.Annotations {
		existing.Annotations[k] = v
	}
	if _, err := c.clientset.CoreV1().Secrets(namespace).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update secret %s/%s: %w", namespace, secretName, err)
	}
	slog.Debug("Updated image pull secret", "namespace", namespace, "secret", secretName, "registry", server)
	return nil
}

// CopySecret copies a secret from a source namespace to a target namespace.
// Used for replicating shared TLS certificates (for example, a pre-existing
// wildcard TLS secret stored in a shared namespace) into each stack namespace
// so ingresses can reference it. If the target secret already exists, its data
// and type are updated to match the source. Source secret must exist; this
// function returns an error otherwise.
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
				"managed-by": "k8s-stack-manager",
				"k8s-stack-manager.io/copied-from-namespace": sourceNS,
				"k8s-stack-manager.io/copied-from-secret":    sourceName,
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
		if err == nil {
			slog.Info("Copied secret", "from", sourceNS+"/"+sourceName, "to", targetNS+"/"+targetName)
			return nil
		}
		if !k8serrors.IsAlreadyExists(err) {
			return fmt.Errorf("create target secret %s/%s: %w", targetNS, targetName, err)
		}
		// Race: another caller created the secret between our Get and Create.
		// Fall through to fetch the existing one and update its data to match.
		existing, err = c.clientset.CoreV1().Secrets(targetNS).Get(ctx, targetName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("get target secret after create race %s/%s: %w", targetNS, targetName, err)
		}
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

package k8s

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AccessRoleBindingName is the name of the RoleBinding that gives the
// backend service account its rights in a stack namespace.
const AccessRoleBindingName = "k8s-stack-manager-access"

// AccessBinding describes the namespace-scoped access of the backend: in
// every stack namespace a RoleBinding binds ServiceAccount (in
// ServiceAccountNamespace) to ClusterRole. The backend then needs no
// cluster-wide rights on namespaced objects (STACK_ACCESS_CLUSTER_ROLE,
// STACK_ACCESS_SERVICE_ACCOUNT).
type AccessBinding struct {
	ClusterRole             string
	ServiceAccount          string
	ServiceAccountNamespace string
}

// SetAccessBinding sets the access binding that EnsureNamespace applies to
// every stack namespace. nil disables it. Set once before the client is
// shared.
func (c *Client) SetAccessBinding(b *AccessBinding) {
	if b == nil || b.ClusterRole == "" || b.ServiceAccount == "" || b.ServiceAccountNamespace == "" {
		c.accessBinding = nil
		return
	}
	cp := *b
	c.accessBinding = &cp
}

// HasAccessBinding reports whether the client applies an access binding.
func (c *Client) HasAccessBinding() bool {
	return c.accessBinding != nil
}

// ensureAccessBinding creates or corrects the access RoleBinding in the
// namespace. It returns true when it wrote the RoleBinding. Without an
// access binding it does nothing.
func (c *Client) ensureAccessBinding(ctx context.Context, namespace string) (bool, error) {
	b := c.accessBinding
	if b == nil {
		return false, nil
	}
	rb, err := c.clientset.RbacV1().RoleBindings(namespace).Get(ctx, AccessRoleBindingName, metav1.GetOptions{})
	switch {
	case err == nil:
		if rb.RoleRef.Kind == "ClusterRole" && rb.RoleRef.Name == b.ClusterRole &&
			len(rb.Subjects) == 1 && rb.Subjects[0].Kind == "ServiceAccount" &&
			rb.Subjects[0].Name == b.ServiceAccount && rb.Subjects[0].Namespace == b.ServiceAccountNamespace {
			return false, nil
		}
		if rb.RoleRef.Name != b.ClusterRole || rb.RoleRef.Kind != "ClusterRole" {
			// roleRef cannot change: delete and create again.
			if delErr := c.clientset.RbacV1().RoleBindings(namespace).Delete(ctx, AccessRoleBindingName, metav1.DeleteOptions{}); delErr != nil && !k8serrors.IsNotFound(delErr) {
				return false, fmt.Errorf("delete RoleBinding %s/%s: %w", namespace, AccessRoleBindingName, delErr)
			}
		}
	case !k8serrors.IsNotFound(err):
		return false, fmt.Errorf("get RoleBinding %s/%s: %w", namespace, AccessRoleBindingName, err)
	}
	if err := c.EnsureRoleBinding(ctx, namespace, AccessRoleBindingName, b.ClusterRole, b.ServiceAccount, b.ServiceAccountNamespace); err != nil {
		return false, err
	}
	return true, nil
}

// EnsureAccessBindings applies the access RoleBinding to every namespace
// with the label managed-by=k8s-stack-manager. It is used once per
// leadership term and periodically, so namespaces created before the
// namespace-scoped mode (or a deleted RoleBinding) get their access back
// without a redeploy. It returns the number of RoleBindings written.
func (c *Client) EnsureAccessBindings(ctx context.Context) (int, error) {
	if c.accessBinding == nil {
		return 0, nil
	}
	list, err := c.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: ManagedByLabelKey + "=" + ManagedByLabelValue,
	})
	if err != nil {
		return 0, fmt.Errorf("list stack namespaces: %w", err)
	}
	written := 0
	var errs []error
	for _, ns := range list.Items {
		if ns.DeletionTimestamp != nil {
			continue
		}
		changed, err := c.ensureAccessBinding(ctx, ns.Name)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if changed {
			written++
			slog.Info("access RoleBinding applied", "namespace", ns.Name)
		}
	}
	return written, errors.Join(errs...)
}

package config

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// StackAccessConfig is the namespace-scoped access of the backend: in every
// stack namespace on an in-cluster cluster a RoleBinding binds the service
// account to the ClusterRole. Empty = off (the backend uses its cluster-wide
// rights, as before).
type StackAccessConfig struct {
	ClusterRole             string
	ServiceAccount          string
	ServiceAccountNamespace string
}

// Enabled reports whether namespace-scoped access is configured.
func (s StackAccessConfig) Enabled() bool { return s.ClusterRole != "" }

// ParseStackAccess reads STACK_ACCESS_CLUSTER_ROLE and
// STACK_ACCESS_SERVICE_ACCOUNT ("<namespace>/<name>"). Both must be set, or
// neither.
func ParseStackAccess(clusterRole, serviceAccount string) (StackAccessConfig, error) {
	clusterRole = strings.TrimSpace(clusterRole)
	serviceAccount = strings.TrimSpace(serviceAccount)
	if clusterRole == "" && serviceAccount == "" {
		return StackAccessConfig{}, nil
	}
	if clusterRole == "" || serviceAccount == "" {
		return StackAccessConfig{}, fmt.Errorf("STACK_ACCESS_CLUSTER_ROLE and STACK_ACCESS_SERVICE_ACCOUNT must both be set, or neither")
	}
	if errs := validation.IsDNS1123Subdomain(clusterRole); len(errs) > 0 {
		return StackAccessConfig{}, fmt.Errorf("STACK_ACCESS_CLUSTER_ROLE %q: %s", clusterRole, strings.Join(errs, "; "))
	}
	ns, name, ok := strings.Cut(serviceAccount, "/")
	if !ok || ns == "" || name == "" {
		return StackAccessConfig{}, fmt.Errorf("STACK_ACCESS_SERVICE_ACCOUNT %q: must be <namespace>/<name>", serviceAccount)
	}
	if errs := validation.IsDNS1123Label(ns); len(errs) > 0 {
		return StackAccessConfig{}, fmt.Errorf("STACK_ACCESS_SERVICE_ACCOUNT namespace %q: %s", ns, strings.Join(errs, "; "))
	}
	if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
		return StackAccessConfig{}, fmt.Errorf("STACK_ACCESS_SERVICE_ACCOUNT name %q: %s", name, strings.Join(errs, "; "))
	}
	return StackAccessConfig{ClusterRole: clusterRole, ServiceAccount: name, ServiceAccountNamespace: ns}, nil
}

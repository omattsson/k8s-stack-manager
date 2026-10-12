package config

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// reservedNamespaceLabelKey is the label the deployer always sets on stack
// namespaces (managed-by=k8s-stack-manager). NAMESPACE_LABELS must not set it.
const reservedNamespaceLabelKey = "managed-by"

// ParseNamespaceLabels parses NAMESPACE_LABELS: comma-separated key=value
// pairs, for example
// "pod-security.kubernetes.io/enforce=baseline,pod-security.kubernetes.io/warn=restricted".
// Spaces around entries, keys and values are removed; empty entries are
// skipped. Each key must be a valid Kubernetes label key and each value a
// valid label value. The key managed-by is reserved; a duplicate key is an
// error. An empty setting returns nil (no extra labels).
func ParseNamespaceLabels(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	labels := make(map[string]string)
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			return nil, fmt.Errorf("entry %q is not in the form key=value", entry)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if errs := validation.IsQualifiedName(key); len(errs) > 0 {
			return nil, fmt.Errorf("label key %q is not valid: %s", key, strings.Join(errs, "; "))
		}
		if key == reservedNamespaceLabelKey {
			return nil, fmt.Errorf("label key %q is reserved (the backend always sets managed-by=k8s-stack-manager)", key)
		}
		if errs := validation.IsValidLabelValue(value); len(errs) > 0 {
			return nil, fmt.Errorf("value %q of label %q is not valid: %s", value, key, strings.Join(errs, "; "))
		}
		if _, dup := labels[key]; dup {
			return nil, fmt.Errorf("label key %q is set more than once", key)
		}
		labels[key] = value
	}
	if len(labels) == 0 {
		return nil, nil
	}
	return labels, nil
}

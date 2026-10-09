package models

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
)

// Quota value sources used in ValidateEffectiveQuota messages.
const (
	quotaSourceCluster  = "cluster quota"
	quotaSourceOverride = "instance override"
)

// MergeQuotaOverride applies a per-instance override on top of the cluster
// quota. Non-empty override fields replace the cluster value; empty or nil
// fields fall back to the cluster value. A nil cluster means an empty base;
// a nil override returns a copy of the cluster quota. The deployer applies
// the result to the instance namespace.
func MergeQuotaOverride(cluster *ResourceQuotaConfig, override *InstanceQuotaOverride) *ResourceQuotaConfig {
	merged := ResourceQuotaConfig{}
	if cluster != nil {
		merged = *cluster
	}
	if override == nil {
		return &merged
	}
	if override.CPURequest != "" {
		merged.CPURequest = override.CPURequest
	}
	if override.CPULimit != "" {
		merged.CPULimit = override.CPULimit
	}
	if override.MemoryRequest != "" {
		merged.MemoryRequest = override.MemoryRequest
	}
	if override.MemoryLimit != "" {
		merged.MemoryLimit = override.MemoryLimit
	}
	if override.StorageLimit != "" {
		merged.StorageLimit = override.StorageLimit
	}
	if override.PodLimit != nil {
		merged.PodLimit = *override.PodLimit
	}
	return &merged
}

// effectiveQuotaField is one merged quota value and where it comes from.
type effectiveQuotaField struct {
	name   string
	value  string
	source string
	qty    resource.Quantity
}

// ValidateEffectiveQuota checks the quota that applies to an instance
// namespace: the cluster quota merged with the instance override (see
// MergeQuotaOverride). It applies the same rules as Validate on each object
// (valid, non-negative quantities; request not above limit; pod_limit not
// negative) and names the source of each value, for example:
//
//	cpu_request 4 (instance override) exceeds the effective cpu_limit 2 (cluster quota)
//
// Either argument may be nil.
func ValidateEffectiveQuota(cluster *ResourceQuotaConfig, override *InstanceQuotaOverride) error {
	if cluster == nil {
		cluster = &ResourceQuotaConfig{}
	}
	pick := func(name, clusterVal string, overrideVal func(*InstanceQuotaOverride) string) *effectiveQuotaField {
		if override != nil && overrideVal(override) != "" {
			return &effectiveQuotaField{name: name, value: overrideVal(override), source: quotaSourceOverride}
		}
		if clusterVal != "" {
			return &effectiveQuotaField{name: name, value: clusterVal, source: quotaSourceCluster}
		}
		return nil
	}

	cpuReq := pick("cpu_request", cluster.CPURequest, func(o *InstanceQuotaOverride) string { return o.CPURequest })
	cpuLim := pick("cpu_limit", cluster.CPULimit, func(o *InstanceQuotaOverride) string { return o.CPULimit })
	memReq := pick("memory_request", cluster.MemoryRequest, func(o *InstanceQuotaOverride) string { return o.MemoryRequest })
	memLim := pick("memory_limit", cluster.MemoryLimit, func(o *InstanceQuotaOverride) string { return o.MemoryLimit })
	storage := pick("storage_limit", cluster.StorageLimit, func(o *InstanceQuotaOverride) string { return o.StorageLimit })

	for _, f := range []*effectiveQuotaField{cpuReq, cpuLim, memReq, memLim, storage} {
		if f == nil {
			continue
		}
		q, err := resource.ParseQuantity(f.value)
		if err != nil {
			return fmt.Errorf("%s %q (%s): invalid quantity", f.name, f.value, f.source)
		}
		if q.Sign() < 0 {
			return fmt.Errorf("%s %s (%s): must not be negative", f.name, f.value, f.source)
		}
		f.qty = q
	}

	for _, pair := range [][2]*effectiveQuotaField{{cpuReq, cpuLim}, {memReq, memLim}} {
		req, lim := pair[0], pair[1]
		if req == nil || lim == nil {
			continue
		}
		if req.qty.Cmp(lim.qty) > 0 {
			return fmt.Errorf("%s %s (%s) exceeds the effective %s %s (%s)",
				req.name, req.value, req.source, lim.name, lim.value, lim.source)
		}
	}

	podLimit, podSource := cluster.PodLimit, quotaSourceCluster
	if override != nil && override.PodLimit != nil {
		podLimit, podSource = *override.PodLimit, quotaSourceOverride
	}
	if podLimit < 0 {
		return fmt.Errorf("pod_limit %d (%s): must not be negative", podLimit, podSource)
	}
	return nil
}

// CheckOverrideWithinClusterQuota checks that each value of the override is
// not above the same value of the cluster quota. Callers apply it to users
// that are not admin or devops: those users may lower a quota, not raise it.
//
// Rules:
//
//   - A field that is empty in the override has no check (the cluster value
//     applies).
//   - A field without a cluster value (empty string, or pod_limit 0) has no
//     cap.
//   - Quantities are compared as Kubernetes quantities, so "16" equals
//     "16000m" and "24Gi" equals "25769803776".
//   - An override pod_limit of 0 means no pod limit, so it is above any
//     cluster pod_limit.
//   - A field equal to the same field of existing (the stored override, may be
//     nil) passes, also above the cluster quota. So an owner can change one
//     field without losing an admin grant on another field. A raise or a new
//     value above the cluster quota still fails.
//
// A nil cluster or nil override passes. The error names the field and both
// values, for example "cpu_limit 64 exceeds the cluster quota 16". Call it
// after Validate on the override, so the override quantities are valid. A
// cluster value that does not parse gives no cap for that field.
func CheckOverrideWithinClusterQuota(cluster *ResourceQuotaConfig, override, existing *InstanceQuotaOverride) error {
	if cluster == nil || override == nil {
		return nil
	}
	if existing == nil {
		existing = &InstanceQuotaOverride{}
	}
	fields := []struct {
		name, overrideVal, clusterVal, existingVal string
	}{
		{"cpu_request", override.CPURequest, cluster.CPURequest, existing.CPURequest},
		{"cpu_limit", override.CPULimit, cluster.CPULimit, existing.CPULimit},
		{"memory_request", override.MemoryRequest, cluster.MemoryRequest, existing.MemoryRequest},
		{"memory_limit", override.MemoryLimit, cluster.MemoryLimit, existing.MemoryLimit},
		{"storage_limit", override.StorageLimit, cluster.StorageLimit, existing.StorageLimit},
	}
	for _, f := range fields {
		if f.overrideVal == "" || f.clusterVal == "" {
			continue
		}
		capQty, err := resource.ParseQuantity(f.clusterVal)
		if err != nil {
			continue
		}
		qty, err := resource.ParseQuantity(f.overrideVal)
		if err != nil {
			return fmt.Errorf("%s %q: invalid quantity", f.name, f.overrideVal)
		}
		if qty.Cmp(capQty) <= 0 {
			continue
		}
		if f.existingVal != "" {
			if prev, prevErr := resource.ParseQuantity(f.existingVal); prevErr == nil && qty.Cmp(prev) == 0 {
				continue
			}
		}
		return fmt.Errorf("%s %s exceeds the cluster quota %s", f.name, f.overrideVal, f.clusterVal)
	}
	if override.PodLimit != nil && cluster.PodLimit > 0 {
		pods := *override.PodLimit
		if existing.PodLimit != nil && *existing.PodLimit == pods {
			return nil
		}
		if pods == 0 {
			return fmt.Errorf("pod_limit 0 (no limit) exceeds the cluster quota %d", cluster.PodLimit)
		}
		if pods > cluster.PodLimit {
			return fmt.Errorf("pod_limit %d exceeds the cluster quota %d", pods, cluster.PodLimit)
		}
	}
	return nil
}

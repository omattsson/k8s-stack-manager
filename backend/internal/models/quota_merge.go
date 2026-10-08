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

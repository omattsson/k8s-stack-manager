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

// quotaQuantityField is one quantity field of ResourceQuotaConfig and the
// same field of InstanceQuotaOverride. The getters return a pointer, so the
// merge can also set the field.
type quotaQuantityField struct {
	cluster  func(*ResourceQuotaConfig) *string
	override func(*InstanceQuotaOverride) *string
	name     string
}

// quotaQuantityFields lists the quantity fields in the order of the
// validation messages. pod_limit is an int and is handled on its own.
var quotaQuantityFields = []quotaQuantityField{
	{name: "cpu_request", cluster: func(q *ResourceQuotaConfig) *string { return &q.CPURequest }, override: func(o *InstanceQuotaOverride) *string { return &o.CPURequest }},
	{name: "cpu_limit", cluster: func(q *ResourceQuotaConfig) *string { return &q.CPULimit }, override: func(o *InstanceQuotaOverride) *string { return &o.CPULimit }},
	{name: "memory_request", cluster: func(q *ResourceQuotaConfig) *string { return &q.MemoryRequest }, override: func(o *InstanceQuotaOverride) *string { return &o.MemoryRequest }},
	{name: "memory_limit", cluster: func(q *ResourceQuotaConfig) *string { return &q.MemoryLimit }, override: func(o *InstanceQuotaOverride) *string { return &o.MemoryLimit }},
	{name: "storage_limit", cluster: func(q *ResourceQuotaConfig) *string { return &q.StorageLimit }, override: func(o *InstanceQuotaOverride) *string { return &o.StorageLimit }},
	{name: "ephemeral_storage_limit", cluster: func(q *ResourceQuotaConfig) *string { return &q.EphemeralStorageLimit }, override: func(o *InstanceQuotaOverride) *string { return &o.EphemeralStorageLimit }},
	{name: "container_cpu_request", cluster: func(q *ResourceQuotaConfig) *string { return &q.ContainerCPURequest }, override: func(o *InstanceQuotaOverride) *string { return &o.ContainerCPURequest }},
	{name: "container_cpu_limit", cluster: func(q *ResourceQuotaConfig) *string { return &q.ContainerCPULimit }, override: func(o *InstanceQuotaOverride) *string { return &o.ContainerCPULimit }},
	{name: "container_cpu_max", cluster: func(q *ResourceQuotaConfig) *string { return &q.ContainerCPUMax }, override: func(o *InstanceQuotaOverride) *string { return &o.ContainerCPUMax }},
	{name: "container_memory_request", cluster: func(q *ResourceQuotaConfig) *string { return &q.ContainerMemoryRequest }, override: func(o *InstanceQuotaOverride) *string { return &o.ContainerMemoryRequest }},
	{name: "container_memory_limit", cluster: func(q *ResourceQuotaConfig) *string { return &q.ContainerMemoryLimit }, override: func(o *InstanceQuotaOverride) *string { return &o.ContainerMemoryLimit }},
	{name: "container_memory_max", cluster: func(q *ResourceQuotaConfig) *string { return &q.ContainerMemoryMax }, override: func(o *InstanceQuotaOverride) *string { return &o.ContainerMemoryMax }},
	{name: "container_ephemeral_storage_request", cluster: func(q *ResourceQuotaConfig) *string { return &q.ContainerEphemeralStorageRequest }, override: func(o *InstanceQuotaOverride) *string { return &o.ContainerEphemeralStorageRequest }},
	{name: "container_ephemeral_storage_limit", cluster: func(q *ResourceQuotaConfig) *string { return &q.ContainerEphemeralStorageLimit }, override: func(o *InstanceQuotaOverride) *string { return &o.ContainerEphemeralStorageLimit }},
}

// quotaOrderRules lists field pairs where the first value must not exceed
// the second when both are set:
//
//   - a request is not above its limit (namespace and per container), and a
//     per-container default is not above the per-container max;
//   - a per-container default is not above the namespace total of the same
//     resource, else every container that uses the default breaks the
//     namespace ResourceQuota. The per-container max may be above the
//     namespace total (it only caps).
var quotaOrderRules = [][2]string{
	{"cpu_request", "cpu_limit"},
	{"memory_request", "memory_limit"},
	{"container_cpu_request", "container_cpu_limit"},
	{"container_cpu_limit", "container_cpu_max"},
	{"container_cpu_request", "container_cpu_max"},
	{"container_memory_request", "container_memory_limit"},
	{"container_memory_limit", "container_memory_max"},
	{"container_memory_request", "container_memory_max"},
	{"container_ephemeral_storage_request", "container_ephemeral_storage_limit"},
	{"container_cpu_request", "cpu_request"},
	{"container_cpu_request", "cpu_limit"},
	{"container_cpu_limit", "cpu_limit"},
	{"container_memory_request", "memory_request"},
	{"container_memory_request", "memory_limit"},
	{"container_memory_limit", "memory_limit"},
	{"container_ephemeral_storage_request", "ephemeral_storage_limit"},
	{"container_ephemeral_storage_limit", "ephemeral_storage_limit"},
}

// IsEmpty reports whether the quota sets no value: no quantity and no
// pod_limit. The deployer applies nothing for an empty quota.
func (rq *ResourceQuotaConfig) IsEmpty() bool {
	if rq.PodLimit != 0 {
		return false
	}
	for _, f := range quotaQuantityFields {
		if *f.cluster(rq) != "" {
			return false
		}
	}
	return true
}

// MergeQuotaOverride applies a per-instance override on top of the cluster
// quota. Non-empty override fields replace the cluster value; empty or nil
// fields fall back to the cluster value. The rule is the same for every
// field, also the per-container fields. A nil cluster means an empty base;
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
	for _, f := range quotaQuantityFields {
		if v := *f.override(override); v != "" {
			*f.cluster(&merged) = v
		}
	}
	if override.PodLimit != nil {
		merged.PodLimit = *override.PodLimit
	}
	return &merged
}

// effectiveQuotaField is one merged quota value and where it comes from.
type effectiveQuotaField struct {
	qty    resource.Quantity
	value  string
	source string
}

// ValidateEffectiveQuota checks the quota that applies to an instance
// namespace: the cluster quota merged with the instance override (see
// MergeQuotaOverride). It applies the same rules as Validate on each object
// (valid, non-negative quantities; the order rules of quotaOrderRules;
// pod_limit not negative) and names the source of each value, for example:
//
//	cpu_request 4 (instance override) exceeds the effective cpu_limit 2 (cluster quota)
//
// Either argument may be nil.
func ValidateEffectiveQuota(cluster *ResourceQuotaConfig, override *InstanceQuotaOverride) error {
	if cluster == nil {
		cluster = &ResourceQuotaConfig{}
	}

	effective := make(map[string]*effectiveQuotaField, len(quotaQuantityFields))
	for _, f := range quotaQuantityFields {
		var field *effectiveQuotaField
		if override != nil && *f.override(override) != "" {
			field = &effectiveQuotaField{value: *f.override(override), source: quotaSourceOverride}
		} else if v := *f.cluster(cluster); v != "" {
			field = &effectiveQuotaField{value: v, source: quotaSourceCluster}
		}
		if field == nil {
			continue
		}
		q, err := resource.ParseQuantity(field.value)
		if err != nil {
			return fmt.Errorf("%s %q (%s): invalid quantity", f.name, field.value, field.source)
		}
		if q.Sign() < 0 {
			return fmt.Errorf("%s %s (%s): must not be negative", f.name, field.value, field.source)
		}
		field.qty = q
		effective[f.name] = field
	}

	for _, rule := range quotaOrderRules {
		low, high := effective[rule[0]], effective[rule[1]]
		if low == nil || high == nil {
			continue
		}
		if low.qty.Cmp(high.qty) > 0 {
			return fmt.Errorf("%s %s (%s) exceeds the effective %s %s (%s)",
				rule[0], low.value, low.source, rule[1], high.value, high.source)
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
//   - Every quantity field has the check, also the per-container fields
//     (container_*_request, container_*_limit, container_*_max) and
//     ephemeral_storage_limit. A higher per-container default or max is a
//     larger grant, so a user may lower it but not raise it.
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
	for _, f := range quotaQuantityFields {
		overrideVal, clusterVal, existingVal := *f.override(override), *f.cluster(cluster), *f.override(existing)
		if overrideVal == "" || clusterVal == "" {
			continue
		}
		capQty, err := resource.ParseQuantity(clusterVal)
		if err != nil {
			continue
		}
		qty, err := resource.ParseQuantity(overrideVal)
		if err != nil {
			return fmt.Errorf("%s %q: invalid quantity", f.name, overrideVal)
		}
		if qty.Cmp(capQty) <= 0 {
			continue
		}
		if existingVal != "" {
			if prev, prevErr := resource.ParseQuantity(existingVal); prevErr == nil && qty.Cmp(prev) == 0 {
				continue
			}
		}
		return fmt.Errorf("%s %s exceeds the cluster quota %s", f.name, overrideVal, clusterVal)
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

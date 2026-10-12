package k8s

import (
	"context"
	"fmt"

	"backend/internal/models"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// quotaName is the name used for the managed ResourceQuota object.
const quotaName = "stack-manager-quota"

// limitRangeName is the name used for the managed LimitRange object.
const limitRangeName = "stack-manager-limits"

// NamespaceResourceUsage represents actual resource usage for a namespace.
type NamespaceResourceUsage struct {
	CPUUsed     string `json:"cpu_used"`
	CPULimit    string `json:"cpu_limit"`
	MemoryUsed  string `json:"memory_used"`
	MemoryLimit string `json:"memory_limit"`
	PodCount    int    `json:"pod_count"`
	PodLimit    int    `json:"pod_limit"`
}

// EnsureResourceQuota creates or updates a ResourceQuota in the given namespace
// based on the provided configuration.
func (c *Client) EnsureResourceQuota(ctx context.Context, namespace string, config *models.ResourceQuotaConfig) error {
	hard := corev1.ResourceList{}

	if config.CPURequest != "" {
		q, err := resource.ParseQuantity(config.CPURequest)
		if err != nil {
			return fmt.Errorf("parse cpu_request %q: %w", config.CPURequest, err)
		}
		hard[corev1.ResourceRequestsCPU] = q
	}
	if config.CPULimit != "" {
		q, err := resource.ParseQuantity(config.CPULimit)
		if err != nil {
			return fmt.Errorf("parse cpu_limit %q: %w", config.CPULimit, err)
		}
		hard[corev1.ResourceLimitsCPU] = q
	}
	if config.MemoryRequest != "" {
		q, err := resource.ParseQuantity(config.MemoryRequest)
		if err != nil {
			return fmt.Errorf("parse memory_request %q: %w", config.MemoryRequest, err)
		}
		hard[corev1.ResourceRequestsMemory] = q
	}
	if config.MemoryLimit != "" {
		q, err := resource.ParseQuantity(config.MemoryLimit)
		if err != nil {
			return fmt.Errorf("parse memory_limit %q: %w", config.MemoryLimit, err)
		}
		hard[corev1.ResourceLimitsMemory] = q
	}
	if config.StorageLimit != "" {
		q, err := resource.ParseQuantity(config.StorageLimit)
		if err != nil {
			return fmt.Errorf("parse storage_limit %q: %w", config.StorageLimit, err)
		}
		hard[corev1.ResourceRequestsStorage] = q
	}
	if config.EphemeralStorageLimit != "" {
		// One value caps both the requests and the limits of the namespace.
		q, err := resource.ParseQuantity(config.EphemeralStorageLimit)
		if err != nil {
			return fmt.Errorf("parse ephemeral_storage_limit %q: %w", config.EphemeralStorageLimit, err)
		}
		hard[corev1.ResourceRequestsEphemeralStorage] = q
		hard[corev1.ResourceLimitsEphemeralStorage] = q.DeepCopy()
	}
	if config.PodLimit > 0 {
		hard[corev1.ResourcePods] = *resource.NewQuantity(int64(config.PodLimit), resource.DecimalSI)
	}

	if len(hard) == 0 {
		return nil // nothing to enforce
	}

	quota := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{
			Name:      quotaName,
			Namespace: namespace,
			Labels: map[string]string{
				"managed-by": "k8s-stack-manager",
			},
		},
		Spec: corev1.ResourceQuotaSpec{
			Hard: hard,
		},
	}

	existing, err := c.clientset.CoreV1().ResourceQuotas(namespace).Get(ctx, quotaName, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			_, createErr := c.clientset.CoreV1().ResourceQuotas(namespace).Create(ctx, quota, metav1.CreateOptions{})
			if createErr != nil {
				return fmt.Errorf("create resource quota in %q: %w", namespace, createErr)
			}
			return nil
		}
		return fmt.Errorf("get resource quota in %q: %w", namespace, err)
	}

	existing.Spec.Hard = hard
	_, err = c.clientset.CoreV1().ResourceQuotas(namespace).Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("update resource quota in %q: %w", namespace, err)
	}
	return nil
}

// Fallback per-container defaults of the LimitRange. They apply when the
// quota sets a value for the resource (a namespace total or a per-container
// limit or max) but no per-container default. A ResourceQuota on cpu, memory
// or ephemeral-storage rejects a container without a request or limit, so the
// LimitRange must give one; it must never be the namespace total (one
// container would then take the whole namespace budget). Each fallback is
// capped at the namespace total and the per-container max (the smaller value
// wins), and the default limit is never below the default request.
const (
	fallbackContainerCPURequest              = "100m"
	fallbackContainerCPULimit                = "500m"
	fallbackContainerMemoryRequest           = "128Mi"
	fallbackContainerMemoryLimit             = "512Mi"
	fallbackContainerEphemeralStorageRequest = "128Mi"
	fallbackContainerEphemeralStorageLimit   = "1Gi"
)

// quotaQuantity is one named quota value ("" = not set).
type quotaQuantity struct {
	field string
	value string
}

// parse returns the parsed quantity, or nil when the value is empty.
func (q quotaQuantity) parse() (*resource.Quantity, error) {
	if q.value == "" {
		return nil, nil
	}
	parsed, err := resource.ParseQuantity(q.value)
	if err != nil {
		return nil, fmt.Errorf("parse %s %q: %w", q.field, q.value, err)
	}
	return &parsed, nil
}

// limitRangeResource holds the quota values of one resource (cpu, memory or
// ephemeral-storage) that decide its LimitRange entries.
type limitRangeResource struct {
	name            corev1.ResourceName
	nsRequest       quotaQuantity // namespace requests total
	nsLimit         quotaQuantity // namespace limits total
	request         quotaQuantity // per-container default request
	limit           quotaQuantity // per-container default limit
	max             quotaQuantity // per-container max
	fallbackRequest string
	fallbackLimit   string
}

// smallestQuantity returns a copy of the smallest of base and the non-nil caps.
func smallestQuantity(base resource.Quantity, caps ...*resource.Quantity) resource.Quantity {
	out := base.DeepCopy()
	for _, c := range caps {
		if c != nil && c.Cmp(out) < 0 {
			out = c.DeepCopy()
		}
	}
	return out
}

// entries returns the LimitRange defaultRequest, default and max of the
// resource (nil = not set):
//
//   - defaultRequest: the per-container request; else, when any value of the
//     resource is set, the fallback request capped at the namespace totals,
//     the per-container limit and the per-container max.
//   - default: the per-container limit; else, when the namespace limit or the
//     per-container max is set, the fallback limit capped at the namespace
//     limit and the per-container max, and not below defaultRequest.
//     (Without a default, Kubernetes would use the max as the default.)
//   - max: the per-container max, only when it is set.
func (r limitRangeResource) entries() (defaultRequest, defaultLimit, maxQty *resource.Quantity, err error) {
	parsed := make([]*resource.Quantity, 5)
	for i, q := range []quotaQuantity{r.nsRequest, r.nsLimit, r.request, r.limit, r.max} {
		if parsed[i], err = q.parse(); err != nil {
			return nil, nil, nil, err
		}
	}
	nsReq, nsLim, req, lim, maxQty := parsed[0], parsed[1], parsed[2], parsed[3], parsed[4]
	if nsReq == nil && nsLim == nil && req == nil && lim == nil && maxQty == nil {
		return nil, nil, nil, nil
	}

	defaultRequest = req
	if defaultRequest == nil {
		q := smallestQuantity(resource.MustParse(r.fallbackRequest), nsReq, nsLim, lim, maxQty)
		defaultRequest = &q
	}

	defaultLimit = lim
	if defaultLimit == nil && (nsLim != nil || maxQty != nil) {
		q := smallestQuantity(resource.MustParse(r.fallbackLimit), nsLim, maxQty)
		if q.Cmp(*defaultRequest) < 0 {
			q = defaultRequest.DeepCopy()
		}
		defaultLimit = &q
	}
	return defaultRequest, defaultLimit, maxQty, nil
}

// limitRangeResources returns the LimitRange inputs of config per resource.
func limitRangeResources(config *models.ResourceQuotaConfig) []limitRangeResource {
	return []limitRangeResource{
		{
			name:            corev1.ResourceCPU,
			nsRequest:       quotaQuantity{"cpu_request", config.CPURequest},
			nsLimit:         quotaQuantity{"cpu_limit", config.CPULimit},
			request:         quotaQuantity{"container_cpu_request", config.ContainerCPURequest},
			limit:           quotaQuantity{"container_cpu_limit", config.ContainerCPULimit},
			max:             quotaQuantity{"container_cpu_max", config.ContainerCPUMax},
			fallbackRequest: fallbackContainerCPURequest,
			fallbackLimit:   fallbackContainerCPULimit,
		},
		{
			name:            corev1.ResourceMemory,
			nsRequest:       quotaQuantity{"memory_request", config.MemoryRequest},
			nsLimit:         quotaQuantity{"memory_limit", config.MemoryLimit},
			request:         quotaQuantity{"container_memory_request", config.ContainerMemoryRequest},
			limit:           quotaQuantity{"container_memory_limit", config.ContainerMemoryLimit},
			max:             quotaQuantity{"container_memory_max", config.ContainerMemoryMax},
			fallbackRequest: fallbackContainerMemoryRequest,
			fallbackLimit:   fallbackContainerMemoryLimit,
		},
		{
			// ephemeral_storage_limit caps both requests and limits of the
			// namespace (see EnsureResourceQuota). There is no per-container max.
			name:            corev1.ResourceEphemeralStorage,
			nsRequest:       quotaQuantity{"ephemeral_storage_limit", config.EphemeralStorageLimit},
			nsLimit:         quotaQuantity{"ephemeral_storage_limit", config.EphemeralStorageLimit},
			request:         quotaQuantity{"container_ephemeral_storage_request", config.ContainerEphemeralStorageRequest},
			limit:           quotaQuantity{"container_ephemeral_storage_limit", config.ContainerEphemeralStorageLimit},
			fallbackRequest: fallbackContainerEphemeralStorageRequest,
			fallbackLimit:   fallbackContainerEphemeralStorageLimit,
		},
	}
}

// EnsureLimitRange creates or updates the LimitRange of the namespace
// (container defaults and max). The per-container fields of config set the
// values; see limitRangeResource.entries for the fallback when they are empty.
// The namespace totals (cpu_request, cpu_limit, ...) only cap the fallback
// defaults; they are never copied into a container default. A resource
// without any value gets no LimitRange entry; when no resource has a value,
// nothing is written.
func (c *Client) EnsureLimitRange(ctx context.Context, namespace string, config *models.ResourceQuotaConfig) error {
	defaultLimits := corev1.ResourceList{}
	defaultRequests := corev1.ResourceList{}
	maxLimits := corev1.ResourceList{}

	for _, r := range limitRangeResources(config) {
		req, lim, maxQty, err := r.entries()
		if err != nil {
			return err
		}
		if req != nil {
			defaultRequests[r.name] = *req
		}
		if lim != nil {
			defaultLimits[r.name] = *lim
		}
		if maxQty != nil {
			maxLimits[r.name] = *maxQty
		}
	}

	if len(defaultLimits) == 0 && len(defaultRequests) == 0 && len(maxLimits) == 0 {
		return nil // nothing to enforce
	}
	item := corev1.LimitRangeItem{Type: corev1.LimitTypeContainer}
	if len(defaultLimits) > 0 {
		item.Default = defaultLimits
	}
	if len(defaultRequests) > 0 {
		item.DefaultRequest = defaultRequests
	}
	if len(maxLimits) > 0 {
		item.Max = maxLimits
	}

	lr := &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{
			Name:      limitRangeName,
			Namespace: namespace,
			Labels: map[string]string{
				"managed-by": "k8s-stack-manager",
			},
		},
		Spec: corev1.LimitRangeSpec{
			Limits: []corev1.LimitRangeItem{item},
		},
	}

	existing, err := c.clientset.CoreV1().LimitRanges(namespace).Get(ctx, limitRangeName, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			_, createErr := c.clientset.CoreV1().LimitRanges(namespace).Create(ctx, lr, metav1.CreateOptions{})
			if createErr != nil {
				return fmt.Errorf("create limit range in %q: %w", namespace, createErr)
			}
			return nil
		}
		return fmt.Errorf("get limit range in %q: %w", namespace, err)
	}

	existing.Spec = lr.Spec
	_, err = c.clientset.CoreV1().LimitRanges(namespace).Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("update limit range in %q: %w", namespace, err)
	}
	return nil
}

// GetNamespaceResourceUsage returns actual resource usage for a namespace
// by reading the ResourceQuota status (which K8s populates with actual usage).
func (c *Client) GetNamespaceResourceUsage(ctx context.Context, namespace string) (*NamespaceResourceUsage, error) {
	usage := &NamespaceResourceUsage{}

	// Try to read the managed resource quota for usage data.
	quota, err := c.clientset.CoreV1().ResourceQuotas(namespace).Get(ctx, quotaName, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			// No quota set -- fall back to counting pods.
			pods, podErr := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
			if podErr != nil {
				return nil, fmt.Errorf("list pods in %q: %w", namespace, podErr)
			}
			usage.PodCount = len(pods.Items)
			return usage, nil
		}
		return nil, fmt.Errorf("get resource quota in %q: %w", namespace, err)
	}

	// Extract used values from quota status.
	if q, ok := quota.Status.Used[corev1.ResourceRequestsCPU]; ok {
		usage.CPUUsed = q.String()
	}
	if q, ok := quota.Status.Hard[corev1.ResourceLimitsCPU]; ok {
		usage.CPULimit = q.String()
	}
	if q, ok := quota.Status.Used[corev1.ResourceRequestsMemory]; ok {
		usage.MemoryUsed = q.String()
	}
	if q, ok := quota.Status.Hard[corev1.ResourceLimitsMemory]; ok {
		usage.MemoryLimit = q.String()
	}
	if q, ok := quota.Status.Used[corev1.ResourcePods]; ok {
		usage.PodCount = int(q.Value())
	}
	if q, ok := quota.Status.Hard[corev1.ResourcePods]; ok {
		usage.PodLimit = int(q.Value())
	}

	return usage, nil
}

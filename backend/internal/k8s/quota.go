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
	CPUUsed    string `json:"cpu_used"`
	CPULimit   string `json:"cpu_limit"`
	MemoryUsed string `json:"memory_used"`
	MemoryLimit string `json:"memory_limit"`
	PodCount   int    `json:"pod_count"`
	PodLimit   int    `json:"pod_limit"`
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

// EnsureLimitRange creates or updates a LimitRange in the given namespace
// with default request/limit values derived from the quota configuration.
func (c *Client) EnsureLimitRange(ctx context.Context, namespace string, config *models.ResourceQuotaConfig) error {
	defaultLimits := corev1.ResourceList{}
	defaultRequests := corev1.ResourceList{}

	if config.CPULimit != "" {
		q, err := resource.ParseQuantity(config.CPULimit)
		if err != nil {
			return fmt.Errorf("parse cpu_limit %q: %w", config.CPULimit, err)
		}
		defaultLimits[corev1.ResourceCPU] = q
	}
	if config.CPURequest != "" {
		q, err := resource.ParseQuantity(config.CPURequest)
		if err != nil {
			return fmt.Errorf("parse cpu_request %q: %w", config.CPURequest, err)
		}
		defaultRequests[corev1.ResourceCPU] = q
	}
	if config.MemoryLimit != "" {
		q, err := resource.ParseQuantity(config.MemoryLimit)
		if err != nil {
			return fmt.Errorf("parse memory_limit %q: %w", config.MemoryLimit, err)
		}
		defaultLimits[corev1.ResourceMemory] = q
	}
	if config.MemoryRequest != "" {
		q, err := resource.ParseQuantity(config.MemoryRequest)
		if err != nil {
			return fmt.Errorf("parse memory_request %q: %w", config.MemoryRequest, err)
		}
		defaultRequests[corev1.ResourceMemory] = q
	}

	if len(defaultLimits) == 0 && len(defaultRequests) == 0 {
		return nil // nothing to enforce
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
			Limits: []corev1.LimitRangeItem{
				{
					Type:            corev1.LimitTypeContainer,
					Default:         defaultLimits,
					DefaultRequest:  defaultRequests,
				},
			},
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
	if q, ok := quota.Status.Hard[corev1.ResourceRequestsCPU]; ok {
		usage.CPULimit = q.String()
	}
	if q, ok := quota.Status.Used[corev1.ResourceRequestsMemory]; ok {
		usage.MemoryUsed = q.String()
	}
	if q, ok := quota.Status.Hard[corev1.ResourceRequestsMemory]; ok {
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

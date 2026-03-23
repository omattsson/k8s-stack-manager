package k8s

import (
	"context"
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestEnsureResourceQuota(t *testing.T) {
	t.Parallel()

	t.Run("creates quota in namespace", func(t *testing.T) {
		t.Parallel()

		cs := fake.NewSimpleClientset(
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-test"}},
		)
		client := NewClientFromInterface(cs)

		config := &models.ResourceQuotaConfig{
			CPURequest:    "500m",
			CPULimit:      "2000m",
			MemoryRequest: "256Mi",
			MemoryLimit:   "1Gi",
			PodLimit:      10,
		}

		err := client.EnsureResourceQuota(context.Background(), "stack-test", config)
		require.NoError(t, err)

		// Verify the quota was created.
		quota, err := cs.CoreV1().ResourceQuotas("stack-test").Get(context.Background(), quotaName, metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, "k8s-stack-manager", quota.Labels["managed-by"])
		cpuReq := quota.Spec.Hard[corev1.ResourceRequestsCPU]
		assert.True(t, cpuReq.Equal(resource.MustParse("500m")), "cpu request: got %s", cpuReq.String())
		cpuLim := quota.Spec.Hard[corev1.ResourceLimitsCPU]
		assert.True(t, cpuLim.Equal(resource.MustParse("2000m")), "cpu limit: got %s", cpuLim.String())
		podLim := quota.Spec.Hard[corev1.ResourcePods]
		assert.True(t, podLim.Equal(resource.MustParse("10")), "pod limit: got %s", podLim.String())
	})

	t.Run("updates existing quota", func(t *testing.T) {
		t.Parallel()

		existing := &corev1.ResourceQuota{
			ObjectMeta: metav1.ObjectMeta{
				Name:      quotaName,
				Namespace: "stack-test",
				Labels:    map[string]string{"managed-by": "k8s-stack-manager"},
			},
			Spec: corev1.ResourceQuotaSpec{
				Hard: corev1.ResourceList{
					corev1.ResourcePods: resource.MustParse("5"),
				},
			},
		}
		cs := fake.NewSimpleClientset(
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-test"}},
			existing,
		)
		client := NewClientFromInterface(cs)

		config := &models.ResourceQuotaConfig{
			PodLimit: 20,
		}

		err := client.EnsureResourceQuota(context.Background(), "stack-test", config)
		require.NoError(t, err)

		quota, err := cs.CoreV1().ResourceQuotas("stack-test").Get(context.Background(), quotaName, metav1.GetOptions{})
		require.NoError(t, err)
		podLim := quota.Spec.Hard[corev1.ResourcePods]
		assert.True(t, podLim.Equal(resource.MustParse("20")), "pod limit: got %s", podLim.String())
	})

	t.Run("skips when no limits configured", func(t *testing.T) {
		t.Parallel()

		cs := fake.NewSimpleClientset(
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-test"}},
		)
		client := NewClientFromInterface(cs)

		config := &models.ResourceQuotaConfig{}

		err := client.EnsureResourceQuota(context.Background(), "stack-test", config)
		require.NoError(t, err)

		// No quota should be created.
		quotas, err := cs.CoreV1().ResourceQuotas("stack-test").List(context.Background(), metav1.ListOptions{})
		require.NoError(t, err)
		assert.Empty(t, quotas.Items)
	})

	t.Run("rejects invalid quantity", func(t *testing.T) {
		t.Parallel()

		cs := fake.NewSimpleClientset(
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-test"}},
		)
		client := NewClientFromInterface(cs)

		config := &models.ResourceQuotaConfig{
			CPURequest: "not-a-quantity",
		}

		err := client.EnsureResourceQuota(context.Background(), "stack-test", config)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "parse cpu_request")
	})
}

func TestEnsureLimitRange(t *testing.T) {
	t.Parallel()

	t.Run("creates limit range", func(t *testing.T) {
		t.Parallel()

		cs := fake.NewSimpleClientset(
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-test"}},
		)
		client := NewClientFromInterface(cs)

		config := &models.ResourceQuotaConfig{
			CPURequest:    "100m",
			CPULimit:      "500m",
			MemoryRequest: "128Mi",
			MemoryLimit:   "512Mi",
		}

		err := client.EnsureLimitRange(context.Background(), "stack-test", config)
		require.NoError(t, err)

		lr, err := cs.CoreV1().LimitRanges("stack-test").Get(context.Background(), limitRangeName, metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, "k8s-stack-manager", lr.Labels["managed-by"])
		require.Len(t, lr.Spec.Limits, 1)
		assert.Equal(t, corev1.LimitTypeContainer, lr.Spec.Limits[0].Type)
	})

	t.Run("skips when no limits configured", func(t *testing.T) {
		t.Parallel()

		cs := fake.NewSimpleClientset(
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-test"}},
		)
		client := NewClientFromInterface(cs)

		config := &models.ResourceQuotaConfig{}

		err := client.EnsureLimitRange(context.Background(), "stack-test", config)
		require.NoError(t, err)

		lrs, err := cs.CoreV1().LimitRanges("stack-test").List(context.Background(), metav1.ListOptions{})
		require.NoError(t, err)
		assert.Empty(t, lrs.Items)
	})
}

func TestGetNamespaceResourceUsage(t *testing.T) {
	t.Parallel()

	t.Run("reads usage from resource quota status", func(t *testing.T) {
		t.Parallel()

		cs := fake.NewSimpleClientset(
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-test"}},
			&corev1.ResourceQuota{
				ObjectMeta: metav1.ObjectMeta{
					Name:      quotaName,
					Namespace: "stack-test",
				},
				Status: corev1.ResourceQuotaStatus{
					Hard: corev1.ResourceList{
						corev1.ResourceLimitsCPU:      resource.MustParse("2000m"),
						corev1.ResourceLimitsMemory:   resource.MustParse("1Gi"),
						corev1.ResourceRequestsCPU:    resource.MustParse("2000m"),
						corev1.ResourceRequestsMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:           resource.MustParse("10"),
					},
					Used: corev1.ResourceList{
						corev1.ResourceRequestsCPU:    resource.MustParse("500m"),
						corev1.ResourceRequestsMemory: resource.MustParse("256Mi"),
						corev1.ResourcePods:           resource.MustParse("3"),
					},
				},
			},
		)
		client := NewClientFromInterface(cs)

		usage, err := client.GetNamespaceResourceUsage(context.Background(), "stack-test")
		require.NoError(t, err)

		assert.Equal(t, "500m", usage.CPUUsed)
		assert.Equal(t, "2", usage.CPULimit)
		assert.Equal(t, "256Mi", usage.MemoryUsed)
		assert.Equal(t, "1Gi", usage.MemoryLimit)
		assert.Equal(t, 3, usage.PodCount)
		assert.Equal(t, 10, usage.PodLimit)
	})

	t.Run("falls back to pod count when no quota exists", func(t *testing.T) {
		t.Parallel()

		cs := fake.NewSimpleClientset(
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-test"}},
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "pod-1", Namespace: "stack-test"},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "img"}}},
			},
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "pod-2", Namespace: "stack-test"},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "img"}}},
			},
		)
		client := NewClientFromInterface(cs)

		usage, err := client.GetNamespaceResourceUsage(context.Background(), "stack-test")
		require.NoError(t, err)

		assert.Equal(t, 2, usage.PodCount)
		assert.Equal(t, "", usage.CPUUsed)
		assert.Equal(t, "", usage.CPULimit)
	})
}

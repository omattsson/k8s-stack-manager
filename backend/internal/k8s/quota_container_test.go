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

// wantList maps a resource to a quantity string; nil means the list must be empty.
type wantList map[corev1.ResourceName]string

func assertResourceList(t *testing.T, label string, want wantList, got corev1.ResourceList) {
	t.Helper()
	assert.Len(t, got, len(want), "%s: %v", label, got)
	for name, value := range want {
		q, ok := got[name]
		if assert.True(t, ok, "%s: %s missing", label, name) {
			assert.True(t, q.Equal(resource.MustParse(value)), "%s %s: got %s, want %s", label, name, q.String(), value)
		}
	}
}

func TestEnsureLimitRange_ContainerFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		config         models.ResourceQuotaConfig
		wantNoLR       bool
		defaultRequest wantList
		defaultLimit   wantList
		max            wantList
	}{
		{
			name: "container fields set",
			config: models.ResourceQuotaConfig{
				CPURequest: "8", CPULimit: "16", MemoryRequest: "12Gi", MemoryLimit: "24Gi",
				ContainerCPURequest: "250m", ContainerCPULimit: "1", ContainerCPUMax: "2",
				ContainerMemoryRequest: "256Mi", ContainerMemoryLimit: "1Gi", ContainerMemoryMax: "4Gi",
			},
			defaultRequest: wantList{corev1.ResourceCPU: "250m", corev1.ResourceMemory: "256Mi"},
			defaultLimit:   wantList{corev1.ResourceCPU: "1", corev1.ResourceMemory: "1Gi"},
			max:            wantList{corev1.ResourceCPU: "2", corev1.ResourceMemory: "4Gi"},
		},
		{
			// The bug: the namespace totals (8 CPU / 12Gi) were copied into
			// every container default. Now the fallback applies.
			name:           "container fields empty, large namespace totals",
			config:         models.ResourceQuotaConfig{CPURequest: "8", CPULimit: "16", MemoryRequest: "12Gi", MemoryLimit: "24Gi"},
			defaultRequest: wantList{corev1.ResourceCPU: "100m", corev1.ResourceMemory: "128Mi"},
			defaultLimit:   wantList{corev1.ResourceCPU: "500m", corev1.ResourceMemory: "512Mi"},
		},
		{
			name:           "fallback capped at a small namespace total",
			config:         models.ResourceQuotaConfig{CPURequest: "50m", CPULimit: "200m", MemoryRequest: "64Mi", MemoryLimit: "256Mi"},
			defaultRequest: wantList{corev1.ResourceCPU: "50m", corev1.ResourceMemory: "64Mi"},
			defaultLimit:   wantList{corev1.ResourceCPU: "200m", corev1.ResourceMemory: "256Mi"},
		},
		{
			name:           "only namespace requests: default request, no default limit",
			config:         models.ResourceQuotaConfig{CPURequest: "4"},
			defaultRequest: wantList{corev1.ResourceCPU: "100m"},
		},
		{
			name:           "max only: fallback defaults capped at max",
			config:         models.ResourceQuotaConfig{ContainerCPUMax: "300m", ContainerMemoryMax: "2Gi"},
			defaultRequest: wantList{corev1.ResourceCPU: "100m", corev1.ResourceMemory: "128Mi"},
			defaultLimit:   wantList{corev1.ResourceCPU: "300m", corev1.ResourceMemory: "512Mi"},
			max:            wantList{corev1.ResourceCPU: "300m", corev1.ResourceMemory: "2Gi"},
		},
		{
			name:           "container request above fallback limit raises the default limit",
			config:         models.ResourceQuotaConfig{CPULimit: "4", ContainerCPURequest: "1"},
			defaultRequest: wantList{corev1.ResourceCPU: "1"},
			defaultLimit:   wantList{corev1.ResourceCPU: "1"},
		},
		{
			name:           "container limit below fallback request caps the default request",
			config:         models.ResourceQuotaConfig{ContainerCPULimit: "50m"},
			defaultRequest: wantList{corev1.ResourceCPU: "50m"},
			defaultLimit:   wantList{corev1.ResourceCPU: "50m"},
		},
		{
			name: "ephemeral storage configured",
			config: models.ResourceQuotaConfig{
				EphemeralStorageLimit: "20Gi", ContainerEphemeralStorageRequest: "256Mi", ContainerEphemeralStorageLimit: "2Gi",
			},
			defaultRequest: wantList{corev1.ResourceEphemeralStorage: "256Mi"},
			defaultLimit:   wantList{corev1.ResourceEphemeralStorage: "2Gi"},
		},
		{
			name:           "ephemeral namespace total only: fallback defaults",
			config:         models.ResourceQuotaConfig{EphemeralStorageLimit: "20Gi"},
			defaultRequest: wantList{corev1.ResourceEphemeralStorage: "128Mi"},
			defaultLimit:   wantList{corev1.ResourceEphemeralStorage: "1Gi"},
		},
		{
			name:           "ephemeral fallback capped at a small namespace total",
			config:         models.ResourceQuotaConfig{EphemeralStorageLimit: "100Mi"},
			defaultRequest: wantList{corev1.ResourceEphemeralStorage: "100Mi"},
			defaultLimit:   wantList{corev1.ResourceEphemeralStorage: "100Mi"},
		},
		{
			name:     "storage and pods only: no limit range",
			config:   models.ResourceQuotaConfig{StorageLimit: "10Gi", PodLimit: 5},
			wantNoLR: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cs := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-test"}})
			client := NewClientFromInterface(cs)
			config := tt.config

			require.NoError(t, client.EnsureLimitRange(context.Background(), "stack-test", &config))

			lrs, err := cs.CoreV1().LimitRanges("stack-test").List(context.Background(), metav1.ListOptions{})
			require.NoError(t, err)
			if tt.wantNoLR {
				assert.Empty(t, lrs.Items)
				return
			}
			require.Len(t, lrs.Items, 1)
			require.Len(t, lrs.Items[0].Spec.Limits, 1)
			item := lrs.Items[0].Spec.Limits[0]
			assert.Equal(t, corev1.LimitTypeContainer, item.Type)
			assertResourceList(t, "defaultRequest", tt.defaultRequest, item.DefaultRequest)
			assertResourceList(t, "default", tt.defaultLimit, item.Default)
			assertResourceList(t, "max", tt.max, item.Max)

			// A container default never equals a larger namespace total.
			for name, total := range map[corev1.ResourceName]string{corev1.ResourceCPU: config.CPULimit, corev1.ResourceMemory: config.MemoryLimit} {
				if total == "" {
					continue
				}
				totalQty := resource.MustParse(total)
				if def, ok := item.Default[name]; ok {
					assert.True(t, def.Cmp(totalQty) <= 0, "default %s %s above the namespace total %s", name, def.String(), total)
				}
			}
		})
	}
}

func TestEnsureLimitRange_ReplacesOldTotalsDefaults(t *testing.T) {
	t.Parallel()

	// A LimitRange written by an older version: defaults = namespace totals.
	old := &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{Name: limitRangeName, Namespace: "stack-test"},
		Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{
			Type:           corev1.LimitTypeContainer,
			Default:        corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("16")},
			DefaultRequest: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")},
		}}},
	}
	cs := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-test"}}, old)
	client := NewClientFromInterface(cs)

	require.NoError(t, client.EnsureLimitRange(context.Background(), "stack-test",
		&models.ResourceQuotaConfig{CPURequest: "8", CPULimit: "16"}))

	lr, err := cs.CoreV1().LimitRanges("stack-test").Get(context.Background(), limitRangeName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, lr.Spec.Limits, 1)
	assertResourceList(t, "defaultRequest", wantList{corev1.ResourceCPU: "100m"}, lr.Spec.Limits[0].DefaultRequest)
	assertResourceList(t, "default", wantList{corev1.ResourceCPU: "500m"}, lr.Spec.Limits[0].Default)
}

func TestEnsureLimitRange_InvalidQuantity(t *testing.T) {
	t.Parallel()

	cs := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-test"}})
	client := NewClientFromInterface(cs)
	err := client.EnsureLimitRange(context.Background(), "stack-test", &models.ResourceQuotaConfig{ContainerCPUMax: "two"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "container_cpu_max")
}

func TestEnsureResourceQuota_EphemeralStorage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  models.ResourceQuotaConfig
		want wantList
	}{
		{
			name: "ephemeral storage limit sets requests and limits",
			cfg:  models.ResourceQuotaConfig{EphemeralStorageLimit: "20Gi"},
			want: wantList{corev1.ResourceRequestsEphemeralStorage: "20Gi", corev1.ResourceLimitsEphemeralStorage: "20Gi"},
		},
		{
			name: "container fields alone add nothing to the namespace quota",
			cfg:  models.ResourceQuotaConfig{PodLimit: 3, ContainerCPUMax: "2", ContainerEphemeralStorageLimit: "1Gi"},
			want: wantList{corev1.ResourcePods: "3"},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cs := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-test"}})
			client := NewClientFromInterface(cs)
			cfg := tt.cfg
			require.NoError(t, client.EnsureResourceQuota(context.Background(), "stack-test", &cfg))
			quota, err := cs.CoreV1().ResourceQuotas("stack-test").Get(context.Background(), quotaName, metav1.GetOptions{})
			require.NoError(t, err)
			assertResourceList(t, "hard", tt.want, quota.Spec.Hard)
		})
	}
}

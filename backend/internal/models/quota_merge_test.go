package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateEffectiveQuota(t *testing.T) {
	t.Parallel()

	two := 2
	neg := -1
	tests := []struct {
		name     string
		cluster  *ResourceQuotaConfig
		override *InstanceQuotaOverride
		wantErr  string
	}{
		{name: "both nil"},
		{name: "cluster only valid", cluster: &ResourceQuotaConfig{CPURequest: "1", CPULimit: "2"}},
		{name: "override raises limit", cluster: &ResourceQuotaConfig{CPULimit: "2"}, override: &InstanceQuotaOverride{CPURequest: "4", CPULimit: "8"}},
		{name: "override request above cluster limit", cluster: &ResourceQuotaConfig{CPULimit: "2"}, override: &InstanceQuotaOverride{CPURequest: "4"},
			wantErr: "cpu_request 4 (instance override) exceeds the effective cpu_limit 2 (cluster quota)"},
		{name: "override limit below cluster request", cluster: &ResourceQuotaConfig{MemoryRequest: "2Gi"}, override: &InstanceQuotaOverride{MemoryLimit: "1Gi"},
			wantErr: "memory_request 2Gi (cluster quota) exceeds the effective memory_limit 1Gi (instance override)"},
		{name: "invalid legacy cluster value", cluster: &ResourceQuotaConfig{StorageLimit: "abc"},
			wantErr: `storage_limit "abc" (cluster quota): invalid quantity`},
		{name: "override replaces invalid cluster value", cluster: &ResourceQuotaConfig{StorageLimit: "abc"}, override: &InstanceQuotaOverride{StorageLimit: "1Gi"}},
		{name: "negative override pod limit", cluster: &ResourceQuotaConfig{PodLimit: 5}, override: &InstanceQuotaOverride{PodLimit: &neg},
			wantErr: "pod_limit -1 (instance override): must not be negative"},
		{name: "override pod limit", cluster: &ResourceQuotaConfig{PodLimit: -3}, override: &InstanceQuotaOverride{PodLimit: &two}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateEffectiveQuota(tt.cluster, tt.override)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, tt.wantErr, err.Error())
		})
	}
}

func TestMergeQuotaOverride_NilArguments(t *testing.T) {
	t.Parallel()

	two := 2
	assert.Equal(t, &ResourceQuotaConfig{}, MergeQuotaOverride(nil, nil))
	assert.Equal(t, &ResourceQuotaConfig{CPULimit: "1", PodLimit: 2}, MergeQuotaOverride(nil, &InstanceQuotaOverride{CPULimit: "1", PodLimit: &two}))

	cluster := &ResourceQuotaConfig{CPULimit: "4"}
	merged := MergeQuotaOverride(cluster, nil)
	merged.CPULimit = "8"
	assert.Equal(t, "4", cluster.CPULimit, "merge must not modify the cluster quota")
}

func TestCheckOverrideWithinClusterQuota(t *testing.T) {
	t.Parallel()

	n := func(v int) *int { return &v }
	tests := []struct {
		name     string
		cluster  *ResourceQuotaConfig
		override *InstanceQuotaOverride
		existing *InstanceQuotaOverride
		wantErr  string
	}{
		{name: "unchanged value above the cap", cluster: &ResourceQuotaConfig{CPULimit: "16", PodLimit: 20}, override: &InstanceQuotaOverride{CPULimit: "32000m", PodLimit: n(0)}, existing: &InstanceQuotaOverride{CPULimit: "32", PodLimit: n(0)}},
		{name: "raise above the existing value", cluster: &ResourceQuotaConfig{CPULimit: "16"}, override: &InstanceQuotaOverride{CPULimit: "33"}, existing: &InstanceQuotaOverride{CPULimit: "32"}, wantErr: "cpu_limit 33 exceeds the cluster quota 16"},
		{name: "lower but still above the cap", cluster: &ResourceQuotaConfig{CPULimit: "16"}, override: &InstanceQuotaOverride{CPULimit: "24"}, existing: &InstanceQuotaOverride{CPULimit: "32"}, wantErr: "cpu_limit 24 exceeds the cluster quota 16"},
		{name: "nil cluster", override: &InstanceQuotaOverride{CPULimit: "64"}},
		{name: "nil override", cluster: &ResourceQuotaConfig{CPULimit: "16"}},
		{name: "empty override field", cluster: &ResourceQuotaConfig{CPULimit: "16"}, override: &InstanceQuotaOverride{}},
		{name: "no cluster value", cluster: &ResourceQuotaConfig{}, override: &InstanceQuotaOverride{CPULimit: "64", PodLimit: n(0)}},
		{name: "equal in other units", cluster: &ResourceQuotaConfig{CPULimit: "16", MemoryLimit: "24Gi"}, override: &InstanceQuotaOverride{CPULimit: "16000m", MemoryLimit: "25769803776"}},
		{name: "below", cluster: &ResourceQuotaConfig{CPURequest: "2"}, override: &InstanceQuotaOverride{CPURequest: "500m"}},
		{name: "cpu request above", cluster: &ResourceQuotaConfig{CPURequest: "2"}, override: &InstanceQuotaOverride{CPURequest: "2001m"}, wantErr: "cpu_request 2001m exceeds the cluster quota 2"},
		{name: "storage above", cluster: &ResourceQuotaConfig{StorageLimit: "100Gi"}, override: &InstanceQuotaOverride{StorageLimit: "1Ti"}, wantErr: "storage_limit 1Ti exceeds the cluster quota 100Gi"},
		{name: "memory request above", cluster: &ResourceQuotaConfig{MemoryRequest: "1Gi"}, override: &InstanceQuotaOverride{MemoryRequest: "1025Mi"}, wantErr: "memory_request 1025Mi exceeds the cluster quota 1Gi"},
		{name: "pod limit equal", cluster: &ResourceQuotaConfig{PodLimit: 20}, override: &InstanceQuotaOverride{PodLimit: n(20)}},
		{name: "pod limit above", cluster: &ResourceQuotaConfig{PodLimit: 20}, override: &InstanceQuotaOverride{PodLimit: n(21)}, wantErr: "pod_limit 21 exceeds the cluster quota 20"},
		{name: "pod limit zero is no limit", cluster: &ResourceQuotaConfig{PodLimit: 20}, override: &InstanceQuotaOverride{PodLimit: n(0)}, wantErr: "pod_limit 0 (no limit) exceeds the cluster quota 20"},
		{name: "unparsable cluster value has no cap", cluster: &ResourceQuotaConfig{CPULimit: "lots"}, override: &InstanceQuotaOverride{CPULimit: "64"}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := CheckOverrideWithinClusterQuota(tt.cluster, tt.override, tt.existing)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, tt.wantErr, err.Error())
		})
	}
}

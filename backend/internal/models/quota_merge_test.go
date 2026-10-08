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

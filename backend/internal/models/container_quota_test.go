package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResourceQuotaConfig_Validate_ContainerFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		quota   ResourceQuotaConfig
		wantErr string
	}{
		{name: "all container fields valid", quota: ResourceQuotaConfig{
			CPURequest: "4", CPULimit: "8", MemoryRequest: "8Gi", MemoryLimit: "16Gi",
			ContainerCPURequest: "100m", ContainerCPULimit: "500m", ContainerCPUMax: "2",
			ContainerMemoryRequest: "128Mi", ContainerMemoryLimit: "512Mi", ContainerMemoryMax: "4Gi",
			EphemeralStorageLimit: "20Gi", ContainerEphemeralStorageRequest: "128Mi", ContainerEphemeralStorageLimit: "1Gi",
		}},
		{name: "max above namespace total is allowed", quota: ResourceQuotaConfig{CPULimit: "1", ContainerCPUMax: "2"}},
		{name: "invalid container quantity", quota: ResourceQuotaConfig{ContainerCPUMax: "two"},
			wantErr: "container_cpu_max: invalid quantity (examples: 500m, 2, 512Mi, 10Gi)"},
		{name: "invalid ephemeral storage", quota: ResourceQuotaConfig{EphemeralStorageLimit: "lots"},
			wantErr: "ephemeral_storage_limit: invalid quantity (examples: 500m, 2, 512Mi, 10Gi)"},
		{name: "negative container value", quota: ResourceQuotaConfig{ContainerMemoryRequest: "-1Mi"},
			wantErr: "container_memory_request: must not be negative"},
		{name: "container request above container limit", quota: ResourceQuotaConfig{ContainerCPURequest: "1", ContainerCPULimit: "500m"},
			wantErr: "container_cpu_request must not exceed container_cpu_limit"},
		{name: "container limit above container max", quota: ResourceQuotaConfig{ContainerMemoryLimit: "2Gi", ContainerMemoryMax: "1Gi"},
			wantErr: "container_memory_limit must not exceed container_memory_max"},
		{name: "container request above container max", quota: ResourceQuotaConfig{ContainerCPURequest: "3", ContainerCPUMax: "2"},
			wantErr: "container_cpu_request must not exceed container_cpu_max"},
		{name: "ephemeral request above ephemeral limit", quota: ResourceQuotaConfig{ContainerEphemeralStorageRequest: "2Gi", ContainerEphemeralStorageLimit: "1Gi"},
			wantErr: "container_ephemeral_storage_request must not exceed container_ephemeral_storage_limit"},
		{name: "container default above namespace total", quota: ResourceQuotaConfig{CPULimit: "2", ContainerCPULimit: "4"},
			wantErr: "container_cpu_limit must not exceed cpu_limit"},
		{name: "container request above namespace request", quota: ResourceQuotaConfig{MemoryRequest: "1Gi", ContainerMemoryRequest: "2Gi"},
			wantErr: "container_memory_request must not exceed memory_request"},
		{name: "container ephemeral limit above namespace ephemeral", quota: ResourceQuotaConfig{EphemeralStorageLimit: "1Gi", ContainerEphemeralStorageLimit: "2Gi"},
			wantErr: "container_ephemeral_storage_limit must not exceed ephemeral_storage_limit"},
		{name: "too long", quota: ResourceQuotaConfig{ContainerCPUMax: "1000000000000000000000m"},
			wantErr: "container_cpu_max: must be at most 20 characters"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			q := tt.quota
			q.ClusterID = "c1"
			err := q.Validate()
			ov := InstanceQuotaOverride{
				StackInstanceID:                  "i1",
				CPURequest:                       q.CPURequest,
				CPULimit:                         q.CPULimit,
				MemoryRequest:                    q.MemoryRequest,
				MemoryLimit:                      q.MemoryLimit,
				ContainerCPURequest:              q.ContainerCPURequest,
				ContainerCPULimit:                q.ContainerCPULimit,
				ContainerCPUMax:                  q.ContainerCPUMax,
				ContainerMemoryRequest:           q.ContainerMemoryRequest,
				ContainerMemoryLimit:             q.ContainerMemoryLimit,
				ContainerMemoryMax:               q.ContainerMemoryMax,
				EphemeralStorageLimit:            q.EphemeralStorageLimit,
				ContainerEphemeralStorageRequest: q.ContainerEphemeralStorageRequest,
				ContainerEphemeralStorageLimit:   q.ContainerEphemeralStorageLimit,
			}
			ovErr := ov.Validate()
			if tt.wantErr == "" {
				assert.NoError(t, err)
				assert.NoError(t, ovErr)
				return
			}
			require.Error(t, err)
			assert.Equal(t, tt.wantErr, err.Error())
			require.Error(t, ovErr, "the override applies the same rules")
			assert.Equal(t, tt.wantErr, ovErr.Error())
		})
	}
}

func TestMergeQuotaOverride_ContainerFields(t *testing.T) {
	t.Parallel()

	cluster := &ResourceQuotaConfig{
		CPULimit:              "8",
		ContainerCPURequest:   "100m",
		ContainerCPULimit:     "500m",
		ContainerCPUMax:       "2",
		ContainerMemoryMax:    "4Gi",
		EphemeralStorageLimit: "20Gi",
	}
	override := &InstanceQuotaOverride{
		ContainerCPUMax:                "4",
		ContainerMemoryLimit:           "1Gi",
		ContainerEphemeralStorageLimit: "2Gi",
	}
	merged := MergeQuotaOverride(cluster, override)
	assert.Equal(t, "8", merged.CPULimit, "cluster value stays")
	assert.Equal(t, "100m", merged.ContainerCPURequest, "empty override field keeps the cluster value")
	assert.Equal(t, "500m", merged.ContainerCPULimit)
	assert.Equal(t, "4", merged.ContainerCPUMax, "override replaces the cluster value")
	assert.Equal(t, "1Gi", merged.ContainerMemoryLimit, "override sets a value the cluster does not have")
	assert.Equal(t, "4Gi", merged.ContainerMemoryMax)
	assert.Equal(t, "20Gi", merged.EphemeralStorageLimit)
	assert.Equal(t, "2Gi", merged.ContainerEphemeralStorageLimit)
	assert.Equal(t, "2", cluster.ContainerCPUMax, "merge must not modify the cluster quota")
}

func TestResourceQuotaConfig_IsEmpty(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		quota ResourceQuotaConfig
		want  bool
	}{
		{name: "empty", quota: ResourceQuotaConfig{ClusterID: "c1"}, want: true},
		{name: "pod limit", quota: ResourceQuotaConfig{PodLimit: 3}},
		{name: "namespace total", quota: ResourceQuotaConfig{CPULimit: "1"}},
		{name: "container max only", quota: ResourceQuotaConfig{ContainerMemoryMax: "1Gi"}},
		{name: "ephemeral storage only", quota: ResourceQuotaConfig{EphemeralStorageLimit: "1Gi"}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.quota.IsEmpty())
		})
	}
}

func TestValidateEffectiveQuota_ContainerFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cluster  *ResourceQuotaConfig
		override *InstanceQuotaOverride
		wantErr  string
	}{
		{name: "override raises max above cluster limit", cluster: &ResourceQuotaConfig{ContainerCPULimit: "500m", ContainerCPUMax: "1"},
			override: &InstanceQuotaOverride{ContainerCPUMax: "4"}},
		{name: "override max below cluster default limit", cluster: &ResourceQuotaConfig{ContainerCPULimit: "1"},
			override: &InstanceQuotaOverride{ContainerCPUMax: "500m"},
			wantErr:  "container_cpu_limit 1 (cluster quota) exceeds the effective container_cpu_max 500m (instance override)"},
		{name: "override namespace limit below cluster container default", cluster: &ResourceQuotaConfig{ContainerMemoryLimit: "2Gi"},
			override: &InstanceQuotaOverride{MemoryLimit: "1Gi"},
			wantErr:  "container_memory_limit 2Gi (cluster quota) exceeds the effective memory_limit 1Gi (instance override)"},
		{name: "invalid cluster ephemeral value", cluster: &ResourceQuotaConfig{EphemeralStorageLimit: "x"},
			wantErr: `ephemeral_storage_limit "x" (cluster quota): invalid quantity`},
		{name: "override ephemeral request above its limit", override: &InstanceQuotaOverride{ContainerEphemeralStorageRequest: "2Gi", ContainerEphemeralStorageLimit: "1Gi"},
			wantErr: "container_ephemeral_storage_request 2Gi (instance override) exceeds the effective container_ephemeral_storage_limit 1Gi (instance override)"},
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

func TestCheckOverrideWithinClusterQuota_ContainerFields(t *testing.T) {
	t.Parallel()

	cluster := &ResourceQuotaConfig{
		ContainerCPURequest: "100m", ContainerCPULimit: "500m", ContainerCPUMax: "2",
		ContainerMemoryLimit: "512Mi", ContainerMemoryMax: "4Gi",
		EphemeralStorageLimit: "20Gi", ContainerEphemeralStorageLimit: "1Gi",
	}
	tests := []struct {
		name     string
		override *InstanceQuotaOverride
		existing *InstanceQuotaOverride
		wantErr  string
	}{
		{name: "lower values pass", override: &InstanceQuotaOverride{ContainerCPUMax: "1", ContainerMemoryLimit: "256Mi", EphemeralStorageLimit: "10Gi"}},
		{name: "equal in other units", override: &InstanceQuotaOverride{ContainerCPUMax: "2000m", ContainerMemoryMax: "4096Mi"}},
		{name: "max above", override: &InstanceQuotaOverride{ContainerCPUMax: "3"}, wantErr: "container_cpu_max 3 exceeds the cluster quota 2"},
		{name: "default limit above", override: &InstanceQuotaOverride{ContainerMemoryLimit: "1Gi"}, wantErr: "container_memory_limit 1Gi exceeds the cluster quota 512Mi"},
		{name: "default request above", override: &InstanceQuotaOverride{ContainerCPURequest: "200m"}, wantErr: "container_cpu_request 200m exceeds the cluster quota 100m"},
		{name: "ephemeral total above", override: &InstanceQuotaOverride{EphemeralStorageLimit: "40Gi"}, wantErr: "ephemeral_storage_limit 40Gi exceeds the cluster quota 20Gi"},
		{name: "container ephemeral limit above", override: &InstanceQuotaOverride{ContainerEphemeralStorageLimit: "2Gi"}, wantErr: "container_ephemeral_storage_limit 2Gi exceeds the cluster quota 1Gi"},
		{name: "no cluster value has no cap", override: &InstanceQuotaOverride{ContainerMemoryRequest: "8Gi"}},
		{name: "unchanged grant above the cap passes", override: &InstanceQuotaOverride{ContainerCPUMax: "8"}, existing: &InstanceQuotaOverride{ContainerCPUMax: "8"}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := CheckOverrideWithinClusterQuota(cluster, tt.override, tt.existing)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, tt.wantErr, err.Error())
		})
	}
}

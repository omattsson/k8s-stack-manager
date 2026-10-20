package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/internal/cluster"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xeipuuv/gojsonschema"
)

// effectiveQuotaSchema is the JSON schema of GET /stack-instances/:id/quota.
const effectiveQuotaSchema = `{
"type": "object",
"required": ["cluster_id", "effective", "cluster", "override", "sources", "can_modify", "valid"],
"additionalProperties": false,
"properties": {
"cluster_id": {"type": "string"},
"valid": {"type": "boolean"},
"invalid_reason": {"type": "string", "minLength": 1},
"effective": {"type": "object", "required": ["cpu_limit", "pod_limit", "container_cpu_max"]},
"cluster": {"type": ["object", "null"]},
"override": {"type": ["object", "null"]},
"sources": {"type": "object", "additionalProperties": {"enum": ["override", "cluster", "none"]}},
"can_modify": {"type": "boolean"}
}
}`

func TestGetEffectiveQuota(t *testing.T) {
	t.Parallel()

	const (
		ownerID   = "uid-owner"
		clusterID = "cl-1"
	)
	five := 5
	clusterQuota := &models.ResourceQuotaConfig{
		ClusterID:          clusterID,
		CPURequest:         "1",
		CPULimit:           "2",
		MemoryLimit:        "2Gi",
		ContainerMemoryMax: "1Gi",
		PodLimit:           10,
	}

	tests := []struct {
		resolver      clusterIDResolver
		cluster       *models.ResourceQuotaConfig
		override      *models.InstanceQuotaOverride
		wantEffective map[string]any
		wantSources   map[string]string
		name          string
		wantInvalid   string
		instanceID    string
		instCluster   string
		callerID      string
		callerRole    string
		wantClusterID string
		wantStatus    int
		wantCluster   bool
		wantOverride  bool
		wantCanModify bool
	}{
		{
			name:          "no cluster quota and no override",
			instCluster:   clusterID,
			callerID:      ownerID,
			callerRole:    "user",
			wantStatus:    http.StatusOK,
			wantClusterID: clusterID,
			wantCanModify: true,
			wantEffective: map[string]any{"cpu_limit": "", "memory_limit": "", "pod_limit": float64(0)},
			wantSources:   map[string]string{"cpu_limit": "none", "memory_limit": "none", "pod_limit": "none"},
		},
		{
			name:          "cluster quota only",
			instCluster:   clusterID,
			cluster:       clusterQuota,
			callerID:      ownerID,
			callerRole:    "user",
			wantStatus:    http.StatusOK,
			wantClusterID: clusterID,
			wantCluster:   true,
			wantCanModify: true,
			wantEffective: map[string]any{"cpu_limit": "2", "memory_limit": "2Gi", "storage_limit": "", "pod_limit": float64(10)},
			wantSources:   map[string]string{"cpu_limit": "cluster", "memory_limit": "cluster", "storage_limit": "none", "pod_limit": "cluster"},
		},
		{
			name:          "override only",
			instCluster:   clusterID,
			override:      &models.InstanceQuotaOverride{CPULimit: "1500m", PodLimit: &five},
			callerID:      ownerID,
			callerRole:    "user",
			wantStatus:    http.StatusOK,
			wantClusterID: clusterID,
			wantOverride:  true,
			wantCanModify: true,
			wantEffective: map[string]any{"cpu_limit": "1500m", "memory_limit": "", "pod_limit": float64(5)},
			wantSources:   map[string]string{"cpu_limit": "override", "memory_limit": "none", "pod_limit": "override"},
		},
		{
			name:          "cluster quota and override give the source of each field",
			instCluster:   clusterID,
			cluster:       clusterQuota,
			override:      &models.InstanceQuotaOverride{CPULimit: "1500m", ContainerMemoryMax: "512Mi"},
			callerID:      ownerID,
			callerRole:    "user",
			wantStatus:    http.StatusOK,
			wantClusterID: clusterID,
			wantCluster:   true,
			wantOverride:  true,
			wantCanModify: true,
			wantEffective: map[string]any{"cpu_request": "1", "cpu_limit": "1500m", "memory_limit": "2Gi", "container_memory_max": "512Mi", "storage_limit": "", "pod_limit": float64(10)},
			wantSources: map[string]string{
				"cpu_request": "cluster", "cpu_limit": "override", "memory_limit": "cluster",
				"container_memory_max": "override", "storage_limit": "none", "pod_limit": "cluster",
			},
		},
		{
			name:          "an invalid merged quota is shown with the reason",
			instCluster:   clusterID,
			cluster:       clusterQuota,
			override:      &models.InstanceQuotaOverride{CPURequest: "3"},
			callerID:      ownerID,
			callerRole:    "user",
			wantStatus:    http.StatusOK,
			wantClusterID: clusterID,
			wantCluster:   true,
			wantOverride:  true,
			wantCanModify: true,
			wantInvalid:   "cpu_request 3 (instance override) exceeds the effective cpu_limit 2 (cluster quota)",
			wantEffective: map[string]any{"cpu_request": "3", "cpu_limit": "2"},
			wantSources:   map[string]string{"cpu_request": "override", "cpu_limit": "cluster"},
		},
		{
			name:          "instance without a cluster uses the default cluster",
			resolver:      fakeResolver{id: clusterID},
			cluster:       clusterQuota,
			callerID:      ownerID,
			callerRole:    "user",
			wantStatus:    http.StatusOK,
			wantClusterID: clusterID,
			wantCluster:   true,
			wantCanModify: true,
			wantEffective: map[string]any{"cpu_limit": "2"},
			wantSources:   map[string]string{"cpu_limit": "cluster"},
		},
		{
			name:          "no default cluster gives an empty cluster ID",
			resolver:      fakeResolver{err: cluster.ErrNoDefaultCluster},
			cluster:       clusterQuota,
			callerID:      ownerID,
			callerRole:    "user",
			wantStatus:    http.StatusOK,
			wantCanModify: true,
			wantEffective: map[string]any{"cpu_limit": ""},
			wantSources:   map[string]string{"cpu_limit": "none"},
		},
		{
			name:          "devops can modify",
			instCluster:   clusterID,
			cluster:       clusterQuota,
			callerID:      "uid-devops",
			callerRole:    "devops",
			wantStatus:    http.StatusOK,
			wantClusterID: clusterID,
			wantCluster:   true,
			wantCanModify: true,
		},
		{
			name:          "admin can modify",
			instCluster:   clusterID,
			cluster:       clusterQuota,
			callerID:      "uid-admin",
			callerRole:    "admin",
			wantStatus:    http.StatusOK,
			wantClusterID: clusterID,
			wantCluster:   true,
			wantCanModify: true,
		},
		{
			name:          "another user reads the quota but cannot modify",
			instCluster:   clusterID,
			cluster:       clusterQuota,
			override:      &models.InstanceQuotaOverride{CPULimit: "1500m"},
			callerID:      "uid-other",
			callerRole:    "user",
			wantStatus:    http.StatusOK,
			wantClusterID: clusterID,
			wantCluster:   true,
			wantOverride:  true,
			wantCanModify: false,
			wantEffective: map[string]any{"cpu_limit": "1500m"},
			wantSources:   map[string]string{"cpu_limit": "override"},
		},
		{
			name:       "unknown instance gives 404",
			instanceID: "missing",
			callerID:   ownerID,
			callerRole: "admin",
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			instRepo := NewMockStackInstanceRepository()
			inst := seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", ownerID, models.StackStatusRunning)
			inst.ClusterID = tt.instCluster
			require.NoError(t, instRepo.Update(inst))

			overrideRepo := NewMockInstanceQuotaOverrideRepository()
			if tt.override != nil {
				ov := *tt.override
				ov.StackInstanceID = inst.ID
				require.NoError(t, overrideRepo.Upsert(context.Background(), &ov))
			}
			quotaRepo := NewMockResourceQuotaRepository()
			if tt.cluster != nil {
				cq := *tt.cluster
				require.NoError(t, quotaRepo.Upsert(context.Background(), &cq))
			}

			h := NewInstanceQuotaOverrideHandler(overrideRepo, instRepo)
			h.clusterQuotaRepo = quotaRepo
			h.clusterResolver = tt.resolver

			gin.SetMode(gin.TestMode)
			r := gin.New()
			r.Use(injectAuthContext(tt.callerID, tt.callerRole))
			r.GET("/api/v1/stack-instances/:id/quota", h.GetEffectiveQuota)

			id := tt.instanceID
			if id == "" {
				id = inst.ID
			}
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/stack-instances/"+id+"/quota", nil)
			r.ServeHTTP(w, req)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantStatus != http.StatusOK {
				return
			}

			result, err := gojsonschema.Validate(gojsonschema.NewStringLoader(effectiveQuotaSchema), gojsonschema.NewBytesLoader(w.Body.Bytes()))
			require.NoError(t, err)
			assert.True(t, result.Valid(), "%v", result.Errors())

			var resp struct {
				Effective     map[string]any                `json:"effective"`
				Cluster       *models.ResourceQuotaConfig   `json:"cluster"`
				Override      *models.InstanceQuotaOverride `json:"override"`
				Sources       map[string]string             `json:"sources"`
				ClusterID     string                        `json:"cluster_id"`
				InvalidReason string                        `json:"invalid_reason"`
				CanModify     bool                          `json:"can_modify"`
				Valid         bool                          `json:"valid"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, tt.wantInvalid == "", resp.Valid, "valid")
			assert.Equal(t, tt.wantInvalid, resp.InvalidReason)
			assert.Equal(t, tt.wantClusterID, resp.ClusterID)
			assert.Equal(t, tt.wantCanModify, resp.CanModify)
			assert.Equal(t, tt.wantCluster, resp.Cluster != nil, "cluster")
			assert.Equal(t, tt.wantOverride, resp.Override != nil, "override")
			if resp.Override != nil {
				assert.Equal(t, inst.ID, resp.Override.StackInstanceID)
			}
			assert.Len(t, resp.Effective, len(resp.Sources))
			for field, want := range tt.wantEffective {
				assert.Equal(t, want, resp.Effective[field], "effective %s", field)
			}
			for field, want := range tt.wantSources {
				assert.Equal(t, want, resp.Sources[field], "source of %s", field)
			}
		})
	}
}

func TestGetEffectiveQuota_RepositoryErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		resolver     clusterIDResolver
		name         string
		instCluster  string
		failOverride bool
		failCluster  bool
	}{
		{name: "override repository error", instCluster: "cl-1", failOverride: true},
		{name: "cluster quota repository error", instCluster: "cl-1", failCluster: true},
		{name: "default cluster lookup error", resolver: fakeResolver{err: assert.AnError}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			instRepo := NewMockStackInstanceRepository()
			inst := seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusRunning)
			inst.ClusterID = tt.instCluster
			require.NoError(t, instRepo.Update(inst))
			overrideRepo := NewMockInstanceQuotaOverrideRepository()
			quotaRepo := NewMockResourceQuotaRepository()
			if tt.failOverride {
				overrideRepo.SetError(assert.AnError)
			}
			if tt.failCluster {
				quotaRepo.SetError(assert.AnError)
			}
			h := NewInstanceQuotaOverrideHandler(overrideRepo, instRepo)
			h.clusterQuotaRepo = quotaRepo
			h.clusterResolver = tt.resolver

			gin.SetMode(gin.TestMode)
			r := gin.New()
			r.Use(injectAuthContext("uid-1", "user"))
			r.GET("/api/v1/stack-instances/:id/quota", h.GetEffectiveQuota)

			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/stack-instances/inst-1/quota", nil))

			require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
			assert.JSONEq(t, `{"error":"Internal server error"}`, w.Body.String())
		})
	}
}

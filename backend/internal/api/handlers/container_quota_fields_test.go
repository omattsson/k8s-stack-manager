package handlers

import (
	"bytes"
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
)

func TestUpdateQuotas_ContainerFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       UpdateQuotaRequest
		wantStatus int
		wantError  string
		check      func(t *testing.T, saved *models.ResourceQuotaConfig)
	}{
		{
			name: "accepts, trims and returns the container fields",
			body: UpdateQuotaRequest{
				CPULimit: "16", MemoryLimit: "24Gi",
				ContainerCPURequest: " 100m", ContainerCPULimit: "500m ", ContainerCPUMax: "2",
				ContainerMemoryRequest: "128Mi", ContainerMemoryLimit: "512Mi", ContainerMemoryMax: "4Gi",
				EphemeralStorageLimit: "20Gi", ContainerEphemeralStorageRequest: "128Mi", ContainerEphemeralStorageLimit: "1Gi",
			},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, saved *models.ResourceQuotaConfig) {
				assert.Equal(t, "100m", saved.ContainerCPURequest)
				assert.Equal(t, "500m", saved.ContainerCPULimit)
				assert.Equal(t, "2", saved.ContainerCPUMax)
				assert.Equal(t, "128Mi", saved.ContainerMemoryRequest)
				assert.Equal(t, "512Mi", saved.ContainerMemoryLimit)
				assert.Equal(t, "4Gi", saved.ContainerMemoryMax)
				assert.Equal(t, "20Gi", saved.EphemeralStorageLimit)
				assert.Equal(t, "128Mi", saved.ContainerEphemeralStorageRequest)
				assert.Equal(t, "1Gi", saved.ContainerEphemeralStorageLimit)
			},
		},
		{
			name:       "invalid container quantity",
			body:       UpdateQuotaRequest{ContainerMemoryMax: "4 GB"},
			wantStatus: http.StatusBadRequest,
			wantError:  "container_memory_max: invalid quantity (examples: 500m, 2, 512Mi, 10Gi)",
		},
		{
			name:       "container limit above container max",
			body:       UpdateQuotaRequest{ContainerCPULimit: "4", ContainerCPUMax: "2"},
			wantStatus: http.StatusBadRequest,
			wantError:  "container_cpu_limit must not exceed container_cpu_max",
		},
		{
			name:       "container default above the namespace total",
			body:       UpdateQuotaRequest{MemoryLimit: "1Gi", ContainerMemoryLimit: "2Gi"},
			wantStatus: http.StatusBadRequest,
			wantError:  "container_memory_limit must not exceed memory_limit",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			clusterRepo := NewMockClusterRepository()
			quotaRepo := NewMockResourceQuotaRepository()
			seedCluster(clusterRepo, "cluster-1", "test-cluster")
			r := setupQuotaRouter(clusterRepo, NewMockStackInstanceRepository(), quotaRepo, nil, "admin")

			body, err := json.Marshal(tt.body)
			require.NoError(t, err)
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPut, "/api/v1/clusters/cluster-1/quotas", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantError != "" {
				var resp map[string]string
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, tt.wantError, resp["error"])
				return
			}
			var resp models.ResourceQuotaConfig
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			tt.check(t, &resp)

			// GET returns the same fields.
			w = httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/clusters/cluster-1/quotas", nil))
			require.Equal(t, http.StatusOK, w.Code)
			var got models.ResourceQuotaConfig
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
			tt.check(t, &got)
		})
	}
}

func TestSetQuotaOverride_ContainerFields(t *testing.T) {
	t.Parallel()

	clusterQuota := models.ResourceQuotaConfig{
		ClusterID:             "cl-1",
		CPULimit:              "16",
		MemoryLimit:           "24Gi",
		ContainerCPULimit:     "500m",
		ContainerCPUMax:       "2",
		ContainerMemoryLimit:  "512Mi",
		ContainerMemoryMax:    "4Gi",
		EphemeralStorageLimit: "20Gi",
	}

	tests := []struct {
		name       string
		role       string
		body       setQuotaOverrideRequest
		wantStatus int
		wantError  string
	}{
		{
			name:       "user lowers the container values",
			role:       "user",
			body:       setQuotaOverrideRequest{ContainerCPULimit: "250m", ContainerCPUMax: "1", ContainerMemoryMax: "2Gi", EphemeralStorageLimit: "10Gi"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "user above the container cpu max",
			role:       "user",
			body:       setQuotaOverrideRequest{ContainerCPUMax: "4"},
			wantStatus: http.StatusForbidden,
			wantError:  "container_cpu_max 4 exceeds the cluster quota 2; only admin or devops can set a higher quota",
		},
		{
			name:       "user above the container memory default",
			role:       "user",
			body:       setQuotaOverrideRequest{ContainerMemoryLimit: "1Gi"},
			wantStatus: http.StatusForbidden,
			wantError:  "container_memory_limit 1Gi exceeds the cluster quota 512Mi; only admin or devops can set a higher quota",
		},
		{
			name:       "user above the ephemeral storage total",
			role:       "user",
			body:       setQuotaOverrideRequest{EphemeralStorageLimit: "50Gi"},
			wantStatus: http.StatusForbidden,
			wantError:  "ephemeral_storage_limit 50Gi exceeds the cluster quota 20Gi; only admin or devops can set a higher quota",
		},
		{
			name:       "admin above the container max",
			role:       "admin",
			body:       setQuotaOverrideRequest{ContainerCPUMax: "8", ContainerMemoryMax: "16Gi"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "effective quota: override max below the cluster default limit",
			role:       "admin",
			body:       setQuotaOverrideRequest{ContainerCPUMax: "250m"},
			wantStatus: http.StatusBadRequest,
			wantError:  "container_cpu_limit 500m (cluster quota) exceeds the effective container_cpu_max 250m (instance override)",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			instRepo := NewMockStackInstanceRepository()
			inst := seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusRunning)
			inst.ClusterID = "cl-1"
			require.NoError(t, instRepo.Update(inst))
			oRepo := NewMockInstanceQuotaOverrideRepository()
			quotaRepo := NewMockResourceQuotaRepository()
			cq := clusterQuota
			require.NoError(t, quotaRepo.Upsert(context.Background(), &cq))

			h := NewInstanceQuotaOverrideHandler(oRepo, instRepo).
				WithClusterQuotas(quotaRepo, cluster.NewRegistryForTest("cl-1", nil, nil))
			gin.SetMode(gin.TestMode)
			r := gin.New()
			r.Use(injectAuthContext("uid-1", tt.role))
			r.PUT("/api/v1/stack-instances/:id/quota-overrides", h.SetQuotaOverride)

			body, _ := json.Marshal(tt.body)
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPut, "/api/v1/stack-instances/inst-1/quota-overrides", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantError != "" {
				var resp map[string]string
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, tt.wantError, resp["error"])
				return
			}
			var saved models.InstanceQuotaOverride
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &saved))
			assert.Equal(t, tt.body.ContainerCPUMax, saved.ContainerCPUMax)
			assert.Equal(t, tt.body.ContainerCPULimit, saved.ContainerCPULimit)
			assert.Equal(t, tt.body.ContainerMemoryMax, saved.ContainerMemoryMax)
			assert.Equal(t, tt.body.EphemeralStorageLimit, saved.EphemeralStorageLimit)
		})
	}
}

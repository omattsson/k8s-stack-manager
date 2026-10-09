package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/internal/cluster"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// quotaValidationCase is shared by the cluster quota and the instance quota
// override tests: both endpoints apply the same quantity rules (#463).
type quotaValidationCase struct {
	name          string
	cpuRequest    string
	cpuLimit      string
	memoryRequest string
	memoryLimit   string
	storageLimit  string
	wantStatus    int
	wantErrField  string
}

func quotaValidationCases() []quotaValidationCase {
	return []quotaValidationCase{
		{name: "valid quantities are saved", cpuRequest: "500m", cpuLimit: "2", memoryRequest: "512Mi", memoryLimit: "1Gi", storageLimit: "10Gi", wantStatus: http.StatusOK},
		{name: "request equal to limit is valid", cpuRequest: "2", cpuLimit: "2000m", memoryRequest: "1Gi", memoryLimit: "1024Mi", wantStatus: http.StatusOK},
		{name: "only a request is valid", cpuRequest: "20", wantStatus: http.StatusOK},
		{name: "invalid memory limit", memoryLimit: "abc", wantStatus: http.StatusBadRequest, wantErrField: "memory_limit"},
		{name: "invalid cpu request", cpuRequest: "1 core", wantStatus: http.StatusBadRequest, wantErrField: "cpu_request"},
		{name: "invalid storage limit", storageLimit: "10GB!", wantStatus: http.StatusBadRequest, wantErrField: "storage_limit"},
		{name: "negative cpu limit", cpuLimit: "-1", wantStatus: http.StatusBadRequest, wantErrField: "cpu_limit"},
		{name: "too long value", memoryLimit: "100000000000000000000Mi", wantStatus: http.StatusBadRequest, wantErrField: "memory_limit"},
		{name: "cpu request above limit", cpuRequest: "20", cpuLimit: "16", wantStatus: http.StatusBadRequest, wantErrField: "cpu_request"},
		{name: "memory request above limit", memoryRequest: "2Gi", memoryLimit: "1Gi", wantStatus: http.StatusBadRequest, wantErrField: "memory_request"},
	}
}

func TestUpdateQuotas_QuantityValidation(t *testing.T) {
	t.Parallel()

	for _, tt := range quotaValidationCases() {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			clusterRepo := NewMockClusterRepository()
			quotaRepo := NewMockResourceQuotaRepository()
			seedCluster(clusterRepo, "cluster-1", "test-cluster")
			r := setupQuotaRouter(clusterRepo, NewMockStackInstanceRepository(), quotaRepo, nil, "admin")

			body, err := json.Marshal(UpdateQuotaRequest{
				CPURequest:    tt.cpuRequest,
				CPULimit:      tt.cpuLimit,
				MemoryRequest: tt.memoryRequest,
				MemoryLimit:   tt.memoryLimit,
				StorageLimit:  tt.storageLimit,
			})
			require.NoError(t, err)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPut, "/api/v1/clusters/cluster-1/quotas", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())

			_, getErr := quotaRepo.GetByClusterID(context.Background(), "cluster-1")
			if tt.wantStatus != http.StatusOK {
				var resp map[string]string
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Contains(t, resp["error"], tt.wantErrField)
				assert.Error(t, getErr, "invalid quota must not be saved")
				return
			}
			assert.NoError(t, getErr)
		})
	}
}

func TestSetQuotaOverride_QuantityValidation(t *testing.T) {
	t.Parallel()

	for _, tt := range quotaValidationCases() {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			instRepo := NewMockStackInstanceRepository()
			oRepo := NewMockInstanceQuotaOverrideRepository()
			seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusRunning)
			router := setupQuotaOverrideRouter(oRepo, instRepo, "uid-1", "developer")

			body, err := json.Marshal(setQuotaOverrideRequest{
				CPURequest:    tt.cpuRequest,
				CPULimit:      tt.cpuLimit,
				MemoryRequest: tt.memoryRequest,
				MemoryLimit:   tt.memoryLimit,
				StorageLimit:  tt.storageLimit,
			})
			require.NoError(t, err)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPut, "/api/v1/stack-instances/inst-1/quota-overrides", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())

			_, getErr := oRepo.GetByInstanceID(context.Background(), "inst-1")
			if tt.wantStatus != http.StatusOK {
				var resp map[string]string
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Contains(t, resp["error"], tt.wantErrField)
				assert.Error(t, getErr, "invalid override must not be saved")
				return
			}
			assert.NoError(t, getErr)
		})
	}
}

// TestQuotaEndpoints_TrimQuantities checks that both quota endpoints store
// trimmed quantities.
func TestQuotaEndpoints_TrimQuantities(t *testing.T) {
	t.Parallel()

	t.Run("cluster quota", func(t *testing.T) {
		t.Parallel()
		clusterRepo := NewMockClusterRepository()
		quotaRepo := NewMockResourceQuotaRepository()
		seedCluster(clusterRepo, "cluster-1", "test-cluster")
		r := setupQuotaRouter(clusterRepo, NewMockStackInstanceRepository(), quotaRepo, nil, "admin")

		body, _ := json.Marshal(UpdateQuotaRequest{CPURequest: " 500m ", MemoryLimit: "\t1Gi\n"})
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/v1/clusters/cluster-1/quotas", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())

		saved, err := quotaRepo.GetByClusterID(context.Background(), "cluster-1")
		require.NoError(t, err)
		assert.Equal(t, "500m", saved.CPURequest)
		assert.Equal(t, "1Gi", saved.MemoryLimit)
	})

	t.Run("instance override", func(t *testing.T) {
		t.Parallel()
		instRepo := NewMockStackInstanceRepository()
		oRepo := NewMockInstanceQuotaOverrideRepository()
		seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusRunning)
		router := setupQuotaOverrideRouter(oRepo, instRepo, "uid-1", "developer")

		body, _ := json.Marshal(setQuotaOverrideRequest{CPULimit: " 2 ", StorageLimit: " 10Gi"})
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/v1/stack-instances/inst-1/quota-overrides", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())

		saved, err := oRepo.GetByInstanceID(context.Background(), "inst-1")
		require.NoError(t, err)
		assert.Equal(t, "2", saved.CPULimit)
		assert.Equal(t, "10Gi", saved.StorageLimit)
	})
}

// TestSetQuotaOverride_EffectiveQuota checks that the override is validated
// merged with the cluster quota of the instance's cluster (issue 463).
func TestSetQuotaOverride_EffectiveQuota(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		instCluster  string // instance cluster ID; empty resolves the default
		role         string // caller role; empty means "developer"
		clusterQuota *models.ResourceQuotaConfig
		quotaErr     error
		body         setQuotaOverrideRequest
		wantStatus   int
		wantError    string
	}{
		{
			name:         "override request above cluster limit",
			instCluster:  "cl-1",
			clusterQuota: &models.ResourceQuotaConfig{ClusterID: "cl-1", CPULimit: "2"},
			body:         setQuotaOverrideRequest{CPURequest: "4"},
			wantStatus:   http.StatusBadRequest,
			wantError:    "cpu_request 4 (instance override) exceeds the effective cpu_limit 2 (cluster quota)",
		},
		{
			name:         "override limit below cluster request",
			instCluster:  "cl-1",
			clusterQuota: &models.ResourceQuotaConfig{ClusterID: "cl-1", MemoryRequest: "2Gi"},
			body:         setQuotaOverrideRequest{MemoryLimit: "1Gi"},
			wantStatus:   http.StatusBadRequest,
			wantError:    "memory_request 2Gi (cluster quota) exceeds the effective memory_limit 1Gi (instance override)",
		},
		{
			name:         "default cluster quota is used for an instance without cluster",
			instCluster:  "",
			clusterQuota: &models.ResourceQuotaConfig{ClusterID: "cl-1", CPULimit: "2"},
			body:         setQuotaOverrideRequest{CPURequest: "3"},
			wantStatus:   http.StatusBadRequest,
			wantError:    "cpu_request 3 (instance override) exceeds the effective cpu_limit 2 (cluster quota)",
		},
		{
			name:         "override within cluster limit",
			instCluster:  "cl-1",
			clusterQuota: &models.ResourceQuotaConfig{ClusterID: "cl-1", CPULimit: "2"},
			body:         setQuotaOverrideRequest{CPURequest: "1"},
			wantStatus:   http.StatusOK,
		},
		{
			name:         "override raises the cluster limit",
			instCluster:  "cl-1",
			role:         "admin",
			clusterQuota: &models.ResourceQuotaConfig{ClusterID: "cl-1", CPURequest: "1", CPULimit: "2"},
			body:         setQuotaOverrideRequest{CPURequest: "4", CPULimit: "8"},
			wantStatus:   http.StatusOK,
		},
		{
			name:        "no cluster quota",
			instCluster: "cl-1",
			body:        setQuotaOverrideRequest{CPURequest: "4"},
			wantStatus:  http.StatusOK,
		},
		{
			name:        "cluster quota lookup error fails closed",
			instCluster: "cl-1",
			quotaErr:    errors.New("connection refused"),
			body:        setQuotaOverrideRequest{CPURequest: "1"},
			wantStatus:  http.StatusInternalServerError,
			wantError:   msgInternalServerError,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			instRepo := NewMockStackInstanceRepository()
			inst := seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusRunning)
			inst.ClusterID = tt.instCluster
			require.NoError(t, instRepo.Update(inst))
			oRepo := NewMockInstanceQuotaOverrideRepository()
			quotaRepo := NewMockResourceQuotaRepository()
			if tt.clusterQuota != nil {
				require.NoError(t, quotaRepo.Upsert(context.Background(), tt.clusterQuota))
			}
			if tt.quotaErr != nil {
				quotaRepo.SetError(tt.quotaErr)
			}

			h := NewInstanceQuotaOverrideHandler(oRepo, instRepo).
				WithClusterQuotas(quotaRepo, cluster.NewRegistryForTest("cl-1", nil, nil))
			gin.SetMode(gin.TestMode)
			r := gin.New()
			role := tt.role
			if role == "" {
				role = "developer"
			}
			r.Use(injectAuthContext("uid-1", role))
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
			}
			_, getErr := oRepo.GetByInstanceID(context.Background(), "inst-1")
			if tt.wantStatus == http.StatusOK {
				assert.NoError(t, getErr)
			} else {
				assert.Error(t, getErr, "rejected override must not be saved")
			}
		})
	}
}

// TestUpdateQuotas_InstanceOverrideConflicts checks that a cluster quota that
// breaks the quota override of an instance on the cluster is rejected.
func TestUpdateQuotas_InstanceOverrideConflicts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		isDefault    bool
		instances    map[string]string // instance ID -> cluster ID
		overrides    map[string]string // instance ID -> override cpu_request
		overrideErr  error
		body         UpdateQuotaRequest
		wantStatus   int
		wantError    string
		wantContains []string
	}{
		{
			name:       "override request above new cluster limit",
			instances:  map[string]string{"i-a": "cl-1"},
			overrides:  map[string]string{"i-a": "4"},
			body:       UpdateQuotaRequest{CPULimit: "2"},
			wantStatus: http.StatusBadRequest,
			wantError:  "the quota conflicts with the quota override of 1 instance(s): name-i-a; first conflict (name-i-a): cpu_request 4 (instance override) exceeds the effective cpu_limit 2 (cluster quota)",
		},
		{
			name:       "override within new cluster limit",
			instances:  map[string]string{"i-a": "cl-1"},
			overrides:  map[string]string{"i-a": "1"},
			body:       UpdateQuotaRequest{CPULimit: "2"},
			wantStatus: http.StatusOK,
		},
		{
			name:         "default cluster covers instances without cluster",
			isDefault:    true,
			instances:    map[string]string{"i-a": ""},
			overrides:    map[string]string{"i-a": "4"},
			body:         UpdateQuotaRequest{CPULimit: "2"},
			wantStatus:   http.StatusBadRequest,
			wantContains: []string{"name-i-a"},
		},
		{
			name:       "non-default cluster ignores instances without cluster",
			instances:  map[string]string{"i-a": "", "i-b": "cl-2"},
			overrides:  map[string]string{"i-a": "4", "i-b": "4"},
			body:       UpdateQuotaRequest{CPULimit: "2"},
			wantStatus: http.StatusOK,
		},
		{
			name: "lists at most 10 instance names",
			instances: map[string]string{
				"i-01": "cl-1", "i-02": "cl-1", "i-03": "cl-1", "i-04": "cl-1", "i-05": "cl-1", "i-06": "cl-1",
				"i-07": "cl-1", "i-08": "cl-1", "i-09": "cl-1", "i-10": "cl-1", "i-11": "cl-1", "i-12": "cl-1",
			},
			overrides: map[string]string{
				"i-01": "4", "i-02": "4", "i-03": "4", "i-04": "4", "i-05": "4", "i-06": "4",
				"i-07": "4", "i-08": "4", "i-09": "4", "i-10": "4", "i-11": "4", "i-12": "4",
			},
			body:         UpdateQuotaRequest{CPULimit: "2"},
			wantStatus:   http.StatusBadRequest,
			wantContains: []string{"12 instance(s): name-i-01, name-i-02", "name-i-10 (+2 more); first conflict (name-i-01)"},
		},
		{
			name:        "override lookup error fails closed",
			instances:   map[string]string{"i-a": "cl-1"},
			overrides:   map[string]string{"i-a": "1"},
			overrideErr: errors.New("connection refused"),
			body:        UpdateQuotaRequest{CPULimit: "2"},
			wantStatus:  http.StatusInternalServerError,
			wantError:   msgInternalServerError,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			clusterRepo := NewMockClusterRepository()
			require.NoError(t, clusterRepo.Create(&models.Cluster{ID: "cl-1", Name: "one", IsDefault: tt.isDefault}))
			instRepo := NewMockStackInstanceRepository()
			for id, clusterID := range tt.instances {
				inst := seedInstance(t, instRepo, id, "name-"+id, "def-1", "uid-1", models.StackStatusRunning)
				inst.ClusterID = clusterID
				require.NoError(t, instRepo.Update(inst))
			}
			oRepo := NewMockInstanceQuotaOverrideRepository()
			for id, cpu := range tt.overrides {
				require.NoError(t, oRepo.Upsert(context.Background(), &models.InstanceQuotaOverride{StackInstanceID: id, CPURequest: cpu}))
			}
			if tt.overrideErr != nil {
				oRepo.SetError(tt.overrideErr)
			}
			quotaRepo := NewMockResourceQuotaRepository()

			h := NewClusterHandlerWithQuotas(clusterRepo, nil, instRepo, quotaRepo).WithInstanceQuotaOverrides(oRepo)
			gin.SetMode(gin.TestMode)
			r := gin.New()
			r.Use(injectAuthContext("admin-1", "admin"))
			r.PUT("/api/v1/clusters/:id/quotas", h.UpdateQuotas)

			body, _ := json.Marshal(tt.body)
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPut, "/api/v1/clusters/cl-1/quotas", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			var resp map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			if tt.wantError != "" {
				assert.Equal(t, tt.wantError, resp["error"])
			}
			for _, part := range tt.wantContains {
				assert.Contains(t, resp["error"], part)
			}
			_, getErr := quotaRepo.GetByClusterID(context.Background(), "cl-1")
			if tt.wantStatus == http.StatusOK {
				assert.NoError(t, getErr)
			} else {
				assert.Error(t, getErr, "rejected quota must not be saved")
			}
		})
	}
}

// TestSetQuotaOverride_ClusterQuotaCap checks that an owner without the admin
// or devops role cannot set an override above the cluster quota, and that
// admin and devops can.
func TestSetQuotaOverride_ClusterQuotaCap(t *testing.T) {
	t.Parallel()

	pods := func(n int) *int { return &n }
	clusterQuota := &models.ResourceQuotaConfig{
		ClusterID:     "cl-1",
		CPURequest:    "8",
		CPULimit:      "16",
		MemoryRequest: "12Gi",
		MemoryLimit:   "24Gi",
		PodLimit:      20,
	}

	tests := []struct {
		name         string
		role         string
		clusterQuota *models.ResourceQuotaConfig
		existing     *models.InstanceQuotaOverride // stored override before the request
		body         setQuotaOverrideRequest
		wantStatus   int
		wantError    string
	}{
		{
			name:       "user keeps an admin grant and lowers another field",
			role:       "user",
			existing:   &models.InstanceQuotaOverride{CPULimit: "64", MemoryLimit: "20Gi"},
			body:       setQuotaOverrideRequest{CPULimit: "64000m", MemoryLimit: "16Gi"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "user raises an admin grant",
			role:       "user",
			existing:   &models.InstanceQuotaOverride{CPULimit: "64"},
			body:       setQuotaOverrideRequest{CPULimit: "65"},
			wantStatus: http.StatusForbidden,
			wantError:  "cpu_limit 65 exceeds the cluster quota 16; only admin or devops can set a higher quota",
		},
		{
			name:       "user adds a new value above the cap next to an admin grant",
			role:       "user",
			existing:   &models.InstanceQuotaOverride{CPULimit: "64"},
			body:       setQuotaOverrideRequest{CPULimit: "64", MemoryLimit: "48Gi"},
			wantStatus: http.StatusForbidden,
			wantError:  "memory_limit 48Gi exceeds the cluster quota 24Gi; only admin or devops can set a higher quota",
		},
		{
			name:       "user above the cpu limit",
			role:       "user",
			body:       setQuotaOverrideRequest{CPULimit: "64"},
			wantStatus: http.StatusForbidden,
			wantError:  "cpu_limit 64 exceeds the cluster quota 16; only admin or devops can set a higher quota",
		},
		{
			name:       "user above the memory limit",
			role:       "user",
			body:       setQuotaOverrideRequest{MemoryLimit: "256Gi"},
			wantStatus: http.StatusForbidden,
			wantError:  "memory_limit 256Gi exceeds the cluster quota 24Gi; only admin or devops can set a higher quota",
		},
		{
			name:       "user above the pod limit",
			role:       "user",
			body:       setQuotaOverrideRequest{PodLimit: pods(50)},
			wantStatus: http.StatusForbidden,
			wantError:  "pod_limit 50 exceeds the cluster quota 20; only admin or devops can set a higher quota",
		},
		{
			name:       "user removes the pod limit",
			role:       "user",
			body:       setQuotaOverrideRequest{PodLimit: pods(0)},
			wantStatus: http.StatusForbidden,
			wantError:  "pod_limit 0 (no limit) exceeds the cluster quota 20; only admin or devops can set a higher quota",
		},
		{
			name:       "user lowers the quota",
			role:       "user",
			body:       setQuotaOverrideRequest{CPURequest: "1", CPULimit: "2", MemoryRequest: "2Gi", MemoryLimit: "4Gi", PodLimit: pods(5)},
			wantStatus: http.StatusOK,
		},
		{
			name:       "user equal to the cluster quota in millicores",
			role:       "user",
			body:       setQuotaOverrideRequest{CPULimit: "16000m"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "user one millicore above the cluster quota",
			role:       "user",
			body:       setQuotaOverrideRequest{CPULimit: "16001m"},
			wantStatus: http.StatusForbidden,
			wantError:  "cpu_limit 16001m exceeds the cluster quota 16; only admin or devops can set a higher quota",
		},
		{
			name:       "user equal to the cluster quota in bytes",
			role:       "user",
			body:       setQuotaOverrideRequest{MemoryLimit: "25769803776"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "user one byte above the cluster quota",
			role:       "user",
			body:       setQuotaOverrideRequest{MemoryLimit: "25769803777"},
			wantStatus: http.StatusForbidden,
			wantError:  "memory_limit 25769803777 exceeds the cluster quota 24Gi; only admin or devops can set a higher quota",
		},
		{
			name:       "field without a cluster value has no cap",
			role:       "user",
			body:       setQuotaOverrideRequest{StorageLimit: "1Ti"},
			wantStatus: http.StatusOK,
		},
		{
			name:         "no cluster quota has no cap",
			role:         "user",
			clusterQuota: &models.ResourceQuotaConfig{ClusterID: "cl-other", CPULimit: "1"},
			body:         setQuotaOverrideRequest{CPULimit: "64"},
			wantStatus:   http.StatusOK,
		},
		{
			name:       "developer above the cpu limit",
			role:       "developer",
			body:       setQuotaOverrideRequest{CPULimit: "17"},
			wantStatus: http.StatusForbidden,
			wantError:  "cpu_limit 17 exceeds the cluster quota 16; only admin or devops can set a higher quota",
		},
		{
			name:       "devops above the cluster quota",
			role:       "devops",
			body:       setQuotaOverrideRequest{CPULimit: "64", MemoryLimit: "256Gi", PodLimit: pods(0)},
			wantStatus: http.StatusOK,
		},
		{
			name:       "admin above the cluster quota",
			role:       "admin",
			body:       setQuotaOverrideRequest{CPULimit: "64", MemoryLimit: "256Gi", PodLimit: pods(100)},
			wantStatus: http.StatusOK,
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
			if tt.existing != nil {
				existing := *tt.existing
				existing.StackInstanceID = "inst-1"
				require.NoError(t, oRepo.Upsert(context.Background(), &existing))
			}
			quotaRepo := NewMockResourceQuotaRepository()
			// Copy: the mock Upsert writes to the stored value.
			cq := *clusterQuota
			if tt.clusterQuota != nil {
				cq = *tt.clusterQuota
			}
			require.NoError(t, quotaRepo.Upsert(context.Background(), &cq))

			h := NewInstanceQuotaOverrideHandler(oRepo, instRepo).
				WithClusterQuotas(quotaRepo, cluster.NewRegistryForTest("cl-1", nil, nil))
			gin.SetMode(gin.TestMode)
			r := gin.New()
			// The caller owns the instance, so the modify rule passes for
			// every role.
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
			}
			stored, getErr := oRepo.GetByInstanceID(context.Background(), "inst-1")
			switch {
			case tt.wantStatus == http.StatusOK:
				require.NoError(t, getErr)
				assert.Equal(t, tt.body.CPULimit, stored.CPULimit)
				assert.Equal(t, tt.body.MemoryLimit, stored.MemoryLimit)
			case tt.existing != nil:
				require.NoError(t, getErr)
				assert.Equal(t, tt.existing.CPULimit, stored.CPULimit, "rejected override must not be saved")
				assert.Equal(t, tt.existing.MemoryLimit, stored.MemoryLimit, "rejected override must not be saved")
			default:
				assert.Error(t, getErr, "rejected override must not be saved")
			}
		})
	}
}

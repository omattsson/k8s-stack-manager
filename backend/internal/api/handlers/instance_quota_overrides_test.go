package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupQuotaOverrideRouter creates a test gin engine with InstanceQuotaOverrideHandler routes.
func setupQuotaOverrideRouter(
	overrideRepo *MockInstanceQuotaOverrideRepository,
	instanceRepo *MockStackInstanceRepository,
	callerID, callerRole string,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(injectAuthContext(callerID, callerRole))

	h := NewInstanceQuotaOverrideHandler(overrideRepo, instanceRepo)

	instances := r.Group("/api/v1/stack-instances")
	{
		instances.GET("/:id/quota-overrides", h.GetQuotaOverride)
		instances.PUT("/:id/quota-overrides", h.SetQuotaOverride)
		instances.DELETE("/:id/quota-overrides", h.DeleteQuotaOverride)
	}
	return r
}

// ---- GetQuotaOverride ----

func TestGetQuotaOverride(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		instanceID string
		callerID   string
		callerRole string
		setup      func(*MockStackInstanceRepository, *MockInstanceQuotaOverrideRepository)
		wantStatus int
		checkBody  func(t *testing.T, body []byte)
	}{
		{
			name:       "owner can get quota override",
			instanceID: "inst-1",
			callerID:   "uid-1",
			callerRole: "developer",
			setup: func(instRepo *MockStackInstanceRepository, oRepo *MockInstanceQuotaOverrideRepository) {
				seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusRunning)
				oRepo.Upsert(nil, &models.InstanceQuotaOverride{
					StackInstanceID: "inst-1",
					CPURequest:      "500m",
					CPULimit:        "2000m",
					MemoryRequest:   "256Mi",
					MemoryLimit:     "1Gi",
				})
			},
			wantStatus: http.StatusOK,
			checkBody: func(t *testing.T, body []byte) {
				var o models.InstanceQuotaOverride
				require.NoError(t, json.Unmarshal(body, &o))
				assert.Equal(t, "inst-1", o.StackInstanceID)
				assert.Equal(t, "500m", o.CPURequest)
				assert.Equal(t, "2000m", o.CPULimit)
			},
		},
		{
			name:       "admin can get quota override for any instance",
			instanceID: "inst-1",
			callerID:   "admin-user",
			callerRole: "admin",
			setup: func(instRepo *MockStackInstanceRepository, oRepo *MockInstanceQuotaOverrideRepository) {
				seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusRunning)
				oRepo.Upsert(nil, &models.InstanceQuotaOverride{
					StackInstanceID: "inst-1",
					CPURequest:      "100m",
				})
			},
			wantStatus: http.StatusOK,
			checkBody: func(t *testing.T, body []byte) {
				var o models.InstanceQuotaOverride
				require.NoError(t, json.Unmarshal(body, &o))
				assert.Equal(t, "100m", o.CPURequest)
			},
		},
		{
			name:       "non-owner non-admin gets 403",
			instanceID: "inst-1",
			callerID:   "uid-other",
			callerRole: "developer",
			setup: func(instRepo *MockStackInstanceRepository, _ *MockInstanceQuotaOverrideRepository) {
				seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusRunning)
			},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "instance not found returns 404",
			instanceID: "nonexistent",
			callerID:   "uid-1",
			callerRole: "admin",
			setup:      func(_ *MockStackInstanceRepository, _ *MockInstanceQuotaOverrideRepository) {},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "no override returns 404",
			instanceID: "inst-1",
			callerID:   "uid-1",
			callerRole: "developer",
			setup: func(instRepo *MockStackInstanceRepository, _ *MockInstanceQuotaOverrideRepository) {
				seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusDraft)
			},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "repo error returns 500",
			instanceID: "inst-1",
			callerID:   "uid-1",
			callerRole: "developer",
			setup: func(instRepo *MockStackInstanceRepository, oRepo *MockInstanceQuotaOverrideRepository) {
				seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusRunning)
				oRepo.SetError(assert.AnError)
			},
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			instRepo := NewMockStackInstanceRepository()
			oRepo := NewMockInstanceQuotaOverrideRepository()
			tt.setup(instRepo, oRepo)

			router := setupQuotaOverrideRouter(oRepo, instRepo, tt.callerID, tt.callerRole)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet,
				"/api/v1/stack-instances/"+tt.instanceID+"/quota-overrides", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.checkBody != nil {
				tt.checkBody(t, w.Body.Bytes())
			}
		})
	}
}

// ---- SetQuotaOverride ----

func TestSetQuotaOverride(t *testing.T) {
	t.Parallel()

	podLimit := 20

	tests := []struct {
		name       string
		instanceID string
		callerID   string
		callerRole string
		body       interface{}
		setup      func(*MockStackInstanceRepository, *MockInstanceQuotaOverrideRepository)
		wantStatus int
		checkBody  func(t *testing.T, body []byte)
	}{
		{
			name:       "owner sets quota override",
			instanceID: "inst-1",
			callerID:   "uid-1",
			callerRole: "developer",
			body: setQuotaOverrideRequest{
				CPURequest:    "500m",
				CPULimit:      "2000m",
				MemoryRequest: "256Mi",
				MemoryLimit:   "1Gi",
				StorageLimit:  "10Gi",
				PodLimit:      &podLimit,
			},
			setup: func(instRepo *MockStackInstanceRepository, _ *MockInstanceQuotaOverrideRepository) {
				seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusRunning)
			},
			wantStatus: http.StatusOK,
			checkBody: func(t *testing.T, body []byte) {
				var o models.InstanceQuotaOverride
				require.NoError(t, json.Unmarshal(body, &o))
				assert.Equal(t, "inst-1", o.StackInstanceID)
				assert.Equal(t, "500m", o.CPURequest)
				assert.Equal(t, "2000m", o.CPULimit)
				assert.Equal(t, "256Mi", o.MemoryRequest)
				assert.Equal(t, "1Gi", o.MemoryLimit)
				assert.Equal(t, "10Gi", o.StorageLimit)
				require.NotNil(t, o.PodLimit)
				assert.Equal(t, 20, *o.PodLimit)
			},
		},
		{
			name:       "admin sets quota override for other user",
			instanceID: "inst-1",
			callerID:   "admin-user",
			callerRole: "admin",
			body: setQuotaOverrideRequest{
				CPURequest: "100m",
			},
			setup: func(instRepo *MockStackInstanceRepository, _ *MockInstanceQuotaOverrideRepository) {
				seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusRunning)
			},
			wantStatus: http.StatusOK,
			checkBody: func(t *testing.T, body []byte) {
				var o models.InstanceQuotaOverride
				require.NoError(t, json.Unmarshal(body, &o))
				assert.Equal(t, "100m", o.CPURequest)
			},
		},
		{
			name:       "non-owner non-admin gets 403",
			instanceID: "inst-1",
			callerID:   "uid-other",
			callerRole: "developer",
			body:       setQuotaOverrideRequest{CPURequest: "500m"},
			setup: func(instRepo *MockStackInstanceRepository, _ *MockInstanceQuotaOverrideRepository) {
				seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusRunning)
			},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "instance not found returns 404",
			instanceID: "nonexistent",
			callerID:   "uid-1",
			callerRole: "admin",
			body:       setQuotaOverrideRequest{CPURequest: "500m"},
			setup:      func(_ *MockStackInstanceRepository, _ *MockInstanceQuotaOverrideRepository) {},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "invalid JSON returns 400",
			instanceID: "inst-1",
			callerID:   "uid-1",
			callerRole: "developer",
			body:       "not-json",
			setup: func(instRepo *MockStackInstanceRepository, _ *MockInstanceQuotaOverrideRepository) {
				seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusRunning)
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "upsert error returns 500",
			instanceID: "inst-1",
			callerID:   "uid-1",
			callerRole: "developer",
			body:       setQuotaOverrideRequest{CPURequest: "500m"},
			setup: func(instRepo *MockStackInstanceRepository, oRepo *MockInstanceQuotaOverrideRepository) {
				seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusRunning)
				oRepo.SetError(assert.AnError)
			},
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			instRepo := NewMockStackInstanceRepository()
			oRepo := NewMockInstanceQuotaOverrideRepository()
			tt.setup(instRepo, oRepo)

			router := setupQuotaOverrideRouter(oRepo, instRepo, tt.callerID, tt.callerRole)

			var bodyBytes []byte
			switch v := tt.body.(type) {
			case string:
				bodyBytes = []byte(v)
			default:
				bodyBytes, _ = json.Marshal(v)
			}

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPut,
				"/api/v1/stack-instances/"+tt.instanceID+"/quota-overrides",
				bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.checkBody != nil {
				tt.checkBody(t, w.Body.Bytes())
			}
		})
	}
}

// ---- DeleteQuotaOverride ----

func TestDeleteQuotaOverride(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		instanceID string
		callerID   string
		callerRole string
		setup      func(*MockStackInstanceRepository, *MockInstanceQuotaOverrideRepository)
		wantStatus int
	}{
		{
			name:       "owner deletes own quota override",
			instanceID: "inst-1",
			callerID:   "uid-1",
			callerRole: "developer",
			setup: func(instRepo *MockStackInstanceRepository, oRepo *MockInstanceQuotaOverrideRepository) {
				seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusRunning)
				oRepo.Upsert(nil, &models.InstanceQuotaOverride{
					StackInstanceID: "inst-1",
					CPURequest:      "500m",
				})
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "admin deletes override for other user",
			instanceID: "inst-1",
			callerID:   "admin-user",
			callerRole: "admin",
			setup: func(instRepo *MockStackInstanceRepository, oRepo *MockInstanceQuotaOverrideRepository) {
				seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusRunning)
				oRepo.Upsert(nil, &models.InstanceQuotaOverride{
					StackInstanceID: "inst-1",
					CPURequest:      "500m",
				})
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "non-owner non-admin gets 403",
			instanceID: "inst-1",
			callerID:   "uid-other",
			callerRole: "developer",
			setup: func(instRepo *MockStackInstanceRepository, _ *MockInstanceQuotaOverrideRepository) {
				seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusRunning)
			},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "instance not found returns 404",
			instanceID: "nonexistent",
			callerID:   "uid-1",
			callerRole: "admin",
			setup:      func(_ *MockStackInstanceRepository, _ *MockInstanceQuotaOverrideRepository) {},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "override not found returns 404",
			instanceID: "inst-1",
			callerID:   "uid-1",
			callerRole: "developer",
			setup: func(instRepo *MockStackInstanceRepository, _ *MockInstanceQuotaOverrideRepository) {
				seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusDraft)
			},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "repo error returns 500",
			instanceID: "inst-1",
			callerID:   "uid-1",
			callerRole: "developer",
			setup: func(instRepo *MockStackInstanceRepository, oRepo *MockInstanceQuotaOverrideRepository) {
				seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusRunning)
				oRepo.SetError(assert.AnError)
			},
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			instRepo := NewMockStackInstanceRepository()
			oRepo := NewMockInstanceQuotaOverrideRepository()
			tt.setup(instRepo, oRepo)

			router := setupQuotaOverrideRouter(oRepo, instRepo, tt.callerID, tt.callerRole)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodDelete,
				"/api/v1/stack-instances/"+tt.instanceID+"/quota-overrides", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
		})
	}
}

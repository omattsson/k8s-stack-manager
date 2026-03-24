package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/internal/helm"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupCompareRouter creates a test gin engine with the compare endpoint registered.
func setupCompareRouter(
	instanceRepo *MockStackInstanceRepository,
	overrideRepo *MockValueOverrideRepository,
	defRepo *MockStackDefinitionRepository,
	ccRepo *MockChartConfigRepository,
	tmplRepo *MockStackTemplateRepository,
	tmplChartRepo *MockTemplateChartConfigRepository,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userID", "test-user")
		c.Set("username", "tester")
		c.Set("role", "devops")
		c.Next()
	})

	valuesGen := helm.NewValuesGenerator()
	userRepo := NewMockUserRepository()
	h := NewInstanceHandler(instanceRepo, overrideRepo, nil, defRepo, ccRepo, tmplRepo, tmplChartRepo, valuesGen, userRepo, 0)

	r.GET("/api/v1/stack-instances/compare", h.CompareInstances)
	return r
}

func TestCompareInstances(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		queryLeft  string
		queryRight string
		setup      func(
			instRepo *MockStackInstanceRepository,
			defRepo *MockStackDefinitionRepository,
			ccRepo *MockChartConfigRepository,
			ovRepo *MockValueOverrideRepository,
		)
		wantStatus int
		checkFn    func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:       "missing left parameter returns 400",
			queryLeft:  "",
			queryRight: "right-id",
			setup:      func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository, ovRepo *MockValueOverrideRepository) {},
			wantStatus: http.StatusBadRequest,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				t.Helper()
				var resp map[string]string
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Contains(t, resp["error"], "required")
			},
		},
		{
			name:       "missing right parameter returns 400",
			queryLeft:  "left-id",
			queryRight: "",
			setup:      func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository, ovRepo *MockValueOverrideRepository) {},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing both parameters returns 400",
			queryLeft:  "",
			queryRight: "",
			setup:      func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository, ovRepo *MockValueOverrideRepository) {},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "left instance not found returns 404",
			queryLeft:  "nonexistent",
			queryRight: "right-id",
			setup:      func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository, ovRepo *MockValueOverrideRepository) {},
			wantStatus: http.StatusNotFound,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				t.Helper()
				var resp map[string]string
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Contains(t, resp["error"], "Left stack instance")
			},
		},
		{
			name:       "right instance not found returns 404",
			queryLeft:  "i-left",
			queryRight: "nonexistent",
			setup: func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository, ovRepo *MockValueOverrideRepository) {
				seedInstance(t, instRepo, "i-left", "stack-left", "d1", "uid-1", models.StackStatusRunning)
			},
			wantStatus: http.StatusNotFound,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				t.Helper()
				var resp map[string]string
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Contains(t, resp["error"], "Right stack instance")
			},
		},
		{
			name:       "same instance compared to itself returns no differences",
			queryLeft:  "i1",
			queryRight: "i1",
			setup: func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository, ovRepo *MockValueOverrideRepository) {
				seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusRunning)
				require.NoError(t, defRepo.Create(&models.StackDefinition{
					ID:   "d1",
					Name: "def-a",
				}))
				require.NoError(t, ccRepo.Create(&models.ChartConfig{
					ID:                "cc1",
					StackDefinitionID: "d1",
					ChartName:         "my-chart",
					DefaultValues:     "key: value\n",
				}))
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				t.Helper()
				var resp CompareInstancesResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, "i1", resp.Left.ID)
				assert.Equal(t, "i1", resp.Right.ID)
				require.Len(t, resp.Charts, 1)
				assert.Equal(t, "my-chart", resp.Charts[0].ChartName)
				assert.False(t, resp.Charts[0].HasDifferences)
			},
		},
		{
			name:       "instances with same chart and different overrides show differences",
			queryLeft:  "i-left",
			queryRight: "i-right",
			setup: func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository, ovRepo *MockValueOverrideRepository) {
				seedInstance(t, instRepo, "i-left", "stack-left", "d1", "uid-1", models.StackStatusRunning)
				seedInstance(t, instRepo, "i-right", "stack-right", "d1", "uid-2", models.StackStatusRunning)
				require.NoError(t, defRepo.Create(&models.StackDefinition{
					ID:   "d1",
					Name: "def-a",
				}))
				require.NoError(t, ccRepo.Create(&models.ChartConfig{
					ID:                "cc1",
					StackDefinitionID: "d1",
					ChartName:         "my-chart",
					DefaultValues:     "key: default\n",
				}))
				require.NoError(t, ovRepo.Create(&models.ValueOverride{
					ID:              "ov1",
					StackInstanceID: "i-left",
					ChartConfigID:   "cc1",
					Values:          "key: left-override\n",
				}))
				require.NoError(t, ovRepo.Create(&models.ValueOverride{
					ID:              "ov2",
					StackInstanceID: "i-right",
					ChartConfigID:   "cc1",
					Values:          "key: right-override\n",
				}))
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				t.Helper()
				var resp CompareInstancesResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, "i-left", resp.Left.ID)
				assert.Equal(t, "i-right", resp.Right.ID)
				require.Len(t, resp.Charts, 1)
				assert.Equal(t, "my-chart", resp.Charts[0].ChartName)
				assert.True(t, resp.Charts[0].HasDifferences)
				require.NotNil(t, resp.Charts[0].LeftValues)
				require.NotNil(t, resp.Charts[0].RightValues)
				assert.Contains(t, *resp.Charts[0].LeftValues, "left-override")
				assert.Contains(t, *resp.Charts[0].RightValues, "right-override")
			},
		},
		{
			name:       "instances with different definitions and different charts",
			queryLeft:  "i-left",
			queryRight: "i-right",
			setup: func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository, ovRepo *MockValueOverrideRepository) {
				seedInstance(t, instRepo, "i-left", "stack-left", "d1", "uid-1", models.StackStatusRunning)
				seedInstance(t, instRepo, "i-right", "stack-right", "d2", "uid-2", models.StackStatusRunning)
				require.NoError(t, defRepo.Create(&models.StackDefinition{
					ID:   "d1",
					Name: "def-a",
				}))
				require.NoError(t, defRepo.Create(&models.StackDefinition{
					ID:   "d2",
					Name: "def-b",
				}))
				require.NoError(t, ccRepo.Create(&models.ChartConfig{
					ID:                "cc1",
					StackDefinitionID: "d1",
					ChartName:         "chart-only-left",
					DefaultValues:     "a: 1\n",
				}))
				require.NoError(t, ccRepo.Create(&models.ChartConfig{
					ID:                "cc2",
					StackDefinitionID: "d2",
					ChartName:         "chart-only-right",
					DefaultValues:     "b: 2\n",
				}))
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				t.Helper()
				var resp CompareInstancesResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, "def-a", resp.Left.DefinitionName)
				assert.Equal(t, "def-b", resp.Right.DefinitionName)
				require.Len(t, resp.Charts, 2)

				// Charts should be sorted alphabetically.
				assert.Equal(t, "chart-only-left", resp.Charts[0].ChartName)
				assert.Equal(t, "chart-only-right", resp.Charts[1].ChartName)

				// chart-only-left: present on left, missing on right.
				assert.NotNil(t, resp.Charts[0].LeftValues)
				assert.Nil(t, resp.Charts[0].RightValues)
				assert.True(t, resp.Charts[0].HasDifferences)

				// chart-only-right: missing on left, present on right.
				assert.Nil(t, resp.Charts[1].LeftValues)
				assert.NotNil(t, resp.Charts[1].RightValues)
				assert.True(t, resp.Charts[1].HasDifferences)
			},
		},
		{
			name:       "instances with identical values show no differences",
			queryLeft:  "i-left",
			queryRight: "i-right",
			setup: func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository, ovRepo *MockValueOverrideRepository) {
				// Both instances share the same definition and no overrides.
				inst1 := &models.StackInstance{
					ID:                "i-left",
					StackDefinitionID: "d1",
					Name:              "stack-x",
					Namespace:         "stack-stack-x-owner",
					OwnerID:           "uid-1",
					Branch:            "master",
					Status:            models.StackStatusRunning,
				}
				inst2 := &models.StackInstance{
					ID:                "i-right",
					StackDefinitionID: "d1",
					Name:              "stack-x",
					Namespace:         "stack-stack-x-owner",
					OwnerID:           "uid-1",
					Branch:            "master",
					Status:            models.StackStatusRunning,
				}
				require.NoError(t, instRepo.Create(inst1))
				require.NoError(t, instRepo.Create(inst2))
				require.NoError(t, defRepo.Create(&models.StackDefinition{
					ID:   "d1",
					Name: "def-a",
				}))
				require.NoError(t, ccRepo.Create(&models.ChartConfig{
					ID:                "cc1",
					StackDefinitionID: "d1",
					ChartName:         "my-chart",
					DefaultValues:     "key: value\n",
				}))
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				t.Helper()
				var resp CompareInstancesResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				require.Len(t, resp.Charts, 1)
				assert.False(t, resp.Charts[0].HasDifferences)
			},
		},
		{
			name:       "definition not found for left instance returns error",
			queryLeft:  "i-left",
			queryRight: "i-right",
			setup: func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository, ovRepo *MockValueOverrideRepository) {
				// Left instance points to a non-existent definition.
				seedInstance(t, instRepo, "i-left", "stack-left", "d-missing", "uid-1", models.StackStatusRunning)
				seedInstance(t, instRepo, "i-right", "stack-right", "d1", "uid-2", models.StackStatusRunning)
				require.NoError(t, defRepo.Create(&models.StackDefinition{
					ID:   "d1",
					Name: "def-a",
				}))
			},
			wantStatus: http.StatusNotFound,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				t.Helper()
				var resp map[string]string
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Contains(t, resp["error"], "Stack definition")
			},
		},
		{
			name:       "no charts returns empty charts array",
			queryLeft:  "i-left",
			queryRight: "i-right",
			setup: func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository, ovRepo *MockValueOverrideRepository) {
				seedInstance(t, instRepo, "i-left", "stack-left", "d1", "uid-1", models.StackStatusRunning)
				seedInstance(t, instRepo, "i-right", "stack-right", "d2", "uid-2", models.StackStatusRunning)
				require.NoError(t, defRepo.Create(&models.StackDefinition{
					ID:   "d1",
					Name: "def-a",
				}))
				require.NoError(t, defRepo.Create(&models.StackDefinition{
					ID:   "d2",
					Name: "def-b",
				}))
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				t.Helper()
				var resp CompareInstancesResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Empty(t, resp.Charts)
			},
		},
		{
			name:       "chart config repo error returns 500",
			queryLeft:  "i-left",
			queryRight: "i-right",
			setup: func(instRepo *MockStackInstanceRepository, defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository, ovRepo *MockValueOverrideRepository) {
				seedInstance(t, instRepo, "i-left", "stack-left", "d1", "uid-1", models.StackStatusRunning)
				seedInstance(t, instRepo, "i-right", "stack-right", "d1", "uid-2", models.StackStatusRunning)
				require.NoError(t, defRepo.Create(&models.StackDefinition{
					ID:   "d1",
					Name: "def-a",
				}))
				ccRepo.SetError(errInternal)
			},
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			instRepo := NewMockStackInstanceRepository()
			defRepo := NewMockStackDefinitionRepository()
			ccRepo := NewMockChartConfigRepository()
			ovRepo := NewMockValueOverrideRepository()
			tmplRepo := NewMockStackTemplateRepository()
			tmplChartRepo := NewMockTemplateChartConfigRepository()

			tt.setup(instRepo, defRepo, ccRepo, ovRepo)

			router := setupCompareRouter(instRepo, ovRepo, defRepo, ccRepo, tmplRepo, tmplChartRepo)

			path := "/api/v1/stack-instances/compare"
			sep := "?"
			if tt.queryLeft != "" {
				path += sep + "left=" + tt.queryLeft
				sep = "&"
			}
			if tt.queryRight != "" {
				path += sep + "right=" + tt.queryRight
			}

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, path, nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.checkFn != nil {
				tt.checkFn(t, w)
			}
		})
	}
}

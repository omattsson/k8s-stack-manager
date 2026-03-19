package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupChartConfigRouter creates a test gin engine with chart config sub-routes
// on the DefinitionHandler.
func setupChartConfigRouter(
	defRepo *MockStackDefinitionRepository,
	chartRepo *MockChartConfigRepository,
	templateChartRepo *MockTemplateChartConfigRepository,
	callerID, callerRole string,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(injectAuthContext(callerID, callerRole))

	h := NewDefinitionHandler(defRepo, chartRepo, NewMockStackInstanceRepository(), nil, templateChartRepo)

	defs := r.Group("/api/v1/stack-definitions")
	{
		defs.POST("/:id/charts", h.AddChartConfig)
		defs.PUT("/:id/charts/:chartId", h.UpdateChartConfig)
		defs.DELETE("/:id/charts/:chartId", h.DeleteChartConfig)
	}
	return r
}

// seedChartConfig inserts a ChartConfig into the mock repo and returns it.
func seedChartConfig(t *testing.T, repo *MockChartConfigRepository, id, defID, chartName string) *models.ChartConfig {
	t.Helper()
	cc := &models.ChartConfig{
		ID:                id,
		StackDefinitionID: defID,
		ChartName:         chartName,
		CreatedAt:         time.Now().UTC(),
	}
	require.NoError(t, repo.Create(cc))
	return cc
}

// ---- AddChartConfig ----

func TestAddChartConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		defID      string
		body       string
		setupDef   func(*MockStackDefinitionRepository)
		wantStatus int
		checkBody  func(t *testing.T, body []byte)
	}{
		{
			name:  "valid creation",
			defID: "def-1",
			body:  `{"chart_name":"nginx","repository_url":"https://charts.example.com"}`,
			setupDef: func(repo *MockStackDefinitionRepository) {
				repo.Create(&models.StackDefinition{
					ID: "def-1", Name: "My Def", OwnerID: "uid-1",
					DefaultBranch: "master",
					CreatedAt:     time.Now().UTC(), UpdatedAt: time.Now().UTC(),
				})
			},
			wantStatus: http.StatusCreated,
			checkBody: func(t *testing.T, body []byte) {
				var chart models.ChartConfig
				require.NoError(t, json.Unmarshal(body, &chart))
				assert.NotEmpty(t, chart.ID)
				assert.Equal(t, "def-1", chart.StackDefinitionID)
				assert.Equal(t, "nginx", chart.ChartName)
			},
		},
		{
			name:       "definition not found",
			defID:      "missing-def",
			body:       `{"chart_name":"nginx"}`,
			setupDef:   func(repo *MockStackDefinitionRepository) {},
			wantStatus: http.StatusNotFound,
		},
		{
			name:  "invalid JSON",
			defID: "def-1",
			body:  `{not valid json}`,
			setupDef: func(repo *MockStackDefinitionRepository) {
				repo.Create(&models.StackDefinition{
					ID: "def-1", Name: "My Def", OwnerID: "uid-1",
					DefaultBranch: "master",
					CreatedAt:     time.Now().UTC(), UpdatedAt: time.Now().UTC(),
				})
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:  "validation error - missing chart_name",
			defID: "def-1",
			body:  `{"repository_url":"https://charts.example.com"}`,
			setupDef: func(repo *MockStackDefinitionRepository) {
				repo.Create(&models.StackDefinition{
					ID: "def-1", Name: "My Def", OwnerID: "uid-1",
					DefaultBranch: "master",
					CreatedAt:     time.Now().UTC(), UpdatedAt: time.Now().UTC(),
				})
			},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			defRepo := NewMockStackDefinitionRepository()
			tt.setupDef(defRepo)
			chartRepo := NewMockChartConfigRepository()
			router := setupChartConfigRouter(defRepo, chartRepo, NewMockTemplateChartConfigRepository(), "uid-1", "admin")

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost,
				"/api/v1/stack-definitions/"+tt.defID+"/charts",
				bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.checkBody != nil {
				tt.checkBody(t, w.Body.Bytes())
			}
		})
	}
}

// ---- UpdateChartConfig ----

func TestUpdateChartConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		chartID    string
		body       string
		setupChart func(*MockChartConfigRepository)
		wantStatus int
		checkBody  func(t *testing.T, body []byte)
	}{
		{
			name:    "valid update",
			chartID: "chart-1",
			body:    `{"chart_name":"updated-nginx","repository_url":"https://charts.example.com"}`,
			setupChart: func(repo *MockChartConfigRepository) {
				repo.Create(&models.ChartConfig{
					ID: "chart-1", StackDefinitionID: "def-1",
					ChartName: "nginx", CreatedAt: time.Now().UTC(),
				})
			},
			wantStatus: http.StatusOK,
			checkBody: func(t *testing.T, body []byte) {
				var chart models.ChartConfig
				require.NoError(t, json.Unmarshal(body, &chart))
				assert.Equal(t, "updated-nginx", chart.ChartName)
			},
		},
		{
			name:       "chart not found",
			chartID:    "missing-chart",
			body:       `{"chart_name":"nginx"}`,
			setupChart: func(repo *MockChartConfigRepository) {},
			wantStatus: http.StatusNotFound,
		},
		{
			name:    "invalid JSON",
			chartID: "chart-1",
			body:    `{not valid json}`,
			setupChart: func(repo *MockChartConfigRepository) {
				repo.Create(&models.ChartConfig{
					ID: "chart-1", StackDefinitionID: "def-1",
					ChartName: "nginx", CreatedAt: time.Now().UTC(),
				})
			},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			defRepo := NewMockStackDefinitionRepository()
			chartRepo := NewMockChartConfigRepository()
			tt.setupChart(chartRepo)

			router := setupChartConfigRouter(defRepo, chartRepo, NewMockTemplateChartConfigRepository(), "uid-1", "admin")
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPut,
				"/api/v1/stack-definitions/def-1/charts/"+tt.chartID,
				bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.checkBody != nil {
				tt.checkBody(t, w.Body.Bytes())
			}
		})
	}
}

// ---- DeleteChartConfig ----

func TestDeleteChartConfig(t *testing.T) {
	t.Parallel()

	t.Run("successful delete", func(t *testing.T) {
		t.Parallel()
		defRepo := NewMockStackDefinitionRepository()
		seedDefinition(t, defRepo, "def-1", "My Def", "uid-1")
		chartRepo := NewMockChartConfigRepository()
		seedChartConfig(t, chartRepo, "chart-1", "def-1", "nginx")

		router := setupChartConfigRouter(defRepo, chartRepo, NewMockTemplateChartConfigRepository(), "uid-1", "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/stack-definitions/def-1/charts/chart-1", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNoContent, w.Code)
	})

	t.Run("chart not found", func(t *testing.T) {
		t.Parallel()
		defRepo := NewMockStackDefinitionRepository()
		chartRepo := NewMockChartConfigRepository()

		router := setupChartConfigRouter(defRepo, chartRepo, NewMockTemplateChartConfigRepository(), "uid-1", "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/stack-definitions/def-1/charts/missing-chart", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("required chart rejected with 409", func(t *testing.T) {
		t.Parallel()
		defRepo := NewMockStackDefinitionRepository()
		def := &models.StackDefinition{
			ID:               "def-1",
			Name:             "My Def",
			OwnerID:          "uid-1",
			DefaultBranch:    "master",
			SourceTemplateID: "tmpl-1",
			CreatedAt:        time.Now().UTC(),
			UpdatedAt:        time.Now().UTC(),
		}
		require.NoError(t, defRepo.Create(def))

		chartRepo := NewMockChartConfigRepository()
		seedChartConfig(t, chartRepo, "chart-1", "def-1", "nginx")

		tmplChartRepo := NewMockTemplateChartConfigRepository()
		require.NoError(t, tmplChartRepo.Create(&models.TemplateChartConfig{
			ID:              "tc-1",
			StackTemplateID: "tmpl-1",
			ChartName:       "nginx",
			Required:        true,
			CreatedAt:       time.Now().UTC(),
		}))

		router := setupChartConfigRouter(defRepo, chartRepo, tmplChartRepo, "uid-1", "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/stack-definitions/def-1/charts/chart-1", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusConflict, w.Code)
		var resp map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Contains(t, resp["error"], "nginx")
	})

	t.Run("non-required chart in template can be deleted", func(t *testing.T) {
		t.Parallel()
		defRepo := NewMockStackDefinitionRepository()
		def := &models.StackDefinition{
			ID:               "def-1",
			Name:             "My Def",
			OwnerID:          "uid-1",
			DefaultBranch:    "master",
			SourceTemplateID: "tmpl-1",
			CreatedAt:        time.Now().UTC(),
			UpdatedAt:        time.Now().UTC(),
		}
		require.NoError(t, defRepo.Create(def))

		chartRepo := NewMockChartConfigRepository()
		seedChartConfig(t, chartRepo, "chart-1", "def-1", "nginx")

		tmplChartRepo := NewMockTemplateChartConfigRepository()
		require.NoError(t, tmplChartRepo.Create(&models.TemplateChartConfig{
			ID:              "tc-1",
			StackTemplateID: "tmpl-1",
			ChartName:       "nginx",
			Required:        false, // not required
			CreatedAt:       time.Now().UTC(),
		}))

		router := setupChartConfigRouter(defRepo, chartRepo, tmplChartRepo, "uid-1", "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/stack-definitions/def-1/charts/chart-1", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNoContent, w.Code)
	})
}

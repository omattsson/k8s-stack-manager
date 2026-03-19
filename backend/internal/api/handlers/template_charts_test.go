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

// setupTemplateChartRouter creates a test gin engine with template chart sub-routes
// on the TemplateHandler.
func setupTemplateChartRouter(
	templateRepo *MockStackTemplateRepository,
	chartRepo *MockTemplateChartConfigRepository,
	callerID, callerRole string,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(injectAuthContext(callerID, callerRole))

	h := NewTemplateHandler(templateRepo, chartRepo, NewMockStackDefinitionRepository(), NewMockChartConfigRepository())

	tpl := r.Group("/api/v1/templates")
	{
		tpl.POST("/:id/charts", h.AddTemplateChart)
		tpl.PUT("/:id/charts/:chartId", h.UpdateTemplateChart)
		tpl.DELETE("/:id/charts/:chartId", h.DeleteTemplateChart)
	}
	return r
}

// seedTemplateChart inserts a TemplateChartConfig into the mock repo and returns it.
func seedTemplateChart(t *testing.T, repo *MockTemplateChartConfigRepository, id, templateID, chartName string) *models.TemplateChartConfig {
	t.Helper()
	tc := &models.TemplateChartConfig{
		ID:              id,
		StackTemplateID: templateID,
		ChartName:       chartName,
		CreatedAt:       time.Now().UTC(),
	}
	require.NoError(t, repo.Create(tc))
	return tc
}

// ---- AddTemplateChart ----

func TestAddTemplateChart(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		templateID    string
		body          string
		setupTemplate func(*MockStackTemplateRepository)
		wantStatus    int
		checkBody     func(t *testing.T, body []byte)
	}{
		{
			name:       "valid creation",
			templateID: "tmpl-1",
			body:       `{"chart_name":"nginx","repository_url":"https://charts.example.com"}`,
			setupTemplate: func(repo *MockStackTemplateRepository) {
				repo.Create(&models.StackTemplate{
					ID: "tmpl-1", Name: "My Template", Category: "Full Stack",
					Version: "1.0.0", OwnerID: "uid-1",
					CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
				})
			},
			wantStatus: http.StatusCreated,
			checkBody: func(t *testing.T, body []byte) {
				var chart models.TemplateChartConfig
				require.NoError(t, json.Unmarshal(body, &chart))
				assert.NotEmpty(t, chart.ID)
				assert.Equal(t, "tmpl-1", chart.StackTemplateID)
				assert.Equal(t, "nginx", chart.ChartName)
			},
		},
		{
			name:          "template not found",
			templateID:    "missing-tmpl",
			body:          `{"chart_name":"nginx"}`,
			setupTemplate: func(repo *MockStackTemplateRepository) {},
			wantStatus:    http.StatusNotFound,
		},
		{
			name:       "invalid JSON",
			templateID: "tmpl-1",
			body:       `{not valid json}`,
			setupTemplate: func(repo *MockStackTemplateRepository) {
				repo.Create(&models.StackTemplate{
					ID: "tmpl-1", Name: "My Template", Category: "Full Stack",
					Version: "1.0.0", OwnerID: "uid-1",
					CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
				})
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "validation error - missing chart_name",
			templateID: "tmpl-1",
			body:       `{"repository_url":"https://charts.example.com"}`,
			setupTemplate: func(repo *MockStackTemplateRepository) {
				repo.Create(&models.StackTemplate{
					ID: "tmpl-1", Name: "My Template", Category: "Full Stack",
					Version: "1.0.0", OwnerID: "uid-1",
					CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
				})
			},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			templateRepo := NewMockStackTemplateRepository()
			tt.setupTemplate(templateRepo)
			chartRepo := NewMockTemplateChartConfigRepository()

			router := setupTemplateChartRouter(templateRepo, chartRepo, "uid-1", "admin")
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost,
				"/api/v1/templates/"+tt.templateID+"/charts",
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

// ---- UpdateTemplateChart ----

func TestUpdateTemplateChart(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		chartID    string
		body       string
		setupChart func(*MockTemplateChartConfigRepository)
		wantStatus int
		checkBody  func(t *testing.T, body []byte)
	}{
		{
			name:    "valid update",
			chartID: "tc-1",
			body:    `{"chart_name":"updated-nginx","repository_url":"https://charts.example.com","required":true}`,
			setupChart: func(repo *MockTemplateChartConfigRepository) {
				repo.Create(&models.TemplateChartConfig{
					ID: "tc-1", StackTemplateID: "tmpl-1",
					ChartName: "nginx", CreatedAt: time.Now().UTC(),
				})
			},
			wantStatus: http.StatusOK,
			checkBody: func(t *testing.T, body []byte) {
				var chart models.TemplateChartConfig
				require.NoError(t, json.Unmarshal(body, &chart))
				assert.Equal(t, "updated-nginx", chart.ChartName)
				assert.True(t, chart.Required)
			},
		},
		{
			name:       "chart not found",
			chartID:    "missing-tc",
			body:       `{"chart_name":"nginx"}`,
			setupChart: func(repo *MockTemplateChartConfigRepository) {},
			wantStatus: http.StatusNotFound,
		},
		{
			name:    "invalid JSON",
			chartID: "tc-1",
			body:    `{not valid json}`,
			setupChart: func(repo *MockTemplateChartConfigRepository) {
				repo.Create(&models.TemplateChartConfig{
					ID: "tc-1", StackTemplateID: "tmpl-1",
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
			templateRepo := NewMockStackTemplateRepository()
			chartRepo := NewMockTemplateChartConfigRepository()
			tt.setupChart(chartRepo)

			router := setupTemplateChartRouter(templateRepo, chartRepo, "uid-1", "admin")
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPut,
				"/api/v1/templates/tmpl-1/charts/"+tt.chartID,
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

// ---- DeleteTemplateChart ----

func TestDeleteTemplateChart(t *testing.T) {
	t.Parallel()

	t.Run("successful delete", func(t *testing.T) {
		t.Parallel()
		templateRepo := NewMockStackTemplateRepository()
		chartRepo := NewMockTemplateChartConfigRepository()
		seedTemplateChart(t, chartRepo, "tc-1", "tmpl-1", "nginx")

		router := setupTemplateChartRouter(templateRepo, chartRepo, "uid-1", "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/templates/tmpl-1/charts/tc-1", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNoContent, w.Code)
	})

	t.Run("chart not found", func(t *testing.T) {
		t.Parallel()
		templateRepo := NewMockStackTemplateRepository()
		chartRepo := NewMockTemplateChartConfigRepository()

		router := setupTemplateChartRouter(templateRepo, chartRepo, "uid-1", "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/templates/tmpl-1/charts/missing-tc", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

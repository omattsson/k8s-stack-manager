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
		defs.GET("/:id/charts/:chartId", h.GetChartConfig)
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

// ---- GetChartConfig ----

func TestGetChartConfig(t *testing.T) {
	t.Parallel()

	t.Run("returns existing chart", func(t *testing.T) {
		defRepo := NewMockStackDefinitionRepository()
		chartRepo := NewMockChartConfigRepository()
		templateChartRepo := NewMockTemplateChartConfigRepository()
		seedChartConfig(t, chartRepo, "c-1", "def-1", "nginx")

		router := setupChartConfigRouter(defRepo, chartRepo, templateChartRepo, "uid-1", "admin")
		req := httptest.NewRequest(http.MethodGet, "/api/v1/stack-definitions/def-1/charts/c-1", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		var got models.ChartConfig
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
		assert.Equal(t, "c-1", got.ID)
		assert.Equal(t, "nginx", got.ChartName)
	})

	t.Run("returns 404 when chart absent", func(t *testing.T) {
		defRepo := NewMockStackDefinitionRepository()
		chartRepo := NewMockChartConfigRepository()
		templateChartRepo := NewMockTemplateChartConfigRepository()

		router := setupChartConfigRouter(defRepo, chartRepo, templateChartRepo, "uid-1", "admin")
		req := httptest.NewRequest(http.MethodGet, "/api/v1/stack-definitions/def-1/charts/does-not-exist", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("cross-definition returns 404", func(t *testing.T) {
		defRepo := NewMockStackDefinitionRepository()
		chartRepo := NewMockChartConfigRepository()
		templateChartRepo := NewMockTemplateChartConfigRepository()
		// Chart belongs to def-A; we look it up under def-B.
		seedChartConfig(t, chartRepo, "c-1", "def-A", "nginx")

		router := setupChartConfigRouter(defRepo, chartRepo, templateChartRepo, "uid-1", "admin")
		req := httptest.NewRequest(http.MethodGet, "/api/v1/stack-definitions/def-B/charts/c-1", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusNotFound, w.Code)
		var resp map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "Chart config not found", resp["error"])
	})
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
		{
			name:    "validation error — empty chart name",
			chartID: "chart-1",
			body:    `{"chart_name":"","repository_url":"https://charts.example.com"}`,
			setupChart: func(repo *MockChartConfigRepository) {
				repo.Create(&models.ChartConfig{
					ID: "chart-1", StackDefinitionID: "def-1",
					ChartName: "nginx", CreatedAt: time.Now().UTC(),
				})
			},
			wantStatus: http.StatusBadRequest,
			checkBody: func(t *testing.T, body []byte) {
				var resp map[string]string
				require.NoError(t, json.Unmarshal(body, &resp))
				assert.Contains(t, resp["error"], "chart_name is required")
			},
		},
		{
			name:    "repo update error returns 500",
			chartID: "chart-1",
			body:    `{"chart_name":"updated-nginx"}`,
			setupChart: func(repo *MockChartConfigRepository) {
				repo.Create(&models.ChartConfig{
					ID: "chart-1", StackDefinitionID: "def-1",
					ChartName: "nginx", CreatedAt: time.Now().UTC(),
				})
				repo.SetError(assert.AnError)
			},
			wantStatus: http.StatusInternalServerError,
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

	// preserves unspecified fields — regression for the PATCH-semantics fix.
	// A PUT body that mentions only chart_name MUST NOT wipe the other fields
	// to their zero values. The seed sets every field to a non-zero sentinel
	// so the assertions below would all fail under the previous buggy code
	// that bound the request straight into models.ChartConfig.
	t.Run("preserves unspecified fields", func(t *testing.T) {
		t.Parallel()

		defRepo := NewMockStackDefinitionRepository()
		chartRepo := NewMockChartConfigRepository()
		seeded := &models.ChartConfig{
			ID:                "chart-1",
			StackDefinitionID: "def-1",
			ChartName:         "nginx",
			RepositoryURL:     "https://charts.example.com",
			SourceRepoURL:     "https://git.example.com/nginx",
			BuildPipelineID:   "pipeline-42",
			ChartPath:         "charts/nginx",
			ChartVersion:      "1.2.3",
			DefaultValues:     "image:\n  tag: stable\n",
			DeployOrder:       7,
			CreatedAt:         time.Now().UTC().Truncate(time.Second),
		}
		require.NoError(t, chartRepo.Create(seeded))

		router := setupChartConfigRouter(defRepo, chartRepo, NewMockTemplateChartConfigRepository(), "uid-1", "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPut,
			"/api/v1/stack-definitions/def-1/charts/chart-1",
			bytes.NewBufferString(`{"chart_name":"renamed"}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)

		var got models.ChartConfig
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))

		// New value applied.
		assert.Equal(t, "renamed", got.ChartName)

		// Every other field must survive the partial update.
		assert.Equal(t, seeded.ID, got.ID)
		assert.Equal(t, seeded.StackDefinitionID, got.StackDefinitionID)
		assert.Equal(t, seeded.RepositoryURL, got.RepositoryURL)
		assert.Equal(t, seeded.SourceRepoURL, got.SourceRepoURL)
		assert.Equal(t, seeded.BuildPipelineID, got.BuildPipelineID)
		assert.Equal(t, seeded.ChartPath, got.ChartPath)
		assert.Equal(t, seeded.ChartVersion, got.ChartVersion)
		assert.Equal(t, seeded.DefaultValues, got.DefaultValues)
		assert.Equal(t, seeded.DeployOrder, got.DeployOrder)
		assert.True(t, seeded.CreatedAt.Equal(got.CreatedAt),
			"CreatedAt should be preserved: want %v, got %v", seeded.CreatedAt, got.CreatedAt)

		// And the persisted record matches what we returned.
		stored, err := chartRepo.FindByID("chart-1")
		require.NoError(t, err)
		assert.Equal(t, "renamed", stored.ChartName)
		assert.Equal(t, seeded.RepositoryURL, stored.RepositoryURL)
		assert.Equal(t, seeded.SourceRepoURL, stored.SourceRepoURL)
		assert.Equal(t, seeded.BuildPipelineID, stored.BuildPipelineID)
		assert.Equal(t, seeded.ChartPath, stored.ChartPath)
		assert.Equal(t, seeded.ChartVersion, stored.ChartVersion)
		assert.Equal(t, seeded.DefaultValues, stored.DefaultValues)
		assert.Equal(t, seeded.DeployOrder, stored.DeployOrder)
	})

	// cross-definition returns 404 — regression for the membership-check fix.
	// Chart belongs to def-A; PUT under def-B must 404 without mutating it,
	// and must not leak existence by returning a different error.
	t.Run("cross-definition returns 404", func(t *testing.T) {
		t.Parallel()

		defRepo := NewMockStackDefinitionRepository()
		chartRepo := NewMockChartConfigRepository()
		seedChartConfig(t, chartRepo, "c-1", "def-A", "nginx")

		router := setupChartConfigRouter(defRepo, chartRepo, NewMockTemplateChartConfigRepository(), "uid-1", "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPut,
			"/api/v1/stack-definitions/def-B/charts/c-1",
			bytes.NewBufferString(`{"chart_name":"renamed"}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusNotFound, w.Code)
		var resp map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "Chart config not found", resp["error"])

		// Ensure the chart was not mutated under the wrong definition.
		stored, err := chartRepo.FindByID("c-1")
		require.NoError(t, err)
		assert.Equal(t, "nginx", stored.ChartName)
		assert.Equal(t, "def-A", stored.StackDefinitionID)
	})
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

	// cross-definition returns 404 — regression for the membership-check fix.
	// Chart belongs to def-A; DELETE under def-B must 404 and the row must
	// remain in the store.
	t.Run("cross-definition returns 404", func(t *testing.T) {
		t.Parallel()

		defRepo := NewMockStackDefinitionRepository()
		chartRepo := NewMockChartConfigRepository()
		seedChartConfig(t, chartRepo, "c-1", "def-A", "nginx")

		router := setupChartConfigRouter(defRepo, chartRepo, NewMockTemplateChartConfigRepository(), "uid-1", "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/stack-definitions/def-B/charts/c-1", nil)
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusNotFound, w.Code)
		var resp map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "Chart config not found", resp["error"])

		// Chart must still be present under its real definition.
		stored, err := chartRepo.FindByID("c-1")
		require.NoError(t, err)
		assert.Equal(t, "def-A", stored.StackDefinitionID)
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

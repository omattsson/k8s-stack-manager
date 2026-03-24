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

// setupDefinitionRouter creates a test gin engine with DefinitionHandler routes.
func setupDefinitionRouter(
	defRepo *MockStackDefinitionRepository,
	chartRepo *MockChartConfigRepository,
	instanceRepo *MockStackInstanceRepository,
	callerID, callerRole string,
) *gin.Engine {
	return setupDefinitionRouterWithTemplates(defRepo, chartRepo, instanceRepo, nil, nil, callerID, callerRole)
}

// setupDefinitionRouterWithTemplates creates a test gin engine with DefinitionHandler routes
// and optional template repositories for testing required-chart enforcement.
func setupDefinitionRouterWithTemplates(
	defRepo *MockStackDefinitionRepository,
	chartRepo *MockChartConfigRepository,
	instanceRepo *MockStackInstanceRepository,
	templateRepo *MockStackTemplateRepository,
	templateChartRepo *MockTemplateChartConfigRepository,
	callerID, callerRole string,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(injectAuthContext(callerID, callerRole))

	h := NewDefinitionHandler(defRepo, chartRepo, instanceRepo, templateRepo, templateChartRepo)

	defs := r.Group("/api/v1/stack-definitions")
	{
		defs.GET("", h.ListDefinitions)
		defs.POST("", h.CreateDefinition)
		defs.POST("/import", h.ImportDefinition)
		defs.GET("/:id", h.GetDefinition)
		defs.GET("/:id/export", h.ExportDefinition)
		defs.PUT("/:id", h.UpdateDefinition)
		defs.DELETE("/:id", h.DeleteDefinition)
		defs.DELETE("/:id/charts/:chartId", h.DeleteChartConfig)
	}
	return r
}

// seedDefinition inserts a StackDefinition into the mock repo.
func seedDefinition(t *testing.T, repo *MockStackDefinitionRepository, id, name, ownerID string) *models.StackDefinition {
	t.Helper()
	def := &models.StackDefinition{
		ID:            id,
		Name:          name,
		Description:   "test definition",
		OwnerID:       ownerID,
		DefaultBranch: "master",
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	require.NoError(t, repo.Create(def))
	return def
}

// ---- ListDefinitions ----

func TestListDefinitions(t *testing.T) {
	t.Parallel()

	t.Run("returns all definitions", func(t *testing.T) {
		t.Parallel()
		defRepo := NewMockStackDefinitionRepository()
		seedDefinition(t, defRepo, "d1", "Def One", "owner-1")
		seedDefinition(t, defRepo, "d2", "Def Two", "owner-2")

		router := setupDefinitionRouter(defRepo, NewMockChartConfigRepository(), NewMockStackInstanceRepository(), "uid-1", "user")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-definitions", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var list []models.StackDefinition
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
		assert.Len(t, list, 2)
	})

	t.Run("repository error returns 500", func(t *testing.T) {
		t.Parallel()
		defRepo := NewMockStackDefinitionRepository()
		defRepo.SetError(errInternal)

		router := setupDefinitionRouter(defRepo, NewMockChartConfigRepository(), NewMockStackInstanceRepository(), "uid-1", "user")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-definitions", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})
}

// ---- CreateDefinition ----

func TestCreateDefinition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		callerID   string
		setup      func(*MockStackDefinitionRepository)
		wantStatus int
	}{
		{
			name:       "valid definition",
			body:       `{"name":"My Stack","description":"desc"}`,
			callerID:   "uid-1",
			setup:      func(_ *MockStackDefinitionRepository) {},
			wantStatus: http.StatusCreated,
		},
		{
			name:       "missing name returns 400",
			body:       `{"description":"desc"}`,
			callerID:   "uid-1",
			setup:      func(_ *MockStackDefinitionRepository) {},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid JSON returns 400",
			body:       `{bad`,
			callerID:   "uid-1",
			setup:      func(_ *MockStackDefinitionRepository) {},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:     "repository error returns 500",
			body:     `{"name":"My Stack"}`,
			callerID: "uid-1",
			setup: func(repo *MockStackDefinitionRepository) {
				repo.SetError(errInternal)
			},
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			defRepo := NewMockStackDefinitionRepository()
			tt.setup(defRepo)
			router := setupDefinitionRouter(defRepo, NewMockChartConfigRepository(), NewMockStackInstanceRepository(), tt.callerID, "user")

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-definitions", bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.wantStatus == http.StatusCreated {
				var resp models.StackDefinition
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.NotEmpty(t, resp.ID)
				assert.Equal(t, tt.callerID, resp.OwnerID)
				assert.Equal(t, "master", resp.DefaultBranch)
			}
		})
	}
}

// ---- GetDefinition ----

func TestGetDefinition(t *testing.T) {
	t.Parallel()

	t.Run("returns definition with charts", func(t *testing.T) {
		t.Parallel()
		defRepo := NewMockStackDefinitionRepository()
		ccRepo := NewMockChartConfigRepository()
		seedDefinition(t, defRepo, "d1", "My Stack", "uid-1")
		require.NoError(t, ccRepo.Create(&models.ChartConfig{
			ID:                "c1",
			StackDefinitionID: "d1",
			ChartName:         "backend",
		}))

		router := setupDefinitionRouter(defRepo, ccRepo, NewMockStackInstanceRepository(), "uid-1", "user")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-definitions/d1", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.NotNil(t, resp["definition"])
		assert.NotNil(t, resp["charts"])
	})

	t.Run("not found returns 404", func(t *testing.T) {
		t.Parallel()
		defRepo := NewMockStackDefinitionRepository()
		router := setupDefinitionRouter(defRepo, NewMockChartConfigRepository(), NewMockStackInstanceRepository(), "uid-1", "user")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-definitions/missing", nil)
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

// ---- UpdateDefinition ----

func TestUpdateDefinition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		id         string
		body       string
		setup      func(*MockStackDefinitionRepository)
		wantStatus int
	}{
		{
			name: "valid update",
			id:   "d1",
			body: `{"name":"Updated Stack","default_branch":"develop"}`,
			setup: func(repo *MockStackDefinitionRepository) {
				seedDefinition(t, repo, "d1", "Old Name", "uid-1")
			},
			wantStatus: http.StatusOK,
		},
		{
			name:       "not found returns 404",
			id:         "missing",
			body:       `{"name":"X"}`,
			setup:      func(_ *MockStackDefinitionRepository) {},
			wantStatus: http.StatusNotFound,
		},
		{
			name: "empty name after update returns 400",
			id:   "d1",
			body: `{"name":""}`,
			setup: func(repo *MockStackDefinitionRepository) {
				seedDefinition(t, repo, "d1", "Old Name", "uid-1")
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "invalid JSON returns 400",
			id:   "d1",
			body: `{invalid`,
			setup: func(repo *MockStackDefinitionRepository) {
				seedDefinition(t, repo, "d1", "Old Name", "uid-1")
			},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			defRepo := NewMockStackDefinitionRepository()
			tt.setup(defRepo)
			router := setupDefinitionRouter(defRepo, NewMockChartConfigRepository(), NewMockStackInstanceRepository(), "uid-1", "user")

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPut, "/api/v1/stack-definitions/"+tt.id, bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
		})
	}
}

// ---- DeleteDefinition ----

func TestDeleteDefinition(t *testing.T) {
	t.Parallel()

	t.Run("deletes definition with no running instances", func(t *testing.T) {
		t.Parallel()
		defRepo := NewMockStackDefinitionRepository()
		instanceRepo := NewMockStackInstanceRepository()
		seedDefinition(t, defRepo, "d1", "My Stack", "uid-1")
		// Stopped instance — should not block deletion.
		require.NoError(t, instanceRepo.Create(&models.StackInstance{
			ID:                "i1",
			StackDefinitionID: "d1",
			Name:              "stopped-instance",
			OwnerID:           "uid-1",
			Status:            models.StackStatusStopped,
		}))

		router := setupDefinitionRouter(defRepo, NewMockChartConfigRepository(), instanceRepo, "uid-1", "user")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/stack-definitions/d1", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNoContent, w.Code)
	})

	t.Run("blocked when running instances exist", func(t *testing.T) {
		t.Parallel()
		defRepo := NewMockStackDefinitionRepository()
		instanceRepo := NewMockStackInstanceRepository()
		seedDefinition(t, defRepo, "d1", "My Stack", "uid-1")
		require.NoError(t, instanceRepo.Create(&models.StackInstance{
			ID:                "i1",
			StackDefinitionID: "d1",
			Name:              "running-instance",
			OwnerID:           "uid-1",
			Status:            models.StackStatusRunning,
		}))

		router := setupDefinitionRouter(defRepo, NewMockChartConfigRepository(), instanceRepo, "uid-1", "user")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/stack-definitions/d1", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusConflict, w.Code)
	})

	t.Run("blocked when deploying instances exist", func(t *testing.T) {
		t.Parallel()
		defRepo := NewMockStackDefinitionRepository()
		instanceRepo := NewMockStackInstanceRepository()
		seedDefinition(t, defRepo, "d1", "My Stack", "uid-1")
		require.NoError(t, instanceRepo.Create(&models.StackInstance{
			ID:                "i1",
			StackDefinitionID: "d1",
			Name:              "deploying-instance",
			OwnerID:           "uid-1",
			Status:            models.StackStatusDeploying,
		}))

		router := setupDefinitionRouter(defRepo, NewMockChartConfigRepository(), instanceRepo, "uid-1", "user")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/stack-definitions/d1", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusConflict, w.Code)
	})

	t.Run("not found returns 404", func(t *testing.T) {
		t.Parallel()
		defRepo := NewMockStackDefinitionRepository()
		router := setupDefinitionRouter(defRepo, NewMockChartConfigRepository(), NewMockStackInstanceRepository(), "uid-1", "user")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/stack-definitions/missing", nil)
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

// ---- ExportDefinition ----

func TestExportDefinition(t *testing.T) {
	t.Parallel()

	t.Run("exports definition with charts", func(t *testing.T) {
		t.Parallel()
		defRepo := NewMockStackDefinitionRepository()
		ccRepo := NewMockChartConfigRepository()
		seedDefinition(t, defRepo, "d1", "My Stack", "uid-1")
		require.NoError(t, ccRepo.Create(&models.ChartConfig{
			ID:                "c1",
			StackDefinitionID: "d1",
			ChartName:         "backend",
			RepositoryURL:     "https://charts.example.com",
			SourceRepoURL:     "https://git.example.com/repo",
			ChartPath:         "charts/backend",
			ChartVersion:      "1.0.0",
			DefaultValues:     "replicas: 1",
			DeployOrder:       1,
			CreatedAt:         time.Now().UTC(),
		}))
		require.NoError(t, ccRepo.Create(&models.ChartConfig{
			ID:                "c2",
			StackDefinitionID: "d1",
			ChartName:         "frontend",
			DeployOrder:       2,
			CreatedAt:         time.Now().UTC(),
		}))

		router := setupDefinitionRouter(defRepo, ccRepo, NewMockStackInstanceRepository(), "uid-1", "devops")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-definitions/d1/export", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var bundle DefinitionExportBundle
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &bundle))
		assert.Equal(t, "1.0", bundle.SchemaVersion)
		assert.False(t, bundle.ExportedAt.IsZero())
		assert.Equal(t, "My Stack", bundle.Definition.Name)
		assert.Equal(t, "test definition", bundle.Definition.Description)
		assert.Equal(t, "master", bundle.Definition.DefaultBranch)
		assert.Len(t, bundle.Charts, 2)

		// Verify chart fields are exported correctly.
		var backendChart *ChartConfigExportData
		for i := range bundle.Charts {
			if bundle.Charts[i].ChartName == "backend" {
				backendChart = &bundle.Charts[i]
				break
			}
		}
		require.NotNil(t, backendChart)
		assert.Equal(t, "https://charts.example.com", backendChart.RepositoryURL)
		assert.Equal(t, "https://git.example.com/repo", backendChart.SourceRepoURL)
		assert.Equal(t, "charts/backend", backendChart.ChartPath)
		assert.Equal(t, "1.0.0", backendChart.ChartVersion)
		assert.Equal(t, "replicas: 1", backendChart.DefaultValues)
		assert.Equal(t, 1, backendChart.DeployOrder)
	})

	t.Run("exports definition with no charts", func(t *testing.T) {
		t.Parallel()
		defRepo := NewMockStackDefinitionRepository()
		ccRepo := NewMockChartConfigRepository()
		seedDefinition(t, defRepo, "d1", "Empty Stack", "uid-1")

		router := setupDefinitionRouter(defRepo, ccRepo, NewMockStackInstanceRepository(), "uid-1", "devops")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-definitions/d1/export", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var bundle DefinitionExportBundle
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &bundle))
		assert.Empty(t, bundle.Charts)
	})

	t.Run("not found returns 404", func(t *testing.T) {
		t.Parallel()
		defRepo := NewMockStackDefinitionRepository()
		router := setupDefinitionRouter(defRepo, NewMockChartConfigRepository(), NewMockStackInstanceRepository(), "uid-1", "devops")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-definitions/missing/export", nil)
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("chart list error returns 500", func(t *testing.T) {
		t.Parallel()
		defRepo := NewMockStackDefinitionRepository()
		ccRepo := NewMockChartConfigRepository()
		seedDefinition(t, defRepo, "d1", "My Stack", "uid-1")
		ccRepo.SetError(errInternal)

		router := setupDefinitionRouter(defRepo, ccRepo, NewMockStackInstanceRepository(), "uid-1", "devops")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-definitions/d1/export", nil)
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})
}

// ---- ImportDefinition ----

func TestImportDefinition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		body           string
		callerID       string
		setupDef       func(*MockStackDefinitionRepository)
		setupChart     func(*MockChartConfigRepository)
		wantStatus     int
		wantErrContain string
	}{
		{
			name: "valid import with charts",
			body: `{
				"schema_version": "1.0",
				"exported_at": "2024-01-01T00:00:00Z",
				"definition": {
					"name": "Imported Stack",
					"description": "imported desc",
					"default_branch": "develop"
				},
				"charts": [
					{
						"chart_name": "backend",
						"repository_url": "https://charts.example.com",
						"source_repo_url": "https://git.example.com/repo",
						"chart_path": "charts/backend",
						"chart_version": "1.0.0",
						"default_values": "replicas: 2",
						"deploy_order": 1
					},
					{
						"chart_name": "frontend",
						"deploy_order": 2
					}
				]
			}`,
			callerID:   "uid-1",
			setupDef:   func(_ *MockStackDefinitionRepository) {},
			setupChart: func(_ *MockChartConfigRepository) {},
			wantStatus: http.StatusCreated,
		},
		{
			name: "valid import with no charts",
			body: `{
				"schema_version": "1.0",
				"definition": {"name": "No Charts Stack"},
				"charts": []
			}`,
			callerID:   "uid-1",
			setupDef:   func(_ *MockStackDefinitionRepository) {},
			setupChart: func(_ *MockChartConfigRepository) {},
			wantStatus: http.StatusCreated,
		},
		{
			name: "valid import defaults branch to master",
			body: `{
				"schema_version": "1.0",
				"definition": {"name": "Default Branch"},
				"charts": []
			}`,
			callerID:   "uid-1",
			setupDef:   func(_ *MockStackDefinitionRepository) {},
			setupChart: func(_ *MockChartConfigRepository) {},
			wantStatus: http.StatusCreated,
		},
		{
			name:           "missing schema_version returns 400",
			body:           `{"definition": {"name": "X"}, "charts": []}`,
			callerID:       "uid-1",
			setupDef:       func(_ *MockStackDefinitionRepository) {},
			setupChart:     func(_ *MockChartConfigRepository) {},
			wantStatus:     http.StatusBadRequest,
			wantErrContain: "schema_version is required",
		},
		{
			name:           "unsupported schema_version returns 400",
			body:           `{"schema_version": "99.0", "definition": {"name": "X"}, "charts": []}`,
			callerID:       "uid-1",
			setupDef:       func(_ *MockStackDefinitionRepository) {},
			setupChart:     func(_ *MockChartConfigRepository) {},
			wantStatus:     http.StatusBadRequest,
			wantErrContain: "unsupported schema_version",
		},
		{
			name:           "missing definition name returns 400",
			body:           `{"schema_version": "1.0", "definition": {"description": "no name"}, "charts": []}`,
			callerID:       "uid-1",
			setupDef:       func(_ *MockStackDefinitionRepository) {},
			setupChart:     func(_ *MockChartConfigRepository) {},
			wantStatus:     http.StatusBadRequest,
			wantErrContain: "definition name is required",
		},
		{
			name: "chart with empty name returns 400",
			body: `{
				"schema_version": "1.0",
				"definition": {"name": "My Stack"},
				"charts": [{"chart_name": "", "deploy_order": 1}]
			}`,
			callerID:       "uid-1",
			setupDef:       func(_ *MockStackDefinitionRepository) {},
			setupChart:     func(_ *MockChartConfigRepository) {},
			wantStatus:     http.StatusBadRequest,
			wantErrContain: "chart_name is required",
		},
		{
			name:       "invalid JSON returns 400",
			body:       `{bad json`,
			callerID:   "uid-1",
			setupDef:   func(_ *MockStackDefinitionRepository) {},
			setupChart: func(_ *MockChartConfigRepository) {},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "definition repo error returns 500",
			body: `{
				"schema_version": "1.0",
				"definition": {"name": "My Stack"},
				"charts": []
			}`,
			callerID: "uid-1",
			setupDef: func(repo *MockStackDefinitionRepository) {
				repo.SetError(errInternal)
			},
			setupChart: func(_ *MockChartConfigRepository) {},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "chart repo error returns 500",
			body: `{
				"schema_version": "1.0",
				"definition": {"name": "My Stack"},
				"charts": [{"chart_name": "backend"}]
			}`,
			callerID: "uid-1",
			setupDef: func(_ *MockStackDefinitionRepository) {},
			setupChart: func(repo *MockChartConfigRepository) {
				repo.SetError(errInternal)
			},
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			defRepo := NewMockStackDefinitionRepository()
			ccRepo := NewMockChartConfigRepository()
			tt.setupDef(defRepo)
			tt.setupChart(ccRepo)
			router := setupDefinitionRouter(defRepo, ccRepo, NewMockStackInstanceRepository(), tt.callerID, "devops")

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-definitions/import", bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)

			if tt.wantErrContain != "" {
				var resp map[string]string
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Contains(t, resp["error"], tt.wantErrContain)
			}

			if tt.wantStatus == http.StatusCreated {
				var resp map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

				var def models.StackDefinition
				require.NoError(t, json.Unmarshal(resp["definition"], &def))
				assert.NotEmpty(t, def.ID)
				assert.Equal(t, tt.callerID, def.OwnerID)
				assert.NotEmpty(t, def.CreatedAt)
				assert.NotEmpty(t, def.UpdatedAt)
			}
		})
	}
}

// ---- Export/Import round-trip ----

func TestExportImportRoundTrip(t *testing.T) {
	t.Parallel()

	t.Run("exported bundle can be imported", func(t *testing.T) {
		t.Parallel()
		defRepo := NewMockStackDefinitionRepository()
		ccRepo := NewMockChartConfigRepository()
		seedDefinition(t, defRepo, "d1", "Round Trip Stack", "uid-1")
		require.NoError(t, ccRepo.Create(&models.ChartConfig{
			ID:                "c1",
			StackDefinitionID: "d1",
			ChartName:         "api",
			RepositoryURL:     "https://charts.example.com",
			DefaultValues:     "port: 8080",
			DeployOrder:       1,
			CreatedAt:         time.Now().UTC(),
		}))

		router := setupDefinitionRouter(defRepo, ccRepo, NewMockStackInstanceRepository(), "uid-2", "devops")

		// Export
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-definitions/d1/export", nil)
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		exportBody := w.Body.Bytes()

		// Import using the exported bundle
		w2 := httptest.NewRecorder()
		req2, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-definitions/import", bytes.NewBuffer(exportBody))
		req2.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w2, req2)

		require.Equal(t, http.StatusCreated, w2.Code)

		var resp map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &resp))

		var importedDef models.StackDefinition
		require.NoError(t, json.Unmarshal(resp["definition"], &importedDef))

		// Fresh ID, different from original.
		assert.NotEqual(t, "d1", importedDef.ID)
		assert.Equal(t, "Round Trip Stack", importedDef.Name)
		// Owner is the importing user.
		assert.Equal(t, "uid-2", importedDef.OwnerID)

		var importedCharts []models.ChartConfig
		require.NoError(t, json.Unmarshal(resp["charts"], &importedCharts))
		assert.Len(t, importedCharts, 1)
		assert.NotEqual(t, "c1", importedCharts[0].ID)
		assert.Equal(t, importedDef.ID, importedCharts[0].StackDefinitionID)
		assert.Equal(t, "api", importedCharts[0].ChartName)
		assert.Equal(t, "https://charts.example.com", importedCharts[0].RepositoryURL)
		assert.Equal(t, "port: 8080", importedCharts[0].DefaultValues)
	})
}

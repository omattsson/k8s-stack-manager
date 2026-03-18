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
		defs.GET("/:id", h.GetDefinition)
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

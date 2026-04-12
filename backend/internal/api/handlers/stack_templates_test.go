package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/database"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTemplateRouter creates a test gin engine with TemplateHandler routes.
func setupTemplateRouter(
	templateRepo *MockStackTemplateRepository,
	chartRepo *MockTemplateChartConfigRepository,
	definitionRepo *MockStackDefinitionRepository,
	chartConfigRepo *MockChartConfigRepository,
	callerID, callerRole string,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(injectAuthContext(callerID, callerRole))

	h := NewTemplateHandler(templateRepo, chartRepo, definitionRepo, chartConfigRepo)
	h.SetTxRunner(&mockHandlerTxRunner{repos: database.TxRepos{
		StackTemplate:   templateRepo,
		TemplateChart:   chartRepo,
		StackDefinition: definitionRepo,
		ChartConfig:     chartConfigRepo,
	}})

	tpl := r.Group("/api/v1/templates")
	{
		tpl.GET("", h.ListTemplates)
		tpl.POST("", h.CreateTemplate)
		tpl.GET("/:id", h.GetTemplate)
		tpl.PUT("/:id", h.UpdateTemplate)
		tpl.DELETE("/:id", h.DeleteTemplate)
		tpl.POST("/:id/publish", h.PublishTemplate)
		tpl.POST("/:id/unpublish", h.UnpublishTemplate)
		tpl.POST("/:id/instantiate", h.InstantiateTemplate)
		tpl.POST("/:id/clone", h.CloneTemplate)
	}
	return r
}

// seedTemplate inserts a StackTemplate into the mock repo and returns it.
func seedTemplate(t *testing.T, repo *MockStackTemplateRepository, id, name, ownerID string, published bool) *models.StackTemplate {
	t.Helper()
	tmpl := &models.StackTemplate{
		ID:          id,
		Name:        name,
		Description: "test template",
		Category:    "Full Stack",
		Version:     "1.0.0",
		OwnerID:     ownerID,
		IsPublished: published,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	require.NoError(t, repo.Create(tmpl))
	return tmpl
}

// ---- ListTemplates ----

// templateListResponse is the paginated envelope returned by ListTemplates.
type templateListResponse struct {
	Data     []TemplateListItem `json:"data"`
	Total    int64              `json:"total"`
	Page     int                `json:"page"`
	PageSize int                `json:"pageSize"`
}

func TestListTemplates(t *testing.T) {
	t.Parallel()

	t.Run("user sees only published templates", func(t *testing.T) {
		t.Parallel()
		repo := NewMockStackTemplateRepository()
		seedTemplate(t, repo, "t1", "Published One", "owner-1", true)
		seedTemplate(t, repo, "t2", "Draft Two", "owner-1", false)

		router := setupTemplateRouter(repo, NewMockTemplateChartConfigRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), "uid-1", "user")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/templates", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp templateListResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Len(t, resp.Data, 1)
		assert.Equal(t, "t1", resp.Data[0].ID)
		assert.Equal(t, int64(1), resp.Total)
		assert.Equal(t, 1, resp.Page)
		assert.Equal(t, 25, resp.PageSize)
	})

	t.Run("admin sees all templates", func(t *testing.T) {
		t.Parallel()
		repo := NewMockStackTemplateRepository()
		seedTemplate(t, repo, "t1", "Published One", "owner-1", true)
		seedTemplate(t, repo, "t2", "Draft Two", "owner-1", false)

		router := setupTemplateRouter(repo, NewMockTemplateChartConfigRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), "uid-1", "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/templates", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp templateListResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Len(t, resp.Data, 2)
		assert.Equal(t, int64(2), resp.Total)
	})

	t.Run("devops sees all templates", func(t *testing.T) {
		t.Parallel()
		repo := NewMockStackTemplateRepository()
		seedTemplate(t, repo, "t1", "Published", "owner-1", true)
		seedTemplate(t, repo, "t2", "Draft", "owner-1", false)

		router := setupTemplateRouter(repo, NewMockTemplateChartConfigRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), "uid-1", "devops")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/templates", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp templateListResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Len(t, resp.Data, 2)
		assert.Equal(t, int64(2), resp.Total)
	})

	t.Run("custom page and pageSize", func(t *testing.T) {
		t.Parallel()
		repo := NewMockStackTemplateRepository()
		for i := 0; i < 5; i++ {
			seedTemplate(t, repo, fmt.Sprintf("t%d", i), fmt.Sprintf("Tmpl %d", i), "owner-1", true)
		}

		router := setupTemplateRouter(repo, NewMockTemplateChartConfigRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), "uid-1", "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/templates?page=2&pageSize=2", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp templateListResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Len(t, resp.Data, 2)
		assert.Equal(t, int64(5), resp.Total)
		assert.Equal(t, 2, resp.Page)
		assert.Equal(t, 2, resp.PageSize)
	})

	t.Run("pageSize capped at 100", func(t *testing.T) {
		t.Parallel()
		repo := NewMockStackTemplateRepository()

		router := setupTemplateRouter(repo, NewMockTemplateChartConfigRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), "uid-1", "admin")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/templates?pageSize=999", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp templateListResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, 100, resp.PageSize)
	})

	t.Run("repository error returns 500", func(t *testing.T) {
		t.Parallel()
		repo := NewMockStackTemplateRepository()
		repo.SetError(errInternal)

		router := setupTemplateRouter(repo, NewMockTemplateChartConfigRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), "uid-1", "user")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/templates", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})
}

// ---- CreateTemplate ----

func TestCreateTemplate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		callerID   string
		setup      func(*MockStackTemplateRepository)
		wantStatus int
	}{
		{
			name:       "valid template",
			body:       `{"name":"My Stack","category":"Full Stack","version":"1.0.0"}`,
			callerID:   "owner-1",
			setup:      func(_ *MockStackTemplateRepository) {},
			wantStatus: http.StatusCreated,
		},
		{
			name:       "missing name returns 400",
			body:       `{"category":"Full Stack","version":"1.0.0"}`,
			callerID:   "owner-1",
			setup:      func(_ *MockStackTemplateRepository) {},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid JSON returns 400",
			body:       `{invalid`,
			callerID:   "owner-1",
			setup:      func(_ *MockStackTemplateRepository) {},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:     "repository error returns 500",
			body:     `{"name":"My Stack","category":"Full Stack","version":"1.0.0"}`,
			callerID: "owner-1",
			setup: func(repo *MockStackTemplateRepository) {
				repo.SetError(errInternal)
			},
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := NewMockStackTemplateRepository()
			tt.setup(repo)
			router := setupTemplateRouter(repo, NewMockTemplateChartConfigRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), tt.callerID, "devops")

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/templates", bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.wantStatus == http.StatusCreated {
				var resp models.StackTemplate
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.NotEmpty(t, resp.ID)
				assert.Equal(t, tt.callerID, resp.OwnerID)
			}
		})
	}
}

// ---- GetTemplate ----

func TestGetTemplate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		id         string
		setup      func(*MockStackTemplateRepository)
		wantStatus int
	}{
		{
			name: "found template",
			id:   "t1",
			setup: func(repo *MockStackTemplateRepository) {
				seedTemplate(t, repo, "t1", "My Template", "owner-1", true)
			},
			wantStatus: http.StatusOK,
		},
		{
			name:       "not found returns 404",
			id:         "missing-id",
			setup:      func(_ *MockStackTemplateRepository) {},
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := NewMockStackTemplateRepository()
			tt.setup(repo)
			router := setupTemplateRouter(repo, NewMockTemplateChartConfigRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), "uid-1", "user")

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/api/v1/templates/"+tt.id, nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
		})
	}
}

// ---- UpdateTemplate ----

func TestUpdateTemplate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		id         string
		body       string
		setup      func(*MockStackTemplateRepository)
		wantStatus int
	}{
		{
			name: "valid update",
			id:   "t1",
			body: `{"name":"Updated Name","version":"2.0.0","category":"Backend Only"}`,
			setup: func(repo *MockStackTemplateRepository) {
				seedTemplate(t, repo, "t1", "Old Name", "owner-1", false)
			},
			wantStatus: http.StatusOK,
		},
		{
			name:       "not found returns 404",
			id:         "missing",
			body:       `{"name":"X","version":"1.0.0","category":"Custom"}`,
			setup:      func(_ *MockStackTemplateRepository) {},
			wantStatus: http.StatusNotFound,
		},
		{
			name: "invalid JSON returns 400",
			id:   "t1",
			body: `{invalid`,
			setup: func(repo *MockStackTemplateRepository) {
				seedTemplate(t, repo, "t1", "My Template", "owner-1", false)
			},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := NewMockStackTemplateRepository()
			tt.setup(repo)
			router := setupTemplateRouter(repo, NewMockTemplateChartConfigRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), "owner-1", "devops")

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPut, "/api/v1/templates/"+tt.id, bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
		})
	}
}

// ---- DeleteTemplate ----

func TestDeleteTemplate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		id         string
		setup      func(*MockStackTemplateRepository)
		wantStatus int
	}{
		{
			name: "deletes existing template",
			id:   "t1",
			setup: func(repo *MockStackTemplateRepository) {
				seedTemplate(t, repo, "t1", "My Template", "owner-1", false)
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "not found returns 404",
			id:         "missing",
			setup:      func(_ *MockStackTemplateRepository) {},
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := NewMockStackTemplateRepository()
			tt.setup(repo)
			router := setupTemplateRouter(repo, NewMockTemplateChartConfigRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), "owner-1", "devops")

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodDelete, "/api/v1/templates/"+tt.id, nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
		})
	}
}

// ---- PublishTemplate ----

func TestPublishTemplate(t *testing.T) {
	t.Parallel()

	t.Run("publishes existing template", func(t *testing.T) {
		t.Parallel()
		repo := NewMockStackTemplateRepository()
		seedTemplate(t, repo, "t1", "My Template", "owner-1", false)

		router := setupTemplateRouter(repo, NewMockTemplateChartConfigRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), "owner-1", "devops")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/templates/t1/publish", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp models.StackTemplate
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.True(t, resp.IsPublished)
	})

	t.Run("not found returns 404", func(t *testing.T) {
		t.Parallel()
		repo := NewMockStackTemplateRepository()
		router := setupTemplateRouter(repo, NewMockTemplateChartConfigRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), "owner-1", "devops")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/templates/missing/publish", nil)
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

// ---- UnpublishTemplate ----

func TestUnpublishTemplate(t *testing.T) {
	t.Parallel()

	t.Run("unpublishes existing template", func(t *testing.T) {
		t.Parallel()
		repo := NewMockStackTemplateRepository()
		seedTemplate(t, repo, "t1", "My Template", "owner-1", true)

		router := setupTemplateRouter(repo, NewMockTemplateChartConfigRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), "owner-1", "devops")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/templates/t1/unpublish", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp models.StackTemplate
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.False(t, resp.IsPublished)
	})

	t.Run("not found returns 404", func(t *testing.T) {
		t.Parallel()
		repo := NewMockStackTemplateRepository()
		router := setupTemplateRouter(repo, NewMockTemplateChartConfigRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), "owner-1", "devops")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/templates/missing/unpublish", nil)
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

// ---- InstantiateTemplate ----

func TestInstantiateTemplate(t *testing.T) {
	t.Parallel()

	t.Run("creates definition from template", func(t *testing.T) {
		t.Parallel()
		tmplRepo := NewMockStackTemplateRepository()
		chartRepo := NewMockTemplateChartConfigRepository()
		defRepo := NewMockStackDefinitionRepository()
		ccRepo := NewMockChartConfigRepository()

		seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)
		// Add a chart to the template so it gets copied.
		require.NoError(t, chartRepo.Create(&models.TemplateChartConfig{
			ID:              "c1",
			StackTemplateID: "t1",
			ChartName:       "my-service",
			RepositoryURL:   "https://charts.example.com",
		}))

		router := setupTemplateRouter(tmplRepo, chartRepo, defRepo, ccRepo, "uid-1", "user")
		body := `{"name":"my-def","description":"my stack"}`
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/templates/t1/instantiate", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusCreated, w.Code)
		var resp map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		var def models.StackDefinition
		require.NoError(t, json.Unmarshal(resp["definition"], &def))
		assert.Equal(t, "my-def", def.Name)
		assert.Equal(t, "t1", def.SourceTemplateID)
		assert.Equal(t, "uid-1", def.OwnerID)

		// Verify that created chart configs are returned (non-empty).
		var charts []models.ChartConfig
		require.NoError(t, json.Unmarshal(resp["charts"], &charts))
		assert.Len(t, charts, 1, "response should contain the copied chart config")
		assert.Equal(t, "my-service", charts[0].ChartName)
		assert.Equal(t, def.ID, charts[0].StackDefinitionID)
	})

	t.Run("template not found returns 404", func(t *testing.T) {
		t.Parallel()
		router := setupTemplateRouter(
			NewMockStackTemplateRepository(),
			NewMockTemplateChartConfigRepository(),
			NewMockStackDefinitionRepository(),
			NewMockChartConfigRepository(),
			"uid-1", "user",
		)
		body := `{"name":"my-def"}`
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/templates/missing/instantiate", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("missing name returns 400", func(t *testing.T) {
		t.Parallel()
		tmplRepo := NewMockStackTemplateRepository()
		seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)

		router := setupTemplateRouter(tmplRepo, NewMockTemplateChartConfigRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), "uid-1", "user")
		body := `{"description":"missing name"}`
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/templates/t1/instantiate", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

// ---- CloneTemplate ----

func TestCloneTemplate(t *testing.T) {
	t.Parallel()

	t.Run("clones existing template", func(t *testing.T) {
		t.Parallel()
		tmplRepo := NewMockStackTemplateRepository()
		chartRepo := NewMockTemplateChartConfigRepository()
		seedTemplate(t, tmplRepo, "t1", "Original", "owner-1", true)
		require.NoError(t, chartRepo.Create(&models.TemplateChartConfig{
			ID:              "c1",
			StackTemplateID: "t1",
			ChartName:       "frontend",
		}))

		router := setupTemplateRouter(tmplRepo, chartRepo, NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), "uid-2", "devops")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/templates/t1/clone", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusCreated, w.Code)
		var resp models.StackTemplate
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.NotEqual(t, "t1", resp.ID, "clone should have a new ID")
		assert.False(t, resp.IsPublished, "clone should start as unpublished")
		assert.Equal(t, "uid-2", resp.OwnerID, "clone owner should be the caller")
	})

	t.Run("template not found returns 404", func(t *testing.T) {
		t.Parallel()
		router := setupTemplateRouter(
			NewMockStackTemplateRepository(),
			NewMockTemplateChartConfigRepository(),
			NewMockStackDefinitionRepository(),
			NewMockChartConfigRepository(),
			"uid-1", "devops",
		)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/templates/missing/clone", nil)
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/helm"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupInstanceRouter creates a test gin engine with InstanceHandler routes.
func setupInstanceRouter(
	instanceRepo *MockStackInstanceRepository,
	overrideRepo *MockValueOverrideRepository,
	defRepo *MockStackDefinitionRepository,
	ccRepo *MockChartConfigRepository,
	tmplRepo *MockStackTemplateRepository,
	tmplChartRepo *MockTemplateChartConfigRepository,
	callerID, callerUsername, callerRole string,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if callerID != "" {
			c.Set("userID", callerID)
		}
		if callerUsername != "" {
			c.Set("username", callerUsername)
		}
		if callerRole != "" {
			c.Set("role", callerRole)
		}
		c.Next()
	})

	valuesGen := helm.NewValuesGenerator()
	userRepo := NewMockUserRepository()
	h := NewInstanceHandler(instanceRepo, overrideRepo, defRepo, ccRepo, tmplRepo, tmplChartRepo, valuesGen, userRepo)

	insts := r.Group("/api/v1/stack-instances")
	{
		insts.GET("", h.ListInstances)
		insts.POST("", h.CreateInstance)
		insts.GET("/:id", h.GetInstance)
		insts.PUT("/:id", h.UpdateInstance)
		insts.DELETE("/:id", h.DeleteInstance)
		insts.POST("/:id/clone", h.CloneInstance)
		insts.GET("/:id/values/:chartId", h.ExportChartValues)
	}
	return r
}

// seedInstance inserts a StackInstance into the mock repo.
func seedInstance(t *testing.T, repo *MockStackInstanceRepository, id, name, defID, ownerID, status string) *models.StackInstance {
	t.Helper()
	inst := &models.StackInstance{
		ID:                id,
		StackDefinitionID: defID,
		Name:              name,
		Namespace:         "stack-" + name + "-owner",
		OwnerID:           ownerID,
		Branch:            "master",
		Status:            status,
		CreatedAt:         time.Now().UTC(),
		UpdatedAt:         time.Now().UTC(),
	}
	require.NoError(t, repo.Create(inst))
	return inst
}

// ---- ListInstances ----

func TestListInstances(t *testing.T) {
	t.Parallel()

	t.Run("returns all instances", func(t *testing.T) {
		t.Parallel()
		instRepo := NewMockStackInstanceRepository()
		seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusRunning)
		seedInstance(t, instRepo, "i2", "stack-b", "d1", "uid-2", models.StackStatusDraft)

		router := setupInstanceRouter(instRepo, NewMockValueOverrideRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), "uid-1", "alice", "user")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var list []models.StackInstance
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
		assert.Len(t, list, 2)
	})

	t.Run("filters by owner=me", func(t *testing.T) {
		t.Parallel()
		instRepo := NewMockStackInstanceRepository()
		seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusRunning)
		seedInstance(t, instRepo, "i2", "stack-b", "d1", "uid-2", models.StackStatusDraft)

		router := setupInstanceRouter(instRepo, NewMockValueOverrideRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), "uid-1", "alice", "user")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances?owner=me", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var list []models.StackInstance
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
		assert.Len(t, list, 1)
		assert.Equal(t, "uid-1", list[0].OwnerID)
	})

	t.Run("repository error returns 500", func(t *testing.T) {
		t.Parallel()
		instRepo := NewMockStackInstanceRepository()
		instRepo.SetError(errInternal)

		router := setupInstanceRouter(instRepo, NewMockValueOverrideRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), "uid-1", "alice", "user")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})
}

// ---- CreateInstance ----

func TestCreateInstance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		callerID   string
		setupDef   func(*MockStackDefinitionRepository)
		wantStatus int
		checkFn    func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:     "valid instance with existing definition",
			body:     `{"stack_definition_id":"d1","name":"my-stack"}`,
			callerID: "uid-1",
			setupDef: func(repo *MockStackDefinitionRepository) {
				seedDefinition(t, repo, "d1", "My Def", "uid-1")
			},
			wantStatus: http.StatusCreated,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp models.StackInstance
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, models.StackStatusDraft, resp.Status)
				assert.Equal(t, "master", resp.Branch)
				assert.NotEmpty(t, resp.Namespace)
			},
		},
		{
			name:       "definition not found returns 404",
			body:       `{"stack_definition_id":"missing","name":"my-stack"}`,
			callerID:   "uid-1",
			setupDef:   func(_ *MockStackDefinitionRepository) {},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "missing stack_definition_id returns 400",
			body:       `{"name":"my-stack"}`,
			callerID:   "uid-1",
			setupDef:   func(_ *MockStackDefinitionRepository) {},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing name returns 400",
			body:       `{"stack_definition_id":"d1"}`,
			callerID:   "uid-1",
			setupDef:   func(_ *MockStackDefinitionRepository) {},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid JSON returns 400",
			body:       `{bad`,
			callerID:   "uid-1",
			setupDef:   func(_ *MockStackDefinitionRepository) {},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			instRepo := NewMockStackInstanceRepository()
			defRepo := NewMockStackDefinitionRepository()
			tt.setupDef(defRepo)
			router := setupInstanceRouter(instRepo, NewMockValueOverrideRepository(), defRepo, NewMockChartConfigRepository(), NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), tt.callerID, "alice", "user")

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances", bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.checkFn != nil {
				tt.checkFn(t, w)
			}
		})
	}
}

// ---- GetInstance ----

func TestGetInstance(t *testing.T) {
	t.Parallel()

	t.Run("returns instance", func(t *testing.T) {
		t.Parallel()
		instRepo := NewMockStackInstanceRepository()
		seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)

		router := setupInstanceRouter(instRepo, NewMockValueOverrideRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), "uid-1", "alice", "user")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/i1", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp models.StackInstance
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "i1", resp.ID)
		assert.Equal(t, "stack-a", resp.Name)
		assert.Equal(t, "d1", resp.StackDefinitionID)
		assert.Equal(t, models.StackStatusDraft, resp.Status)
	})

	t.Run("not found returns 404", func(t *testing.T) {
		t.Parallel()
		instRepo := NewMockStackInstanceRepository()
		router := setupInstanceRouter(instRepo, NewMockValueOverrideRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), "uid-1", "alice", "user")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/missing", nil)
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

// ---- UpdateInstance ----

func TestUpdateInstance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		id         string
		body       string
		setup      func(*MockStackInstanceRepository)
		wantStatus int
	}{
		{
			name: "valid update",
			id:   "i1",
			body: `{"name":"new-name","branch":"develop"}`,
			setup: func(repo *MockStackInstanceRepository) {
				seedInstance(t, repo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)
			},
			wantStatus: http.StatusOK,
		},
		{
			name:       "not found returns 404",
			id:         "missing",
			body:       `{"name":"x","branch":"main"}`,
			setup:      func(_ *MockStackInstanceRepository) {},
			wantStatus: http.StatusNotFound,
		},
		{
			name: "partial update with only branch keeps existing name",
			id:   "i1",
			body: `{"branch":"develop"}`,
			setup: func(repo *MockStackInstanceRepository) {
				seedInstance(t, repo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)
			},
			wantStatus: http.StatusOK,
		},
		{
			name: "invalid JSON returns 400",
			id:   "i1",
			body: `{bad`,
			setup: func(repo *MockStackInstanceRepository) {
				seedInstance(t, repo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)
			},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			instRepo := NewMockStackInstanceRepository()
			tt.setup(instRepo)
			router := setupInstanceRouter(instRepo, NewMockValueOverrideRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), "uid-1", "alice", "user")

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPut, "/api/v1/stack-instances/"+tt.id, bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
		})
	}
}

// ---- DeleteInstance ----

func TestDeleteInstance(t *testing.T) {
	t.Parallel()

	t.Run("deletes existing instance", func(t *testing.T) {
		t.Parallel()
		instRepo := NewMockStackInstanceRepository()
		seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)

		router := setupInstanceRouter(instRepo, NewMockValueOverrideRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), "uid-1", "alice", "user")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/stack-instances/i1", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNoContent, w.Code)
	})

	t.Run("not found returns 404", func(t *testing.T) {
		t.Parallel()
		instRepo := NewMockStackInstanceRepository()
		router := setupInstanceRouter(instRepo, NewMockValueOverrideRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), "uid-1", "alice", "user")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/stack-instances/missing", nil)
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

// ---- CloneInstance ----

func TestCloneInstance(t *testing.T) {
	t.Parallel()

	t.Run("clones existing instance", func(t *testing.T) {
		t.Parallel()
		instRepo := NewMockStackInstanceRepository()
		overrideRepo := NewMockValueOverrideRepository()
		seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusRunning)
		require.NoError(t, overrideRepo.Create(&models.ValueOverride{
			ID:              "ov1",
			StackInstanceID: "i1",
			ChartConfigID:   "c1",
			Values:          "replicas: 3",
		}))

		router := setupInstanceRouter(instRepo, overrideRepo, NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), "uid-2", "bob", "user")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/i1/clone", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusCreated, w.Code)
		var resp models.StackInstance
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.NotEmpty(t, resp.ID)
		assert.NotEqual(t, "i1", resp.ID, "clone must have a new ID")
		assert.Equal(t, models.StackStatusDraft, resp.Status, "clone should start as draft")
		assert.Equal(t, "uid-2", resp.OwnerID, "clone owner should be the caller")
	})

	t.Run("source not found returns 404", func(t *testing.T) {
		t.Parallel()
		router := setupInstanceRouter(NewMockStackInstanceRepository(), NewMockValueOverrideRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), "uid-1", "alice", "user")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/missing/clone", nil)
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

// ---- ExportChartValues ----

func TestExportChartValues(t *testing.T) {
	t.Parallel()

	t.Run("exports values for existing chart", func(t *testing.T) {
		t.Parallel()
		instRepo := NewMockStackInstanceRepository()
		overrideRepo := NewMockValueOverrideRepository()
		ccRepo := NewMockChartConfigRepository()
		defRepo := NewMockStackDefinitionRepository()
		tmplRepo := NewMockStackTemplateRepository()
		tmplChartRepo := NewMockTemplateChartConfigRepository()

		seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
		seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)

		require.NoError(t, ccRepo.Create(&models.ChartConfig{
			ID:                "c1",
			StackDefinitionID: "d1",
			ChartName:         "backend",
			DefaultValues:     "replicas: 1\nimage: myapp",
		}))
		require.NoError(t, overrideRepo.Create(&models.ValueOverride{
			ID:              "ov1",
			StackInstanceID: "i1",
			ChartConfigID:   "c1",
			Values:          "replicas: 2",
		}))

		router := setupInstanceRouter(instRepo, overrideRepo, defRepo, ccRepo, tmplRepo, tmplChartRepo, "uid-1", "alice", "user")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/i1/values/c1", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("instance not found returns 404", func(t *testing.T) {
		t.Parallel()
		router := setupInstanceRouter(NewMockStackInstanceRepository(), NewMockValueOverrideRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), "uid-1", "alice", "user")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/missing/values/c1", nil)
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

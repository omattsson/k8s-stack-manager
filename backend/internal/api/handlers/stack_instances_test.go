package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
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
	h := NewInstanceHandler(instanceRepo, overrideRepo, nil, defRepo, ccRepo, tmplRepo, tmplChartRepo, valuesGen, userRepo, 0)

	insts := r.Group("/api/v1/stack-instances")
	{
		insts.GET("", h.ListInstances)
		insts.POST("", h.CreateInstance)
		insts.GET("/:id", h.GetInstance)
		insts.PUT("/:id", h.UpdateInstance)
		insts.DELETE("/:id", h.DeleteInstance)
		insts.POST("/:id/clone", h.CloneInstance)
		insts.GET("/:id/values/:chartId", h.ExportChartValues)
		insts.POST("/:id/extend", h.ExtendTTL)
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
			name: "partial update with only branch succeeds",
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

// ---- Namespace Uniqueness ----

func TestCreateInstanceNamespaceConflict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		seedNS     string // namespace to pre-seed as taken
		wantStatus int
		checkFn    func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:       "duplicate namespace returns 409 with suggestions",
			body:       `{"stack_definition_id":"d1","name":"my-stack"}`,
			seedNS:     "stack-my-stack-alice", // matches generated namespace
			wantStatus: http.StatusConflict,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp map[string]interface{}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, "namespace already exists", resp["error"])
				assert.Contains(t, resp["message"], "stack-my-stack-alice")
				// Suggestions are instance name suggestions (not namespaces).
				suggestions, ok := resp["suggestions"].([]interface{})
				require.True(t, ok, "suggestions should be an array")
				assert.Len(t, suggestions, 3)
				assert.Equal(t, "my-stack-2", suggestions[0])
				assert.Equal(t, "my-stack-3", suggestions[1])
				assert.Equal(t, "my-stack-4", suggestions[2])
			},
		},
		{
			name:       "unique namespace succeeds",
			body:       `{"stack_definition_id":"d1","name":"unique-stack"}`,
			seedNS:     "stack-other-alice", // different namespace, no conflict
			wantStatus: http.StatusCreated,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp models.StackInstance
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, "stack-unique-stack-alice", resp.Namespace)
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			instRepo := NewMockStackInstanceRepository()
			defRepo := NewMockStackDefinitionRepository()
			seedDefinition(t, defRepo, "d1", "My Def", "uid-1")

			// Pre-seed an instance with the conflicting namespace.
			existing := &models.StackInstance{
				ID:                "existing-1",
				StackDefinitionID: "d1",
				Name:              "existing",
				Namespace:         tt.seedNS,
				OwnerID:           "uid-other",
				Branch:            "master",
				Status:            models.StackStatusRunning,
			}
			require.NoError(t, instRepo.Create(existing))

			router := setupInstanceRouter(instRepo, NewMockValueOverrideRepository(), defRepo, NewMockChartConfigRepository(), NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), "uid-1", "alice", "user")
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

func TestCloneInstanceNamespaceConflict(t *testing.T) {
	t.Parallel()

	t.Run("clone with duplicate namespace returns 409", func(t *testing.T) {
		t.Parallel()
		instRepo := NewMockStackInstanceRepository()
		overrideRepo := NewMockValueOverrideRepository()

		// Seed the source instance.
		seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusRunning)

		// Seed another instance that already occupies the clone namespace.
		taken := &models.StackInstance{
			ID:                "i-taken",
			StackDefinitionID: "d1",
			Name:              "taken",
			Namespace:         "stack-stack-a-copy-bob",
			OwnerID:           "uid-other",
			Branch:            "master",
			Status:            models.StackStatusRunning,
		}
		require.NoError(t, instRepo.Create(taken))

		router := setupInstanceRouter(instRepo, overrideRepo, NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), "uid-2", "bob", "user")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/i1/clone", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusConflict, w.Code)
		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "namespace already exists", resp["error"])
		suggestions, ok := resp["suggestions"].([]interface{})
		require.True(t, ok)
		assert.Len(t, suggestions, 3)
	})
}

func TestCreateInstanceNameTooLong(t *testing.T) {
	t.Parallel()

	t.Run("name exceeding 50 chars returns 400", func(t *testing.T) {
		t.Parallel()
		instRepo := NewMockStackInstanceRepository()
		defRepo := NewMockStackDefinitionRepository()
		seedDefinition(t, defRepo, "d1", "My Def", "uid-1")

		longName := "a]bcdefghij-klmnopqrst-uvwxyz-0123456789-extra-chars"
		// Ensure the name is actually >50 chars.
		require.Greater(t, len(longName), 50)

		body := `{"stack_definition_id":"d1","name":"` + longName + `"}`
		router := setupInstanceRouter(instRepo, NewMockValueOverrideRepository(), defRepo, NewMockChartConfigRepository(), NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), "uid-1", "alice", "user")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Contains(t, resp["error"], "at most 50 characters")
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

// ---- TTL / Expiry Tests ----

// setupInstanceRouterWithTTL creates a test gin engine with a default TTL configured.
func setupInstanceRouterWithTTL(
	instanceRepo *MockStackInstanceRepository,
	defRepo *MockStackDefinitionRepository,
	defaultTTL int,
	callerID, callerUsername string,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userID", callerID)
		c.Set("username", callerUsername)
		c.Set("role", "user")
		c.Next()
	})
	valuesGen := helm.NewValuesGenerator()
	userRepo := NewMockUserRepository()
	h := NewInstanceHandler(instanceRepo, NewMockValueOverrideRepository(), nil, defRepo, NewMockChartConfigRepository(), NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), valuesGen, userRepo, defaultTTL)
	insts := r.Group("/api/v1/stack-instances")
	{
		insts.POST("", h.CreateInstance)
		insts.PUT("/:id", h.UpdateInstance)
		insts.POST("/:id/extend", h.ExtendTTL)
	}
	return r
}

func TestCreateInstance_WithTTL(t *testing.T) {
	t.Parallel()

	t.Run("TTL from request sets ExpiresAt", func(t *testing.T) {
		t.Parallel()
		instRepo := NewMockStackInstanceRepository()
		defRepo := NewMockStackDefinitionRepository()
		defRepo.Create(&models.StackDefinition{ID: "d1", Name: "def1", OwnerID: "uid-1"})

		router := setupInstanceRouterWithTTL(instRepo, defRepo, 0, "uid-1", "alice")

		body := `{"name":"ttl-test","stack_definition_id":"d1","ttl_minutes":120}`
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusCreated, w.Code)
		var inst models.StackInstance
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &inst))
		assert.Equal(t, 120, inst.TTLMinutes)
		assert.NotNil(t, inst.ExpiresAt)
		assert.True(t, inst.ExpiresAt.After(time.Now().Add(119*time.Minute)))
	})

	t.Run("default TTL applied when request has 0", func(t *testing.T) {
		t.Parallel()
		instRepo := NewMockStackInstanceRepository()
		defRepo := NewMockStackDefinitionRepository()
		defRepo.Create(&models.StackDefinition{ID: "d1", Name: "def1", OwnerID: "uid-1"})

		router := setupInstanceRouterWithTTL(instRepo, defRepo, 60, "uid-1", "alice")

		body := `{"name":"ttl-default","stack_definition_id":"d1"}`
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusCreated, w.Code)
		var inst models.StackInstance
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &inst))
		assert.Equal(t, 60, inst.TTLMinutes)
		assert.NotNil(t, inst.ExpiresAt)
	})

	t.Run("no TTL results in nil ExpiresAt", func(t *testing.T) {
		t.Parallel()
		instRepo := NewMockStackInstanceRepository()
		defRepo := NewMockStackDefinitionRepository()
		defRepo.Create(&models.StackDefinition{ID: "d1", Name: "def1", OwnerID: "uid-1"})

		router := setupInstanceRouterWithTTL(instRepo, defRepo, 0, "uid-1", "alice")

		body := `{"name":"no-ttl","stack_definition_id":"d1"}`
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusCreated, w.Code)
		var inst models.StackInstance
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &inst))
		assert.Equal(t, 0, inst.TTLMinutes)
		assert.Nil(t, inst.ExpiresAt)
	})
}

func TestExtendTTL(t *testing.T) {
	t.Parallel()

	t.Run("extends with request body ttl_minutes", func(t *testing.T) {
		t.Parallel()
		instRepo := NewMockStackInstanceRepository()
		defRepo := NewMockStackDefinitionRepository()

		past := time.Now().Add(-5 * time.Minute)
		inst := &models.StackInstance{
			ID:                "i1",
			StackDefinitionID: "d1",
			Name:              "test",
			Namespace:         "stack-test-alice",
			OwnerID:           "uid-1",
			Branch:            "master",
			Status:            models.StackStatusRunning,
			TTLMinutes:        60,
			ExpiresAt:         &past,
		}
		require.NoError(t, instRepo.Create(inst))

		router := setupInstanceRouterWithTTL(instRepo, defRepo, 0, "uid-1", "alice")

		body := `{"ttl_minutes":240}`
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/i1/extend", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var result models.StackInstance
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
		assert.Equal(t, 240, result.TTLMinutes)
		assert.NotNil(t, result.ExpiresAt)
		assert.True(t, result.ExpiresAt.After(time.Now().Add(239*time.Minute)))
	})

	t.Run("extends using existing TTLMinutes when body omitted", func(t *testing.T) {
		t.Parallel()
		instRepo := NewMockStackInstanceRepository()
		defRepo := NewMockStackDefinitionRepository()

		past := time.Now().Add(-5 * time.Minute)
		inst := &models.StackInstance{
			ID:                "i2",
			StackDefinitionID: "d1",
			Name:              "test2",
			Namespace:         "stack-test2-alice",
			OwnerID:           "uid-1",
			Branch:            "master",
			Status:            models.StackStatusRunning,
			TTLMinutes:        120,
			ExpiresAt:         &past,
		}
		require.NoError(t, instRepo.Create(inst))

		router := setupInstanceRouterWithTTL(instRepo, defRepo, 0, "uid-1", "alice")

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/i2/extend", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var result models.StackInstance
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
		assert.Equal(t, 120, result.TTLMinutes)
		assert.NotNil(t, result.ExpiresAt)
		assert.True(t, result.ExpiresAt.After(time.Now().Add(119*time.Minute)))
	})

	t.Run("returns 400 when no TTL configured", func(t *testing.T) {
		t.Parallel()
		instRepo := NewMockStackInstanceRepository()
		defRepo := NewMockStackDefinitionRepository()

		inst := &models.StackInstance{
			ID:                "i3",
			StackDefinitionID: "d1",
			Name:              "no-ttl",
			Namespace:         "stack-no-ttl-alice",
			OwnerID:           "uid-1",
			Branch:            "master",
			Status:            models.StackStatusRunning,
			TTLMinutes:        0,
		}
		require.NoError(t, instRepo.Create(inst))

		router := setupInstanceRouterWithTTL(instRepo, defRepo, 0, "uid-1", "alice")

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/i3/extend", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("returns 404 for missing instance", func(t *testing.T) {
		t.Parallel()
		instRepo := NewMockStackInstanceRepository()
		defRepo := NewMockStackDefinitionRepository()

		router := setupInstanceRouterWithTTL(instRepo, defRepo, 0, "uid-1", "alice")

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/missing/extend", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("returns 500 when repo update fails", func(t *testing.T) {
		t.Parallel()
		instRepo := NewMockStackInstanceRepository()
		defRepo := NewMockStackDefinitionRepository()

		past := time.Now().Add(-5 * time.Minute)
		inst := &models.StackInstance{
			ID:                "i-err",
			StackDefinitionID: "d1",
			Name:              "err-ttl",
			Namespace:         "stack-err-ttl-alice",
			OwnerID:           "uid-1",
			Branch:            "master",
			Status:            models.StackStatusRunning,
			TTLMinutes:        60,
			ExpiresAt:         &past,
		}
		require.NoError(t, instRepo.Create(inst))
		instRepo.SetError(errors.New("db write failure"))

		router := setupInstanceRouterWithTTL(instRepo, defRepo, 0, "uid-1", "alice")

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/i-err/extend", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})
}

// ---- GetRecentInstances repo error ----

func TestGetRecentInstances_RepoError(t *testing.T) {
	t.Parallel()
	router, repo := setupRecentInstancesRouter()
	repo.SetError(errors.New("db read failure"))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/stack-instances/recent", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ---- MaxTTLMinutes enforcement ----

func TestCreateInstance_MaxTTLExceeded(t *testing.T) {
	t.Parallel()

	instRepo := NewMockStackInstanceRepository()
	defRepo := NewMockStackDefinitionRepository()
	defRepo.Create(&models.StackDefinition{ID: "d1", Name: "def1", OwnerID: "uid-1"})

	router := setupInstanceRouterWithTTL(instRepo, defRepo, 0, "uid-1", "alice")

	body := `{"name":"ttl-big","stack_definition_id":"d1","ttl_minutes":99999}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp["error"], "TTL must not exceed")
}

func TestUpdateInstance_MaxTTLExceeded(t *testing.T) {
	t.Parallel()

	instRepo := NewMockStackInstanceRepository()
	defRepo := NewMockStackDefinitionRepository()
	seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)

	router := setupInstanceRouterWithTTL(instRepo, defRepo, 0, "uid-1", "alice")

	body := `{"name":"stack-a","branch":"master","ttl_minutes":99999}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPut, "/api/v1/stack-instances/i1", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp["error"], "TTL must not exceed")
}

func TestExtendTTL_MaxTTLExceeded(t *testing.T) {
	t.Parallel()

	instRepo := NewMockStackInstanceRepository()
	defRepo := NewMockStackDefinitionRepository()

	past := time.Now().Add(-5 * time.Minute)
	inst := &models.StackInstance{
		ID:                "i-max",
		StackDefinitionID: "d1",
		Name:              "max-ttl",
		Namespace:         "stack-max-ttl-alice",
		OwnerID:           "uid-1",
		Branch:            "master",
		Status:            models.StackStatusRunning,
		TTLMinutes:        60,
		ExpiresAt:         &past,
	}
	require.NoError(t, instRepo.Create(inst))

	router := setupInstanceRouterWithTTL(instRepo, defRepo, 0, "uid-1", "alice")

	body := `{"ttl_minutes":99999}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/i-max/extend", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp["error"], "TTL must not exceed")
}

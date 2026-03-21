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

// setupValueOverrideRouter creates a test gin engine with override routes on the InstanceHandler.
func setupValueOverrideRouter(
	instanceRepo *MockStackInstanceRepository,
	overrideRepo *MockValueOverrideRepository,
	defRepo *MockStackDefinitionRepository,
	ccRepo *MockChartConfigRepository,
	tmplRepo *MockStackTemplateRepository,
	tmplChartRepo *MockTemplateChartConfigRepository,
	callerID, callerRole string,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(injectAuthContext(callerID, callerRole))

	valuesGen := helm.NewValuesGenerator()
	userRepo := NewMockUserRepository()
	h := NewInstanceHandler(instanceRepo, overrideRepo, nil, defRepo, ccRepo, tmplRepo, tmplChartRepo, valuesGen, userRepo, 0)

	insts := r.Group("/api/v1/stack-instances")
	{
		insts.GET("/:id/overrides", h.GetOverrides)
		insts.PUT("/:id/overrides/:chartId", h.SetOverride)
	}
	return r
}

// seedValueOverride inserts a ValueOverride into the mock repo and returns it.
func seedValueOverride(t *testing.T, repo *MockValueOverrideRepository, id, instanceID, chartID, values string) *models.ValueOverride {
	t.Helper()
	v := &models.ValueOverride{
		ID:              id,
		StackInstanceID: instanceID,
		ChartConfigID:   chartID,
		Values:          values,
		UpdatedAt:       time.Now().UTC(),
	}
	require.NoError(t, repo.Create(v))
	return v
}

// ---- GetOverrides ----

func TestGetOverrides(t *testing.T) {
	t.Parallel()

	t.Run("returns overrides list", func(t *testing.T) {
		t.Parallel()
		instRepo := NewMockStackInstanceRepository()
		seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusDraft)

		overrideRepo := NewMockValueOverrideRepository()
		seedValueOverride(t, overrideRepo, "or-1", "inst-1", "chart-1", "replicaCount: 2")
		seedValueOverride(t, overrideRepo, "or-2", "inst-1", "chart-2", "image.tag: latest")

		router := setupValueOverrideRouter(
			instRepo, overrideRepo,
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			"uid-1", "user",
		)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/inst-1/overrides", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var overrides []models.ValueOverride
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &overrides))
		assert.Len(t, overrides, 2)
	})

	t.Run("returns empty list when no overrides", func(t *testing.T) {
		t.Parallel()
		instRepo := NewMockStackInstanceRepository()
		seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusDraft)

		overrideRepo := NewMockValueOverrideRepository()

		router := setupValueOverrideRouter(
			instRepo, overrideRepo,
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			"uid-1", "user",
		)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/inst-1/overrides", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var overrides []models.ValueOverride
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &overrides))
		assert.Empty(t, overrides)
	})

	t.Run("instance not found returns 404", func(t *testing.T) {
		t.Parallel()
		instRepo := NewMockStackInstanceRepository()
		overrideRepo := NewMockValueOverrideRepository()

		router := setupValueOverrideRouter(
			instRepo, overrideRepo,
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			"uid-1", "user",
		)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/missing-inst/overrides", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

// ---- SetOverride ----

func TestSetOverride(t *testing.T) {
	t.Parallel()

	t.Run("creates new override", func(t *testing.T) {
		t.Parallel()
		instRepo := NewMockStackInstanceRepository()
		seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusDraft)

		overrideRepo := NewMockValueOverrideRepository()

		router := setupValueOverrideRouter(
			instRepo, overrideRepo,
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			"uid-1", "user",
		)
		w := httptest.NewRecorder()
		body := `{"values":"replicaCount: 3"}`
		req, _ := http.NewRequest(http.MethodPut, "/api/v1/stack-instances/inst-1/overrides/chart-1", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var override models.ValueOverride
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &override))
		assert.NotEmpty(t, override.ID)
		assert.Equal(t, "inst-1", override.StackInstanceID)
		assert.Equal(t, "chart-1", override.ChartConfigID)
		assert.Equal(t, "replicaCount: 3", override.Values)
	})

	t.Run("updates existing override", func(t *testing.T) {
		t.Parallel()
		instRepo := NewMockStackInstanceRepository()
		seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusDraft)

		overrideRepo := NewMockValueOverrideRepository()
		seedValueOverride(t, overrideRepo, "or-1", "inst-1", "chart-1", "replicaCount: 1")

		router := setupValueOverrideRouter(
			instRepo, overrideRepo,
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			"uid-1", "user",
		)
		w := httptest.NewRecorder()
		body := `{"values":"replicaCount: 5"}`
		req, _ := http.NewRequest(http.MethodPut, "/api/v1/stack-instances/inst-1/overrides/chart-1", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var override models.ValueOverride
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &override))
		assert.Equal(t, "replicaCount: 5", override.Values)
	})

	t.Run("locked values conflict returns 400", func(t *testing.T) {
		t.Parallel()
		// Setup: definition from a template with a locked chart value.
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

		ccRepo := NewMockChartConfigRepository()
		seedChartConfig(t, ccRepo, "chart-1", "def-1", "nginx")

		tmplChartRepo := NewMockTemplateChartConfigRepository()
		require.NoError(t, tmplChartRepo.Create(&models.TemplateChartConfig{
			ID:              "tc-1",
			StackTemplateID: "tmpl-1",
			ChartName:       "nginx",
			LockedValues:    "replicaCount: 3",
			CreatedAt:       time.Now().UTC(),
		}))

		instRepo := NewMockStackInstanceRepository()
		seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusDraft)

		overrideRepo := NewMockValueOverrideRepository()

		router := setupValueOverrideRouter(
			instRepo, overrideRepo,
			defRepo, ccRepo,
			NewMockStackTemplateRepository(), tmplChartRepo,
			"uid-1", "user",
		)
		w := httptest.NewRecorder()
		// Attempting to override "replicaCount" which is locked.
		body := `{"values":"replicaCount: 10"}`
		req, _ := http.NewRequest(http.MethodPut, "/api/v1/stack-instances/inst-1/overrides/chart-1", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		var resp map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Contains(t, resp["error"], "replicaCount")
	})

	t.Run("missing instance returns 404", func(t *testing.T) {
		t.Parallel()
		instRepo := NewMockStackInstanceRepository()
		overrideRepo := NewMockValueOverrideRepository()

		router := setupValueOverrideRouter(
			instRepo, overrideRepo,
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			"uid-1", "user",
		)
		w := httptest.NewRecorder()
		body := `{"values":"replicaCount: 3"}`
		req, _ := http.NewRequest(http.MethodPut, "/api/v1/stack-instances/missing-inst/overrides/chart-1", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("invalid JSON returns 400", func(t *testing.T) {
		t.Parallel()
		instRepo := NewMockStackInstanceRepository()
		seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusDraft)
		overrideRepo := NewMockValueOverrideRepository()

		router := setupValueOverrideRouter(
			instRepo, overrideRepo,
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			"uid-1", "user",
		)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPut, "/api/v1/stack-instances/inst-1/overrides/chart-1", bytes.NewBufferString(`{not json}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

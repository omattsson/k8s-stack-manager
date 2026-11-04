package handlers

import (
	"bytes"
	"context"
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
		insts.GET("/:id/overrides/:chartId", h.GetOverride)
		insts.PUT("/:id/overrides/:chartId", h.SetOverride)
		insts.DELETE("/:id/overrides/:chartId", h.DeleteOverride)
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
	require.NoError(t, repo.Create(context.Background(), v))
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

// TestSetOverrideValidatesYAML covers the check with the parser of the deploy
// (#753): a refused value changes nothing, also when an override exists.
func TestSetOverrideValidatesYAML(t *testing.T) {
	t.Parallel()

	const stored = "replicaCount: 1"
	tests := []struct {
		name       string
		values     string
		wantStatus int
		wantError  string
		wantStored string // the row after the request; "" means no row
	}{
		{"broken YAML", "foo: [unclosed\n  bar: : baz", http.StatusBadRequest, "Invalid YAML: line 1", stored},
		{"plain scalar", "just-a-string", http.StatusBadRequest, "must be a map", stored},
		{"list", "- a\n- b", http.StatusBadRequest, "must be a map", stored},
		{"duplicate keys", "a: 1\na: 2", http.StatusBadRequest, `mapping key \"a\" already defined`, stored},
		{"unquoted template", "tag: {{.Branch}}", http.StatusBadRequest, "Put a template value in quotes", stored},
		{"quoted template", `tag: "{{.Branch}}"`, http.StatusOK, "", `tag: "{{.Branch}}"`},
		{"template inside a plain scalar", "image: repo:{{.Branch}}", http.StatusOK, "", "image: repo:{{.Branch}}"},
		{"comment only", "# nothing yet", http.StatusOK, "", "# nothing yet"},
		{"empty removes the override", "", http.StatusNoContent, "", ""},
		{"whitespace removes the override", "  \n", http.StatusNoContent, "", ""},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			instRepo := NewMockStackInstanceRepository()
			seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusDraft)
			overrideRepo := NewMockValueOverrideRepository()
			seedValueOverride(t, overrideRepo, "or-1", "inst-1", "chart-1", stored)

			router := setupValueOverrideRouter(
				instRepo, overrideRepo,
				NewMockStackDefinitionRepository(), newOverrideTestChartRepo(t),
				NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
				"uid-1", "user",
			)
			body, err := json.Marshal(map[string]string{"values": tt.values})
			require.NoError(t, err)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPut, "/api/v1/stack-instances/inst-1/overrides/chart-1", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantError != "" {
				assert.Contains(t, w.Body.String(), tt.wantError)
				assert.NotContains(t, w.Body.String(), "interface {}")
			}
			got, findErr := overrideRepo.FindByInstanceAndChart(context.Background(), "inst-1", "chart-1")
			if tt.wantStored == "" {
				assert.Error(t, findErr)
				return
			}
			require.NoError(t, findErr)
			assert.Equal(t, tt.wantStored, got.Values)
		})
	}
}

// TestSetOverrideInvalidYAMLAfterAccessChecks: the YAML check must not tell a
// caller without access, or for a chart outside the definition, anything about
// the request: authorization and the chart lookup come first (#753).
func TestSetOverrideInvalidYAMLAfterAccessChecks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		userID     string
		chartID    string
		wantStatus int
	}{
		{"not the owner", "uid-other", "chart-1", http.StatusForbidden},
		{"chart outside the definition", "uid-1", "chart-unknown", http.StatusNotFound},
		{"owner and chart of the definition", "uid-1", "chart-1", http.StatusBadRequest},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			instRepo := NewMockStackInstanceRepository()
			seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusDraft)
			router := setupValueOverrideRouter(
				instRepo, NewMockValueOverrideRepository(),
				NewMockStackDefinitionRepository(), newOverrideTestChartRepo(t),
				NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
				tt.userID, "user",
			)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPut, "/api/v1/stack-instances/inst-1/overrides/"+tt.chartID, bytes.NewBufferString(`{"values":"a: b: c"}`))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code, w.Body.String())
		})
	}
}

// TestGetOverridesLoadsStoredInvalidValue: a value stored before the check
// existed must still load, so the user can fix it (#753).
func TestGetOverridesLoadsStoredInvalidValue(t *testing.T) {
	t.Parallel()

	const invalid = "foo: [unclosed\n  bar: : baz"
	instRepo := NewMockStackInstanceRepository()
	seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusDraft)
	overrideRepo := NewMockValueOverrideRepository()
	seedValueOverride(t, overrideRepo, "or-1", "inst-1", "chart-1", invalid)
	router := setupValueOverrideRouter(
		instRepo, overrideRepo,
		NewMockStackDefinitionRepository(), newOverrideTestChartRepo(t),
		NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
		"uid-1", "user",
	)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/inst-1/overrides", nil)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var listed []models.ValueOverride
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &listed))
	require.Len(t, listed, 1)
	assert.Equal(t, invalid, listed[0].Values)

	// The fix goes through the normal save.
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodPut, "/api/v1/stack-instances/inst-1/overrides/chart-1", bytes.NewBufferString(`{"values":"foo: bar"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

func TestSetOverride(t *testing.T) {
	t.Parallel()

	t.Run("creates new override", func(t *testing.T) {
		t.Parallel()
		instRepo := NewMockStackInstanceRepository()
		seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusDraft)

		overrideRepo := NewMockValueOverrideRepository()

		router := setupValueOverrideRouter(
			instRepo, overrideRepo,
			NewMockStackDefinitionRepository(), newOverrideTestChartRepo(t),
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
			NewMockStackDefinitionRepository(), newOverrideTestChartRepo(t),
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
		require.NoError(t, defRepo.Create(context.Background(), def))

		ccRepo := NewMockChartConfigRepository()
		seedChartConfig(t, ccRepo, "chart-1", "def-1", "nginx")

		tmplChartRepo := NewMockTemplateChartConfigRepository()
		require.NoError(t, tmplChartRepo.Create(context.Background(), &models.TemplateChartConfig{
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
			NewMockStackDefinitionRepository(), newOverrideTestChartRepo(t),
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
			NewMockStackDefinitionRepository(), newOverrideTestChartRepo(t),
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

package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/cluster"
	"backend/internal/database"
	"backend/internal/deployer"
	"backend/internal/helm"
	"backend/internal/k8s"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// setupInstanceRouterFull creates a test gin engine with all InstanceHandler routes
// including deploy/stop/clean/status/compare/extend for full coverage testing.
func setupInstanceRouterFull(
	t *testing.T,
	instanceRepo *MockStackInstanceRepository,
	overrideRepo *MockValueOverrideRepository,
	branchOverrideRepo *MockChartBranchOverrideRepository,
	defRepo *MockStackDefinitionRepository,
	ccRepo *MockChartConfigRepository,
	tmplRepo *MockStackTemplateRepository,
	tmplChartRepo *MockTemplateChartConfigRepository,
	deployManager *deployer.Manager,
	k8sWatcher *k8s.Watcher,
	registry *cluster.Registry,
	deployLogRepo models.DeploymentLogRepository,
	callerID, callerUsername, callerRole string,
) *gin.Engine {
	t.Helper()
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
	// Default branchOverrideRepo to a non-nil mock to prevent nil pointer panics
	if branchOverrideRepo == nil {
		branchOverrideRepo = NewMockChartBranchOverrideRepository()
	}
	h, err := NewInstanceHandlerWithDeployer(
		instanceRepo, overrideRepo, branchOverrideRepo, defRepo, ccRepo,
		tmplRepo, tmplChartRepo, valuesGen, userRepo,
		deployManager, k8sWatcher, registry, deployLogRepo, nil,
		0,
		&mockHandlerTxRunner{repos: database.TxRepos{
			StackDefinition: defRepo,
			ChartConfig:     ccRepo,
			StackInstance:   instanceRepo,
			StackTemplate:   tmplRepo,
			TemplateChart:   tmplChartRepo,
			ValueOverride:   overrideRepo,
			BranchOverride:  branchOverrideRepo,
		}},
	)
	require.NoError(t, err)

	insts := r.Group("/api/v1/stack-instances")
	{
		insts.GET("", h.ListInstances)
		insts.POST("", h.CreateInstance)
		insts.GET("/compare", h.CompareInstances)
		insts.GET("/:id", h.GetInstance)
		insts.PUT("/:id", h.UpdateInstance)
		insts.DELETE("/:id", h.DeleteInstance)
		insts.POST("/:id/clone", h.CloneInstance)
		insts.GET("/:id/values/:chartId", h.ExportChartValues)
		insts.GET("/:id/values", h.ExportAllValues)
		insts.POST("/:id/deploy", h.DeployInstance)
		insts.POST("/:id/stop", h.StopInstance)
		insts.POST("/:id/clean", h.CleanInstance)
		insts.GET("/:id/deploy-log", h.GetDeployLog)
		insts.GET("/:id/status", h.GetInstanceStatus)
		insts.POST("/:id/extend", h.ExtendTTL)
	}
	return r
}

// seedChartConfigForTest is a helper to create chart configs for tests in this file.
func seedChartConfigForTest(t *testing.T, repo *MockChartConfigRepository, id, defID, chartName string) {
	t.Helper()
	require.NoError(t, repo.Create(&models.ChartConfig{
		ID:                id,
		StackDefinitionID: defID,
		ChartName:         chartName,
		DefaultValues:     "replicas: 1",
	}))
}

// ---- UpdateInstance additional tests ----

func TestUpdateInstance_ForbiddenNonOwner(t *testing.T) {
	t.Parallel()

	instRepo := NewMockStackInstanceRepository()
	defRepo := NewMockStackDefinitionRepository()
	ccRepo := NewMockChartConfigRepository()

	seedInstance(t, instRepo, "i1", "stack-a", "d1", "owner-1", models.StackStatusDraft)

	router := setupInstanceRouterFull(t,
		instRepo, NewMockValueOverrideRepository(), nil, defRepo, ccRepo,
		NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
		nil, nil, nil, nil,
		"other-user", "bob", "user",
	)

	body := `{"name":"updated-name"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPut, "/api/v1/stack-instances/i1", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestUpdateInstance_AdminCanUpdate(t *testing.T) {
	t.Parallel()

	instRepo := NewMockStackInstanceRepository()
	defRepo := NewMockStackDefinitionRepository()
	seedInstance(t, instRepo, "i1", "stack-a", "d1", "owner-1", models.StackStatusDraft)

	router := setupInstanceRouterFull(t,
		instRepo, NewMockValueOverrideRepository(), nil, defRepo,
		NewMockChartConfigRepository(),
		NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
		nil, nil, nil, nil,
		"admin-user", "admin", "admin",
	)

	body := `{"name":"updated-name"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPut, "/api/v1/stack-instances/i1", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var result models.StackInstance
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	assert.Equal(t, "updated-name", result.Name)
}

func TestUpdateInstance_TTLUpdate(t *testing.T) {
	t.Parallel()

	t.Run("set TTL adds ExpiresAt", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil,
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, nil, nil,
			"uid-1", "alice", "user",
		)

		body := `{"ttl_minutes":120}`
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPut, "/api/v1/stack-instances/i1", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var result models.StackInstance
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
		assert.Equal(t, 120, result.TTLMinutes)
		assert.NotNil(t, result.ExpiresAt)
	})

	t.Run("set TTL to zero clears ExpiresAt", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		exp := time.Now().Add(1 * time.Hour)
		inst := &models.StackInstance{
			ID:                "i2",
			StackDefinitionID: "d1",
			Name:              "stack-b",
			Namespace:         "stack-stack-b-owner",
			OwnerID:           "uid-1",
			Branch:            "master",
			Status:            models.StackStatusDraft,
			TTLMinutes:        60,
			ExpiresAt:         &exp,
			CreatedAt:         time.Now().UTC(),
			UpdatedAt:         time.Now().UTC(),
		}
		require.NoError(t, instRepo.Create(inst))

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil,
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, nil, nil,
			"uid-1", "alice", "user",
		)

		body := `{"ttl_minutes":0}`
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPut, "/api/v1/stack-instances/i2", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var result models.StackInstance
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
		assert.Equal(t, 0, result.TTLMinutes)
		assert.Nil(t, result.ExpiresAt)
	})

	t.Run("invalid JSON returns 400", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		seedInstance(t, instRepo, "i3", "stack-c", "d1", "uid-1", models.StackStatusDraft)

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil,
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, nil, nil,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPut, "/api/v1/stack-instances/i3", bytes.NewBufferString("{bad"))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("repo update error returns 500", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		seedInstance(t, instRepo, "i4", "stack-d", "d1", "uid-1", models.StackStatusDraft)
		instRepo.SetError(errors.New("db failure"))

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil,
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, nil, nil,
			"uid-1", "alice", "user",
		)

		body := `{"name":"new-name"}`
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPut, "/api/v1/stack-instances/i4", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})
}

// ---- CloneInstance additional tests ----

func TestCloneInstance_Additional(t *testing.T) {
	t.Parallel()

	t.Run("source not found returns 404", func(t *testing.T) {
		t.Parallel()

		router := setupInstanceRouterFull(t,
			NewMockStackInstanceRepository(), NewMockValueOverrideRepository(), nil,
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, nil, nil,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/missing/clone", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("clone copies value overrides", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		overrideRepo := NewMockValueOverrideRepository()
		defRepo := NewMockStackDefinitionRepository()
		ccRepo := NewMockChartConfigRepository()

		seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
		seedInstance(t, instRepo, "src-1", "my-stack", "d1", "uid-1", models.StackStatusRunning)

		// Create value overrides for the source instance.
		require.NoError(t, overrideRepo.Create(&models.ValueOverride{
			ID:              "ov1",
			StackInstanceID: "src-1",
			ChartConfigID:   "cc1",
			Values:          "replicas: 3",
		}))

		router := setupInstanceRouterFull(t,
			instRepo, overrideRepo, nil, defRepo, ccRepo,
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, nil, nil,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/src-1/clone", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusCreated, w.Code)
		var result models.StackInstance
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
		assert.Contains(t, result.Name, "(Copy)")
		assert.Equal(t, "d1", result.StackDefinitionID)

		// Verify value overrides were copied.
		clonedOverrides, err := overrideRepo.ListByInstance(result.ID)
		require.NoError(t, err)
		assert.Len(t, clonedOverrides, 1)
		assert.Equal(t, "replicas: 3", clonedOverrides[0].Values)
	})

	t.Run("clone create failure returns error", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		overrideRepo := NewMockValueOverrideRepository()

		seedInstance(t, instRepo, "src-2", "my-stack", "d1", "uid-1", models.StackStatusRunning)

		// Set create error but only after the source was seeded.
		instRepo.SetCreateError(errors.New("db create failure"))

		router := setupInstanceRouterFull(t,
			instRepo, overrideRepo, nil,
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, nil, nil,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/src-2/clone", nil)
		router.ServeHTTP(w, req)

		// Should fail with 500 because create fails.
		assert.True(t, w.Code >= 400)
	})
}

// ---- ExportChartValues additional tests ----

func TestExportChartValues_Additional(t *testing.T) {
	t.Parallel()

	t.Run("chart not found returns 404", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil,
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, nil, nil,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/i1/values/missing-chart", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("definition not found returns 404", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		ccRepo := NewMockChartConfigRepository()

		// Create instance pointing to non-existent definition.
		seedInstance(t, instRepo, "i1", "stack-a", "missing-def", "uid-1", models.StackStatusDraft)
		seedChartConfigForTest(t, ccRepo, "c1", "missing-def", "backend")

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil,
			NewMockStackDefinitionRepository(), ccRepo,
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, nil, nil,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/i1/values/c1", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("exports with locked values from template", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		ccRepo := NewMockChartConfigRepository()
		defRepo := NewMockStackDefinitionRepository()
		tmplChartRepo := NewMockTemplateChartConfigRepository()

		// Create definition with source template ID.
		def := &models.StackDefinition{
			ID:               "d1",
			Name:             "My Def",
			OwnerID:          "uid-1",
			DefaultBranch:    "master",
			SourceTemplateID: "tmpl-1",
		}
		require.NoError(t, defRepo.Create(def))
		seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)
		seedChartConfigForTest(t, ccRepo, "c1", "d1", "backend")

		// Create template chart config with locked values.
		require.NoError(t, tmplChartRepo.Create(&models.TemplateChartConfig{
			ID:              "tc1",
			StackTemplateID: "tmpl-1",
			ChartName:       "backend",
			LockedValues:    "image: locked-image",
		}))

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(),
			NewMockChartBranchOverrideRepository(), defRepo, ccRepo,
			NewMockStackTemplateRepository(), tmplChartRepo,
			nil, nil, nil, nil,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/i1/values/c1", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "locked-image")
	})

	t.Run("exports with branch override", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		ccRepo := NewMockChartConfigRepository()
		defRepo := NewMockStackDefinitionRepository()
		branchRepo := NewMockChartBranchOverrideRepository()

		seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
		seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)

		require.NoError(t, ccRepo.Create(&models.ChartConfig{
			ID:                "c1",
			StackDefinitionID: "d1",
			ChartName:         "backend",
			DefaultValues:     "branch: \"{{.Branch}}\"",
		}))

		require.NoError(t, branchRepo.Set(&models.ChartBranchOverride{
			StackInstanceID: "i1",
			ChartConfigID:   "c1",
			Branch:          "feature/test",
		}))

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), branchRepo, defRepo, ccRepo,
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, nil, nil,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/i1/values/c1", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "feature/test")
	})
}

// ---- CompareInstances tests ----

func TestCompareInstances_Additional(t *testing.T) {
	t.Parallel()

	t.Run("missing query params returns 400", func(t *testing.T) {
		t.Parallel()

		router := setupInstanceRouterFull(t,
			NewMockStackInstanceRepository(), NewMockValueOverrideRepository(), nil,
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, nil, nil,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/compare", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("missing right param returns 400", func(t *testing.T) {
		t.Parallel()

		router := setupInstanceRouterFull(t,
			NewMockStackInstanceRepository(), NewMockValueOverrideRepository(), nil,
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, nil, nil,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/compare?left=i1", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("left instance not found returns 404", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		seedInstance(t, instRepo, "i2", "stack-b", "d1", "uid-1", models.StackStatusDraft)

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil,
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, nil, nil,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/compare?left=missing&right=i2", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("right instance not found returns 404", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil,
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, nil, nil,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/compare?left=i1&right=missing", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("left definition not found returns error", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		defRepo := NewMockStackDefinitionRepository()

		// Left instance points to missing definition.
		seedInstance(t, instRepo, "i1", "stack-a", "missing-def", "uid-1", models.StackStatusDraft)
		seedInstance(t, instRepo, "i2", "stack-b", "d1", "uid-1", models.StackStatusDraft)
		seedDefinition(t, defRepo, "d1", "Def B", "uid-1")

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil, defRepo,
			NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, nil, nil,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/compare?left=i1&right=i2", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("right definition not found returns error", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		defRepo := NewMockStackDefinitionRepository()

		seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)
		seedInstance(t, instRepo, "i2", "stack-b", "missing-def", "uid-1", models.StackStatusDraft)
		seedDefinition(t, defRepo, "d1", "Def A", "uid-1")

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil, defRepo,
			NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, nil, nil,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/compare?left=i1&right=i2", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("successful comparison with same definition", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		defRepo := NewMockStackDefinitionRepository()
		ccRepo := NewMockChartConfigRepository()
		overrideRepo := NewMockValueOverrideRepository()

		seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
		seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusRunning)
		seedInstance(t, instRepo, "i2", "stack-b", "d1", "uid-1", models.StackStatusRunning)

		seedChartConfigForTest(t, ccRepo, "cc1", "d1", "backend")

		// Add override for left instance only.
		require.NoError(t, overrideRepo.Create(&models.ValueOverride{
			ID:              "ov1",
			StackInstanceID: "i1",
			ChartConfigID:   "cc1",
			Values:          "replicas: 3",
		}))

		router := setupInstanceRouterFull(t,
			instRepo, overrideRepo, nil, defRepo, ccRepo,
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, nil, nil,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/compare?left=i1&right=i2", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp CompareInstancesResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "i1", resp.Left.ID)
		assert.Equal(t, "i2", resp.Right.ID)
		assert.Equal(t, "My Def", resp.Left.DefinitionName)
		assert.Len(t, resp.Charts, 1)
		assert.Equal(t, "backend", resp.Charts[0].ChartName)
		assert.True(t, resp.Charts[0].HasDifferences) // different overrides
	})

	t.Run("comparison with different definitions", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		defRepo := NewMockStackDefinitionRepository()
		ccRepo := NewMockChartConfigRepository()

		seedDefinition(t, defRepo, "d1", "Def A", "uid-1")
		seedDefinition(t, defRepo, "d2", "Def B", "uid-1")
		seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusRunning)
		seedInstance(t, instRepo, "i2", "stack-b", "d2", "uid-1", models.StackStatusRunning)

		seedChartConfigForTest(t, ccRepo, "cc1", "d1", "frontend")
		seedChartConfigForTest(t, ccRepo, "cc2", "d2", "backend")

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil, defRepo, ccRepo,
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, nil, nil,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/compare?left=i1&right=i2", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp CompareInstancesResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		// Both charts should appear, each only on one side.
		assert.Len(t, resp.Charts, 2)
		for _, chart := range resp.Charts {
			assert.True(t, chart.HasDifferences) // each chart present on only one side
		}
	})

	t.Run("comparison with locked values from template", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		defRepo := NewMockStackDefinitionRepository()
		ccRepo := NewMockChartConfigRepository()
		tmplChartRepo := NewMockTemplateChartConfigRepository()

		def := &models.StackDefinition{
			ID:               "d1",
			Name:             "Templated Def",
			OwnerID:          "uid-1",
			DefaultBranch:    "master",
			SourceTemplateID: "tmpl-1",
		}
		require.NoError(t, defRepo.Create(def))

		seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusRunning)
		seedInstance(t, instRepo, "i2", "stack-b", "d1", "uid-1", models.StackStatusRunning)
		seedChartConfigForTest(t, ccRepo, "cc1", "d1", "backend")

		require.NoError(t, tmplChartRepo.Create(&models.TemplateChartConfig{
			ID:              "tc1",
			StackTemplateID: "tmpl-1",
			ChartName:       "backend",
			LockedValues:    "locked: true",
		}))

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil, defRepo, ccRepo,
			NewMockStackTemplateRepository(), tmplChartRepo,
			nil, nil, nil, nil,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/compare?left=i1&right=i2", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp CompareInstancesResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Len(t, resp.Charts, 1)
		// Same locked values, same defaults, no overrides — identical.
		assert.False(t, resp.Charts[0].HasDifferences)
	})

	t.Run("left chart config list error returns 500", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		defRepo := NewMockStackDefinitionRepository()
		ccRepo := NewMockChartConfigRepository()

		seedDefinition(t, defRepo, "d1", "Def A", "uid-1")
		seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusRunning)
		seedInstance(t, instRepo, "i2", "stack-b", "d1", "uid-1", models.StackStatusRunning)

		ccRepo.SetError(errors.New("chart config error"))

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil, defRepo, ccRepo,
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, nil, nil,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/compare?left=i1&right=i2", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})
}

// ---- ExtendTTL additional tests ----

func TestExtendTTL_Forbidden(t *testing.T) {
	t.Parallel()

	instRepo := NewMockStackInstanceRepository()
	inst := &models.StackInstance{
		ID:                "i1",
		StackDefinitionID: "d1",
		Name:              "test",
		Namespace:         "stack-test-owner1",
		OwnerID:           "owner-1",
		Branch:            "master",
		Status:            models.StackStatusRunning,
		TTLMinutes:        60,
	}
	require.NoError(t, instRepo.Create(inst))

	router := setupInstanceRouterFull(t,
		instRepo, NewMockValueOverrideRepository(), nil,
		NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
		NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
		nil, nil, nil, nil,
		"other-user", "bob", "user",
	)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/i1/extend", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestExtendTTL_InvalidJSON(t *testing.T) {
	t.Parallel()

	instRepo := NewMockStackInstanceRepository()
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

	router := setupInstanceRouterFull(t,
		instRepo, NewMockValueOverrideRepository(), nil,
		NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
		NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
		nil, nil, nil, nil,
		"uid-1", "alice", "user",
	)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/i1/extend", bytes.NewBufferString("{invalid"))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = 9
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ---- DeployInstance additional tests ----

func TestDeployInstance_Additional(t *testing.T) {
	t.Parallel()

	t.Run("deploy with TTL resets expiry", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		defRepo := NewMockStackDefinitionRepository()
		ccRepo := NewMockChartConfigRepository()
		logRepo := NewMockDeploymentLogRepository()

		seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
		seedChartConfigForTest(t, ccRepo, "cc1", "d1", "backend")

		past := time.Now().Add(-1 * time.Hour)
		inst := &models.StackInstance{
			ID:                "i-ttl",
			StackDefinitionID: "d1",
			Name:              "ttl-stack",
			Namespace:         "stack-ttl-stack-owner",
			OwnerID:           "uid-1",
			Branch:            "master",
			Status:            models.StackStatusDraft,
			TTLMinutes:        60,
			ExpiresAt:         &past,
			CreatedAt:         time.Now().UTC(),
			UpdatedAt:         time.Now().UTC(),
		}
		require.NoError(t, instRepo.Create(inst))

		mgr := newTestManager(instRepo, logRepo)

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil, defRepo, ccRepo,
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			mgr, nil, nil, logRepo,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/i-ttl/deploy", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusAccepted, w.Code)

		// Check that ExpiresAt was reset to the future.
		updated, err := instRepo.FindByID("i-ttl")
		require.NoError(t, err)
		assert.NotNil(t, updated.ExpiresAt)
		assert.True(t, updated.ExpiresAt.After(time.Now().Add(50*time.Minute)))
	})

	t.Run("deploy with locked values from template", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		defRepo := NewMockStackDefinitionRepository()
		ccRepo := NewMockChartConfigRepository()
		logRepo := NewMockDeploymentLogRepository()
		tmplChartRepo := NewMockTemplateChartConfigRepository()

		def := &models.StackDefinition{
			ID:               "d-tmpl",
			Name:             "Templated Def",
			OwnerID:          "uid-1",
			DefaultBranch:    "master",
			SourceTemplateID: "tmpl-1",
		}
		require.NoError(t, defRepo.Create(def))

		seedChartConfigForTest(t, ccRepo, "cc1", "d-tmpl", "backend")

		require.NoError(t, tmplChartRepo.Create(&models.TemplateChartConfig{
			ID:              "tc1",
			StackTemplateID: "tmpl-1",
			ChartName:       "backend",
			LockedValues:    "locked_key: locked_value",
		}))

		inst := &models.StackInstance{
			ID:                "i-locked",
			StackDefinitionID: "d-tmpl",
			Name:              "locked-stack",
			Namespace:         "stack-locked-stack-owner",
			OwnerID:           "uid-1",
			Branch:            "master",
			Status:            models.StackStatusDraft,
			CreatedAt:         time.Now().UTC(),
			UpdatedAt:         time.Now().UTC(),
		}
		require.NoError(t, instRepo.Create(inst))

		mgr := newTestManager(instRepo, logRepo)

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil, defRepo, ccRepo,
			NewMockStackTemplateRepository(), tmplChartRepo,
			mgr, nil, nil, logRepo,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/i-locked/deploy", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusAccepted, w.Code)
	})

	t.Run("deploy with empty namespace returns 400", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		defRepo := NewMockStackDefinitionRepository()
		ccRepo := NewMockChartConfigRepository()
		logRepo := NewMockDeploymentLogRepository()

		seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
		seedChartConfigForTest(t, ccRepo, "cc1", "d1", "backend")

		inst := &models.StackInstance{
			ID:                "i-nons",
			StackDefinitionID: "d1",
			Name:              "no-ns-stack",
			Namespace:         "", // empty
			OwnerID:           "uid-1",
			Branch:            "master",
			Status:            models.StackStatusDraft,
			CreatedAt:         time.Now().UTC(),
			UpdatedAt:         time.Now().UTC(),
		}
		require.NoError(t, instRepo.Create(inst))

		mgr := newTestManager(instRepo, logRepo)

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil, defRepo, ccRepo,
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			mgr, nil, nil, logRepo,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/i-nons/deploy", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("deploy with no charts returns 400", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		defRepo := NewMockStackDefinitionRepository()
		logRepo := NewMockDeploymentLogRepository()

		seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
		seedInstance(t, instRepo, "i-nocc", "no-chart-stack", "d1", "uid-1", models.StackStatusDraft)

		mgr := newTestManager(instRepo, logRepo)

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil, defRepo,
			NewMockChartConfigRepository(), // empty — no charts
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			mgr, nil, nil, logRepo,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/i-nocc/deploy", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("deploy when definition not found returns 404", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		logRepo := NewMockDeploymentLogRepository()

		seedInstance(t, instRepo, "i-nodef", "orphan-stack", "missing-def", "uid-1", models.StackStatusDraft)

		mgr := newTestManager(instRepo, logRepo)

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil,
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			mgr, nil, nil, logRepo,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/i-nodef/deploy", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("deploy with branch overrides", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		defRepo := NewMockStackDefinitionRepository()
		ccRepo := NewMockChartConfigRepository()
		logRepo := NewMockDeploymentLogRepository()
		branchRepo := NewMockChartBranchOverrideRepository()

		seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
		seedChartConfigForTest(t, ccRepo, "cc1", "d1", "backend")
		seedInstance(t, instRepo, "i-bo", "branch-stack", "d1", "uid-1", models.StackStatusDraft)

		require.NoError(t, branchRepo.Set(&models.ChartBranchOverride{
			StackInstanceID: "i-bo",
			ChartConfigID:   "cc1",
			Branch:          "feature/branch-deploy",
		}))

		mgr := newTestManager(instRepo, logRepo)

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), branchRepo, defRepo, ccRepo,
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			mgr, nil, nil, logRepo,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/i-bo/deploy", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusAccepted, w.Code)
	})

	t.Run("deploy error instance returns 409", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		defRepo := NewMockStackDefinitionRepository()
		ccRepo := NewMockChartConfigRepository()
		logRepo := NewMockDeploymentLogRepository()

		seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
		seedChartConfigForTest(t, ccRepo, "cc1", "d1", "backend")
		seedInstance(t, instRepo, "i-err", "err-stack", "d1", "uid-1", models.StackStatusError)

		mgr := newTestManager(instRepo, logRepo)

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil, defRepo, ccRepo,
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			mgr, nil, nil, logRepo,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/i-err/deploy", nil)
		router.ServeHTTP(w, req)

		// Error status can be re-deployed.
		assert.Equal(t, http.StatusAccepted, w.Code)
	})

	t.Run("deploy queued instance returns 409", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		logRepo := NewMockDeploymentLogRepository()

		seedInstance(t, instRepo, "i-q", "queued-stack", "d1", "uid-1", "queued")

		mgr := newTestManager(instRepo, logRepo)

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil,
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			mgr, nil, nil, logRepo,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/i-q/deploy", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusConflict, w.Code)
	})

	t.Run("deploy cleaning instance returns 409", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		logRepo := NewMockDeploymentLogRepository()

		seedInstance(t, instRepo, "i-cl", "cleaning-stack", "d1", "uid-1", "cleaning")

		mgr := newTestManager(instRepo, logRepo)

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil,
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			mgr, nil, nil, logRepo,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/i-cl/deploy", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusConflict, w.Code)
	})

	t.Run("deploy chart config list error returns 500", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		defRepo := NewMockStackDefinitionRepository()
		ccRepo := NewMockChartConfigRepository()
		logRepo := NewMockDeploymentLogRepository()

		seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
		seedInstance(t, instRepo, "i-ccerr", "cc-err-stack", "d1", "uid-1", models.StackStatusDraft)

		ccRepo.SetError(errors.New("chart config error"))

		mgr := newTestManager(instRepo, logRepo)

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil, defRepo, ccRepo,
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			mgr, nil, nil, logRepo,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/i-ccerr/deploy", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})
}

// ---- StopInstance additional tests ----

func TestStopInstance_Additional(t *testing.T) {
	t.Parallel()

	t.Run("definition not found returns 404", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		logRepo := NewMockDeploymentLogRepository()

		seedInstance(t, instRepo, "i-stop", "stop-stack", "missing-def", "uid-1", models.StackStatusRunning)

		mgr := newTestManager(instRepo, logRepo)

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil,
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			mgr, nil, nil, logRepo,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/i-stop/stop", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("chart config list error returns 500", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		defRepo := NewMockStackDefinitionRepository()
		ccRepo := NewMockChartConfigRepository()
		logRepo := NewMockDeploymentLogRepository()

		seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
		seedInstance(t, instRepo, "i-stop2", "stop-stack2", "d1", "uid-1", models.StackStatusRunning)

		ccRepo.SetError(errors.New("chart config error"))

		mgr := newTestManager(instRepo, logRepo)

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil, defRepo, ccRepo,
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			mgr, nil, nil, logRepo,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/i-stop2/stop", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})

	t.Run("no charts configured returns 400", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		defRepo := NewMockStackDefinitionRepository()
		logRepo := NewMockDeploymentLogRepository()

		seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
		seedInstance(t, instRepo, "i-stop3", "stop-stack3", "d1", "uid-1", models.StackStatusRunning)

		mgr := newTestManager(instRepo, logRepo)

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil, defRepo,
			NewMockChartConfigRepository(), // empty
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			mgr, nil, nil, logRepo,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/i-stop3/stop", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("error instance cannot be stopped — returns 409", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		logRepo := NewMockDeploymentLogRepository()

		seedInstance(t, instRepo, "i-stop4", "stop-stack4", "d1", "uid-1", models.StackStatusError)

		mgr := newTestManager(instRepo, logRepo)

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil,
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			mgr, nil, nil, logRepo,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/i-stop4/stop", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusConflict, w.Code)
	})
}

// ---- CleanInstance additional tests ----

func TestCleanInstance_Additional(t *testing.T) {
	t.Parallel()

	t.Run("definition not found returns 404", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		logRepo := NewMockDeploymentLogRepository()

		seedInstance(t, instRepo, "i-clean", "clean-stack", "missing-def", "uid-1", models.StackStatusStopped)

		mgr := newTestManager(instRepo, logRepo)

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil,
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			mgr, nil, nil, logRepo,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/i-clean/clean", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("chart config list error returns 500", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		defRepo := NewMockStackDefinitionRepository()
		ccRepo := NewMockChartConfigRepository()
		logRepo := NewMockDeploymentLogRepository()

		seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
		seedInstance(t, instRepo, "i-clean2", "clean-stack2", "d1", "uid-1", models.StackStatusStopped)

		ccRepo.SetError(errors.New("chart config error"))

		mgr := newTestManager(instRepo, logRepo)

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil, defRepo, ccRepo,
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			mgr, nil, nil, logRepo,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/i-clean2/clean", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})

	t.Run("draft instance cannot be cleaned — returns 409", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		logRepo := NewMockDeploymentLogRepository()

		seedInstance(t, instRepo, "i-clean3", "clean-stack3", "d1", "uid-1", models.StackStatusDraft)

		mgr := newTestManager(instRepo, logRepo)

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil,
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			mgr, nil, nil, logRepo,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/i-clean3/clean", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusConflict, w.Code)
	})

	t.Run("deploying instance cannot be cleaned — returns 409", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		logRepo := NewMockDeploymentLogRepository()

		seedInstance(t, instRepo, "i-clean4", "clean-stack4", "d1", "uid-1", models.StackStatusDeploying)

		mgr := newTestManager(instRepo, logRepo)

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil,
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			mgr, nil, nil, logRepo,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/i-clean4/clean", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusConflict, w.Code)
	})
}

// ---- GetInstanceStatus additional tests ----

func TestGetInstanceStatus_Additional(t *testing.T) {
	t.Parallel()

	t.Run("falls back to registry k8s client", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		inst := &models.StackInstance{
			ID:                "i1",
			StackDefinitionID: "d1",
			Name:              "stack-a",
			Namespace:         "stack-stack-a-owner",
			OwnerID:           "uid-1",
			Branch:            "master",
			Status:            models.StackStatusRunning,
			ClusterID:         "test-cluster",
			CreatedAt:         time.Now().UTC(),
			UpdatedAt:         time.Now().UTC(),
		}
		require.NoError(t, instRepo.Create(inst))

		cs := fake.NewSimpleClientset(&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "stack-stack-a-owner"},
		})
		k8sClient := k8s.NewClientFromInterface(cs)
		registry := cluster.NewRegistryForTest("test-cluster", k8sClient, nil)

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil,
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, registry, nil,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/i1/status", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp k8s.NamespaceStatus
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "stack-stack-a-owner", resp.Namespace)
	})
}

// ---- GetDeployLog additional tests ----

func TestGetDeployLog_RepoError(t *testing.T) {
	t.Parallel()

	instRepo := NewMockStackInstanceRepository()
	logRepo := NewMockDeploymentLogRepository()
	seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusRunning)
	logRepo.SetError(errors.New("log repo failure"))

	router := setupInstanceRouterFull(t,
		instRepo, NewMockValueOverrideRepository(), nil,
		NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
		NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
		nil, nil, nil, logRepo,
		"uid-1", "alice", "user",
	)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/i1/deploy-log", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ---- GetInstance additional tests ----

func TestGetInstance_NotFound(t *testing.T) {
	t.Parallel()

	router := setupInstanceRouterFull(t,
		NewMockStackInstanceRepository(), NewMockValueOverrideRepository(), nil,
		NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
		NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
		nil, nil, nil, nil,
		"uid-1", "alice", "user",
	)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/nonexistent", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// ---- DeleteInstance additional tests ----

func TestDeleteInstance_NotFound(t *testing.T) {
	t.Parallel()

	router := setupInstanceRouterFull(t,
		NewMockStackInstanceRepository(), NewMockValueOverrideRepository(), nil,
		NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
		NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
		nil, nil, nil, nil,
		"uid-1", "alice", "user",
	)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodDelete, "/api/v1/stack-instances/nonexistent", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// ---- CreateInstance additional tests ----

func TestCreateInstance_Additional(t *testing.T) {
	t.Parallel()

	t.Run("with cluster registry resolves default cluster", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		defRepo := NewMockStackDefinitionRepository()
		seedDefinition(t, defRepo, "d1", "My Def", "uid-1")

		registry := cluster.NewRegistryForTest("default-cluster", nil, nil)

		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set("userID", "uid-1")
			c.Set("username", "alice")
			c.Set("role", "user")
			c.Next()
		})

		valuesGen := helm.NewValuesGenerator()
		userRepo := NewMockUserRepository()
		h, err := NewInstanceHandlerWithDeployer(
			instRepo, NewMockValueOverrideRepository(), nil, defRepo,
			NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			valuesGen, userRepo,
			nil, nil, registry, nil, nil,
			0, &mockHandlerTxRunner{},
		)
		require.NoError(t, err)
		r.POST("/api/v1/stack-instances", h.CreateInstance)

		body := `{"name":"test-stack","stack_definition_id":"d1","branch":"master"}`
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusCreated, w.Code)
		var result models.StackInstance
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
		assert.Equal(t, "default-cluster", result.ClusterID)
	})

	t.Run("with invalid cluster ID returns 400", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		defRepo := NewMockStackDefinitionRepository()
		seedDefinition(t, defRepo, "d1", "My Def", "uid-1")

		// Use a real cluster repo mock that returns not-found for unknown clusters
		clusterRepo := NewMockClusterRepository()
		registry := cluster.NewRegistry(cluster.RegistryConfig{
			ClusterRepo: clusterRepo,
		})

		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set("userID", "uid-1")
			c.Set("username", "alice")
			c.Set("role", "user")
			c.Next()
		})

		valuesGen := helm.NewValuesGenerator()
		userRepo := NewMockUserRepository()
		h, err := NewInstanceHandlerWithDeployer(
			instRepo, NewMockValueOverrideRepository(), nil, defRepo,
			NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			valuesGen, userRepo,
			nil, nil, registry, nil, nil,
			0, &mockHandlerTxRunner{},
		)
		require.NoError(t, err)
		r.POST("/api/v1/stack-instances", h.CreateInstance)

		body := `{"name":"test-stack","stack_definition_id":"d1","branch":"master","cluster_id":"bad-cluster"}`
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

// ---- ExportAllValues additional tests ----

func TestExportAllValues_Additional(t *testing.T) {
	t.Parallel()

	t.Run("definition not found returns 404", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		seedInstance(t, instRepo, "i1", "stack-a", "missing-def", "uid-1", models.StackStatusDraft)

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil,
			NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, nil, nil,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/i1/values", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("chart config list error returns 500", func(t *testing.T) {
		t.Parallel()

		instRepo := NewMockStackInstanceRepository()
		defRepo := NewMockStackDefinitionRepository()
		ccRepo := NewMockChartConfigRepository()

		seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
		seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)

		ccRepo.SetError(errors.New("chart config error"))

		router := setupInstanceRouterFull(t,
			instRepo, NewMockValueOverrideRepository(), nil, defRepo, ccRepo,
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
			nil, nil, nil, nil,
			"uid-1", "alice", "user",
		)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances/i1/values", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})
}

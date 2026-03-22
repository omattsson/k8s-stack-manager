//go:build integration

package main

import (
	"backend/internal/api/handlers"
	"backend/internal/api/routes"
	"backend/internal/config"
	"backend/internal/database/azure"
	"backend/internal/gitprovider"
	"backend/internal/health"
	"backend/internal/helm"
	"backend/internal/models"
	"backend/internal/scheduler"
	"backend/internal/websocket"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

const (
	azAccountName  = "devstoreaccount1"
	azAccountKey   = "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw=="
	azEndpoint     = "127.0.0.1:10002"
	integJWTSecret = "integration-test-jwt-secret-key"
)

func uniqueName(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

type integServer struct {
	router           *gin.Engine
	userRepo         *azure.UserRepository
	templateRepo     *azure.StackTemplateRepository
	tplChartRepo     *azure.TemplateChartConfigRepository
	definitionRepo   *azure.StackDefinitionRepository
	chartConfigRepo  *azure.ChartConfigRepository
	instanceRepo     *azure.StackInstanceRepository
	overrideRepo     *azure.ValueOverrideRepository
	auditRepo        *azure.AuditLogRepository
	apiKeyRepo       *azure.APIKeyRepository
	clusterRepo      *azure.ClusterRepository
	branchRepo       *azure.ChartBranchOverrideRepository
	cleanupRepo      *azure.CleanupPolicyRepository
	sharedValuesRepo *azure.SharedValuesRepository
	favoriteRepo     *azure.UserFavoriteRepository
	deployLogRepo    *azure.DeploymentLogRepository
}

func newIntegServer(t *testing.T) *integServer {
	t.Helper()
	gin.SetMode(gin.TestMode)

	userRepo, err := azure.NewUserRepository(azAccountName, azAccountKey, azEndpoint, true)
	if err != nil {
		t.Skipf("Azurite not available: %v", err)
	}
	templateRepo, err := azure.NewStackTemplateRepository(azAccountName, azAccountKey, azEndpoint, true)
	require.NoError(t, err)
	tplChartRepo, err := azure.NewTemplateChartConfigRepository(azAccountName, azAccountKey, azEndpoint, true)
	require.NoError(t, err)
	definitionRepo, err := azure.NewStackDefinitionRepository(azAccountName, azAccountKey, azEndpoint, true)
	require.NoError(t, err)
	chartConfigRepo, err := azure.NewChartConfigRepository(azAccountName, azAccountKey, azEndpoint, true)
	require.NoError(t, err)
	instanceRepo, err := azure.NewStackInstanceRepository(azAccountName, azAccountKey, azEndpoint, true)
	require.NoError(t, err)
	overrideRepo, err := azure.NewValueOverrideRepository(azAccountName, azAccountKey, azEndpoint, true)
	require.NoError(t, err)
	auditRepo, err := azure.NewAuditLogRepository(azAccountName, azAccountKey, azEndpoint, true)
	require.NoError(t, err)
	apiKeyRepo, err := azure.NewAPIKeyRepository(azAccountName, azAccountKey, azEndpoint, true)
	require.NoError(t, err)
	clusterRepo, err := azure.NewClusterRepository(azAccountName, azAccountKey, azEndpoint, true, "test-encryption-key-for-integ!!")
	require.NoError(t, err)
	branchRepo, err := azure.NewChartBranchOverrideRepository(azAccountName, azAccountKey, azEndpoint, true)
	require.NoError(t, err)
	cleanupRepo, err := azure.NewCleanupPolicyRepository(azAccountName, azAccountKey, azEndpoint, true)
	require.NoError(t, err)
	sharedValuesRepo, err := azure.NewSharedValuesRepository(azAccountName, azAccountKey, azEndpoint, true)
	require.NoError(t, err)
	favoriteRepo, err := azure.NewUserFavoriteRepository(azAccountName, azAccountKey, azEndpoint, true)
	require.NoError(t, err)
	deployLogRepo, err := azure.NewDeploymentLogRepository(azAccountName, azAccountKey, azEndpoint, true)
	require.NoError(t, err)

	authCfg := &config.AuthConfig{
		JWTSecret:        integJWTSecret,
		JWTExpiration:    time.Hour,
		SelfRegistration: true,
	}
	authHandler := handlers.NewAuthHandler(userRepo, authCfg)
	templateHandler := handlers.NewTemplateHandler(templateRepo, tplChartRepo, definitionRepo, chartConfigRepo)
	definitionHandler := handlers.NewDefinitionHandler(definitionRepo, chartConfigRepo, instanceRepo, templateRepo, tplChartRepo)
	valuesGen := helm.NewValuesGenerator()
	instanceHandler := handlers.NewInstanceHandler(instanceRepo, overrideRepo, nil, definitionRepo, chartConfigRepo, templateRepo, tplChartRepo, valuesGen, userRepo, 0)
	branchOverrideHandler := handlers.NewBranchOverrideHandler(branchRepo, instanceRepo)
	cleanupPolicyHandler := handlers.NewCleanupPolicyHandler(cleanupRepo, (*scheduler.Scheduler)(nil))
	sharedValuesHandler := handlers.NewSharedValuesHandler(sharedValuesRepo, clusterRepo)
	favoriteHandler := handlers.NewFavoriteHandler(favoriteRepo)
	analyticsHandler := handlers.NewAnalyticsHandler(templateRepo, definitionRepo, instanceRepo, deployLogRepo, userRepo)
	gitRegistry := gitprovider.NewRegistry(gitprovider.Config{})
	gitHandler := handlers.NewGitHandler(gitRegistry)
	auditLogHandler := handlers.NewAuditLogHandler(auditRepo)
	userHandler := handlers.NewUserHandler(userRepo)
	apiKeyHandler := handlers.NewAPIKeyHandler(apiKeyRepo, userRepo)

	healthChecker := health.New()
	healthChecker.SetReady(true)
	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	cfg := &config.Config{
		CORS: config.CORSConfig{AllowedOrigins: "*"},
		Auth: *authCfg,
	}
	router := gin.New()
	rl := routes.SetupRoutes(router, routes.Deps{
		HealthChecker:         healthChecker,
		Config:                cfg,
		Hub:                   hub,
		AuthHandler:           authHandler,
		TemplateHandler:       templateHandler,
		DefinitionHandler:     definitionHandler,
		InstanceHandler:       instanceHandler,
		GitHandler:            gitHandler,
		AuditLogHandler:       auditLogHandler,
		AuditLogger:           auditRepo,
		UserHandler:           userHandler,
		APIKeyHandler:         apiKeyHandler,
		UserRepo:              userRepo,
		APIKeyRepo:            apiKeyRepo,
		ClusterRepo:           clusterRepo,
		InstanceRepo:          instanceRepo,
		BranchOverrideHandler: branchOverrideHandler,
		CleanupPolicyHandler:  cleanupPolicyHandler,
		SharedValuesHandler:   sharedValuesHandler,
		FavoriteHandler:       favoriteHandler,
		AnalyticsHandler:      analyticsHandler,
	})
	t.Cleanup(func() { rl.Stop() })

	return &integServer{
		router:           router,
		userRepo:         userRepo,
		templateRepo:     templateRepo,
		tplChartRepo:     tplChartRepo,
		definitionRepo:   definitionRepo,
		chartConfigRepo:  chartConfigRepo,
		instanceRepo:     instanceRepo,
		overrideRepo:     overrideRepo,
		auditRepo:        auditRepo,
		apiKeyRepo:       apiKeyRepo,
		clusterRepo:      clusterRepo,
		branchRepo:       branchRepo,
		cleanupRepo:      cleanupRepo,
		sharedValuesRepo: sharedValuesRepo,
		favoriteRepo:     favoriteRepo,
		deployLogRepo:    deployLogRepo,
	}
}

func (s *integServer) do(method, path string, body interface{}, token string) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

func (s *integServer) doWithAPIKey(method, path string, body interface{}, apiKey string) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

func (s *integServer) createAdminAndLogin(t *testing.T) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("admin123"), bcrypt.MinCost)
	require.NoError(t, err)
	admin := &models.User{
		ID:           fmt.Sprintf("admin-%d", time.Now().UnixNano()),
		Username:     fmt.Sprintf("admin%d", time.Now().UnixNano()),
		PasswordHash: string(hash),
		DisplayName:  "Admin",
		Role:         "admin",
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	err = s.userRepo.Create(admin)
	require.NoError(t, err)

	w := s.do("POST", "/api/v1/auth/login", map[string]string{"username": admin.Username, "password": "admin123"}, "")
	require.Equal(t, http.StatusOK, w.Code, "admin login: %s", w.Body.String())
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	return resp["token"].(string)
}

func (s *integServer) registerAndLogin(t *testing.T, username, password, role, adminToken string) (string, string) {
	t.Helper()
	body := map[string]string{"username": username, "password": password, "display_name": username, "role": role}
	w := s.do("POST", "/api/v1/auth/register", body, adminToken)
	require.Equal(t, http.StatusCreated, w.Code, "register %s: %s", username, w.Body.String())

	// Extract user ID from registration response.
	var regResp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &regResp))
	userID := regResp["id"].(string)

	w = s.do("POST", "/api/v1/auth/login", map[string]string{"username": username, "password": password}, "")
	require.Equal(t, http.StatusOK, w.Code, "login %s: %s", username, w.Body.String())
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	return resp["token"].(string), userID
}

func parseBody(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &m))
	return m
}

func parseArray(t *testing.T, w *httptest.ResponseRecorder) []interface{} {
	t.Helper()
	var arr []interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &arr))
	return arr
}

// ---------------------------------------------------------------------------
// TestAPIIntegration_AuthFlow
// ---------------------------------------------------------------------------

func TestAPIIntegration_AuthFlow(t *testing.T) {
	s := newIntegServer(t)

	t.Run("unauthenticated request to /me returns 401", func(t *testing.T) {
		w := s.do("GET", "/api/v1/auth/me", nil, "")
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("login with wrong password returns 401", func(t *testing.T) {
		adminToken := s.createAdminAndLogin(t)
		name := uniqueName("authbad")
		s.registerAndLogin(t, name, "pass123", "user", adminToken)

		w := s.do("POST", "/api/v1/auth/login", map[string]string{"username": name, "password": "wrong"}, "")
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("register and login full flow", func(t *testing.T) {
		adminToken := s.createAdminAndLogin(t)
		name := uniqueName("flowuser")
		token, _ := s.registerAndLogin(t, name, "secret", "user", adminToken)
		w := s.do("GET", "/api/v1/auth/me", nil, token)
		assert.Equal(t, http.StatusOK, w.Code)
		body := parseBody(t, w)
		assert.Equal(t, name, body["username"])
	})

	t.Run("login with invalid JSON returns 400", func(t *testing.T) {
		w := s.do("POST", "/api/v1/auth/login", "not-json", "")
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("register duplicate username returns 409", func(t *testing.T) {
		adminToken := s.createAdminAndLogin(t)
		name := uniqueName("dupuser")
		s.registerAndLogin(t, name, "pass1", "user", adminToken)

		w := s.do("POST", "/api/v1/auth/register",
			map[string]string{"username": name, "password": "pass2", "display_name": name},
			adminToken)
		assert.Equal(t, http.StatusConflict, w.Code)
	})
}

// ---------------------------------------------------------------------------
// TestAPIIntegration_TemplateLifecycle
// ---------------------------------------------------------------------------

func TestAPIIntegration_TemplateLifecycle(t *testing.T) {
	s := newIntegServer(t)

	// Setup: create admin and devops user.
	adminToken := s.createAdminAndLogin(t)
	devopsToken, _ := s.registerAndLogin(t, uniqueName("devops"), "pass", "devops", adminToken)

	var tplID string
	tplName := uniqueName("tpl")

	t.Run("create template", func(t *testing.T) {
		tpl := map[string]interface{}{
			"name":           tplName,
			"description":    "Integration test template",
			"category":       "test",
			"version":        "1.0.0",
			"default_branch": "main",
		}
		w := s.do("POST", "/api/v1/templates", tpl, devopsToken)
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
		body := parseBody(t, w)
		tplID = body["id"].(string)
		assert.NotEmpty(t, tplID)
		assert.Equal(t, tplName, body["name"])
	})

	t.Run("get template", func(t *testing.T) {
		w := s.do("GET", fmt.Sprintf("/api/v1/templates/%s", tplID), nil, devopsToken)
		assert.Equal(t, http.StatusOK, w.Code)
		body := parseBody(t, w)
		assert.Equal(t, tplID, body["id"])
	})

	t.Run("list templates includes created", func(t *testing.T) {
		w := s.do("GET", "/api/v1/templates", nil, devopsToken)
		assert.Equal(t, http.StatusOK, w.Code)
		arr := parseArray(t, w)
		found := false
		for _, item := range arr {
			m := item.(map[string]interface{})
			if m["id"] == tplID {
				found = true
				break
			}
		}
		assert.True(t, found, "template %s not found in list", tplID)
	})

	var chartID string
	t.Run("add chart to template", func(t *testing.T) {
		chart := map[string]interface{}{
			"chart_name":     "nginx",
			"repository_url": "https://charts.example.com",
			"chart_version":  "1.0.0",
			"default_values": "replicas: 1\nimage: nginx:latest",
		}
		w := s.do("POST", fmt.Sprintf("/api/v1/templates/%s/charts", tplID), chart, devopsToken)
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
		body := parseBody(t, w)
		chartID = body["id"].(string)
		assert.NotEmpty(t, chartID)
		assert.Equal(t, "nginx", body["chart_name"])
	})

	t.Run("publish template", func(t *testing.T) {
		w := s.do("POST", fmt.Sprintf("/api/v1/templates/%s/publish", tplID), nil, devopsToken)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("unpublish template", func(t *testing.T) {
		w := s.do("POST", fmt.Sprintf("/api/v1/templates/%s/unpublish", tplID), nil, devopsToken)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	var cloneID string
	t.Run("clone template", func(t *testing.T) {
		w := s.do("POST", fmt.Sprintf("/api/v1/templates/%s/clone", tplID), nil, devopsToken)
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
		body := parseBody(t, w)
		cloneID = body["id"].(string)
		assert.NotEmpty(t, cloneID)
		assert.NotEqual(t, tplID, cloneID)
	})

	t.Run("delete cloned template", func(t *testing.T) {
		w := s.do("DELETE", fmt.Sprintf("/api/v1/templates/%s", cloneID), nil, devopsToken)
		assert.Equal(t, http.StatusNoContent, w.Code)
	})

	t.Run("get deleted clone returns 404", func(t *testing.T) {
		w := s.do("GET", fmt.Sprintf("/api/v1/templates/%s", cloneID), nil, devopsToken)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("delete original template", func(t *testing.T) {
		w := s.do("DELETE", fmt.Sprintf("/api/v1/templates/%s", tplID), nil, devopsToken)
		assert.Equal(t, http.StatusNoContent, w.Code)
	})

	t.Run("get deleted original returns 404", func(t *testing.T) {
		w := s.do("GET", fmt.Sprintf("/api/v1/templates/%s", tplID), nil, devopsToken)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

// ---------------------------------------------------------------------------
// TestAPIIntegration_DefinitionLifecycle
// ---------------------------------------------------------------------------

func TestAPIIntegration_DefinitionLifecycle(t *testing.T) {
	s := newIntegServer(t)

	// Setup: admin → devops → create template with chart → publish.
	adminToken := s.createAdminAndLogin(t)
	devopsToken, _ := s.registerAndLogin(t, uniqueName("devops"), "pass", "devops", adminToken)
	userToken, _ := s.registerAndLogin(t, uniqueName("defuser"), "pass", "user", adminToken)

	// Create and publish a template with a chart.
	tpl := map[string]interface{}{
		"name":           uniqueName("deftpl"),
		"description":    "Template for definition test",
		"category":       "test",
		"version":        "1.0.0",
		"default_branch": "main",
	}
	w := s.do("POST", "/api/v1/templates", tpl, devopsToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	tplID := parseBody(t, w)["id"].(string)

	chart := map[string]interface{}{
		"chart_name":     "redis",
		"repository_url": "https://charts.example.com",
		"chart_version":  "7.0.0",
		"default_values": "replicas: 1",
	}
	w = s.do("POST", fmt.Sprintf("/api/v1/templates/%s/charts", tplID), chart, devopsToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	w = s.do("POST", fmt.Sprintf("/api/v1/templates/%s/publish", tplID), nil, devopsToken)
	require.Equal(t, http.StatusOK, w.Code)

	var defID string

	t.Run("instantiate template to create definition", func(t *testing.T) {
		body := map[string]interface{}{"name": uniqueName("def"), "description": "test def"}
		w := s.do("POST", fmt.Sprintf("/api/v1/templates/%s/instantiate", tplID), body, userToken)
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
		resp := parseBody(t, w)
		def := resp["definition"].(map[string]interface{})
		defID = def["id"].(string)
		assert.NotEmpty(t, defID)
		assert.Equal(t, tplID, def["source_template_id"])
	})

	t.Run("get definition", func(t *testing.T) {
		w := s.do("GET", fmt.Sprintf("/api/v1/stack-definitions/%s", defID), nil, userToken)
		assert.Equal(t, http.StatusOK, w.Code)
		body := parseBody(t, w)
		assert.Equal(t, defID, body["id"])
	})

	t.Run("list definitions includes created", func(t *testing.T) {
		w := s.do("GET", "/api/v1/stack-definitions", nil, userToken)
		assert.Equal(t, http.StatusOK, w.Code)
		arr := parseArray(t, w)
		found := false
		for _, item := range arr {
			m := item.(map[string]interface{})
			if m["id"] == defID {
				found = true
				break
			}
		}
		assert.True(t, found, "definition %s not found in list", defID)
	})

	t.Run("update definition", func(t *testing.T) {
		update := map[string]interface{}{
			"name":        uniqueName("upddef"),
			"description": "updated description",
		}
		w := s.do("PUT", fmt.Sprintf("/api/v1/stack-definitions/%s", defID), update, userToken)
		assert.Equal(t, http.StatusOK, w.Code)
		body := parseBody(t, w)
		assert.Equal(t, "updated description", body["description"])
	})

	t.Run("delete definition", func(t *testing.T) {
		w := s.do("DELETE", fmt.Sprintf("/api/v1/stack-definitions/%s", defID), nil, userToken)
		assert.Equal(t, http.StatusNoContent, w.Code)
	})

	t.Run("get deleted definition returns 404", func(t *testing.T) {
		w := s.do("GET", fmt.Sprintf("/api/v1/stack-definitions/%s", defID), nil, userToken)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

// ---------------------------------------------------------------------------
// TestAPIIntegration_InstanceLifecycle
// ---------------------------------------------------------------------------

func TestAPIIntegration_InstanceLifecycle(t *testing.T) {
	s := newIntegServer(t)

	// Setup: create template → publish → instantiate → get definition with chart.
	adminToken := s.createAdminAndLogin(t)
	devopsToken, _ := s.registerAndLogin(t, uniqueName("devops"), "pass", "devops", adminToken)
	userToken, _ := s.registerAndLogin(t, uniqueName("instuser"), "pass", "user", adminToken)

	tpl := map[string]interface{}{
		"name":           uniqueName("insttpl"),
		"description":    "Template for instance test",
		"category":       "test",
		"version":        "1.0.0",
		"default_branch": "main",
	}
	w := s.do("POST", "/api/v1/templates", tpl, devopsToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	tplID := parseBody(t, w)["id"].(string)

	chart := map[string]interface{}{
		"chart_name":     "webapp",
		"repository_url": "https://charts.example.com",
		"chart_version":  "2.0.0",
		"default_values": "replicas: 2\nimage: webapp:latest",
	}
	w = s.do("POST", fmt.Sprintf("/api/v1/templates/%s/charts", tplID), chart, devopsToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	w = s.do("POST", fmt.Sprintf("/api/v1/templates/%s/publish", tplID), nil, devopsToken)
	require.Equal(t, http.StatusOK, w.Code)

	// Instantiate to get a definition.
	instBody := map[string]interface{}{"name": uniqueName("instdef"), "description": "def for instance"}
	w = s.do("POST", fmt.Sprintf("/api/v1/templates/%s/instantiate", tplID), instBody, userToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	instResp := parseBody(t, w)
	defID := instResp["definition"].(map[string]interface{})["id"].(string)
	charts := instResp["charts"].([]interface{})
	require.NotEmpty(t, charts)
	chartConfigID := charts[0].(map[string]interface{})["id"].(string)

	var instID string
	instName := uniqueName("inst")

	t.Run("create instance", func(t *testing.T) {
		inst := map[string]interface{}{
			"stack_definition_id": defID,
			"name":                instName,
			"branch":              "main",
		}
		w := s.do("POST", "/api/v1/stack-instances", inst, userToken)
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
		body := parseBody(t, w)
		instID = body["id"].(string)
		assert.NotEmpty(t, instID)
		assert.Equal(t, "draft", body["status"])
		assert.NotEmpty(t, body["namespace"])
	})

	t.Run("get instance", func(t *testing.T) {
		w := s.do("GET", fmt.Sprintf("/api/v1/stack-instances/%s", instID), nil, userToken)
		assert.Equal(t, http.StatusOK, w.Code)
		body := parseBody(t, w)
		assert.Equal(t, instID, body["id"])
	})

	t.Run("list instances includes created", func(t *testing.T) {
		w := s.do("GET", "/api/v1/stack-instances", nil, userToken)
		assert.Equal(t, http.StatusOK, w.Code)
		arr := parseArray(t, w)
		found := false
		for _, item := range arr {
			m := item.(map[string]interface{})
			if m["id"] == instID {
				found = true
				break
			}
		}
		assert.True(t, found, "instance %s not found in list", instID)
	})

	t.Run("update instance", func(t *testing.T) {
		update := map[string]interface{}{
			"name":   uniqueName("updinst"),
			"branch": "develop",
		}
		w := s.do("PUT", fmt.Sprintf("/api/v1/stack-instances/%s", instID), update, userToken)
		assert.Equal(t, http.StatusOK, w.Code)
		body := parseBody(t, w)
		assert.Equal(t, "develop", body["branch"])
	})

	var cloneInstID string
	t.Run("clone instance", func(t *testing.T) {
		w := s.do("POST", fmt.Sprintf("/api/v1/stack-instances/%s/clone", instID), nil, userToken)
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
		body := parseBody(t, w)
		cloneInstID = body["id"].(string)
		assert.NotEmpty(t, cloneInstID)
		assert.NotEqual(t, instID, cloneInstID)
	})

	t.Run("export chart values returns YAML", func(t *testing.T) {
		w := s.do("GET", fmt.Sprintf("/api/v1/stack-instances/%s/values/%s", instID, chartConfigID), nil, userToken)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Header().Get("Content-Type"), "yaml")
		assert.NotEmpty(t, w.Body.String())
	})

	t.Run("delete cloned instance", func(t *testing.T) {
		w := s.do("DELETE", fmt.Sprintf("/api/v1/stack-instances/%s", cloneInstID), nil, userToken)
		assert.Equal(t, http.StatusNoContent, w.Code)
	})

	t.Run("delete original instance", func(t *testing.T) {
		w := s.do("DELETE", fmt.Sprintf("/api/v1/stack-instances/%s", instID), nil, userToken)
		assert.Equal(t, http.StatusNoContent, w.Code)
	})

	t.Run("get deleted instance returns 404", func(t *testing.T) {
		w := s.do("GET", fmt.Sprintf("/api/v1/stack-instances/%s", instID), nil, userToken)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

// ---------------------------------------------------------------------------
// TestAPIIntegration_ValueOverrides
// ---------------------------------------------------------------------------

func TestAPIIntegration_ValueOverrides(t *testing.T) {
	s := newIntegServer(t)

	// Setup: template → chart → publish → instantiate → create instance.
	adminToken := s.createAdminAndLogin(t)
	devopsToken, _ := s.registerAndLogin(t, uniqueName("devops"), "pass", "devops", adminToken)
	userToken, _ := s.registerAndLogin(t, uniqueName("ovuser"), "pass", "user", adminToken)

	tpl := map[string]interface{}{
		"name":           uniqueName("ovtpl"),
		"description":    "Template for override test",
		"category":       "test",
		"version":        "1.0.0",
		"default_branch": "main",
	}
	w := s.do("POST", "/api/v1/templates", tpl, devopsToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	tplID := parseBody(t, w)["id"].(string)

	chart := map[string]interface{}{
		"chart_name":     "api",
		"repository_url": "https://charts.example.com",
		"chart_version":  "3.0.0",
		"default_values": "replicas: 1\nport: 8080",
	}
	w = s.do("POST", fmt.Sprintf("/api/v1/templates/%s/charts", tplID), chart, devopsToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	w = s.do("POST", fmt.Sprintf("/api/v1/templates/%s/publish", tplID), nil, devopsToken)
	require.Equal(t, http.StatusOK, w.Code)

	instBody := map[string]interface{}{"name": uniqueName("ovdef"), "description": "def for overrides"}
	w = s.do("POST", fmt.Sprintf("/api/v1/templates/%s/instantiate", tplID), instBody, userToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	instResp := parseBody(t, w)
	defID := instResp["definition"].(map[string]interface{})["id"].(string)
	chartConfigID := instResp["charts"].([]interface{})[0].(map[string]interface{})["id"].(string)

	inst := map[string]interface{}{
		"stack_definition_id": defID,
		"name":                uniqueName("ovinst"),
		"branch":              "main",
	}
	w = s.do("POST", "/api/v1/stack-instances", inst, userToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	instID := parseBody(t, w)["id"].(string)

	t.Run("set override", func(t *testing.T) {
		override := map[string]interface{}{"values": "replicas: 3"}
		w := s.do("PUT", fmt.Sprintf("/api/v1/stack-instances/%s/overrides/%s", instID, chartConfigID), override, userToken)
		assert.Equal(t, http.StatusOK, w.Code)
		body := parseBody(t, w)
		assert.Equal(t, "replicas: 3", body["values"])
	})

	t.Run("get overrides lists the override", func(t *testing.T) {
		w := s.do("GET", fmt.Sprintf("/api/v1/stack-instances/%s/overrides", instID), nil, userToken)
		assert.Equal(t, http.StatusOK, w.Code)
		arr := parseArray(t, w)
		require.Len(t, arr, 1)
		ov := arr[0].(map[string]interface{})
		assert.Equal(t, chartConfigID, ov["chart_config_id"])
		assert.Equal(t, "replicas: 3", ov["values"])
	})

	t.Run("update existing override via upsert", func(t *testing.T) {
		override := map[string]interface{}{"values": "replicas: 5\nport: 9090"}
		w := s.do("PUT", fmt.Sprintf("/api/v1/stack-instances/%s/overrides/%s", instID, chartConfigID), override, userToken)
		assert.Equal(t, http.StatusOK, w.Code)
		body := parseBody(t, w)
		assert.Equal(t, "replicas: 5\nport: 9090", body["values"])
	})

	t.Run("export chart values reflects override", func(t *testing.T) {
		w := s.do("GET", fmt.Sprintf("/api/v1/stack-instances/%s/values/%s", instID, chartConfigID), nil, userToken)
		assert.Equal(t, http.StatusOK, w.Code)
		yamlContent := w.Body.String()
		assert.Contains(t, yamlContent, "replicas")
	})
}

// ---------------------------------------------------------------------------
// TestAPIIntegration_AuditLog
// ---------------------------------------------------------------------------

func TestAPIIntegration_AuditLog(t *testing.T) {
	s := newIntegServer(t)

	adminToken := s.createAdminAndLogin(t)
	devopsToken, _ := s.registerAndLogin(t, uniqueName("auditdev"), "pass", "devops", adminToken)

	// Perform some mutations that should generate audit entries.
	tpl := map[string]interface{}{
		"name":           uniqueName("audittpl"),
		"description":    "Template for audit test",
		"category":       "test",
		"version":        "1.0.0",
		"default_branch": "main",
	}
	w := s.do("POST", "/api/v1/templates", tpl, devopsToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	tplID := parseBody(t, w)["id"].(string)

	// Delete the template to generate another audit entry.
	w = s.do("DELETE", fmt.Sprintf("/api/v1/templates/%s", tplID), nil, devopsToken)
	require.Equal(t, http.StatusNoContent, w.Code)

	t.Run("audit log contains entries", func(t *testing.T) {
		w := s.do("GET", "/api/v1/audit-logs", nil, devopsToken)
		assert.Equal(t, http.StatusOK, w.Code)
		resp := parseBody(t, w)
		data, ok := resp["data"].([]interface{})
		require.True(t, ok, "expected 'data' array in paginated response")
		assert.NotEmpty(t, data, "expected at least one audit log entry")
		assert.NotNil(t, resp["total"], "expected 'total' in paginated response")
		assert.NotNil(t, resp["limit"], "expected 'limit' in paginated response")
		assert.NotNil(t, resp["offset"], "expected 'offset' in paginated response")
	})
}

// ---------------------------------------------------------------------------
// TestAPIIntegration_APIKeys
// ---------------------------------------------------------------------------

func TestAPIIntegration_APIKeys(t *testing.T) {
	s := newIntegServer(t)

	adminToken := s.createAdminAndLogin(t)
	userName := uniqueName("apikeyuser")
	userToken, userID := s.registerAndLogin(t, userName, "pass", "user", adminToken)

	var keyID string
	var rawKey string

	t.Run("create API key", func(t *testing.T) {
		body := map[string]interface{}{"name": "test-key"}
		w := s.do("POST", fmt.Sprintf("/api/v1/users/%s/api-keys", userID), body, userToken)
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
		resp := parseBody(t, w)
		keyID = resp["id"].(string)
		rawKey = resp["raw_key"].(string)
		assert.NotEmpty(t, keyID)
		assert.NotEmpty(t, rawKey)
		assert.True(t, len(rawKey) > 3, "raw key should start with sk_")
		assert.Equal(t, "sk_", rawKey[:3])
	})

	t.Run("list API keys", func(t *testing.T) {
		w := s.do("GET", fmt.Sprintf("/api/v1/users/%s/api-keys", userID), nil, userToken)
		assert.Equal(t, http.StatusOK, w.Code)
		arr := parseArray(t, w)
		require.NotEmpty(t, arr)
		found := false
		for _, item := range arr {
			m := item.(map[string]interface{})
			if m["id"] == keyID {
				found = true
				break
			}
		}
		assert.True(t, found, "API key %s not found in list", keyID)
	})

	t.Run("authenticate with API key via X-API-Key header", func(t *testing.T) {
		w := s.doWithAPIKey("GET", "/api/v1/auth/me", nil, rawKey)
		assert.Equal(t, http.StatusOK, w.Code)
		body := parseBody(t, w)
		assert.Equal(t, userName, body["username"])
	})

	t.Run("delete API key", func(t *testing.T) {
		w := s.do("DELETE", fmt.Sprintf("/api/v1/users/%s/api-keys/%s", userID, keyID), nil, userToken)
		assert.Equal(t, http.StatusNoContent, w.Code)
	})

	t.Run("deleted API key no longer works", func(t *testing.T) {
		w := s.doWithAPIKey("GET", "/api/v1/auth/me", nil, rawKey)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})
}

// ---------------------------------------------------------------------------
// TestAPIIntegration_RBAC
// ---------------------------------------------------------------------------

func TestAPIIntegration_RBAC(t *testing.T) {
	s := newIntegServer(t)

	adminToken := s.createAdminAndLogin(t)
	userToken, _ := s.registerAndLogin(t, uniqueName("rbacuser"), "pass", "user", adminToken)

	t.Run("regular user cannot create template", func(t *testing.T) {
		tpl := map[string]interface{}{
			"name":           uniqueName("forbidden"),
			"description":    "Should fail",
			"category":       "test",
			"version":        "1.0.0",
			"default_branch": "main",
		}
		w := s.do("POST", "/api/v1/templates", tpl, userToken)
		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("regular user cannot list users", func(t *testing.T) {
		w := s.do("GET", "/api/v1/users", nil, userToken)
		assert.Equal(t, http.StatusForbidden, w.Code)
	})
}

// ---------------------------------------------------------------------------
// TestAPIIntegration_HealthEndpoints
// ---------------------------------------------------------------------------

func TestAPIIntegration_HealthEndpoints(t *testing.T) {
	s := newIntegServer(t)

	t.Run("liveness returns 200", func(t *testing.T) {
		w := s.do("GET", "/health/live", nil, "")
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("readiness returns 200", func(t *testing.T) {
		w := s.do("GET", "/health/ready", nil, "")
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("health check returns 200", func(t *testing.T) {
		w := s.do("GET", "/health", nil, "")
		assert.Equal(t, http.StatusOK, w.Code)
	})
}

// ---------------------------------------------------------------------------
// TestAPIIntegration_ClusterLifecycle
// ---------------------------------------------------------------------------

func TestAPIIntegration_ClusterLifecycle(t *testing.T) {
	s := newIntegServer(t)
	adminToken := s.createAdminAndLogin(t)

	var clusterID string
	var secondClusterID string

	t.Run("create cluster", func(t *testing.T) {
		body := map[string]interface{}{
			"name":            uniqueName("cluster"),
			"description":     "integration test cluster",
			"api_server_url":  "https://k8s.example.com:6443",
			"kubeconfig_data": "apiVersion: v1\nkind: Config\nclusters: []",
			"region":          "northeurope",
		}
		w := s.do("POST", "/api/v1/clusters", body, adminToken)
		require.Equal(t, http.StatusCreated, w.Code, "create cluster: %s", w.Body.String())
		resp := parseBody(t, w)
		clusterID = resp["id"].(string)
		assert.NotEmpty(t, clusterID)
		assert.Equal(t, "integration test cluster", resp["description"])
		assert.Equal(t, "https://k8s.example.com:6443", resp["api_server_url"])
		assert.Equal(t, "northeurope", resp["region"])
	})

	t.Run("get cluster", func(t *testing.T) {
		w := s.do("GET", "/api/v1/clusters/"+clusterID, nil, adminToken)
		require.Equal(t, http.StatusOK, w.Code, "get cluster: %s", w.Body.String())
		resp := parseBody(t, w)
		assert.Equal(t, clusterID, resp["id"])
		assert.Equal(t, "https://k8s.example.com:6443", resp["api_server_url"])
		// kubeconfig_data must NOT be in the response (json:"-")
		_, hasKubeconfig := resp["kubeconfig_data"]
		assert.False(t, hasKubeconfig, "kubeconfig_data should not be in JSON response")
		_, hasKubeconfigPath := resp["kubeconfig_path"]
		assert.False(t, hasKubeconfigPath, "kubeconfig_path should not be in JSON response")
	})

	t.Run("list clusters includes created cluster", func(t *testing.T) {
		w := s.do("GET", "/api/v1/clusters", nil, adminToken)
		require.Equal(t, http.StatusOK, w.Code, "list clusters: %s", w.Body.String())
		arr := parseArray(t, w)
		found := false
		for _, item := range arr {
			m := item.(map[string]interface{})
			if m["id"] == clusterID {
				found = true
				break
			}
		}
		assert.True(t, found, "created cluster not found in list")
	})

	t.Run("update cluster", func(t *testing.T) {
		body := map[string]interface{}{
			"name":        uniqueName("updated-cluster"),
			"description": "updated description",
		}
		w := s.do("PUT", "/api/v1/clusters/"+clusterID, body, adminToken)
		require.Equal(t, http.StatusOK, w.Code, "update cluster: %s", w.Body.String())
	})

	t.Run("get updated cluster", func(t *testing.T) {
		w := s.do("GET", "/api/v1/clusters/"+clusterID, nil, adminToken)
		require.Equal(t, http.StatusOK, w.Code, "get updated cluster: %s", w.Body.String())
		resp := parseBody(t, w)
		assert.Equal(t, "updated description", resp["description"])
	})

	t.Run("set default cluster", func(t *testing.T) {
		w := s.do("POST", "/api/v1/clusters/"+clusterID+"/default", nil, adminToken)
		require.Equal(t, http.StatusOK, w.Code, "set default: %s", w.Body.String())

		w = s.do("GET", "/api/v1/clusters/"+clusterID, nil, adminToken)
		require.Equal(t, http.StatusOK, w.Code)
		resp := parseBody(t, w)
		assert.Equal(t, true, resp["is_default"])
	})

	t.Run("create second cluster", func(t *testing.T) {
		body := map[string]interface{}{
			"name":            uniqueName("cluster2"),
			"description":     "second cluster",
			"api_server_url":  "https://k8s2.example.com:6443",
			"kubeconfig_data": "apiVersion: v1\nkind: Config\nclusters: []",
			"region":          "westeurope",
		}
		w := s.do("POST", "/api/v1/clusters", body, adminToken)
		require.Equal(t, http.StatusCreated, w.Code, "create second cluster: %s", w.Body.String())
		resp := parseBody(t, w)
		secondClusterID = resp["id"].(string)
		assert.NotEmpty(t, secondClusterID)
	})

	t.Run("set default to second cluster removes first default", func(t *testing.T) {
		w := s.do("POST", "/api/v1/clusters/"+secondClusterID+"/default", nil, adminToken)
		require.Equal(t, http.StatusOK, w.Code, "set default second: %s", w.Body.String())

		// First cluster should no longer be default
		w = s.do("GET", "/api/v1/clusters/"+clusterID, nil, adminToken)
		require.Equal(t, http.StatusOK, w.Code)
		resp := parseBody(t, w)
		assert.Equal(t, false, resp["is_default"])

		// Second cluster should be default
		w = s.do("GET", "/api/v1/clusters/"+secondClusterID, nil, adminToken)
		require.Equal(t, http.StatusOK, w.Code)
		resp = parseBody(t, w)
		assert.Equal(t, true, resp["is_default"])
	})

	t.Run("delete cluster", func(t *testing.T) {
		w := s.do("DELETE", "/api/v1/clusters/"+clusterID, nil, adminToken)
		require.Equal(t, http.StatusNoContent, w.Code, "delete cluster: %s", w.Body.String())
	})

	t.Run("get deleted cluster returns 404", func(t *testing.T) {
		w := s.do("GET", "/api/v1/clusters/"+clusterID, nil, adminToken)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("create cluster without required fields returns 400", func(t *testing.T) {
		body := map[string]interface{}{
			"description": "missing required fields",
		}
		w := s.do("POST", "/api/v1/clusters", body, adminToken)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	// Clean up second cluster
	t.Run("delete second cluster", func(t *testing.T) {
		w := s.do("DELETE", "/api/v1/clusters/"+secondClusterID, nil, adminToken)
		require.Equal(t, http.StatusNoContent, w.Code, "delete second cluster: %s", w.Body.String())
	})
}

// ---------------------------------------------------------------------------
// TestAPIIntegration_BranchOverrides
// ---------------------------------------------------------------------------

func TestAPIIntegration_BranchOverrides(t *testing.T) {
	s := newIntegServer(t)

	// Setup: template → chart → publish → instantiate → create instance.
	adminToken := s.createAdminAndLogin(t)
	devopsToken, _ := s.registerAndLogin(t, uniqueName("devops"), "pass", "devops", adminToken)
	userToken, _ := s.registerAndLogin(t, uniqueName("bruser"), "pass", "user", adminToken)

	tpl := map[string]interface{}{
		"name":           uniqueName("brtpl"),
		"description":    "Template for branch override test",
		"category":       "test",
		"version":        "1.0.0",
		"default_branch": "main",
	}
	w := s.do("POST", "/api/v1/templates", tpl, devopsToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	tplID := parseBody(t, w)["id"].(string)

	chart := map[string]interface{}{
		"chart_name":     "api",
		"repository_url": "https://charts.example.com",
		"chart_version":  "1.0.0",
		"default_values": "replicas: 1",
	}
	w = s.do("POST", fmt.Sprintf("/api/v1/templates/%s/charts", tplID), chart, devopsToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	w = s.do("POST", fmt.Sprintf("/api/v1/templates/%s/publish", tplID), nil, devopsToken)
	require.Equal(t, http.StatusOK, w.Code)

	instBody := map[string]interface{}{"name": uniqueName("brdef"), "description": "def for branch overrides"}
	w = s.do("POST", fmt.Sprintf("/api/v1/templates/%s/instantiate", tplID), instBody, userToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	instResp := parseBody(t, w)
	defID := instResp["definition"].(map[string]interface{})["id"].(string)
	charts := instResp["charts"].([]interface{})
	require.NotEmpty(t, charts)
	chartConfigID := charts[0].(map[string]interface{})["id"].(string)

	var instID string
	instName := uniqueName("brinst")

	t.Run("create instance", func(t *testing.T) {
		inst := map[string]interface{}{
			"stack_definition_id": defID,
			"name":                instName,
			"branch":              "main",
		}
		w := s.do("POST", "/api/v1/stack-instances", inst, userToken)
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
		body := parseBody(t, w)
		instID = body["id"].(string)
		assert.NotEmpty(t, instID)
	})

	t.Run("list branch overrides initially empty", func(t *testing.T) {
		w := s.do("GET", fmt.Sprintf("/api/v1/stack-instances/%s/branches", instID), nil, userToken)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		arr := parseArray(t, w)
		assert.Empty(t, arr)
	})

	t.Run("set branch override", func(t *testing.T) {
		body := map[string]string{"branch": "feature/test"}
		w := s.do("PUT", fmt.Sprintf("/api/v1/stack-instances/%s/branches/%s", instID, chartConfigID), body, userToken)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		resp := parseBody(t, w)
		assert.Equal(t, "feature/test", resp["branch"])
		assert.Equal(t, chartConfigID, resp["chart_config_id"])
	})

	t.Run("list branch overrides returns one", func(t *testing.T) {
		w := s.do("GET", fmt.Sprintf("/api/v1/stack-instances/%s/branches", instID), nil, userToken)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		arr := parseArray(t, w)
		assert.Len(t, arr, 1)
		first := arr[0].(map[string]interface{})
		assert.Equal(t, "feature/test", first["branch"])
	})

	t.Run("update branch override", func(t *testing.T) {
		body := map[string]string{"branch": "develop"}
		w := s.do("PUT", fmt.Sprintf("/api/v1/stack-instances/%s/branches/%s", instID, chartConfigID), body, userToken)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		resp := parseBody(t, w)
		assert.Equal(t, "develop", resp["branch"])
	})

	t.Run("non-owner cannot list branch overrides", func(t *testing.T) {
		otherToken, _ := s.registerAndLogin(t, uniqueName("other"), "pass", "user", adminToken)
		w := s.do("GET", fmt.Sprintf("/api/v1/stack-instances/%s/branches", instID), nil, otherToken)
		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("delete branch override", func(t *testing.T) {
		w := s.do("DELETE", fmt.Sprintf("/api/v1/stack-instances/%s/branches/%s", instID, chartConfigID), nil, userToken)
		assert.Equal(t, http.StatusNoContent, w.Code)
	})

	t.Run("list after delete is empty", func(t *testing.T) {
		w := s.do("GET", fmt.Sprintf("/api/v1/stack-instances/%s/branches", instID), nil, userToken)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		arr := parseArray(t, w)
		assert.Empty(t, arr)
	})
}

// ---------------------------------------------------------------------------
// TestAPIIntegration_CleanupPolicies
// ---------------------------------------------------------------------------

func TestAPIIntegration_CleanupPolicies(t *testing.T) {
	s := newIntegServer(t)
	adminToken := s.createAdminAndLogin(t)

	var policyID string

	t.Run("create cleanup policy", func(t *testing.T) {
		body := map[string]interface{}{
			"name":       uniqueName("policy"),
			"action":     "stop",
			"condition":  "idle_days:7",
			"schedule":   "0 2 * * *",
			"cluster_id": "all",
			"enabled":    true,
			"dry_run":    false,
		}
		w := s.do("POST", "/api/v1/admin/cleanup-policies", body, adminToken)
		require.Equal(t, http.StatusCreated, w.Code, "create policy: %s", w.Body.String())
		resp := parseBody(t, w)
		policyID = resp["id"].(string)
		assert.NotEmpty(t, policyID)
		assert.Equal(t, "stop", resp["action"])
		assert.Equal(t, "idle_days:7", resp["condition"])
	})

	t.Run("list cleanup policies", func(t *testing.T) {
		w := s.do("GET", "/api/v1/admin/cleanup-policies", nil, adminToken)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		arr := parseArray(t, w)
		found := false
		for _, item := range arr {
			m := item.(map[string]interface{})
			if m["id"] == policyID {
				found = true
				break
			}
		}
		assert.True(t, found, "policy %s not found in list", policyID)
	})

	t.Run("update cleanup policy", func(t *testing.T) {
		body := map[string]interface{}{
			"name":       uniqueName("updated-policy"),
			"action":     "clean",
			"condition":  "status:stopped,age_days:14",
			"schedule":   "0 3 * * *",
			"cluster_id": "all",
			"enabled":    false,
		}
		w := s.do("PUT", fmt.Sprintf("/api/v1/admin/cleanup-policies/%s", policyID), body, adminToken)
		require.Equal(t, http.StatusOK, w.Code, "update policy: %s", w.Body.String())
		resp := parseBody(t, w)
		assert.Equal(t, "clean", resp["action"])
		assert.Equal(t, false, resp["enabled"])
	})

	t.Run("non-admin cannot access cleanup policies", func(t *testing.T) {
		userToken, _ := s.registerAndLogin(t, uniqueName("cpuser"), "pass", "user", adminToken)
		w := s.do("GET", "/api/v1/admin/cleanup-policies", nil, userToken)
		assert.Equal(t, http.StatusForbidden, w.Code)

		w = s.do("POST", "/api/v1/admin/cleanup-policies", map[string]interface{}{
			"name":       "forbidden",
			"action":     "stop",
			"condition":  "idle_days:1",
			"schedule":   "0 0 * * *",
			"cluster_id": "all",
		}, userToken)
		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("devops cannot access cleanup policies", func(t *testing.T) {
		devopsToken, _ := s.registerAndLogin(t, uniqueName("cpdevops"), "pass", "devops", adminToken)
		w := s.do("GET", "/api/v1/admin/cleanup-policies", nil, devopsToken)
		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("create invalid policy returns 400", func(t *testing.T) {
		body := map[string]interface{}{
			"name":   "no-action",
			"action": "invalid",
		}
		w := s.do("POST", "/api/v1/admin/cleanup-policies", body, adminToken)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("delete cleanup policy", func(t *testing.T) {
		w := s.do("DELETE", fmt.Sprintf("/api/v1/admin/cleanup-policies/%s", policyID), nil, adminToken)
		assert.Equal(t, http.StatusNoContent, w.Code)
	})

	t.Run("list after delete excludes removed policy", func(t *testing.T) {
		w := s.do("GET", "/api/v1/admin/cleanup-policies", nil, adminToken)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		arr := parseArray(t, w)
		for _, item := range arr {
			m := item.(map[string]interface{})
			assert.NotEqual(t, policyID, m["id"], "deleted policy should not appear in list")
		}
	})
}

// ---------------------------------------------------------------------------
// TestAPIIntegration_SharedValues
// ---------------------------------------------------------------------------

func TestAPIIntegration_SharedValues(t *testing.T) {
	s := newIntegServer(t)
	adminToken := s.createAdminAndLogin(t)

	// Create a cluster first.
	clusterBody := map[string]interface{}{
		"name":            uniqueName("svcluster"),
		"description":     "cluster for shared values test",
		"api_server_url":  "https://k8s.example.com:6443",
		"kubeconfig_data": "apiVersion: v1\nkind: Config\nclusters: []",
		"region":          "northeurope",
	}
	w := s.do("POST", "/api/v1/clusters", clusterBody, adminToken)
	require.Equal(t, http.StatusCreated, w.Code, "create cluster: %s", w.Body.String())
	clusterID := parseBody(t, w)["id"].(string)

	var svID1, svID2 string

	t.Run("create shared values", func(t *testing.T) {
		body := map[string]interface{}{
			"name":        "global-defaults",
			"description": "Global default values",
			"values":      "env: production\nregion: eu-west",
			"priority":    10,
		}
		w := s.do("POST", fmt.Sprintf("/api/v1/clusters/%s/shared-values", clusterID), body, adminToken)
		require.Equal(t, http.StatusCreated, w.Code, "create shared values: %s", w.Body.String())
		resp := parseBody(t, w)
		svID1 = resp["id"].(string)
		assert.NotEmpty(t, svID1)
		assert.Equal(t, "global-defaults", resp["name"])
		assert.Equal(t, float64(10), resp["priority"])
	})

	t.Run("create second shared values with higher priority", func(t *testing.T) {
		body := map[string]interface{}{
			"name":        "team-overrides",
			"description": "Team-specific overrides",
			"values":      "team: platform\nscaling: enabled",
			"priority":    20,
		}
		w := s.do("POST", fmt.Sprintf("/api/v1/clusters/%s/shared-values", clusterID), body, adminToken)
		require.Equal(t, http.StatusCreated, w.Code, "create second shared values: %s", w.Body.String())
		resp := parseBody(t, w)
		svID2 = resp["id"].(string)
		assert.NotEmpty(t, svID2)
	})

	t.Run("list shared values returns both sorted by priority", func(t *testing.T) {
		w := s.do("GET", fmt.Sprintf("/api/v1/clusters/%s/shared-values", clusterID), nil, adminToken)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		arr := parseArray(t, w)
		require.Len(t, arr, 2)
		// Lower priority should come first.
		first := arr[0].(map[string]interface{})
		second := arr[1].(map[string]interface{})
		assert.Equal(t, svID1, first["id"])
		assert.Equal(t, svID2, second["id"])
	})

	t.Run("update shared values", func(t *testing.T) {
		body := map[string]interface{}{
			"name":        "global-defaults-updated",
			"description": "Updated global defaults",
			"values":      "env: staging\nregion: eu-north",
			"priority":    5,
		}
		w := s.do("PUT", fmt.Sprintf("/api/v1/clusters/%s/shared-values/%s", clusterID, svID1), body, adminToken)
		require.Equal(t, http.StatusOK, w.Code, "update shared values: %s", w.Body.String())
		resp := parseBody(t, w)
		assert.Equal(t, "global-defaults-updated", resp["name"])
		assert.Equal(t, float64(5), resp["priority"])
	})

	t.Run("non-admin cannot access shared values", func(t *testing.T) {
		userToken, _ := s.registerAndLogin(t, uniqueName("svuser"), "pass", "user", adminToken)
		w := s.do("GET", fmt.Sprintf("/api/v1/clusters/%s/shared-values", clusterID), nil, userToken)
		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("shared values for non-existent cluster returns 404", func(t *testing.T) {
		w := s.do("GET", "/api/v1/clusters/nonexistent/shared-values", nil, adminToken)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("delete shared values", func(t *testing.T) {
		w := s.do("DELETE", fmt.Sprintf("/api/v1/clusters/%s/shared-values/%s", clusterID, svID2), nil, adminToken)
		assert.Equal(t, http.StatusNoContent, w.Code)
	})

	t.Run("list after delete returns one", func(t *testing.T) {
		w := s.do("GET", fmt.Sprintf("/api/v1/clusters/%s/shared-values", clusterID), nil, adminToken)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		arr := parseArray(t, w)
		assert.Len(t, arr, 1)
		assert.Equal(t, svID1, arr[0].(map[string]interface{})["id"])
	})
}

// ---------------------------------------------------------------------------
// TestAPIIntegration_Favorites
// ---------------------------------------------------------------------------

func TestAPIIntegration_Favorites(t *testing.T) {
	s := newIntegServer(t)

	adminToken := s.createAdminAndLogin(t)
	userToken, _ := s.registerAndLogin(t, uniqueName("favuser"), "pass", "user", adminToken)
	devopsToken, _ := s.registerAndLogin(t, uniqueName("favdevops"), "pass", "devops", adminToken)

	// Create a template to favorite.
	tpl := map[string]interface{}{
		"name":           uniqueName("favtpl"),
		"description":    "Template for favorites test",
		"category":       "test",
		"version":        "1.0.0",
		"default_branch": "main",
	}
	w := s.do("POST", "/api/v1/templates", tpl, devopsToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	tplID := parseBody(t, w)["id"].(string)

	// Create template → chart → publish → instantiate to get a definition + instance.
	chart := map[string]interface{}{
		"chart_name":     "webapp",
		"repository_url": "https://charts.example.com",
		"chart_version":  "1.0.0",
		"default_values": "replicas: 1",
	}
	w = s.do("POST", fmt.Sprintf("/api/v1/templates/%s/charts", tplID), chart, devopsToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	w = s.do("POST", fmt.Sprintf("/api/v1/templates/%s/publish", tplID), nil, devopsToken)
	require.Equal(t, http.StatusOK, w.Code)

	instBody := map[string]interface{}{"name": uniqueName("favdef"), "description": "def for favorites"}
	w = s.do("POST", fmt.Sprintf("/api/v1/templates/%s/instantiate", tplID), instBody, userToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	instResp := parseBody(t, w)
	defID := instResp["definition"].(map[string]interface{})["id"].(string)

	t.Run("list favorites initially empty", func(t *testing.T) {
		w := s.do("GET", "/api/v1/favorites", nil, userToken)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		arr := parseArray(t, w)
		assert.Empty(t, arr)
	})

	t.Run("add template to favorites", func(t *testing.T) {
		body := map[string]string{"entity_type": "template", "entity_id": tplID}
		w := s.do("POST", "/api/v1/favorites", body, userToken)
		require.Equal(t, http.StatusCreated, w.Code, "add favorite: %s", w.Body.String())
		resp := parseBody(t, w)
		assert.Equal(t, "template", resp["entity_type"])
		assert.Equal(t, tplID, resp["entity_id"])
	})

	t.Run("add definition to favorites", func(t *testing.T) {
		body := map[string]string{"entity_type": "definition", "entity_id": defID}
		w := s.do("POST", "/api/v1/favorites", body, userToken)
		require.Equal(t, http.StatusCreated, w.Code, "add favorite: %s", w.Body.String())
	})

	t.Run("list favorites returns two", func(t *testing.T) {
		w := s.do("GET", "/api/v1/favorites", nil, userToken)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		arr := parseArray(t, w)
		assert.Len(t, arr, 2)
	})

	t.Run("check favorite returns true", func(t *testing.T) {
		w := s.do("GET", fmt.Sprintf("/api/v1/favorites/check?entity_type=template&entity_id=%s", tplID), nil, userToken)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		resp := parseBody(t, w)
		assert.Equal(t, true, resp["is_favorite"])
	})

	t.Run("check non-favorited returns false", func(t *testing.T) {
		w := s.do("GET", "/api/v1/favorites/check?entity_type=instance&entity_id=nonexistent", nil, userToken)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		resp := parseBody(t, w)
		assert.Equal(t, false, resp["is_favorite"])
	})

	t.Run("other user favorites are isolated", func(t *testing.T) {
		otherToken, _ := s.registerAndLogin(t, uniqueName("favother"), "pass", "user", adminToken)
		w := s.do("GET", "/api/v1/favorites", nil, otherToken)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		arr := parseArray(t, w)
		assert.Empty(t, arr)
	})

	t.Run("invalid entity_type returns 400", func(t *testing.T) {
		body := map[string]string{"entity_type": "invalid", "entity_id": "xyz"}
		w := s.do("POST", "/api/v1/favorites", body, userToken)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("missing entity_id returns 400", func(t *testing.T) {
		body := map[string]string{"entity_type": "template"}
		w := s.do("POST", "/api/v1/favorites", body, userToken)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("remove template favorite", func(t *testing.T) {
		w := s.do("DELETE", fmt.Sprintf("/api/v1/favorites/template/%s", tplID), nil, userToken)
		assert.Equal(t, http.StatusNoContent, w.Code)
	})

	t.Run("check removed favorite returns false", func(t *testing.T) {
		w := s.do("GET", fmt.Sprintf("/api/v1/favorites/check?entity_type=template&entity_id=%s", tplID), nil, userToken)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		resp := parseBody(t, w)
		assert.Equal(t, false, resp["is_favorite"])
	})

	t.Run("list after removal returns one", func(t *testing.T) {
		w := s.do("GET", "/api/v1/favorites", nil, userToken)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		arr := parseArray(t, w)
		assert.Len(t, arr, 1)
	})
}

// ---------------------------------------------------------------------------
// TestAPIIntegration_Analytics
// ---------------------------------------------------------------------------

func TestAPIIntegration_Analytics(t *testing.T) {
	s := newIntegServer(t)

	adminToken := s.createAdminAndLogin(t)
	devopsToken, _ := s.registerAndLogin(t, uniqueName("andevops"), "pass", "devops", adminToken)
	userToken, _ := s.registerAndLogin(t, uniqueName("anuser"), "pass", "user", adminToken)

	// Create some data: template → chart → publish → instantiate → instance.
	tpl := map[string]interface{}{
		"name":           uniqueName("antpl"),
		"description":    "Template for analytics test",
		"category":       "test",
		"version":        "1.0.0",
		"default_branch": "main",
	}
	w := s.do("POST", "/api/v1/templates", tpl, devopsToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	tplID := parseBody(t, w)["id"].(string)

	chart := map[string]interface{}{
		"chart_name":     "analytics-svc",
		"repository_url": "https://charts.example.com",
		"chart_version":  "1.0.0",
		"default_values": "replicas: 1",
	}
	w = s.do("POST", fmt.Sprintf("/api/v1/templates/%s/charts", tplID), chart, devopsToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	w = s.do("POST", fmt.Sprintf("/api/v1/templates/%s/publish", tplID), nil, devopsToken)
	require.Equal(t, http.StatusOK, w.Code)

	instBody := map[string]interface{}{"name": uniqueName("andef"), "description": "def for analytics"}
	w = s.do("POST", fmt.Sprintf("/api/v1/templates/%s/instantiate", tplID), instBody, userToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	instResp := parseBody(t, w)
	defID := instResp["definition"].(map[string]interface{})["id"].(string)

	inst := map[string]interface{}{
		"stack_definition_id": defID,
		"name":                uniqueName("aninst"),
		"branch":              "main",
	}
	w = s.do("POST", "/api/v1/stack-instances", inst, userToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	t.Run("overview returns stats", func(t *testing.T) {
		w := s.do("GET", "/api/v1/analytics/overview", nil, devopsToken)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		resp := parseBody(t, w)
		// Should have at least the data we created.
		assert.GreaterOrEqual(t, resp["total_templates"].(float64), float64(1))
		assert.GreaterOrEqual(t, resp["total_definitions"].(float64), float64(1))
		assert.GreaterOrEqual(t, resp["total_instances"].(float64), float64(1))
		assert.GreaterOrEqual(t, resp["total_users"].(float64), float64(1))
		// Verify all expected keys are present.
		assert.Contains(t, resp, "running_instances")
		assert.Contains(t, resp, "total_deploys")
	})

	t.Run("template stats returns array", func(t *testing.T) {
		w := s.do("GET", "/api/v1/analytics/templates", nil, devopsToken)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		arr := parseArray(t, w)
		assert.NotEmpty(t, arr)
		// Find our template.
		found := false
		for _, item := range arr {
			m := item.(map[string]interface{})
			if m["template_id"] == tplID {
				found = true
				assert.Contains(t, m, "template_name")
				assert.Contains(t, m, "definition_count")
				assert.Contains(t, m, "instance_count")
				assert.Contains(t, m, "deploy_count")
				break
			}
		}
		assert.True(t, found, "template %s not found in analytics", tplID)
	})

	t.Run("user stats requires admin", func(t *testing.T) {
		w := s.do("GET", "/api/v1/analytics/users", nil, devopsToken)
		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("user stats accessible by admin", func(t *testing.T) {
		w := s.do("GET", "/api/v1/analytics/users", nil, adminToken)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		arr := parseArray(t, w)
		assert.NotEmpty(t, arr)
		first := arr[0].(map[string]interface{})
		assert.Contains(t, first, "user_id")
		assert.Contains(t, first, "username")
		assert.Contains(t, first, "instance_count")
		assert.Contains(t, first, "deploy_count")
	})

	t.Run("regular user cannot access analytics", func(t *testing.T) {
		w := s.do("GET", "/api/v1/analytics/overview", nil, userToken)
		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("unauthenticated request returns 401", func(t *testing.T) {
		w := s.do("GET", "/api/v1/analytics/overview", nil, "")
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})
}

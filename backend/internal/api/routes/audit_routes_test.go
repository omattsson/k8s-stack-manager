package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/internal/api/handlers"
	"backend/internal/api/middleware"
	"backend/internal/health"
	"backend/internal/models"
	"backend/internal/websocket"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSetupRoutes_EveryMutatingRouteHasKnownAuditEntry registers every
// handler and checks that each authenticated POST, PUT and DELETE route gives
// a known audit action and entity type (issue #438). A new route with an
// operation segment (for example /:id/deploy) must get a route table entry in
// middleware/audit.go; else the entity type is the operation name, and this
// test fails.
func TestSetupRoutes_EveryMutatingRouteHasKnownAuditEntry(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	healthChecker := health.New()
	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	cfg := testConfig()
	rl := SetupRoutes(router, Deps{
		Repository:                   handlers.NewMockRepository(),
		HealthChecker:                healthChecker,
		Config:                       cfg,
		Hub:                          hub,
		AuthHandler:                  handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth, &cfg.OIDC),
		TemplateHandler:              &handlers.TemplateHandler{},
		DefinitionHandler:            &handlers.DefinitionHandler{},
		InstanceHandler:              &handlers.InstanceHandler{},
		GitHandler:                   &handlers.GitHandler{},
		AuditLogHandler:              handlers.NewAuditLogHandler(nil),
		AuditLogger:                  &stubAuditLogger{},
		UserHandler:                  handlers.NewUserHandler(&stubUserRepo{}, nil, &stubAPIKeyRepo{}),
		APIKeyHandler:                handlers.NewAPIKeyHandler(&stubAPIKeyRepo{}, &stubUserRepo{}, &cfg.Auth),
		AdminHandler:                 &handlers.AdminHandler{},
		BranchOverrideHandler:        &handlers.BranchOverrideHandler{},
		InstanceQuotaOverrideHandler: &handlers.InstanceQuotaOverrideHandler{},
		FavoriteHandler:              &handlers.FavoriteHandler{},
		QuickDeployHandler:           &handlers.QuickDeployHandler{},
		AnalyticsHandler:             &handlers.AnalyticsHandler{},
		CleanupPolicyHandler:         &handlers.CleanupPolicyHandler{},
		NotificationChannelHandler:   &handlers.NotificationChannelHandler{},
		ClusterHandler:               &handlers.ClusterHandler{},
		SharedValuesHandler:          &handlers.SharedValuesHandler{},
		DashboardHandler:             &handlers.DashboardHandler{},
		TemplateVersionHandler:       &handlers.TemplateVersionHandler{},
		NotificationHandler:          &handlers.NotificationHandler{},
		UserRepo:                     &stubUserRepo{},
		APIKeyRepo:                   &stubAPIKeyRepo{},
	})
	t.Cleanup(func() { rl.Stop() })

	// Routes outside the audited (authenticated) group.
	unaudited := map[string]bool{
		"POST /api/v1/auth/login":          true,
		"POST /api/v1/auth/refresh":        true,
		"POST /api/v1/auth/logout":         true,
		"POST /api/v1/auth/logout-all":     true,
		"POST /api/v1/auth/oidc/cli-auth":  true,
		"POST /api/v1/auth/oidc/cli-token": true,
	}

	checked := 0
	for _, r := range router.Routes() {
		if r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodDelete {
			continue
		}
		if !strings.HasPrefix(r.Path, "/api/v1/") || strings.HasPrefix(r.Path, "/api/v1/items") {
			continue
		}
		key := r.Method + " " + r.Path
		if unaudited[key] {
			continue
		}
		checked++
		got := middleware.AuditRouteFor(r.Method, r.Path)
		if got.Skip {
			continue
		}
		assert.Contains(t, middleware.KnownAuditActions, got.Action, key)
		assert.Contains(t, middleware.KnownAuditEntityTypes, got.EntityType, key)
	}
	t.Logf("checked %d mutating routes", checked)
	require.Greater(t, checked, 50, "expected the full API to be registered")
}

// emptyAuditLogRepo returns an empty audit log page.
type emptyAuditLogRepo struct{}

func (s *emptyAuditLogRepo) Create(_ *models.AuditLog) error { return nil }
func (s *emptyAuditLogRepo) List(_ models.AuditLogFilters) (*models.AuditLogResult, error) {
	return &models.AuditLogResult{Data: []models.AuditLog{}}, nil
}

// TestSetupRoutes_AuditLogRoleGates checks the role gates of the audit log
// routes (issue #496): list requires devops or admin, export requires admin.
func TestSetupRoutes_AuditLogRoleGates(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })

	cfg := testConfig()
	rl := SetupRoutes(router, Deps{
		Repository:      handlers.NewMockRepository(),
		HealthChecker:   health.New(),
		Config:          cfg,
		Hub:             hub,
		AuthHandler:     handlers.NewAuthHandler(&stubUserRepo{}, &cfg.Auth, &cfg.OIDC),
		AuditLogHandler: handlers.NewAuditLogHandler(&emptyAuditLogRepo{}),
	})
	t.Cleanup(func() { rl.Stop() })

	tests := []struct {
		name       string
		role       string
		path       string
		wantStatus int
	}{
		{"user cannot list", "user", "/api/v1/audit-logs", http.StatusForbidden},
		{"devops can list", "devops", "/api/v1/audit-logs", http.StatusOK},
		{"admin can list", "admin", "/api/v1/audit-logs", http.StatusOK},
		{"user cannot export", "user", "/api/v1/audit-logs/export", http.StatusForbidden},
		{"devops cannot export", "devops", "/api/v1/audit-logs/export", http.StatusForbidden},
		{"admin can export", "admin", "/api/v1/audit-logs/export", http.StatusOK},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			token, err := middleware.GenerateToken("u-"+tt.role, tt.role, tt.role, cfg.Auth.JWTSecret, time.Hour)
			require.NoError(t, err)
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			assert.Equal(t, tt.wantStatus, w.Code, w.Body.String())
		})
	}
}

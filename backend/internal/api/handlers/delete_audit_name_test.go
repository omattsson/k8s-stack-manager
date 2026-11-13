package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// auditRecorder is an AuditLogger that keeps the entries.
type auditRecorder struct {
	mu      sync.Mutex
	entries []models.AuditLog
}

func (r *auditRecorder) Create(_ context.Context, e *models.AuditLog) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, *e)
	return nil
}

func (r *auditRecorder) all() []models.AuditLog {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]models.AuditLog(nil), r.entries...)
}

// waitOne waits for exactly one entry (the middleware writes in a goroutine).
func (r *auditRecorder) waitOne(t *testing.T) models.AuditLog {
	t.Helper()
	require.Eventually(t, func() bool { return len(r.all()) == 1 }, 2*time.Second, 5*time.Millisecond)
	return r.all()[0]
}

// TestDeleteHandlers_AuditEntityName checks that each delete handler names
// the deleted entity in its audit entry (#787): the response holds no name.
func TestDeleteHandlers_AuditEntityName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		router   func(rec *auditRecorder) *gin.Engine
		path     string
		wantName string
	}{
		{
			name: "cluster",
			router: func(rec *auditRecorder) *gin.Engine {
				clusters := NewMockClusterRepository()
				seedCluster(clusters, "cl-1", "prod")
				return setupClusterRouter(clusters, NewMockStackInstanceRepository(), nil, "admin", middleware.NewAuditMiddleware(rec))
			},
			path: "/api/v1/clusters/cl-1", wantName: "prod",
		},
		{
			name: "user",
			router: func(rec *auditRecorder) *gin.Engine {
				users := NewMockUserRepository()
				_ = users.Create(context.Background(), &models.User{ID: "u-2", Username: "bob", Role: "user"})
				return setupUserRouter(users, "admin-1", "admin", middleware.NewAuditMiddleware(rec))
			},
			path: "/api/v1/users/u-2", wantName: "bob",
		},
		{
			name: "definition",
			router: func(rec *auditRecorder) *gin.Engine {
				defs := NewMockStackDefinitionRepository()
				_ = defs.Create(context.Background(), &models.StackDefinition{ID: "d-1", Name: "shop", OwnerID: "admin-1"})
				return setupDefinitionRouterWithTemplates(defs, NewMockChartConfigRepository(), NewMockStackInstanceRepository(), nil, nil, "admin-1", "admin", middleware.NewAuditMiddleware(rec))
			},
			path: "/api/v1/stack-definitions/d-1", wantName: "shop",
		},
		{
			name: "template",
			router: func(rec *auditRecorder) *gin.Engine {
				tmpls := NewMockStackTemplateRepository()
				_ = tmpls.Create(context.Background(), &models.StackTemplate{ID: "t-1", Name: "base", OwnerID: "admin-1"})
				return setupTemplateRouter(tmpls, NewMockTemplateChartConfigRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), "admin-1", "admin", middleware.NewAuditMiddleware(rec))
			},
			path: "/api/v1/templates/t-1", wantName: "base",
		},
		{
			name: "instance",
			router: func(rec *auditRecorder) *gin.Engine {
				insts := NewMockStackInstanceRepository()
				_ = insts.Create(context.Background(), &models.StackInstance{ID: "i-1", Name: "stack-a", OwnerID: "admin-1", Status: models.StackStatusDraft})
				return setupInstanceRouter(insts, NewMockValueOverrideRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
					NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), "admin-1", "admin", "admin", middleware.NewAuditMiddleware(rec))
			},
			path: "/api/v1/stack-instances/i-1", wantName: "stack-a",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := &auditRecorder{}
			r := tt.router(rec)
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodDelete, tt.path, nil)
			r.ServeHTTP(w, req)
			require.Less(t, w.Code, 300, w.Body.String())
			e := rec.waitOne(t)
			assert.Equal(t, "delete", e.Action)
			assert.Equal(t, tt.wantName, e.EntityName)
		})
	}
}

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// injectRole returns middleware that sets the role context key (simulates a prior AuthRequired call).
func injectRole(role string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if role != "" {
			c.Set(contextKeyRole, role)
		}
		c.Next()
	}
}

func setupRoleRouter(roleMiddleware gin.HandlerFunc, requiredRole ...string) *gin.Engine {
	r := gin.New()
	r.Use(injectRole(""))
	group := r.Group("/protected")
	if len(requiredRole) > 0 {
		group.Use(RequireRole(requiredRole...))
	}
	group.GET("", func(c *gin.Context) { c.Status(http.StatusOK) })
	return r
}

func doRoleRequest(router *gin.Engine, role string) int {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/protected", nil)
	// Inject role directly by pre-processing via a custom chain.
	// We rebuild the router with the role injected.
	_ = role
	router.ServeHTTP(w, req)
	return w.Code
}

func buildRouter(role string, middleware ...gin.HandlerFunc) *gin.Engine {
	r := gin.New()
	// Inject role via a setup handler.
	r.Use(func(c *gin.Context) {
		if role != "" {
			c.Set(contextKeyRole, role)
		}
		c.Next()
	})
	for _, m := range middleware {
		r.Use(m)
	}
	r.GET("/test", func(c *gin.Context) { c.Status(http.StatusOK) })
	return r
}

func requireRequest(router *gin.Engine) int {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/test", nil)
	router.ServeHTTP(w, req)
	return w.Code
}

func TestRequireRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		userRole      string
		requiredRoles []string
		wantStatus    int
	}{
		{"no role in context, require user", "", []string{"user"}, http.StatusUnauthorized},
		{"unknown role, require user", "superadmin", []string{"user"}, http.StatusForbidden},
		{"user role, require user", "user", []string{"user"}, http.StatusOK},
		{"user role, require devops", "user", []string{"devops"}, http.StatusForbidden},
		{"user role, require admin", "user", []string{"admin"}, http.StatusForbidden},
		{"devops role, require user", "devops", []string{"user"}, http.StatusOK},
		{"devops role, require devops", "devops", []string{"devops"}, http.StatusOK},
		{"devops role, require admin", "devops", []string{"admin"}, http.StatusForbidden},
		{"admin role, require user", "admin", []string{"user"}, http.StatusOK},
		{"admin role, require devops", "admin", []string{"devops"}, http.StatusOK},
		{"admin role, require admin", "admin", []string{"admin"}, http.StatusOK},
		{"no required roles specified", "user", []string{}, http.StatusOK},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router := buildRouter(tt.userRole, RequireRole(tt.requiredRoles...))
			assert.Equal(t, tt.wantStatus, requireRequest(router))
		})
	}
}

func TestRequireAdmin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		role       string
		wantStatus int
	}{
		{"admin", http.StatusOK},
		{"devops", http.StatusForbidden},
		{"user", http.StatusForbidden},
		{"", http.StatusUnauthorized},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.role, func(t *testing.T) {
			t.Parallel()
			router := buildRouter(tt.role, RequireAdmin())
			assert.Equal(t, tt.wantStatus, requireRequest(router))
		})
	}
}

func TestRequireDevOps(t *testing.T) {
	t.Parallel()

	tests := []struct {
		role       string
		wantStatus int
	}{
		{"admin", http.StatusOK},
		{"devops", http.StatusOK},
		{"user", http.StatusForbidden},
		{"", http.StatusUnauthorized},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.role, func(t *testing.T) {
			t.Parallel()
			router := buildRouter(tt.role, RequireDevOps())
			assert.Equal(t, tt.wantStatus, requireRequest(router))
		})
	}
}

func TestRequireUser(t *testing.T) {
	t.Parallel()

	tests := []struct {
		role       string
		wantStatus int
	}{
		{"admin", http.StatusOK},
		{"devops", http.StatusOK},
		{"user", http.StatusOK},
		{"", http.StatusUnauthorized},
		{"unknown", http.StatusForbidden},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.role, func(t *testing.T) {
			t.Parallel()
			router := buildRouter(tt.role, RequireUser())
			assert.Equal(t, tt.wantStatus, requireRequest(router))
		})
	}
}

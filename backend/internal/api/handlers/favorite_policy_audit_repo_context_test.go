package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/models"
	"backend/internal/scheduler"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hookFavoriteRepo runs the hook of hooks with "before:Favorite.<Method>" and
// "after:Favorite.<Method>" around the calls of MockUserFavoriteRepository,
// which fails on a done context (#656).
type hookFavoriteRepo struct {
	*MockUserFavoriteRepository
	hooks *hookInstanceRepo
}

func (r *hookFavoriteRepo) List(ctx context.Context, userID string) ([]*models.UserFavorite, error) {
	r.hooks.run("before:Favorite.List", ctx)
	defer r.hooks.run("after:Favorite.List", ctx)
	return r.MockUserFavoriteRepository.List(ctx, userID)
}

func (r *hookFavoriteRepo) Add(ctx context.Context, fav *models.UserFavorite) error {
	r.hooks.run("before:Favorite.Add", ctx)
	defer r.hooks.run("after:Favorite.Add", ctx)
	return r.MockUserFavoriteRepository.Add(ctx, fav)
}

func (r *hookFavoriteRepo) Remove(ctx context.Context, userID, entityType, entityID string) error {
	r.hooks.run("before:Favorite.Remove", ctx)
	defer r.hooks.run("after:Favorite.Remove", ctx)
	return r.MockUserFavoriteRepository.Remove(ctx, userID, entityType, entityID)
}

func (r *hookFavoriteRepo) IsFavorite(ctx context.Context, userID, entityType, entityID string) (bool, error) {
	r.hooks.run("before:Favorite.IsFavorite", ctx)
	defer r.hooks.run("after:Favorite.IsFavorite", ctx)
	return r.MockUserFavoriteRepository.IsFavorite(ctx, userID, entityType, entityID)
}

// hookPolicyRepo runs the hook of hooks with "before:Policy.<Method>" and
// "after:Policy.<Method>" around the calls of mockCleanupPolicyRepo, which
// fails on a done context (#656).
type hookPolicyRepo struct {
	*mockCleanupPolicyRepo
	hooks *hookInstanceRepo
}

func (r *hookPolicyRepo) Create(ctx context.Context, p *models.CleanupPolicy) error {
	r.hooks.run("before:Policy.Create", ctx)
	defer r.hooks.run("after:Policy.Create", ctx)
	return r.mockCleanupPolicyRepo.Create(ctx, p)
}

func (r *hookPolicyRepo) FindByID(ctx context.Context, id string) (*models.CleanupPolicy, error) {
	r.hooks.run("before:Policy.FindByID", ctx)
	defer r.hooks.run("after:Policy.FindByID", ctx)
	return r.mockCleanupPolicyRepo.FindByID(ctx, id)
}

func (r *hookPolicyRepo) Update(ctx context.Context, p *models.CleanupPolicy) error {
	r.hooks.run("before:Policy.Update", ctx)
	defer r.hooks.run("after:Policy.Update", ctx)
	return r.mockCleanupPolicyRepo.Update(ctx, p)
}

func (r *hookPolicyRepo) Delete(ctx context.Context, id string) error {
	r.hooks.run("before:Policy.Delete", ctx)
	defer r.hooks.run("after:Policy.Delete", ctx)
	return r.mockCleanupPolicyRepo.Delete(ctx, id)
}

func (r *hookPolicyRepo) List(ctx context.Context) ([]models.CleanupPolicy, error) {
	r.hooks.run("before:Policy.List", ctx)
	defer r.hooks.run("after:Policy.List", ctx)
	return r.mockCleanupPolicyRepo.List(ctx)
}

func (r *hookPolicyRepo) ListEnabled(ctx context.Context) ([]models.CleanupPolicy, error) {
	r.hooks.run("before:Policy.ListEnabled", ctx)
	defer r.hooks.run("after:Policy.ListEnabled", ctx)
	return r.mockCleanupPolicyRepo.ListEnabled(ctx)
}

// hookAuditRepo runs the hook of hooks with "before:Audit.<Method>" and
// "after:Audit.<Method>" around the calls of MockAuditLogRepository, which
// fails on a done context (#656).
type hookAuditRepo struct {
	*MockAuditLogRepository
	hooks *hookInstanceRepo
}

func (r *hookAuditRepo) Create(ctx context.Context, l *models.AuditLog) error {
	r.hooks.run("before:Audit.Create", ctx)
	defer r.hooks.run("after:Audit.Create", ctx)
	return r.MockAuditLogRepository.Create(ctx, l)
}

func (r *hookAuditRepo) List(ctx context.Context, f models.AuditLogFilters) (*models.AuditLogResult, error) {
	r.hooks.run("before:Audit.List", ctx)
	defer r.hooks.run("after:Audit.List", ctx)
	return r.MockAuditLogRepository.List(ctx, f)
}

// fpaFixture holds the routers and the hooked repositories of the favorite,
// cleanup policy and audit log handlers.
type fpaFixture struct {
	hooks     *hookInstanceRepo
	favorites *hookFavoriteRepo
	policies  *hookPolicyRepo
	audit     *hookAuditRepo
	sched     *scheduler.Scheduler
	router    *gin.Engine
}

func newFPAFixture(t *testing.T) *fpaFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	hooks := newHookInstanceRepo()
	f := &fpaFixture{
		hooks:     hooks,
		favorites: &hookFavoriteRepo{MockUserFavoriteRepository: NewMockUserFavoriteRepository(), hooks: hooks},
		policies:  &hookPolicyRepo{mockCleanupPolicyRepo: newMockCleanupPolicyRepo(), hooks: hooks},
		audit:     &hookAuditRepo{MockAuditLogRepository: NewMockAuditLogRepository(), hooks: hooks},
	}
	ctx := context.Background()
	require.NoError(t, f.favorites.MockUserFavoriteRepository.Add(ctx, &models.UserFavorite{UserID: "user-1", EntityType: "template", EntityID: "tmpl-1"}))
	require.NoError(t, f.policies.mockCleanupPolicyRepo.Create(ctx, &models.CleanupPolicy{
		ID: "p1", Name: "stop-idle", Action: "stop", Condition: "idle_days:7",
		Schedule: "0 2 * * *", ClusterID: "all", Enabled: true,
	}))
	require.NoError(t, f.audit.MockAuditLogRepository.Create(ctx, &models.AuditLog{ID: "a1", UserID: "u1", Action: "create", EntityType: "stack_instance", EntityID: "si-1", Timestamp: time.Now().UTC()}))

	f.sched = scheduler.NewScheduler(f.policies, &cleanupMockInstanceRepo{}, &cleanupMockAuditRepo{}, nil, nil)
	require.NoError(t, f.sched.Start())
	t.Cleanup(f.sched.Stop)

	fav := NewFavoriteHandler(f.favorites)
	pol := NewCleanupPolicyHandler(f.policies, f.sched)
	aud := NewAuditLogHandler(f.audit)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userID", "user-1")
		c.Set("username", "testuser")
		c.Set("role", "admin")
		c.Next()
	})
	r.GET("/api/v1/favorites", fav.ListFavorites)
	r.POST("/api/v1/favorites", fav.AddFavorite)
	r.DELETE("/api/v1/favorites/:entityType/:entityId", fav.RemoveFavorite)
	r.GET("/api/v1/favorites/check", fav.CheckFavorite)
	g := r.Group("/api/v1/admin/cleanup-policies")
	g.GET("", pol.ListCleanupPolicies)
	g.POST("", pol.CreateCleanupPolicy)
	g.PUT("/:id", pol.UpdateCleanupPolicy)
	g.DELETE("/:id", pol.DeleteCleanupPolicy)
	g.POST("/:id/run", pol.RunCleanupPolicy)
	r.GET("/api/v1/audit-logs", aud.ListAuditLogs)
	r.GET("/api/v1/audit-logs/export", aud.ExportAuditLogs)
	f.router = r
	return f
}

func (f *fpaFixture) favoriteCount(t *testing.T) int {
	t.Helper()
	favs, err := f.favorites.MockUserFavoriteRepository.List(context.Background(), "user-1")
	require.NoError(t, err)
	return len(favs)
}

func (f *fpaFixture) policyNames(t *testing.T) []string {
	t.Helper()
	all, err := f.policies.mockCleanupPolicyRepo.List(context.Background())
	require.NoError(t, err)
	var out []string
	for _, p := range all {
		out = append(out, p.ID+":"+p.Name)
	}
	return out
}

// TestFavoritePolicyWrites_CancelledAtWriteStillCommit checks that a client
// that leaves at a favorite or cleanup policy write cannot stop the write or
// the 2xx answer (#656). The mocks fail on a done context, so a write with
// the request context fails.
func TestFavoritePolicyWrites_CancelledAtWriteStillCommit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		check      func(t *testing.T, f *fpaFixture)
		name       string
		method     string
		path       string
		body       string
		cancelAt   string
		wantStatus int
	}{
		{
			name: "add favorite", method: http.MethodPost, path: "/api/v1/favorites",
			body: `{"entity_type":"template","entity_id":"tmpl-2"}`, cancelAt: "before:Favorite.Add", wantStatus: http.StatusCreated,
			check: func(t *testing.T, f *fpaFixture) { assert.Equal(t, 2, f.favoriteCount(t)) },
		},
		{
			name: "remove favorite", method: http.MethodDelete, path: "/api/v1/favorites/template/tmpl-1",
			cancelAt: "before:Favorite.Remove", wantStatus: http.StatusNoContent,
			check: func(t *testing.T, f *fpaFixture) { assert.Equal(t, 0, f.favoriteCount(t)) },
		},
		{
			name: "create policy", method: http.MethodPost, path: "/api/v1/admin/cleanup-policies",
			body:     `{"name":"new","action":"stop","condition":"idle_days:3","schedule":"0 3 * * *","cluster_id":"all","enabled":true}`,
			cancelAt: "before:Policy.Create", wantStatus: http.StatusCreated,
			check: func(t *testing.T, f *fpaFixture) { assert.Len(t, f.policyNames(t), 2) },
		},
		{
			name: "update policy", method: http.MethodPut, path: "/api/v1/admin/cleanup-policies/p1",
			body: `{"name":"renamed"}`, cancelAt: "before:Policy.Update", wantStatus: http.StatusOK,
			check: func(t *testing.T, f *fpaFixture) { assert.Equal(t, []string{"p1:renamed"}, f.policyNames(t)) },
		},
		{
			name: "delete policy", method: http.MethodDelete, path: "/api/v1/admin/cleanup-policies/p1",
			cancelAt: "before:Policy.Delete", wantStatus: http.StatusNoContent,
			check: func(t *testing.T, f *fpaFixture) { assert.Empty(t, f.policyNames(t)) },
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFPAFixture(t)
			req, cancel := liveRequest(t, tt.method, tt.path, tt.body)
			f.hooks.setHook(cancelAt(tt.cancelAt, 1, cancel))

			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			require.Error(t, req.Context().Err(), "the request was cancelled during the handler")
			tt.check(t, f)
		})
	}
}

// TestCleanupPolicyWrites_ReloadRunsDetached checks that the scheduler reload
// after a committed policy write does not use the cancelled request context:
// a cancelled reload would leave the scheduler with the old policies (#656).
func TestCleanupPolicyWrites_ReloadRunsDetached(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		method   string
		path     string
		body     string
		cancelAt string
	}{
		{name: "create", method: http.MethodPost, path: "/api/v1/admin/cleanup-policies", cancelAt: "before:Policy.Create",
			body: `{"name":"new","action":"stop","condition":"idle_days:3","schedule":"0 3 * * *","cluster_id":"all","enabled":true}`},
		{name: "update", method: http.MethodPut, path: "/api/v1/admin/cleanup-policies/p1", body: `{"schedule":"0 4 * * *"}`, cancelAt: "before:Policy.Update"},
		{name: "delete", method: http.MethodDelete, path: "/api/v1/admin/cleanup-policies/p1", cancelAt: "before:Policy.Delete"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFPAFixture(t)
			req, cancel := liveRequest(t, tt.method, tt.path, tt.body)
			cancelFirst := cancelAt(tt.cancelAt, 1, cancel)
			reload := firstCtxErrAt("before:Policy.ListEnabled")
			// The scheduler loaded the policies at Start; the next ListEnabled is the reload.
			f.hooks.setHook(func(point string, ctx context.Context) {
				cancelFirst(point, ctx)
				reload.record(point, ctx)
			})

			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)

			require.Less(t, w.Code, 300, w.Body.String())
			require.Error(t, req.Context().Err(), "the request was cancelled during the handler")
			reload.mu.Lock()
			defer reload.mu.Unlock()
			require.True(t, reload.seen, "the scheduler reloaded the policies")
			assert.NoError(t, reload.err, "the reload got a live context")
		})
	}
}

// fpaReadCases are the reads of the favorite, cleanup policy and audit log
// handlers, also the read before a policy write.
var fpaReadCases = []struct {
	name   string
	method string
	path   string
	body   string
	readAt string
}{
	{name: "list favorites", method: http.MethodGet, path: "/api/v1/favorites", readAt: "Favorite.List"},
	{name: "check favorite", method: http.MethodGet, path: "/api/v1/favorites/check?entity_type=template&entity_id=tmpl-1", readAt: "Favorite.IsFavorite"},
	{name: "list policies", method: http.MethodGet, path: "/api/v1/admin/cleanup-policies", readAt: "Policy.List"},
	{name: "update policy reads the policy", method: http.MethodPut, path: "/api/v1/admin/cleanup-policies/p1", body: `{"name":"renamed"}`, readAt: "Policy.FindByID"},
	{name: "run policy reads the policy", method: http.MethodPost, path: "/api/v1/admin/cleanup-policies/p1/run?dry_run=true", readAt: "Policy.FindByID"},
	{name: "list audit logs", method: http.MethodGet, path: "/api/v1/audit-logs", readAt: "Audit.List"},
	{name: "export audit logs as json", method: http.MethodGet, path: "/api/v1/audit-logs/export?format=json", readAt: "Audit.List"},
	{name: "export audit logs as csv", method: http.MethodGet, path: "/api/v1/audit-logs/export?format=csv", readAt: "Audit.List"},
}

// TestFavoritePolicyAuditReads_CancelledReadAnswers499 checks that a client
// that leaves at a read gets 499 (not 500), that the read got the request
// context, and that nothing changes, also for the read before a write (#656).
func TestFavoritePolicyAuditReads_CancelledReadAnswers499(t *testing.T) {
	t.Parallel()

	for _, tt := range fpaReadCases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFPAFixture(t)
			policiesBefore := f.policyNames(t)
			req, cancel := liveRequest(t, tt.method, tt.path, tt.body)
			cancelFirst := cancelAt("before:"+tt.readAt, 1, cancel)
			readCtx := firstCtxErrAt("after:" + tt.readAt)
			f.hooks.setHook(func(point string, ctx context.Context) {
				cancelFirst(point, ctx)
				readCtx.record(point, ctx)
			})

			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)

			assert.Equal(t, statusClientClosedRequest, w.Code, w.Body.String())
			assert.JSONEq(t, `{"error":"Client closed request"}`, w.Body.String())
			readCtx.assertCancelled(t)
			assert.Equal(t, policiesBefore, f.policyNames(t), "the policy is unchanged")
		})
	}
}

// TestFavoritePolicyAuditReads_DeadlineAnswers500 checks that a passed
// deadline at a read answers 500, not 499 (#656).
func TestFavoritePolicyAuditReads_DeadlineAnswers500(t *testing.T) {
	t.Parallel()

	for _, tt := range fpaReadCases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFPAFixture(t)
			req := deadlineRequest(t, tt.method, tt.path, tt.body)
			f.hooks.setHook(waitForDeadline("before:" + tt.readAt))

			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)

			assert.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
			assert.JSONEq(t, `{"error":"Internal server error"}`, w.Body.String())
		})
	}
}

// TestRunCleanupPolicy_ClientLeavesAfterTheReadStillRuns checks that a
// manual run that has started does not stop when the client leaves: only the
// policy read uses the request context, the run uses the scheduler context
// (#656).
func TestRunCleanupPolicy_ClientLeavesAfterTheReadStillRuns(t *testing.T) {
	t.Parallel()

	f := newFPAFixture(t)
	hooks := f.hooks
	req, cancel := liveRequest(t, http.MethodPost, "/api/v1/admin/cleanup-policies/p1/run?dry_run=true", "")
	hooks.setHook(cancelAt("after:Policy.FindByID", 1, cancel))

	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Error(t, req.Context().Err(), "the request was cancelled during the handler")
}

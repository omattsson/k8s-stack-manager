package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/cluster"
	"backend/internal/database"
	"backend/internal/deployer"
	"backend/internal/helm"
	"backend/internal/hooks"
	"backend/internal/k8s"
	"backend/internal/models"
	"backend/internal/notifier"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// hookInstanceRepo calls hook with "before:<Method>" and "after:<Method>"
// around the calls of MockStackInstanceRepository. A hook that cancels the
// request context simulates a client that leaves at that point (#656).
type hookInstanceRepo struct {
	*MockStackInstanceRepository
	mu   sync.Mutex
	hook func(point string, ctx context.Context)
}

func newHookInstanceRepo() *hookInstanceRepo {
	return &hookInstanceRepo{MockStackInstanceRepository: NewMockStackInstanceRepository()}
}

func (r *hookInstanceRepo) setHook(fn func(point string, ctx context.Context)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hook = fn
}

func (r *hookInstanceRepo) run(point string, ctx context.Context) {
	r.mu.Lock()
	fn := r.hook
	r.mu.Unlock()
	if fn != nil {
		fn(point, ctx)
	}
}

func (r *hookInstanceRepo) Create(ctx context.Context, i *models.StackInstance) error {
	r.run("before:Create", ctx)
	defer r.run("after:Create", ctx)
	return r.MockStackInstanceRepository.Create(ctx, i)
}

func (r *hookInstanceRepo) FindByID(ctx context.Context, id string) (*models.StackInstance, error) {
	r.run("before:FindByID", ctx)
	defer r.run("after:FindByID", ctx)
	return r.MockStackInstanceRepository.FindByID(ctx, id)
}

func (r *hookInstanceRepo) Update(ctx context.Context, i *models.StackInstance) error {
	r.run("before:Update", ctx)
	defer r.run("after:Update", ctx)
	return r.MockStackInstanceRepository.Update(ctx, i)
}

func (r *hookInstanceRepo) Delete(ctx context.Context, id string) error {
	r.run("before:Delete", ctx)
	defer r.run("after:Delete", ctx)
	return r.MockStackInstanceRepository.Delete(ctx, id)
}

func (r *hookInstanceRepo) FindByCluster(ctx context.Context, clusterID string) ([]models.StackInstance, error) {
	r.run("before:FindByCluster", ctx)
	defer r.run("after:FindByCluster", ctx)
	return r.MockStackInstanceRepository.FindByCluster(ctx, clusterID)
}

func (*hookInstanceRepo) FindByIDs(ctx context.Context, _ []string) (map[string]*models.StackInstance, error) {
	return nil, ctx.Err()
}

func (r *hookInstanceRepo) ListExpiringSoon(ctx context.Context, threshold time.Duration) ([]*models.StackInstance, error) {
	r.run("before:ListExpiringSoon", ctx)
	defer r.run("after:ListExpiringSoon", ctx)
	return r.MockStackInstanceRepository.ListExpiringSoon(ctx, threshold)
}

// hookDefinitionRepo runs the hook of hooks with "before:Definition.<Method>"
// and "after:Definition.<Method>" around the definition reads and writes.
type hookDefinitionRepo struct {
	*MockStackDefinitionRepository
	hooks *hookInstanceRepo
}

func (r *hookDefinitionRepo) FindByID(ctx context.Context, id string) (*models.StackDefinition, error) {
	r.hooks.run("before:Definition.FindByID", ctx)
	defer r.hooks.run("after:Definition.FindByID", ctx)
	return r.MockStackDefinitionRepository.FindByID(ctx, id)
}

func (r *hookDefinitionRepo) FindByName(ctx context.Context, name string) ([]models.StackDefinition, error) {
	r.hooks.run("before:Definition.FindByName", ctx)
	defer r.hooks.run("after:Definition.FindByName", ctx)
	return r.MockStackDefinitionRepository.FindByName(ctx, name)
}

func (r *hookDefinitionRepo) Create(ctx context.Context, d *models.StackDefinition) error {
	r.hooks.run("before:Definition.Create", ctx)
	defer r.hooks.run("after:Definition.Create", ctx)
	return r.MockStackDefinitionRepository.Create(ctx, d)
}

func (r *hookDefinitionRepo) Update(ctx context.Context, d *models.StackDefinition) error {
	r.hooks.run("before:Definition.Update", ctx)
	defer r.hooks.run("after:Definition.Update", ctx)
	return r.MockStackDefinitionRepository.Update(ctx, d)
}

func (r *hookDefinitionRepo) SetTemplateLink(ctx context.Context, id, templateID, version string) error {
	r.hooks.run("before:Definition.SetTemplateLink", ctx)
	defer r.hooks.run("after:Definition.SetTemplateLink", ctx)
	return r.MockStackDefinitionRepository.SetTemplateLink(ctx, id, templateID, version)
}

func (r *hookDefinitionRepo) Delete(ctx context.Context, id string) error {
	r.hooks.run("before:Definition.Delete", ctx)
	defer r.hooks.run("after:Definition.Delete", ctx)
	return r.MockStackDefinitionRepository.Delete(ctx, id)
}

func (r *hookDefinitionRepo) ListByTemplate(ctx context.Context, templateID string) ([]models.StackDefinition, error) {
	r.hooks.run("before:Definition.ListByTemplate", ctx)
	defer r.hooks.run("after:Definition.ListByTemplate", ctx)
	return r.MockStackDefinitionRepository.ListByTemplate(ctx, templateID)
}

// cancelAt returns a hook that cancels the request at call number n of
// point (1-based).
func cancelAt(point string, n int32, cancel context.CancelFunc) func(string, context.Context) {
	var calls atomic.Int32
	return func(p string, _ context.Context) {
		if p == point && calls.Add(1) == n {
			cancel()
		}
	}
}

// liveRequest returns a request with a live context and its cancel.
func liveRequest(t *testing.T, method, path, body string) (*http.Request, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, cancel
}

// followerCtxNotifier returns its followers only for a live context, as the
// MySQL lookup does, and records each notification with its context error.
type followerCtxNotifier struct {
	followers []string
	mu        sync.Mutex
	types     []string
	targets   []models.NotificationTarget
	ctxErrs   []error
}

func (n *followerCtxNotifier) NotifyInstance(ctx context.Context, target models.NotificationTarget, notifType, _, _ string, _ ...notifier.InstanceOption) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.types = append(n.types, notifType)
	n.targets = append(n.targets, target)
	n.ctxErrs = append(n.ctxErrs, ctx.Err())
	return nil
}

func (n *followerCtxNotifier) FollowerIDs(ctx context.Context, _ string) []string {
	if ctx.Err() != nil {
		return nil
	}
	return n.followers
}

// notification returns the first notification of notifType.
func (n *followerCtxNotifier) notification(t *testing.T, notifType string) (models.NotificationTarget, error) {
	t.Helper()
	n.mu.Lock()
	defer n.mu.Unlock()
	for i, typ := range n.types {
		if typ == notifType {
			return n.targets[i], n.ctxErrs[i]
		}
	}
	t.Fatalf("no %s notification; got %v", notifType, n.types)
	return models.NotificationTarget{}, nil
}

// ctxFollowerRepo fails on a done context, as the GORM repository does.
type ctxFollowerRepo struct {
	*MockInstanceFollowerRepository
}

func (r ctxFollowerRepo) Follow(ctx context.Context, userID, instanceID string) error {
	if err := ctxDone(ctx, "follow"); err != nil {
		return err
	}
	return r.MockInstanceFollowerRepository.Follow(ctx, userID, instanceID)
}

func (r ctxFollowerRepo) FollowState(ctx context.Context, userID, instanceID string) (bool, int64, error) {
	if err := ctxDone(ctx, "follow_state"); err != nil {
		return false, 0, err
	}
	return r.MockInstanceFollowerRepository.FollowState(ctx, userID, instanceID)
}

// ctxQuotaOverrideRepo fails on a done context, as the GORM repository does.
type ctxQuotaOverrideRepo struct {
	*MockInstanceQuotaOverrideRepository
}

func (r ctxQuotaOverrideRepo) Upsert(ctx context.Context, o *models.InstanceQuotaOverride) error {
	if err := ctxDone(ctx, "upsert"); err != nil {
		return err
	}
	return r.MockInstanceQuotaOverrideRepository.Upsert(ctx, o)
}

func (r ctxQuotaOverrideRepo) GetByInstanceID(ctx context.Context, instanceID string) (*models.InstanceQuotaOverride, error) {
	if err := ctxDone(ctx, "get_by_instance_id"); err != nil {
		return nil, err
	}
	return r.MockInstanceQuotaOverrideRepository.GetByInstanceID(ctx, instanceID)
}

func (r ctxQuotaOverrideRepo) Delete(ctx context.Context, instanceID string) error {
	if err := ctxDone(ctx, "delete"); err != nil {
		return err
	}
	return r.MockInstanceQuotaOverrideRepository.Delete(ctx, instanceID)
}

// writeCtxFixture is an InstanceHandler over mocks that fail on a done
// context, with the audit middleware and a deploy manager.
type writeCtxFixture struct {
	inst      *hookInstanceRepo
	def       *hookDefinitionRepo
	charts    *hookChartConfigRepo
	branches  *hookBranchOverrideRepo
	logs      *MockDeploymentLogRepository
	ov        *hookValueOverrideRepo
	shared    *hookSharedValuesRepo
	followers *MockInstanceFollowerRepository
	notif     *followerCtxNotifier
	audit     *MockAuditLogRepository
	router    *gin.Engine
}

// newWriteCtxFixture seeds definition d1 (chart cc1) of uid-1 (alice) and
// the instances draft-1 (draft, TTL 60), run-1 (running), stopped-1
// (stopped) and ttl-1 (running, expires in 30 minutes).
func newWriteCtxFixture(t *testing.T) *writeCtxFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	f := &writeCtxFixture{
		inst:      newHookInstanceRepo(),
		logs:      NewMockDeploymentLogRepository(),
		followers: NewMockInstanceFollowerRepository(),
		notif:     &followerCtxNotifier{followers: []string{"f-1"}},
		audit:     NewMockAuditLogRepository(),
	}
	f.def = &hookDefinitionRepo{MockStackDefinitionRepository: NewMockStackDefinitionRepository(), hooks: f.inst}
	def := f.def
	f.ov = newHookValueOverrideRepo(f.inst)
	f.shared = newHookSharedValuesRepo(f.inst)
	f.charts = newHookChartConfigRepo(f.inst)
	f.branches = newHookBranchOverrideRepo(f.inst)
	cc, bo := f.charts, f.branches
	seedDefinition(t, def.MockStackDefinitionRepository, "d1", "def-1", "uid-1")
	require.NoError(t, cc.Create(context.Background(), &models.ChartConfig{ID: "cc1", StackDefinitionID: "d1", ChartName: "web", DefaultValues: "replicas: 1\n"}))
	expires := time.Now().UTC().Add(30 * time.Minute)
	for _, s := range []struct {
		expiresAt *time.Time
		id        string
		status    string
		ttl       int
	}{
		{id: "draft-1", status: models.StackStatusDraft, ttl: 60},
		{id: "run-1", status: models.StackStatusRunning},
		{id: "stopped-1", status: models.StackStatusStopped},
		{id: "ttl-1", status: models.StackStatusRunning, ttl: 60, expiresAt: &expires},
	} {
		require.NoError(t, f.inst.Create(context.Background(), &models.StackInstance{
			ID: s.id, StackDefinitionID: "d1", Name: s.id, Namespace: "stack-" + s.id + "-alice",
			OwnerID: "uid-1", Branch: "master", Status: s.status, TTLMinutes: s.ttl, ExpiresAt: s.expiresAt,
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}))
	}

	mgr := newTestManager(f.inst, f.logs)
	t.Cleanup(mgr.Shutdown)
	h, err := NewInstanceHandlerWithDeployer(
		f.inst, f.ov, bo, def, cc,
		NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
		helm.NewValuesGenerator(), NewMockUserRepository(),
		mgr, nil, nil, f.logs, nil, 0,
		&mockHandlerTxRunner{repos: database.TxRepos{
			StackDefinition:  def,
			ChartConfig:      cc,
			StackInstance:    f.inst,
			ValueOverride:    f.ov,
			BranchOverride:   bo,
			DeploymentLog:    f.logs,
			InstanceFollower: f.followers,
		}},
	)
	require.NoError(t, err)
	h.WithNotifier(f.notif)
	h.WithFollowers(ctxFollowerRepo{f.followers})
	h.WithSharedValues(f.shared)

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userID", "uid-1")
		c.Set("username", "alice")
		c.Set("role", "user")
		c.Next()
	}, middleware.NewAuditMiddleware(f.audit))
	g := r.Group("/api/v1/stack-instances")
	g.POST("", h.CreateInstance)
	g.PUT("/:id", h.UpdateInstance)
	g.DELETE("/:id", h.DeleteInstance)
	g.POST("/:id/deploy", h.DeployInstance)
	g.POST("/:id/stop", h.StopInstance)
	g.POST("/:id/clean", h.CleanInstance)
	g.POST("/:id/rollback", h.RollbackInstance)
	g.POST("/:id/extend", h.ExtendTTL)
	g.POST("/:id/clone", h.CloneInstance)
	g.POST("/:id/follow", h.FollowInstance)
	g.PUT("/:id/overrides/:chartId", h.SetOverride)
	g.POST("/bulk/deploy", h.BulkDeploy)
	g.POST("/bulk/stop", h.BulkStop)
	g.POST("/bulk/clean", h.BulkClean)
	g.POST("/bulk/delete", h.BulkDelete)
	f.router = r
	return f
}

func (f *writeCtxFixture) instance(t *testing.T, id string) *models.StackInstance {
	t.Helper()
	inst, err := f.inst.FindByID(context.Background(), id)
	require.NoError(t, err)
	return inst
}

func (f *writeCtxFixture) logActions(t *testing.T, id string) []string {
	t.Helper()
	logs, err := f.logs.ListByInstance(context.Background(), id)
	require.NoError(t, err)
	out := make([]string, 0, len(logs))
	for _, l := range logs {
		out = append(out, l.Action)
	}
	return out
}

func bulkResults(t *testing.T, w *httptest.ResponseRecorder) []BulkOperationResultItem {
	t.Helper()
	var resp BulkOperationResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	return resp.Results
}

// TestInstanceWrites_CancelledAfterReadsStillCommit checks that a client that
// leaves after the reads of an instance write cannot stop the write after it
// started, nor the side effects and the audit entry after it (#656). The
// mocks fail on a done context, so a write with the request context fails.
func TestInstanceWrites_CancelledAfterReadsStillCommit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		check      func(t *testing.T, f *writeCtxFixture, w *httptest.ResponseRecorder)
		name       string
		method     string
		path       string
		body       string
		cancelAt   string
		wantStatus int
	}{
		{
			name: "create", method: http.MethodPost, path: "/api/v1/stack-instances",
			body: `{"stack_definition_id":"d1","name":"new-one"}`, cancelAt: "before:Create", wantStatus: http.StatusCreated,
			check: func(t *testing.T, f *writeCtxFixture, w *httptest.ResponseRecorder) {
				var inst models.StackInstance
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &inst))
				assert.Equal(t, "new-one", f.instance(t, inst.ID).Name)
				_, ctxErr := f.notif.notification(t, "instance.created")
				assert.NoError(t, ctxErr)
			},
		},
		{
			name: "update", method: http.MethodPut, path: "/api/v1/stack-instances/draft-1",
			body: `{"branch":"feature-x"}`, cancelAt: "before:Update", wantStatus: http.StatusOK,
			check: func(t *testing.T, f *writeCtxFixture, _ *httptest.ResponseRecorder) {
				assert.Equal(t, "feature-x", f.instance(t, "draft-1").Branch)
			},
		},
		{
			name: "deploy", method: http.MethodPost, path: "/api/v1/stack-instances/draft-1/deploy",
			cancelAt: "before:Update", wantStatus: http.StatusAccepted,
			check: func(t *testing.T, f *writeCtxFixture, _ *httptest.ResponseRecorder) {
				assert.NotNil(t, f.instance(t, "draft-1").ExpiresAt, "the TTL expiry is written")
				assert.Equal(t, []string{models.DeployActionDeploy}, f.logActions(t, "draft-1"))
			},
		},
		{
			name: "stop", method: http.MethodPost, path: "/api/v1/stack-instances/run-1/stop",
			cancelAt: "after:Chart.ListByDefinition", wantStatus: http.StatusAccepted,
			check: func(t *testing.T, f *writeCtxFixture, _ *httptest.ResponseRecorder) {
				assert.Equal(t, []string{models.DeployActionStop}, f.logActions(t, "run-1"))
			},
		},
		{
			name: "clean", method: http.MethodPost, path: "/api/v1/stack-instances/stopped-1/clean",
			cancelAt: "after:Chart.ListByDefinition", wantStatus: http.StatusAccepted,
			check: func(t *testing.T, f *writeCtxFixture, _ *httptest.ResponseRecorder) {
				assert.Equal(t, []string{models.DeployActionClean}, f.logActions(t, "stopped-1"))
			},
		},
		{
			name: "rollback", method: http.MethodPost, path: "/api/v1/stack-instances/run-1/rollback",
			cancelAt: "after:Chart.ListByDefinition", wantStatus: http.StatusAccepted,
			check: func(t *testing.T, f *writeCtxFixture, _ *httptest.ResponseRecorder) {
				assert.Equal(t, []string{models.DeployActionRollback}, f.logActions(t, "run-1"))
			},
		},
		{
			name: "delete notifies the followers", method: http.MethodDelete, path: "/api/v1/stack-instances/draft-1",
			cancelAt: "after:FindByID", wantStatus: http.StatusNoContent,
			check: func(t *testing.T, f *writeCtxFixture, _ *httptest.ResponseRecorder) {
				_, err := f.inst.FindByID(context.Background(), "draft-1")
				assert.Error(t, err, "the instance is deleted")
				target, ctxErr := f.notif.notification(t, "instance.deleted")
				assert.NoError(t, ctxErr)
				assert.Equal(t, []string{"f-1"}, target.FollowerIDs, "the followers are read before the delete")
			},
		},
		{
			name: "delete with a clean", method: http.MethodDelete, path: "/api/v1/stack-instances/run-1",
			cancelAt: "after:FindByID", wantStatus: http.StatusAccepted,
			check: func(t *testing.T, f *writeCtxFixture, _ *httptest.ResponseRecorder) {
				assert.Equal(t, []string{models.DeployActionClean}, f.logActions(t, "run-1"))
			},
		},
		{
			name: "extend", method: http.MethodPost, path: "/api/v1/stack-instances/ttl-1/extend",
			body: `{"minutes":30}`, cancelAt: "before:Update", wantStatus: http.StatusOK,
			check: func(t *testing.T, f *writeCtxFixture, _ *httptest.ResponseRecorder) {
				exp := f.instance(t, "ttl-1").ExpiresAt
				require.NotNil(t, exp)
				assert.True(t, exp.After(time.Now().Add(55*time.Minute)), "the expiry moved: %s", exp)
			},
		},
		{
			name: "clone", method: http.MethodPost, path: "/api/v1/stack-instances/draft-1/clone",
			body: `{"name":"copy-one"}`, cancelAt: "before:Create", wantStatus: http.StatusCreated,
			check: func(t *testing.T, f *writeCtxFixture, w *httptest.ResponseRecorder) {
				var resp CloneInstanceResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, "copy-one", f.instance(t, resp.ID).Name)
				_, ctxErr := f.notif.notification(t, "instance.created")
				assert.NoError(t, ctxErr)
			},
		},
		{
			name: "bulk deploy", method: http.MethodPost, path: "/api/v1/stack-instances/bulk/deploy",
			body: `{"instance_ids":["draft-1"]}`, cancelAt: "before:Update", wantStatus: http.StatusOK,
			check: func(t *testing.T, f *writeCtxFixture, w *httptest.ResponseRecorder) {
				results := bulkResults(t, w)
				require.Len(t, results, 1)
				assert.Equal(t, "success", results[0].Status, results[0].Error)
				assert.NotNil(t, f.instance(t, "draft-1").ExpiresAt, "the TTL expiry is written")
				assert.Equal(t, []string{models.DeployActionDeploy}, f.logActions(t, "draft-1"))
			},
		},
		{
			name: "bulk clean", method: http.MethodPost, path: "/api/v1/stack-instances/bulk/clean",
			body: `{"instance_ids":["stopped-1"]}`, cancelAt: "after:Chart.ListByDefinition", wantStatus: http.StatusOK,
			check: func(t *testing.T, f *writeCtxFixture, w *httptest.ResponseRecorder) {
				results := bulkResults(t, w)
				require.Len(t, results, 1)
				assert.Equal(t, "success", results[0].Status, results[0].Error)
				assert.Equal(t, []string{models.DeployActionClean}, f.logActions(t, "stopped-1"))
			},
		},
		{
			name: "bulk stop", method: http.MethodPost, path: "/api/v1/stack-instances/bulk/stop",
			body: `{"instance_ids":["run-1"]}`, cancelAt: "after:Chart.ListByDefinition", wantStatus: http.StatusOK,
			check: func(t *testing.T, f *writeCtxFixture, w *httptest.ResponseRecorder) {
				results := bulkResults(t, w)
				require.Len(t, results, 1)
				assert.Equal(t, "success", results[0].Status, results[0].Error)
				assert.Equal(t, []string{models.DeployActionStop}, f.logActions(t, "run-1"))
			},
		},
		{
			name: "bulk delete", method: http.MethodPost, path: "/api/v1/stack-instances/bulk/delete",
			body: `{"instance_ids":["draft-1"]}`, cancelAt: "after:FindByID", wantStatus: http.StatusOK,
			check: func(t *testing.T, f *writeCtxFixture, w *httptest.ResponseRecorder) {
				results := bulkResults(t, w)
				require.Len(t, results, 1)
				assert.Equal(t, "success", results[0].Status, results[0].Error)
				target, ctxErr := f.notif.notification(t, "instance.deleted")
				assert.NoError(t, ctxErr)
				assert.Equal(t, []string{"f-1"}, target.FollowerIDs)
			},
		},
		{
			name: "follow", method: http.MethodPost, path: "/api/v1/stack-instances/draft-1/follow",
			cancelAt: "after:FindByID", wantStatus: http.StatusOK,
			check: func(t *testing.T, f *writeCtxFixture, _ *httptest.ResponseRecorder) {
				following, _, err := f.followers.FollowState(context.Background(), "uid-1", "draft-1")
				require.NoError(t, err)
				assert.True(t, following)
			},
		},
		{
			name: "value override", method: http.MethodPut, path: "/api/v1/stack-instances/draft-1/overrides/cc1",
			body: `{"values":"replicas: 2\n"}`, cancelAt: "after:Override.FindByInstanceAndChart", wantStatus: http.StatusOK,
			check: func(t *testing.T, f *writeCtxFixture, _ *httptest.ResponseRecorder) {
				ov, err := f.ov.FindByInstanceAndChart(context.Background(), "draft-1", "cc1")
				require.NoError(t, err)
				assert.Equal(t, "replicas: 2\n", ov.Values)
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newWriteCtxFixture(t)
			req, cancel := liveRequest(t, tt.method, tt.path, tt.body)
			f.inst.setHook(cancelAt(tt.cancelAt, 1, cancel))

			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			require.Error(t, req.Context().Err(), "the request was cancelled during the handler")
			tt.check(t, f, w)
			assert.Eventually(t, func() bool { return len(f.audit.snapshot()) == 1 },
				2*time.Second, 10*time.Millisecond, "one audit entry for the write")
		})
	}
}

// TestSetQuotaOverride_CancelledAfterReadStillCommits checks that a quota
// override write whose client left after the instance read still commits
// and answers 200 (#656).
func TestSetQuotaOverride_CancelledAfterReadStillCommits(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	instRepo := newHookInstanceRepo()
	seedInstance(t, instRepo.MockStackInstanceRepository, "i1", "zz-one", "d1", "uid-1", models.StackStatusDraft)
	overrides := NewMockInstanceQuotaOverrideRepository()
	h := NewInstanceQuotaOverrideHandler(ctxQuotaOverrideRepo{overrides}, instRepo)
	r := gin.New()
	r.Use(injectAuthContext("uid-1", "user"))
	r.PUT("/api/v1/stack-instances/:id/quota-overrides", h.SetQuotaOverride)
	r.DELETE("/api/v1/stack-instances/:id/quota-overrides", h.DeleteQuotaOverride)

	req, cancel := liveRequest(t, http.MethodPut, "/api/v1/stack-instances/i1/quota-overrides", `{"cpu_limit":"2"}`)
	instRepo.setHook(cancelAt("after:FindByID", 1, cancel))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	got, err := overrides.GetByInstanceID(context.Background(), "i1")
	require.NoError(t, err)
	assert.Equal(t, "2", got.CPULimit)

	req, cancel = liveRequest(t, http.MethodDelete, "/api/v1/stack-instances/i1/quota-overrides", "")
	instRepo.setHook(cancelAt("after:FindByID", 1, cancel))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	_, err = overrides.GetByInstanceID(context.Background(), "i1")
	assert.Error(t, err, "the override is deleted")
}

// TestQuickDeploy_CancelledAfterCreateStillDeploys checks that a quick
// deploy whose client left at the instance create still creates the
// instance, starts the deploy and writes its audit entry (#656).
func TestQuickDeploy_CancelledAfterCreateStillDeploys(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	tmplRepo := NewMockStackTemplateRepository()
	tmplChartRepo := NewMockTemplateChartConfigRepository()
	defRepo := NewMockStackDefinitionRepository()
	ccRepo := NewMockChartConfigRepository()
	instRepo := newHookInstanceRepo()
	boRepo := NewMockChartBranchOverrideRepository()
	ovRepo := NewMockValueOverrideRepository()
	logRepo := NewMockDeploymentLogRepository()
	audit := NewMockAuditLogRepository()
	seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)
	require.NoError(t, tmplChartRepo.Create(context.Background(), &models.TemplateChartConfig{
		ID: "tc1", StackTemplateID: "t1", ChartName: "nginx", RepositoryURL: "oci://example.com/charts/nginx", DeployOrder: 1,
	}))
	mgr := newTestManager(instRepo, logRepo)
	t.Cleanup(mgr.Shutdown)
	h, err := NewQuickDeployHandler(
		tmplRepo, tmplChartRepo, defRepo, ccRepo, instRepo, boRepo, ovRepo, helm.NewValuesGenerator(),
		mgr, NewMockUserRepository(), logRepo, audit,
		&MockBroadcastSender{}, cluster.NewRegistryForTest("test-cluster", nil, &noopHelmExecutor{}), nil, 0,
		&mockHandlerTxRunner{repos: database.TxRepos{
			StackDefinition: defRepo, ChartConfig: ccRepo, StackInstance: instRepo,
			ValueOverride: ovRepo, BranchOverride: boRepo,
		}},
	)
	require.NoError(t, err)
	notif := &followerCtxNotifier{}
	h.WithTemplateVersions(newWorkingCopyReleaseRepo(tmplRepo, tmplChartRepo)).WithNotifier(notif)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userID", "uid-1")
		c.Set("username", "alice")
		c.Set("role", "user")
		c.Next()
	})
	r.POST("/api/v1/templates/:id/quick-deploy", h.QuickDeploy)

	req, cancel := liveRequest(t, http.MethodPost, "/api/v1/templates/t1/quick-deploy", `{"instance_name":"qd-cancel"}`)
	instRepo.setHook(cancelAt("before:Create", 1, cancel))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	var resp quickDeployResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.LogID, "the deploy started")
	got, err := instRepo.FindByID(context.Background(), resp.Instance.ID)
	require.NoError(t, err)
	assert.NotEqual(t, models.StackStatusError, got.Status)
	_, ctxErr := notif.notification(t, "instance.created")
	assert.NoError(t, ctxErr)
	assert.Len(t, audit.snapshot(), 1, "quick deploy writes its own audit entry")
}

// TestCreateInstance_CancelledAtCreateStillRunsSideEffects checks that a
// create whose client left at the row write still commits, fires
// post-instance-create with the user trigger and sends instance.created
// (notification and WebSocket), also in the transaction of the instance
// limit (#656).
func TestCreateInstance_CancelledAtCreateStillRunsSideEffects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		maxInstances int
		cancelled    bool
	}{
		{name: "live request"},
		{name: "cancelled at the create", cancelled: true},
		{name: "cancelled at the create in the limit transaction", maxInstances: 5, cancelled: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gin.SetMode(gin.TestMode)
			rec := newHandlerHookRecorder(t)
			instRepo := newHookInstanceRepo()
			defRepo := NewMockStackDefinitionRepository()
			seedDefinition(t, defRepo, "d1", "def-1", "uid-1")
			clusterRepo := NewMockClusterRepository()
			require.NoError(t, clusterRepo.Create(context.Background(), &models.Cluster{ID: "cl-1", Name: "one", MaxInstancesPerUser: tt.maxInstances}))
			hub := &MockBroadcastSender{}
			mgr := deployer.NewManager(deployer.ManagerConfig{
				InstanceRepo: instRepo, DeployLogRepo: NewMockDeploymentLogRepository(), Hub: hub, MaxConcurrent: 1,
			})
			t.Cleanup(mgr.Shutdown)
			h := NewInstanceHandler(instRepo, NewMockValueOverrideRepository(), NewMockChartBranchOverrideRepository(),
				defRepo, NewMockChartConfigRepository(), NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
				helm.NewValuesGenerator(), NewMockUserRepository(), 0)
			h.deployManager = mgr
			h.clusterRepo = clusterRepo
			h.txRunner = &mockHandlerTxRunner{repos: database.TxRepos{StackInstance: instRepo, StackDefinition: defRepo}}
			h.WithHooks(rec.dispatcher(t, hooks.FailurePolicyIgnore))
			notif := &ctxNotifier{}
			h.WithNotifier(notif)
			r := gin.New()
			r.Use(injectAuthContext("uid-1", "user"), func(c *gin.Context) {
				c.Set("username", "alice")
				c.Next()
			})
			r.POST("/api/v1/stack-instances", h.CreateInstance)

			req, cancel := liveRequest(t, http.MethodPost, "/api/v1/stack-instances",
				`{"stack_definition_id":"d1","name":"created-one","cluster_id":"cl-1"}`)
			if tt.cancelled {
				instRepo.setHook(cancelAt("before:Create", 1, cancel))
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
			assert.Equal(t, tt.cancelled, req.Context().Err() != nil)
			var created models.StackInstance
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
			stored, err := instRepo.FindByID(context.Background(), created.ID)
			require.NoError(t, err, "the instance is stored")
			assert.Equal(t, "created-one", stored.Name)

			want := hooks.Trigger{Type: hooks.TriggerUser, ID: "uid-1", Name: "alice"}
			assert.Equal(t, []string{hooks.EventPreInstanceCreate, hooks.EventPostInstanceCreate}, rec.names(),
				"post-instance-create reaches the subscriber")
			rec.mu.Lock()
			for _, e := range rec.events {
				if assert.NotNil(t, e.envelope.Trigger, e.event) {
					assert.Equal(t, want, *e.envelope.Trigger, e.event)
				}
			}
			rec.mu.Unlock()

			notif.mu.Lock()
			require.Equal(t, []string{"instance.created"}, notif.types)
			assert.NoError(t, notif.ctxErrs[0])
			require.NotNil(t, notif.triggers[0])
			assert.Equal(t, want, *notif.triggers[0])
			notif.mu.Unlock()

			var types []string
			for _, raw := range hub.Messages() {
				var msg struct {
					Type string `json:"type"`
				}
				require.NoError(t, json.Unmarshal(raw, &msg))
				types = append(types, msg.Type)
			}
			assert.Contains(t, types, "instance.created", "the WebSocket message is sent")
		})
	}
}

// TestInstanceReads_CancelledRequest checks that instance reads for a client
// that left answer 499, and that a passed deadline answers 500 (#656).
func TestInstanceReads_CancelledRequest(t *testing.T) {
	t.Parallel()

	seeded := func(t *testing.T) *MockStackInstanceRepository {
		repo := NewMockStackInstanceRepository()
		seedInstance(t, repo, "i1", "zz-one", "d1", "uid-1", models.StackStatusRunning)
		seedInstance(t, repo, "i2", "zz-two", "d1", "uid-1", models.StackStatusRunning)
		return repo
	}
	instanceRouter := func(t *testing.T) *gin.Engine {
		return valuesCancelRouterFor(t, seeded(t))
	}
	analyticsRouter := func(t *testing.T) *gin.Engine {
		defRepo := NewMockStackDefinitionRepository()
		tmplRepo := NewMockStackTemplateRepository()
		seedTemplate(t, tmplRepo, "t1", "tmpl", "uid-1", true)
		require.NoError(t, defRepo.Create(context.Background(), &models.StackDefinition{ID: "d1", Name: "def", OwnerID: "uid-1", SourceTemplateID: "t1"}))
		return setupAnalyticsRouter(tmplRepo, defRepo, seeded(t), newMockDeployLogRepo(), NewMockUserRepository())
	}
	adminRouter := func(t *testing.T) *gin.Engine {
		cs := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-orphan-u"}})
		return setupAdminRouter(k8s.NewClientFromInterface(cs), &mockAdminHelmExecutor{}, seeded(t), "admin")
	}

	tests := []struct {
		router func(t *testing.T) *gin.Engine
		name   string
		path   string
	}{
		{name: "get", router: instanceRouter, path: "/api/v1/stack-instances/i1"},
		{name: "list", router: instanceRouter, path: "/api/v1/stack-instances"},
		{name: "recent", router: instanceRouter, path: "/api/v1/stack-instances/recent"},
		{name: "compare", router: instanceRouter, path: "/api/v1/stack-instances/compare?left=i1&right=i2"},
		{name: "deploy preview", router: instanceRouter, path: "/api/v1/stack-instances/i1/deploy-preview"},
		{name: "status", router: instanceRouter, path: "/api/v1/stack-instances/i1/status"},
		{name: "analytics overview", router: analyticsRouter, path: "/api/v1/analytics/overview"},
		{name: "analytics template stats", router: analyticsRouter, path: "/api/v1/analytics/templates"},
		{name: "admin orphaned namespaces", router: adminRouter, path: "/api/v1/admin/orphaned-namespaces"},
	}
	ends := []struct {
		req        func(t *testing.T, path string) *http.Request
		name       string
		wantBody   string
		wantStatus int
	}{
		{
			name: "cancelled", wantStatus: statusClientClosedRequest, wantBody: `{"error":"Client closed request"}`,
			req: func(t *testing.T, path string) *http.Request {
				return cancelledRequest(t, http.MethodGet, path, "")
			},
		},
		{
			name: "deadline exceeded", wantStatus: http.StatusInternalServerError, wantBody: `{"error":"Internal server error"}`,
			req: func(t *testing.T, path string) *http.Request {
				ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				t.Cleanup(cancel)
				return httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
			},
		},
	}

	for _, tt := range tests {
		for _, end := range ends {
			tt, end := tt, end
			t.Run(tt.name+", "+end.name, func(t *testing.T) {
				t.Parallel()
				w := httptest.NewRecorder()
				tt.router(t).ServeHTTP(w, end.req(t, tt.path))

				assert.Equal(t, end.wantStatus, w.Code, w.Body.String())
				assert.JSONEq(t, end.wantBody, w.Body.String())
			})
		}
	}
}

// valuesCancelRouterFor returns the instance read routes over instRepo.
func valuesCancelRouterFor(t *testing.T, instRepo *MockStackInstanceRepository) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	defRepo := NewMockStackDefinitionRepository()
	ccRepo := NewMockChartConfigRepository()
	seedDefinition(t, defRepo, "d1", "def-1", "uid-1")
	require.NoError(t, ccRepo.Create(context.Background(), &models.ChartConfig{ID: "cc1", StackDefinitionID: "d1", ChartName: "web"}))
	h := NewInstanceHandler(instRepo, NewMockValueOverrideRepository(), NewMockChartBranchOverrideRepository(),
		defRepo, ccRepo, NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
		helm.NewValuesGenerator(), NewMockUserRepository(), 0)
	r := gin.New()
	r.Use(injectAuthContext("uid-1", "user"))
	g := r.Group("/api/v1/stack-instances")
	g.GET("", h.ListInstances)
	g.GET("/recent", h.GetRecentInstances)
	g.GET("/compare", h.CompareInstances)
	g.GET("/:id", h.GetInstance)
	g.GET("/:id/deploy-preview", h.DeployPreview)
	g.GET("/:id/status", h.GetInstanceStatus)
	return r
}

// TestDeleteDefinition_CancelledInstanceCheck checks that a definition
// delete whose client left during the running-instance check answers 499
// and keeps the definition: the failed check must not let the delete
// through (#656).
func TestDeleteDefinition_CancelledInstanceCheck(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	defRepo := NewMockStackDefinitionRepository()
	seedDefinition(t, defRepo, "d1", "def-1", "uid-1")
	h := NewDefinitionHandler(defRepo, NewMockChartConfigRepository(), NewMockStackInstanceRepository(),
		NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository())
	r := gin.New()
	r.Use(injectAuthContext("uid-1", "user"))
	r.DELETE("/api/v1/stack-definitions/:id", h.DeleteDefinition)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, cancelledRequest(t, http.MethodDelete, "/api/v1/stack-definitions/d1", ""))

	assert.Equal(t, statusClientClosedRequest, w.Code, w.Body.String())
	_, err := defRepo.FindByID(context.Background(), "d1")
	assert.NoError(t, err, "the definition is kept")
}

// TestGetDashboard_CancelledFirstCallerDoesNotFailWaiters checks that the
// shared dashboard build does not use the context of the first caller: a
// waiter still gets the dashboard when the first caller leaves (#656).
func TestGetDashboard_CancelledFirstCallerDoesNotFailWaiters(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		instRepo := newHookInstanceRepo()
		soon := time.Now().Add(20 * time.Minute)
		require.NoError(t, instRepo.Create(context.Background(), &models.StackInstance{
			ID: "exp-1", Name: "exp-1", Status: models.StackStatusRunning, ExpiresAt: &soon, TTLMinutes: 60,
		}))
		require.NoError(t, instRepo.Create(context.Background(), &models.StackInstance{
			ID: "err-1", Name: "err-1", Status: models.StackStatusError, ErrorMessage: "boom",
		}))
		h := NewDashboardHandler(NewMockClusterRepository(), instRepo, newMockDeployLogRepo(), nil)
		defer h.Stop()

		entered := make(chan struct{})
		release := make(chan struct{})
		var builds atomic.Int32
		instRepo.setHook(func(point string, _ context.Context) {
			if point == "before:ListExpiringSoon" && builds.Add(1) == 1 {
				close(entered)
				<-release
			}
		})
		r := gin.New()
		r.Use(injectAuthContext("uid-1", "user"))
		r.GET("/api/v1/dashboard", h.GetDashboard)

		first, cancelFirst := liveRequest(t, http.MethodGet, "/api/v1/dashboard", "")
		wFirst, wWaiter := httptest.NewRecorder(), httptest.NewRecorder()
		var wg sync.WaitGroup
		wg.Go(func() { r.ServeHTTP(wFirst, first) })
		<-entered
		wg.Go(func() { r.ServeHTTP(wWaiter, httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)) })
		// The waiter blocks in the shared build of the first caller.
		synctest.Wait()
		cancelFirst()
		close(release)
		wg.Wait()

		assert.Equal(t, int32(1), builds.Load(), "the waiter shares the build of the first caller")
		require.Equal(t, http.StatusOK, wWaiter.Code, wWaiter.Body.String())
		var resp DashboardResponse
		require.NoError(t, json.Unmarshal(wWaiter.Body.Bytes(), &resp))
		require.Len(t, resp.ExpiringSoon, 1)
		assert.Equal(t, "exp-1", resp.ExpiringSoon[0].ID)
		require.Len(t, resp.FailingInstances, 1)
		assert.Equal(t, "err-1", resp.FailingInstances[0].ID)
	})
}

// TestUpdateQuotas_PropagationUsesItsOwnContext checks that the background
// quota write after a cluster quota save reads the instances with its own
// bounded context, not the request context (#656).
func TestUpdateQuotas_PropagationUsesItsOwnContext(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	registry, _ := newQuotaTestRegistry("stack-one-u")
	instRepo := newHookInstanceRepo()
	clusterRepo := NewMockClusterRepository()
	require.NoError(t, clusterRepo.Create(context.Background(), &models.Cluster{ID: "cl-1", Name: "one", IsDefault: true}))
	h := NewClusterHandlerWithQuotas(clusterRepo, registry, instRepo, NewMockResourceQuotaRepository())

	type seen struct {
		err      error
		deadline time.Time
		ok       bool
	}
	requestDone := make(chan struct{})
	got := make(chan seen, 2)
	instRepo.setHook(func(point string, ctx context.Context) {
		if point != "before:FindByCluster" {
			return
		}
		<-requestDone
		d, ok := ctx.Deadline()
		got <- seen{err: ctx.Err(), deadline: d, ok: ok}
	})
	r := gin.New()
	r.Use(injectAuthContext("admin-1", "admin"))
	r.PUT("/api/v1/clusters/:id/quotas", h.UpdateQuotas)

	req, cancel := liveRequest(t, http.MethodPut, "/api/v1/clusters/cl-1/quotas", `{"cpu_limit":"4"}`)
	start := time.Now()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	cancel()
	close(requestDone)

	select {
	case s := <-got:
		assert.NoError(t, s.err, "the request cancel does not reach the background write")
		require.True(t, s.ok, "the background context has a deadline")
		assert.WithinDuration(t, start.Add(quotaPropagationTimeout), s.deadline, 5*time.Second)
	case <-time.After(5 * time.Second):
		t.Fatal("the background quota write did not read the instances")
	}
}

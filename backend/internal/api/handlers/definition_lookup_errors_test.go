package handlers

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"backend/internal/cluster"
	"backend/internal/database"
	"backend/internal/helm"
	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// endableCtx is a request context that the test ends with a chosen error
// (context.Canceled or context.DeadlineExceeded) at a chosen point.
type endableCtx struct {
	context.Context
	done chan struct{}
	once sync.Once
	mu   sync.Mutex
	err  error
}

func newEndableCtx() *endableCtx {
	return &endableCtx{Context: context.Background(), done: make(chan struct{})}
}

func (c *endableCtx) Done() <-chan struct{} { return c.done }

func (c *endableCtx) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *endableCtx) end(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		close(c.done)
	})
}

// ctxReleaseRepo fails the release read on a done context, as the GORM
// repository does.
type ctxReleaseRepo struct {
	models.TemplateVersionRepository
}

func (r ctxReleaseRepo) GetLatestByTemplate(ctx context.Context, templateID string) (*models.TemplateVersion, error) {
	if err := ctxDone(ctx, "get_latest_by_template"); err != nil {
		return nil, err
	}
	return r.TemplateVersionRepository.GetLatestByTemplate(ctx, templateID)
}

// captureLogs sends the default slog logger to a buffer at debug level until
// the test ends. The returned func returns the lines so far and empties the
// buffer. Tests that call it must not be parallel.
func captureLogs(t *testing.T) func() string {
	t.Helper()
	var buf bytes.Buffer
	var mu sync.Mutex
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&lockedWriter{mu: &mu, w: &buf}, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		s := buf.String()
		buf.Reset()
		return s
	}
}

// TestQuickDeploy_ReleaseReadContext checks the release read of quick deploy,
// before the first write (#656): a cancelled read answers 499, a passed
// deadline 500, and nothing is created.
func TestQuickDeploy_ReleaseReadContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		endErr     error
		name       string
		wantBody   string
		wantStatus int
	}{
		{name: "live", wantStatus: http.StatusAccepted},
		{name: "cancelled", endErr: context.Canceled, wantStatus: statusClientClosedRequest, wantBody: `{"error":"Client closed request"}`},
		{name: "deadline exceeded", endErr: context.DeadlineExceeded, wantStatus: http.StatusInternalServerError, wantBody: `{"error":"Internal server error"}`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gin.SetMode(gin.TestMode)
			tmplRepo := NewMockStackTemplateRepository()
			tmplChartRepo := NewMockTemplateChartConfigRepository()
			instRepo := newHookInstanceRepo()
			defRepo := &hookDefinitionRepo{MockStackDefinitionRepository: NewMockStackDefinitionRepository(), hooks: instRepo}
			ccRepo := NewMockChartConfigRepository()
			boRepo := NewMockChartBranchOverrideRepository()
			ovRepo := NewMockValueOverrideRepository()
			logRepo := NewMockDeploymentLogRepository()
			seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)
			require.NoError(t, tmplChartRepo.Create(&models.TemplateChartConfig{
				ID: "tc1", StackTemplateID: "t1", ChartName: "nginx", RepositoryURL: "oci://example.com/charts/nginx", DeployOrder: 1,
			}))
			mgr := newTestManager(instRepo, logRepo)
			t.Cleanup(mgr.Shutdown)
			h, err := NewQuickDeployHandler(
				tmplRepo, tmplChartRepo, defRepo, ccRepo, instRepo, boRepo, ovRepo, helm.NewValuesGenerator(),
				mgr, NewMockUserRepository(), logRepo, NewMockAuditLogRepository(),
				&MockBroadcastSender{}, cluster.NewRegistryForTest("test-cluster", nil, &noopHelmExecutor{}), nil, 0,
				&mockHandlerTxRunner{repos: database.TxRepos{
					StackDefinition: defRepo, ChartConfig: ccRepo, StackInstance: instRepo,
					ValueOverride: ovRepo, BranchOverride: boRepo,
				}},
			)
			require.NoError(t, err)
			h.WithTemplateVersions(ctxReleaseRepo{newWorkingCopyReleaseRepo(tmplRepo, tmplChartRepo)}).WithNotifier(&followerCtxNotifier{})
			r := gin.New()
			r.Use(injectAuthContext("uid-1", "user"))
			r.POST("/api/v1/templates/:id/quick-deploy", h.QuickDeploy)
			var repoCalls sync.Map
			instRepo.setHook(func(point string, _ context.Context) { repoCalls.Store(point, true) })

			ctx := newEndableCtx()
			if tt.endErr != nil {
				ctx.end(tt.endErr)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/templates/t1/quick-deploy", strings.NewReader(`{"instance_name":"qd-rel"}`)).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			defs, err := defRepo.MockStackDefinitionRepository.List(context.Background())
			require.NoError(t, err)
			if tt.endErr == nil {
				assert.Len(t, defs, 1, "the definition is stored")
				return
			}
			assert.JSONEq(t, tt.wantBody, w.Body.String())
			assert.Empty(t, defs, "no definition is created")
			all, err := instRepo.List(context.Background())
			require.NoError(t, err)
			assert.Empty(t, all, "no instance is created")
			_, nameChecked := repoCalls.Load("before:Definition.FindByName")
			assert.False(t, nameChecked, "the release read ends the request")
		})
	}
}

// TestSetTemplateLink_ReleaseReadContext checks the release read of the
// template link, after the definition read and before the write (#656): a
// cancelled read answers 499, a passed deadline 500, and the link is not set.
func TestSetTemplateLink_ReleaseReadContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		endErr     error
		name       string
		wantBody   string
		wantStatus int
	}{
		{name: "cancelled", endErr: context.Canceled, wantStatus: statusClientClosedRequest, wantBody: `{"error":"Client closed request"}`},
		{name: "deadline exceeded", endErr: context.DeadlineExceeded, wantStatus: http.StatusInternalServerError, wantBody: `{"error":"Internal server error"}`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newDefCtxFixture(t)
			ctx := newEndableCtx()
			var liveAtRead, linkWritten bool
			var mu sync.Mutex
			f.hooks.setHook(func(point string, hctx context.Context) {
				mu.Lock()
				defer mu.Unlock()
				switch point {
				case "after:Definition.FindByID":
					liveAtRead = hctx.Err() == nil
					ctx.end(tt.endErr)
				case "before:Definition.SetTemplateLink":
					linkWritten = true
				}
			})
			req := httptest.NewRequest(http.MethodPut, "/api/v1/stack-definitions/d2/template-link", strings.NewReader(`{"template_id":"t1"}`)).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			assert.JSONEq(t, tt.wantBody, w.Body.String())
			mu.Lock()
			assert.True(t, liveAtRead, "the definition read runs before the end")
			assert.False(t, linkWritten, "no link write")
			mu.Unlock()
			assert.Empty(t, f.definition(t, "d2").SourceTemplateID)
		})
	}
}

// TestBulkOperation_DefinitionLookupError checks the result error of a failed
// definition read in bulk deploy, stop and clean (#656): "request cancelled"
// for a cancel, "stack definition not found" only for a missing definition,
// else "lookup failed" with an error log.
// Not parallel: it replaces the default slog logger.
func TestBulkOperation_DefinitionLookupError(t *testing.T) {
	logs := captureLogs(t)
	const errorLine = `level=ERROR msg="bulk operation: stack definition lookup failed" definition_id=d1`

	tests := []struct {
		fetchErr     error
		name         string
		want         string
		wantErrorLog bool
	}{
		{name: "missing definition", want: "stack definition not found"},
		{name: "request cancelled", fetchErr: dberrors.NewDatabaseError("find_by_id", context.Canceled), want: "request cancelled"},
		{name: "database error", fetchErr: errors.New("connection reset"), want: bulkLookupFailed, wantErrorLog: true},
		{name: "deadline exceeded", fetchErr: dberrors.NewDatabaseError("find_by_id", context.DeadlineExceeded), want: bulkLookupFailed, wantErrorLog: true},
	}
	for _, tt := range tests {
		for _, op := range []string{"deploy", "stop", "clean"} {
			t.Run(tt.name+"/"+op, func(t *testing.T) {
				instRepo := NewMockStackInstanceRepository()
				seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusRunning)
				defRepo := NewMockStackDefinitionRepository()
				if tt.fetchErr != nil {
					seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
					defRepo.SetFetchError(tt.fetchErr)
				}
				logRepo := NewMockDeploymentLogRepository()
				mgr := newBulkTestManager(instRepo, logRepo)
				t.Cleanup(mgr.Shutdown)
				router := setupBulkRouter(t, instRepo, NewMockValueOverrideRepository(), defRepo, NewMockChartConfigRepository(),
					NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), mgr, logRepo, "uid-1", "testuser", "admin")
				logs()

				w := serve(router, http.MethodPost, "/api/v1/stack-instances/bulk/"+op, `{"instance_ids":["i1"]}`)

				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				results := bulkResults(t, w)
				require.Len(t, results, 1)
				assert.Equal(t, "error", results[0].Status)
				assert.Equal(t, tt.want, results[0].Error)
				got := logs()
				if tt.wantErrorLog {
					assert.Equal(t, 1, strings.Count(got, errorLine), got)
					return
				}
				assert.NotContains(t, got, "level=ERROR", got)
			})
		}
	}
}

// TestLookupNames_LogLevel checks the log line of a failed batch name
// lookup: a constant message with the kind attribute, at debug for a
// cancelled request, else at warn (#656).
// Not parallel: it replaces the default slog logger.
func TestLookupNames_LogLevel(t *testing.T) {
	logs := captureLogs(t)

	tests := []struct {
		err      error
		name     string
		wantLine string
	}{
		{name: "cancelled", err: dberrors.NewDatabaseError("names_by_ids", context.Canceled),
			wantLine: `level=DEBUG msg="failed to batch-fetch names" kind=stack_definition`},
		{name: "database error", err: errors.New("db down"),
			wantLine: `level=WARN msg="failed to batch-fetch names" kind=stack_definition`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs()
			lookup := func([]string) (map[string]string, error) { return nil, tt.err }

			got := lookupNames(context.Background(), lookup, "stack_definition", []string{"d1"})

			assert.Empty(t, got)
			out := logs()
			assert.Equal(t, 1, strings.Count(out, tt.wantLine), out)
		})
	}
}

// TestInstanceValuesDrift_DefinitionLookupLogLevel checks the log line of a
// failed definition read in the values drift check: debug for a cancelled
// request, warn for another error; the drift is false (#656).
// Not parallel: it replaces the default slog logger.
func TestInstanceValuesDrift_DefinitionLookupLogLevel(t *testing.T) {
	logs := captureLogs(t)

	cancelled := func() context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}
	tests := []struct {
		ctx      func() context.Context
		fetchErr error
		name     string
		wantLine string
	}{
		{name: "cancelled", ctx: cancelled,
			wantLine: `level=DEBUG msg="values drift: definition lookup failed" instance_id=i1`},
		{name: "database error", ctx: context.Background, fetchErr: errors.New("db down"),
			wantLine: `level=WARN msg="values drift: definition lookup failed" instance_id=i1`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defRepo := NewMockStackDefinitionRepository()
			seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
			defRepo.SetFetchError(tt.fetchErr)
			logRepo := NewMockDeploymentLogRepository()
			require.NoError(t, logRepo.Create(context.Background(), &models.DeploymentLog{
				ID: "log-1", StackInstanceID: "i1", Action: models.DeployActionRollback, Status: models.DeployLogSuccess,
			}))
			h := &InstanceHandler{definitionRepo: defRepo, deployLogRepo: logRepo}
			inst := &models.StackInstance{ID: "i1", StackDefinitionID: "d1", LastDeployedValues: `{"web":"replicas: 1"}`}
			logs()

			assert.False(t, h.instanceValuesDrift(tt.ctx(), inst))
			out := logs()
			assert.Equal(t, 1, strings.Count(out, tt.wantLine), out)
		})
	}
}

// TestDeleteChartConfig_DefinitionReadLogs checks that a failed definition
// read of the required chart check logs an error for a 500, and not for a
// cancelled request (499) (#656).
// Not parallel: it replaces the default slog logger.
func TestDeleteChartConfig_DefinitionReadLogs(t *testing.T) {
	logs := captureLogs(t)
	const errorLine = `level=ERROR msg="Failed to read the definition for the required chart check" definition_id=d1`

	cancelled := func() context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}
	tests := []struct {
		ctx          func() context.Context
		fetchErr     error
		name         string
		wantStatus   int
		wantErrorLog bool
	}{
		{name: "cancelled", ctx: cancelled, wantStatus: statusClientClosedRequest},
		{name: "database error", ctx: context.Background, fetchErr: errors.New("db down"), wantStatus: http.StatusInternalServerError, wantErrorLog: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			defRepo := NewMockStackDefinitionRepository()
			seedDefinition(t, defRepo, "d1", "def-1", "uid-1")
			defRepo.SetFetchError(tt.fetchErr)
			chartRepo := NewMockChartConfigRepository()
			require.NoError(t, chartRepo.Create(&models.ChartConfig{ID: "c1", StackDefinitionID: "d1", ChartName: "web", RepositoryURL: "oci://charts/web"}))
			h := NewDefinitionHandler(defRepo, chartRepo, NewMockStackInstanceRepository(), NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository())
			r := gin.New()
			r.Use(injectAuthContext("uid-1", "devops"))
			r.DELETE("/api/v1/stack-definitions/:id/charts/:chartId", h.DeleteChartConfig)
			logs()

			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/v1/stack-definitions/d1/charts/c1", nil).WithContext(tt.ctx()))

			assert.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			_, err := chartRepo.FindByID("c1")
			assert.NoError(t, err, "the chart stays")
			out := logs()
			if tt.wantErrorLog {
				assert.Equal(t, 1, strings.Count(out, errorLine), out)
				return
			}
			assert.NotContains(t, out, "level=ERROR", out)
		})
	}
}

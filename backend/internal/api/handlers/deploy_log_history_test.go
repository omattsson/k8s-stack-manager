package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"backend/internal/helm"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deployLogSummaryRowSchema is a summary row: the row columns, and never
// output, values_snapshot or chart_versions.
const deployLogSummaryRowSchema = `{
"type": "object",
"required": ["id", "stack_instance_id", "action", "status", "started_at"],
"properties": {
"id": {"type": "string"},
"stack_instance_id": {"type": "string"},
"action": {"type": "string", "enum": ["deploy", "rollback", "stop", "clean"]},
"status": {"type": "string", "enum": ["running", "success", "error", "partial"]},
"branch": {"type": "string"},
"error_message": {"type": "string"},
"target_log_id": {"type": "string"},
"user_id": {"type": "string"},
"started_by": {"type": "string"},
"started_at": {"type": "string"},
"completed_at": {"type": "string"}
},
"not": {"anyOf": [
{"required": ["output"]},
{"required": ["values_snapshot"]},
{"required": ["chart_versions"]}
]}
}`

// deployLogSummaryResultSchema is the summary view of the deploy log list.
const deployLogSummaryResultSchema = `{
"type": "object",
"required": ["data", "total"],
"properties": {
"data": {"type": "array", "items": ` + deployLogSummaryRowSchema + `},
"total": {"type": "integer"},
"next_cursor": {"type": "string"},
"highlights": {
"type": "object",
"additionalProperties": false,
"properties": {
"running": ` + deployLogSummaryRowSchema + `,
"current": ` + deployLogSummaryRowSchema + `,
"latest_failure": ` + deployLogSummaryRowSchema + `
}
}
}
}`

// deployLogRepoWithoutHighlights hides HistoryHighlights of the mock.
type deployLogRepoWithoutHighlights struct {
	models.DeploymentLogRepository
}

// caseInsensitiveInstanceRepo finds instances without case, as MySQL does.
type caseInsensitiveInstanceRepo struct {
	*MockStackInstanceRepository
}

func (r caseInsensitiveInstanceRepo) FindByID(ctx context.Context, id string) (*models.StackInstance, error) {
	return r.MockStackInstanceRepository.FindByID(ctx, strings.ToLower(id))
}

// setupDeployLogHistoryRouter builds a router with the deploy log list,
// entry and values routes for the caller. The instance repository finds IDs
// without case.
func setupDeployLogHistoryRouter(
	t *testing.T,
	instRepo *MockStackInstanceRepository,
	logRepo models.DeploymentLogRepository,
	userRepo *MockUserRepository,
	callerID, callerRole string,
) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userID", callerID)
		c.Set("username", callerID)
		c.Set("role", callerRole)
		c.Next()
	})

	h, err := NewInstanceHandlerWithDeployer(
		caseInsensitiveInstanceRepo{instRepo}, NewMockValueOverrideRepository(), nil,
		NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
		NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
		helm.NewValuesGenerator(), userRepo,
		nil, nil, nil, logRepo, nil,
		0, &mockHandlerTxRunner{},
	)
	require.NoError(t, err)

	insts := r.Group("/api/v1/stack-instances")
	insts.GET("/:id/deploy-log", h.GetDeployLog)
	insts.GET("/:id/deploy-log/:logId", h.GetDeployLogEntry)
	insts.GET("/:id/deploy-log/:logId/values", h.GetDeployLogValues)
	return r
}

// seedDeployLogHistory adds instance i1 (owner uid-1), the users alice
// (uid-1) and bob (uid-2), and three logs of i1 plus one log of i2.
func seedDeployLogHistory(t *testing.T) (*MockStackInstanceRepository, *MockDeploymentLogRepository, *MockUserRepository) {
	t.Helper()
	instRepo := NewMockStackInstanceRepository()
	seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusRunning)
	seedInstance(t, instRepo, "i2", "stack-b", "d1", "uid-2", models.StackStatusRunning)

	userRepo := NewMockUserRepository()
	require.NoError(t, userRepo.Create(context.Background(), &models.User{ID: "uid-1", Username: "alice", Role: "user"}))
	require.NoError(t, userRepo.Create(context.Background(), &models.User{ID: "uid-2", Username: "bob", Role: "devops"}))

	logRepo := NewMockDeploymentLogRepository()
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	logs := []models.DeploymentLog{
		{ID: "log-1", StackInstanceID: "i1", Action: models.DeployActionDeploy, Status: models.DeployLogSuccess,
			UserID: "uid-1", Output: "deploy one", ValuesSnapshot: `{"a":1}`, ChartVersions: `{"web":"1.0.0"}`,
			Branch: "main", StartedAt: base},
		{ID: "log-2", StackInstanceID: "i1", Action: models.DeployActionDeploy, Status: models.DeployLogError,
			UserID: "uid-2", Output: "deploy two", ErrorMessage: "chart web failed", ValuesSnapshot: `{"a":2}`,
			StartedAt: base.Add(time.Minute)},
		{ID: "log-3", StackInstanceID: "i1", Action: models.DeployActionStop, Status: models.DeployLogSuccess,
			UserID: "uid-gone", Output: "stopped", StartedAt: base.Add(2 * time.Minute)},
		{ID: "log-other", StackInstanceID: "i2", Action: models.DeployActionDeploy, Status: models.DeployLogSuccess,
			Output: "other", StartedAt: base},
	}
	for i := range logs {
		require.NoError(t, logRepo.Create(context.Background(), &logs[i]))
	}
	logRepo.highlights = &models.DeployLogHighlights{
		Current:       &models.DeploymentLog{ID: "log-1", StackInstanceID: "i1", Action: models.DeployActionDeploy, Status: models.DeployLogSuccess, UserID: "uid-1", StartedAt: base},
		LatestFailure: &models.DeploymentLog{ID: "log-2", StackInstanceID: "i1", Action: models.DeployActionDeploy, Status: models.DeployLogError, UserID: "uid-2", ErrorMessage: "chart web failed", StartedAt: base.Add(time.Minute)},
	}
	return instRepo, logRepo, userRepo
}

// deployLogRows decodes the data rows of a list response as raw maps.
func deployLogRows(t *testing.T, body []byte) (map[string]json.RawMessage, []map[string]interface{}) {
	t.Helper()
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &raw))
	var rows []map[string]interface{}
	require.NoError(t, json.Unmarshal(raw["data"], &rows))
	return raw, rows
}

func TestGetDeployLog_History(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// query is appended to /api/v1/stack-instances/i1/deploy-log.
		query string
		// setup changes the seeded repositories; it can replace the log repository.
		setup      func(*MockDeploymentLogRepository) models.DeploymentLogRepository
		wantStatus int
		wantError  string
		check      func(t *testing.T, body []byte, logRepo *MockDeploymentLogRepository)
	}{
		{
			name:       "default view keeps all fields",
			wantStatus: http.StatusOK,
			check: func(t *testing.T, body []byte, logRepo *MockDeploymentLogRepository) {
				raw, rows := deployLogRows(t, body)
				require.Len(t, rows, 3)
				assert.Equal(t, "log-3", rows[0]["id"], "newest first")
				byID := map[string]map[string]interface{}{}
				for _, r := range rows {
					byID[r["id"].(string)] = r
				}
				assert.Equal(t, "deploy one", byID["log-1"]["output"])
				assert.Equal(t, `{"a":1}`, byID["log-1"]["values_snapshot"])
				assert.Equal(t, `{"web":"1.0.0"}`, byID["log-1"]["chart_versions"])
				assert.Equal(t, "alice", byID["log-1"]["started_by"])
				assert.Equal(t, "bob", byID["log-2"]["started_by"])
				assert.NotContains(t, byID["log-3"], "started_by", "unknown user")
				assert.NotContains(t, raw, "highlights")
				assert.NotContains(t, raw, "next_cursor")
				assert.JSONEq(t, "3", string(raw["total"]))
				assert.False(t, logRepo.LastFilters().Summary)
				assert.Zero(t, logRepo.HighlightCalls())
			},
		},
		{
			name:       "full view equals the default view",
			query:      "?view=full",
			wantStatus: http.StatusOK,
			check: func(t *testing.T, body []byte, logRepo *MockDeploymentLogRepository) {
				raw, rows := deployLogRows(t, body)
				require.Len(t, rows, 3)
				assert.Equal(t, "stopped", rows[0]["output"])
				assert.NotContains(t, raw, "highlights")
				assert.False(t, logRepo.LastFilters().Summary)
			},
		},
		{
			name:       "summary view has row columns, started_by and highlights",
			query:      "?view=summary",
			wantStatus: http.StatusOK,
			check: func(t *testing.T, body []byte, logRepo *MockDeploymentLogRepository) {
				assert.True(t, validateJSONSchema(t, deployLogSummaryResultSchema, body))
				raw, rows := deployLogRows(t, body)
				require.Len(t, rows, 3)
				for _, r := range rows {
					assert.NotContains(t, r, "output")
					assert.NotContains(t, r, "values_snapshot")
					assert.NotContains(t, r, "chart_versions")
				}
				assert.Equal(t, "chart web failed", rows[1]["error_message"])
				assert.Equal(t, "bob", rows[1]["started_by"])

				var hl map[string]map[string]interface{}
				require.NoError(t, json.Unmarshal(raw["highlights"], &hl))
				assert.NotContains(t, hl, "running", "empty highlight omitted")
				assert.Equal(t, "log-1", hl["current"]["id"])
				assert.Equal(t, "alice", hl["current"]["started_by"])
				assert.Equal(t, "log-2", hl["latest_failure"]["id"])
				assert.Equal(t, "bob", hl["latest_failure"]["started_by"])

				assert.True(t, logRepo.LastFilters().Summary)
				assert.Equal(t, 1, logRepo.HighlightCalls())
			},
		},
		{
			name:       "summary view with a cursor has no highlights",
			query:      "?view=summary&cursor=abc",
			wantStatus: http.StatusOK,
			check: func(t *testing.T, body []byte, logRepo *MockDeploymentLogRepository) {
				raw, _ := deployLogRows(t, body)
				assert.NotContains(t, raw, "highlights")
				assert.Equal(t, "abc", logRepo.LastFilters().Cursor)
				assert.Zero(t, logRepo.HighlightCalls())
			},
		},
		{
			name:  "summary view without a highlight reader has no highlights",
			query: "?view=summary",
			setup: func(logRepo *MockDeploymentLogRepository) models.DeploymentLogRepository {
				return deployLogRepoWithoutHighlights{logRepo}
			},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, body []byte, logRepo *MockDeploymentLogRepository) {
				assert.True(t, validateJSONSchema(t, deployLogSummaryResultSchema, body))
				raw, rows := deployLogRows(t, body)
				assert.Len(t, rows, 3)
				assert.NotContains(t, raw, "highlights")
				assert.Zero(t, logRepo.HighlightCalls())
			},
		},
		{
			name:       "action and status filters go to the repository",
			query:      "?view=summary&action=deploy,%20rollback,&status=error",
			wantStatus: http.StatusOK,
			check: func(t *testing.T, body []byte, logRepo *MockDeploymentLogRepository) {
				raw, rows := deployLogRows(t, body)
				require.Len(t, rows, 1)
				assert.Equal(t, "log-2", rows[0]["id"])
				assert.JSONEq(t, "1", string(raw["total"]))
				f := logRepo.LastFilters()
				assert.Equal(t, []string{"deploy", "rollback"}, f.Actions)
				assert.Equal(t, []string{"error"}, f.Statuses)
				assert.Equal(t, "i1", f.InstanceID)
				// The highlights ignore the filters.
				var hl map[string]map[string]interface{}
				require.NoError(t, json.Unmarshal(raw["highlights"], &hl))
				assert.Equal(t, "log-1", hl["current"]["id"])
			},
		},
		{
			name:       "filters apply in the full view",
			query:      "?action=stop",
			wantStatus: http.StatusOK,
			check: func(t *testing.T, body []byte, logRepo *MockDeploymentLogRepository) {
				_, rows := deployLogRows(t, body)
				require.Len(t, rows, 1)
				assert.Equal(t, "stopped", rows[0]["output"])
				assert.Equal(t, []string{"stop"}, logRepo.LastFilters().Actions)
				assert.Nil(t, logRepo.LastFilters().Statuses)
			},
		},
		{
			name:       "offset path returns next_cursor when more rows follow",
			query:      "?view=summary&limit=2",
			wantStatus: http.StatusOK,
			check: func(t *testing.T, body []byte, _ *MockDeploymentLogRepository) {
				var res models.DeploymentLogResult
				require.NoError(t, json.Unmarshal(body, &res))
				assert.Len(t, res.Data, 2)
				assert.Equal(t, int64(3), res.Total)
				assert.Equal(t, "cursor-log-2", res.NextCursor)
			},
		},
		{
			name:       "last offset page has no next_cursor",
			query:      "?limit=2&offset=2",
			wantStatus: http.StatusOK,
			check: func(t *testing.T, body []byte, _ *MockDeploymentLogRepository) {
				raw, rows := deployLogRows(t, body)
				assert.Len(t, rows, 1)
				assert.NotContains(t, raw, "next_cursor")
			},
		},
		{
			name:       "unknown view",
			query:      "?view=compact",
			wantStatus: http.StatusBadRequest,
			wantError:  msgInvalidDeployLogView,
		},
		{
			name:       "unknown action",
			query:      "?action=deploy,upgrade",
			wantStatus: http.StatusBadRequest,
			wantError:  msgInvalidDeployLogAction,
		},
		{
			name:       "action is case sensitive",
			query:      "?action=Deploy",
			wantStatus: http.StatusBadRequest,
			wantError:  msgInvalidDeployLogAction,
		},
		{
			name:       "unknown status",
			query:      "?status=success,failed",
			wantStatus: http.StatusBadRequest,
			wantError:  msgInvalidDeployLogStatus,
		},
		{
			name:  "list error is a 500",
			query: "?view=summary",
			setup: func(logRepo *MockDeploymentLogRepository) models.DeploymentLogRepository {
				logRepo.SetError(errors.New("connection reset"))
				return logRepo
			},
			wantStatus: http.StatusInternalServerError,
			wantError:  msgInternalServerError,
		},
		{
			name:  "highlights error omits the highlights",
			query: "?view=summary",
			setup: func(logRepo *MockDeploymentLogRepository) models.DeploymentLogRepository {
				logRepo.highlightsErr = errors.New("connection reset")
				return logRepo
			},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, body []byte, logRepo *MockDeploymentLogRepository) {
				assert.True(t, validateJSONSchema(t, deployLogSummaryResultSchema, body))
				raw, rows := deployLogRows(t, body)
				assert.Len(t, rows, 3)
				assert.NotContains(t, raw, "highlights")
				assert.Equal(t, 1, logRepo.HighlightCalls())
			},
		},
		{
			name:       "limit above the maximum gives the maximum",
			query:      "?limit=1000",
			wantStatus: http.StatusOK,
			check: func(t *testing.T, body []byte, logRepo *MockDeploymentLogRepository) {
				_, rows := deployLogRows(t, body)
				assert.Len(t, rows, 3)
				assert.Equal(t, models.MaxDeployLogPageSize, logRepo.LastFilters().Limit)
			},
		},
		{
			name:       "largest int limit gives the maximum",
			query:      "?view=summary&cursor=abc&limit=" + strconv.Itoa(int(^uint(0)>>1)),
			wantStatus: http.StatusOK,
			check: func(t *testing.T, _ []byte, logRepo *MockDeploymentLogRepository) {
				assert.Equal(t, models.MaxDeployLogPageSize, logRepo.LastFilters().Limit)
			},
		},
		{
			name:       "limit at the maximum stays",
			query:      "?limit=100",
			wantStatus: http.StatusOK,
			check: func(t *testing.T, _ []byte, logRepo *MockDeploymentLogRepository) {
				assert.Equal(t, 100, logRepo.LastFilters().Limit)
			},
		},
		{
			name:       "negative limit",
			query:      "?limit=-1",
			wantStatus: http.StatusBadRequest,
			wantError:  "Invalid limit parameter",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			instRepo, logRepo, userRepo := seedDeployLogHistory(t)
			var repo models.DeploymentLogRepository = logRepo
			if tt.setup != nil {
				repo = tt.setup(logRepo)
			}
			router := setupDeployLogHistoryRouter(t, instRepo, repo, userRepo, "uid-3", "user")

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/stack-instances/i1/deploy-log"+tt.query, nil)
			router.ServeHTTP(w, req)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantError != "" {
				assert.True(t, validateJSONSchema(t, errorSchema, w.Body.Bytes()))
				assert.JSONEq(t, `{"error":"`+tt.wantError+`"}`, w.Body.String())
			}
			if tt.check != nil {
				tt.check(t, w.Body.Bytes(), logRepo)
			}
		})
	}
}

func TestGetDeployLog_StoredInstanceID(t *testing.T) {
	t.Parallel()

	instRepo, logRepo, userRepo := seedDeployLogHistory(t)
	router := setupDeployLogHistoryRouter(t, instRepo, logRepo, userRepo, "uid-3", "user")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/stack-instances/I1/deploy-log?view=summary", nil)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	_, rows := deployLogRows(t, w.Body.Bytes())
	assert.Len(t, rows, 3)
	assert.Equal(t, "i1", logRepo.LastFilters().InstanceID)
}

func TestGetDeployLogEntry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		instanceID string
		logID      string
		role       string
		noRepo     bool
		repoErr    error
		wantStatus int
		wantError  string
	}{
		{name: "user who is not the owner", instanceID: "i1", logID: "log-2", role: "user", wantStatus: http.StatusOK},
		{name: "devops", instanceID: "i1", logID: "log-2", role: "devops", wantStatus: http.StatusOK},
		{name: "admin", instanceID: "i1", logID: "log-2", role: "admin", wantStatus: http.StatusOK},
		{name: "instance ID in upper case", instanceID: "I1", logID: "log-2", role: "user", wantStatus: http.StatusOK},
		{name: "log of another instance", instanceID: "i1", logID: "log-other", role: "user",
			wantStatus: http.StatusNotFound, wantError: "Deployment log not found for this instance"},
		{name: "unknown log", instanceID: "i1", logID: "log-missing", role: "user",
			wantStatus: http.StatusNotFound, wantError: "Deployment log not found"},
		{name: "unknown instance", instanceID: "missing", logID: "log-1", role: "user",
			wantStatus: http.StatusNotFound, wantError: "Stack instance not found"},
		{name: "no repository", instanceID: "i1", logID: "log-1", role: "user", noRepo: true,
			wantStatus: http.StatusServiceUnavailable, wantError: "Deployment log service not configured"},
		{name: "repository error", instanceID: "i1", logID: "log-1", role: "user", repoErr: errors.New("connection reset"),
			wantStatus: http.StatusInternalServerError, wantError: msgInternalServerError},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			instRepo, logRepo, userRepo := seedDeployLogHistory(t)
			var repo models.DeploymentLogRepository = logRepo
			if tt.noRepo {
				repo = nil
			}
			if tt.repoErr != nil {
				logRepo.SetError(tt.repoErr)
			}
			router := setupDeployLogHistoryRouter(t, instRepo, repo, userRepo, "uid-3", tt.role)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet,
				"/api/v1/stack-instances/"+tt.instanceID+"/deploy-log/"+tt.logID, nil)
			router.ServeHTTP(w, req)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantError != "" {
				assert.JSONEq(t, `{"error":"`+tt.wantError+`"}`, w.Body.String())
				return
			}
			var got map[string]interface{}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
			assert.Equal(t, "log-2", got["id"])
			assert.Equal(t, "deploy two", got["output"])
			assert.Equal(t, "chart web failed", got["error_message"])
			assert.Equal(t, "bob", got["started_by"])
			assert.NotContains(t, got, "values_snapshot")
			assert.NotContains(t, got, "chart_versions")
		})
	}
}

func TestParseDeployLogList(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		raw    string
		want   []string
		wantOK bool
	}{
		{name: "empty", raw: "", want: nil, wantOK: true},
		{name: "only commas and spaces", raw: " , ,", want: nil, wantOK: true},
		{name: "trims and drops empty items", raw: " deploy ,,rollback ", want: []string{"deploy", "rollback"}, wantOK: true},
		{name: "unknown item", raw: "deploy,upgrade", want: nil, wantOK: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseDeployLogList(tt.raw, models.IsDeployLogAction)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGetDeployLogValues_StoredInstanceID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		instanceID string
		logID      string
		wantStatus int
	}{
		{name: "lower case", instanceID: "i1", logID: "log-1", wantStatus: http.StatusOK},
		{name: "upper case", instanceID: "I1", logID: "log-1", wantStatus: http.StatusOK},
		{name: "upper case, log of another instance", instanceID: "I1", logID: "log-other", wantStatus: http.StatusNotFound},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			instRepo, logRepo, userRepo := seedDeployLogHistory(t)
			router := setupDeployLogHistoryRouter(t, instRepo, logRepo, userRepo, "uid-3", "user")

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet,
				"/api/v1/stack-instances/"+tt.instanceID+"/deploy-log/"+tt.logID+"/values", nil)
			router.ServeHTTP(w, req)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantStatus != http.StatusOK {
				assert.JSONEq(t, `{"error":"Deployment log not found for this instance"}`, w.Body.String())
				return
			}
			assert.JSONEq(t, `{"log_id":"log-1","values":{"a":1}}`, w.Body.String())
		})
	}
}

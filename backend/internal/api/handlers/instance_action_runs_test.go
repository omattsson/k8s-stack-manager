package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"backend/internal/helm"
	"backend/internal/hooks"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Values that the action run history must never store.
const (
	runsUndeclaredValue = "undeclared-value-0xfeed"
	runsSensitiveValue  = "hunter2-sensitive"
)

// runsFullBody sends every declared parameter of refresh-db, a sensitive
// value and an undeclared parameter.
const runsFullBody = `{"parameters":{"image":"golden","dry_run":true,"market":"a","token":"` + runsSensitiveValue + `","extra":"` + runsUndeclaredValue + `"}}`

// runSubscriber is a fake action subscriber with one handler for invoke
// calls (POST) and one for job log reads (GET).
func runSubscriber(t *testing.T, invoke, logReply http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			logReply(w, r)
			return
		}
		invoke(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// replyWith answers with a fixed status, content type and body.
func replyWith(code int, contentType, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
}

func runsRegistry(t *testing.T, baseURL string) *hooks.ActionRegistry {
	t.Helper()
	r, err := hooks.NewActionRegistry([]hooks.ActionSubscription{
		{
			Name:           "refresh-db",
			Label:          "Refresh database",
			URL:            baseURL + "/actions/refresh-db",
			LogPath:        "/jobs/{job_id}/log",
			TimeoutSeconds: 5,
			Parameters: []hooks.ActionParameter{
				{Name: "image"},
				{Name: "dry_run", Type: "bool"},
				{Name: "market", Type: "enum", Options: []string{"a", "b"}},
				{Name: "token", Sensitive: true},
			},
		},
		{Name: "seed-data", URL: baseURL + "/actions/seed", TimeoutSeconds: 5},
	}, nil)
	require.NoError(t, err)
	return r
}

// setupActionRunsRouter builds the action routes with an action run
// repository (nil: none). The users alice (owner), root (admin) and ops
// (devops) exist.
func setupActionRunsRouter(t *testing.T, registry *hooks.ActionRegistry, runs *MockActionRunRepository, users *MockUserRepository, callerID, role string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	instRepo := NewMockStackInstanceRepository()
	seedInstance(t, instRepo, actionsInstanceID, "demo", "d1", actionsOwnerID, "running")
	if users == nil {
		users = NewMockUserRepository()
	}
	for id, name := range map[string]string{actionsOwnerID: "alice", "uid-admin": "root", "uid-devops": "ops"} {
		require.NoError(t, users.Create(&models.User{ID: id, Username: name}))
	}

	h := NewInstanceHandler(instRepo, NewMockValueOverrideRepository(), NewMockChartBranchOverrideRepository(),
		NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), NewMockStackTemplateRepository(),
		NewMockTemplateChartConfigRepository(), helm.NewValuesGenerator(), users, 0)
	h.WithActions(registry)
	if runs != nil {
		h.WithActionRuns(runs)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userID", callerID)
		c.Set("username", callerID)
		c.Set("role", role)
		c.Next()
	})
	insts := r.Group("/api/v1/stack-instances")
	insts.GET("/:id/actions", h.ListActions)
	insts.POST("/:id/actions/:name", h.InvokeAction)
	insts.GET("/:id/actions/:name/jobs/:job_id/log", h.GetActionJobLog)
	insts.GET("/:id/action-runs", h.ListActionRuns)
	return r
}

func decodeObject(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))
	return m
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestInvokeAction_RecordsRun(t *testing.T) {
	t.Parallel()

	declared := map[string]any{"image": "golden", "dry_run": true, "market": "a", "token": hooks.RedactedParameterValue}
	tests := []struct {
		name         string
		action       string
		body         string
		reply        http.HandlerFunc
		closed       bool // the subscriber is not reachable
		wantHTTP     int
		wantStatus   int
		wantMessage  string
		wantJobID    string
		wantFinished bool
		wantParams   map[string]any
	}{
		{
			name: "sync 200", action: "seed-data", body: `{"parameters":{"extra":"` + runsUndeclaredValue + `"}}`,
			reply:    replyWith(http.StatusOK, "application/json", `{"ok":true}`),
			wantHTTP: http.StatusOK, wantStatus: http.StatusOK, wantFinished: true, wantParams: map[string]any{},
		},
		{
			name: "job 202 keeps finished_at empty", action: "refresh-db", body: runsFullBody,
			reply:    replyWith(http.StatusAccepted, "application/json", `{"job_id":"job-0123456789ab"}`),
			wantHTTP: http.StatusOK, wantStatus: http.StatusAccepted, wantJobID: "job-0123456789ab", wantParams: declared,
		},
		{
			name: "refusal 409 stores the message", action: "refresh-db", body: runsFullBody,
			reply:    replyWith(http.StatusConflict, "application/json", `{"error":"refresh-db already in flight, see https://internal.example/x"}`),
			wantHTTP: http.StatusOK, wantStatus: http.StatusConflict, wantMessage: "refresh-db already in flight, see [url]",
			wantFinished: true, wantParams: declared,
		},
		{
			name: "refusal message with a sensitive value is redacted", action: "refresh-db", body: runsFullBody,
			reply:    replyWith(http.StatusUnauthorized, "application/json", `{"error":"token `+runsSensitiveValue+` refused for golden"}`),
			wantHTTP: http.StatusOK, wantStatus: http.StatusUnauthorized, wantMessage: "token [redacted] refused for golden",
			wantFinished: true, wantParams: declared,
		},
		{
			name: "redirect with an HTML body stores 302", action: "seed-data", body: `{}`,
			reply: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", "https://elsewhere.example/stub-redirect-secret")
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusFound)
				_, _ = w.Write([]byte(`<html><a href="https://elsewhere.example/stub-redirect-secret">moved</a></html>`))
			},
			wantHTTP: http.StatusBadGateway, wantStatus: http.StatusFound, wantMessage: actionRunMsgNonJSON,
			wantFinished: true, wantParams: map[string]any{},
		},
		{
			name: "too large body stores the status", action: "seed-data", body: `{}`,
			reply:    replyWith(http.StatusOK, "application/json", `"`+strings.Repeat("x", 1<<20)+`"`),
			wantHTTP: http.StatusBadGateway, wantStatus: http.StatusOK, wantMessage: actionRunMsgTooLarge,
			wantFinished: true, wantParams: map[string]any{},
		},
		{
			name: "transport error stores status 0", action: "refresh-db", body: runsFullBody, closed: true,
			reply:    replyWith(http.StatusOK, "application/json", `{}`),
			wantHTTP: http.StatusBadGateway, wantStatus: 0, wantMessage: actionRunMsgUnreachable,
			wantFinished: true, wantParams: declared,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := runSubscriber(t, tt.reply, replyWith(http.StatusOK, "", ""))
			registry := runsRegistry(t, srv.URL)
			if tt.closed {
				srv.Close()
			}
			runs := NewMockActionRunRepository()
			r := setupActionRunsRouter(t, registry, runs, nil, actionsOwnerID, "user")

			before := time.Now().UTC()
			w := doRequest(r, http.MethodPost, "/api/v1/stack-instances/"+actionsInstanceID+"/actions/"+tt.action, tt.body)
			require.Equal(t, tt.wantHTTP, w.Code, w.Body.String())
			resp := decodeObject(t, w.Body.Bytes())

			stored := runs.Runs()
			require.Len(t, stored, 1, "exactly one run for each invoke that reached the subscriber call")
			run := stored[0]
			assert.Equal(t, actionsInstanceID, run.InstanceID)
			assert.Equal(t, tt.action, run.Action)
			assert.Equal(t, actionsOwnerID, run.UserID)
			assert.Equal(t, tt.wantStatus, run.StatusCode)
			assert.Equal(t, tt.wantMessage, run.Message)
			assert.Equal(t, tt.wantJobID, run.JobID)
			assert.Empty(t, run.JobStatus)
			assert.True(t, strings.HasPrefix(run.RequestID, "req-"), run.RequestID)
			assert.False(t, run.StartedAt.Before(before.Add(-time.Second)))
			if tt.wantFinished {
				require.NotNil(t, run.FinishedAt)
				assert.False(t, run.FinishedAt.Before(run.StartedAt))
			} else {
				assert.Nil(t, run.FinishedAt)
			}
			var params map[string]any
			require.NoError(t, json.Unmarshal([]byte(run.Parameters), &params))
			assert.Equal(t, tt.wantParams, params)
			assert.NotContains(t, run.Parameters, runsUndeclaredValue, "an undeclared parameter is never stored")
			assert.NotContains(t, run.Parameters, runsSensitiveValue, "a sensitive value is never stored")
			assert.NotContains(t, run.Message, "elsewhere.example")
			assert.NotContains(t, run.Message, runsSensitiveValue, "a sensitive value is never stored in the message")

			if tt.wantHTTP == http.StatusOK {
				assert.Equal(t, run.ID, resp["run_id"])
				return
			}
			// The 502 body to the client does not change.
			assert.Equal(t, []string{"action", "error", "request_id"}, sortedKeys(resp))
			assert.Equal(t, run.RequestID, resp["request_id"])
			assert.NotContains(t, w.Body.String(), "stub-redirect-secret")
		})
	}
}

// TestInvokeAction_ClientClosedRequest checks a caller that goes away while
// the subscriber works: the run says so (not "unreachable"), and the write
// ignores the cancelled request context.
func TestInvokeAction_ClientClosedRequest(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv := runSubscriber(t, func(_ http.ResponseWriter, r *http.Request) {
		// The server sees the closed connection only after the body is read.
		_, _ = io.Copy(io.Discard, r.Body)
		cancel()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}, replyWith(http.StatusOK, "", ""))
	runs := NewMockActionRunRepository()
	r := setupActionRunsRouter(t, runsRegistry(t, srv.URL), runs, nil, actionsOwnerID, "user")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/stack-instances/"+actionsInstanceID+"/actions/refresh-db",
		strings.NewReader(runsFullBody)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
	assert.Equal(t, []string{"action", "error", "request_id"}, sortedKeys(decodeObject(t, w.Body.Bytes())))
	stored := runs.Runs()
	require.Len(t, stored, 1)
	assert.Equal(t, 0, stored[0].StatusCode)
	assert.Equal(t, actionRunMsgClientClosed, stored[0].Message)
	require.NotNil(t, stored[0].FinishedAt)
	assert.Equal(t, []error{nil}, runs.CreateCtxErrs(), "the write does not use the cancelled request context")
}

func TestInvokeAction_NoRunWithoutSubscriberCall(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		registry   bool
		callerID   string
		role       string
		path       string
		body       string
		wantStatus int
	}{
		{"bad body", true, actionsOwnerID, "user", "/" + actionsInstanceID + "/actions/refresh-db", `{"parameters":`, http.StatusBadRequest},
		{"invalid parameter", true, actionsOwnerID, "user", "/" + actionsInstanceID + "/actions/refresh-db", `{"parameters":{"market":"z"}}`, http.StatusBadRequest},
		{"forbidden", true, "uid-other", "user", "/" + actionsInstanceID + "/actions/refresh-db", `{}`, http.StatusForbidden},
		{"unknown instance", true, actionsOwnerID, "user", "/missing/actions/refresh-db", `{}`, http.StatusNotFound},
		{"unknown action", true, actionsOwnerID, "user", "/" + actionsInstanceID + "/actions/missing", `{}`, http.StatusNotFound},
		{"no registry", false, actionsOwnerID, "user", "/" + actionsInstanceID + "/actions/refresh-db", `{}`, http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := runSubscriber(t, replyWith(http.StatusOK, "application/json", `{}`), replyWith(http.StatusOK, "", ""))
			var registry *hooks.ActionRegistry
			if tt.registry {
				registry = runsRegistry(t, srv.URL)
			}
			runs := NewMockActionRunRepository()
			r := setupActionRunsRouter(t, registry, runs, nil, tt.callerID, tt.role)

			w := doRequest(r, http.MethodPost, "/api/v1/stack-instances"+tt.path, tt.body)
			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			assert.Empty(t, runs.Runs())
		})
	}
}

func TestInvokeAction_RunWriteFailureKeepsResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		runs    *MockActionRunRepository
		wantRun bool
	}{
		{"create error", &MockActionRunRepository{createErr: errors.New("db down")}, false},
		{"no repository", nil, false},
		{"stored", NewMockActionRunRepository(), true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := runSubscriber(t, replyWith(http.StatusAccepted, "application/json", `{"job_id":"job-1"}`), replyWith(http.StatusOK, "", ""))
			r := setupActionRunsRouter(t, runsRegistry(t, srv.URL), tt.runs, nil, actionsOwnerID, "user")

			w := doRequest(r, http.MethodPost, "/api/v1/stack-instances/"+actionsInstanceID+"/actions/refresh-db", `{}`)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			resp := decodeObject(t, w.Body.Bytes())
			assert.Equal(t, "job-1", resp["job_id"])
			assert.Equal(t, float64(http.StatusAccepted), resp["status_code"])
			_, hasRunID := resp["run_id"]
			assert.Equal(t, tt.wantRun, hasRunID)
		})
	}
}

func TestInvokeAction_RunMessageFitsColumn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		msg  string
	}{
		{"ascii above the limit", strings.Repeat("a", 700)},
		{"multibyte cut inside a character", strings.Repeat("€", 400)},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			body, err := json.Marshal(map[string]string{"error": tt.msg})
			require.NoError(t, err)
			srv := runSubscriber(t, replyWith(http.StatusConflict, "application/json", string(body)), replyWith(http.StatusOK, "", ""))
			runs := NewMockActionRunRepository()
			r := setupActionRunsRouter(t, runsRegistry(t, srv.URL), runs, nil, actionsOwnerID, "user")

			w := doRequest(r, http.MethodPost, "/api/v1/stack-instances/"+actionsInstanceID+"/actions/seed-data", `{}`)
			require.Equal(t, http.StatusOK, w.Code)
			resp := decodeObject(t, w.Body.Bytes())
			stored := runs.Runs()
			require.Len(t, stored, 1)
			msg := stored[0].Message
			assert.NotEmpty(t, msg)
			assert.True(t, utf8.ValidString(msg))
			assert.LessOrEqual(t, utf8.RuneCountInString(msg), maxActionRunMessageLen)
			full := hooks.ActionResultMessage(http.StatusConflict, body)
			assert.True(t, strings.HasPrefix(strings.ToValidUTF8(full, ""), msg), "the stored message is the start of the subscriber message")
			assert.NotEmpty(t, resp["message"], "the envelope keeps its message")
		})
	}
}

func TestTruncateRunes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"short", "abc", 5, "abc"},
		{"exact", "abcde", 5, "abcde"},
		{"ascii cut", "abcdef", 5, "abcde"},
		{"multibyte cut", "ééééé", 3, "ééé"},
		{"multibyte fits", "éé", 3, "éé"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, truncateRunes(tt.in, tt.n))
		})
	}
}

func TestGetActionJobLog_StoresFinalStatus(t *testing.T) {
	t.Parallel()

	logReply := func(status, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			if status != "" {
				w.Header().Set(hooks.HeaderJobStatus, status)
			}
			_, _ = w.Write([]byte(body))
		}
	}
	big := strings.Repeat(strings.Repeat("x", 99)+"\n", 3000) // above the 256 KiB cap

	tests := []struct {
		name       string
		callerID   string
		role       string
		reply      http.HandlerFunc
		finalErr   error
		noRepo     bool
		wantHTTP   int
		wantCall   bool
		wantStored string
	}{
		{name: "done stores the status", callerID: actionsOwnerID, role: "user", reply: logReply("succeeded", "done\n"), wantHTTP: http.StatusOK, wantCall: true, wantStored: "succeeded"},
		{name: "end marker stores the status", callerID: "uid-devops", role: "devops", reply: logReply("", "x\n===JOB-END=== status=failed\n"), wantHTTP: http.StatusOK, wantCall: true, wantStored: "failed"},
		{name: "running stores nothing", callerID: actionsOwnerID, role: "user", reply: logReply("running", "step 1\n"), wantHTTP: http.StatusOK},
		{name: "truncated chunk stores nothing", callerID: actionsOwnerID, role: "user", reply: logReply("succeeded", big), wantHTTP: http.StatusOK},
		{name: "upstream error stores nothing", callerID: actionsOwnerID, role: "user", reply: replyWith(http.StatusInternalServerError, "", "boom"), wantHTTP: http.StatusBadGateway},
		{name: "job not found stores nothing", callerID: actionsOwnerID, role: "user", reply: replyWith(http.StatusNotFound, "", ""), wantHTTP: http.StatusNotFound},
		{name: "forbidden stores nothing", callerID: "uid-other", role: "user", reply: logReply("succeeded", "done\n"), wantHTTP: http.StatusForbidden},
		{name: "update error keeps the response", callerID: actionsOwnerID, role: "user", reply: logReply("succeeded", "done\n"), finalErr: errors.New("db down"), wantHTTP: http.StatusOK, wantCall: true},
		{name: "no repository", callerID: actionsOwnerID, role: "user", reply: logReply("succeeded", "done\n"), noRepo: true, wantHTTP: http.StatusOK},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := runSubscriber(t, replyWith(http.StatusOK, "application/json", `{}`), tt.reply)
			runs := NewMockActionRunRepository()
			runs.finalErr = tt.finalErr
			require.NoError(t, runs.Create(t.Context(), &models.ActionRun{
				ID: "run-job", InstanceID: actionsInstanceID, Action: "refresh-db", UserID: actionsOwnerID, StatusCode: 202, JobID: "job-1",
			}))
			repo := runs
			if tt.noRepo {
				repo = nil
			}
			r := setupActionRunsRouter(t, runsRegistry(t, srv.URL), repo, nil, tt.callerID, tt.role)

			w := doRequest(r, http.MethodGet, "/api/v1/stack-instances/"+actionsInstanceID+"/actions/refresh-db/jobs/job-1/log", "")
			require.Equal(t, tt.wantHTTP, w.Code, w.Body.String())
			if tt.wantHTTP == http.StatusOK {
				assert.True(t, validateJSONSchema(t, actionJobLogSchema, w.Body.Bytes()))
			}

			calls := runs.FinalCalls()
			if !tt.wantCall {
				assert.Empty(t, calls)
				return
			}
			require.Len(t, calls, 1)
			assert.Equal(t, finalStatusCall{InstanceID: actionsInstanceID, Action: "refresh-db", JobID: "job-1", Status: calls[0].Status}, calls[0])
			if tt.wantStored != "" {
				assert.Equal(t, tt.wantStored, calls[0].Status)
				stored := runs.Runs()
				assert.Equal(t, tt.wantStored, stored[0].JobStatus)
				assert.NotNil(t, stored[0].FinishedAt)
			}
		})
	}
}

// seedListRuns stores four runs of the test instance and one run of another
// instance, and returns the IDs newest first.
func seedListRuns(t *testing.T, runs *MockActionRunRepository) []string {
	t.Helper()
	base := time.Date(2026, 10, 20, 10, 0, 0, 123_000_000, time.UTC)
	finished := base.Add(time.Minute)
	rows := []models.ActionRun{
		{ID: "r1", InstanceID: actionsInstanceID, Action: "refresh-db", UserID: actionsOwnerID, StartedAt: base, FinishedAt: &finished,
			StatusCode: 202, JobID: "job-1", JobStatus: "succeeded", RequestID: "req-1", Parameters: `{"image":"golden","dry_run":true,"token":"[redacted]"}`},
		{ID: "r2", InstanceID: actionsInstanceID, Action: "removed-action", UserID: "uid-gone", StartedAt: base.Add(time.Second),
			StatusCode: 409, Message: "busy", JobID: "job-2", RequestID: "req-2", Parameters: ""},
		{ID: "r3", InstanceID: actionsInstanceID, Action: "seed-data", UserID: "uid-admin", StartedAt: base.Add(2 * time.Second),
			StatusCode: 200, JobID: "job-3", Parameters: `{}`},
		{ID: "r4", InstanceID: actionsInstanceID, Action: "refresh-db", UserID: "uid-devops", StartedAt: base.Add(3 * time.Second),
			StatusCode: 202, JobID: "job-4", Parameters: `null`},
		{ID: "other", InstanceID: "i-other", Action: "refresh-db", UserID: actionsOwnerID, StartedAt: base.Add(4 * time.Second)},
	}
	for i := range rows {
		require.NoError(t, runs.Create(t.Context(), &rows[i]))
	}
	return []string{"r4", "r3", "r2", "r1"}
}

type actionRunPage struct {
	InstanceID  string           `json:"instance_id"`
	NextCursor  string           `json:"next_cursor"`
	Data        []map[string]any `json:"data"`
	CanViewLogs bool             `json:"can_view_logs"`
}

func TestListActionRuns_Rows(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		callerID    string
		role        string
		canViewLogs bool
	}{
		{"owner", actionsOwnerID, "user", true},
		{"admin", "uid-admin", "admin", true},
		{"devops", "uid-devops", "devops", true},
		{"other user views without logs", "uid-other", "user", false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := runSubscriber(t, replyWith(http.StatusOK, "", ""), replyWith(http.StatusOK, "", ""))
			runs := NewMockActionRunRepository()
			want := seedListRuns(t, runs)
			users := NewMockUserRepository()
			r := setupActionRunsRouter(t, runsRegistry(t, srv.URL), runs, users, tt.callerID, tt.role)

			w := doRequest(r, http.MethodGet, "/api/v1/stack-instances/"+actionsInstanceID+"/action-runs", "")
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			assert.True(t, validateJSONSchema(t, actionRunListSchema, w.Body.Bytes()))
			assert.Equal(t, models.DefaultActionRunPageSize, runs.LastLimit())
			assert.EqualValues(t, 1, users.findByIDsCalls.Load(), "one batch lookup for started_by")

			var page actionRunPage
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &page))
			assert.Equal(t, actionsInstanceID, page.InstanceID)
			assert.Equal(t, tt.canViewLogs, page.CanViewLogs)
			assert.Empty(t, page.NextCursor)
			require.Len(t, page.Data, 4)
			ids := make([]string, 0, len(page.Data))
			byID := map[string]map[string]any{}
			for _, row := range page.Data {
				id := row["id"].(string)
				ids = append(ids, id)
				byID[id] = row
			}
			assert.Equal(t, want, ids, "newest first, other instances excluded")

			r1 := byID["r1"]
			assert.Equal(t, "Refresh database", r1["action_label"])
			assert.Equal(t, "alice", r1["started_by"])
			assert.Equal(t, "2026-10-20T10:00:00.123Z", r1["started_at"])
			assert.Equal(t, "2026-10-20T10:01:00.123Z", r1["finished_at"])
			assert.Equal(t, map[string]any{"image": "golden", "dry_run": true, "token": "[redacted]"}, r1["parameters"])
			assert.Equal(t, "succeeded", r1["job_status"])
			assert.Equal(t, true, r1["has_job_log"])
			assert.Equal(t, "req-1", r1["request_id"])

			r2 := byID["r2"]
			assert.Equal(t, "removed-action", r2["action_label"], "an action that is gone shows its name")
			assert.Equal(t, false, r2["has_job_log"], "an action that is gone has no job log")
			assert.NotContains(t, r2, "started_by", "a missing user omits started_by")
			assert.NotContains(t, r2, "finished_at")
			assert.Equal(t, "busy", r2["message"])
			assert.Equal(t, map[string]any{}, r2["parameters"])

			r3 := byID["r3"]
			assert.Equal(t, "seed-data", r3["action_label"], "no label shows the name")
			assert.Equal(t, "root", r3["started_by"])
			assert.Equal(t, false, r3["has_job_log"], "an action without log_path has no job log")
			assert.NotContains(t, r3, "message")
			assert.NotContains(t, r3, "request_id")
			assert.NotContains(t, r3, "job_status")

			r4 := byID["r4"]
			assert.Equal(t, true, r4["has_job_log"])
			assert.Equal(t, map[string]any{}, r4["parameters"], "a null value gives an empty object")
			assert.NotContains(t, r4, "job_status")
		})
	}
}

func TestListActionRuns_Paging(t *testing.T) {
	t.Parallel()
	srv := runSubscriber(t, replyWith(http.StatusOK, "", ""), replyWith(http.StatusOK, "", ""))
	runs := NewMockActionRunRepository()
	want := seedListRuns(t, runs)
	r := setupActionRunsRouter(t, runsRegistry(t, srv.URL), runs, nil, "uid-other", "user")

	var got []string
	path := "/api/v1/stack-instances/" + actionsInstanceID + "/action-runs?limit=3"
	for page := 0; page < 2; page++ {
		w := doRequest(r, http.MethodGet, path, "")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.True(t, validateJSONSchema(t, actionRunListSchema, w.Body.Bytes()))
		var p actionRunPage
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &p))
		for _, row := range p.Data {
			got = append(got, row["id"].(string))
		}
		if page == 0 {
			require.NotEmpty(t, p.NextCursor)
			path = "/api/v1/stack-instances/" + actionsInstanceID + "/action-runs?limit=3&cursor=" + p.NextCursor
		} else {
			assert.Empty(t, p.NextCursor)
		}
	}
	assert.Equal(t, want, got)
}

func TestListActionRuns_ParamsAndErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		path      string
		listErr   error
		noRepo    bool
		wantHTTP  int
		wantError string
		wantLimit int
	}{
		{name: "limit above the maximum", path: "?limit=500", wantHTTP: http.StatusOK, wantLimit: models.MaxActionRunPageSize},
		{name: "custom limit", path: "?limit=10", wantHTTP: http.StatusOK, wantLimit: 10},
		{name: "empty limit is the default", path: "?limit=", wantHTTP: http.StatusOK, wantLimit: models.DefaultActionRunPageSize},
		{name: "limit 0", path: "?limit=0", wantHTTP: http.StatusBadRequest, wantError: "Invalid limit parameter"},
		{name: "negative limit", path: "?limit=-1", wantHTTP: http.StatusBadRequest, wantError: "Invalid limit parameter"},
		{name: "limit not a number", path: "?limit=ten", wantHTTP: http.StatusBadRequest, wantError: "Invalid limit parameter"},
		{name: "bad cursor", path: "?cursor=garbage", wantHTTP: http.StatusBadRequest, wantError: "Invalid cursor parameter"},
		{name: "database error", listErr: errors.New("connection reset by db-host:3306"), wantHTTP: http.StatusInternalServerError, wantError: msgInternalServerError},
		{name: "no repository", noRepo: true, wantHTTP: http.StatusOK},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := runSubscriber(t, replyWith(http.StatusOK, "", ""), replyWith(http.StatusOK, "", ""))
			runs := NewMockActionRunRepository()
			runs.listErr = tt.listErr
			seedListRuns(t, runs)
			repo := runs
			if tt.noRepo {
				repo = nil
			}
			r := setupActionRunsRouter(t, runsRegistry(t, srv.URL), repo, nil, actionsOwnerID, "user")

			w := doRequest(r, http.MethodGet, "/api/v1/stack-instances/"+actionsInstanceID+"/action-runs"+tt.path, "")
			require.Equal(t, tt.wantHTTP, w.Code, w.Body.String())
			if tt.wantError != "" {
				assert.True(t, validateJSONSchema(t, errorSchema, w.Body.Bytes()))
				assert.Equal(t, tt.wantError, decodeObject(t, w.Body.Bytes())["error"])
				assert.NotContains(t, w.Body.String(), "db-host")
				return
			}
			assert.True(t, validateJSONSchema(t, actionRunListSchema, w.Body.Bytes()))
			if tt.noRepo {
				assert.JSONEq(t, `[]`, mustField(t, w.Body.Bytes(), "data"))
				assert.JSONEq(t, `true`, mustField(t, w.Body.Bytes(), "can_view_logs"))
				return
			}
			assert.Equal(t, tt.wantLimit, runs.LastLimit())
		})
	}
}

func TestListActionRuns_UnknownInstance(t *testing.T) {
	t.Parallel()
	r := setupActionRunsRouter(t, nil, NewMockActionRunRepository(), nil, actionsOwnerID, "user")
	w := doRequest(r, http.MethodGet, "/api/v1/stack-instances/missing/action-runs", "")
	require.Equal(t, http.StatusNotFound, w.Code)
	assert.True(t, validateJSONSchema(t, errorSchema, w.Body.Bytes()))
	assert.Equal(t, "Stack instance not found", decodeObject(t, w.Body.Bytes())["error"])
}

func TestListActionRuns_NoRegistryUsesNames(t *testing.T) {
	t.Parallel()
	runs := NewMockActionRunRepository()
	seedListRuns(t, runs)
	r := setupActionRunsRouter(t, nil, runs, nil, actionsOwnerID, "user")

	w := doRequest(r, http.MethodGet, "/api/v1/stack-instances/"+actionsInstanceID+"/action-runs", "")
	require.Equal(t, http.StatusOK, w.Code)
	var page actionRunPage
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &page))
	require.Len(t, page.Data, 4)
	for _, row := range page.Data {
		assert.Equal(t, row["action"], row["action_label"])
		assert.Equal(t, false, row["has_job_log"])
	}
}

func TestListActions_SensitiveParameter(t *testing.T) {
	t.Parallel()
	srv := runSubscriber(t, replyWith(http.StatusOK, "", ""), replyWith(http.StatusOK, "", ""))
	r := setupActionRunsRouter(t, runsRegistry(t, srv.URL), nil, nil, actionsOwnerID, "user")

	w := doRequest(r, http.MethodGet, "/api/v1/stack-instances/"+actionsInstanceID+"/actions", "")
	require.Equal(t, http.StatusOK, w.Code)
	assert.True(t, validateJSONSchema(t, actionListSchema, w.Body.Bytes()))
	var resp actionListResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Actions, 2)
	params := resp.Actions[0].Parameters
	require.Len(t, params, 4)
	assert.False(t, params[0].Sensitive)
	assert.True(t, params[3].Sensitive)
	assert.Equal(t, 1, strings.Count(w.Body.String(), `"sensitive":true`), "omitted when false")
}

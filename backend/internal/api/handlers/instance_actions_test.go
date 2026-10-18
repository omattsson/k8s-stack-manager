package handlers

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"backend/internal/helm"
	"backend/internal/hooks"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// actionListSchema checks the list response. additionalProperties=false on
// the action and parameter objects makes sure no URL, secret or header
// field can leak into the response.
const actionListSchema = `{
"type": "object",
"required": ["instance_id", "can_invoke", "actions"],
"additionalProperties": false,
"properties": {
"instance_id": {"type": "string"},
"can_invoke": {"type": "boolean"},
"actions": {
"type": "array",
"items": {
"type": "object",
"required": ["name", "label", "parameters", "has_job_log", "can_invoke"],
"additionalProperties": false,
"properties": {
"name": {"type": "string", "minLength": 1},
"label": {"type": "string", "minLength": 1},
"description": {"type": "string"},
"confirm": {"type": "string"},
"has_job_log": {"type": "boolean"},
"can_invoke": {"type": "boolean"},
"parameters": {
"type": "array",
"items": {
"type": "object",
"required": ["name", "label", "type", "required"],
"additionalProperties": false,
"properties": {
"name": {"type": "string"},
"label": {"type": "string"},
"description": {"type": "string"},
"type": {"enum": ["string", "bool", "enum"]},
"required": {"type": "boolean"},
"default": {},
"options": {"type": "array", "items": {"type": "string"}}
}
}
}
}
}
}
}
}`

// actionJobLogSchema checks the job log response.
const actionJobLogSchema = `{
"type": "object",
"required": ["action", "instance_id", "job_id", "status", "log", "offset", "next_offset", "done", "truncated"],
"additionalProperties": false,
"properties": {
"action": {"type": "string"},
"instance_id": {"type": "string"},
"job_id": {"type": "string"},
"status": {"type": "string"},
"log": {"type": "string"},
"offset": {"type": "integer", "minimum": 0},
"next_offset": {"type": "integer", "minimum": 0},
"done": {"type": "boolean"},
"truncated": {"type": "boolean"}
}
}`

const (
	actionsInstanceID = "i-1"
	actionsOwnerID    = "uid-owner"
	actionsSecret     = "s3cr3t-value"
)

// actionSubscriber is a fake action subscriber. It answers invoke calls
// with a job ID and job log reads with the configured handler.
type actionSubscriber struct {
	mu       sync.Mutex
	logReqs  []*http.Request
	invokes  int
	logReply http.HandlerFunc
	srv      *httptest.Server
}

func newActionSubscriber(t *testing.T, logReply http.HandlerFunc) *actionSubscriber {
	t.Helper()
	s := &actionSubscriber{logReply: logReply}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		if r.Method == http.MethodGet {
			s.logReqs = append(s.logReqs, r.Clone(r.Context()))
		} else {
			s.invokes++
		}
		s.mu.Unlock()
		if r.Method == http.MethodGet {
			s.logReply(w, r)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"job_id":"job-0123456789ab","status":"started"}`))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *actionSubscriber) logRequests() []*http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*http.Request(nil), s.logReqs...)
}

func (s *actionSubscriber) invokeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.invokes
}

func (s *actionSubscriber) registry(t *testing.T) *hooks.ActionRegistry {
	t.Helper()
	r, err := hooks.NewActionRegistry([]hooks.ActionSubscription{
		{
			Name:        "refresh-db",
			URL:         s.srv.URL + "/actions/refresh-db",
			Description: "Replace the database",
			Label:       "Refresh database",
			Confirm:     "This replaces the database of the stack.",
			LogPath:     "/jobs/{job_id}/log",
			Secret:      actionsSecret,
			Parameters: []hooks.ActionParameter{
				{Name: "image", Label: "Image", Default: "golden"},
				{Name: "market", Type: "enum", Options: []string{"a", "b"}, Required: true},
				{Name: "dry_run", Type: "bool"},
			},
			TimeoutSeconds: 5,
		},
		{Name: "seed-data", URL: s.srv.URL + "/actions/seed", TimeoutSeconds: 5},
	}, s.srv.Client())
	require.NoError(t, err)
	return r
}

func setupActionsRouter(t *testing.T, registry *hooks.ActionRegistry, callerID, role string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	instRepo := NewMockStackInstanceRepository()
	seedInstance(t, instRepo, actionsInstanceID, "demo", "d1", actionsOwnerID, "running")

	h := NewInstanceHandler(instRepo, NewMockValueOverrideRepository(), NewMockChartBranchOverrideRepository(),
		NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), NewMockStackTemplateRepository(),
		NewMockTemplateChartConfigRepository(), helm.NewValuesGenerator(), NewMockUserRepository(), 0)
	h.WithActions(registry)

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
	return r
}

func doRequest(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	var req *http.Request
	if body != "" {
		req, _ = http.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, _ = http.NewRequest(method, path, nil)
	}
	r.ServeHTTP(w, req)
	return w
}

func TestListActions_PerRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		callerID  string
		role      string
		canInvoke bool
	}{
		{"owner", actionsOwnerID, "user", true},
		{"admin", "uid-admin", "admin", true},
		{"devops", "uid-devops", "devops", true},
		{"other user", "uid-other", "user", false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sub := newActionSubscriber(t, func(w http.ResponseWriter, _ *http.Request) {})
			r := setupActionsRouter(t, sub.registry(t), tt.callerID, tt.role)

			w := doRequest(r, http.MethodGet, "/api/v1/stack-instances/"+actionsInstanceID+"/actions", "")
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			assert.True(t, validateJSONSchema(t, actionListSchema, w.Body.Bytes()))

			body := w.Body.String()
			assert.NotContains(t, body, sub.srv.URL, "subscriber URL must not leak")
			assert.NotContains(t, body, "127.0.0.1")
			assert.NotContains(t, body, actionsSecret)
			assert.NotContains(t, body, "secret")
			assert.NotContains(t, body, "log_path")

			var resp actionListResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, tt.canInvoke, resp.CanInvoke)
			require.Len(t, resp.Actions, 2)

			refresh := resp.Actions[0]
			assert.Equal(t, "refresh-db", refresh.Name)
			assert.Equal(t, "Refresh database", refresh.Label)
			assert.Equal(t, "Replace the database", refresh.Description)
			assert.Equal(t, "This replaces the database of the stack.", refresh.Confirm)
			assert.True(t, refresh.HasJobLog)
			assert.Equal(t, tt.canInvoke, refresh.CanInvoke)
			require.Len(t, refresh.Parameters, 3)
			assert.Equal(t, "Image", refresh.Parameters[0].Label)
			assert.Equal(t, "string", refresh.Parameters[0].Type, "empty type is normalized to string")
			assert.Equal(t, "golden", refresh.Parameters[0].Default)
			assert.Equal(t, "market", refresh.Parameters[1].Label, "label defaults to the name")
			assert.Equal(t, []string{"a", "b"}, refresh.Parameters[1].Options)
			assert.True(t, refresh.Parameters[1].Required)

			seed := resp.Actions[1]
			assert.Equal(t, "seed-data", seed.Label, "label defaults to the name")
			assert.False(t, seed.HasJobLog)
			assert.Empty(t, seed.Parameters)
		})
	}
}

func TestListActions_EmptyAndErrors(t *testing.T) {
	t.Parallel()

	empty, err := hooks.NewActionRegistry(nil, nil)
	require.NoError(t, err)

	tests := []struct {
		name     string
		registry *hooks.ActionRegistry
		path     string
		want     int
	}{
		{"no registry gives empty list", nil, "/api/v1/stack-instances/" + actionsInstanceID + "/actions", http.StatusOK},
		{"empty registry gives empty list", empty, "/api/v1/stack-instances/" + actionsInstanceID + "/actions", http.StatusOK},
		{"unknown instance", empty, "/api/v1/stack-instances/missing/actions", http.StatusNotFound},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := setupActionsRouter(t, tt.registry, actionsOwnerID, "user")
			w := doRequest(r, http.MethodGet, tt.path, "")
			require.Equal(t, tt.want, w.Code, w.Body.String())
			if tt.want == http.StatusOK {
				assert.True(t, validateJSONSchema(t, actionListSchema, w.Body.Bytes()))
				assert.JSONEq(t, `[]`, mustField(t, w.Body.Bytes(), "actions"))
			} else {
				assert.True(t, validateJSONSchema(t, errorSchema, w.Body.Bytes()))
			}
		})
	}
}

func mustField(t *testing.T, data []byte, key string) string {
	t.Helper()
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &m))
	return string(m[key])
}

func TestInvokeAction_ParameterSchemaAndJobID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		action      string
		body        string
		wantStatus  int
		wantJobID   string
		wantInvoked bool
		wantErr     string
	}{
		{"valid params returns job_id", "refresh-db", `{"parameters":{"market":"a","dry_run":true}}`, http.StatusOK, "job-0123456789ab", true, ""},
		{"missing required param", "refresh-db", `{"parameters":{"dry_run":true}}`, http.StatusBadRequest, "", false, "is required"},
		{"enum value not allowed", "refresh-db", `{"parameters":{"market":"z"}}`, http.StatusBadRequest, "", false, "must be one of"},
		{"bool wrong type", "refresh-db", `{"parameters":{"market":"a","dry_run":"yes"}}`, http.StatusBadRequest, "", false, "must be a boolean"},
		{"no log_path gives no job_id", "seed-data", `{}`, http.StatusOK, "", true, ""},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sub := newActionSubscriber(t, func(w http.ResponseWriter, _ *http.Request) {})
			r := setupActionsRouter(t, sub.registry(t), actionsOwnerID, "user")

			w := doRequest(r, http.MethodPost, "/api/v1/stack-instances/"+actionsInstanceID+"/actions/"+tt.action, tt.body)
			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			assert.Equal(t, tt.wantInvoked, sub.invokeCount() == 1)
			if tt.wantErr != "" {
				assert.Contains(t, w.Body.String(), tt.wantErr)
				return
			}
			var resp map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, float64(http.StatusAccepted), resp["status_code"])
			if tt.wantJobID == "" {
				_, has := resp["job_id"]
				assert.False(t, has)
			} else {
				assert.Equal(t, tt.wantJobID, resp["job_id"])
			}
		})
	}
}

func TestAsyncJobID(t *testing.T) {
	t.Parallel()
	withLog := hooks.ActionSubscription{Name: "a", LogPath: "/jobs/{job_id}/log"}
	tests := []struct {
		name   string
		sub    hooks.ActionSubscription
		code   int
		result string
		want   string
	}{
		{"valid 200", withLog, 200, `{"job_id":"job-1"}`, "job-1"},
		{"valid 202", withLog, 202, `{"job_id":"job-1"}`, "job-1"},
		{"non-2xx 409", withLog, 409, `{"job_id":"job-1"}`, ""},
		{"non-2xx 500", withLog, 500, `{"job_id":"job-1"}`, ""},
		{"redirect 302", withLog, 302, `{"job_id":"job-1"}`, ""},
		{"no log path", hooks.ActionSubscription{Name: "a"}, 200, `{"job_id":"job-1"}`, ""},
		{"invalid id", withLog, 200, `{"job_id":"../x"}`, ""},
		{"number id", withLog, 200, `{"job_id":12}`, ""},
		{"array result", withLog, 200, `["job-1"]`, ""},
		{"null result", withLog, 200, `null`, ""},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, asyncJobID(tt.sub, tt.code, json.RawMessage(tt.result)))
		})
	}
}

func TestGetActionJobLog(t *testing.T) {
	t.Parallel()

	logOK := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(hooks.HeaderLogOffset, "123")
		w.Header().Set(hooks.HeaderJobStatus, "running")
		_, _ = w.Write([]byte("step 1\nstep 2\n"))
	}
	logStatus := func(code int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"error":"stat /var/log/jobs/` + r.URL.Path + `: internal detail"}`))
		}
	}
	base := "/api/v1/stack-instances/" + actionsInstanceID + "/actions"

	tests := []struct {
		name         string
		registryNil  bool
		callerID     string
		role         string
		path         string
		reply        http.HandlerFunc
		wantStatus   int
		wantUpstream bool
		wantErr      string
	}{
		{"owner reads log", false, actionsOwnerID, "user", base + "/refresh-db/jobs/job-0123456789ab/log?offset=7", logOK, http.StatusOK, true, ""},
		{"admin reads log", false, "uid-admin", "admin", base + "/refresh-db/jobs/job-0123456789ab/log", logOK, http.StatusOK, true, ""},
		{"devops reads log", false, "uid-devops", "devops", base + "/refresh-db/jobs/job-0123456789ab/log", logOK, http.StatusOK, true, ""},
		{"other user forbidden", false, "uid-other", "user", base + "/refresh-db/jobs/job-0123456789ab/log", logOK, http.StatusForbidden, false, msgInstanceModifyForbidden},
		{"invalid job_id dot-dot", false, actionsOwnerID, "user", base + "/refresh-db/jobs/../log", logOK, http.StatusBadRequest, false, "invalid job_id"},
		{"invalid job_id leading dot", false, actionsOwnerID, "user", base + "/refresh-db/jobs/.x/log", logOK, http.StatusBadRequest, false, "invalid job_id"},
		// The router decodes %2F to a second segment: no route matches.
		{"invalid job_id encoded slash", false, actionsOwnerID, "user", base + "/refresh-db/jobs/a%2Fb/log", logOK, http.StatusNotFound, false, ""},
		{"invalid job_id too long", false, actionsOwnerID, "user", base + "/refresh-db/jobs/" + strings.Repeat("a", 129) + "/log", logOK, http.StatusBadRequest, false, "invalid job_id"},
		{"negative offset", false, actionsOwnerID, "user", base + "/refresh-db/jobs/job-1/log?offset=-1", logOK, http.StatusBadRequest, false, "offset"},
		{"non-numeric offset", false, actionsOwnerID, "user", base + "/refresh-db/jobs/job-1/log?offset=abc", logOK, http.StatusBadRequest, false, "offset"},
		{"offset query injection", false, actionsOwnerID, "user", base + "/refresh-db/jobs/job-1/log?offset=1%26x%3D2", logOK, http.StatusBadRequest, false, "offset"},
		{"huge offset", false, actionsOwnerID, "user", base + "/refresh-db/jobs/job-1/log?offset=99999999999999999999", logOK, http.StatusBadRequest, false, "offset"},
		{"unknown action", false, actionsOwnerID, "user", base + "/missing/jobs/job-1/log", logOK, http.StatusNotFound, false, "unknown action"},
		{"action without log_path", false, actionsOwnerID, "user", base + "/seed-data/jobs/job-1/log", logOK, http.StatusNotFound, false, "no job log"},
		{"unknown instance", false, actionsOwnerID, "user", "/api/v1/stack-instances/missing/actions/refresh-db/jobs/job-1/log", logOK, http.StatusNotFound, false, ""},
		{"job not found upstream", false, actionsOwnerID, "user", base + "/refresh-db/jobs/job-1/log", logStatus(http.StatusNotFound), http.StatusNotFound, true, "job log not found"},
		{"upstream error gives 502", false, actionsOwnerID, "user", base + "/refresh-db/jobs/job-1/log", logStatus(http.StatusInternalServerError), http.StatusBadGateway, true, "unreachable or returned an error"},
		{"no registry", true, actionsOwnerID, "user", base + "/refresh-db/jobs/job-1/log", logOK, http.StatusServiceUnavailable, false, "not configured"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sub := newActionSubscriber(t, tt.reply)
			var registry *hooks.ActionRegistry
			if !tt.registryNil {
				registry = sub.registry(t)
			}
			r := setupActionsRouter(t, registry, tt.callerID, tt.role)

			w := doRequest(r, http.MethodGet, tt.path, "")
			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())

			body := w.Body.String()
			assert.NotContains(t, body, sub.srv.URL, "subscriber URL must not leak")
			assert.NotContains(t, body, "127.0.0.1")
			assert.NotContains(t, body, "internal detail", "subscriber error body must not leak")
			if tt.wantErr != "" {
				assert.Contains(t, body, tt.wantErr)
			}
			if tt.wantUpstream {
				require.Len(t, sub.logRequests(), 1)
			} else {
				assert.Empty(t, sub.logRequests(), "subscriber must not be called")
			}
			if tt.wantStatus != http.StatusOK {
				return
			}

			assert.True(t, validateJSONSchema(t, actionJobLogSchema, w.Body.Bytes()))
			var resp actionJobLogResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, "refresh-db", resp.Action)
			assert.Equal(t, actionsInstanceID, resp.InstanceID)
			assert.Equal(t, "job-0123456789ab", resp.JobID)
			assert.Equal(t, "step 1\nstep 2\n", resp.Log)
			assert.Equal(t, int64(123), resp.NextOffset)
			assert.Equal(t, "running", resp.Status)
			assert.False(t, resp.Done)

			// The outbound request is signed (HMAC over the request URI) and
			// carries the offset and the instance ID.
			out := sub.logRequests()[0]
			assert.Equal(t, "/jobs/job-0123456789ab/log", out.URL.Path)
			assert.Equal(t, actionsInstanceID, out.URL.Query().Get("instance_id"))
			assert.Equal(t, "action-log:refresh-db", out.Header.Get("X-StackManager-Event"))
			sig := out.Header.Get("X-StackManager-Signature")
			assert.True(t, strings.HasPrefix(sig, "sha256="), "signature header present")
			assert.Equal(t, expectedSignature(out.URL.RequestURI(), actionsSecret), sig)
			if strings.Contains(tt.path, "offset=7") {
				assert.Equal(t, int64(7), resp.Offset)
				assert.Equal(t, "7", out.URL.Query().Get("offset"))
			} else {
				assert.Equal(t, int64(0), resp.Offset)
				assert.Equal(t, "0", out.URL.Query().Get("offset"))
			}
		})
	}
}

func TestGetActionJobLog_SizeCapAndDone(t *testing.T) {
	t.Parallel()
	big := strings.Repeat(strings.Repeat("x", 99)+"\n", 5000) // 500 KB, above the 256 KiB cap
	tests := []struct {
		name          string
		body          string
		status        string
		wantTruncated bool
		wantDone      bool
	}{
		{"capped chunk is truncated and not done", big, "succeeded", true, false},
		{"end marker ends the job", "last\n===JOB-END=== status=failed\n", "", false, true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sub := newActionSubscriber(t, func(w http.ResponseWriter, _ *http.Request) {
				if tt.status != "" {
					w.Header().Set(hooks.HeaderJobStatus, tt.status)
				}
				_, _ = w.Write([]byte(tt.body))
			})
			r := setupActionsRouter(t, sub.registry(t), actionsOwnerID, "user")
			w := doRequest(r, http.MethodGet, "/api/v1/stack-instances/"+actionsInstanceID+"/actions/refresh-db/jobs/job-1/log", "")
			require.Equal(t, http.StatusOK, w.Code)
			var resp actionJobLogResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, tt.wantTruncated, resp.Truncated)
			assert.Equal(t, tt.wantDone, resp.Done)
			assert.LessOrEqual(t, len(resp.Log), 256<<10)
			assert.Equal(t, int64(len(resp.Log)), resp.NextOffset)
		})
	}
}

// expectedSignature is the HMAC-SHA256 signature a subscriber checks.
func expectedSignature(message, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(message))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestInvokeAction_RefusalMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		code        int
		contentType string
		body        string
		wantMessage string
	}{
		{"409 json error", http.StatusConflict, "application/json", `{"error":"refresh-db already in flight for this stack"}`, "refresh-db already in flight for this stack"},
		{"409 plain text", http.StatusConflict, "text/plain", "refresh-db already in flight", "refresh-db already in flight"},
		{"500 without message", http.StatusInternalServerError, "application/json", `{"code":17}`, ""},
		{"202 has no message", http.StatusAccepted, "application/json", `{"message":"started","job_id":"job-0123456789ab"}`, ""},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tt.contentType)
				w.WriteHeader(tt.code)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(srv.Close)
			reg, err := hooks.NewActionRegistry([]hooks.ActionSubscription{
				{Name: "refresh-db", URL: srv.URL + "/actions/refresh-db", TimeoutSeconds: 5},
			}, nil)
			require.NoError(t, err)
			r := setupActionsRouter(t, reg, actionsOwnerID, "user")

			w := doRequest(r, http.MethodPost, "/api/v1/stack-instances/"+actionsInstanceID+"/actions/refresh-db", `{}`)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			var resp map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, float64(tt.code), resp["status_code"])
			if tt.wantMessage == "" {
				_, has := resp["message"]
				assert.False(t, has)
			} else {
				assert.Equal(t, tt.wantMessage, resp["message"])
			}
			assert.NotContains(t, w.Body.String(), srv.URL, "the subscriber URL must not leak")
		})
	}
}

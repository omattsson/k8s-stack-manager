package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"backend/internal/hooks"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scopedRegistry returns a registry with an action for team A, one for
// team B and one for every instance. The instance of the actions routers
// (definition d1) has a template of team A; lookupErr makes the template
// lookup fail.
func scopedRegistry(t *testing.T, sub *actionSubscriber, lookupErr error) *hooks.ActionRegistry {
	t.Helper()
	reg, err := hooks.NewActionRegistry([]hooks.ActionSubscription{
		{Name: "refresh-db-a", URL: sub.srv.URL + "/actions/a", LogPath: "/jobs/{job_id}/log", TimeoutSeconds: 5,
			AppliesTo: hooks.Scope{TemplateCategories: []string{"team-a"}}},
		{Name: "refresh-db-b", URL: sub.srv.URL + "/actions/b", LogPath: "/jobs/{job_id}/log", TimeoutSeconds: 5,
			AppliesTo: hooks.Scope{TemplateCategories: []string{"team-b"}}},
		{Name: "restart", URL: sub.srv.URL + "/actions/restart", TimeoutSeconds: 5},
	}, sub.srv.Client())
	require.NoError(t, err)
	templates := hooks.NewTemplateResolver(func(_ context.Context, ids []string) (map[string]hooks.TemplateRef, error) {
		if lookupErr != nil {
			return nil, lookupErr
		}
		return map[string]hooks.TemplateRef{"d1": {ID: "tpl-a", Name: "Team A", Category: "Team-A"}}, nil
	})
	t.Cleanup(templates.Stop)
	return reg.WithTemplates(templates)
}

func TestListActions_AppliesTo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		lookupErr      error
		wantActions    []string
		wantIncomplete bool
	}{
		{name: "only the actions of the instance's template", wantActions: []string{"refresh-db-a", "restart"}},
		{name: "lookup failed", lookupErr: errors.New("db down"), wantActions: []string{"restart"}, wantIncomplete: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sub := newActionSubscriber(t, func(http.ResponseWriter, *http.Request) {})
			r := setupActionsRouter(t, scopedRegistry(t, sub, tt.lookupErr), actionsOwnerID, "user")

			w := doRequest(r, http.MethodGet, "/api/v1/stack-instances/"+actionsInstanceID+"/actions", "")
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			assert.True(t, validateJSONSchema(t, actionListSchema, w.Body.Bytes()))
			assert.NotContains(t, w.Body.String(), "applies_to")

			var resp actionListResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			names := make([]string, 0, len(resp.Actions))
			for _, a := range resp.Actions {
				names = append(names, a.Name)
			}
			assert.Equal(t, tt.wantActions, names)
			assert.Equal(t, tt.wantIncomplete, resp.ScopeIncomplete)
		})
	}
}

func TestInvokeAction_AppliesTo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		action      string
		body        string
		lookupErr   error
		wantStatus  int
		wantErr     string
		wantInvokes int
		wantRuns    int
	}{
		{name: "in scope", action: "refresh-db-a", wantStatus: http.StatusOK, wantInvokes: 1, wantRuns: 1},
		{name: "out of scope", action: "refresh-db-b", wantStatus: http.StatusNotFound, wantErr: `unknown action "refresh-db-b"`},
		{name: "malformed body is checked first", action: "refresh-db-b", body: `{"parameters":"x"}`,
			wantStatus: http.StatusBadRequest, wantErr: msgInvalidRequestFormat},
		{name: "unknown action", action: "nope", wantStatus: http.StatusNotFound, wantErr: `unknown action "nope"`},
		{name: "scope unknown", action: "refresh-db-a", lookupErr: errors.New("db down"),
			wantStatus: http.StatusServiceUnavailable, wantErr: msgActionScopeUnknown},
		{name: "unscoped action with a failed lookup", action: "restart", lookupErr: errors.New("db down"),
			wantStatus: http.StatusOK, wantInvokes: 1, wantRuns: 1},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sub := newActionSubscriber(t, func(http.ResponseWriter, *http.Request) {})
			runs := NewMockActionRunRepository()
			r := setupActionRunsRouter(t, scopedRegistry(t, sub, tt.lookupErr), runs, nil, actionsOwnerID, "user")

			w := doRequest(r, http.MethodPost, "/api/v1/stack-instances/"+actionsInstanceID+"/actions/"+tt.action, tt.body)
			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantErr != "" {
				assert.True(t, validateJSONSchema(t, errorSchema, w.Body.Bytes()))
				assert.Equal(t, tt.wantErr, decodeObject(t, w.Body.Bytes())["error"])
			}
			assert.Equal(t, tt.wantInvokes, sub.invokeCount())
			assert.Len(t, runs.Runs(), tt.wantRuns)
		})
	}
}

// TestGetActionJobLog_IgnoresAppliesTo checks that the job log of an action
// stays readable for an instance outside its scope (a run that started
// before a config change).
func TestGetActionJobLog_IgnoresAppliesTo(t *testing.T) {
	t.Parallel()
	sub := newActionSubscriber(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(hooks.HeaderJobStatus, "succeeded")
		_, _ = w.Write([]byte("done\n"))
	})
	r := setupActionsRouter(t, scopedRegistry(t, sub, nil), actionsOwnerID, "user")

	w := doRequest(r, http.MethodGet, "/api/v1/stack-instances/"+actionsInstanceID+"/actions/refresh-db-b/jobs/job-1/log", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.True(t, validateJSONSchema(t, actionJobLogSchema, w.Body.Bytes()))
	assert.Len(t, sub.logRequests(), 1)
}

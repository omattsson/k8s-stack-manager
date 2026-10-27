package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resolvedRef returns an InstanceRef whose template lookup answered.
func resolvedRef(defID, clusterID, templateID, category string) *InstanceRef {
	return &InstanceRef{
		ID: "i1", StackDefinitionID: defID, ClusterID: clusterID,
		TemplateID: templateID, TemplateCategory: category, templateResolved: true,
	}
}

func TestScope_Decide(t *testing.T) {
	t.Parallel()
	unresolved := &InstanceRef{ID: "i1", StackDefinitionID: "d1", ClusterID: "c1"}

	tests := []struct {
		name  string
		scope Scope
		ref   *InstanceRef
		want  scopeDecision
	}{
		{"empty scope matches", Scope{}, unresolved, scopeMatch},
		{"empty scope matches a nil ref", Scope{}, nil, scopeMatch},
		{"scope with a nil ref", Scope{ClusterIDs: []string{"c1"}}, nil, scopeNoMatch},
		{"cluster match", Scope{ClusterIDs: []string{"c0", "c1"}}, unresolved, scopeMatch},
		{"cluster no match", Scope{ClusterIDs: []string{"c2"}}, unresolved, scopeNoMatch},
		{"definition match", Scope{DefinitionIDs: []string{"d1"}}, unresolved, scopeMatch},
		{"definition no match", Scope{DefinitionIDs: []string{"d2"}}, unresolved, scopeNoMatch},
		{"template needs lookup", Scope{TemplateIDs: []string{"t1"}}, unresolved, scopeUnknown},
		{"category needs lookup", Scope{TemplateCategories: []string{"team-a"}}, unresolved, scopeUnknown},
		{"cluster decides before the lookup", Scope{ClusterIDs: []string{"c2"}, TemplateIDs: []string{"t1"}}, unresolved, scopeNoMatch},
		{"template match", Scope{TemplateIDs: []string{"t1"}}, resolvedRef("d1", "c1", "t1", ""), scopeMatch},
		{"template no match", Scope{TemplateIDs: []string{"t1"}}, resolvedRef("d1", "c1", "t2", ""), scopeNoMatch},
		{"no template", Scope{TemplateIDs: []string{"t1"}}, resolvedRef("d1", "c1", "", ""), scopeNoMatch},
		{"category match, case and space", Scope{TemplateCategories: []string{"team-a"}}, resolvedRef("d1", "c1", "t1", "  Team-A "), scopeMatch},
		{"category no match", Scope{TemplateCategories: []string{"team-a"}}, resolvedRef("d1", "c1", "t1", "team-b"), scopeNoMatch},
		{"category empty", Scope{TemplateCategories: []string{"team-a"}}, resolvedRef("d1", "c1", "t1", ""), scopeNoMatch},
		{"fields combine with AND", Scope{TemplateCategories: []string{"team-a"}, ClusterIDs: []string{"c1"}}, resolvedRef("d1", "c1", "t1", "team-a"), scopeMatch},
		{"AND, one field fails", Scope{TemplateCategories: []string{"team-a"}, DefinitionIDs: []string{"d9"}}, resolvedRef("d1", "c1", "t1", "team-a"), scopeNoMatch},
		{"empty instance cluster", Scope{ClusterIDs: []string{"c1"}}, &InstanceRef{ID: "i1"}, scopeNoMatch},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.scope.decide(tt.ref))
		})
	}
}

func TestScope_Validate(t *testing.T) {
	t.Parallel()
	many := make([]string, maxScopeValues+1)
	for i := range many {
		many[i] = "c"
	}

	tests := []struct {
		name    string
		scope   Scope
		want    Scope
		wantErr string
	}{
		{name: "empty", scope: Scope{}, want: Scope{}},
		{
			name:  "trims and lowers categories",
			scope: Scope{TemplateCategories: []string{" Team-A "}, ClusterIDs: []string{" c1 "}},
			want:  Scope{TemplateCategories: []string{"team-a"}, ClusterIDs: []string{"c1"}},
		},
		{name: "empty value", scope: Scope{TemplateIDs: []string{" "}}, wantErr: "applies_to.template_ids[0]: must not be empty"},
		{name: "too many values", scope: Scope{ClusterIDs: many}, wantErr: "applies_to.cluster_ids: at most 50 values"},
		{name: "ID too long", scope: Scope{DefinitionIDs: []string{strings.Repeat("a", 37)}}, wantErr: "applies_to.definition_ids[0]: at most 36 characters"},
		{name: "category too long", scope: Scope{TemplateCategories: []string{strings.Repeat("a", 256)}}, wantErr: "applies_to.template_categories[0]: at most 255 characters"},
		{name: "cluster all", scope: Scope{ClusterIDs: []string{"c1", " all "}}, wantErr: `applies_to.cluster_ids: "all" matches no instance; omit cluster_ids to match every cluster`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := tt.scope
			err := s.Validate()
			if tt.wantErr != "" {
				assert.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, s)
		})
	}
}

func TestScope_ValidateKeepsCallerSlice(t *testing.T) {
	t.Parallel()
	cats := []string{"Team-A"}
	s := Scope{TemplateCategories: cats}
	require.NoError(t, s.Validate())
	assert.Equal(t, "Team-A", cats[0])
	assert.Equal(t, "team-a", s.TemplateCategories[0])
}

func TestScope_UnmarshalRejectsUnknownFields(t *testing.T) {
	t.Parallel()
	var spec ActionSubscriptionSpec
	err := json.Unmarshal([]byte(`{"name":"a","applies_to":{"template_categoreis":["x"]}}`), &spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "template_categoreis")

	require.NoError(t, json.Unmarshal([]byte(`{"name":"a","applies_to":{"template_categories":["x"]}}`), &spec))
	assert.Equal(t, []string{"x"}, spec.AppliesTo.TemplateCategories)
	require.NoError(t, json.Unmarshal([]byte(`{"name":"b","applies_to":null}`), &spec))
}

func TestLoadConfigFile_AppliesTo(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "hooks.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
  "subscriptions": [
    {"name":"gate","events":["pre-deploy"],"url":"https://example.com/g","applies_to":{"template_ids":["t1"],"cluster_ids":["c1"]}}
  ],
  "actions": [
    {"name":"refresh-db","url":"https://example.com/a","applies_to":{"template_categories":["Team-A"]}}
  ]
}`), 0o600))
	cfg, actions, err := LoadConfigFile(path)
	require.NoError(t, err)
	require.Len(t, cfg.Subscriptions, 1)
	assert.Equal(t, Scope{TemplateIDs: []string{"t1"}, ClusterIDs: []string{"c1"}}, cfg.Subscriptions[0].AppliesTo)
	require.Len(t, actions, 1)
	assert.Equal(t, []string{"Team-A"}, actions[0].AppliesTo.TemplateCategories)

	reg, err := NewActionRegistry(actions, nil)
	require.NoError(t, err)
	sub, _ := reg.Lookup("refresh-db")
	assert.Equal(t, []string{"team-a"}, sub.AppliesTo.TemplateCategories, "validation normalizes the category")
}

func TestLoadConfigFile_MisspeltAppliesTo(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name:    "camel case on a subscription",
			body:    `{"subscriptions":[{"name":"g","events":["pre-deploy"],"url":"https://example.com","appliesTo":{"template_ids":["t1"]}}]}`,
			wantErr: `subscription[0]: unknown field "appliesTo", did you mean "applies_to"?`,
		},
		{
			name:    "dash on an action",
			body:    `{"actions":[{"name":"a","url":"https://example.com"},{"name":"b","url":"https://example.com","applies-to":{}}]}`,
			wantErr: `action[1]: unknown field "applies-to", did you mean "applies_to"?`,
		},
		{
			name: "other case of the field itself",
			body: `{"actions":[{"name":"a","url":"https://example.com","Applies_To":{"cluster_ids":["c1"]}}]}`,
		},
		{
			name: "other unknown keys stay allowed",
			body: `{"actions":[{"name":"a","url":"https://example.com","_comment":"x"}]}`,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "hooks.json")
			require.NoError(t, os.WriteFile(path, []byte(tt.body), 0o600))
			_, actions, err := LoadConfigFile(path)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			if tt.name == "other case of the field itself" {
				assert.Equal(t, []string{"c1"}, actions[0].AppliesTo.ClusterIDs)
			}
		})
	}
}

func TestConfigValidate_AppliesTo(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		sub     Subscription
		wantErr string
	}{
		{
			name: "valid",
			sub:  Subscription{Name: "s", Events: []string{EventPreDeploy}, URL: "https://example.com", AppliesTo: Scope{TemplateCategories: []string{"team-a"}}},
		},
		{
			name:    "cleanup-policy-executed",
			sub:     Subscription{Name: "s", Events: []string{EventPreDeploy, EventCleanupPolicyExecuted}, URL: "https://example.com", AppliesTo: Scope{ClusterIDs: []string{"c1"}}},
			wantErr: `subscription "s": applies_to cannot be used with the cleanup-policy-executed event`,
		},
		{
			name: "cleanup-policy-executed without a scope",
			sub:  Subscription{Name: "s", Events: []string{EventCleanupPolicyExecuted}, URL: "https://example.com"},
		},
		{
			name:    "invalid value",
			sub:     Subscription{Name: "s", Events: []string{EventPreDeploy}, URL: "https://example.com", AppliesTo: Scope{ClusterIDs: []string{""}}},
			wantErr: `subscription "s": applies_to.cluster_ids[0]: must not be empty`,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := Config{Subscriptions: []Subscription{tt.sub}}
			err := cfg.Validate()
			if tt.wantErr != "" {
				assert.EqualError(t, err, tt.wantErr)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestNewActionRegistry_InvalidAppliesTo(t *testing.T) {
	t.Parallel()
	_, err := NewActionRegistry([]ActionSubscription{{
		Name: "a", URL: "https://example.com", AppliesTo: Scope{TemplateIDs: []string{""}},
	}}, nil)
	assert.EqualError(t, err, "action[0]: applies_to.template_ids[0]: must not be empty")
}

// staticTemplates returns a resolver whose lookup answers with refs (keyed
// by definition ID), or with err.
func staticTemplates(t *testing.T, refs map[string]TemplateRef, err error) *TemplateResolver {
	t.Helper()
	r := NewTemplateResolver(func(ctx context.Context, ids []string) (map[string]TemplateRef, error) {
		if err != nil {
			return nil, err
		}
		out := map[string]TemplateRef{}
		for _, id := range ids {
			if ref, ok := refs[id]; ok {
				out[id] = ref
			}
		}
		return out, nil
	})
	t.Cleanup(r.Stop)
	return r
}

var teamTemplates = map[string]TemplateRef{
	"def-a": {ID: "tpl-a", Name: "Team A stack", Category: "Team-A"},
	"def-b": {ID: "tpl-b", Name: "Team B stack", Category: "team-b"},
}

func TestDispatcher_FireAppliesTo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		defID     string
		lookupErr error
		policy    FailurePolicy
		wantCalls []string
		wantErr   bool
	}{
		{name: "team A instance", defID: "def-a", policy: FailurePolicyFail, wantCalls: []string{"/all", "/team-a"}},
		{name: "team B instance", defID: "def-b", policy: FailurePolicyFail, wantCalls: []string{"/all"}},
		{name: "no template", defID: "def-x", policy: FailurePolicyFail, wantCalls: []string{"/all"}},
		{name: "lookup failed, fail policy", defID: "def-a", lookupErr: errors.New("db down"), policy: FailurePolicyFail, wantCalls: []string{"/all"}, wantErr: true},
		{name: "lookup failed, ignore policy", defID: "def-a", lookupErr: errors.New("db down"), policy: FailurePolicyIgnore, wantCalls: []string{"/all", "/after"}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := &recordingServer{}
			srv := httptest.NewServer(rec.handler(nil))
			t.Cleanup(srv.Close)

			d, err := NewDispatcher(Config{Subscriptions: []Subscription{
				{Name: "all", Events: []string{EventPreDeploy}, URL: srv.URL + "/all"},
				{Name: "team-a", Events: []string{EventPreDeploy}, URL: srv.URL + "/team-a", FailurePolicy: tt.policy,
					AppliesTo: Scope{TemplateCategories: []string{"team-a"}}},
				{Name: "after", Events: []string{EventPreDeploy}, URL: srv.URL + "/after", AppliesTo: Scope{ClusterIDs: []string{"c9"}}},
			}}, srv.Client())
			require.NoError(t, err)
			d.WithTemplates(staticTemplates(t, teamTemplates, tt.lookupErr))

			ref := &InstanceRef{ID: "i1", StackDefinitionID: tt.defID, ClusterID: "c1"}
			if tt.wantCalls[len(tt.wantCalls)-1] == "/after" {
				ref.ClusterID = "c9"
			}
			fireErr := d.Fire(context.Background(), EventPreDeploy, EventEnvelope{InstanceRef: ref})
			if tt.wantErr {
				var failed *FailedError
				require.ErrorAs(t, fireErr, &failed)
				assert.Equal(t, "team-a", failed.Hook)
				assert.ErrorIs(t, fireErr, ErrScopeUnknown)
				assert.Equal(t, `pre-deploy hook "team-a" failed (could not check where it applies; try again)`,
					UserMessage(fireErr, EventPreDeploy, "deployment"))
			} else {
				require.NoError(t, fireErr)
			}
			assert.Equal(t, tt.wantCalls, rec.Calls())
		})
	}
}

func TestDispatcher_EnvelopeTemplateFields(t *testing.T) {
	t.Parallel()
	srv, last := captureServer(t)
	d, err := NewDispatcher(Config{Subscriptions: []Subscription{{
		Name: "s", Events: []string{EventPostDeploy}, URL: srv.URL,
	}}}, srv.Client())
	require.NoError(t, err)
	d.WithTemplates(staticTemplates(t, teamTemplates, nil))

	ref := &InstanceRef{ID: "i1", StackDefinitionID: "def-a"}
	require.NoError(t, d.Fire(context.Background(), EventPostDeploy, EventEnvelope{InstanceRef: ref}))
	inst, _ := last()["instance"].(map[string]any)
	assert.Equal(t, "tpl-a", inst["template_id"])
	assert.Equal(t, "Team A stack", inst["template_name"])
	assert.Equal(t, "Team-A", inst["template_category"], "the envelope keeps the stored category")
	assert.Empty(t, ref.TemplateID, "the caller's InstanceRef is not changed")

	require.NoError(t, d.Fire(context.Background(), EventPostDeploy, EventEnvelope{InstanceRef: &InstanceRef{ID: "i2", StackDefinitionID: "def-x"}}))
	inst, _ = last()["instance"].(map[string]any)
	assert.NotContains(t, inst, "template_id")
	assert.NotContains(t, inst, "template_category")
}

func TestDispatcher_BlockingFor(t *testing.T) {
	t.Parallel()
	rec := &recordingServer{}
	srv := httptest.NewServer(rec.handler(nil))
	t.Cleanup(srv.Close)

	d, err := NewDispatcher(Config{Subscriptions: []Subscription{
		{Name: "restore-a", Events: []string{EventPostDeploy}, URL: srv.URL + "/restore-a", Blocking: true, TimeoutSeconds: 600,
			FailurePolicy: FailurePolicyFail, AppliesTo: Scope{TemplateCategories: []string{"team-a"}}},
		{Name: "seed-a", Events: []string{EventPostDeploy}, URL: srv.URL + "/seed-a", Blocking: true, TimeoutSeconds: 60,
			FailurePolicy: FailurePolicyIgnore, AppliesTo: Scope{TemplateCategories: []string{"team-a"}}},
		{Name: "warm-c2", Events: []string{EventPostDeploy}, URL: srv.URL + "/warm-c2", Blocking: true, TimeoutSeconds: 30,
			AppliesTo: Scope{ClusterIDs: []string{"c2"}}},
	}}, srv.Client())
	require.NoError(t, err)
	d.WithTemplates(staticTemplates(t, teamTemplates, nil))
	ctx := context.Background()

	assert.True(t, d.HasBlocking(EventPostDeploy))
	assert.Equal(t, 690*time.Second, d.BlockingTimeout(EventPostDeploy), "the global sum is an upper bound")

	teamA := d.ResolveInstance(ctx, &InstanceRef{ID: "i1", StackDefinitionID: "def-a", ClusterID: "c1"})
	assert.True(t, d.HasBlockingFor(EventPostDeploy, teamA))
	assert.Equal(t, 660*time.Second, d.BlockingTimeoutFor(EventPostDeploy, teamA))

	teamB := d.ResolveInstance(ctx, &InstanceRef{ID: "i2", StackDefinitionID: "def-b", ClusterID: "c1"})
	assert.False(t, d.HasBlockingFor(EventPostDeploy, teamB))
	assert.Zero(t, d.BlockingTimeoutFor(EventPostDeploy, teamB))

	// Unresolved: the template-scoped subscribers count (FireBlocking fails
	// on the fail-policy one and skips the ignore one); the cluster scope
	// still decides.
	unknown := &InstanceRef{ID: "i3", StackDefinitionID: "def-a", ClusterID: "c1"}
	assert.True(t, d.HasBlockingFor(EventPostDeploy, unknown))
	assert.Equal(t, 660*time.Second, d.BlockingTimeoutFor(EventPostDeploy, unknown))

	ignored, err := d.FireBlocking(ctx, EventPostDeploy, EventEnvelope{InstanceRef: teamA}, BlockingCallbacks{})
	require.NoError(t, err)
	assert.Empty(t, ignored)
	assert.Equal(t, []string{"/restore-a", "/seed-a"}, rec.Calls())

	ignored, err = d.FireBlocking(ctx, EventPostDeploy, EventEnvelope{InstanceRef: teamB}, BlockingCallbacks{})
	require.NoError(t, err)
	assert.Empty(t, ignored)
	assert.Len(t, rec.Calls(), 2, "no subscriber applies to team B")
}

func TestDispatcher_FireBlockingScopeUnknown(t *testing.T) {
	t.Parallel()
	rec := &recordingServer{}
	srv := httptest.NewServer(rec.handler(nil))
	t.Cleanup(srv.Close)

	d, err := NewDispatcher(Config{Subscriptions: []Subscription{
		{Name: "restore-a", Events: []string{EventPostDeploy}, URL: srv.URL + "/restore-a", Blocking: true,
			FailurePolicy: FailurePolicyFail, AppliesTo: Scope{TemplateIDs: []string{"tpl-a"}}},
	}}, srv.Client())
	require.NoError(t, err)
	d.WithTemplates(staticTemplates(t, nil, errors.New("db down")))

	var started []string
	_, err = d.FireBlocking(context.Background(), EventPostDeploy,
		EventEnvelope{InstanceRef: &InstanceRef{ID: "i1", StackDefinitionID: "def-a"}},
		BlockingCallbacks{OnStart: func(h string) { started = append(started, h) }})
	assert.ErrorIs(t, err, ErrScopeUnknown)
	assert.Empty(t, started, "the subscriber was not started")
	assert.Empty(t, rec.Calls())
}

func TestDispatcher_FireBlockingScopeUnknownIgnore(t *testing.T) {
	t.Parallel()
	rec := &recordingServer{}
	srv := httptest.NewServer(rec.handler(nil))
	t.Cleanup(srv.Close)

	d, err := NewDispatcher(Config{Subscriptions: []Subscription{
		{Name: "seed-a", Events: []string{EventPostDeploy}, URL: srv.URL + "/seed-a", Blocking: true,
			FailurePolicy: FailurePolicyIgnore, AppliesTo: Scope{TemplateIDs: []string{"tpl-a"}}},
	}}, srv.Client())
	require.NoError(t, err)
	d.WithTemplates(staticTemplates(t, nil, errors.New("db down")))

	ref := d.ResolveInstance(context.Background(), &InstanceRef{ID: "i1", StackDefinitionID: "def-a"})
	assert.True(t, d.HasBlockingFor(EventPostDeploy, ref), "the phase runs, so the skip is logged")
	ignored, err := d.FireBlocking(context.Background(), EventPostDeploy, EventEnvelope{InstanceRef: ref}, BlockingCallbacks{})
	require.NoError(t, err)
	assert.Empty(t, ignored)
	assert.Empty(t, rec.Calls())
}

func TestDispatcher_NoTemplateResolver(t *testing.T) {
	t.Parallel()
	rec := &recordingServer{}
	srv := httptest.NewServer(rec.handler(nil))
	t.Cleanup(srv.Close)

	d, err := NewDispatcher(Config{Subscriptions: []Subscription{
		{Name: "gate", Events: []string{EventPreDeploy}, URL: srv.URL + "/gate", FailurePolicy: FailurePolicyFail,
			AppliesTo: Scope{TemplateIDs: []string{"tpl-a"}}},
	}}, srv.Client())
	require.NoError(t, err)

	err = d.Fire(context.Background(), EventPreDeploy, EventEnvelope{InstanceRef: &InstanceRef{ID: "i1", StackDefinitionID: "def-a"}})
	assert.ErrorIs(t, err, ErrScopeUnknown, "without a resolver the scope is never guessed")
	assert.Empty(t, rec.Calls())
}

func TestActionRegistry_AppliesTo(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	newRegistry := func(lookupErr error) *ActionRegistry {
		reg, err := NewActionRegistry([]ActionSubscription{
			{Name: "refresh-db-a", URL: srv.URL, AppliesTo: Scope{TemplateCategories: []string{"team-a"}}},
			{Name: "refresh-db-b", URL: srv.URL, AppliesTo: Scope{TemplateCategories: []string{"team-b"}}},
			{Name: "restart", URL: srv.URL},
			{Name: "warm-c1", URL: srv.URL, AppliesTo: Scope{ClusterIDs: []string{"c1"}}},
		}, srv.Client())
		require.NoError(t, err)
		return reg.WithTemplates(staticTemplates(t, teamTemplates, lookupErr))
	}
	names := func(actions []ActionSubscription) []string {
		out := make([]string, 0, len(actions))
		for _, a := range actions {
			out = append(out, a.Name)
		}
		return out
	}
	ctx := context.Background()
	teamA := &InstanceRef{ID: "i1", StackDefinitionID: "def-a", ClusterID: "c1"}

	t.Run("list", func(t *testing.T) {
		t.Parallel()
		reg := newRegistry(nil)
		actions, incomplete := reg.ListFor(ctx, teamA)
		assert.Equal(t, []string{"refresh-db-a", "restart", "warm-c1"}, names(actions))
		assert.False(t, incomplete)

		actions, incomplete = reg.ListFor(ctx, &InstanceRef{ID: "i2", StackDefinitionID: "def-b", ClusterID: "c2"})
		assert.Equal(t, []string{"refresh-db-b", "restart"}, names(actions))
		assert.False(t, incomplete)
	})

	t.Run("list with a failed lookup", func(t *testing.T) {
		t.Parallel()
		actions, incomplete := newRegistry(errors.New("db down")).ListFor(ctx, teamA)
		assert.Equal(t, []string{"restart", "warm-c1"}, names(actions), "scopes without a template still decide")
		assert.True(t, incomplete)
	})

	t.Run("lookup and invoke", func(t *testing.T) {
		t.Parallel()
		reg := newRegistry(nil)
		sub, ref, err := reg.LookupFor(ctx, "refresh-db-a", teamA)
		require.NoError(t, err)
		assert.Equal(t, "refresh-db-a", sub.Name)
		assert.Equal(t, "tpl-a", ref.TemplateID)

		_, _, err = reg.LookupFor(ctx, "refresh-db-b", teamA)
		assert.Equal(t, ErrUnknownAction{Name: "refresh-db-b"}, err)
		_, _, err = reg.LookupFor(ctx, "nope", teamA)
		assert.Equal(t, ErrUnknownAction{Name: "nope"}, err)

		before := calls.Load()
		_, err = reg.Invoke(ctx, "refresh-db-b", teamA, nil)
		assert.Equal(t, ErrUnknownAction{Name: "refresh-db-b"}, err)
		assert.Equal(t, before, calls.Load(), "an out-of-scope action is not called")
	})

	t.Run("invoke with a failed lookup", func(t *testing.T) {
		t.Parallel()
		reg := newRegistry(errors.New("db down"))
		_, _, err := reg.LookupFor(ctx, "refresh-db-a", teamA)
		assert.ErrorIs(t, err, ErrScopeUnknown)
		_, err = reg.Invoke(ctx, "refresh-db-a", teamA, nil)
		assert.ErrorIs(t, err, ErrScopeUnknown)
		_, _, err = reg.LookupFor(ctx, "warm-c1", teamA)
		assert.NoError(t, err, "a scope without a template needs no lookup")
	})
}

func TestActionRegistry_RequestTemplateFields(t *testing.T) {
	t.Parallel()
	got := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		got <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	reg, err := NewActionRegistry([]ActionSubscription{{Name: "restart", URL: srv.URL}}, srv.Client())
	require.NoError(t, err)
	reg.WithTemplates(staticTemplates(t, teamTemplates, nil))

	_, err = reg.Invoke(context.Background(), "restart", &InstanceRef{ID: "i1", StackDefinitionID: "def-b"}, nil)
	require.NoError(t, err)
	body := <-got
	inst, _ := body["instance"].(map[string]any)
	assert.Equal(t, "tpl-b", inst["template_id"])
	assert.Equal(t, "Team B stack", inst["template_name"])
	assert.Equal(t, "team-b", inst["template_category"])
}

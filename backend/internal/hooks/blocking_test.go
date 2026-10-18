package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingServer answers each subscriber path with its handler and records
// the order of the calls and the received envelopes.
type recordingServer struct {
	mu        sync.Mutex
	calls     []string
	envelopes []EventEnvelope
}

func (r *recordingServer) handler(handlers map[string]http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var env EventEnvelope
		_ = json.Unmarshal(body, &env)
		r.mu.Lock()
		r.calls = append(r.calls, req.URL.Path)
		r.envelopes = append(r.envelopes, env)
		r.mu.Unlock()
		if h, ok := handlers[req.URL.Path]; ok {
			h(w, req)
			return
		}
		_ = json.NewEncoder(w).Encode(HookResponse{Allowed: true})
	}
}

func (r *recordingServer) Calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func allow(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(HookResponse{Allowed: true})
}

func deny(msg string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(HookResponse{Allowed: false, Message: msg})
	}
}

func streaming(lines ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		for _, l := range lines {
			_, _ = fmt.Fprintln(w, "LOG: "+l)
		}
		_ = json.NewEncoder(w).Encode(HookResponse{Allowed: true})
	}
}

func TestConfigValidate_Blocking(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		events    []string
		blocking  bool
		expectErr string
	}{
		{name: "blocking post-deploy is valid", events: []string{EventPostDeploy}, blocking: true},
		{name: "blocking with post-deploy and other events is valid", events: []string{EventPostDeploy, EventDeployFinalized}, blocking: true},
		{name: "blocking without post-deploy is rejected", events: []string{EventPreDeploy}, blocking: true, expectErr: "blocking requires the post-deploy event"},
		{name: "non-blocking pre-deploy is valid", events: []string{EventPreDeploy}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := Config{Subscriptions: []Subscription{{
				Name: "s", Events: tt.events, URL: "https://example.com/h", Blocking: tt.blocking,
			}}}
			err := cfg.Validate()
			if tt.expectErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestLoadConfigFile_Blocking(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
  "subscriptions": [
    {"name":"restore","events":["post-deploy"],"url":"https://example.com/h","blocking":true,"timeout_seconds":900},
    {"name":"notify","events":["post-deploy"],"url":"https://example.com/n"}
  ]
}`), 0o600))
	cfg, _, err := LoadConfigFile(path)
	require.NoError(t, err)
	require.Len(t, cfg.Subscriptions, 2)
	assert.True(t, cfg.Subscriptions[0].Blocking)
	assert.False(t, cfg.Subscriptions[1].Blocking)
}

func TestDispatcher_BlockingSubscriptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		handlers      map[string]http.HandlerFunc
		subs          []Subscription
		fireBlocking  bool
		expectCalls   []string
		expectLines   []string
		expectStarted []string
		expectErrHook string
		expectIgnored []string
	}{
		{
			name: "Fire skips the blocking subscriber of post-deploy",
			subs: []Subscription{
				{Name: "restore", Events: []string{EventPostDeploy}, Blocking: true},
				{Name: "notify", Events: []string{EventPostDeploy}},
			},
			expectCalls: []string{"/notify"},
		},
		{
			name: "FireBlocking calls only blocking subscribers and streams progress",
			handlers: map[string]http.HandlerFunc{
				"/restore": streaming("restoring snapshot", "warming cache"),
			},
			subs: []Subscription{
				{Name: "restore", Events: []string{EventPostDeploy}, Blocking: true},
				{Name: "notify", Events: []string{EventPostDeploy}},
			},
			fireBlocking:  true,
			expectCalls:   []string{"/restore"},
			expectLines:   []string{"restoring snapshot", "warming cache"},
			expectStarted: []string{"restore"},
		},
		{
			name:     "ignore failure is collected and the next subscriber runs",
			handlers: map[string]http.HandlerFunc{"/first": deny("restore failed"), "/second": allow},
			subs: []Subscription{
				{Name: "first", Events: []string{EventPostDeploy}, Blocking: true, FailurePolicy: FailurePolicyIgnore},
				{Name: "second", Events: []string{EventPostDeploy}, Blocking: true, FailurePolicy: FailurePolicyIgnore},
			},
			fireBlocking:  true,
			expectCalls:   []string{"/first", "/second"},
			expectStarted: []string{"first", "second"},
			expectIgnored: []string{"first"},
		},
		{
			name:     "fail failure stops later subscribers",
			handlers: map[string]http.HandlerFunc{"/first": deny("no data"), "/second": allow},
			subs: []Subscription{
				{Name: "first", Events: []string{EventPostDeploy}, Blocking: true, FailurePolicy: FailurePolicyFail},
				{Name: "second", Events: []string{EventPostDeploy}, Blocking: true, FailurePolicy: FailurePolicyFail},
			},
			fireBlocking:  true,
			expectCalls:   []string{"/first"},
			expectStarted: []string{"first"},
			expectErrHook: "first",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := &recordingServer{}
			srv := httptest.NewServer(rec.handler(tt.handlers))
			defer srv.Close()
			subs := make([]Subscription, len(tt.subs))
			for i, s := range tt.subs {
				s.URL = srv.URL + "/" + s.Name
				subs[i] = s
			}
			d, err := NewDispatcher(Config{Subscriptions: subs}, srv.Client())
			require.NoError(t, err)

			if !tt.fireBlocking {
				require.NoError(t, d.Fire(context.Background(), EventPostDeploy, EventEnvelope{}))
				assert.Equal(t, tt.expectCalls, rec.Calls())
				return
			}

			var mu sync.Mutex
			var lines, started []string
			ignored, err := d.FireBlocking(context.Background(), EventPostDeploy, EventEnvelope{}, BlockingCallbacks{
				OnStart:    func(h string) { mu.Lock(); started = append(started, h); mu.Unlock() },
				OnProgress: func(l string) { mu.Lock(); lines = append(lines, l); mu.Unlock() },
			})
			assert.Equal(t, tt.expectCalls, rec.Calls())
			assert.Equal(t, tt.expectStarted, started)
			if tt.expectLines != nil {
				assert.Equal(t, tt.expectLines, lines)
			}
			if tt.expectErrHook != "" {
				var failed *FailedError
				require.True(t, errors.As(err, &failed))
				assert.Equal(t, tt.expectErrHook, failed.Hook)
			} else {
				require.NoError(t, err)
			}
			var names []string
			for _, f := range ignored {
				names = append(names, f.Hook)
				assert.Contains(t, f.UserMessage(EventPostDeploy, "deployment"), "post-deploy hook \""+f.Hook+"\"")
			}
			assert.Equal(t, tt.expectIgnored, names)
		})
	}
}

func TestDispatcher_BlockingTimeoutAndHasBlocking(t *testing.T) {
	t.Parallel()

	d, err := NewDispatcher(Config{Subscriptions: []Subscription{
		{Name: "a", Events: []string{EventPostDeploy}, URL: "https://a.example/h", Blocking: true, TimeoutSeconds: 600},
		{Name: "b", Events: []string{EventPostDeploy}, URL: "https://b.example/h", Blocking: true, TimeoutSeconds: 60},
		{Name: "c", Events: []string{EventPostDeploy}, URL: "https://c.example/h", TimeoutSeconds: 30},
	}}, nil)
	require.NoError(t, err)
	assert.True(t, d.HasBlocking(EventPostDeploy))
	assert.False(t, d.HasBlocking(EventDeployFinalized))
	assert.Equal(t, 11*time.Minute, d.BlockingTimeout(EventPostDeploy))
	assert.Equal(t, 11*time.Minute+30*time.Second, d.TotalTimeout(EventPostDeploy))
	assert.Zero(t, d.TotalTimeout(EventPreInstanceDelete))

	var nilDispatcher *Dispatcher
	assert.False(t, nilDispatcher.HasBlocking(EventPostDeploy))
	assert.Zero(t, nilDispatcher.BlockingTimeout(EventPostDeploy))
	assert.Zero(t, nilDispatcher.TotalTimeout(EventPostDeploy))
}

func TestDispatcher_BlockingTimeoutFails(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer srv.Close()
	d, err := NewDispatcher(Config{Subscriptions: []Subscription{
		{Name: "slow", Events: []string{EventPostDeploy}, URL: srv.URL, Blocking: true, FailurePolicy: FailurePolicyFail, TimeoutSeconds: 1},
	}}, srv.Client())
	require.NoError(t, err)

	_, err = d.FireBlocking(context.Background(), EventPostDeploy, EventEnvelope{}, BlockingCallbacks{})
	require.Error(t, err)
	msg := UserMessage(err, EventPostDeploy, "deployment")
	assert.Equal(t, `post-deploy hook "slow" failed (unreachable or timed out)`, msg)
	assert.NotContains(t, msg, srv.URL)
}

func TestTriggerContextAndEnvelopeJSON(t *testing.T) {
	t.Parallel()

	_, ok := TriggerFromContext(context.Background())
	assert.False(t, ok)

	ctx := WithTrigger(context.Background(), Trigger{Type: TriggerCleanupPolicy, ID: "p1", Name: "nightly"})
	got, ok := TriggerFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, Trigger{Type: TriggerCleanupPolicy, ID: "p1", Name: "nightly"}, got)

	env := EventEnvelope{
		Trigger: &got,
		CleanupPolicy: &CleanupPolicyRun{
			ID: "p1", Name: "nightly", Action: "stop", ClusterID: "all", DryRun: true, Run: "manual",
			Matched: 1, Instances: []CleanupPolicyInstance{{ID: "i1", Name: "demo", Namespace: "stack-demo", OwnerID: "u1", Result: CleanupResultDryRun}},
		},
	}
	raw, err := json.Marshal(env)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	assert.Equal(t, map[string]any{"type": "cleanup-policy", "id": "p1", "name": "nightly"}, decoded["trigger"])
	cp := decoded["cleanup_policy"].(map[string]any)
	assert.Equal(t, true, cp["dry_run"])
	assert.Equal(t, "manual", cp["run"])
	inst := cp["instances"].([]any)[0].(map[string]any)
	assert.Equal(t, "dry_run", inst["result"])
	assert.NotContains(t, inst, "error")

	// An envelope without trigger and run summary has neither key.
	raw, err = json.Marshal(EventEnvelope{})
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "trigger")
	assert.NotContains(t, string(raw), "cleanup_policy")
}

package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingLookup is a ClusterNameLookup that records each call.
type countingLookup struct {
	mu    sync.Mutex
	calls [][]string
	names map[string]string
	err   error
}

func (l *countingLookup) lookup(ids []string) (map[string]string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, append([]string(nil), ids...))
	if l.err != nil {
		return nil, l.err
	}
	out := map[string]string{}
	for _, id := range ids {
		if n, ok := l.names[id]; ok {
			out[id] = n
		}
	}
	return out, nil
}

func (l *countingLookup) callCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.calls)
}

// captureServer returns a subscriber that stores the last envelope body.
func captureServer(t *testing.T) (*httptest.Server, func() map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var last map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		last = body
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(HookResponse{Allowed: true})
	}))
	t.Cleanup(srv.Close)
	return srv, func() map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return last
	}
}

func TestDispatcher_ClusterNameInEnvelope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		envelope        EventEnvelope
		lookupErr       error
		noLookup        bool
		wantInstance    string // expected instance.cluster_name ("" = absent)
		wantPolicy      string // expected cleanup_policy.cluster_name ("" = absent)
		wantLookupCalls int
	}{
		{
			name:            "instance cluster name is set",
			envelope:        EventEnvelope{InstanceRef: &InstanceRef{ID: "i1", ClusterID: "c1"}},
			wantInstance:    "Production",
			wantLookupCalls: 1,
		},
		{
			name:            "unknown cluster omits the name",
			envelope:        EventEnvelope{InstanceRef: &InstanceRef{ID: "i1", ClusterID: "gone"}},
			wantLookupCalls: 1,
		},
		{
			name:            "empty cluster id needs no lookup",
			envelope:        EventEnvelope{InstanceRef: &InstanceRef{ID: "i1"}},
			wantLookupCalls: 0,
		},
		{
			name:            "lookup error omits the name and the dispatch continues",
			envelope:        EventEnvelope{InstanceRef: &InstanceRef{ID: "i1", ClusterID: "c1"}},
			lookupErr:       errors.New("db down"),
			wantLookupCalls: 1,
		},
		{
			name:     "without lookup the name is omitted",
			envelope: EventEnvelope{InstanceRef: &InstanceRef{ID: "i1", ClusterID: "c1"}},
			noLookup: true,
		},
		{
			name:            "cleanup policy cluster name is set",
			envelope:        EventEnvelope{CleanupPolicy: &CleanupPolicyRun{ID: "p1", ClusterID: "c2"}},
			wantPolicy:      "Staging",
			wantLookupCalls: 1,
		},
		{
			name:            "cleanup policy for all clusters has no name",
			envelope:        EventEnvelope{CleanupPolicy: &CleanupPolicyRun{ID: "p1", ClusterID: "all"}},
			wantLookupCalls: 0,
		},
		{
			name:            "a name set by the caller is kept",
			envelope:        EventEnvelope{InstanceRef: &InstanceRef{ID: "i1", ClusterID: "c1", ClusterName: "given"}},
			wantInstance:    "given",
			wantLookupCalls: 0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv, last := captureServer(t)
			d, err := NewDispatcher(Config{Subscriptions: []Subscription{{
				Name:   "capture",
				Events: []string{EventPostDeploy},
				URL:    srv.URL,
			}}}, srv.Client())
			require.NoError(t, err)
			lookup := &countingLookup{
				names: map[string]string{"c1": "Production", "c2": "Staging"},
				err:   tt.lookupErr,
			}
			if !tt.noLookup {
				resolver := NewClusterNameResolver(lookup.lookup)
				t.Cleanup(resolver.Stop)
				d.WithClusterNames(resolver)
			}

			var origInstance InstanceRef
			if tt.envelope.InstanceRef != nil {
				origInstance = *tt.envelope.InstanceRef
			}
			require.NoError(t, d.Fire(context.Background(), EventPostDeploy, tt.envelope))

			body := last()
			require.NotNil(t, body)
			if inst, ok := body["instance"].(map[string]any); ok {
				if tt.wantInstance == "" {
					assert.NotContains(t, inst, "cluster_name")
				} else {
					assert.Equal(t, tt.wantInstance, inst["cluster_name"])
				}
				// The caller's InstanceRef is not changed.
				assert.Equal(t, origInstance, *tt.envelope.InstanceRef)
			}
			if run, ok := body["cleanup_policy"].(map[string]any); ok {
				if tt.wantPolicy == "" {
					assert.NotContains(t, run, "cluster_name")
				} else {
					assert.Equal(t, tt.wantPolicy, run["cluster_name"])
				}
			}
			assert.Equal(t, tt.wantLookupCalls, lookup.callCount())
		})
	}
}

// TestDispatcher_ClusterNameCached checks that the dispatcher looks up a
// cluster once for many events (also an unknown one), and again after a
// failed lookup.
func TestDispatcher_ClusterNameCached(t *testing.T) {
	t.Parallel()

	srv, last := captureServer(t)
	d, err := NewDispatcher(Config{Subscriptions: []Subscription{{
		Name:   "capture",
		Events: []string{EventPreDeploy, EventPostDeploy},
		URL:    srv.URL,
	}}}, srv.Client())
	require.NoError(t, err)
	lookup := &countingLookup{names: map[string]string{"c1": "Production"}}
	resolver := NewClusterNameResolver(lookup.lookup)
	t.Cleanup(resolver.Stop)
	d.WithClusterNames(resolver)

	for i := 0; i < 3; i++ {
		require.NoError(t, d.Fire(context.Background(), EventPreDeploy, EventEnvelope{InstanceRef: &InstanceRef{ID: "i1", ClusterID: "c1"}}))
		require.NoError(t, d.Fire(context.Background(), EventPostDeploy, EventEnvelope{InstanceRef: &InstanceRef{ID: "i2", ClusterID: "gone"}}))
	}
	assert.Equal(t, 2, lookup.callCount(), "one lookup per cluster, then the cache")
	inst, _ := last()["instance"].(map[string]any)
	assert.NotContains(t, inst, "cluster_name")

	// A failed lookup is not cached.
	failing := &countingLookup{err: errors.New("db down")}
	d2, err := NewDispatcher(Config{Subscriptions: []Subscription{{
		Name: "capture", Events: []string{EventPreDeploy}, URL: srv.URL,
	}}}, srv.Client())
	require.NoError(t, err)
	failingResolver := NewClusterNameResolver(failing.lookup)
	t.Cleanup(failingResolver.Stop)
	d2.WithClusterNames(failingResolver)
	for i := 0; i < 2; i++ {
		require.NoError(t, d2.Fire(context.Background(), EventPreDeploy, EventEnvelope{InstanceRef: &InstanceRef{ID: "i1", ClusterID: "c1"}}))
	}
	assert.Equal(t, 2, failing.callCount())
}

func TestClusterNameResolver_StopIsSafe(t *testing.T) {
	t.Parallel()
	var nilResolver *ClusterNameResolver
	nilResolver.Stop()
	assert.Nil(t, NewClusterNameResolver(nil))

	r := NewClusterNameResolver(func([]string) (map[string]string, error) {
		return map[string]string{"c1": "Production"}, nil
	})
	r.Stop()
	r.Stop()
	// Still usable after Stop.
	assert.Equal(t, "Production", r.names(context.Background(), []string{"c1"})["c1"])
}

// TestDispatcher_ClusterNameLookupTimeout checks that a slow lookup does not
// delay the dispatch beyond the timeout: the envelope goes out without
// cluster_name, and the late answer fills the cache for the next event.
func TestDispatcher_ClusterNameLookupTimeout(t *testing.T) {
	t.Parallel()

	srv, last := captureServer(t)
	d, err := NewDispatcher(Config{Subscriptions: []Subscription{{
		Name: "gate", Events: []string{EventPreDeploy}, URL: srv.URL, FailurePolicy: FailurePolicyFail,
	}}}, srv.Client())
	require.NoError(t, err)

	release := make(chan struct{})
	answered := make(chan struct{})
	resolver := NewClusterNameResolver(func([]string) (map[string]string, error) {
		<-release
		defer close(answered)
		return map[string]string{"c1": "Production"}, nil
	})
	resolver.timeout = 50 * time.Millisecond
	t.Cleanup(resolver.Stop)
	d.WithClusterNames(resolver)

	start := time.Now()
	require.NoError(t, d.Fire(context.Background(), EventPreDeploy, EventEnvelope{InstanceRef: &InstanceRef{ID: "i1", ClusterID: "c1"}}))
	assert.Less(t, time.Since(start), 2*time.Second)
	inst, _ := last()["instance"].(map[string]any)
	assert.NotContains(t, inst, "cluster_name")

	close(release)
	<-answered
	require.Eventually(t, func() bool {
		_, ok := resolver.cache.Get("c1")
		return ok
	}, time.Second, 10*time.Millisecond)
	require.NoError(t, d.Fire(context.Background(), EventPreDeploy, EventEnvelope{InstanceRef: &InstanceRef{ID: "i1", ClusterID: "c1"}}))
	inst, _ = last()["instance"].(map[string]any)
	assert.Equal(t, "Production", inst["cluster_name"])
}

// TestDispatcher_ClusterNameCanceledContext checks that an ended context
// stops the wait for the lookup.
func TestDispatcher_ClusterNameCanceledContext(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	resolver := NewClusterNameResolver(func([]string) (map[string]string, error) {
		<-release
		return nil, nil
	})
	t.Cleanup(resolver.Stop)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	names := resolver.names(ctx, []string{"c1"})
	assert.Equal(t, "", names["c1"])
}

// TestActionRegistry_ClusterNameInRequest checks that an action request has
// instance.cluster_name, as a hook envelope has.
func TestActionRegistry_ClusterNameInRequest(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		got = body
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	reg, err := NewActionRegistry([]ActionSubscription{{Name: "refresh-db", URL: srv.URL}}, srv.Client())
	require.NoError(t, err)
	resolver := NewClusterNameResolver(func([]string) (map[string]string, error) {
		return map[string]string{"c1": "Production"}, nil
	})
	t.Cleanup(resolver.Stop)
	reg.WithClusterNames(resolver)

	ref := &InstanceRef{ID: "i1", ClusterID: "c1"}
	_, err = reg.Invoke(context.Background(), "refresh-db", ref, nil)
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	inst, _ := got["instance"].(map[string]any)
	assert.Equal(t, "Production", inst["cluster_name"])
	assert.Empty(t, ref.ClusterName, "the caller's InstanceRef is not changed")
}

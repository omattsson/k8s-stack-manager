package hooks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDispatcher_Fire(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		serverHandler  func(t *testing.T, hits *int32) http.HandlerFunc
		failurePolicy  FailurePolicy
		secret         string
		event          string
		expectErr      bool
		expectErrSub   string
		expectHits     int32
		expectSigCheck bool
	}{
		{
			name: "success — allowed response",
			serverHandler: func(_ *testing.T, hits *int32) http.HandlerFunc {
				return func(w http.ResponseWriter, _ *http.Request) {
					atomic.AddInt32(hits, 1)
					_ = json.NewEncoder(w).Encode(HookResponse{Allowed: true})
				}
			},
			failurePolicy: FailurePolicyFail,
			event:         EventPreDeploy,
			expectHits:    1,
		},
		{
			name: "empty 200 body treated as allowed",
			serverHandler: func(_ *testing.T, hits *int32) http.HandlerFunc {
				return func(w http.ResponseWriter, _ *http.Request) {
					atomic.AddInt32(hits, 1)
					w.WriteHeader(http.StatusOK)
				}
			},
			failurePolicy: FailurePolicyFail,
			event:         EventPreDeploy,
			expectHits:    1,
		},
		{
			name: "denied response with failure_policy=fail aborts",
			serverHandler: func(_ *testing.T, hits *int32) http.HandlerFunc {
				return func(w http.ResponseWriter, _ *http.Request) {
					atomic.AddInt32(hits, 1)
					_ = json.NewEncoder(w).Encode(HookResponse{Allowed: false, Message: "nope"})
				}
			},
			failurePolicy: FailurePolicyFail,
			event:         EventPreDeploy,
			expectErr:     true,
			expectErrSub:  "nope",
			expectHits:    1,
		},
		{
			name: "denied response with failure_policy=ignore continues",
			serverHandler: func(_ *testing.T, hits *int32) http.HandlerFunc {
				return func(w http.ResponseWriter, _ *http.Request) {
					atomic.AddInt32(hits, 1)
					_ = json.NewEncoder(w).Encode(HookResponse{Allowed: false, Message: "nope"})
				}
			},
			failurePolicy: FailurePolicyIgnore,
			event:         EventPostDeploy,
			expectHits:    1,
		},
		{
			name: "5xx with failure_policy=fail aborts",
			serverHandler: func(_ *testing.T, hits *int32) http.HandlerFunc {
				return func(w http.ResponseWriter, _ *http.Request) {
					atomic.AddInt32(hits, 1)
					http.Error(w, "boom", http.StatusInternalServerError)
				}
			},
			failurePolicy: FailurePolicyFail,
			event:         EventPreDeploy,
			expectErr:     true,
			expectErrSub:  "status 500",
			expectHits:    1,
		},
		{
			name: "5xx with failure_policy=ignore continues",
			serverHandler: func(_ *testing.T, hits *int32) http.HandlerFunc {
				return func(w http.ResponseWriter, _ *http.Request) {
					atomic.AddInt32(hits, 1)
					http.Error(w, "boom", http.StatusInternalServerError)
				}
			},
			failurePolicy: FailurePolicyIgnore,
			event:         EventPostDeploy,
			expectHits:    1,
		},
		{
			name: "HMAC signature header is set when secret configured",
			serverHandler: func(t *testing.T, hits *int32) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					atomic.AddInt32(hits, 1)
					body, err := io.ReadAll(r.Body)
					require.NoError(t, err)
					sig := r.Header.Get(headerSignature)
					assert.Equal(t, sign(body, "topsecret"), sig)
					assert.Equal(t, EventPreDeploy, r.Header.Get(headerEvent))
					assert.NotEmpty(t, r.Header.Get(headerRequestID))
					_ = json.NewEncoder(w).Encode(HookResponse{Allowed: true})
				}
			},
			failurePolicy:  FailurePolicyFail,
			secret:         "topsecret",
			event:          EventPreDeploy,
			expectHits:     1,
			expectSigCheck: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var hits int32
			srv := httptest.NewServer(tt.serverHandler(t, &hits))
			defer srv.Close()

			cfg := Config{
				Subscriptions: []Subscription{{
					Name:           "test",
					Events:         []string{tt.event},
					URL:            srv.URL,
					FailurePolicy:  tt.failurePolicy,
					Secret:         tt.secret,
					TimeoutSeconds: 5,
				}},
			}
			d, err := NewDispatcher(cfg, srv.Client())
			require.NoError(t, err)

			err = d.Fire(context.Background(), tt.event, EventEnvelope{
				InstanceRef: &InstanceRef{ID: "instance-1", Name: "demo"},
			})

			if tt.expectErr {
				require.Error(t, err)
				if tt.expectErrSub != "" {
					assert.Contains(t, err.Error(), tt.expectErrSub)
				}
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.expectHits, atomic.LoadInt32(&hits))
		})
	}
}

func TestDispatcher_NoSubscriptionsForEvent(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be called for unsubscribed event")
	}))
	defer srv.Close()

	cfg := Config{Subscriptions: []Subscription{{
		Name:   "only-pre-deploy",
		Events: []string{EventPreDeploy},
		URL:    srv.URL,
	}}}
	d, err := NewDispatcher(cfg, srv.Client())
	require.NoError(t, err)

	err = d.Fire(context.Background(), EventPostDeploy, EventEnvelope{})
	require.NoError(t, err)
}

func TestDispatcher_MultipleSubscriptionsInRegistrationOrder(t *testing.T) {
	t.Parallel()

	var calls []string
	mu := make(chan struct{}, 1)
	mu <- struct{}{}
	record := func(name string) {
		<-mu
		calls = append(calls, name)
		mu <- struct{}{}
	}

	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		record("a")
		_ = json.NewEncoder(w).Encode(HookResponse{Allowed: true})
	}))
	defer srvA.Close()
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		record("b")
		_ = json.NewEncoder(w).Encode(HookResponse{Allowed: true})
	}))
	defer srvB.Close()

	cfg := Config{Subscriptions: []Subscription{
		{Name: "a", Events: []string{EventPreDeploy}, URL: srvA.URL},
		{Name: "b", Events: []string{EventPreDeploy}, URL: srvB.URL},
	}}
	d, err := NewDispatcher(cfg, http.DefaultClient)
	require.NoError(t, err)

	require.NoError(t, d.Fire(context.Background(), EventPreDeploy, EventEnvelope{}))
	assert.Equal(t, []string{"a", "b"}, calls)
}

func TestDispatcher_FailFastStopsLaterSubscribers(t *testing.T) {
	t.Parallel()

	bCalled := false
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srvA.Close()
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		bCalled = true
		_ = json.NewEncoder(w).Encode(HookResponse{Allowed: true})
	}))
	defer srvB.Close()

	cfg := Config{Subscriptions: []Subscription{
		{Name: "a", Events: []string{EventPreDeploy}, URL: srvA.URL, FailurePolicy: FailurePolicyFail},
		{Name: "b", Events: []string{EventPreDeploy}, URL: srvB.URL, FailurePolicy: FailurePolicyFail},
	}}
	d, err := NewDispatcher(cfg, http.DefaultClient)
	require.NoError(t, err)

	err = d.Fire(context.Background(), EventPreDeploy, EventEnvelope{})
	require.Error(t, err)
	assert.False(t, bCalled, "subscriber b must not be called after a aborts")
}

func TestDispatcher_TimeoutEnforced(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(HookResponse{Allowed: true})
	}))
	defer srv.Close()

	cfg := Config{Subscriptions: []Subscription{{
		Name:           "slow",
		Events:         []string{EventPreDeploy},
		URL:            srv.URL,
		FailurePolicy:  FailurePolicyFail,
		TimeoutSeconds: 1,
	}}}
	d, err := NewDispatcher(cfg, http.DefaultClient)
	require.NoError(t, err)

	d.now = func() time.Time { return time.Unix(0, 0) }

	start := time.Now()
	err = d.Fire(context.Background(), EventPreDeploy, EventEnvelope{})
	require.NoError(t, err, "200ms server response within 1s timeout should succeed")
	assert.Less(t, time.Since(start), 2*time.Second)
}

func TestDispatcher_EnvelopeFieldsPopulated(t *testing.T) {
	t.Parallel()

	var got EventEnvelope
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		_ = json.NewEncoder(w).Encode(HookResponse{Allowed: true})
	}))
	defer srv.Close()

	cfg := Config{Subscriptions: []Subscription{{
		Name:   "echo",
		Events: []string{EventPreDeploy},
		URL:    srv.URL,
	}}}
	d, err := NewDispatcher(cfg, srv.Client())
	require.NoError(t, err)

	require.NoError(t, d.Fire(context.Background(), EventPreDeploy, EventEnvelope{
		InstanceRef: &InstanceRef{ID: "i-1", Name: "demo", Namespace: "stack-demo-alice"},
	}))

	assert.Equal(t, envelopeAPIVersion, got.APIVersion)
	assert.Equal(t, "EventEnvelope", got.Kind)
	assert.Equal(t, EventPreDeploy, got.Event)
	assert.NotEmpty(t, got.RequestID)
	require.NotNil(t, got.InstanceRef)
	assert.Equal(t, "i-1", got.InstanceRef.ID)
}

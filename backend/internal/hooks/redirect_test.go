package hooks

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDispatcher_RedirectNotFollowed checks that the event dispatcher does
// not follow a 3xx answer on any dispatch path (Fire, FireWithProgress,
// FireBlocking). The redirect target never gets the signed body, and the
// 3xx answer is a subscriber error that respects failure_policy.
func TestDispatcher_RedirectNotFollowed(t *testing.T) {
	t.Parallel()

	type path int
	const (
		viaFire path = iota
		viaProgress
		viaBlocking
	)

	var tests []struct {
		name   string
		status int
		policy FailurePolicy
		via    path
		event  string
	}
	for _, status := range []int{http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		for _, policy := range []FailurePolicy{FailurePolicyFail, FailurePolicyIgnore} {
			for _, c := range []struct {
				via   path
				event string
				label string
			}{
				{viaFire, EventPreDeploy, "fire pre-deploy"},
				{viaFire, EventDeleteCompleted, "fire delete-completed"},
				{viaProgress, EventPreRollback, "progress pre-rollback"},
				{viaBlocking, EventPostDeploy, "blocking post-deploy"},
			} {
				tests = append(tests, struct {
					name   string
					status int
					policy FailurePolicy
					via    path
					event  string
				}{
					name:   fmt.Sprintf("%d %s %s", status, policy, c.label),
					status: status,
					policy: policy,
					via:    c.via,
					event:  c.event,
				})
			}
		}
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var targetHits int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				atomic.AddInt32(&targetHits, 1)
				allow(w, nil)
			}))
			defer target.Close()

			var subscriberHits int32
			subscriber := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				atomic.AddInt32(&subscriberHits, 1)
				w.Header().Set("Location", target.URL+"/collect?token=s3cret")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte("moved to " + target.URL))
			}))
			defer subscriber.Close()

			sub := Subscription{
				Name:          "gate",
				Events:        []string{tt.event},
				URL:           subscriber.URL + "/hook",
				Secret:        "signing-secret",
				FailurePolicy: tt.policy,
				Blocking:      tt.via == viaBlocking,
			}
			// http.DefaultClient follows redirects; the dispatcher must not.
			d, err := NewDispatcher(Config{Subscriptions: []Subscription{sub}}, http.DefaultClient)
			require.NoError(t, err)

			ctx := context.Background()
			var fireErr error
			var ignored []IgnoredFailure
			switch tt.via {
			case viaFire:
				fireErr = d.Fire(ctx, tt.event, EventEnvelope{})
			case viaProgress:
				fireErr = d.FireWithProgress(ctx, tt.event, EventEnvelope{}, func(string) {})
			case viaBlocking:
				ignored, fireErr = d.FireBlocking(ctx, tt.event, EventEnvelope{}, BlockingCallbacks{})
			}

			assert.Equal(t, int32(1), atomic.LoadInt32(&subscriberHits))
			assert.Zero(t, atomic.LoadInt32(&targetHits), "the redirect target must not get the signed body")

			var cause error
			if tt.policy == FailurePolicyFail {
				var failed *FailedError
				require.True(t, errors.As(fireErr, &failed), "fail policy aborts: %v", fireErr)
				cause = fireErr
			} else {
				require.NoError(t, fireErr)
				if tt.via == viaBlocking {
					require.Len(t, ignored, 1)
					cause = &FailedError{Hook: ignored[0].Hook, Err: ignored[0].Err}
				}
			}
			if cause == nil {
				return
			}
			var redirect *RedirectError
			require.True(t, errors.As(cause, &redirect))
			assert.Equal(t, tt.status, redirect.StatusCode)
			assert.NotContains(t, cause.Error(), target.URL, "the error must not contain the Location URL")

			msg := UserMessage(cause, tt.event, "deployment")
			assert.Equal(t, fmt.Sprintf("%s hook %q failed (the subscriber answered with a redirect)", tt.event, "gate"), msg)
			assert.False(t, strings.Contains(msg, "http"), "no URL in the user message")
		})
	}
}

func TestNoRedirectClient_DefaultClientUnchanged(t *testing.T) {
	t.Parallel()
	_, err := NewDispatcher(Config{Subscriptions: []Subscription{{
		Name: "a", Events: []string{EventPreDeploy}, URL: "http://example.invalid/hook",
	}}}, http.DefaultClient)
	require.NoError(t, err)
	assert.Nil(t, http.DefaultClient.CheckRedirect, "the dispatcher copies the client and leaves the global client unchanged")
}

func TestClassifyErr_Redirect(t *testing.T) {
	t.Parallel()
	assert.Equal(t, outcomeHTTPError, classifyErr(&RedirectError{StatusCode: http.StatusTemporaryRedirect}))
}

package channel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDispatch_RedirectNotFollowed checks that a 3xx answer of a channel is
// a failed delivery: the redirect target never gets the signed body, and the
// delivery log has the status code but no URL.
func TestDispatch_RedirectNotFollowed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		status     int
		dispatchTo bool
	}{
		{name: "302 dispatch", status: http.StatusFound},
		{name: "307 dispatch", status: http.StatusTemporaryRedirect},
		{name: "308 dispatch", status: http.StatusPermanentRedirect},
		{name: "307 test send", status: http.StatusTemporaryRedirect, dispatchTo: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var targetHits int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				atomic.AddInt32(&targetHits, 1)
				w.WriteHeader(http.StatusOK)
			}))
			defer target.Close()
			var channelHits int32
			webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				atomic.AddInt32(&channelHits, 1)
				w.Header().Set("Location", target.URL+"/collect?token=s3cret")
				w.WriteHeader(tt.status)
			}))
			defer webhook.Close()

			ch := models.NotificationChannel{ID: "ch-1", Name: "chat", WebhookURL: webhook.URL + "/hook?token=s3cret", Secret: "signing-secret", Enabled: true}
			repo := &mockChannelRepo{channels: []models.NotificationChannel{ch}}
			d := NewDispatcher(repo)

			var status, errMsg string
			var code int
			if tt.dispatchTo {
				status, code, errMsg = d.DispatchTo(context.Background(), ch, EventPayload{EventType: "deployment.success"})
			} else {
				d.Dispatch(context.Background(), EventPayload{EventType: "deployment.success"})
				require.Len(t, repo.deliveries, 1)
				status, code, errMsg = repo.deliveries[0].Status, repo.deliveries[0].StatusCode, repo.deliveries[0].ErrorMessage
			}

			assert.Equal(t, int32(1), atomic.LoadInt32(&channelHits), "a 3xx is not retried")
			assert.Zero(t, atomic.LoadInt32(&targetHits), "the redirect target must not get the signed body")
			assert.Equal(t, "failed", status)
			assert.Equal(t, tt.status, code)
			assert.Contains(t, errMsg, "redirect not followed")
			assert.NotContains(t, errMsg, "http")
			assert.NotContains(t, errMsg, "s3cret")
		})
	}
}

// TestDispatch_TransportErrorHidesURL checks that a connection error does
// not put the webhook URL (which can hold a token) in the delivery log.
func TestDispatch_TransportErrorHidesURL(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL + "/hook?token=s3cret"
	server.Close()

	ch := models.NotificationChannel{ID: "ch-1", Name: "chat", WebhookURL: url, Enabled: true}
	d := NewDispatcher(&mockChannelRepo{})
	// The connection error is retried once (2 s delay).
	status, _, errMsg := d.DispatchTo(context.Background(), ch, EventPayload{EventType: "deployment.success"})
	assert.Equal(t, "failed", status)
	assert.Contains(t, errMsg, "sending request")
	assert.NotContains(t, errMsg, "s3cret")
	assert.NotContains(t, errMsg, "/hook")
}

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

// A channel whose secret or URL cannot be decrypted sends nothing: not with
// the ciphertext as the signing key and not unsigned. It writes a delivery
// log with a fixed text.
func TestDispatch_UnreadableValuesSendNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		secret     bool
		url        bool
		wantErrMsg string
	}{
		{"secret", true, false, errSecretUnreadable.Error()},
		{"url", false, true, errURLUnreadable.Error()},
		{"secret and url", true, true, errURLUnreadable.Error()},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			ch := models.NotificationChannel{ID: "ch-1", Name: "broken", WebhookURL: server.URL, Enabled: true,
				SecretUnreadable: tt.secret, WebhookURLUnreadable: tt.url}
			if tt.url {
				ch.WebhookURL = ""
			}
			repo := &mockChannelRepo{channels: []models.NotificationChannel{ch}}
			d := newTestDispatcher(t, repo)
			d.retryDelay = 1

			d.Dispatch(context.Background(), EventPayload{EventType: "test.event"})
			logs := waitDeliveries(t, repo, 1)
			assert.Equal(t, "failed", logs[0].Status)
			assert.Equal(t, tt.wantErrMsg, logs[0].ErrorMessage)
			assert.Zero(t, logs[0].StatusCode)
			assert.NotContains(t, logs[0].ErrorMessage, server.URL)
			assert.Zero(t, requests.Load(), "no request, signed or unsigned")

			status, code, msg := d.DeliverOnce(context.Background(), ch, EventPayload{EventType: "test"})
			assert.Equal(t, "failed", status)
			assert.Zero(t, code)
			assert.Equal(t, tt.wantErrMsg, msg)

			status, _, msg = d.DispatchTo(context.Background(), ch, EventPayload{EventType: "test"})
			assert.Equal(t, "failed", status)
			assert.Equal(t, tt.wantErrMsg, msg)
			assert.Zero(t, requests.Load())
		})
	}

	t.Run("a readable channel still sends", func(t *testing.T) {
		t.Parallel()
		var requests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requests.Add(1)
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()
		repo := &mockChannelRepo{channels: []models.NotificationChannel{{ID: "ch-1", Name: "ok", WebhookURL: server.URL, Secret: "s", Enabled: true}}}
		d := newTestDispatcher(t, repo)
		d.Dispatch(context.Background(), EventPayload{EventType: "test.event"})
		waitDeliveries(t, repo, 1)
		require.EqualValues(t, 1, requests.Load())
	})
}

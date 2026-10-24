package channel

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUnreadableChannel checks that a channel with a secret or URL that the
// repository could not decrypt sends no request, whatever the path, and gives
// the fixed delivery message (#692).
func TestUnreadableChannel(t *testing.T) {
	t.Parallel()

	payload := EventPayload{EventType: "deployment.success", Timestamp: time.Now(), Title: "t", Message: "m"}

	tests := []struct {
		name    string
		mutate  func(*models.NotificationChannel)
		wantMsg string
	}{
		{"secret unreadable", func(c *models.NotificationChannel) { c.Secret, c.SecretUnreadable = "", true }, models.ChannelSecretUnreadableMessage},
		{"URL unreadable", func(c *models.NotificationChannel) { c.WebhookURL, c.WebhookURLUnreadable = "", true }, models.ChannelWebhookURLUnreadableMessage},
		{"both unreadable reports the URL", func(c *models.NotificationChannel) {
			c.Secret, c.SecretUnreadable, c.WebhookURL, c.WebhookURLUnreadable = "", true, "", true
		}, models.ChannelWebhookURLUnreadableMessage},
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

			ch := models.NotificationChannel{ID: "ch-1", Name: "c", WebhookURL: server.URL, Secret: "s", Enabled: true}
			tt.mutate(&ch)

			t.Run("worker path", func(t *testing.T) {
				repo := &mockChannelRepo{channels: []models.NotificationChannel{ch}}
				d := newTestDispatcher(t, repo)
				d.Dispatch(context.Background(), payload)
				got := waitDeliveries(t, repo, 1)
				assert.Equal(t, "failed", got[0].Status)
				assert.Zero(t, got[0].StatusCode)
				assert.Equal(t, tt.wantMsg, got[0].ErrorMessage)
			})

			t.Run("queued copy is used when the read fails", func(t *testing.T) {
				repo := &mockChannelRepo{channels: []models.NotificationChannel{ch}}
				d := newTestDispatcher(t, repo)
				repo.setGetErr(errors.New("db down"))
				d.Dispatch(context.Background(), payload)
				got := waitDeliveries(t, repo, 1)
				assert.Equal(t, "failed", got[0].Status)
				assert.Equal(t, tt.wantMsg, got[0].ErrorMessage)
			})

			t.Run("DeliverOnce", func(t *testing.T) {
				status, code, msg := NewDispatcher(&mockChannelRepo{}).DeliverOnce(context.Background(), ch, payload)
				assert.Equal(t, "failed", status)
				assert.Zero(t, code)
				assert.Equal(t, tt.wantMsg, msg)
			})

			t.Run("DispatchTo", func(t *testing.T) {
				status, code, msg := NewDispatcher(&mockChannelRepo{}).DispatchTo(context.Background(), ch, payload)
				assert.Equal(t, "failed", status)
				assert.Zero(t, code)
				assert.Equal(t, tt.wantMsg, msg)
			})

			assert.Zero(t, requests.Load(), "no request is sent")
			assert.NotContains(t, tt.wantMsg, server.URL)
		})
	}
}

func TestReadableChannelStillSigns(t *testing.T) {
	t.Parallel()
	var sig atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sig.Store(r.Header.Get("X-StackManager-Signature"))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	repo := &mockChannelRepo{channels: []models.NotificationChannel{
		{ID: "ch-1", Name: "c", WebhookURL: server.URL, Secret: "s3cret", Enabled: true},
	}}
	d := newTestDispatcher(t, repo)
	d.Dispatch(context.Background(), EventPayload{EventType: "deployment.success", Timestamp: time.Now(), Title: "t"})
	got := waitDeliveries(t, repo, 1)
	assert.Equal(t, "success", got[0].Status)
	s, _ := sig.Load().(string)
	require.NotEmpty(t, s)
	assert.Contains(t, s, signaturePrefix)
}

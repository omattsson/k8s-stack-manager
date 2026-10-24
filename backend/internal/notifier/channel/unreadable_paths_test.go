package channel

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUnreadableChannel_ReadVersusQueuedCopy checks which copy wins in the
// worker when the fresh read and the queued copy differ in readability (#692).
func TestUnreadableChannel_ReadVersusQueuedCopy(t *testing.T) {
	t.Parallel()
	payload := EventPayload{EventType: "deployment.success", Timestamp: time.Now(), Title: "t", Message: "m"}

	tests := []struct {
		name         string
		queuedBroken bool // the copy queued at Dispatch time is unreadable
		wantStatus   string
		wantRequests int32
	}{
		{"repaired after queueing: the current read is used and signs", true, "success", 1},
		{"broken after queueing: nothing is sent", false, "failed", 0},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var requests atomic.Int32
			var sig atomic.Value
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				sig.Store(r.Header.Get("X-StackManager-Signature"))
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			good := models.NotificationChannel{ID: "ch-1", Name: "c", WebhookURL: server.URL, Secret: "s3cret", Enabled: true}
			broken := good
			broken.Secret, broken.SecretUnreadable = "", true
			queued, stored := good, broken
			if tt.queuedBroken {
				queued, stored = broken, good
			}

			repo := &mockChannelRepo{channels: []models.NotificationChannel{stored}}
			d := newTestDispatcher(t, repo)
			body, err := encodeBody(bodyFormat(&queued), payload)
			require.NoError(t, err)
			d.submit(channelJob{channel: queued, eventType: payload.EventType, format: bodyFormat(&queued), body: body, payload: payload})

			got := waitDeliveries(t, repo, 1)
			assert.Equal(t, tt.wantStatus, got[0].Status)
			assert.Equal(t, tt.wantRequests, requests.Load())
			if tt.wantStatus == "success" {
				s, _ := sig.Load().(string)
				assert.Contains(t, s, signaturePrefix)
			} else {
				assert.Equal(t, models.ChannelSecretUnreadableMessage, got[0].ErrorMessage)
			}
		})
	}
}

// Not parallel: it replaces the default slog logger.
func TestUnreadableChannel_LogsHaveNoSecretOrURL(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })

	const url = "https://hooks.example.com/tok-12345"
	ch := models.NotificationChannel{ID: "ch-1", Name: "c", WebhookURL: url, Secret: "topsecret-xyz", Enabled: true, SecretUnreadable: true}
	repo := &mockChannelRepo{channels: []models.NotificationChannel{ch}}
	repo.setGetErr(errors.New("db down"))
	d := newTestDispatcher(t, repo)
	d.Dispatch(context.Background(), EventPayload{EventType: "deployment.success", Timestamp: time.Now(), Title: "t"})
	got := waitDeliveries(t, repo, 1)
	assert.Equal(t, "failed", got[0].Status)

	_, _, _ = d.DeliverOnce(context.Background(), ch, EventPayload{EventType: "x", Timestamp: time.Now()})
	_, _, _ = d.DispatchTo(context.Background(), ch, EventPayload{EventType: "x", Timestamp: time.Now()})

	assert.NotContains(t, buf.String(), "topsecret-xyz")
	assert.NotContains(t, buf.String(), "tok-12345")
	assert.NotContains(t, got[0].ErrorMessage, "tok-12345")
	require.NotEmpty(t, got[0].ErrorMessage)
}

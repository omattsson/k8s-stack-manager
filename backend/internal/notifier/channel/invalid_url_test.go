package channel

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
)

// invalidWebhookURLs are stored webhook URLs that do not give a request.
// Each one holds a token in its query.
var invalidWebhookURLs = []struct {
	name string
	url  string
}{
	{name: "invalid escape", url: "https://hooks.example.com/%zz?sig=TOKEN"},
	{name: "control character", url: "https://hooks.example.com/x\x7f?sig=TOKEN"},
	{name: "missing scheme", url: "://hooks.example.com/hook?sig=TOKEN"},
}

// assertNoWebhookURL checks that a text has no part of the webhook URL.
func assertNoWebhookURL(t *testing.T, text string) {
	t.Helper()
	assert.NotContains(t, text, "sig=TOKEN")
	assert.NotContains(t, text, "TOKEN")
	assert.NotContains(t, text, "hooks.example.com")
	assert.NotContains(t, text, "/hook")
}

// TestDelivery_InvalidURLHidesURL checks that a stored webhook URL that does
// not parse gives a fixed error text. The delivery log has no part of the URL.
// The send is not retried.
func TestDelivery_InvalidURLHidesURL(t *testing.T) {
	t.Parallel()

	type sendFunc func(t *testing.T, d *Dispatcher, repo *mockChannelRepo, ch models.NotificationChannel) (string, int, string)
	sends := []struct {
		name string
		send sendFunc
	}{
		{
			name: "dispatch",
			send: func(t *testing.T, d *Dispatcher, repo *mockChannelRepo, _ models.NotificationChannel) (string, int, string) {
				d.Dispatch(context.Background(), EventPayload{EventType: "deployment.success"})
				got := waitDeliveries(t, repo, 1)
				return got[0].Status, got[0].StatusCode, got[0].ErrorMessage
			},
		},
		{
			name: "dispatch to",
			send: func(_ *testing.T, d *Dispatcher, _ *mockChannelRepo, ch models.NotificationChannel) (string, int, string) {
				return d.DispatchTo(context.Background(), ch, EventPayload{EventType: "deployment.success"})
			},
		},
		{
			name: "deliver once",
			send: func(_ *testing.T, d *Dispatcher, _ *mockChannelRepo, ch models.NotificationChannel) (string, int, string) {
				return d.DeliverOnce(context.Background(), ch, EventPayload{EventType: models.NotificationChannelTestEvent})
			},
		},
	}

	type testCase struct {
		name string
		url  string
		send sendFunc
	}
	var tests []testCase
	for _, u := range invalidWebhookURLs {
		for _, s := range sends {
			tests = append(tests, testCase{name: s.name + " " + u.name, url: u.url, send: s.send})
		}
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ch := models.NotificationChannel{ID: "ch-1", Name: "chat", WebhookURL: tt.url, Secret: "signing-secret", Enabled: true}
			repo := &mockChannelRepo{channels: []models.NotificationChannel{ch}}
			d := newTestDispatcher(t, repo)
			// Far above any slow runner, so only a retry reaches it.
			d.retryDelay = time.Minute

			start := time.Now()
			status, code, errMsg := tt.send(t, d, repo, ch)

			assert.Less(t, time.Since(start), d.retryDelay, "an invalid URL is not retried")
			assert.Equal(t, "failed", status)
			assert.Zero(t, code)
			assert.Equal(t, "creating request: invalid webhook URL", errMsg)
			assertNoWebhookURL(t, errMsg)
		})
	}
}

// TestDelivery_InvalidURLLogLine checks that the log line of a failed
// delivery has no part of the webhook URL.
func TestDelivery_InvalidURLLogLine(t *testing.T) {
	// Not parallel: this test changes the global slog default logger.
	var buf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(orig)

	for _, u := range invalidWebhookURLs {
		ch := models.NotificationChannel{ID: "ch-1", Name: "chat", WebhookURL: u.url, Enabled: true}
		repo := &mockChannelRepo{channels: []models.NotificationChannel{ch}}
		d := newTestDispatcher(t, repo)
		d.Dispatch(context.Background(), EventPayload{EventType: "deployment.success"})
		waitDeliveries(t, repo, 1)
		// Stop waits for the worker, so its log lines are in buf.
		d.Stop()
	}

	out := buf.String()
	assert.Contains(t, out, "delivery failed")
	assert.Contains(t, out, "invalid webhook URL")
	assertNoWebhookURL(t, out)
}

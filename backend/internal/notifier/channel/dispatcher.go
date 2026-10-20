package channel

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"backend/internal/hooks"
	"backend/internal/models"
)

const (
	maxResponseBody = 4096
	requestTimeout  = 10 * time.Second
	retryDelay      = 2 * time.Second
	signaturePrefix = "sha256="
)

// errInvalidWebhookURL is the error for a webhook URL that does not give a
// request. The text has no URL, because the URL can hold a token.
var errInvalidWebhookURL = errors.New("creating request: invalid webhook URL")

// Dispatcher sends notification payloads to subscribed external channels.
// Each channel has its own worker and queue (workers.go). Call Stop at
// shutdown when Dispatch was used.
type Dispatcher struct {
	repo    models.NotificationChannelRepository
	client  *http.Client
	workers workerPool
	drops   dropLog
}

// NewDispatcher creates a channel dispatcher. It starts no goroutine: the
// worker of a channel starts with the first event of the channel.
func NewDispatcher(repo models.NotificationChannelRepository) *Dispatcher {
	return &Dispatcher{
		repo: repo,
		// A redirect is not followed: a signed body must not go to another
		// URL. A 3xx answer is a failed delivery.
		client: hooks.NoRedirectHTTPClient(&http.Client{
			Timeout: requestTimeout,
		}),
	}
}

// Dispatch finds all enabled channels subscribed to the payload's event type
// whose filters match the instances of the payload, encodes the body once
// per format (generic event JSON or a Teams Adaptive Card envelope) and
// queues one job per channel. It does not wait for the deliveries: the
// worker of each channel sends its events in order and writes one delivery
// log per event. A channel that the filters skip gets no delivery log (only
// a debug log line). A full channel queue drops the event (RecordDrop). ctx
// limits the channel lookup only; a cancelled lookup counts as a shutdown
// drop.
func (d *Dispatcher) Dispatch(ctx context.Context, payload EventPayload) {
	found, err := d.repo.FindChannelsByEvent(ctx, payload.EventType)
	if err != nil {
		if ctx.Err() != nil {
			d.RecordDrop(payload.EventType, "", DropReasonShutdown)
			return
		}
		slog.Error("notification channel: failed to find channels", "event", payload.EventType, "error", err)
		return
	}

	bodies := make(map[string][]byte, 2)
	failed := make(map[string]bool, 2)
	for _, ch := range found {
		if !ch.Filters.MatchesAny(payload.Instances) {
			slog.Debug("notification channel: skipped by filters",
				"channel", ch.Name, "event", payload.EventType, "entity_id", payload.EntityID)
			continue
		}
		format := bodyFormat(&ch)
		body, ok := bodies[format]
		if !ok && !failed[format] {
			body, err = encodeBody(format, payload)
			if err != nil {
				slog.Error("notification channel: failed to marshal payload", "format", format, "error", err)
				failed[format] = true
				body = nil
			} else {
				bodies[format] = body
			}
		}
		job := channelJob{channel: ch, eventType: payload.EventType, body: body}
		if body == nil {
			job.errMsg = "failed to encode the payload"
		}
		d.submit(job)
	}
}

// DispatchTo sends a payload to a single specific channel in the format of
// the channel, with a retry.
func (d *Dispatcher) DispatchTo(ctx context.Context, ch models.NotificationChannel, payload EventPayload) (string, int, string) {
	body, err := encodeBody(bodyFormat(&ch), payload)
	if err != nil {
		return "failed", 0, fmt.Sprintf("marshal error: %v", err)
	}
	return d.deliver(ctx, &ch, payload.EventType, body)
}

// DeliverOnce sends a payload to one channel with one attempt (no retry),
// also when the channel is disabled, in the format of the channel. TestChannel
// uses it, so the admin gets the result at once. It returns the delivery status ("success" or
// "failed"), the HTTP status code (0 when no answer came) and an error
// message without the webhook URL. It writes no delivery log.
func (d *Dispatcher) DeliverOnce(ctx context.Context, ch models.NotificationChannel, payload EventPayload) (string, int, string) {
	body, err := encodeBody(bodyFormat(&ch), payload)
	if err != nil {
		return "failed", 0, "marshal error"
	}
	statusCode, err := d.post(ctx, &ch, payload.EventType, body)
	if err != nil {
		return "failed", statusCode, err.Error()
	}
	return "success", statusCode, ""
}

func (d *Dispatcher) deliver(ctx context.Context, ch *models.NotificationChannel, eventType string, body []byte) (status string, statusCode int, errMsg string) {
	statusCode, err := d.post(ctx, ch, eventType, body)
	if err == nil {
		return "success", statusCode, ""
	}

	// An invalid URL gives the same error again, so it gets no retry.
	if isRetryable(statusCode) && !errors.Is(err, errInvalidWebhookURL) {
		select {
		case <-ctx.Done():
			return "failed", 0, "context cancelled"
		case <-time.After(retryDelay):
		}
		statusCode, err = d.post(ctx, ch, eventType, body)
		if err == nil {
			return "success", statusCode, ""
		}
	}

	slog.Warn("notification channel: delivery failed",
		"channel", ch.Name, "event", eventType,
		"status_code", statusCode, "error", err)
	return "failed", statusCode, err.Error()
}

func (d *Dispatcher) post(ctx context.Context, ch *models.NotificationChannel, eventType string, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ch.WebhookURL, bytes.NewReader(body))
	if err != nil {
		// The parse error contains the webhook URL (it can hold a token).
		// The delivery log and the log line get a fixed text only.
		return 0, errInvalidWebhookURL
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-StackManager-Event", eventType)

	if ch.Secret != "" {
		sig := sign(body, ch.Secret)
		req.Header.Set("X-StackManager-Signature", sig)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		// A *url.Error contains the webhook URL (it can hold a token).
		// The delivery log shows the cause only.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return 0, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()
	// Drain the body so the connection can be reused. A read error does not
	// change the delivery result, which depends on the status code only.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBody))

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		// The Location header is not in the message.
		return resp.StatusCode, fmt.Errorf("HTTP %d: redirect not followed", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

func sign(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return signaturePrefix + hex.EncodeToString(mac.Sum(nil))
}

func isRetryable(statusCode int) bool {
	return statusCode == 0 || statusCode == 429 || statusCode == 502 || statusCode == 503 || statusCode == 504
}

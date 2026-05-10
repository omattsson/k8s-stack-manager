package channel

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"backend/internal/models"

	"github.com/google/uuid"
)

const (
	maxResponseBody = 4096
	requestTimeout  = 10 * time.Second
	retryDelay      = 2 * time.Second
	signaturePrefix = "sha256="
)

// Dispatcher sends notification payloads to subscribed external channels.
type Dispatcher struct {
	repo   models.NotificationChannelRepository
	client *http.Client
}

// NewDispatcher creates a channel dispatcher.
func NewDispatcher(repo models.NotificationChannelRepository) *Dispatcher {
	return &Dispatcher{
		repo: repo,
		client: &http.Client{
			Timeout: requestTimeout,
		},
	}
}

// Dispatch finds all enabled channels subscribed to the payload's event type
// and POSTs the generic JSON to each. Errors are logged and recorded as
// delivery logs; they never propagate to the caller.
func (d *Dispatcher) Dispatch(ctx context.Context, payload EventPayload) {
	channels, err := d.repo.FindChannelsByEvent(ctx, payload.EventType)
	if err != nil {
		slog.Error("notification channel: failed to find channels", "event", payload.EventType, "error", err)
		return
	}
	if len(channels) == 0 {
		return
	}

	body, err := json.Marshal(payload)
	if err != nil {
		slog.Error("notification channel: failed to marshal payload", "error", err)
		return
	}

	for i := range channels {
		ch := &channels[i]
		status, statusCode, errMsg := d.deliver(ctx, ch, payload.EventType, body)
		if logErr := d.repo.CreateDeliveryLog(ctx, &models.NotificationDeliveryLog{
			ID:           uuid.New().String(),
			ChannelID:    ch.ID,
			ChannelName:  ch.Name,
			EventType:    payload.EventType,
			Status:       status,
			StatusCode:   statusCode,
			ErrorMessage: errMsg,
			CreatedAt:    time.Now().UTC(),
		}); logErr != nil {
			slog.Error("notification channel: failed to create delivery log",
				"channel", ch.Name, "event", payload.EventType, "error", logErr)
		}
	}
}

// DispatchTo sends a payload to a single specific channel (used by TestChannel).
func (d *Dispatcher) DispatchTo(ctx context.Context, ch models.NotificationChannel, payload EventPayload) (string, int, string) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "failed", 0, fmt.Sprintf("marshal error: %v", err)
	}
	return d.deliver(ctx, &ch, payload.EventType, body)
}

func (d *Dispatcher) deliver(ctx context.Context, ch *models.NotificationChannel, eventType string, body []byte) (status string, statusCode int, errMsg string) {
	statusCode, err := d.post(ctx, ch, eventType, body)
	if err == nil {
		return "success", statusCode, ""
	}

	if isRetryable(statusCode) {
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
		return 0, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-StackManager-Event", eventType)

	if ch.Secret != "" {
		sig := sign(body, ch.Secret)
		req.Header.Set("X-StackManager-Signature", sig)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBody))

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

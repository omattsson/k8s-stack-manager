package hooks

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// Dispatcher fans an event out to all subscriptions registered for that event.
// It is safe for concurrent use after construction; subscriptions are immutable
// for the lifetime of the Dispatcher.
type Dispatcher struct {
	subs   []Subscription
	byEvent map[string][]int
	client httpClient
	now    func() time.Time
}

// NewDispatcher validates cfg and returns a Dispatcher.
// Pass http.DefaultClient (or an injected client in tests).
func NewDispatcher(cfg Config, client httpClient) (*Dispatcher, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	d := &Dispatcher{
		subs:    cfg.Subscriptions,
		byEvent: make(map[string][]int),
		client:  client,
		now:     time.Now,
	}
	for i, s := range cfg.Subscriptions {
		for _, e := range s.Events {
			d.byEvent[e] = append(d.byEvent[e], i)
		}
	}
	return d, nil
}

// Fire dispatches the envelope to every subscription registered for the event.
// All subscriptions are invoked in registration order. The first error from a
// subscription with FailurePolicyFail aborts the operation and is returned;
// other errors are logged and ignored. Subscriptions that respond with
// Allowed=false are treated as failures.
//
// envelope.APIVersion, .Kind, .Event, .Timestamp, .RequestID are populated
// by Fire — callers do not need to set them.
func (d *Dispatcher) Fire(ctx context.Context, event string, envelope EventEnvelope) error {
	indices := d.byEvent[event]
	if len(indices) == 0 {
		return nil
	}

	envelope.APIVersion = envelopeAPIVersion
	envelope.Kind = "EventEnvelope"
	envelope.Event = event
	envelope.Timestamp = d.now()
	if envelope.RequestID == "" {
		envelope.RequestID = newRequestID()
	}

	for _, idx := range indices {
		sub := d.subs[idx]
		if err := d.invoke(ctx, sub, envelope); err != nil {
			if sub.FailurePolicy == FailurePolicyFail {
				return fmt.Errorf("hook %q failed (failure_policy=fail): %w", sub.Name, err)
			}
			slog.Warn("hook failed (failure_policy=ignore)",
				"subscription", sub.Name,
				"event", event,
				"request_id", envelope.RequestID,
				"error", err)
		}
	}
	return nil
}

func (d *Dispatcher) invoke(ctx context.Context, sub Subscription, envelope EventEnvelope) error {
	ctx, _, finish := startDispatchSpan(ctx, envelope.Event, sub.Name, envelope.RequestID)

	resp, statusCode, err := deliver(ctx, d.client, sub, envelope)
	if err != nil {
		finish(classifyErr(err), err, statusCode)
		return err
	}
	if !resp.Allowed {
		msg := resp.Message
		if msg == "" {
			msg = "subscriber denied"
		}
		denyErr := errors.New(msg)
		finish(outcomeDenied, denyErr, statusCode)
		return denyErr
	}
	finish(outcomeSuccess, nil, statusCode)
	return nil
}

// EventNames returns the events that have at least one subscription.
// Useful for logging the active configuration on startup.
func (d *Dispatcher) EventNames() []string {
	out := make([]string, 0, len(d.byEvent))
	for e := range d.byEvent {
		out = append(out, e)
	}
	return out
}

func newRequestID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fallback preserves the req- + 24-hex-char format so parsers and
		// dashboards see a consistent shape even when entropy is unavailable.
		fb := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
		return "req-" + hex.EncodeToString(fb[:12])
	}
	return "req-" + hex.EncodeToString(b[:])
}

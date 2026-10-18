package hooks

import "context"

// Trigger types: what started the operation of an event.
const (
	// TriggerUser is an API call by a user (also the default when the
	// caller sets no trigger).
	TriggerUser = "user"
	// TriggerCleanupPolicy is a cleanup policy run (scheduled or manual).
	TriggerCleanupPolicy = "cleanup-policy"
	// TriggerTTL is the TTL reaper that stops an expired instance.
	TriggerTTL = "ttl"
)

// Trigger tells a subscriber what started the operation of an event, for
// example "stopped by cleanup policy nightly-stop". ID and Name identify the
// user or the cleanup policy; they are empty for TriggerTTL.
type Trigger struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

type triggerKey struct{}

// WithTrigger returns a context that carries t. The deploy manager reads it
// when an operation starts and adds it to the envelopes of that operation.
func WithTrigger(ctx context.Context, t Trigger) context.Context {
	return context.WithValue(ctx, triggerKey{}, t)
}

// TriggerFromContext returns the trigger that WithTrigger stored in ctx.
func TriggerFromContext(ctx context.Context) (Trigger, bool) {
	if ctx == nil {
		return Trigger{}, false
	}
	t, ok := ctx.Value(triggerKey{}).(Trigger)
	return t, ok
}

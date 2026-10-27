package hooks

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// Dispatcher fans an event out to all subscriptions registered for that event.
// It is safe for concurrent use after construction; subscriptions are immutable
// for the lifetime of the Dispatcher.
type Dispatcher struct {
	subs    []Subscription
	byEvent map[string][]int
	client  httpClient
	now     func() time.Time
	// clusterNames sets cluster_name in envelopes (WithClusterNames); nil
	// leaves it empty.
	clusterNames *ClusterNameResolver
	// templates sets the template fields of the instance and decides the
	// applies_to scopes that need them (WithTemplates); nil leaves them
	// empty and such scopes unknown.
	templates *TemplateResolver
}

// NewDispatcher validates cfg and returns a Dispatcher.
// Pass http.DefaultClient (or an injected client in tests). The dispatcher
// does not follow redirects (noRedirectClient): a 3xx answer is a subscriber
// error, so a signed body never goes to another URL.
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
		client:  noRedirectClient(client),
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
	return d.fireInternal(ctx, event, envelope, nil)
}

// FireWithProgress is like Fire but streams progress lines from the subscriber
// response to onProgress. Subscribers can write "LOG: <message>\n" lines before
// their final JSON response; each such line is forwarded to onProgress with the
// prefix stripped. This enables long-running hooks (e.g. CI trigger gates) to
// report status back to the deployment log in near-real-time.
// When onProgress is nil, behaviour is identical to Fire.
func (d *Dispatcher) FireWithProgress(ctx context.Context, event string, envelope EventEnvelope, onProgress func(string)) error {
	return d.fireInternal(ctx, event, envelope, onProgress)
}

func (d *Dispatcher) fireInternal(ctx context.Context, event string, envelope EventEnvelope, onProgress func(string)) error {
	indices := d.byEvent[event]
	if len(indices) == 0 {
		return nil
	}

	envelope = d.prepare(ctx, event, envelope)

	for _, idx := range indices {
		sub := d.subs[idx]
		if isBlocking(sub, event) {
			// FireBlocking calls this subscription.
			continue
		}
		run, scopeErr := checkScope(sub, event, envelope)
		if scopeErr != nil {
			return scopeErr
		}
		if !run {
			continue
		}
		if err := d.invoke(ctx, sub, envelope, onProgress); err != nil {
			if sub.FailurePolicy == FailurePolicyFail {
				return &FailedError{Hook: sub.Name, Err: err}
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

// prepare sets the envelope fields that every dispatch sets.
func (d *Dispatcher) prepare(ctx context.Context, event string, envelope EventEnvelope) EventEnvelope {
	envelope.APIVersion = envelopeAPIVersion
	envelope.Kind = "EventEnvelope"
	envelope.Event = event
	envelope.Timestamp = d.now()
	if envelope.RequestID == "" {
		envelope.RequestID = newRequestID()
	}
	// The two lookups run at the same time: one wait limit, not two.
	template := d.templates.startResolve(ctx, envelope.InstanceRef)
	envelope = d.clusterNames.setClusterNames(ctx, envelope)
	envelope.InstanceRef = withTemplateOf(envelope.InstanceRef, template())
	return envelope
}

// checkScope reports whether sub runs for the instance of envelope (its
// applies_to). When the scope needs the template and the lookup did not
// answer, a subscriber with failure_policy=fail gives a *FailedError with
// ErrScopeUnknown (a gate that cannot decide is never skipped silently); a
// subscriber with failure_policy=ignore is skipped with a warning.
func checkScope(sub Subscription, event string, envelope EventEnvelope) (bool, error) {
	switch sub.AppliesTo.decide(envelope.InstanceRef) {
	case scopeMatch:
		return true, nil
	case scopeNoMatch:
		return false, nil
	}
	if sub.FailurePolicy == FailurePolicyFail {
		return false, &FailedError{Hook: sub.Name, Err: ErrScopeUnknown}
	}
	slog.Warn("hook skipped: could not check applies_to (failure_policy=ignore)",
		"subscription", sub.Name,
		"event", event,
		"request_id", envelope.RequestID)
	return false, nil
}

// isBlocking reports whether sub is a blocking subscriber of event. Only
// post-deploy supports blocking subscribers.
func isBlocking(sub Subscription, event string) bool {
	return sub.Blocking && event == EventPostDeploy
}

// blockingIndices returns the blocking subscriptions of event in
// registration order.
func (d *Dispatcher) blockingIndices(event string) []int {
	if d == nil {
		return nil
	}
	var out []int
	for _, idx := range d.byEvent[event] {
		if isBlocking(d.subs[idx], event) {
			out = append(out, idx)
		}
	}
	return out
}

// blockingIndicesFor returns the blocking subscriptions of event that
// FireBlocking handles for the instance ref: all but the ones whose
// applies_to does not match. A subscriber whose scope is unknown counts:
// FireBlocking fails on it (failure_policy=fail) or skips it with a warning
// (ignore), as Fire does. Pass a ref from ResolveInstance, so the answer
// agrees with the dispatch.
func (d *Dispatcher) blockingIndicesFor(event string, ref *InstanceRef) []int {
	var out []int
	for _, idx := range d.blockingIndices(event) {
		if d.subs[idx].AppliesTo.decide(ref) != scopeNoMatch {
			out = append(out, idx)
		}
	}
	return out
}

// HasBlocking reports whether event has at least one blocking subscriber,
// for any instance. Use HasBlockingFor for one instance.
func (d *Dispatcher) HasBlocking(event string) bool {
	return len(d.blockingIndices(event)) > 0
}

// HasBlockingFor reports whether FireBlocking has a blocking subscriber of
// event to handle for the instance ref (see blockingIndicesFor).
func (d *Dispatcher) HasBlockingFor(event string, ref *InstanceRef) bool {
	return len(d.blockingIndicesFor(event, ref)) > 0
}

// BlockingTimeout returns the sum of the timeouts of the blocking
// subscribers of event, for any instance: an upper bound of the time
// FireBlocking can take. Use BlockingTimeoutFor for one instance.
func (d *Dispatcher) BlockingTimeout(event string) time.Duration {
	return d.sumTimeouts(d.blockingIndices(event))
}

// BlockingTimeoutFor returns the sum of the timeouts of the blocking
// subscribers of event for the instance ref: the longest time FireBlocking
// can take for it.
func (d *Dispatcher) BlockingTimeoutFor(event string, ref *InstanceRef) time.Duration {
	return d.sumTimeouts(d.blockingIndicesFor(event, ref))
}

func (d *Dispatcher) sumTimeouts(indices []int) time.Duration {
	var total time.Duration
	for _, idx := range indices {
		total += time.Duration(d.subs[idx].TimeoutSeconds) * time.Second
	}
	return total
}

// TotalTimeout returns the sum of the timeouts of all subscribers of event:
// the longest time Fire can take for event (subscribers run one after the
// other). It ignores applies_to, so it is an upper bound for any instance.
func (d *Dispatcher) TotalTimeout(event string) time.Duration {
	if d == nil {
		return 0
	}
	var total time.Duration
	for _, idx := range d.byEvent[event] {
		total += time.Duration(d.subs[idx].TimeoutSeconds) * time.Second
	}
	return total
}

// IgnoredFailure is a blocking subscriber with failure_policy=ignore that
// failed or denied. The operation continues; the caller shows a warning.
type IgnoredFailure struct {
	Hook string
	Err  error
}

// UserMessage returns a text about the failure that is safe to show to
// users (never the subscriber URL), see the package function UserMessage.
func (f IgnoredFailure) UserMessage(event, operation string) string {
	return UserMessage(&FailedError{Hook: f.Hook, Err: f.Err}, event, operation)
}

// BlockingCallbacks are the optional callbacks of FireBlocking.
type BlockingCallbacks struct {
	// OnStart is called with the subscription name before each call.
	OnStart func(hook string)
	// OnProgress receives the "LOG: " lines of the subscriber (prefix
	// removed), as for FireWithProgress.
	OnProgress func(line string)
}

// FireBlocking calls the blocking subscribers of event one after the other
// (registration order) and waits for each, with progress streaming. Each call
// is limited by the subscription timeout and by ctx.
//
// A failure (also a denial or a timeout) of a subscriber with
// failure_policy=fail stops the dispatch and returns a *FailedError. A
// failure of a subscriber with failure_policy=ignore is returned in the
// IgnoredFailure list and the next subscriber is called. Non-blocking
// subscribers of event are not called; use Fire for them. A subscriber whose
// applies_to does not match the instance is not called; one whose scope is
// unknown is handled as in Fire (see checkScope).
func (d *Dispatcher) FireBlocking(ctx context.Context, event string, envelope EventEnvelope, cb BlockingCallbacks) ([]IgnoredFailure, error) {
	indices := d.blockingIndices(event)
	if len(indices) == 0 {
		return nil, nil
	}
	envelope = d.prepare(ctx, event, envelope)
	onProgress := cb.OnProgress
	if onProgress == nil {
		onProgress = func(string) {}
	}

	var ignored []IgnoredFailure
	for _, idx := range indices {
		sub := d.subs[idx]
		run, scopeErr := checkScope(sub, event, envelope)
		if scopeErr != nil {
			return ignored, scopeErr
		}
		if !run {
			continue
		}
		if cb.OnStart != nil {
			cb.OnStart(sub.Name)
		}
		err := d.invoke(ctx, sub, envelope, onProgress)
		if err == nil {
			continue
		}
		if sub.FailurePolicy == FailurePolicyFail {
			return ignored, &FailedError{Hook: sub.Name, Err: err}
		}
		slog.Warn("blocking hook failed (failure_policy=ignore)",
			"subscription", sub.Name,
			"event", event,
			"request_id", envelope.RequestID,
			"error", err)
		ignored = append(ignored, IgnoredFailure{Hook: sub.Name, Err: err})
	}
	return ignored, nil
}

func (d *Dispatcher) invoke(ctx context.Context, sub Subscription, envelope EventEnvelope, onProgress func(string)) error {
	ctx, _, finish := startDispatchSpan(ctx, envelope.Event, sub.Name, envelope.RequestID)

	var resp HookResponse
	var statusCode int
	var err error
	if onProgress != nil {
		resp, statusCode, err = deliverStreaming(ctx, d.client, sub, envelope, onProgress)
	} else {
		resp, statusCode, err = deliver(ctx, d.client, sub, envelope)
	}
	if err != nil {
		finish(classifyErr(err), err, statusCode)
		return err
	}
	if !resp.Allowed {
		msg := resp.Message
		if msg == "" {
			msg = "subscriber denied"
		}
		denyErr := &DeniedError{Hook: sub.Name, Message: msg}
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

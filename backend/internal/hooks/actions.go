package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// actionRequestKind is the value placed in ActionRequest.Kind.
const actionRequestKind = "ActionRequest"

// ActionSubscription registers a named, RPC-style webhook reachable via
// POST /api/v1/stack-instances/:id/actions/:name. Unlike event Subscriptions,
// actions are explicitly invoked by API callers and may return arbitrary JSON.
type ActionSubscription struct {
	Name           string `json:"name"`
	URL            string `json:"url"`
	Description    string `json:"description,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	Secret         string `json:"-"`
}

// ActionRequest is the JSON payload posted to action subscribers.
type ActionRequest struct {
	APIVersion string         `json:"apiVersion"`
	Kind       string         `json:"kind"`
	Action     string         `json:"action"`
	Timestamp  time.Time      `json:"timestamp"`
	RequestID  string         `json:"request_id"`
	Instance   *InstanceRef   `json:"instance"`
	Parameters map[string]any `json:"parameters,omitempty"`
}

// ActionResult captures the subscriber's response so callers can return it
// verbatim to API clients.
type ActionResult struct {
	StatusCode int             `json:"status_code"`
	Body       json.RawMessage `json:"body"`
}

// ActionRegistry holds named action subscriptions and dispatches invocations.
// Safe for concurrent use: the actions map is written only by
// NewActionRegistry before the pointer escapes, and read-only afterwards
// (mirrors Dispatcher). Adding runtime registration would require a lock;
// the current shape matches the "config-at-boot" operational model.
type ActionRegistry struct {
	actions map[string]ActionSubscription
	client  httpClient
	now     func() time.Time
}

// NewActionRegistry validates each subscription, normalizes optional fields,
// and returns a registry. Pass http.DefaultClient (or an injected client in tests).
func NewActionRegistry(actions []ActionSubscription, client httpClient) (*ActionRegistry, error) {
	if client == nil {
		client = http.DefaultClient
	}
	r := &ActionRegistry{
		actions: make(map[string]ActionSubscription, len(actions)),
		client:  client,
		now:     time.Now,
	}
	for i, a := range actions {
		if err := validateAction(&a); err != nil {
			return nil, fmt.Errorf("action[%d]: %w", i, err)
		}
		if _, dup := r.actions[a.Name]; dup {
			return nil, fmt.Errorf("action %q: duplicate name", a.Name)
		}
		r.actions[a.Name] = a
	}
	return r, nil
}

// Names returns the registered action names in unspecified order.
// Useful for serving a discoverability endpoint.
func (r *ActionRegistry) Names() []string {
	out := make([]string, 0, len(r.actions))
	for name := range r.actions {
		out = append(out, name)
	}
	return out
}

// Lookup returns the subscription registered for name, if any.
func (r *ActionRegistry) Lookup(name string) (ActionSubscription, bool) {
	s, ok := r.actions[name]
	return s, ok
}

// ErrUnknownAction is returned by Invoke when the action is not registered.
type ErrUnknownAction struct{ Name string }

func (e ErrUnknownAction) Error() string {
	return fmt.Sprintf("unknown action %q", e.Name)
}

// Invoke posts an ActionRequest to the subscriber and returns its raw response.
// Returns ErrUnknownAction when name is not registered. Network/HTTP errors
// propagate; non-2xx responses are returned as ActionResult with the status
// and body for the caller to surface.
//
// Emits an OTel span "hooks.action" for each invocation and the
// hook.action_invocations_total / hook.action_invocation_duration metrics.
// The outbound request carries W3C traceparent so subscribers can stitch
// their own spans as children.
func (r *ActionRegistry) Invoke(ctx context.Context, name string, instance *InstanceRef, params map[string]any) (ActionResult, error) {
	sub, ok := r.Lookup(name)
	if !ok {
		// Record an outcome metric for unknown actions too — operators want to
		// see spikes in bad action names, not just invocation successes.
		_, _, finish := startActionSpan(ctx, name, "")
		err := ErrUnknownAction{Name: name}
		finish(outcomeUnknownAction, err, 0)
		return ActionResult{}, err
	}

	req := ActionRequest{
		APIVersion: envelopeAPIVersion,
		Kind:       actionRequestKind,
		Action:     name,
		Timestamp:  r.now(),
		RequestID:  newRequestID(),
		Instance:   instance,
		Parameters: params,
	}

	ctx, _, finish := startActionSpan(ctx, name, req.RequestID)

	body, err := json.Marshal(req)
	if err != nil {
		finish(outcomeMarshalError, err, 0)
		return ActionResult{}, fmt.Errorf("marshal action request: %w", err)
	}

	timeout := time.Duration(sub.TimeoutSeconds) * time.Second
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, sub.URL, bytes.NewReader(body))
	if err != nil {
		finish(outcomeTransportError, err, 0)
		return ActionResult{}, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set(headerEvent, "action:"+name)
	httpReq.Header.Set(headerRequestID, req.RequestID)
	if sub.Secret != "" {
		httpReq.Header.Set(headerSignature, sign(body, sub.Secret))
	}
	injectTraceContext(reqCtx, httpReq)

	resp, err := r.client.Do(httpReq)
	if err != nil {
		finish(classifyErr(err), err, 0)
		return ActionResult{}, fmt.Errorf("post action: %w", err)
	}
	defer resp.Body.Close()

	respBody, truncated, err := readLimitedBody(resp.Body, maxHookResponseBytes)
	if err != nil {
		finish(outcomeTransportError, err, resp.StatusCode)
		return ActionResult{}, fmt.Errorf("read action response: %w", err)
	}
	if truncated {
		tooLarge := fmt.Errorf("action response exceeded %d-byte limit", maxHookResponseBytes)
		finish(outcomeTransportError, tooLarge, resp.StatusCode)
		return ActionResult{}, tooLarge
	}

	// Empty bodies are returned as "null" so the JSON envelope stays valid.
	if len(bytes.TrimSpace(respBody)) == 0 {
		respBody = []byte("null")
	}

	if !json.Valid(respBody) {
		badJSON := fmt.Errorf("action %q subscriber returned non-JSON body (%d bytes)", name, len(respBody))
		finish(outcomeTransportError, badJSON, resp.StatusCode)
		return ActionResult{}, badJSON
	}

	// Actions are RPC-style and forward arbitrary JSON; a 2xx is a successful
	// invocation from our perspective even if the subscriber encoded its own
	// logical error in the body. A non-2xx is classified as http_error so
	// dashboards can spot bad subscribers.
	outcome := outcomeSuccess
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		outcome = outcomeHTTPError
	}
	finish(outcome, nil, resp.StatusCode)
	return ActionResult{StatusCode: resp.StatusCode, Body: json.RawMessage(respBody)}, nil
}

func validateAction(a *ActionSubscription) error {
	if a.Name == "" {
		return fmt.Errorf("name is required")
	}
	if a.URL == "" {
		return fmt.Errorf("url is required")
	}
	u, err := url.Parse(a.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("invalid url")
	}
	if a.TimeoutSeconds < 0 {
		return fmt.Errorf("timeout_seconds must be >= 0")
	}
	if a.TimeoutSeconds == 0 {
		// Actions tend to do real work (db wipes, syncs) — give them more headroom
		// than event hooks. Default 30s, ceiling 5m.
		a.TimeoutSeconds = 30
	}
	if time.Duration(a.TimeoutSeconds)*time.Second > 5*time.Minute {
		return fmt.Errorf("timeout_seconds must be <= 300")
	}
	return nil
}

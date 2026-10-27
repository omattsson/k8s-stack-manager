package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
//
// Label, Confirm and Parameters are optional UI metadata: the web UI and the
// CLI read them from GET /api/v1/stack-instances/:id/actions. LogPath is an
// optional path template (for example "/jobs/{job_id}/log") on the host of
// URL; when set, the backend proxies the job log of an asynchronous action.
type ActionSubscription struct {
	Name           string            `json:"name"`
	URL            string            `json:"url"`
	Description    string            `json:"description,omitempty"`
	Label          string            `json:"label,omitempty"`
	Confirm        string            `json:"confirm,omitempty"`
	LogPath        string            `json:"log_path,omitempty"`
	Parameters     []ActionParameter `json:"parameters,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	// AppliesTo limits the action to some instances (#720): the list
	// endpoint shows it only for them, and an invoke for another instance
	// answers as for an unknown action. Empty: every instance.
	AppliesTo Scope  `json:"applies_to"`
	Secret    string `json:"-"`
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

// Invoke errors for an answer that the backend cannot forward. With these
// errors ActionResult.StatusCode holds the HTTP status of the answer (for
// example 302 with an HTML body); a transport error leaves it 0.
var (
	ErrActionNonJSONBody  = errors.New("action subscriber returned a non-JSON body")
	ErrActionBodyTooLarge = errors.New("action subscriber returned a body that is too large")
)

// ActionResult captures the subscriber's response so callers can return it
// verbatim to API clients.
type ActionResult struct {
	StatusCode int             `json:"status_code"`
	Body       json.RawMessage `json:"body"`
	RequestID  string          `json:"request_id"`
}

// ActionRegistry holds named action subscriptions and dispatches invocations.
// Safe for concurrent use: the actions map is written only by
// NewActionRegistry before the pointer escapes, and read-only afterwards
// (mirrors Dispatcher). Adding runtime registration would require a lock;
// the current shape matches the "config-at-boot" operational model.
type ActionRegistry struct {
	actions map[string]ActionSubscription
	// client sends invoke calls and job log reads. It never follows a
	// redirect, so a signed request cannot reach another host.
	client httpClient
	now    func() time.Time
	// clusterNames sets instance.cluster_name in requests
	// (WithClusterNames); nil leaves it empty.
	clusterNames *ClusterNameResolver
	// templates sets the template fields of the instance and decides the
	// applies_to scopes that need them (WithTemplates).
	templates *TemplateResolver
}

// NewActionRegistry validates each subscription, normalizes optional fields,
// and returns a registry. Pass http.DefaultClient (or an injected client in tests).
func NewActionRegistry(actions []ActionSubscription, client httpClient) (*ActionRegistry, error) {
	if client == nil {
		client = http.DefaultClient
	}
	r := &ActionRegistry{
		actions: make(map[string]ActionSubscription, len(actions)),
		client:  noRedirectClient(client),
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

// ErrUnknownAction is returned by Invoke and LookupFor when the action is
// not registered, or when its applies_to does not match the instance (the
// same answer, so the list and the invoke agree).
type ErrUnknownAction struct{ Name string }

func (e ErrUnknownAction) Error() string {
	return fmt.Sprintf("unknown action %q", e.Name)
}

// LookupFor returns the action name for the instance ref, and ref with the
// template fields set (pass it to Invoke, so the instance is not looked up
// again). It returns ErrUnknownAction when the action is not registered or
// does not apply to the instance, and ErrScopeUnknown when its applies_to
// needs the template and the lookup did not answer. Both errors are counted
// in the action metrics, as an unknown action in Invoke.
func (r *ActionRegistry) LookupFor(ctx context.Context, name string, ref *InstanceRef) (ActionSubscription, *InstanceRef, error) {
	sub, ok := r.Lookup(name)
	if !ok {
		return ActionSubscription{}, ref, r.refuse(ctx, name, ErrUnknownAction{Name: name})
	}
	if sub.AppliesTo.needsTemplate() {
		ref = r.templates.resolve(ctx, ref)
	}
	switch sub.AppliesTo.decide(ref) {
	case scopeNoMatch:
		return ActionSubscription{}, ref, r.refuse(ctx, name, ErrUnknownAction{Name: name})
	case scopeUnknown:
		return ActionSubscription{}, ref, r.refuse(ctx, name, ErrScopeUnknown)
	}
	return sub, ref, nil
}

// refuse records an invocation that did not reach a subscriber and returns
// err.
func (r *ActionRegistry) refuse(ctx context.Context, name string, err error) error {
	outcome := outcomeUnknownAction
	if errors.Is(err, ErrScopeUnknown) {
		outcome = outcomeScopeUnknown
	}
	_, _, finish := startActionSpan(ctx, name, "")
	finish(outcome, err, 0)
	return err
}

// Invoke posts an ActionRequest to the subscriber and returns its raw response.
// Returns ErrUnknownAction when name is not registered or its applies_to does
// not match the instance, and ErrScopeUnknown when the scope cannot be
// checked (see LookupFor); the subscriber is then not called. Network/HTTP errors
// propagate; non-2xx responses are returned as ActionResult with the status
// and body for the caller to surface. A redirect is not followed: the 3xx
// response is returned like any other non-2xx response.
//
// Emits an OTel span "hooks.action" for each invocation and the
// hook.action_invocations_total / hook.action_invocation_duration metrics.
// The outbound request carries W3C traceparent so subscribers can stitch
// their own spans as children.
func (r *ActionRegistry) Invoke(ctx context.Context, name string, instance *InstanceRef, params map[string]any) (ActionResult, error) {
	// Unknown and out-of-scope actions are counted too: operators want to
	// see spikes in bad action names, not just invocation successes.
	sub, instance, err := r.LookupFor(ctx, name, instance)
	if err != nil {
		return ActionResult{}, err
	}
	// The request carries the template fields also when the scope does not
	// need them (no second lookup when LookupFor resolved the instance). The
	// template and cluster name lookups run at the same time.
	template := r.templates.startResolve(ctx, instance)
	instance = withTemplateOf(r.clusterNames.instanceRef(ctx, instance), template())

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
	fail := func(err error, statusCode int) (ActionResult, error) {
		return ActionResult{RequestID: req.RequestID, StatusCode: statusCode}, err
	}

	body, err := json.Marshal(req)
	if err != nil {
		finish(outcomeMarshalError, err, 0)
		return fail(fmt.Errorf("marshal action request: %w", err), 0)
	}

	timeout := time.Duration(sub.TimeoutSeconds) * time.Second
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, sub.URL, bytes.NewReader(body))
	if err != nil {
		finish(outcomeTransportError, err, 0)
		return fail(fmt.Errorf("build request: %w", err), 0)
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
		return fail(fmt.Errorf("post action: %w", err), 0)
	}
	defer resp.Body.Close()

	respBody, truncated, err := readLimitedBody(resp.Body, maxHookResponseBytes)
	if err != nil {
		finish(outcomeTransportError, err, resp.StatusCode)
		return fail(fmt.Errorf("read action response: %w", err), 0)
	}
	if truncated {
		tooLarge := fmt.Errorf("%w: exceeded %d-byte limit", ErrActionBodyTooLarge, maxHookResponseBytes)
		finish(outcomeTransportError, tooLarge, resp.StatusCode)
		return fail(tooLarge, resp.StatusCode)
	}

	// Empty bodies are returned as "null" so the JSON envelope stays valid.
	if len(bytes.TrimSpace(respBody)) == 0 {
		respBody = []byte("null")
	}

	// A refusal (status 400 or higher) with a plain text body keeps its
	// reason: the body becomes a JSON string. Other non-JSON refusals give a
	// null result. A non-JSON body of another status stays an error.
	if resp.StatusCode >= 400 && !json.Valid(respBody) {
		respBody = plainTextActionBody(resp.Header.Get("Content-Type"), respBody)
	}

	if !json.Valid(respBody) {
		badJSON := fmt.Errorf("action %q: %w (%d bytes)", name, ErrActionNonJSONBody, len(respBody))
		finish(outcomeTransportError, badJSON, resp.StatusCode)
		return fail(badJSON, resp.StatusCode)
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
	return ActionResult{StatusCode: resp.StatusCode, Body: json.RawMessage(respBody), RequestID: req.RequestID}, nil
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
		a.TimeoutSeconds = 30
	}
	if time.Duration(a.TimeoutSeconds)*time.Second > 10*time.Minute {
		return fmt.Errorf("timeout_seconds must be <= 600")
	}
	if len(a.Label) > maxActionLabelLen {
		return fmt.Errorf("label must be at most %d characters", maxActionLabelLen)
	}
	if len(a.Confirm) > maxActionConfirmLen {
		return fmt.Errorf("confirm must be at most %d characters", maxActionConfirmLen)
	}
	if err := validateLogPath(a.LogPath); err != nil {
		return err
	}
	if err := a.AppliesTo.Validate(); err != nil {
		return err
	}
	// Copy before normalizing so the caller's slice stays unchanged.
	a.Parameters = append([]ActionParameter(nil), a.Parameters...)
	return validateParameters(a.Parameters)
}

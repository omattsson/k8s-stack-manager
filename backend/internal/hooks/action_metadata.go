package hooks

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Parameter types that an action can declare. The web UI renders a text
// field, a switch or a select for them.
const (
	ParamTypeString = "string"
	ParamTypeBool   = "bool"
	ParamTypeEnum   = "enum"
)

const (
	maxActionLabelLen   = 100
	maxActionConfirmLen = 1000
	maxActionParams     = 20
	maxParamOptions     = 50
	maxParamStringLen   = 1024
	maxParamTextLen     = 200
)

// RedactedParameterValue replaces the value of a sensitive parameter in the
// action run history.
const RedactedParameterValue = "[redacted]"

// paramNamePattern restricts parameter names to identifiers that are safe as
// JSON keys, HTML form field names and CLI flags.
var paramNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,63}$`)

// ActionParameter describes one input of an action. It is UI metadata: the
// web UI renders a form from it, and the invoke endpoint checks the declared
// parameters (type, required, enum options) before it calls the subscriber.
type ActionParameter struct {
	// Default is a string for "string" and "enum", a bool for "bool".
	Default     any      `json:"default,omitempty"`
	Name        string   `json:"name"`
	Label       string   `json:"label,omitempty"`
	Description string   `json:"description,omitempty"`
	Type        string   `json:"type,omitempty"`
	Options     []string `json:"options,omitempty"`
	Required    bool     `json:"required,omitempty"`
	// Sensitive (type string only, no default): the value goes to the
	// subscriber, but the action run history stores RedactedParameterValue
	// and the web UI uses a password field.
	Sensitive bool `json:"sensitive,omitempty"`
}

// ErrInvalidParameters is returned by ValidateParameters when the caller's
// parameters do not match the declared schema. The message is safe to show
// to the API caller.
type ErrInvalidParameters struct{ Msg string }

func (e ErrInvalidParameters) Error() string { return e.Msg }

// validateParameters checks the declared parameter schema and normalizes the
// type ("" becomes "string"). It runs once at startup.
func validateParameters(params []ActionParameter) error {
	if len(params) > maxActionParams {
		return fmt.Errorf("at most %d parameters are allowed", maxActionParams)
	}
	seen := make(map[string]struct{}, len(params))
	for i := range params {
		p := &params[i]
		if !paramNamePattern.MatchString(p.Name) {
			return fmt.Errorf("parameters[%d]: name must match %s", i, paramNamePattern.String())
		}
		if _, dup := seen[p.Name]; dup {
			return fmt.Errorf("parameter %q: duplicate name", p.Name)
		}
		seen[p.Name] = struct{}{}
		if len(p.Label) > maxParamTextLen || len(p.Description) > maxParamTextLen {
			return fmt.Errorf("parameter %q: label and description must be at most %d characters", p.Name, maxParamTextLen)
		}
		if p.Type == "" {
			p.Type = ParamTypeString
		}
		switch p.Type {
		case ParamTypeString, ParamTypeBool:
			if len(p.Options) > 0 {
				return fmt.Errorf("parameter %q: options are only allowed for type enum", p.Name)
			}
		case ParamTypeEnum:
			if err := validateOptions(p); err != nil {
				return err
			}
		default:
			return fmt.Errorf("parameter %q: type must be string, bool or enum", p.Name)
		}
		if p.Sensitive {
			if p.Type != ParamTypeString {
				return fmt.Errorf("parameter %q: sensitive is only allowed for type string", p.Name)
			}
			// Defaults are public metadata (GET .../actions).
			if p.Default != nil {
				return fmt.Errorf("parameter %q: a sensitive parameter cannot have a default", p.Name)
			}
		}
		if p.Default != nil {
			if err := checkParamValue(*p, p.Default); err != nil {
				return fmt.Errorf("parameter %q: invalid default: %w", p.Name, err)
			}
		}
	}
	return nil
}

func validateOptions(p *ActionParameter) error {
	if len(p.Options) == 0 {
		return fmt.Errorf("parameter %q: type enum needs at least one option", p.Name)
	}
	if len(p.Options) > maxParamOptions {
		return fmt.Errorf("parameter %q: at most %d options are allowed", p.Name, maxParamOptions)
	}
	seen := make(map[string]struct{}, len(p.Options))
	for _, o := range p.Options {
		if o == "" || len(o) > maxParamTextLen {
			return fmt.Errorf("parameter %q: options must be non-empty and at most %d characters", p.Name, maxParamTextLen)
		}
		if _, dup := seen[o]; dup {
			return fmt.Errorf("parameter %q: duplicate option %q", p.Name, o)
		}
		seen[o] = struct{}{}
	}
	return nil
}

// checkParamValue reports whether v is a valid value for p.
func checkParamValue(p ActionParameter, v any) error {
	switch p.Type {
	case ParamTypeBool:
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("must be a boolean")
		}
	case ParamTypeEnum:
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("must be a string")
		}
		for _, o := range p.Options {
			if s == o {
				return nil
			}
		}
		return fmt.Errorf("must be one of: %s", strings.Join(p.Options, ", "))
	default:
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("must be a string")
		}
		if len(s) > maxParamStringLen {
			return fmt.Errorf("must be at most %d characters", maxParamStringLen)
		}
	}
	return nil
}

// ValidateParameters checks params against the declared parameters of the
// action. Declared parameters must have the declared type, an enum value must
// be one of the options, and a required parameter must be present (a
// required string must not be empty). Parameters that the action does not
// declare pass through unchanged, so existing API and CLI callers keep
// working when an operator adds a schema.
func (a ActionSubscription) ValidateParameters(params map[string]any) error {
	for _, p := range a.Parameters {
		v, ok := params[p.Name]
		if !ok || v == nil {
			if p.Required {
				return ErrInvalidParameters{Msg: fmt.Sprintf("parameter %q is required", p.Name)}
			}
			continue
		}
		if err := checkParamValue(p, v); err != nil {
			return ErrInvalidParameters{Msg: fmt.Sprintf("parameter %q %s", p.Name, err.Error())}
		}
		if s, isString := v.(string); isString && p.Required && strings.TrimSpace(s) == "" {
			return ErrInvalidParameters{Msg: fmt.Sprintf("parameter %q is required", p.Name)}
		}
	}
	return nil
}

// RecordedParameters returns the parameters of params that the action run
// history may store: only declared parameters with a valid value. It drops
// undeclared parameters and nil values, and replaces the value of a
// sensitive parameter with RedactedParameterValue. An action without
// declared parameters gives an empty map.
func (a ActionSubscription) RecordedParameters(params map[string]any) map[string]any {
	out := make(map[string]any, len(a.Parameters))
	for _, p := range a.Parameters {
		v, ok := params[p.Name]
		if !ok || v == nil || checkParamValue(p, v) != nil {
			continue
		}
		if p.Sensitive {
			out[p.Name] = RedactedParameterValue
			continue
		}
		out[p.Name] = v
	}
	return out
}

// minRedactedTail is the shortest start of a sensitive value that
// RedactSensitive removes from the end of a cut message.
const minRedactedTail = 4

// RedactSensitive replaces each non-empty value of a sensitive parameter in
// params (as sent) with RedactedParameterValue in text. It also replaces the
// one-line form of the value (whitespace runs as one space), and the start of
// a value at the end of a message that ActionResultMessage cut. It is a
// safety net for text that every viewer of the run history reads.
func (a ActionSubscription) RedactSensitive(text string, params map[string]any) string {
	for _, p := range a.Parameters {
		if !p.Sensitive {
			continue
		}
		v, ok := params[p.Name].(string)
		if !ok || strings.TrimSpace(v) == "" {
			continue
		}
		text = redactValue(text, v)
		if oneLine := strings.Join(strings.Fields(v), " "); oneLine != v {
			text = redactValue(text, oneLine)
		}
	}
	return text
}

// redactValue replaces v in text, and a start of v (at least
// minRedactedTail bytes) at the end of a cut text ("…" suffix of
// sanitizeDenyMessage).
func redactValue(text, v string) string {
	text = strings.ReplaceAll(text, v, RedactedParameterValue)
	body, cut := strings.CutSuffix(text, "…")
	if !cut {
		return text
	}
	for k := len(v) - 1; k >= minRedactedTail; k-- {
		if strings.HasSuffix(body, v[:k]) {
			return body[:len(body)-k] + RedactedParameterValue + "…"
		}
	}
	return text
}

// HasJobLog reports whether the backend can proxy the job log of the action.
func (a ActionSubscription) HasJobLog() bool { return a.LogPath != "" }

// List returns the registered actions sorted by name. The returned values
// include URL and Secret: callers that serve them to API clients must copy
// only the public fields.
func (r *ActionRegistry) List() []ActionSubscription {
	out := make([]ActionSubscription, 0, len(r.actions))
	for _, a := range r.actions {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ListFor returns the actions whose applies_to matches the instance ref,
// sorted by name. When a scope needs the template and the lookup did not
// answer, the action is left out and incomplete is true. The template is
// looked up only when a scope needs it.
func (r *ActionRegistry) ListFor(ctx context.Context, ref *InstanceRef) (actions []ActionSubscription, incomplete bool) {
	all := r.List()
	for _, a := range all {
		if a.AppliesTo.needsTemplate() {
			ref = r.templates.resolve(ctx, ref)
			break
		}
	}
	actions = make([]ActionSubscription, 0, len(all))
	for _, a := range all {
		switch a.AppliesTo.decide(ref) {
		case scopeMatch:
			actions = append(actions, a)
		case scopeUnknown:
			incomplete = true
		}
	}
	return actions, incomplete
}

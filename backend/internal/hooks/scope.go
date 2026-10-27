package hooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Limits of an applies_to block (#720).
const (
	maxScopeValues   = 50
	maxScopeValueLen = 255
	maxScopeIDLen    = 36
)

// ErrScopeUnknown is the cause of a *FailedError (event subscriptions) and
// the error of ActionRegistry.LookupFor and Invoke (actions) when applies_to
// needs the template of the instance and the template lookup failed or
// timed out. The core never guesses: a gate that cannot decide fails, an
// action is not called.
var ErrScopeUnknown = errors.New("could not check where the hook applies: the template of the instance is unknown")

// Scope is the applies_to block of a subscription or an action: it limits
// the subscription or the action to some instances. Fields combine with
// AND, the values inside one field with OR. An empty Scope applies to every
// instance.
//
// TemplateIDs and TemplateCategories match the template of the instance's
// definition (source_template_id) and that template's category. An instance
// whose definition has no template, or whose template was deleted, never
// matches them.
type Scope struct {
	TemplateIDs []string `json:"template_ids,omitempty"`
	// TemplateCategories match trimmed and case-insensitive, exactly (no
	// globs). Validate stores them in lower case.
	TemplateCategories []string `json:"template_categories,omitempty"`
	DefinitionIDs      []string `json:"definition_ids,omitempty"`
	// ClusterIDs match the cluster_id of the instance. An instance without
	// a cluster_id matches none.
	ClusterIDs []string `json:"cluster_ids,omitempty"`
}

// UnmarshalJSON rejects unknown fields: a misspelt field would otherwise
// leave the scope empty, and the subscription would apply to every instance.
func (s *Scope) UnmarshalJSON(b []byte) error {
	type plain Scope
	var p plain
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return fmt.Errorf("applies_to: %w", err)
	}
	*s = Scope(p)
	return nil
}

// IsEmpty reports whether s applies to every instance.
func (s Scope) IsEmpty() bool {
	return len(s.TemplateIDs) == 0 && len(s.TemplateCategories) == 0 &&
		len(s.DefinitionIDs) == 0 && len(s.ClusterIDs) == 0
}

// needsTemplate reports whether a decision needs the template of the
// instance.
func (s Scope) needsTemplate() bool {
	return len(s.TemplateIDs) > 0 || len(s.TemplateCategories) > 0
}

// Validate checks the limits and normalizes the values in place: trimmed,
// categories in lower case. It copies the slices first, so the caller's
// slices stay unchanged.
func (s *Scope) Validate() error {
	var err error
	if s.TemplateIDs, err = normalizeScopeValues("template_ids", s.TemplateIDs, maxScopeIDLen, false); err != nil {
		return err
	}
	if s.TemplateCategories, err = normalizeScopeValues("template_categories", s.TemplateCategories, maxScopeValueLen, true); err != nil {
		return err
	}
	if s.DefinitionIDs, err = normalizeScopeValues("definition_ids", s.DefinitionIDs, maxScopeIDLen, false); err != nil {
		return err
	}
	if s.ClusterIDs, err = normalizeScopeValues("cluster_ids", s.ClusterIDs, maxScopeValueLen, false); err != nil {
		return err
	}
	// "all" is the cleanup policy value for every cluster; no instance has
	// it, so a scope with it would match nothing.
	if slices.Contains(s.ClusterIDs, "all") {
		return errors.New(`applies_to.cluster_ids: "all" matches no instance; omit cluster_ids to match every cluster`)
	}
	return nil
}

func normalizeScopeValues(field string, values []string, maxLen int, lower bool) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) > maxScopeValues {
		return nil, fmt.Errorf("applies_to.%s: at most %d values", field, maxScopeValues)
	}
	out := make([]string, len(values))
	for i, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			return nil, fmt.Errorf("applies_to.%s[%d]: must not be empty", field, i)
		}
		if len(v) > maxLen {
			return nil, fmt.Errorf("applies_to.%s[%d]: at most %d characters", field, i, maxLen)
		}
		if lower {
			v = normalizeCategory(v)
		}
		out[i] = v
	}
	return out, nil
}

func normalizeCategory(c string) string {
	return strings.ToLower(strings.TrimSpace(c))
}

// scopeDecision is the result of Scope.decide.
type scopeDecision int

const (
	scopeMatch scopeDecision = iota
	scopeNoMatch
	// scopeUnknown: the decision needs the template of the instance, and
	// the template lookup did not answer.
	scopeUnknown
)

// decide reports whether s applies to the instance ref. The fields that
// need no lookup are checked first, so an instance on another cluster is
// scopeNoMatch also when its template is unknown.
func (s Scope) decide(ref *InstanceRef) scopeDecision {
	if s.IsEmpty() {
		return scopeMatch
	}
	if ref == nil {
		return scopeNoMatch
	}
	if len(s.DefinitionIDs) > 0 && !containsString(s.DefinitionIDs, ref.StackDefinitionID) {
		return scopeNoMatch
	}
	if len(s.ClusterIDs) > 0 && !containsString(s.ClusterIDs, ref.ClusterID) {
		return scopeNoMatch
	}
	if !s.needsTemplate() {
		return scopeMatch
	}
	if !ref.templateResolved {
		return scopeUnknown
	}
	if len(s.TemplateIDs) > 0 && !containsString(s.TemplateIDs, ref.TemplateID) {
		return scopeNoMatch
	}
	if len(s.TemplateCategories) > 0 && !containsString(s.TemplateCategories, normalizeCategory(ref.TemplateCategory)) {
		return scopeNoMatch
	}
	return scopeMatch
}

// containsString reports whether v is in values. An empty v never matches:
// Validate refuses empty values.
func containsString(values []string, v string) bool {
	return v != "" && slices.Contains(values, v)
}

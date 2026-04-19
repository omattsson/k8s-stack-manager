package hooks

import (
	"encoding/json"
	"fmt"
	"os"
)

// SubscriptionsFile is the on-disk schema for HOOKS_CONFIG_FILE. Keeping event
// subscriptions and action subscriptions in one file lets operators manage them
// as a single unit; either list may be empty.
type SubscriptionsFile struct {
	Subscriptions []SubscriptionSpec       `json:"subscriptions"`
	Actions       []ActionSubscriptionSpec `json:"actions"`
}

// SubscriptionSpec mirrors Subscription but resolves the HMAC secret from an
// environment variable so the file itself can be committed to version control.
type SubscriptionSpec struct {
	Name           string        `json:"name"`
	Events         []string      `json:"events"`
	URL            string        `json:"url"`
	TimeoutSeconds int           `json:"timeout_seconds,omitempty"`
	FailurePolicy  FailurePolicy `json:"failure_policy,omitempty"`
	// SecretEnv names an environment variable that holds the HMAC secret.
	// Leave empty to disable signature generation for this subscription.
	SecretEnv string `json:"secret_env,omitempty"`
}

// ActionSubscriptionSpec mirrors ActionSubscription with the same secret_env
// indirection.
type ActionSubscriptionSpec struct {
	Name           string `json:"name"`
	URL            string `json:"url"`
	Description    string `json:"description,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	SecretEnv      string `json:"secret_env,omitempty"`
}

// LoadConfigFile reads path and materialises a Config + []ActionSubscription.
// Secret values are pulled from the environment variables named by SecretEnv.
// An empty path returns an empty Config and nil actions — the caller can then
// construct a no-op dispatcher without any subscribers.
func LoadConfigFile(path string) (Config, []ActionSubscription, error) {
	if path == "" {
		return Config{}, nil, nil
	}
	raw, err := os.ReadFile(path) //nolint:gosec // operator-controlled path
	if err != nil {
		return Config{}, nil, fmt.Errorf("read %s: %w", path, err)
	}
	var spec SubscriptionsFile
	if err := json.Unmarshal(raw, &spec); err != nil {
		return Config{}, nil, fmt.Errorf("parse %s: %w", path, err)
	}

	cfg := Config{Subscriptions: make([]Subscription, 0, len(spec.Subscriptions))}
	for _, s := range spec.Subscriptions {
		sub := Subscription{
			Name:           s.Name,
			Events:         s.Events,
			URL:            s.URL,
			TimeoutSeconds: s.TimeoutSeconds,
			FailurePolicy:  s.FailurePolicy,
		}
		if s.SecretEnv != "" {
			sub.Secret = os.Getenv(s.SecretEnv)
		}
		cfg.Subscriptions = append(cfg.Subscriptions, sub)
	}

	actions := make([]ActionSubscription, 0, len(spec.Actions))
	for _, a := range spec.Actions {
		act := ActionSubscription{
			Name:           a.Name,
			URL:            a.URL,
			Description:    a.Description,
			TimeoutSeconds: a.TimeoutSeconds,
		}
		if a.SecretEnv != "" {
			act.Secret = os.Getenv(a.SecretEnv)
		}
		actions = append(actions, act)
	}

	return cfg, actions, nil
}

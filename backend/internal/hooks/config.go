package hooks

import (
	"fmt"
	"net/url"
	"time"
)

const (
	defaultTimeout = 5 * time.Second
	maxTimeout     = 30 * time.Second
)

// Config holds dispatcher-wide settings and the registered subscriptions.
type Config struct {
	Subscriptions []Subscription
}

// Validate verifies subscription fields and normalizes optional values.
// Returns the first validation error encountered.
func (c *Config) Validate() error {
	seen := make(map[string]struct{}, len(c.Subscriptions))
	for i := range c.Subscriptions {
		s := &c.Subscriptions[i]
		if s.Name == "" {
			return fmt.Errorf("subscription[%d]: name is required", i)
		}
		if _, dup := seen[s.Name]; dup {
			return fmt.Errorf("subscription %q: duplicate name", s.Name)
		}
		seen[s.Name] = struct{}{}

		if len(s.Events) == 0 {
			return fmt.Errorf("subscription %q: at least one event required", s.Name)
		}
		for _, e := range s.Events {
			if !isKnownEvent(e) {
				return fmt.Errorf("subscription %q: unknown event %q", s.Name, e)
			}
		}

		if s.URL == "" {
			return fmt.Errorf("subscription %q: url is required", s.Name)
		}
		u, err := url.Parse(s.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("subscription %q: invalid url", s.Name)
		}

		if s.FailurePolicy == "" {
			s.FailurePolicy = FailurePolicyIgnore
		}
		if s.FailurePolicy != FailurePolicyFail && s.FailurePolicy != FailurePolicyIgnore {
			return fmt.Errorf("subscription %q: failure_policy must be fail or ignore", s.Name)
		}

		if s.TimeoutSeconds < 0 {
			return fmt.Errorf("subscription %q: timeout_seconds must be >= 0", s.Name)
		}
		if s.TimeoutSeconds == 0 {
			s.TimeoutSeconds = int(defaultTimeout.Seconds())
		}
		if time.Duration(s.TimeoutSeconds)*time.Second > maxTimeout {
			return fmt.Errorf("subscription %q: timeout_seconds must be <= %d", s.Name, int(maxTimeout.Seconds()))
		}
	}
	return nil
}

func isKnownEvent(e string) bool {
	switch e {
	case EventPreDeploy, EventPostDeploy,
		EventPreInstanceCreate, EventPostInstanceCreate,
		EventPreInstanceDelete, EventPostInstanceDelete,
		EventPreNamespaceCreate, EventPostNamespaceCreate,
		EventPreRollback, EventPostRollback,
		EventDeployFinalized,
		EventStopCompleted, EventCleanCompleted,
		EventRollbackCompleted, EventDeleteCompleted,
		EventInstanceCreated,
		EventDeployTimeout, EventCleanupPolicyExecuted,
		EventStackExpired, EventStackExpiring,
		EventQuotaWarning, EventSecretExpiring:
		return true
	}
	return false
}

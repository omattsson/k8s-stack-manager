package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Defaults and limits of the stream settings (port-forward, later exec).
const (
	DefaultPortForwardMaxDuration = time.Hour
	DefaultPortForwardIdleTimeout = 15 * time.Minute
	DefaultPortForwardMaxPerUser  = 5
	// MaxStreamDuration caps the maximum duration and the idle timeout.
	MaxStreamDuration = 24 * time.Hour
	// maxStreamPerUser is the upper bound of STACK_PORT_FORWARD_MAX_PER_USER.
	maxStreamPerUser = 100
)

// PortForwardConfig holds the settings of the port-forward tunnels.
type PortForwardConfig struct {
	// MaxDuration is the longest life of a tunnel
	// (env: STACK_PORT_FORWARD_MAX_DURATION, default 1h, > 0 and <= 24h).
	MaxDuration time.Duration
	// IdleTimeout closes a tunnel without data frames
	// (env: STACK_PORT_FORWARD_IDLE_TIMEOUT, default 15m, > 0, <= 24h and <= MaxDuration).
	IdleTimeout time.Duration
	// MaxPerUser is the number of open tunnels of a user in this replica
	// (env: STACK_PORT_FORWARD_MAX_PER_USER, default 5, 1..100).
	MaxPerUser int
	// Enabled turns the feature on (env: STACK_PORT_FORWARD_ENABLED, default false).
	Enabled bool
}

// StreamConfig holds the settings of the stream routes (port-forward now,
// exec in a later step).
type StreamConfig struct {
	// err is the first parse error of the environment variables. Validate
	// returns it, so an invalid value stops the start.
	err error
	// BackendNamespace is the namespace of the backend pod (env: POD_NAMESPACE,
	// else the service account namespace file). Empty outside a pod. The
	// namespace guard refuses this namespace.
	BackendNamespace string
	PortForward      PortForwardConfig
	// AllowAPIKeys lets API keys open streams (env: STACK_STREAM_ALLOW_API_KEYS, default false).
	AllowAPIKeys bool
}

// Validate checks the stream settings. A zero field (a config built in code)
// is not checked; the loader checks the values of the environment strictly.
func (c *StreamConfig) Validate() error {
	if c.err != nil {
		return c.err
	}
	return c.check(false)
}

func (c *StreamConfig) check(strict bool) error {
	p := c.PortForward
	if (strict || p.MaxDuration != 0) && (p.MaxDuration <= 0 || p.MaxDuration > MaxStreamDuration) {
		return errors.New("STACK_PORT_FORWARD_MAX_DURATION must be more than 0 and at most 24h")
	}
	if (strict || p.IdleTimeout != 0) && (p.IdleTimeout <= 0 || p.IdleTimeout > MaxStreamDuration) {
		return errors.New("STACK_PORT_FORWARD_IDLE_TIMEOUT must be more than 0 and at most 24h")
	}
	if p.MaxDuration > 0 && p.IdleTimeout > p.MaxDuration {
		return errors.New("STACK_PORT_FORWARD_IDLE_TIMEOUT must not be longer than STACK_PORT_FORWARD_MAX_DURATION")
	}
	if (strict || p.MaxPerUser != 0) && (p.MaxPerUser < 1 || p.MaxPerUser > maxStreamPerUser) {
		return fmt.Errorf("STACK_PORT_FORWARD_MAX_PER_USER must be between 1 and %d", maxStreamPerUser)
	}
	return nil
}

func loadStreamConfig() StreamConfig {
	var c StreamConfig
	setErr := func(err error) {
		if c.err == nil {
			c.err = err
		}
	}
	boolean := func(key string) bool {
		raw := strings.TrimSpace(os.Getenv(key))
		if raw == "" {
			return false
		}
		v, err := strconv.ParseBool(raw)
		if err != nil {
			setErr(fmt.Errorf("%s %q is not a boolean", key, raw))
			return false
		}
		return v
	}
	duration := func(key string, fallback time.Duration) time.Duration {
		raw := strings.TrimSpace(os.Getenv(key))
		if raw == "" {
			return fallback
		}
		v, err := time.ParseDuration(raw)
		if err != nil {
			setErr(fmt.Errorf("%s %q is not a duration (for example 1h)", key, raw))
			return fallback
		}
		return v
	}

	c.PortForward.Enabled = boolean("STACK_PORT_FORWARD_ENABLED")
	c.PortForward.MaxDuration = duration("STACK_PORT_FORWARD_MAX_DURATION", DefaultPortForwardMaxDuration)
	c.PortForward.IdleTimeout = duration("STACK_PORT_FORWARD_IDLE_TIMEOUT", DefaultPortForwardIdleTimeout)
	c.PortForward.MaxPerUser = DefaultPortForwardMaxPerUser
	if raw := strings.TrimSpace(os.Getenv("STACK_PORT_FORWARD_MAX_PER_USER")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			setErr(fmt.Errorf("STACK_PORT_FORWARD_MAX_PER_USER %q is not a whole number", raw))
		} else {
			c.PortForward.MaxPerUser = v
		}
	}
	c.AllowAPIKeys = boolean("STACK_STREAM_ALLOW_API_KEYS")

	c.BackendNamespace = strings.TrimSpace(os.Getenv("POD_NAMESPACE"))
	if c.BackendNamespace == "" {
		if b, err := os.ReadFile(serviceAccountNamespaceFile); err == nil {
			c.BackendNamespace = strings.TrimSpace(string(b))
		}
	}
	if c.err == nil {
		c.err = c.check(true)
	}
	return c
}

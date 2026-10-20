package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Default retention of the log tables, in days. 0 keeps the rows forever.
const (
	DefaultDeployLogRetentionDays    = 180
	DefaultAuditLogRetentionDays     = 365
	DefaultNotificationRetentionDays = 90
	DefaultDeliveryLogRetentionDays  = 30
	DefaultActionRunRetentionDays    = 180
	// DefaultLogRetentionInterval is the time between two retention runs.
	DefaultLogRetentionInterval = time.Hour
	// maxRetentionDays caps a retention setting (100 years) so that the age
	// in nanoseconds cannot overflow.
	maxRetentionDays = 36500
)

// RetentionConfig holds the retention of the log tables. The leader worker
// log-retention deletes older rows. A value of 0 keeps the rows forever.
type RetentionConfig struct {
	// err is the first parse error of the environment variables. Validate
	// returns it, so a value that is not a number stops the start.
	err error
	// Interval is the time between two runs (env: LOG_RETENTION_INTERVAL).
	// 0 means DefaultLogRetentionInterval.
	Interval time.Duration
	// DeployLogDays: deployment_logs (env: DEPLOY_LOG_RETENTION_DAYS).
	DeployLogDays int
	// AuditLogDays: audit_logs (env: AUDIT_LOG_RETENTION_DAYS).
	AuditLogDays int
	// NotificationDays: notifications (env: NOTIFICATION_RETENTION_DAYS).
	NotificationDays int
	// DeliveryLogDays: notification channel delivery logs
	// (env: DELIVERY_LOG_RETENTION_DAYS).
	DeliveryLogDays int
	// ActionRunDays: action_runs, the custom action history
	// (env: ACTION_RUN_RETENTION_DAYS).
	ActionRunDays int
}

// Enabled reports whether at least one table has a retention.
func (c *RetentionConfig) Enabled() bool {
	return c.DeployLogDays > 0 || c.AuditLogDays > 0 || c.NotificationDays > 0 || c.DeliveryLogDays > 0 ||
		c.ActionRunDays > 0
}

// Validate checks the retention settings.
func (c *RetentionConfig) Validate() error {
	if c.err != nil {
		return c.err
	}
	for _, s := range []struct {
		name string
		days int
	}{
		{"DEPLOY_LOG_RETENTION_DAYS", c.DeployLogDays},
		{"AUDIT_LOG_RETENTION_DAYS", c.AuditLogDays},
		{"NOTIFICATION_RETENTION_DAYS", c.NotificationDays},
		{"DELIVERY_LOG_RETENTION_DAYS", c.DeliveryLogDays},
		{"ACTION_RUN_RETENTION_DAYS", c.ActionRunDays},
	} {
		if s.days < 0 || s.days > maxRetentionDays {
			return fmt.Errorf("%s must be between 0 (keep forever) and %d", s.name, maxRetentionDays)
		}
	}
	// 0 (a config built in code) means the default interval.
	if c.Interval != 0 && c.Interval < time.Minute {
		return errors.New("LOG_RETENTION_INTERVAL must be at least 1m")
	}
	return nil
}

// loadRetentionConfig reads the retention settings. An empty variable gives
// the default. A value that is not valid is kept as an error for Validate.
func loadRetentionConfig() RetentionConfig {
	var c RetentionConfig
	days := func(key string, fallback int) int {
		raw := strings.TrimSpace(os.Getenv(key))
		if raw == "" {
			return fallback
		}
		v, err := strconv.Atoi(raw)
		if err != nil {
			if c.err == nil {
				c.err = fmt.Errorf("%s %q is not a whole number of days", key, raw)
			}
			return fallback
		}
		return v
	}
	c.DeployLogDays = days("DEPLOY_LOG_RETENTION_DAYS", DefaultDeployLogRetentionDays)
	c.AuditLogDays = days("AUDIT_LOG_RETENTION_DAYS", DefaultAuditLogRetentionDays)
	c.NotificationDays = days("NOTIFICATION_RETENTION_DAYS", DefaultNotificationRetentionDays)
	c.DeliveryLogDays = days("DELIVERY_LOG_RETENTION_DAYS", DefaultDeliveryLogRetentionDays)
	c.ActionRunDays = days("ACTION_RUN_RETENTION_DAYS", DefaultActionRunRetentionDays)

	c.Interval = DefaultLogRetentionInterval
	if raw := strings.TrimSpace(os.Getenv("LOG_RETENTION_INTERVAL")); raw != "" {
		v, err := time.ParseDuration(raw)
		switch {
		case err != nil:
			if c.err == nil {
				c.err = fmt.Errorf("LOG_RETENTION_INTERVAL %q is not a duration (for example 1h)", raw)
			}
		case v < time.Minute:
			// Also 0: Validate accepts 0 only from a config built in code.
			if c.err == nil {
				c.err = errors.New("LOG_RETENTION_INTERVAL must be at least 1m")
			}
		default:
			c.Interval = v
		}
	}
	return c
}

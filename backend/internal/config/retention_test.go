package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// retentionEnvKeys are the environment variables of RetentionConfig.
var retentionEnvKeys = []string{
	"DEPLOY_LOG_RETENTION_DAYS", "AUDIT_LOG_RETENTION_DAYS", "NOTIFICATION_RETENTION_DAYS",
	"DELIVERY_LOG_RETENTION_DAYS", "LOG_RETENTION_INTERVAL",
}

// No t.Parallel: the cases set environment variables (t.Setenv).
func TestLoadRetentionConfig(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    RetentionConfig
		wantErr string
	}{
		{
			name: "defaults",
			want: RetentionConfig{DeployLogDays: 180, AuditLogDays: 365, NotificationDays: 90, DeliveryLogDays: 30, Interval: time.Hour},
		},
		{
			name: "0 keeps the rows and a custom interval",
			env: map[string]string{
				"DEPLOY_LOG_RETENTION_DAYS": "0", "AUDIT_LOG_RETENTION_DAYS": " 730 ", "NOTIFICATION_RETENTION_DAYS": "0",
				"DELIVERY_LOG_RETENTION_DAYS": "7", "LOG_RETENTION_INTERVAL": "15m",
			},
			want: RetentionConfig{AuditLogDays: 730, DeliveryLogDays: 7, Interval: 15 * time.Minute},
		},
		{name: "not a number", env: map[string]string{"AUDIT_LOG_RETENTION_DAYS": "1y"}, wantErr: `AUDIT_LOG_RETENTION_DAYS "1y" is not a whole number of days`},
		{name: "negative", env: map[string]string{"NOTIFICATION_RETENTION_DAYS": "-1"}, wantErr: "NOTIFICATION_RETENTION_DAYS must be between 0 (keep forever) and 36500"},
		{name: "too large", env: map[string]string{"DEPLOY_LOG_RETENTION_DAYS": "36501"}, wantErr: "DEPLOY_LOG_RETENTION_DAYS must be between 0 (keep forever) and 36500"},
		{name: "interval not a duration", env: map[string]string{"LOG_RETENTION_INTERVAL": "1d"}, wantErr: `LOG_RETENTION_INTERVAL "1d" is not a duration (for example 1h)`},
		{name: "interval too short", env: map[string]string{"LOG_RETENTION_INTERVAL": "30s"}, wantErr: "LOG_RETENTION_INTERVAL must be at least 1m"},
		{name: "interval 0", env: map[string]string{"LOG_RETENTION_INTERVAL": "0"}, wantErr: "LOG_RETENTION_INTERVAL must be at least 1m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, k := range retentionEnvKeys {
				t.Setenv(k, "")
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			cfg := loadRetentionConfig()
			err := cfg.Validate()

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Equal(t, tt.wantErr, err.Error())
				return
			}
			require.NoError(t, err)
			cfg.err = nil
			assert.Equal(t, tt.want, cfg)
		})
	}
}

func TestRetentionConfig_Enabled(t *testing.T) {
	t.Parallel()
	assert.False(t, (&RetentionConfig{}).Enabled())
	assert.True(t, (&RetentionConfig{DeliveryLogDays: 1}).Enabled())
	// A config built in code (Interval 0) is valid.
	assert.NoError(t, (&RetentionConfig{}).Validate())
}

package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestLoadWSFanoutConfig(t *testing.T) {
	// Not parallel: subtests use t.Setenv.

	tests := []struct {
		env           map[string]string
		name          string
		wantInterval  time.Duration
		wantRetention time.Duration
		wantEnabled   bool
	}{
		{
			name:          "defaults: off",
			env:           map[string]string{},
			wantInterval:  500 * time.Millisecond,
			wantRetention: 5 * time.Minute,
		},
		{
			name: "enabled with custom timings",
			env: map[string]string{
				"WS_FANOUT_ENABLED":       "true",
				"WS_FANOUT_POLL_INTERVAL": "250ms",
				"WS_FANOUT_RETENTION":     "10m",
			},
			wantEnabled:   true,
			wantInterval:  250 * time.Millisecond,
			wantRetention: 10 * time.Minute,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			for _, k := range []string{"WS_FANOUT_ENABLED", "WS_FANOUT_POLL_INTERVAL", "WS_FANOUT_RETENTION"} {
				t.Setenv(k, tt.env[k])
			}
			c := loadWSFanoutConfig()
			assert.Equal(t, tt.wantEnabled, c.Enabled)
			assert.Equal(t, tt.wantInterval, c.PollInterval)
			assert.Equal(t, tt.wantRetention, c.Retention)
		})
	}
}

func TestWSFanoutConfigValidate(t *testing.T) {
	t.Parallel()

	valid := WSFanoutConfig{Enabled: true, PollInterval: 500 * time.Millisecond, Retention: 5 * time.Minute}
	tests := []struct {
		name    string
		mutate  func(*WSFanoutConfig)
		wantErr string
	}{
		{name: "valid", mutate: func(*WSFanoutConfig) {}},
		{name: "disabled ignores bad values", mutate: func(c *WSFanoutConfig) { c.Enabled = false; c.PollInterval = 0; c.Retention = 0 }},
		{name: "poll interval too short", mutate: func(c *WSFanoutConfig) { c.PollInterval = 10 * time.Millisecond }, wantErr: "WS_FANOUT_POLL_INTERVAL"},
		{name: "poll interval too long", mutate: func(c *WSFanoutConfig) { c.PollInterval = 2 * time.Minute }, wantErr: "WS_FANOUT_POLL_INTERVAL"},
		{name: "retention too short", mutate: func(c *WSFanoutConfig) { c.Retention = 30 * time.Second }, wantErr: "WS_FANOUT_RETENTION"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := valid
			tt.mutate(&c)
			err := c.Validate()
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

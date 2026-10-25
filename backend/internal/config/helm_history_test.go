package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseHelmHistoryMax(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		raw     string
		want    int
		wantErr bool
	}{
		{name: "empty gives no limit flag", raw: "", want: 0},
		{name: "blank gives no limit flag", raw: "  ", want: 0},
		{name: "two", raw: "2", want: 2},
		{name: "three with spaces", raw: " 3 ", want: 3},
		{name: "the Helm default", raw: "10", want: 10},
		{name: "one", raw: "1", wantErr: true},
		{name: "zero (no limit in Helm)", raw: "0", wantErr: true},
		{name: "negative", raw: "-5", wantErr: true},
		{name: "not a number", raw: "ten", wantErr: true},
		{name: "not a whole number", raw: "2.5", wantErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseHelmHistoryMax(tt.raw)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "HELM_HISTORY_MAX must be a whole number of 2 or more")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestLoadConfig_HelmHistoryMax uses t.Setenv, so it cannot run in parallel.
func TestLoadConfig_HelmHistoryMax(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		want    int
		wantErr bool
	}{
		{name: "not set: no limit flag", env: "", want: 0},
		{name: "set", env: "3", want: 3},
		{name: "one stops the start", env: "1", wantErr: true},
		{name: "zero stops the start", env: "0", wantErr: true},
		{name: "negative stops the start", env: "-1", wantErr: true},
		{name: "text stops the start", env: "abc", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ENV_FILE", "/nonexistent/.env")
			t.Setenv("HELM_HISTORY_MAX", tt.env)
			cfg, err := LoadConfig()
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "invalid configuration: HELM_HISTORY_MAX")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.Deployment.HelmHistoryMax)
		})
	}
}

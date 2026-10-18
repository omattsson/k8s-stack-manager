package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseMaxInstanceTTLDays(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		raw     string
		want    int
		wantErr bool
	}{
		{name: "empty gives the default", raw: "", want: DefaultMaxInstanceTTLDays},
		{name: "blank gives the default", raw: "  ", want: DefaultMaxInstanceTTLDays},
		{name: "one day", raw: "1", want: 1},
		{name: "seven days with spaces", raw: " 7 ", want: 7},
		{name: "one year", raw: "365", want: 365},
		{name: "zero", raw: "0", wantErr: true},
		{name: "negative", raw: "-1", wantErr: true},
		{name: "above one year", raw: "366", wantErr: true},
		{name: "not a number", raw: "abc", wantErr: true},
		{name: "not a whole number", raw: "1.5", wantErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseMaxInstanceTTLDays(tt.raw)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "MAX_INSTANCE_TTL_DAYS must be a whole number from 1 to 365")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestAppConfig_MaxInstanceTTLDays uses t.Setenv, so it cannot run in parallel.
func TestAppConfig_MaxInstanceTTLDays(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		want    int
		wantErr bool
	}{
		{name: "not set: 30 days", env: "", want: 30},
		{name: "set", env: "7", want: 7},
		{name: "invalid stops the start", env: "abc", wantErr: true},
		{name: "zero stops the start", env: "0", wantErr: true},
		{name: "366 stops the start", env: "366", wantErr: true},
	}
	t.Run("a default TTL above the limit stops the start", func(t *testing.T) {
		t.Setenv("MAX_INSTANCE_TTL_DAYS", "1")
		t.Setenv("DEFAULT_INSTANCE_TTL_MINUTES", "2880")
		app := loadAppConfig()
		err := app.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "DEFAULT_INSTANCE_TTL_MINUTES (2880) must not exceed MAX_INSTANCE_TTL_DAYS (1 days, 1440 minutes)")
	})
	t.Run("a default TTL at the limit is valid", func(t *testing.T) {
		t.Setenv("MAX_INSTANCE_TTL_DAYS", "1")
		t.Setenv("DEFAULT_INSTANCE_TTL_MINUTES", "1440")
		app := loadAppConfig()
		require.NoError(t, app.Validate())
	})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("MAX_INSTANCE_TTL_DAYS", tt.env)
			app := loadAppConfig()
			err := app.Validate()
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "MAX_INSTANCE_TTL_DAYS")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, app.MaxInstanceTTLDays)
		})
	}
}

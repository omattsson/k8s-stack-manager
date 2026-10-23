package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// No t.Parallel: the cases set environment variables (t.Setenv).
func TestLoadServerConfig_PprofProfiles(t *testing.T) {
	tests := []struct {
		name         string
		env          map[string]string
		wantMutex    int
		wantBlock    int
		wantErrParts []string
	}{
		{name: "default", env: map[string]string{"PPROF_ENABLED": "true"}},
		{name: "valid", env: map[string]string{"PPROF_ENABLED": "true", "PPROF_MUTEX_FRACTION": "5", "PPROF_BLOCK_RATE": " 1000 "}, wantMutex: 5, wantBlock: 1000},
		{name: "zero", env: map[string]string{"PPROF_ENABLED": "true", "PPROF_MUTEX_FRACTION": "0", "PPROF_BLOCK_RATE": "0"}},
		{name: "negative mutex", env: map[string]string{"PPROF_ENABLED": "true", "PPROF_MUTEX_FRACTION": "-1"}, wantErrParts: []string{"PPROF_MUTEX_FRACTION"}},
		{name: "negative block", env: map[string]string{"PPROF_ENABLED": "true", "PPROF_BLOCK_RATE": "-5"}, wantErrParts: []string{"PPROF_BLOCK_RATE"}},
		{name: "garbage", env: map[string]string{"PPROF_ENABLED": "true", "PPROF_MUTEX_FRACTION": "abc", "PPROF_BLOCK_RATE": "1.5"}, wantErrParts: []string{"PPROF_MUTEX_FRACTION", "PPROF_BLOCK_RATE"}},
		{name: "ignored when pprof is off", env: map[string]string{"PPROF_MUTEX_FRACTION": "abc", "PPROF_BLOCK_RATE": "-1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, k := range []string{"PPROF_ENABLED", "PPROF_MUTEX_FRACTION", "PPROF_BLOCK_RATE"} {
				t.Setenv(k, "")
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			cfg := loadServerConfig()
			err := cfg.Validate()

			if len(tt.wantErrParts) > 0 {
				require.Error(t, err)
				for _, part := range tt.wantErrParts {
					assert.Contains(t, err.Error(), part)
				}
				return
			}
			// Other server fields may fail Validate; only the pprof error matters here.
			assert.NoError(t, cfg.pprofErr)
			assert.Equal(t, tt.wantMutex, cfg.PprofMutexFraction)
			assert.Equal(t, tt.wantBlock, cfg.PprofBlockRate)
		})
	}
}

//go:build !windows

package deployer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHelmHealthCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		binary     string
		wantErr    bool
		errContain string
	}{
		{
			name:   "valid binary succeeds",
			binary: "true", // /usr/bin/true — always exits 0
		},
		{
			name:       "invalid binary path fails",
			binary:     "/nonexistent/helm-binary-xyz",
			wantErr:    true,
			errContain: "helm binary not available",
		},
		{
			name:       "non-zero exit code fails",
			binary:     "false", // /usr/bin/false — always exits 1
			wantErr:    true,
			errContain: "helm binary not available",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			check := HelmHealthCheck(tt.binary)
			err := check(context.Background())

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContain)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestHelmHealthCheck_CancelledContext(t *testing.T) {
	t.Parallel()

	check := HelmHealthCheck("true") // valid binary, but context is cancelled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := check(ctx)
	require.Error(t, err, "cancelled context should cause exec to fail")
}

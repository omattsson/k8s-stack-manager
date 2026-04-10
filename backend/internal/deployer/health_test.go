//go:build !windows

package deployer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHelmHealthCheck(t *testing.T) {
	t.Parallel()

	// Create a helper script that validates it receives "version --short" args.
	helperScript := filepath.Join(t.TempDir(), "helm-test")
	err := os.WriteFile(helperScript, []byte("#!/bin/sh\n"+
		"if [ \"$1\" = \"version\" ] && [ \"$2\" = \"--short\" ]; then\n"+
		"  echo \"v3.14.0\"\n"+
		"  exit 0\n"+
		"fi\n"+
		"echo \"unexpected args: $@\" >&2\n"+
		"exit 1\n"), 0o755)
	require.NoError(t, err)

	// Also create a script that always fails.
	failScript := filepath.Join(t.TempDir(), "helm-fail")
	err = os.WriteFile(failScript, []byte("#!/bin/sh\nexit 1\n"), 0o755)
	require.NoError(t, err)

	tests := []struct {
		name       string
		binary     string
		wantErr    bool
		errContain string
	}{
		{
			name:   "valid binary with correct args succeeds",
			binary: helperScript,
		},
		{
			name:       "invalid binary path fails",
			binary:     "/nonexistent/helm-binary-xyz",
			wantErr:    true,
			errContain: "helm binary not available",
		},
		{
			name:       "non-zero exit code fails",
			binary:     failScript,
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

	// Use the helper script for consistency.
	helperScript := filepath.Join(t.TempDir(), "helm-test")
	err := os.WriteFile(helperScript, []byte("#!/bin/sh\n"+
		"if [ \"$1\" = \"version\" ] && [ \"$2\" = \"--short\" ]; then\n"+
		"  echo \"v3.14.0\"\n"+
		"  exit 0\n"+
		"fi\n"+
		"echo \"unexpected args: $@\" >&2\n"+
		"exit 1\n"), 0o755)
	require.NoError(t, err)

	check := HelmHealthCheck(helperScript)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = check(ctx)
	require.Error(t, err, "cancelled context should cause exec to fail")
}

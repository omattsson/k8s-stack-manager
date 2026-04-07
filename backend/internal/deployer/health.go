package deployer

import (
	"context"
	"fmt"
	"os/exec"

	"backend/internal/health"
)

// HelmHealthCheck returns a health check function that verifies the helm binary
// is available and executable by running "helm version --short".
func HelmHealthCheck(helmBinary string) health.HealthCheck {
	return func(ctx context.Context) error {
		cmd := exec.CommandContext(ctx, helmBinary, "version", "--short")
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("helm binary %q: %w (output: %s)", helmBinary, err, string(output))
		}
		return nil
	}
}

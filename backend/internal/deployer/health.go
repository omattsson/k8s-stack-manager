package deployer

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"

	"backend/internal/health"
)

// HelmHealthCheck returns a health check function that verifies the helm binary
// is available and executable by running "helm version --short".
func HelmHealthCheck(helmBinary string) health.HealthCheck {
	return func(ctx context.Context) error {
		cmd := exec.CommandContext(ctx, helmBinary, "version", "--short")
		if output, err := cmd.CombinedOutput(); err != nil {
			slog.Error("helm health check failed", "binary", helmBinary, "output", string(output), "error", err)
			return fmt.Errorf("helm binary not available")
		}
		return nil
	}
}

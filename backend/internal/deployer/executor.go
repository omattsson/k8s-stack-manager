package deployer

import (
	"context"
	"time"
)

// HelmExecutor abstracts helm operations for testability. The concrete
// implementation is *HelmClient; tests can substitute a mock.
type HelmExecutor interface {
	Install(ctx context.Context, req InstallRequest) (string, error)
	Uninstall(ctx context.Context, req UninstallRequest) (string, error)
	Status(ctx context.Context, releaseName, namespace string) (*ReleaseStatus, error)
	Timeout() time.Duration
}

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
	ListReleases(ctx context.Context, namespace string) ([]string, error)
	History(ctx context.Context, releaseName, namespace string, max int) ([]ReleaseRevision, error)
	Rollback(ctx context.Context, releaseName, namespace string, revision int) (string, error)
	GetValues(ctx context.Context, releaseName, namespace string, revision int) (string, error)
	Timeout() time.Duration
}

// ReleaseRevision represents a single entry from helm history.
type ReleaseRevision struct {
	Revision    int    `json:"revision"`
	Updated     string `json:"updated"`
	Status      string `json:"status"`
	Chart       string `json:"chart"`
	AppVersion  string `json:"app_version"`
	Description string `json:"description"`
}

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
	RegistryLogin(ctx context.Context, host, username, password string) error
	Timeout() time.Duration
}

// StreamingHelmExecutor extends HelmExecutor with line-by-line output streaming.
// When the underlying executor supports streaming, the Manager wraps it via
// WithLineHandler so each line of Helm CLI output is broadcast over WebSocket
// in real time rather than waiting for the command to finish.
type StreamingHelmExecutor interface {
	HelmExecutor
	WithLineHandler(fn func(string)) HelmExecutor
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

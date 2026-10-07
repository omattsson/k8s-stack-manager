// Package gitprovider implements Git provider integrations for Azure DevOps and GitLab.
// It provides branch listing, validation, and caching via a provider registry that
// auto-detects the provider from repository URLs.
package gitprovider

import "context"

// GitProvider defines the interface for interacting with a Git hosting provider.
type GitProvider interface {
	ListBranches(ctx context.Context, repoURL string) ([]Branch, error)
	GetDefaultBranch(ctx context.Context, repoURL string) (string, error)
	ValidateBranch(ctx context.Context, repoURL string, branch string) (bool, error)
	ProviderType() string
}

// Branch represents a Git branch.
type Branch struct {
	Name      string `json:"name"`
	IsDefault bool   `json:"is_default"`
}

// ProviderStatus describes the availability of a configured provider.
type ProviderStatus struct {
	Type      string `json:"type"`
	Available bool   `json:"available"`
}

// Config holds configuration for all Git providers.
type Config struct {
	AzureDevOps AzureDevOpsConfig
	GitLab      GitLabConfig
}

// AzureDevOpsConfig holds Azure DevOps provider configuration. Set PAT for
// personal-access-token auth, or TokenSource for an Entra ID bearer token
// (for example workload identity). PAT wins when both are set.
type AzureDevOpsConfig struct {
	PAT         string
	DefaultOrg  string
	TokenSource TokenSource
}

// TokenSource returns a bearer token for Azure DevOps. Implementations must
// cache tokens; the provider calls it for every request.
type TokenSource func(ctx context.Context) (string, error)

// GitLabConfig holds GitLab provider configuration.
type GitLabConfig struct {
	Token   string
	BaseURL string
}

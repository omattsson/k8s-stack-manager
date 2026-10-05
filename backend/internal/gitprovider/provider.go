// Package gitprovider implements Git provider integrations for Azure DevOps, GitHub, and GitLab.
// It provides branch listing, validation, and caching via a provider registry that
// auto-detects the provider from repository URLs.
package gitprovider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrInvalidRepositoryURL identifies a malformed or invalid repository URL.
	ErrInvalidRepositoryURL = errors.New("invalid repository URL")
	// ErrUnsupportedProvider identifies a repository hosted by an unsupported or unconfigured provider.
	ErrUnsupportedProvider = errors.New("unsupported Git provider URL")
	// ErrAuthentication identifies provider authentication or permission failures.
	ErrAuthentication = errors.New("Git provider authentication or permission denied")
	// ErrRepositoryNotFound identifies a repository that the provider did not find.
	ErrRepositoryNotFound = errors.New("Git repository not found")
	// ErrRateLimited identifies provider rate limiting.
	ErrRateLimited = errors.New("Git provider rate limit exceeded")
	// ErrRateLimit is retained as a convenient alias for ErrRateLimited.
	ErrRateLimit = ErrRateLimited
	// ErrRepositoryNotAllowed identifies a repository rejected by the configured allowlist.
	ErrRepositoryNotAllowed = errors.New("Git repository is not allowed")
	// ErrUpstreamFailure identifies an unexpected provider or transport failure.
	ErrUpstreamFailure = errors.New("Git provider upstream failure")
)

// ProviderError classifies a sanitized provider failure while preserving its cause.
type ProviderError struct {
	Kind     error
	Cause    error
	Provider string
	Message  string
}

func (e *ProviderError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// Unwrap exposes both the stable category and an optional underlying cause.
func (e *ProviderError) Unwrap() []error {
	if e == nil {
		return nil
	}
	if e.Cause == nil {
		return []error{e.Kind}
	}
	return []error{e.Kind, e.Cause}
}

// RateLimitError reports a sanitized rate-limit failure and an optional retry time.
type RateLimitError struct {
	RetryAfter time.Time
	Provider   string
	Message    string
}

func (e *RateLimitError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// Unwrap classifies every RateLimitError as ErrRateLimited.
func (e *RateLimitError) Unwrap() error {
	return ErrRateLimited
}

func providerError(kind error, provider, message string, cause error) error {
	return &ProviderError{Kind: kind, Cause: cause, Provider: provider, Message: message}
}

func rateLimitError(provider, message string, retryAfter time.Time) error {
	return &RateLimitError{Provider: provider, Message: message, RetryAfter: retryAfter}
}

func parseRetryAfter(rawValue string, now time.Time) time.Time {
	rawValue = strings.TrimSpace(rawValue)
	if seconds, err := strconv.ParseInt(rawValue, 10, 32); err == nil && seconds >= 0 {
		return now.Add(time.Duration(seconds) * time.Second)
	}
	if retryAt, err := http.ParseTime(rawValue); err == nil {
		return retryAt.UTC()
	}
	return time.Time{}
}

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
	GitHub      GitHubConfig
	GitLab      GitLabConfig
}

// AzureDevOpsConfig holds Azure DevOps provider configuration.
type AzureDevOpsConfig struct {
	PAT        string
	DefaultOrg string
}

// GitHubConfig holds GitHub provider configuration.
type GitHubConfig struct {
	Token               string
	AllowedRepositories []string
}

// Validate rejects unsafe GitHub token and allowlist configurations without reflecting their contents.
func (c GitHubConfig) Validate() error {
	allowedRepositories, err := normalizeGitHubAllowedRepositories(c.AllowedRepositories)
	if err != nil {
		return err
	}
	if c.Token != "" && len(allowedRepositories) == 0 {
		return providerError(ErrRepositoryNotAllowed, "github", "GitHub token requires at least one allowed repository", nil)
	}
	return nil
}

// Validate checks provider-layer configuration.
func (c Config) Validate() error {
	if err := c.GitHub.Validate(); err != nil {
		return fmt.Errorf("GitHub configuration: %w", err)
	}
	return nil
}

// GitLabConfig holds GitLab provider configuration.
type GitLabConfig struct {
	Token   string
	BaseURL string
}

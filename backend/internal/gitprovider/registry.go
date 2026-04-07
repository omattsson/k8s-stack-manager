package gitprovider

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const cacheTTL = 5 * time.Minute

type cacheEntry struct {
	branches  []Branch
	expiresAt time.Time
}

// Registry routes Git operations to the correct provider based on URL detection.
type Registry struct {
	azureDevOps      *azureDevOpsProvider
	gitlab           *gitlabProvider
	gitlabCustomHost string
	mu               sync.RWMutex
	cache            map[string]cacheEntry
	nowFunc          func() time.Time
}

// NewRegistry creates a new provider registry from configuration.
func NewRegistry(cfg Config) *Registry {
	r := &Registry{
		cache:   make(map[string]cacheEntry),
		nowFunc: time.Now,
	}

	if cfg.AzureDevOps.PAT != "" {
		r.azureDevOps = newAzureDevOpsProvider(cfg.AzureDevOps)
	}

	if cfg.GitLab.Token != "" {
		r.gitlab = newGitLabProvider(cfg.GitLab)
		baseURL := cfg.GitLab.BaseURL
		if baseURL == "" {
			baseURL = "https://gitlab.com"
		}
		host := strings.TrimPrefix(baseURL, "https://")
		host = strings.TrimPrefix(host, "http://")
		host = strings.TrimSuffix(host, "/")
		if host != "gitlab.com" {
			r.gitlabCustomHost = host
		}
	}

	return r
}

func (r *Registry) detectProvider(repoURL string) (GitProvider, error) {
	normalized := strings.ToLower(repoURL)

	if strings.Contains(normalized, "dev.azure.com") ||
		strings.Contains(normalized, "visualstudio.com") {
		if r.azureDevOps == nil {
			return nil, fmt.Errorf("Azure DevOps provider is not configured (PAT not set)")
		}
		return r.azureDevOps, nil
	}

	if r.gitlabCustomHost != "" && strings.Contains(normalized, strings.ToLower(r.gitlabCustomHost)) {
		if r.gitlab == nil {
			return nil, fmt.Errorf("GitLab provider is not configured (token not set)")
		}
		return r.gitlab, nil
	}

	if strings.Contains(normalized, "gitlab.com") {
		if r.gitlab == nil {
			return nil, fmt.Errorf("GitLab provider is not configured (token not set)")
		}
		return r.gitlab, nil
	}

	return nil, fmt.Errorf("unsupported Git provider for URL: %s", repoURL)
}

// ListBranches detects the provider, checks the cache, and returns branches.
func (r *Registry) ListBranches(ctx context.Context, repoURL string) ([]Branch, error) {
	provider, err := r.detectProvider(repoURL)
	if err != nil {
		return nil, err
	}

	key := normalizeCacheKey(repoURL)

	r.mu.RLock()
	entry, found := r.cache[key]
	r.mu.RUnlock()

	if found && r.nowFunc().Before(entry.expiresAt) {
		return entry.branches, nil
	}

	branches, err := provider.ListBranches(ctx, repoURL)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	r.cache[key] = cacheEntry{
		branches:  branches,
		expiresAt: r.nowFunc().Add(cacheTTL),
	}
	r.mu.Unlock()

	return branches, nil
}

// GetDefaultBranch detects the provider and returns the default branch.
func (r *Registry) GetDefaultBranch(ctx context.Context, repoURL string) (string, error) {
	provider, err := r.detectProvider(repoURL)
	if err != nil {
		return "", err
	}
	return provider.GetDefaultBranch(ctx, repoURL)
}

// ValidateBranch checks whether the branch exists using the cached branch list.
func (r *Registry) ValidateBranch(ctx context.Context, repoURL string, branch string) (bool, error) {
	branches, err := r.ListBranches(ctx, repoURL)
	if err != nil {
		return false, err
	}
	for _, b := range branches {
		if b.Name == branch {
			return true, nil
		}
	}
	return false, nil
}

// GetProviderStatus returns the availability status of all configured providers.
func (r *Registry) GetProviderStatus() []ProviderStatus {
	return []ProviderStatus{
		{Type: "azure_devops", Available: r.azureDevOps != nil},
		{Type: "gitlab", Available: r.gitlab != nil},
	}
}

// HealthCheck verifies that at least one configured Git provider is reachable.
// Returns nil if no providers are configured (valid for fresh installs) or if
// at least one provider responds with HTTP 2xx. Returns an error only when all
// configured providers are unreachable.
func (r *Registry) HealthCheck(ctx context.Context) error {
	// Copy provider references — no need to hold a lock during I/O.
	azDo := r.azureDevOps
	gl := r.gitlab

	if azDo == nil && gl == nil {
		return nil
	}

	var lastErr error

	if azDo != nil {
		if err := r.pingAzureDevOps(ctx, azDo); err != nil {
			lastErr = err
		} else {
			return nil
		}
	}

	if gl != nil {
		if err := r.pingGitLab(ctx, gl); err != nil {
			lastErr = err
		} else {
			return nil
		}
	}

	return fmt.Errorf("all configured git providers are unreachable: %w", lastErr)
}

func (r *Registry) pingAzureDevOps(ctx context.Context, p *azureDevOpsProvider) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://dev.azure.com/_apis/connectionData", nil)
	if err != nil {
		return fmt.Errorf("azure devops: create request: %w", err)
	}
	auth := base64.StdEncoding.EncodeToString([]byte(":" + p.pat))
	req.Header.Set("Authorization", "Basic "+auth)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("azure devops: request failed: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("azure devops: unexpected status %d", resp.StatusCode)
	}
	return nil
}

func (r *Registry) pingGitLab(ctx context.Context, p *gitlabProvider) error {
	apiURL := p.baseURL + "/api/v4/version"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return fmt.Errorf("gitlab: create request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", p.token)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("gitlab: request failed: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("gitlab: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// InvalidateCache removes the cached branch list for the given repository URL.
func (r *Registry) InvalidateCache(repoURL string) {
	key := normalizeCacheKey(repoURL)
	r.mu.Lock()
	delete(r.cache, key)
	r.mu.Unlock()
}

func normalizeCacheKey(repoURL string) string {
	key := strings.TrimSpace(repoURL)
	key = strings.ToLower(key)
	key = strings.TrimSuffix(key, "/")
	key = strings.TrimSuffix(key, ".git")
	return key
}

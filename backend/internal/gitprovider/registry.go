package gitprovider

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	cacheTTL                  = 5 * time.Minute
	maxBranchCacheEntries     = 128
	maxBranchCacheWeight      = 1 << 20 // 1 MiB, comfortably below the backend's 1 GiB memory limit.
	branchCacheEntryOverhead  = 256
	branchCacheBranchOverhead = 96
)

type cacheEntry struct {
	branches  []Branch
	expiresAt time.Time
	storedAt  time.Time
	weight    int
}

type branchFill struct {
	done      chan struct{}
	cancel    context.CancelFunc
	branches  []Branch
	err       error
	waiters   int
	abandoned bool
}

// Registry routes Git operations to the correct provider based on URL detection.
type Registry struct {
	azureDevOps      *azureDevOpsProvider
	github           *githubProvider
	gitlab           *gitlabProvider
	gitlabCustomHost string
	mu               sync.RWMutex
	cache            map[string]cacheEntry
	nowFunc          func() time.Time
	cacheCapacity    int
	cacheWeightLimit int
	fillMu           sync.Mutex
	inflight         map[string]*branchFill
}

// NewRegistry creates a new provider registry from validated configuration.
func NewRegistry(cfg Config) (*Registry, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid Git provider configuration: %w", err)
	}

	r := &Registry{
		cache:            make(map[string]cacheEntry),
		nowFunc:          time.Now,
		cacheCapacity:    maxBranchCacheEntries,
		cacheWeightLimit: maxBranchCacheWeight,
		inflight:         make(map[string]*branchFill),
		github:           newGitHubProvider(cfg.GitHub),
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

	return r, nil
}

func (r *Registry) detectProvider(repoURL string) (GitProvider, error) {
	normalized := strings.ToLower(repoURL)
	if isGitHubURLCandidate(repoURL) {
		if _, err := parseGitHubURL(repoURL); err != nil {
			return nil, err
		}
		return r.github, nil
	}

	if strings.Contains(normalized, "dev.azure.com") ||
		strings.Contains(normalized, "visualstudio.com") {
		if r.azureDevOps == nil {
			return nil, providerError(ErrUnsupportedProvider, "azure_devops", "Azure DevOps provider is not configured (PAT not set)", nil)
		}
		return r.azureDevOps, nil
	}

	if r.gitlabCustomHost != "" && strings.Contains(normalized, strings.ToLower(r.gitlabCustomHost)) {
		if r.gitlab == nil {
			return nil, providerError(ErrUnsupportedProvider, "gitlab", "GitLab provider is not configured (token not set)", nil)
		}
		return r.gitlab, nil
	}

	if strings.Contains(normalized, "gitlab.com") {
		if r.gitlab == nil {
			return nil, providerError(ErrUnsupportedProvider, "gitlab", "GitLab provider is not configured (token not set)", nil)
		}
		return r.gitlab, nil
	}

	return nil, providerError(ErrUnsupportedProvider, "", "unsupported Git provider URL", nil)
}

// ListBranches detects the provider, checks the cache, and returns branches.
func (r *Registry) ListBranches(ctx context.Context, repoURL string) ([]Branch, error) {
	start := time.Now()
	provider, err := r.detectProvider(repoURL)
	if err != nil {
		recordBranchListResult("unknown", "failure", time.Since(start))
		return nil, err
	}
	if err := authorizeRepository(provider, repoURL); err != nil {
		recordBranchListResult(providerTypeName(provider), "failure", time.Since(start))
		return nil, err
	}

	key := normalizeCacheKey(repoURL)

	r.mu.RLock()
	entry, found := r.cache[key]
	r.mu.RUnlock()

	if found && r.nowFunc().Before(entry.expiresAt) {
		recordBranchListResult(providerTypeName(provider), "success", time.Since(start))
		return cloneBranches(entry.branches), nil
	}

	fill := r.joinBranchFill(ctx, key, provider, repoURL)
	var branches []Branch
	select {
	case <-fill.done:
		branches, err = cloneBranches(fill.branches), fill.err
	case <-ctx.Done():
		r.leaveBranchFill(key, fill)
		err = ctx.Err()
	}
	if err != nil {
		recordBranchListResult(providerTypeName(provider), "failure", time.Since(start))
		return nil, err
	}

	recordBranchListResult(providerTypeName(provider), "success", time.Since(start))
	return branches, nil
}

func authorizeRepository(provider GitProvider, repoURL string) error {
	github, ok := provider.(*githubProvider)
	if !ok {
		return nil
	}
	_, err := github.authorizeRepository(repoURL)
	return err
}

func (r *Registry) joinBranchFill(ctx context.Context, key string, provider GitProvider, repoURL string) *branchFill {
	r.fillMu.Lock()
	defer r.fillMu.Unlock()
	if r.inflight == nil {
		r.inflight = make(map[string]*branchFill)
	}
	if fill, found := r.inflight[key]; found {
		fill.waiters++
		return fill
	}

	fillContext, cancel := context.WithCancel(context.WithoutCancel(ctx))
	fill := &branchFill{done: make(chan struct{}), cancel: cancel, waiters: 1}
	r.inflight[key] = fill
	go r.runBranchFill(fillContext, key, repoURL, provider, fill)
	return fill
}

func (r *Registry) runBranchFill(ctx context.Context, key, repoURL string, provider GitProvider, fill *branchFill) {
	branches, err := provider.ListBranches(ctx, repoURL)

	r.fillMu.Lock()
	if !fill.abandoned && err == nil {
		r.storeCacheEntry(key, branches)
	}
	fill.branches = cloneBranches(branches)
	fill.err = err
	if current := r.inflight[key]; current == fill {
		delete(r.inflight, key)
	}
	close(fill.done)
	r.fillMu.Unlock()
	fill.cancel()
}

func (r *Registry) leaveBranchFill(key string, fill *branchFill) {
	r.fillMu.Lock()
	defer r.fillMu.Unlock()
	if current := r.inflight[key]; current != fill {
		return
	}
	fill.waiters--
	if fill.waiters > 0 {
		return
	}
	fill.abandoned = true
	delete(r.inflight, key)
	fill.cancel()
}

func (r *Registry) storeCacheEntry(key string, branches []Branch) {
	now := r.nowFunc()
	capacity := configuredCacheLimit(r.cacheCapacity, maxBranchCacheEntries)
	weightLimit := configuredCacheLimit(r.cacheWeightLimit, maxBranchCacheWeight)
	weight := branchCacheWeight(key, branches)
	if weight > weightLimit {
		return
	}
	retainedBranches := cloneBranches(branches)

	r.mu.Lock()
	defer r.mu.Unlock()

	r.evictExpiredCacheEntries(now)
	delete(r.cache, key)
	r.evictCacheEntriesUntilFit(capacity, weightLimit, weight)
	r.cache[key] = cacheEntry{branches: retainedBranches, expiresAt: now.Add(cacheTTL), storedAt: now, weight: weight}
}

func configuredCacheLimit(configured, fallback int) int {
	if configured > 0 {
		return configured
	}
	return fallback
}

func (r *Registry) evictExpiredCacheEntries(now time.Time) {
	for key, entry := range r.cache {
		if !now.Before(entry.expiresAt) {
			delete(r.cache, key)
		}
	}
}

func (r *Registry) evictCacheEntriesUntilFit(capacity, weightLimit, additionalWeight int) {
	for len(r.cache) >= capacity || retainedCacheWeight(r.cache)+additionalWeight > weightLimit {
		oldestKey := oldestCacheEntryKey(r.cache)
		if oldestKey == "" {
			return
		}
		delete(r.cache, oldestKey)
	}
}

func oldestCacheEntryKey(cache map[string]cacheEntry) string {
	var oldestKey string
	var oldestTime time.Time
	for key, entry := range cache {
		entryTime := entry.storedAt
		if entryTime.IsZero() {
			entryTime = entry.expiresAt
		}
		if oldestKey == "" || entryTime.Before(oldestTime) {
			oldestKey = key
			oldestTime = entryTime
		}
	}
	return oldestKey
}

func branchCacheWeight(key string, branches []Branch) int {
	weight := branchCacheEntryOverhead + len(key)
	for _, branch := range branches {
		weight += branchCacheBranchOverhead + len(branch.Name)
	}
	return weight
}

func retainedCacheWeight(cache map[string]cacheEntry) int {
	weight := 0
	for key, entry := range cache {
		if entry.weight > 0 {
			weight += entry.weight
			continue
		}
		weight += branchCacheWeight(key, entry.branches)
	}
	return weight
}

func cloneBranches(branches []Branch) []Branch {
	if branches == nil {
		return nil
	}
	return append([]Branch(nil), branches...)
}

// providerTypeName returns the metric label for a provider. It delegates to
// the GitProvider.ProviderType() contract so wrappers and future
// implementations keep an accurate label instead of falling through to
// "unknown".
func providerTypeName(p GitProvider) string {
	if p == nil {
		return "unknown"
	}
	if t := p.ProviderType(); t != "" {
		return t
	}
	return "unknown"
}

// GetDefaultBranch detects the provider and returns the default branch.
func (r *Registry) GetDefaultBranch(ctx context.Context, repoURL string) (string, error) {
	provider, err := r.detectProvider(repoURL)
	if err != nil {
		return "", err
	}
	if err := authorizeRepository(provider, repoURL); err != nil {
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
		{Type: "github", Available: r.github != nil},
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
	gh := r.github
	gl := r.gitlab

	if !hasHealthCheckProvider(azDo, gh, gl) {
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

	if hasGitHubToken(gh) {
		if err := r.pingGitHub(ctx, gh); err != nil {
			lastErr = err
		} else {
			return nil
		}
	}

	slog.Warn("all configured git providers unreachable", "last_error", lastErr)
	return fmt.Errorf("all configured git providers are unreachable")
}

func hasHealthCheckProvider(azDo *azureDevOpsProvider, gh *githubProvider, gl *gitlabProvider) bool {
	return azDo != nil || gl != nil || hasGitHubToken(gh)
}

func hasGitHubToken(provider *githubProvider) bool {
	return provider != nil && provider.token != ""
}

func (r *Registry) pingAzureDevOps(ctx context.Context, p *azureDevOpsProvider) error {
	pingURL := "https://dev.azure.com/_apis/connectionData"
	if p.defaultOrg != "" {
		pingURL = fmt.Sprintf("https://dev.azure.com/%s/_apis/connectionData", p.defaultOrg)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pingURL, nil)
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

func (r *Registry) pingGitHub(ctx context.Context, p *githubProvider) error {
	resp, err := p.get(ctx, p.baseURL+"/user")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.CopyN(io.Discard, resp.Body, githubMaxResponseBytes+1)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("github: unexpected status %d", resp.StatusCode)
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

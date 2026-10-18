package gitprovider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
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
	// gitlabCustomPort is the effective port (explicit or scheme default) of
	// the configured custom GitLab base URL. Empty when gitlab.com is used.
	gitlabCustomPort string
	// afterCacheMiss is a test seam invoked by ListBranches between the
	// initial cache miss and joining a branch fill. Always nil in production.
	afterCacheMiss   func(key string)
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
	return newRegistryWithLogger(cfg, slog.Default())
}

func newRegistryWithLogger(cfg Config, logger *slog.Logger) (*Registry, error) {
	gitLabBaseURL, err := NormalizeGitLabBaseURL(cfg.GitLab.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid Git provider configuration: %w", err)
	}
	cfg.GitLab.BaseURL = gitLabBaseURL
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

	if cfg.AzureDevOps.PAT != "" || cfg.AzureDevOps.TokenSource != nil {
		r.azureDevOps = newAzureDevOpsProvider(cfg.AzureDevOps)
	}

	baseOrigin, _ := parseRepositoryOrigin(cfg.GitLab.BaseURL)
	if baseOrigin.host != "gitlab.com" {
		r.gitlabCustomHost = baseOrigin.host
		r.gitlabCustomPort = baseOrigin.effectivePort()
	}
	if cfg.GitLab.Token != "" {
		r.gitlab = newGitLabProvider(cfg.GitLab)
		if baseOrigin.scheme == "http" && logger != nil {
			logger.Warn("GitLab base URL uses plain HTTP; token is sent unencrypted", "host", baseOrigin.host)
		}
	}

	return r, nil
}

func (r *Registry) detectProvider(repoURL string) (GitProvider, error) {
	origin, _ := parseRepositoryOrigin(repoURL)
	switch {
	case isGitHubURLCandidate(repoURL):
		return r.detectGitHubProvider(repoURL)
	case isAzureDevOpsHost(origin.host):
		return r.detectAzureDevOpsProvider(repoURL)
	case r.gitlabCustomHost != "":
		// With a custom GitLab base URL, only URLs on that origin are routed to
		// the GitLab provider. gitlab.com URLs are not: the provider would query
		// the custom server (with its token) for a project that lives elsewhere.
		if r.matchesGitLabCustomOrigin(origin) {
			return r.detectGitLabProvider(repoURL)
		}
		return nil, providerError(ErrUnsupportedProvider, "", "unsupported Git provider URL", nil)
	case origin.host == "gitlab.com":
		return r.detectGitLabProvider(repoURL)
	default:
		return nil, providerError(ErrUnsupportedProvider, "", "unsupported Git provider URL", nil)
	}
}

// matchesGitLabCustomOrigin reports whether a repository URL belongs to the
// configured custom GitLab server.
//
// The hostname must match exactly. For http(s) repository URLs the effective
// port (explicit, or 80/443 from the scheme) must also equal the effective port
// of the base URL. The scheme itself is not compared: http and https URLs only
// match when they resolve to the same port, which keeps the rule simple and
// still rejects a different service on the same host. SSH URLs (scp-style
// git@host:path and ssh://) match on hostname only, because the SSH port is
// unrelated to the API port. Other schemes match on hostname so the GitLab
// parser can reject them as invalid repository URLs.
func (r *Registry) matchesGitLabCustomOrigin(origin repositoryOrigin) bool {
	if origin.host == "" || origin.host != r.gitlabCustomHost {
		return false
	}
	switch origin.scheme {
	case "http", "https":
		return origin.effectivePort() == r.gitlabCustomPort
	default:
		return true
	}
}

// repositoryOrigin is the scheme, lowercase hostname and explicit port of a
// repository URL. scp-style SSH URLs (user@host:path) report scheme "ssh".
type repositoryOrigin struct {
	scheme string
	host   string
	port   string
}

func (o repositoryOrigin) effectivePort() string {
	if o.port != "" {
		return o.port
	}
	switch o.scheme {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}

// parseRepositoryOrigin extracts the origin from a repository URL by parsing
// only its authority, so malformed paths still yield a hostname for routing.
// The hostname is returned verbatim apart from lowercasing: a trailing dot is
// kept so that "dev.azure.com." or "gitlab.com." never match a provider host.
func parseRepositoryOrigin(rawURL string) (repositoryOrigin, bool) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return repositoryOrigin{}, false
	}
	if !strings.Contains(rawURL, "://") {
		if colon := strings.IndexByte(rawURL, ':'); colon > 0 && strings.Contains(rawURL[:colon], "@") {
			rawURL = "ssh://" + rawURL[:colon] + "/" + rawURL[colon+1:]
		} else {
			rawURL = "https://" + rawURL
		}
	}
	schemeEnd := strings.Index(rawURL, "://")
	if schemeEnd < 0 {
		return repositoryOrigin{}, false
	}
	authorityEnd := len(rawURL)
	if separator := strings.IndexAny(rawURL[schemeEnd+3:], "/?#"); separator >= 0 {
		authorityEnd = schemeEnd + 3 + separator
	}
	parsed, err := url.Parse(rawURL[:authorityEnd])
	if err != nil {
		return repositoryOrigin{}, false
	}
	return repositoryOrigin{
		scheme: strings.ToLower(parsed.Scheme),
		host:   strings.ToLower(parsed.Hostname()),
		port:   parsed.Port(),
	}, true
}

func repositoryHostname(rawURL string) string {
	origin, _ := parseRepositoryOrigin(rawURL)
	return origin.host
}

func isAzureDevOpsHost(host string) bool {
	return host == "dev.azure.com" || host == "ssh.dev.azure.com" ||
		(strings.HasSuffix(host, ".visualstudio.com") && host != "visualstudio.com")
}

func (r *Registry) detectGitHubProvider(repoURL string) (GitProvider, error) {
	if _, err := parseGitHubURL(repoURL); err != nil {
		return nil, err
	}
	return configuredProvider(r.github != nil, r.github, "github", "GitHub provider is not configured")
}

func (r *Registry) detectAzureDevOpsProvider(repoURL string) (GitProvider, error) {
	if _, err := parseAzureDevOpsURL(repoURL); err != nil {
		return nil, err
	}
	return configuredProvider(r.azureDevOps != nil, r.azureDevOps, "azure_devops", "Azure DevOps provider is not configured")
}

func (r *Registry) detectGitLabProvider(repoURL string) (GitProvider, error) {
	if _, err := parseGitLabProjectPath(repoURL); err != nil {
		return nil, err
	}
	return configuredProvider(r.gitlab != nil, r.gitlab, "gitlab", "GitLab provider is not configured")
}

func configuredProvider(configured bool, provider GitProvider, providerType, message string) (GitProvider, error) {
	if !configured {
		return nil, providerError(ErrProviderUnavailable, providerType, message, nil)
	}
	return provider, nil
}

// ListBranches detects the provider, checks the cache, and returns branches.
func (r *Registry) ListBranches(ctx context.Context, repoURL string) ([]Branch, error) {
	start := time.Now()
	provider, err := r.detectProvider(repoURL)
	if err != nil {
		recordBranchListResult(unavailableProviderType(err), "failure", time.Since(start))
		return nil, err
	}
	if err := authorizeRepository(provider, repoURL); err != nil {
		recordBranchListResult(providerTypeName(provider), "failure", time.Since(start))
		return nil, err
	}

	key := normalizeCacheKey(repoURL)

	if branches, found := r.cachedBranches(key); found {
		recordBranchListResult(providerTypeName(provider), "success", time.Since(start))
		return branches, nil
	}
	if r.afterCacheMiss != nil {
		r.afterCacheMiss(key)
	}

	fill := r.joinBranchFill(ctx, key, provider, repoURL)
	var branches []Branch
	select {
	case <-fill.done:
		// Prefer a result that is already available (e.g. the cache was filled
		// concurrently) over a cancelled context, matching the initial cache hit.
		branches, err = cloneBranches(fill.branches), fill.err
	default:
		select {
		case <-fill.done:
			branches, err = cloneBranches(fill.branches), fill.err
		case <-ctx.Done():
			r.leaveBranchFill(key, fill)
			err = ctx.Err()
		}
	}
	if err != nil {
		recordBranchListResult(providerTypeName(provider), "failure", time.Since(start))
		return nil, err
	}

	recordBranchListResult(providerTypeName(provider), "success", time.Since(start))
	return branches, nil
}

func unavailableProviderType(err error) string {
	if !errors.Is(err, ErrProviderUnavailable) {
		return "unknown"
	}
	var providerErr *ProviderError
	if errors.As(err, &providerErr) && providerErr.Provider != "" {
		return providerErr.Provider
	}
	return "unknown"
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
	if branches, found := r.cachedBranches(key); found {
		// Synthetic, already-completed fill for a cache hit found after the
		// initial miss. It is never registered in r.inflight; the no-op cancel
		// keeps it safe if a caller ever cancels or leaves it.
		done := make(chan struct{})
		close(done)
		return &branchFill{done: done, cancel: func() {}, branches: branches}
	}

	fillContext, cancel := context.WithCancel(context.WithoutCancel(ctx))
	fill := &branchFill{done: make(chan struct{}), cancel: cancel, waiters: 1}
	r.inflight[key] = fill
	go r.runBranchFill(fillContext, key, repoURL, provider, fill)
	return fill
}

func (r *Registry) cachedBranches(key string) ([]Branch, bool) {
	r.mu.RLock()
	entry, found := r.cache[key]
	r.mu.RUnlock()
	if !found || !r.nowFunc().Before(entry.expiresAt) {
		return nil, false
	}
	return cloneBranches(entry.branches), true
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
	if fill.cancel != nil {
		fill.cancel()
	}
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
	if err := p.authorize(req); err != nil {
		return err
	}

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

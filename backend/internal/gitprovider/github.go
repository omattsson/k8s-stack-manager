package gitprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	githubAPIVersion         = "2022-11-28"
	githubDefaultBaseURL     = "https://api.github.com"
	githubSCPPrefix          = "git@github.com:"
	githubHTTPTimeout        = 15 * time.Second
	githubOperationTimeout   = 30 * time.Second
	providerMaxResponseBytes = 1 << 20
	providerMaxBranches      = 1000
	githubMaxResponseBytes   = providerMaxResponseBytes
	githubMaxPages           = 10
	githubMaxBranches        = providerMaxBranches
	githubInvalidURLMessage  = "invalid GitHub repository URL"
)

var (
	githubOwnerPattern             = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
	githubRepoPattern              = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
	githubCanonicalBranchesPattern = regexp.MustCompile(`^/repositories/[1-9][0-9]*/branches$`)
	errGitHubRedirect              = errors.New("GitHub API redirect rejected")
)

type githubProvider struct {
	token               string
	baseURL             string
	httpClient          *http.Client
	timeout             time.Duration
	allowedRepositories map[string]struct{}
	configErr           error
}

type githubRepoInfo struct {
	Owner string
	Repo  string
}

type githubBranchResponse struct {
	Name string `json:"name"`
}

type githubRepositoryResponse struct {
	DefaultBranch string `json:"default_branch"`
}

func newGitHubProvider(cfg GitHubConfig) *githubProvider {
	return newGitHubProviderWithClient(cfg, githubDefaultBaseURL, &http.Client{Timeout: githubHTTPTimeout})
}

func newGitHubProviderWithClient(cfg GitHubConfig, baseURL string, client *http.Client) *githubProvider {
	if client == nil {
		client = &http.Client{Timeout: githubHTTPTimeout}
	}
	trustedClient := *client
	trustedClient.CheckRedirect = githubRedirectPolicy(baseURL, client.CheckRedirect)
	allowedRepositories, configErr := normalizeGitHubAllowedRepositories(cfg.AllowedRepositories)
	return &githubProvider{
		token:               cfg.Token,
		baseURL:             strings.TrimSuffix(baseURL, "/"),
		httpClient:          &trustedClient,
		timeout:             githubOperationTimeout,
		allowedRepositories: allowedRepositories,
		configErr:           configErr,
	}
}

func githubRedirectPolicy(baseURL string, fallback func(*http.Request, []*http.Request) error) func(*http.Request, []*http.Request) error {
	trusted, parseErr := url.Parse(baseURL)
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errGitHubRedirect
		}
		if parseErr != nil || req.URL.Scheme != trusted.Scheme || !strings.EqualFold(req.URL.Host, trusted.Host) || req.URL.User != nil {
			return errGitHubRedirect
		}
		if fallback != nil {
			return fallback(req, via)
		}
		return nil
	}
}

func (p *githubProvider) ProviderType() string {
	return "github"
}

func parseGitHubURL(rawURL string) (*githubRepoInfo, error) {
	rawURL = strings.TrimSpace(rawURL)
	if strings.HasPrefix(rawURL, githubSCPPrefix) {
		return parseGitHubPath(strings.TrimPrefix(rawURL, githubSCPPrefix))
	}

	parsed, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(parsed.Hostname(), "github.com") {
		return nil, providerError(ErrInvalidRepositoryURL, "github", githubInvalidURLMessage, nil)
	}
	if err := validateGitHubParsedURL(parsed); err != nil {
		return nil, err
	}
	return parseGitHubPath(strings.TrimPrefix(parsed.Path, "/"))
}

func isGitHubURLCandidate(rawURL string) bool {
	rawURL = strings.TrimSpace(rawURL)
	normalized := strings.ToLower(rawURL)
	if strings.HasPrefix(normalized, githubSCPPrefix) || strings.HasPrefix(normalized, "github.com/") {
		return true
	}

	authorityEnd := len(rawURL)
	if schemeEnd := strings.Index(rawURL, "://"); schemeEnd >= 0 {
		if separator := strings.IndexAny(rawURL[schemeEnd+3:], "/?#"); separator >= 0 {
			authorityEnd = schemeEnd + 3 + separator
		}
	}
	parsed, err := url.Parse(rawURL[:authorityEnd])
	return err == nil && strings.EqualFold(parsed.Hostname(), "github.com")
}

func validateGitHubParsedURL(parsed *url.URL) error {
	if parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return providerError(ErrInvalidRepositoryURL, "github", githubInvalidURLMessage, nil)
	}
	switch parsed.Scheme {
	case "https":
		if parsed.User != nil || parsed.Port() != "" {
			return providerError(ErrInvalidRepositoryURL, "github", githubInvalidURLMessage, nil)
		}
		return nil
	case "ssh":
		return validateGitHubSSHURL(parsed)
	default:
		return providerError(ErrInvalidRepositoryURL, "github", "unsupported GitHub URL scheme", nil)
	}
}

func validateGitHubSSHURL(parsed *url.URL) error {
	if parsed.User == nil || parsed.User.Username() != "git" {
		return providerError(ErrInvalidRepositoryURL, "github", githubInvalidURLMessage, nil)
	}
	if _, hasPassword := parsed.User.Password(); hasPassword {
		return providerError(ErrInvalidRepositoryURL, "github", githubInvalidURLMessage, nil)
	}
	if parsed.Port() != "" && parsed.Port() != "22" {
		return providerError(ErrInvalidRepositoryURL, "github", githubInvalidURLMessage, nil)
	}
	return nil
}

func parseGitHubPath(path string) (*githubRepoInfo, error) {
	path = strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || !githubOwnerPattern.MatchString(parts[0]) || strings.Contains(parts[0], "--") || !githubRepoPattern.MatchString(parts[1]) || parts[1] == "." || parts[1] == ".." {
		return nil, providerError(ErrInvalidRepositoryURL, "github", githubInvalidURLMessage, nil)
	}
	return &githubRepoInfo{Owner: parts[0], Repo: parts[1]}, nil
}

func normalizeGitHubAllowedRepositories(entries []string) (map[string]struct{}, error) {
	if len(entries) == 0 {
		return nil, nil
	}

	allowed := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		parts := strings.Split(strings.TrimSpace(entry), "/")
		if len(parts) != 2 || !githubOwnerPattern.MatchString(parts[0]) || strings.Contains(parts[0], "--") ||
			!githubRepoPattern.MatchString(parts[1]) || parts[1] == "." || parts[1] == ".." {
			return nil, providerError(ErrInvalidRepositoryURL, "github", "invalid GitHub allowed repository entry", nil)
		}
		allowed[strings.ToLower(parts[0]+"/"+parts[1])] = struct{}{}
	}
	return allowed, nil
}

func (p *githubProvider) authorizeRepository(repoURL string) (*githubRepoInfo, error) {
	if p.configErr != nil {
		return nil, p.configErr
	}
	info, err := parseGitHubURL(repoURL)
	if err != nil {
		return nil, err
	}
	if len(p.allowedRepositories) == 0 {
		return info, nil
	}
	if _, allowed := p.allowedRepositories[strings.ToLower(info.Owner+"/"+info.Repo)]; !allowed {
		return nil, providerError(ErrRepositoryNotAllowed, "github", "GitHub repository is not allowed", nil)
	}
	return info, nil
}

func (p *githubProvider) ListBranches(ctx context.Context, repoURL string) ([]Branch, error) {
	info, err := p.authorizeRepository(repoURL)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, p.operationTimeout())
	defer cancel()

	expectedPath := fmt.Sprintf("/repos/%s/%s/branches", url.PathEscape(info.Owner), url.PathEscape(info.Repo))
	nextURL := p.baseURL + expectedPath + "?per_page=100"
	paginationPath := expectedPath
	branches := make([]Branch, 0)
	visited := make(map[string]struct{}, githubMaxPages)
	pages := 0
	for nextURL != "" {
		if pages >= githubMaxPages {
			return nil, providerError(ErrUpstreamFailure, "github", "GitHub branch pagination limit exceeded", nil)
		}
		if _, found := visited[nextURL]; found {
			return nil, providerError(ErrUpstreamFailure, "github", "invalid GitHub pagination URL", nil)
		}
		visited[nextURL] = struct{}{}
		pages++

		resp, requestErr := p.get(ctx, nextURL)
		if requestErr != nil {
			return nil, requestErr
		}

		if responseErr := githubResponseError(resp, info); responseErr != nil {
			_ = resp.Body.Close()
			return nil, responseErr
		}

		var response []githubBranchResponse
		if decodeErr := decodeGitHubJSON(resp.Body, &response); decodeErr != nil {
			_ = resp.Body.Close()
			return nil, decodeErr
		}
		_ = resp.Body.Close()
		if len(branches)+len(response) > githubMaxBranches {
			return nil, providerError(ErrUpstreamFailure, "github", "GitHub branch collection limit exceeded", nil)
		}

		for _, branch := range response {
			branches = append(branches, Branch{Name: branch.Name})
		}

		nextURL, paginationPath, err = githubNextPageURL(resp.Header.Get("Link"), p.baseURL, resp.Request.URL, expectedPath, paginationPath)
		if err != nil {
			return nil, providerError(ErrUpstreamFailure, "github", err.Error(), err)
		}
	}
	return branches, nil
}

func githubNextPageURL(linkHeader, baseURL string, responseURL *url.URL, expectedPath, paginationPath string) (string, string, error) {
	for _, link := range strings.Split(linkHeader, ",") {
		parts := strings.Split(link, ";")
		if len(parts) < 2 || strings.TrimSpace(parts[1]) != `rel="next"` {
			continue
		}

		rawURL := strings.Trim(strings.TrimSpace(parts[0]), "<>")
		next, err := url.Parse(rawURL)
		if err != nil {
			return "", paginationPath, fmt.Errorf("invalid GitHub pagination URL")
		}
		base, err := url.Parse(baseURL)
		if err != nil || responseURL == nil || responseURL.Scheme != base.Scheme || !strings.EqualFold(responseURL.Host, base.Host) || responseURL.User != nil || responseURL.RawPath != "" || responseURL.Path != paginationPath {
			return "", paginationPath, fmt.Errorf("invalid GitHub pagination URL")
		}
		if next.Scheme != base.Scheme || !strings.EqualFold(next.Host, base.Host) || next.User != nil || next.RawPath != "" || next.Fragment != "" {
			return "", paginationPath, fmt.Errorf("invalid GitHub pagination URL")
		}
		if next.Path == paginationPath {
			return next.String(), paginationPath, nil
		}
		if paginationPath == expectedPath && githubCanonicalBranchesPattern.MatchString(next.Path) {
			return next.String(), next.Path, nil
		}
		return "", paginationPath, fmt.Errorf("invalid GitHub pagination URL")
	}
	return "", paginationPath, nil
}

func (p *githubProvider) GetDefaultBranch(ctx context.Context, repoURL string) (string, error) {
	info, err := p.authorizeRepository(repoURL)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, p.operationTimeout())
	defer cancel()

	apiURL := fmt.Sprintf("%s/repos/%s/%s", p.baseURL, url.PathEscape(info.Owner), url.PathEscape(info.Repo))
	resp, err := p.get(ctx, apiURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if err := githubResponseError(resp, info); err != nil {
		return "", err
	}

	var response githubRepositoryResponse
	if err := decodeGitHubJSON(resp.Body, &response); err != nil {
		return "", err
	}
	if response.DefaultBranch == "" {
		return "", providerError(ErrUpstreamFailure, "github", "GitHub repository has no default branch", nil)
	}
	return response.DefaultBranch, nil
}

func (p *githubProvider) ValidateBranch(ctx context.Context, repoURL string, branch string) (bool, error) {
	branches, err := p.ListBranches(ctx, repoURL)
	if err != nil {
		return false, err
	}
	for _, candidate := range branches {
		if candidate.Name == branch {
			return true, nil
		}
	}
	return false, nil
}

func (p *githubProvider) get(ctx context.Context, apiURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	req.Header.Set("User-Agent", "k8s-stack-manager")
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		if errors.Is(err, errGitHubRedirect) {
			return nil, providerError(ErrUpstreamFailure, "github", errGitHubRedirect.Error(), errGitHubRedirect)
		}
		if ctx.Err() != nil {
			return nil, providerError(ErrUpstreamFailure, "github", "GitHub API request failed", ctx.Err())
		}
		return nil, providerError(ErrUpstreamFailure, "github", "GitHub API request failed", nil)
	}
	return resp, nil
}

func (p *githubProvider) operationTimeout() time.Duration {
	if p.timeout > 0 {
		return p.timeout
	}
	return githubOperationTimeout
}

func decodeGitHubJSON(body io.Reader, target any) error {
	return decodeProviderJSON(body, target, "github", "GitHub", "GitHub API")
}

func decodeProviderJSON(body io.Reader, target any, provider, responseName, sizeLimitName string) error {
	data, err := io.ReadAll(io.LimitReader(body, providerMaxResponseBytes+1))
	if err != nil {
		return providerError(ErrUpstreamFailure, provider, "read "+responseName+" response", err)
	}
	if len(data) > providerMaxResponseBytes {
		return providerError(ErrUpstreamFailure, provider, sizeLimitName+" response exceeds size limit", nil)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return providerError(ErrUpstreamFailure, provider, "decode "+responseName+" response", err)
	}
	return nil
}

func githubResponseError(resp *http.Response, info *githubRepoInfo) error {
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return nil
	}

	retryAfter := resp.Header.Get("Retry-After")
	if (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests) && !parseRetryAfter(retryAfter, time.Now()).IsZero() {
		return githubRateLimitError(resp.Header.Get("X-RateLimit-Reset"), retryAfter)
	}
	remaining, remainingErr := strconv.ParseInt(resp.Header.Get("X-RateLimit-Remaining"), 10, 64)
	if (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests) && remainingErr == nil && remaining == 0 {
		return githubRateLimitError(resp.Header.Get("X-RateLimit-Reset"), retryAfter)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return rateLimitError("github", "GitHub API rate limit exceeded; retry later", parseRetryAfter(retryAfter, time.Now()))
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return providerError(ErrAuthentication, "github", "GitHub authentication failed (HTTP 401); check GITHUB_TOKEN", nil)
	}
	if resp.StatusCode == http.StatusNotFound {
		return providerError(ErrRepositoryNotFound, "github", "GitHub repository not found", nil)
	}
	if resp.StatusCode == http.StatusForbidden {
		return providerError(ErrAuthentication, "github", "GitHub API access forbidden (HTTP 403); check token permissions or rate limits", nil)
	}
	return providerError(ErrUpstreamFailure, "github", fmt.Sprintf("GitHub API returned HTTP %d", resp.StatusCode), nil)
}

func githubRateLimitError(rawReset, rawRetryAfter string) error {
	retryAfter := parseRetryAfter(rawRetryAfter, time.Now())
	reset, err := strconv.ParseInt(rawReset, 10, 64)
	if retryAfter.IsZero() && err == nil && reset > 0 {
		retryAfter = time.Unix(reset, 0).UTC()
	}
	if retryAfter.IsZero() {
		return rateLimitError("github", "GitHub API rate limit exhausted; retry later or configure GITHUB_TOKEN", time.Time{})
	}
	return rateLimitError("github", fmt.Sprintf("GitHub API rate limit exhausted; retry after %s or configure GITHUB_TOKEN", retryAfter.Format(time.RFC3339)), retryAfter)
}

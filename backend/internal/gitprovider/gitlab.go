package gitprovider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type gitlabProvider struct {
	token      string
	baseURL    string
	httpClient *http.Client
}

func newGitLabProvider(cfg GitLabConfig) *gitlabProvider {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://gitlab.com"
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	return &gitlabProvider{
		token:      cfg.Token,
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

func (p *gitlabProvider) ProviderType() string {
	return "gitlab"
}

func parseGitLabProjectPath(rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if strings.HasPrefix(rawURL, "git@") {
		return parseGitLabSSH(rawURL)
	}
	return parseGitLabHTTPS(rawURL)
}

func parseGitLabSSH(rawURL string) (string, error) {
	colonIdx := strings.Index(rawURL, ":")
	if colonIdx < 0 {
		return "", providerError(ErrInvalidRepositoryURL, "gitlab", "invalid GitLab SSH URL", nil)
	}
	path := rawURL[colonIdx+1:]
	path = strings.TrimSuffix(path, ".git")
	if path == "" || !strings.Contains(path, "/") {
		return "", providerError(ErrInvalidRepositoryURL, "gitlab", "invalid GitLab SSH URL path", nil)
	}
	return path, nil
}

func parseGitLabHTTPS(rawURL string) (string, error) {
	rawURL = strings.TrimPrefix(rawURL, "https://")
	rawURL = strings.TrimPrefix(rawURL, "http://")
	rawURL = strings.TrimSuffix(rawURL, ".git")
	slashIdx := strings.Index(rawURL, "/")
	if slashIdx < 0 {
		return "", providerError(ErrInvalidRepositoryURL, "gitlab", "invalid GitLab URL: missing path", nil)
	}
	path := rawURL[slashIdx+1:]
	if path == "" || !strings.Contains(path, "/") {
		return "", providerError(ErrInvalidRepositoryURL, "gitlab", "invalid GitLab URL path", nil)
	}
	return path, nil
}

type gitlabBranchResponse struct {
	Name    string `json:"name"`
	Default bool   `json:"default"`
}

func (p *gitlabProvider) ListBranches(ctx context.Context, repoURL string) ([]Branch, error) {
	projectPath, err := parseGitLabProjectPath(repoURL)
	if err != nil {
		return nil, err
	}

	encodedPath := url.PathEscape(projectPath)
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/repository/branches?per_page=100", p.baseURL, encodedPath)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", p.token)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, providerError(ErrUpstreamFailure, "gitlab", "GitLab API request failed", err)
	}
	defer resp.Body.Close()

	if err := gitLabResponseError(resp); err != nil {
		return nil, err
	}

	var glBranches []gitlabBranchResponse
	if err := decodeProviderJSON(resp.Body, &glBranches, "gitlab", "GitLab", "GitLab"); err != nil {
		return nil, err
	}
	if len(glBranches) > providerMaxBranches {
		return nil, providerError(ErrUpstreamFailure, "gitlab", "GitLab branch collection limit exceeded", nil)
	}

	branches := make([]Branch, 0, len(glBranches))
	for _, b := range glBranches {
		branches = append(branches, Branch{Name: b.Name, IsDefault: b.Default})
	}
	return branches, nil
}

func gitLabResponseError(resp *http.Response) error {
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return rateLimitError("gitlab", "GitLab API rate limit exceeded; retry later", parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()))
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return providerError(ErrAuthentication, "gitlab", fmt.Sprintf("GitLab authentication failed or permission denied (HTTP %d)", resp.StatusCode), nil)
	}
	if resp.StatusCode == http.StatusNotFound {
		return providerError(ErrRepositoryNotFound, "gitlab", "GitLab project not found", nil)
	}
	return providerError(ErrUpstreamFailure, "gitlab", fmt.Sprintf("GitLab API returned HTTP %d", resp.StatusCode), nil)
}

func (p *gitlabProvider) GetDefaultBranch(ctx context.Context, repoURL string) (string, error) {
	branches, err := p.ListBranches(ctx, repoURL)
	if err != nil {
		return "", err
	}
	for _, b := range branches {
		if b.IsDefault {
			return b.Name, nil
		}
	}
	return "", providerError(ErrUpstreamFailure, "gitlab", "GitLab project has no default branch", nil)
}

func (p *gitlabProvider) ValidateBranch(ctx context.Context, repoURL string, branch string) (bool, error) {
	branches, err := p.ListBranches(ctx, repoURL)
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

func gitlabHost(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if strings.HasPrefix(rawURL, "git@") {
		colonIdx := strings.Index(rawURL, ":")
		if colonIdx > 4 {
			return rawURL[4:colonIdx]
		}
		return ""
	}
	rawURL = strings.TrimPrefix(rawURL, "https://")
	rawURL = strings.TrimPrefix(rawURL, "http://")
	slashIdx := strings.Index(rawURL, "/")
	if slashIdx > 0 {
		return rawURL[:slashIdx]
	}
	return rawURL
}

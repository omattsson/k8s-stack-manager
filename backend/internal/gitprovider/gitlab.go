package gitprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
		return "", fmt.Errorf("invalid GitLab SSH URL: %s", rawURL)
	}
	path := rawURL[colonIdx+1:]
	path = strings.TrimSuffix(path, ".git")
	if path == "" || !strings.Contains(path, "/") {
		return "", fmt.Errorf("invalid GitLab SSH URL path: %s", rawURL)
	}
	return path, nil
}

func parseGitLabHTTPS(rawURL string) (string, error) {
	rawURL = strings.TrimPrefix(rawURL, "https://")
	rawURL = strings.TrimPrefix(rawURL, "http://")
	rawURL = strings.TrimSuffix(rawURL, ".git")
	slashIdx := strings.Index(rawURL, "/")
	if slashIdx < 0 {
		return "", fmt.Errorf("invalid GitLab URL: missing path")
	}
	path := rawURL[slashIdx+1:]
	if path == "" || !strings.Contains(path, "/") {
		return "", fmt.Errorf("invalid GitLab URL path: %s", rawURL)
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
		return nil, fmt.Errorf("GitLab API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("GitLab authentication failed (HTTP 401)")
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("GitLab project not found: %s", projectPath)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("GitLab API returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var glBranches []gitlabBranchResponse
	if err := json.NewDecoder(resp.Body).Decode(&glBranches); err != nil {
		return nil, fmt.Errorf("decode GitLab response: %w", err)
	}

	branches := make([]Branch, 0, len(glBranches))
	for _, b := range glBranches {
		branches = append(branches, Branch{Name: b.Name, IsDefault: b.Default})
	}
	return branches, nil
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
	return "", fmt.Errorf("no default branch found for %s", repoURL)
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

package gitprovider

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type azureDevOpsProvider struct {
	pat        string
	defaultOrg string
	httpClient *http.Client
}

func newAzureDevOpsProvider(cfg AzureDevOpsConfig) *azureDevOpsProvider {
	return &azureDevOpsProvider{
		pat:        cfg.PAT,
		defaultOrg: cfg.DefaultOrg,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

func (p *azureDevOpsProvider) ProviderType() string {
	return "azure_devops"
}

type azureRepoInfo struct {
	Org     string
	Project string
	Repo    string
}

func parseAzureDevOpsURL(rawURL string) (*azureRepoInfo, error) {
	rawURL = strings.TrimSpace(rawURL)
	rawURL = strings.TrimSuffix(rawURL, ".git")

	if strings.Contains(rawURL, "vs-ssh.visualstudio.com") {
		return parseAzureSSH(rawURL)
	}

	if strings.Contains(rawURL, "dev.azure.com") {
		return parseAzureDevURL(rawURL)
	}

	if strings.Contains(rawURL, "visualstudio.com") {
		return parseAzureVSURL(rawURL)
	}

	return nil, providerError(ErrInvalidRepositoryURL, "azure_devops", "not an Azure DevOps URL", nil)
}

func parseAzureSSH(rawURL string) (*azureRepoInfo, error) {
	colonIdx := strings.Index(rawURL, ":v3/")
	if colonIdx < 0 {
		return nil, providerError(ErrInvalidRepositoryURL, "azure_devops", "invalid Azure DevOps SSH URL", nil)
	}
	path := rawURL[colonIdx+4:]
	parts := strings.Split(path, "/")
	if len(parts) < 3 {
		return nil, providerError(ErrInvalidRepositoryURL, "azure_devops", "invalid Azure DevOps SSH URL path", nil)
	}
	return &azureRepoInfo{Org: parts[0], Project: parts[1], Repo: parts[2]}, nil
}

func parseAzureDevURL(rawURL string) (*azureRepoInfo, error) {
	rawURL = strings.TrimPrefix(rawURL, "https://")
	rawURL = strings.TrimPrefix(rawURL, "http://")
	parts := strings.Split(rawURL, "/")
	if len(parts) < 5 || parts[3] != "_git" {
		return nil, providerError(ErrInvalidRepositoryURL, "azure_devops", "invalid Azure DevOps URL format", nil)
	}
	return &azureRepoInfo{Org: parts[1], Project: parts[2], Repo: parts[4]}, nil
}

func parseAzureVSURL(rawURL string) (*azureRepoInfo, error) {
	rawURL = strings.TrimPrefix(rawURL, "https://")
	rawURL = strings.TrimPrefix(rawURL, "http://")
	parts := strings.Split(rawURL, "/")
	if len(parts) < 4 || parts[2] != "_git" {
		return nil, providerError(ErrInvalidRepositoryURL, "azure_devops", "invalid Azure DevOps visualstudio.com URL format", nil)
	}
	host := parts[0]
	dotIdx := strings.Index(host, ".visualstudio.com")
	if dotIdx <= 0 {
		return nil, providerError(ErrInvalidRepositoryURL, "azure_devops", "cannot extract org from host", nil)
	}
	return &azureRepoInfo{Org: host[:dotIdx], Project: parts[1], Repo: parts[3]}, nil
}

type azureRefsResponse struct {
	Value []azureRef `json:"value"`
}

type azureRef struct {
	Name     string `json:"name"`
	ObjectID string `json:"objectId"`
}

func (p *azureDevOpsProvider) ListBranches(ctx context.Context, repoURL string) ([]Branch, error) {
	info, err := parseAzureDevOpsURL(repoURL)
	if err != nil {
		return nil, err
	}

	apiURL := fmt.Sprintf(
		"https://dev.azure.com/%s/%s/_apis/git/repositories/%s/refs?filter=heads/&api-version=7.1",
		info.Org, info.Project, info.Repo,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	auth := base64.StdEncoding.EncodeToString([]byte(":" + p.pat))
	req.Header.Set("Authorization", "Basic "+auth)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, providerError(ErrUpstreamFailure, "azure_devops", "Azure DevOps API request failed", err)
	}
	defer resp.Body.Close()

	if err := azureDevOpsResponseError(resp); err != nil {
		return nil, err
	}

	var refsResp azureRefsResponse
	if err := decodeProviderJSON(resp.Body, &refsResp, "azure_devops", "Azure DevOps", "Azure DevOps"); err != nil {
		return nil, err
	}
	if len(refsResp.Value) > providerMaxBranches {
		return nil, providerError(ErrUpstreamFailure, "azure_devops", "Azure DevOps branch collection limit exceeded", nil)
	}

	branches := make([]Branch, 0, len(refsResp.Value))
	for _, ref := range refsResp.Value {
		name := strings.TrimPrefix(ref.Name, "refs/heads/")
		branches = append(branches, Branch{Name: name, IsDefault: false})
	}
	return branches, nil
}

func (p *azureDevOpsProvider) GetDefaultBranch(ctx context.Context, repoURL string) (string, error) {
	info, err := parseAzureDevOpsURL(repoURL)
	if err != nil {
		return "", err
	}

	apiURL := fmt.Sprintf(
		"https://dev.azure.com/%s/%s/_apis/git/repositories/%s?api-version=7.1",
		info.Org, info.Project, info.Repo,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	auth := base64.StdEncoding.EncodeToString([]byte(":" + p.pat))
	req.Header.Set("Authorization", "Basic "+auth)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", providerError(ErrUpstreamFailure, "azure_devops", "Azure DevOps API request failed", err)
	}
	defer resp.Body.Close()

	if err := azureDevOpsResponseError(resp); err != nil {
		return "", err
	}

	var repoResp struct {
		DefaultBranch string `json:"defaultBranch"`
	}
	if err := decodeProviderJSON(resp.Body, &repoResp, "azure_devops", "Azure DevOps", "Azure DevOps"); err != nil {
		return "", err
	}
	return strings.TrimPrefix(repoResp.DefaultBranch, "refs/heads/"), nil
}

func azureDevOpsResponseError(resp *http.Response) error {
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return rateLimitError("azure_devops", "Azure DevOps API rate limit exceeded; retry later", parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()))
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return providerError(ErrAuthentication, "azure_devops", fmt.Sprintf("Azure DevOps authentication failed or permission denied (HTTP %d)", resp.StatusCode), nil)
	}
	if resp.StatusCode == http.StatusNotFound {
		return providerError(ErrRepositoryNotFound, "azure_devops", "Azure DevOps repository not found", nil)
	}
	return providerError(ErrUpstreamFailure, "azure_devops", fmt.Sprintf("Azure DevOps API returned HTTP %d", resp.StatusCode), nil)
}

func (p *azureDevOpsProvider) ValidateBranch(ctx context.Context, repoURL string, branch string) (bool, error) {
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

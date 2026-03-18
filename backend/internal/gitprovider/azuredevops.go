package gitprovider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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

	return nil, fmt.Errorf("not an Azure DevOps URL: %s", rawURL)
}

func parseAzureSSH(rawURL string) (*azureRepoInfo, error) {
	colonIdx := strings.Index(rawURL, ":v3/")
	if colonIdx < 0 {
		return nil, fmt.Errorf("invalid Azure DevOps SSH URL: %s", rawURL)
	}
	path := rawURL[colonIdx+4:]
	parts := strings.Split(path, "/")
	if len(parts) < 3 {
		return nil, fmt.Errorf("invalid Azure DevOps SSH URL path: %s", rawURL)
	}
	return &azureRepoInfo{Org: parts[0], Project: parts[1], Repo: parts[2]}, nil
}

func parseAzureDevURL(rawURL string) (*azureRepoInfo, error) {
	rawURL = strings.TrimPrefix(rawURL, "https://")
	rawURL = strings.TrimPrefix(rawURL, "http://")
	parts := strings.Split(rawURL, "/")
	if len(parts) < 5 || parts[3] != "_git" {
		return nil, fmt.Errorf("invalid Azure DevOps URL format: %s", rawURL)
	}
	return &azureRepoInfo{Org: parts[1], Project: parts[2], Repo: parts[4]}, nil
}

func parseAzureVSURL(rawURL string) (*azureRepoInfo, error) {
	rawURL = strings.TrimPrefix(rawURL, "https://")
	rawURL = strings.TrimPrefix(rawURL, "http://")
	parts := strings.Split(rawURL, "/")
	if len(parts) < 4 || parts[2] != "_git" {
		return nil, fmt.Errorf("invalid Azure DevOps visualstudio.com URL format: %s", rawURL)
	}
	host := parts[0]
	dotIdx := strings.Index(host, ".visualstudio.com")
	if dotIdx <= 0 {
		return nil, fmt.Errorf("cannot extract org from host: %s", host)
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
		return nil, fmt.Errorf("Azure DevOps API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("Azure DevOps authentication failed (HTTP 401)")
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("repository not found: %s/%s/%s", info.Org, info.Project, info.Repo)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("Azure DevOps API returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var refsResp azureRefsResponse
	if err := json.NewDecoder(resp.Body).Decode(&refsResp); err != nil {
		return nil, fmt.Errorf("decode Azure DevOps response: %w", err)
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
		return "", fmt.Errorf("Azure DevOps API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Azure DevOps API returned HTTP %d", resp.StatusCode)
	}

	var repoResp struct {
		DefaultBranch string `json:"defaultBranch"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&repoResp); err != nil {
		return "", fmt.Errorf("decode Azure DevOps response: %w", err)
	}
	return strings.TrimPrefix(repoResp.DefaultBranch, "refs/heads/"), nil
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

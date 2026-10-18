package gitprovider

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type azureDevOpsProvider struct {
	pat        string
	defaultOrg string
	httpClient *http.Client
}

func newAzureDevOpsProvider(cfg AzureDevOpsConfig) *azureDevOpsProvider {
	return newAzureDevOpsProviderWithClient(cfg, &http.Client{Timeout: 15 * time.Second})
}

func newAzureDevOpsProviderWithClient(cfg AzureDevOpsConfig, client *http.Client) *azureDevOpsProvider {
	return &azureDevOpsProvider{
		pat:        cfg.PAT,
		defaultOrg: cfg.DefaultOrg,
		httpClient: redirectRejectingHTTPClient(client, 15*time.Second),
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
	if !strings.Contains(rawURL, "://") && isAzureDevOpsSSHHost(repositoryHostname(rawURL)) {
		return parseAzureSSH(rawURL)
	}
	if len(rawURL) >= len("ssh://") && strings.EqualFold(rawURL[:len("ssh://")], "ssh://") {
		return parseAzureSSHURL(rawURL)
	}

	parsed, err := parseAzureHTTPURL(rawURL)
	if err != nil {
		return nil, err
	}
	// Hostnames are matched exactly; a trailing dot ("dev.azure.com.") is
	// rejected for consistency with provider detection.
	host := strings.ToLower(parsed.Hostname())
	switch {
	case host == "dev.azure.com":
		return parseAzureDevURL(parsed)
	case strings.HasSuffix(host, ".visualstudio.com") && host != "visualstudio.com":
		return parseAzureVSParsedURL(parsed, host)
	default:
		return nil, providerError(ErrInvalidRepositoryURL, "azure_devops", "not an Azure DevOps URL", nil)
	}
}

// isAzureDevOpsSSHHost reports whether host is one of the Azure DevOps SSH
// endpoints (legacy vs-ssh.visualstudio.com or ssh.dev.azure.com).
func isAzureDevOpsSSHHost(host string) bool {
	return host == "vs-ssh.visualstudio.com" || host == "ssh.dev.azure.com"
}

// parseAzureSSH parses SCP-style Azure DevOps SSH URLs of the form
// user@host:v3/{org}/{project}/{repo}[.git]. Each identifier must be a plain
// path segment; anything that could alter the API request URL is rejected.
func parseAzureSSH(rawURL string) (*azureRepoInfo, error) {
	colonIdx := strings.Index(rawURL, ":v3/")
	if colonIdx < 0 || !strings.Contains(rawURL[:colonIdx], "@") {
		return nil, providerError(ErrInvalidRepositoryURL, "azure_devops", "invalid Azure DevOps SSH URL", nil)
	}
	return parseAzureSSHPath(rawURL[colonIdx+4:])
}

// parseAzureSSHURL parses ssh:// Azure DevOps URLs of the form
// ssh://user@host[:22]/v3/{org}/{project}/{repo}[.git], applying the same
// decoding and validation as the scp form.
func parseAzureSSHURL(rawURL string) (*azureRepoInfo, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || strings.Contains(rawURL, "#") || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Opaque != "" {
		return nil, providerError(ErrInvalidRepositoryURL, "azure_devops", "invalid Azure DevOps SSH URL", nil)
	}
	if !isAzureDevOpsSSHHost(strings.ToLower(parsed.Hostname())) || (parsed.Port() != "" && parsed.Port() != "22") {
		return nil, providerError(ErrInvalidRepositoryURL, "azure_devops", "invalid Azure DevOps SSH URL", nil)
	}
	if parsed.User == nil || parsed.User.Username() == "" {
		return nil, providerError(ErrInvalidRepositoryURL, "azure_devops", "invalid Azure DevOps SSH URL", nil)
	}
	if _, hasPassword := parsed.User.Password(); hasPassword {
		return nil, providerError(ErrInvalidRepositoryURL, "azure_devops", "invalid Azure DevOps SSH URL", nil)
	}
	escapedPath := parsed.EscapedPath()
	if !strings.HasPrefix(escapedPath, "/v3/") {
		return nil, providerError(ErrInvalidRepositoryURL, "azure_devops", "invalid Azure DevOps SSH URL", nil)
	}
	return parseAzureSSHPath(strings.TrimPrefix(escapedPath, "/v3/"))
}

// parseAzureSSHPath parses the escaped "{org}/{project}/{repo}[.git]" part of
// an Azure DevOps SSH URL.
func parseAzureSSHPath(escapedPath string) (*azureRepoInfo, error) {
	path := strings.TrimSuffix(escapedPath, "/")
	escapedParts := strings.Split(path, "/")
	if len(escapedParts) != 3 {
		return nil, providerError(ErrInvalidRepositoryURL, "azure_devops", "invalid Azure DevOps SSH URL path", nil)
	}
	// Azure DevOps SSH clone URLs percent-encode names (e.g. My%20Project).
	// Decode each segment first, then validate the decoded value so encoded
	// delimiters such as %3F, %23, %2F or %2E%2E are still rejected.
	parts := make([]string, len(escapedParts))
	for i, escapedPart := range escapedParts {
		part, err := url.PathUnescape(escapedPart)
		if err != nil {
			return nil, providerError(ErrInvalidRepositoryURL, "azure_devops", "invalid Azure DevOps SSH URL path", nil)
		}
		parts[i] = part
	}
	parts[2] = strings.TrimSuffix(parts[2], ".git")
	for _, part := range parts {
		if !isSafeRepositoryPathSegment(part) {
			return nil, providerError(ErrInvalidRepositoryURL, "azure_devops", "invalid Azure DevOps SSH URL path", nil)
		}
	}
	return &azureRepoInfo{Org: parts[0], Project: parts[1], Repo: parts[2]}, nil
}

func parseAzureHTTPURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(rawURL)
	// url.Parse drops an empty fragment ("...repo#"), so check the raw input.
	if err != nil || strings.Contains(rawURL, "#") {
		return nil, providerError(ErrInvalidRepositoryURL, "azure_devops", "invalid Azure DevOps URL", nil)
	}
	// Azure DevOps "Clone" URLs embed the organization as a username
	// (https://myorg@dev.azure.com/...). Accept a username-only userinfo and
	// ignore it; reject any password so credentials are never accepted.
	if parsed.User != nil {
		if _, hasPassword := parsed.User.Password(); hasPassword {
			return nil, providerError(ErrInvalidRepositoryURL, "azure_devops", "invalid Azure DevOps URL", nil)
		}
		withoutUser := *parsed
		withoutUser.User = nil
		parsed = &withoutUser
	}
	if !isCanonicalRepositoryHTTPURL(parsed) {
		return nil, providerError(ErrInvalidRepositoryURL, "azure_devops", "invalid Azure DevOps URL", nil)
	}
	return parsed, nil
}

// isCanonicalRepositoryHTTPURL reports whether parsed is an http(s) URL with a
// host and without userinfo, query string or fragment. Repository URLs are
// identifiers only; extra components are never meaningful and could otherwise
// be smuggled into authenticated provider API requests.
func isCanonicalRepositoryHTTPURL(parsed *url.URL) bool {
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return false
	}
	if parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" {
		return false
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" {
		return false
	}
	return true
}

func parseAzureDevURL(parsed *url.URL) (*azureRepoInfo, error) {
	parts, err := decodedRepositoryPath(parsed, "azure_devops")
	if err != nil || len(parts) != 4 || parts[2] != "_git" {
		return nil, providerError(ErrInvalidRepositoryURL, "azure_devops", "invalid Azure DevOps URL format", nil)
	}
	repo := strings.TrimSuffix(parts[3], ".git")
	if !isSafeRepositoryPathSegment(repo) {
		return nil, providerError(ErrInvalidRepositoryURL, "azure_devops", "invalid Azure DevOps URL format", nil)
	}
	return &azureRepoInfo{Org: parts[0], Project: parts[1], Repo: repo}, nil
}

func parseAzureVSParsedURL(parsed *url.URL, host string) (*azureRepoInfo, error) {
	parts, err := decodedRepositoryPath(parsed, "azure_devops")
	// Legacy TFS-style URLs include the collection name:
	// https://org.visualstudio.com/DefaultCollection/{project}/_git/{repo}.
	if err == nil && len(parts) == 4 && strings.EqualFold(parts[0], "DefaultCollection") {
		parts = parts[1:]
	}
	if err != nil || len(parts) != 3 || parts[1] != "_git" {
		return nil, providerError(ErrInvalidRepositoryURL, "azure_devops", "invalid Azure DevOps visualstudio.com URL format", nil)
	}
	dotIdx := strings.LastIndex(host, ".visualstudio.com")
	if dotIdx <= 0 {
		return nil, providerError(ErrInvalidRepositoryURL, "azure_devops", "cannot extract org from host", nil)
	}
	org := host[:dotIdx]
	repo := strings.TrimSuffix(parts[2], ".git")
	if !isSafeRepositoryPathSegment(org) || !isSafeRepositoryPathSegment(repo) {
		return nil, providerError(ErrInvalidRepositoryURL, "azure_devops", "invalid Azure DevOps visualstudio.com URL format", nil)
	}
	return &azureRepoInfo{Org: org, Project: parts[0], Repo: repo}, nil
}

func decodedRepositoryPath(parsed *url.URL, provider string) ([]string, error) {
	escapedPath := strings.Trim(parsed.EscapedPath(), "/")
	if escapedPath == "" {
		return nil, providerError(ErrInvalidRepositoryURL, provider, "invalid repository URL path", nil)
	}

	escapedParts := strings.Split(escapedPath, "/")
	parts := make([]string, 0, len(escapedParts))
	for _, escapedPart := range escapedParts {
		part, err := url.PathUnescape(escapedPart)
		if err != nil || !isSafeRepositoryPathSegment(part) {
			return nil, providerError(ErrInvalidRepositoryURL, provider, "invalid repository URL path", nil)
		}
		parts = append(parts, part)
	}
	return parts, nil
}

// isSafeRepositoryPathSegment reports whether a decoded repository identifier
// segment is safe to place into a provider API URL path. It rejects empty and
// dot segments, path separators, URL delimiters (query, fragment, percent) and
// control characters, so that decoded input can never change the structure of
// the authenticated API request.
func isSafeRepositoryPathSegment(part string) bool {
	if part == "" || part == "." || part == ".." {
		return false
	}
	for _, r := range part {
		switch {
		case r == '/' || r == '\\' || r == '?' || r == '#' || r == '%':
			return false
		case r < 0x20 || r == 0x7f:
			return false
		}
	}
	return true
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
		url.PathEscape(info.Org), url.PathEscape(info.Project), url.PathEscape(info.Repo),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	auth := base64.StdEncoding.EncodeToString([]byte(":" + p.pat))
	req.Header.Set("Authorization", "Basic "+auth)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		if errors.Is(err, errProviderRedirect) {
			return nil, providerError(ErrUpstreamFailure, "azure_devops", errProviderRedirect.Error(), errProviderRedirect)
		}
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
		url.PathEscape(info.Org), url.PathEscape(info.Project), url.PathEscape(info.Repo),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	auth := base64.StdEncoding.EncodeToString([]byte(":" + p.pat))
	req.Header.Set("Authorization", "Basic "+auth)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		if errors.Is(err, errProviderRedirect) {
			return "", providerError(ErrUpstreamFailure, "azure_devops", errProviderRedirect.Error(), errProviderRedirect)
		}
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

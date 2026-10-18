package gitprovider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingRegistry returns a registry with every provider configured and every
// provider transport replaced by a request counter, so tests can prove that a
// rejected URL never produced an outbound (authenticated) request.
func countingRegistry(t *testing.T, gitLabBaseURL string) (*Registry, *atomic.Int32) {
	t.Helper()
	registry := mustNewRegistry(t, Config{
		AzureDevOps: AzureDevOpsConfig{PAT: "azure-token-marker"},
		GitHub:      GitHubConfig{Token: "github-token-marker", AllowedRepositories: []string{"o/r"}},
		GitLab:      GitLabConfig{Token: "gitlab-token-marker", BaseURL: gitLabBaseURL},
	})
	var requests atomic.Int32
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		return providerResponse(req, `[]`), nil
	})
	registry.azureDevOps.httpClient.Transport = transport
	registry.github.httpClient.Transport = transport
	registry.gitlab.httpClient.Transport = transport
	return registry, &requests
}

// Gap 1: host spoofing must never route a URL to a provider.
func TestRegistryRejectsSpoofedProviderHostsWithoutRequests(t *testing.T) {
	t.Parallel()

	repoURLs := []struct {
		name    string
		repoURL string
	}{
		{name: "GitHub host in path", repoURL: "https://evil.com/github.com/o/r"},
		{name: "GitHub host as subdomain prefix", repoURL: "https://github.com.evil.com/o/r"},
		{name: "GitHub host as userinfo", repoURL: "https://github.com@evil.com/o/r"},
		{name: "GitHub host prefix in scp URL", repoURL: "git@github.com.evil.com:o/r"},
		{name: "Azure host in path", repoURL: "https://evil.com/dev.azure.com/org/p/_git/r"},
		{name: "visualstudio.com host as subdomain prefix", repoURL: "https://evil.visualstudio.com.evil.com/p/_git/r"},
	}
	configs := []struct {
		name          string
		gitLabBaseURL string
	}{
		{name: "gitlab.com", gitLabBaseURL: ""},
		{name: "custom GitLab", gitLabBaseURL: "https://git.example.com"},
	}
	operations := []struct {
		name string
		run  func(*Registry, string) error
	}{
		{name: "list branches", run: func(registry *Registry, repoURL string) error {
			_, err := registry.ListBranches(context.Background(), repoURL)
			return err
		}},
		{name: "validate branch", run: func(registry *Registry, repoURL string) error {
			_, err := registry.ValidateBranch(context.Background(), repoURL, "main")
			return err
		}},
		{name: "default branch", run: func(registry *Registry, repoURL string) error {
			_, err := registry.GetDefaultBranch(context.Background(), repoURL)
			return err
		}},
	}

	for _, config := range configs {
		config := config
		t.Run(config.name, func(t *testing.T) {
			t.Parallel()
			for _, tt := range repoURLs {
				tt := tt
				t.Run(tt.name, func(t *testing.T) {
					t.Parallel()
					registry, requests := countingRegistry(t, config.gitLabBaseURL)

					provider, err := registry.detectProvider(tt.repoURL)
					require.Error(t, err)
					assert.Nil(t, provider)
					assert.True(t, errors.Is(err, ErrUnsupportedProvider) || errors.Is(err, ErrInvalidRepositoryURL),
						"expected unsupported or invalid repository URL, got %v", err)

					for _, operation := range operations {
						err := operation.run(registry, tt.repoURL)
						require.Error(t, err, operation.name)
						assert.True(t, errors.Is(err, ErrUnsupportedProvider) || errors.Is(err, ErrInvalidRepositoryURL),
							"%s: expected unsupported or invalid repository URL, got %v", operation.name, err)
						assert.NotErrorIs(t, err, ErrProviderUnavailable, operation.name)
					}
					assert.Zero(t, requests.Load(), "spoofed hosts must not reach any provider API")
				})
			}
		})
	}
}

// Gap 2: provider hostnames are case-insensitive.
func TestRegistryAcceptsUppercaseProviderHosts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		repoURL      string
		providerType string
		expectedHost string
		expectedPath string
		responseBody string
	}{
		{
			name:         "GitHub",
			repoURL:      "https://GITHUB.COM/o/r",
			providerType: "github",
			expectedHost: "api.github.com",
			expectedPath: "/repos/o/r/branches",
			responseBody: `[{"name":"main"}]`,
		},
		{
			name:         "GitLab",
			repoURL:      "https://GITLAB.com/g/r",
			providerType: "gitlab",
			expectedHost: "gitlab.com",
			expectedPath: "/api/v4/projects/g%2Fr/repository/branches",
			responseBody: `[{"name":"main"}]`,
		},
		{
			name:         "Azure DevOps",
			repoURL:      "https://DEV.AZURE.COM/org/p/_git/r",
			providerType: "azure_devops",
			expectedHost: "dev.azure.com",
			expectedPath: "/org/p/_apis/git/repositories/r/refs",
			responseBody: `{"value":[{"name":"refs/heads/main"}]}`,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			registry := mustNewRegistry(t, Config{
				AzureDevOps: AzureDevOpsConfig{PAT: "azure-token-marker"},
				GitLab:      GitLabConfig{Token: "gitlab-token-marker"},
			})
			var requests atomic.Int32
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				requests.Add(1)
				assert.Equal(t, tt.expectedHost, req.URL.Host)
				assert.Equal(t, tt.expectedPath, req.URL.EscapedPath())
				return providerResponse(req, tt.responseBody), nil
			})
			registry.azureDevOps.httpClient.Transport = transport
			registry.github.httpClient.Transport = transport
			registry.gitlab.httpClient.Transport = transport

			provider, err := registry.detectProvider(tt.repoURL)
			require.NoError(t, err)
			assert.Equal(t, tt.providerType, provider.ProviderType())

			branches, err := registry.ListBranches(context.Background(), tt.repoURL)
			require.NoError(t, err)
			require.Len(t, branches, 1)
			assert.Equal(t, "main", branches[0].Name)
			assert.Equal(t, int32(1), requests.Load())
		})
	}
}

// Gap 3: Azure DevOps host and path edge cases.
func TestAzureDevOpsHostAndPathEdgeCasesAreRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		repoURL     string
		detectedErr error
	}{
		{name: "bare visualstudio.com", repoURL: "https://visualstudio.com/p/_git/r", detectedErr: ErrUnsupportedProvider},
		{name: "SSH host over HTTPS", repoURL: "https://ssh.dev.azure.com/org/p/_git/r", detectedErr: ErrInvalidRepositoryURL},
		{name: "SSH host over HTTPS with v3 path", repoURL: "https://ssh.dev.azure.com/v3/org/p/r", detectedErr: ErrInvalidRepositoryURL},
		{name: "dev.azure.com extra trailing segment", repoURL: "https://dev.azure.com/org/p/_git/r/extra", detectedErr: ErrInvalidRepositoryURL},
		{name: "visualstudio.com extra trailing segment", repoURL: "https://org.visualstudio.com/p/_git/r/extra", detectedErr: ErrInvalidRepositoryURL},
		{name: "dev.azure.com dot-dot project", repoURL: "https://dev.azure.com/org/../_git/r", detectedErr: ErrInvalidRepositoryURL},
		{name: "dev.azure.com dot-dot repository", repoURL: "https://dev.azure.com/org/p/_git/..", detectedErr: ErrInvalidRepositoryURL},
		{name: "visualstudio.com dot-dot project", repoURL: "https://org.visualstudio.com/../_git/r", detectedErr: ErrInvalidRepositoryURL},
		{name: "dev.azure.com encoded dot-dot", repoURL: "https://dev.azure.com/org/%2E%2E/_git/r", detectedErr: ErrInvalidRepositoryURL},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := parseAzureDevOpsURL(tt.repoURL)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidRepositoryURL)

			registry, requests := countingRegistry(t, "")
			provider, err := registry.detectProvider(tt.repoURL)
			require.Error(t, err)
			assert.Nil(t, provider)
			assert.ErrorIs(t, err, tt.detectedErr)

			_, err = registry.ListBranches(context.Background(), tt.repoURL)
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.detectedErr)
			assert.Zero(t, requests.Load())
		})
	}
}

// Gap 4: GetDefaultBranch must not follow redirects (Azure DevOps and GitLab).
func TestGetDefaultBranchRejectsAPIRedirectsBeforeForwardingCredentials(t *testing.T) {
	t.Parallel()

	providers := []struct {
		name        string
		tokenHeader string
		token       string
		newRegistry func(t *testing.T, apiServer *httptest.Server) (*Registry, string)
	}{
		{
			name:        "Azure DevOps",
			tokenHeader: "Authorization",
			token:       "azure-token-marker",
			newRegistry: func(t *testing.T, apiServer *httptest.Server) (*Registry, string) {
				registry := mustNewRegistry(t, Config{AzureDevOps: AzureDevOpsConfig{PAT: "azure-token-marker"}})
				registry.azureDevOps.httpClient.Transport = azureAPIOriginTransport(apiServer.URL)
				return registry, "https://dev.azure.com/org/project/_git/repo"
			},
		},
		{
			name:        "GitLab",
			tokenHeader: "PRIVATE-TOKEN",
			token:       "gitlab-token-marker",
			newRegistry: func(t *testing.T, apiServer *httptest.Server) (*Registry, string) {
				registry := mustNewRegistry(t, Config{GitLab: GitLabConfig{Token: "gitlab-token-marker", BaseURL: apiServer.URL}})
				return registry, apiServer.URL + "/group/repo"
			},
		},
	}
	redirects := []struct {
		name     string
		location func(targetServer *httptest.Server) string
	}{
		{name: "same origin different path", location: func(*httptest.Server) string { return "/redirect-target" }},
		{name: "cross origin", location: func(targetServer *httptest.Server) string { return targetServer.URL + "/redirect-target" }},
	}

	for _, provider := range providers {
		provider := provider
		t.Run(provider.name, func(t *testing.T) {
			t.Parallel()
			for _, redirect := range redirects {
				redirect := redirect
				t.Run(redirect.name, func(t *testing.T) {
					t.Parallel()

					var apiRequests, targetRequests atomic.Int32
					var targetToken atomic.Value
					recordTarget := func(r *http.Request) {
						targetRequests.Add(1)
						targetToken.Store(r.Header.Get(provider.tokenHeader))
					}
					targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						recordTarget(r)
						_, _ = w.Write([]byte(`{"defaultBranch":"refs/heads/main"}`))
					}))
					t.Cleanup(targetServer.Close)
					apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path == "/redirect-target" {
							recordTarget(r)
							_, _ = w.Write([]byte(`[{"name":"main","default":true}]`))
							return
						}
						apiRequests.Add(1)
						assert.NotEmpty(t, r.Header.Get(provider.tokenHeader), "first request is authenticated")
						w.Header().Set("Location", redirect.location(targetServer))
						w.WriteHeader(http.StatusFound)
					}))
					t.Cleanup(apiServer.Close)

					registry, repoURL := provider.newRegistry(t, apiServer)
					branch, err := registry.GetDefaultBranch(context.Background(), repoURL)

					require.Error(t, err)
					assert.Empty(t, branch)
					assert.ErrorIs(t, err, ErrUpstreamFailure)
					assert.ErrorIs(t, err, errProviderRedirect)
					assert.NotContains(t, err.Error(), provider.token)
					assert.Equal(t, int32(1), apiRequests.Load())
					assert.Zero(t, targetRequests.Load(), "redirect must not be followed")
					assert.Nil(t, targetToken.Load(), "token must not be sent to the redirect target")
				})
			}
		})
	}
}

// Gap 5: a recognized custom GitLab host without a token is unavailable.
func TestRegistryUnconfiguredCustomGitLabHostIsUnavailable(t *testing.T) {
	t.Parallel()

	registry := mustNewRegistry(t, Config{GitLab: GitLabConfig{BaseURL: "https://gitlab.example.com"}})
	require.Nil(t, registry.gitlab)

	tests := []struct {
		name    string
		repoURL string
	}{
		{name: "HTTPS", repoURL: "https://gitlab.example.com/group/repo"},
		{name: "scp SSH", repoURL: "git@gitlab.example.com:group/repo.git"},
		{name: "ssh URL", repoURL: "ssh://git@gitlab.example.com:2222/group/repo.git"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			for name, run := range map[string]func() error{
				"list branches": func() error {
					_, err := registry.ListBranches(context.Background(), tt.repoURL)
					return err
				},
				"validate branch": func() error {
					_, err := registry.ValidateBranch(context.Background(), tt.repoURL, "main")
					return err
				},
				"default branch": func() error {
					_, err := registry.GetDefaultBranch(context.Background(), tt.repoURL)
					return err
				},
			} {
				err := run()
				require.Error(t, err, name)
				assert.ErrorIs(t, err, ErrProviderUnavailable, name)
				assert.NotErrorIs(t, err, ErrUnsupportedProvider, name)
				var providerErr *ProviderError
				if assert.ErrorAs(t, err, &providerErr, name) {
					assert.Equal(t, "gitlab", providerErr.Provider, name)
				}
			}
		})
	}

	t.Run("gitlab.com is unsupported, not unavailable", func(t *testing.T) {
		t.Parallel()
		_, err := registry.ListBranches(context.Background(), "https://gitlab.com/group/repo")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrUnsupportedProvider)
		assert.NotErrorIs(t, err, ErrProviderUnavailable)
	})
}

// Gap 6: GitHub redirect target guards.
func TestGitHubRedirectTargetAllowedGuards(t *testing.T) {
	t.Parallel()

	trusted, err := url.Parse("https://api.github.test")
	require.NoError(t, err)
	target := func(path string) *http.Request {
		return &http.Request{URL: &url.URL{Scheme: "https", Host: "api.github.test", Path: path}}
	}

	tests := []struct {
		name     string
		req      *http.Request
		via      []*http.Request
		parseErr error
		allowed  bool
	}{
		{name: "initial request on trusted origin", req: target("/repos/o/r"), allowed: true},
		{name: "same path redirect", req: target("/repos/o/r"), via: []*http.Request{target("/repos/o/r")}, allowed: true},
		{
			name: "target with fragment",
			req:  &http.Request{URL: &url.URL{Scheme: "https", Host: "api.github.test", Path: "/repos/o/r", Fragment: "x"}},
			via:  []*http.Request{target("/repos/o/r")},
		},
		{
			name: "initial target with fragment",
			req:  &http.Request{URL: &url.URL{Scheme: "https", Host: "api.github.test", Path: "/repos/o/r", Fragment: "x"}},
		},
		{name: "nil original request", req: target("/repos/o/r"), via: []*http.Request{nil}},
		{name: "original request without URL", req: target("/repos/o/r"), via: []*http.Request{{}}},
		{
			name: "original request with raw path",
			req:  target("/repos/o/r/x"),
			via: []*http.Request{{URL: &url.URL{
				Scheme: "https", Host: "api.github.test", Path: "/repos/o/r/x", RawPath: "/repos/o/r%2Fx",
			}}},
		},
		{
			name: "target with raw path",
			req:  &http.Request{URL: &url.URL{Scheme: "https", Host: "api.github.test", Path: "/repos/o/r/x", RawPath: "/repos/o/r%2Fx"}},
			via:  []*http.Request{target("/repos/o/r/x")},
		},
		{name: "nil request", req: nil},
		{name: "request without URL", req: &http.Request{}},
		{name: "base URL parse error", req: target("/repos/o/r"), parseErr: errors.New("parse failed")},
		{name: "scheme change", req: &http.Request{URL: &url.URL{Scheme: "http", Host: "api.github.test", Path: "/repos/o/r"}}},
		{name: "host change", req: &http.Request{URL: &url.URL{Scheme: "https", Host: "evil.test", Path: "/repos/o/r"}}},
		{
			name: "userinfo",
			req:  &http.Request{URL: &url.URL{Scheme: "https", Host: "api.github.test", Path: "/repos/o/r", User: url.User("x")}},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.allowed, githubRedirectTargetAllowed(tt.req, tt.via, trusted, tt.parseErr))
		})
	}
}

// Gap 7: isSafeRepositoryPathSegment, directly and through every parser.
func TestIsSafeRepositoryPathSegment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		segment string
		safe    bool
	}{
		{name: "plain", segment: "repo", safe: true},
		{name: "space", segment: "My Project", safe: true},
		{name: "dots and dashes", segment: "my.repo-name_1", safe: true},
		{name: "three dots", segment: "...", safe: true},
		{name: "unicode", segment: "projekt-åäö", safe: true},
		{name: "empty", segment: ""},
		{name: "dot", segment: "."},
		{name: "dot-dot", segment: ".."},
		{name: "slash", segment: "a/b"},
		{name: "backslash", segment: `a\b`},
		{name: "question mark", segment: "a?b"},
		{name: "hash", segment: "a#b"},
		{name: "percent", segment: "a%b"},
		{name: "newline", segment: "a\nb"},
		{name: "carriage return", segment: "a\rb"},
		{name: "tab", segment: "a\tb"},
		{name: "NUL", segment: "a\x00b"},
		{name: "unit separator", segment: "a\x1fb"},
		{name: "DEL", segment: "a\x7fb"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.safe, isSafeRepositoryPathSegment(tt.segment))
		})
	}
}

func TestParsersRejectDecodedUnsafeSegmentCharacters(t *testing.T) {
	t.Parallel()

	parsers := []struct {
		name     string
		template string // %s is replaced by the encoded segment
		parse    func(string) error
	}{
		{name: "Azure dev.azure.com project", template: "https://dev.azure.com/org/p%s/_git/r", parse: parseAzureErr},
		{name: "Azure dev.azure.com repository", template: "https://dev.azure.com/org/p/_git/r%s", parse: parseAzureErr},
		{name: "Azure visualstudio.com project", template: "https://org.visualstudio.com/p%s/_git/r", parse: parseAzureErr},
		{name: "Azure scp SSH project", template: "git@ssh.dev.azure.com:v3/org/p%s/r", parse: parseAzureErr},
		{name: "Azure ssh URL repository", template: "ssh://git@ssh.dev.azure.com/v3/org/p/r%s", parse: parseAzureErr},
		{name: "GitLab HTTPS group", template: "https://gitlab.com/g%s/r", parse: parseGitLabErr},
		{name: "GitLab HTTPS project", template: "https://gitlab.example.com/g/r%s", parse: parseGitLabErr},
		{name: "GitLab scp SSH project", template: "git@gitlab.com:g/r%s.git", parse: parseGitLabErr},
		{name: "GitLab ssh URL project", template: "ssh://git@gitlab.example.com/g/r%s", parse: parseGitLabErr},
		{name: "GitHub HTTPS repository", template: "https://github.com/o/r%s", parse: parseGitHubErr},
	}
	encodings := []struct {
		name    string
		encoded string
	}{
		{name: "backslash", encoded: "%5C"},
		{name: "percent", encoded: "%25"},
		{name: "newline", encoded: "%0A"},
	}
	for _, parser := range parsers {
		parser := parser
		t.Run(parser.name, func(t *testing.T) {
			t.Parallel()
			for _, encoding := range encodings {
				encoding := encoding
				t.Run(encoding.name, func(t *testing.T) {
					t.Parallel()
					repoURL := fmt.Sprintf(parser.template, encoding.encoded)
					err := parser.parse(repoURL)
					require.Error(t, err, repoURL)
					assert.ErrorIs(t, err, ErrInvalidRepositoryURL, repoURL)
				})
			}
			// Sanity: the template is valid without the unsafe character.
			require.NoError(t, parser.parse(fmt.Sprintf(parser.template, "x")))
		})
	}
}

func parseAzureErr(repoURL string) error {
	_, err := parseAzureDevOpsURL(repoURL)
	return err
}

func parseGitLabErr(repoURL string) error {
	_, err := parseGitLabProjectPath(repoURL)
	return err
}

func parseGitHubErr(repoURL string) error {
	_, err := parseGitHubURL(repoURL)
	return err
}

// Gap 8: redirectRejectingHTTPClient and unavailableProviderType.
func TestRedirectRejectingHTTPClient(t *testing.T) {
	t.Parallel()

	baseTransport := &http.Transport{}
	tests := []struct {
		name              string
		client            *http.Client
		expectedTimeout   time.Duration
		expectedTransport http.RoundTripper
	}{
		{name: "nil client uses timeout", client: nil, expectedTimeout: 7 * time.Second},
		{
			name:              "existing client keeps its settings",
			client:            &http.Client{Timeout: 3 * time.Second, Transport: baseTransport},
			expectedTimeout:   3 * time.Second,
			expectedTransport: baseTransport,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var targetRequests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/target" {
					targetRequests.Add(1)
					return
				}
				http.Redirect(w, r, "/target", http.StatusFound)
			}))
			t.Cleanup(server.Close)

			client := redirectRejectingHTTPClient(tt.client, 7*time.Second)
			require.NotNil(t, client)
			assert.Equal(t, tt.expectedTimeout, client.Timeout)
			assert.Equal(t, tt.expectedTransport, client.Transport)
			require.NotNil(t, client.CheckRedirect)
			assert.ErrorIs(t, client.CheckRedirect(&http.Request{}, nil), errProviderRedirect)
			if tt.client != nil {
				assert.NotSame(t, tt.client, client, "the caller's client must be copied")
				assert.Nil(t, tt.client.CheckRedirect, "the caller's client must not be mutated")
			}

			resp, err := client.Get(server.URL + "/start")
			if resp != nil {
				_ = resp.Body.Close()
			}
			require.Error(t, err)
			assert.ErrorIs(t, err, errProviderRedirect)
			assert.Zero(t, targetRequests.Load())
		})
	}
}

func TestUnavailableProviderType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{name: "unavailable with provider", err: providerError(ErrProviderUnavailable, "gitlab", "unavailable", nil), expected: "gitlab"},
		{
			name:     "wrapped unavailable with provider",
			err:      fmt.Errorf("wrapped: %w", providerError(ErrProviderUnavailable, "azure_devops", "unavailable", nil)),
			expected: "azure_devops",
		},
		{name: "unavailable without provider", err: providerError(ErrProviderUnavailable, "", "unavailable", nil), expected: "unknown"},
		{name: "bare unavailable sentinel", err: ErrProviderUnavailable, expected: "unknown"},
		{name: "non-provider error", err: errors.New("boom"), expected: "unknown"},
		{name: "other provider error kind", err: providerError(ErrUnsupportedProvider, "github", "unsupported", nil), expected: "unknown"},
		{name: "upstream failure is not unavailable", err: providerError(ErrUpstreamFailure, "github", "upstream", nil), expected: "unknown"},
		{name: "nil error", err: nil, expected: "unknown"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, unavailableProviderType(tt.err))
		})
	}
}

// Ensure the spoofing table keeps exercising host parsing rather than empty input.
func TestSpoofedHostsParseToNonProviderHostnames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		repoURL      string
		expectedHost string
	}{
		{repoURL: "https://evil.com/github.com/o/r", expectedHost: "evil.com"},
		{repoURL: "https://github.com.evil.com/o/r", expectedHost: "github.com.evil.com"},
		{repoURL: "https://github.com@evil.com/o/r", expectedHost: "evil.com"},
		{repoURL: "git@github.com.evil.com:o/r", expectedHost: "github.com.evil.com"},
		{repoURL: "https://evil.com/dev.azure.com/org/p/_git/r", expectedHost: "evil.com"},
		{repoURL: "https://evil.visualstudio.com.evil.com/p/_git/r", expectedHost: "evil.visualstudio.com.evil.com"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(strings.ReplaceAll(tt.repoURL, "/", "_"), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expectedHost, repositoryHostname(tt.repoURL))
			assert.False(t, isGitHubURLCandidate(tt.repoURL))
			assert.False(t, isAzureDevOpsHost(repositoryHostname(tt.repoURL)))
		})
	}
}

package gitprovider

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAzureDevOpsRejectsAPIRedirectsBeforeForwardingCredentials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		location   func(apiServer, targetServer *httptest.Server) string
		targetPath string
	}{
		{
			name:       "same origin different path",
			location:   func(_, _ *httptest.Server) string { return "/redirect-target" },
			targetPath: "/redirect-target",
		},
		{
			name: "cross origin scheme and port change",
			location: func(_, targetServer *httptest.Server) string {
				return targetServer.URL + "/redirect-target"
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var targetRequests atomic.Int32
			var targetAuthorization atomic.Value
			targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				targetRequests.Add(1)
				targetAuthorization.Store(r.Header.Get("Authorization"))
				writeAzureRefs(w)
			}))
			t.Cleanup(targetServer.Close)

			var apiServer *httptest.Server
			apiServer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.targetPath != "" && r.URL.Path == tt.targetPath {
					targetRequests.Add(1)
					targetAuthorization.Store(r.Header.Get("Authorization"))
					writeAzureRefs(w)
					return
				}
				w.Header().Set("Location", tt.location(apiServer, targetServer))
				w.WriteHeader(http.StatusFound)
			}))
			t.Cleanup(apiServer.Close)

			provider := newAzureDevOpsProvider(AzureDevOpsConfig{PAT: "azure-token-marker"})
			provider.httpClient.Transport = azureAPIOriginTransport(apiServer.URL)
			_, err := provider.ListBranches(context.Background(), "https://dev.azure.com/org/project/_git/repo")

			if assert.Error(t, err) {
				assert.ErrorIs(t, err, ErrUpstreamFailure)
				assert.NotContains(t, err.Error(), "azure-token-marker")
			}
			assert.Zero(t, targetRequests.Load())
			assert.Nil(t, targetAuthorization.Load())
		})
	}
}

func TestGitLabRejectsAPIRedirectsBeforeForwardingCredentials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		location   func(apiServer, targetServer *httptest.Server) string
		targetPath string
	}{
		{
			name:       "same origin different path",
			location:   func(_, _ *httptest.Server) string { return "/redirect-target" },
			targetPath: "/redirect-target",
		},
		{
			name: "cross origin scheme and port change",
			location: func(_, targetServer *httptest.Server) string {
				return targetServer.URL + "/redirect-target"
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var targetRequests atomic.Int32
			var targetToken atomic.Value
			targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				targetRequests.Add(1)
				targetToken.Store(r.Header.Get("PRIVATE-TOKEN"))
				writeGitLabBranches(w)
			}))
			t.Cleanup(targetServer.Close)

			var apiServer *httptest.Server
			apiServer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.targetPath != "" && r.URL.Path == tt.targetPath {
					targetRequests.Add(1)
					targetToken.Store(r.Header.Get("PRIVATE-TOKEN"))
					writeGitLabBranches(w)
					return
				}
				w.Header().Set("Location", tt.location(apiServer, targetServer))
				w.WriteHeader(http.StatusFound)
			}))
			t.Cleanup(apiServer.Close)

			provider := newGitLabProvider(GitLabConfig{Token: "gitlab-token-marker", BaseURL: apiServer.URL})
			provider.httpClient.Transport = insecureTestTransport()
			_, err := provider.ListBranches(context.Background(), "https://gitlab.com/group/repo")

			if assert.Error(t, err) {
				assert.ErrorIs(t, err, ErrUpstreamFailure)
				assert.NotContains(t, err.Error(), "gitlab-token-marker")
			}
			assert.Zero(t, targetRequests.Load())
			assert.Nil(t, targetToken.Load())
		})
	}
}

func TestAzureDevOpsRejectsEncodedRepositoryIdentifiersBeforeAuthenticatedRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		repoURL string
	}{
		{name: "question mark in organization", repoURL: "https://dev.azure.com/org%3Fscope/project/_git/repo"},
		{name: "fragment delimiter in organization", repoURL: "https://dev.azure.com/org%23fragment/project/_git/repo"},
		{name: "slash in organization", repoURL: "https://dev.azure.com/org%2Fother/project/_git/repo"},
		{name: "question mark in project", repoURL: "https://dev.azure.com/org/project%3Fscope/_git/repo"},
		{name: "fragment delimiter in project", repoURL: "https://dev.azure.com/org/project%23fragment/_git/repo"},
		{name: "slash in project", repoURL: "https://dev.azure.com/org/project%2Fother/_git/repo"},
		{name: "query delimiters in repository", repoURL: "https://dev.azure.com/org/project/_git/repo%3Fapi-version%3D1%26filter%3Devil"},
		{name: "fragment delimiter in repository", repoURL: "https://dev.azure.com/org/project/_git/repo%23fragment"},
		{name: "slash in repository", repoURL: "https://dev.azure.com/org/project/_git/repo%2Fother"},
	}
	operations := []struct {
		name string
		run  func(*azureDevOpsProvider, string) error
	}{
		{
			name: "list branches",
			run: func(provider *azureDevOpsProvider, repoURL string) error {
				_, err := provider.ListBranches(context.Background(), repoURL)
				return err
			},
		},
		{
			name: "validate branch",
			run: func(provider *azureDevOpsProvider, repoURL string) error {
				_, err := provider.ValidateBranch(context.Background(), repoURL, "main")
				return err
			},
		},
	}

	for _, operation := range operations {
		operation := operation
		t.Run(operation.name, func(t *testing.T) {
			t.Parallel()
			for _, tt := range tests {
				tt := tt
				t.Run(tt.name, func(t *testing.T) {
					t.Parallel()

					var requests atomic.Int32
					var authorization atomic.Value
					provider := newAzureDevOpsProvider(AzureDevOpsConfig{PAT: "azure-token-marker"})
					provider.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
						requests.Add(1)
						authorization.Store(req.Header.Get("Authorization"))
						return providerResponse(req, `{"value":[]}`), nil
					})

					err := operation.run(provider, tt.repoURL)
					if assert.Error(t, err) {
						assert.ErrorIs(t, err, ErrInvalidRepositoryURL)
					}
					assert.Zero(t, requests.Load(), "invalid identifiers must fail before building an API request")
					assert.Nil(t, authorization.Load())
				})
			}
		})
	}
}

func TestAzureDevOpsSSHRepositorySuffixIsNormalizedInAPIPath(t *testing.T) {
	t.Parallel()

	const repoURL = "git@ssh.dev.azure.com:v3/org/project/repo.git"
	info, err := parseAzureDevOpsURL(repoURL)
	require.NoError(t, err)
	assert.Equal(t, &azureRepoInfo{Org: "org", Project: "project", Repo: "repo"}, info)

	var requests atomic.Int32
	provider := newAzureDevOpsProvider(AzureDevOpsConfig{PAT: "azure-token-marker"})
	provider.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		assert.Equal(t, "/org/project/_apis/git/repositories/repo/refs", req.URL.Path)
		assert.Equal(t, "filter=heads/&api-version=7.1", req.URL.RawQuery)
		assert.Empty(t, req.URL.Fragment)
		assert.NotEmpty(t, req.Header.Get("Authorization"))
		return providerResponse(req, `{"value":[]}`), nil
	})

	_, err = provider.ListBranches(context.Background(), repoURL)
	require.NoError(t, err)
	assert.Equal(t, int32(1), requests.Load())
}

func TestCanonicalHTTPRepositoryURLsRejectQueryAndFragment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		repoURL string
		parse   func(string) error
	}{
		{
			name:    "Azure query",
			repoURL: "https://dev.azure.com/org/project/_git/repo?version=marker",
			parse: func(repoURL string) error {
				_, err := parseAzureDevOpsURL(repoURL)
				return err
			},
		},
		{
			name:    "Azure fragment",
			repoURL: "https://dev.azure.com/org/project/_git/repo#marker",
			parse: func(repoURL string) error {
				_, err := parseAzureDevOpsURL(repoURL)
				return err
			},
		},
		{
			name:    "GitLab query",
			repoURL: "https://gitlab.com/group/repo?version=marker",
			parse: func(repoURL string) error {
				_, err := parseGitLabProjectPath(repoURL)
				return err
			},
		},
		{
			name:    "GitLab fragment",
			repoURL: "https://gitlab.com/group/repo#marker",
			parse: func(repoURL string) error {
				_, err := parseGitLabProjectPath(repoURL)
				return err
			},
		},
		{
			name:    "custom GitLab query",
			repoURL: "https://git.example.com/team/repo?version=marker",
			parse: func(repoURL string) error {
				_, err := parseGitLabProjectPath(repoURL)
				return err
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.parse(tt.repoURL)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidRepositoryURL)
			assert.NotContains(t, err.Error(), "marker")
		})
	}
}

func TestCanonicalHTTPRepositoryURLsPreserveValidAzureAndGitLabURLs(t *testing.T) {
	t.Parallel()

	azureInfo, err := parseAzureDevOpsURL("https://dev.azure.com/org/project/_git/repo.git")
	require.NoError(t, err)
	assert.Equal(t, &azureRepoInfo{Org: "org", Project: "project", Repo: "repo"}, azureInfo)

	for _, repoURL := range []string{
		"https://gitlab.com/group/repo.git",
		"https://git.example.com/team/repo.git",
	} {
		projectPath, err := parseGitLabProjectPath(repoURL)
		require.NoError(t, err)
		assert.NotContains(t, projectPath, ".git")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func azureAPIOriginTransport(targetURL string) http.RoundTripper {
	target, err := url.Parse(targetURL)
	if err != nil {
		panic(err)
	}
	base := insecureTestTransport()
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.EqualFold(req.URL.Hostname(), "dev.azure.com") {
			clone := req.Clone(req.Context())
			clone.URL = new(url.URL)
			*clone.URL = *req.URL
			clone.URL.Scheme = target.Scheme
			clone.URL.Host = target.Host
			clone.Host = target.Host
			return base.RoundTrip(clone)
		}
		return base.RoundTrip(req)
	})
}

func insecureTestTransport() *http.Transport {
	return &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec
}

func providerResponse(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func writeAzureRefs(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"value":[]}`)
}

func writeGitLabBranches(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `[]`)
}

func TestAzureDevOpsCloneURLWithUsernameIsAccepted(t *testing.T) {
	t.Parallel()

	const repoURL = "https://myorg@dev.azure.com/myorg/My%20Project/_git/repo"
	info, err := parseAzureDevOpsURL(repoURL)
	require.NoError(t, err)
	assert.Equal(t, &azureRepoInfo{Org: "myorg", Project: "My Project", Repo: "repo"}, info)

	var requests atomic.Int32
	provider := newAzureDevOpsProvider(AzureDevOpsConfig{PAT: "azure-token-marker"})
	provider.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		assert.Equal(t, "dev.azure.com", req.URL.Host)
		assert.Nil(t, req.URL.User)
		assert.Equal(t, "/myorg/My%20Project/_apis/git/repositories/repo/refs", req.URL.EscapedPath())
		assert.Equal(t, "filter=heads/&api-version=7.1", req.URL.RawQuery)
		return providerResponse(req, `{"value":[]}`), nil
	})

	_, err = provider.ListBranches(context.Background(), repoURL)
	require.NoError(t, err)
	assert.Equal(t, int32(1), requests.Load())
}

func TestAzureDevOpsHTTPURLUserinfoAndDelimiters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		repoURL   string
		expectErr bool
	}{
		{name: "username only dev.azure.com", repoURL: "https://myorg@dev.azure.com/myorg/project/_git/repo"},
		{name: "username only visualstudio.com", repoURL: "https://myorg@myorg.visualstudio.com/project/_git/repo"},
		{name: "password rejected", repoURL: "https://myorg:credential-marker@dev.azure.com/myorg/project/_git/repo", expectErr: true},
		{name: "empty password rejected", repoURL: "https://myorg:@dev.azure.com/myorg/project/_git/repo", expectErr: true},
		{name: "username with query rejected", repoURL: "https://myorg@dev.azure.com/myorg/project/_git/repo?credential-marker=1", expectErr: true},
		{name: "username with fragment rejected", repoURL: "https://myorg@dev.azure.com/myorg/project/_git/repo#credential-marker", expectErr: true},
		{name: "empty query rejected", repoURL: "https://dev.azure.com/myorg/project/_git/repo?", expectErr: true},
		{name: "empty fragment rejected", repoURL: "https://dev.azure.com/myorg/project/_git/repo#", expectErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			info, err := parseAzureDevOpsURL(tt.repoURL)
			if tt.expectErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrInvalidRepositoryURL)
				assert.NotContains(t, err.Error(), "credential-marker")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, &azureRepoInfo{Org: "myorg", Project: "project", Repo: "repo"}, info)
		})
	}
}

func TestGitLabHTTPURLRejectsEmptyQueryAndFragment(t *testing.T) {
	t.Parallel()

	for _, repoURL := range []string{
		"https://gitlab.com/group/repo?",
		"https://gitlab.com/group/repo#",
		"https://user@gitlab.com/group/repo",
	} {
		repoURL := repoURL
		t.Run(repoURL, func(t *testing.T) {
			t.Parallel()
			_, err := parseGitLabProjectPath(repoURL)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidRepositoryURL)
		})
	}
}

func TestAzureDevOpsSSHURLsDecodePercentEncodedNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		repoURL string
	}{
		{name: "ssh.dev.azure.com", repoURL: "git@ssh.dev.azure.com:v3/myorg/My%20Project/repo"},
		{name: "vs-ssh.visualstudio.com", repoURL: "myorg@vs-ssh.visualstudio.com:v3/myorg/My%20Project/repo"},
		{name: "ssh.dev.azure.com .git", repoURL: "git@ssh.dev.azure.com:v3/myorg/My%20Project/repo.git"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			info, err := parseAzureDevOpsURL(tt.repoURL)
			require.NoError(t, err)
			assert.Equal(t, &azureRepoInfo{Org: "myorg", Project: "My Project", Repo: "repo"}, info)

			var requests atomic.Int32
			provider := newAzureDevOpsProvider(AzureDevOpsConfig{PAT: "azure-token-marker"})
			provider.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				requests.Add(1)
				assert.Equal(t, "/myorg/My%20Project/_apis/git/repositories/repo/refs", req.URL.EscapedPath())
				return providerResponse(req, `{"value":[]}`), nil
			})
			_, err = provider.ListBranches(context.Background(), tt.repoURL)
			require.NoError(t, err)
			assert.Equal(t, int32(1), requests.Load())
		})
	}
}

func TestAzureDevOpsSSHURLsRejectEncodedDelimitersAfterDecoding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		repoURL string
	}{
		{name: "encoded question mark in org", repoURL: "git@ssh.dev.azure.com:v3/org%3Fscope/project/repo"},
		{name: "encoded fragment in project", repoURL: "git@ssh.dev.azure.com:v3/org/project%23fragment/repo"},
		{name: "encoded slash in repo", repoURL: "git@ssh.dev.azure.com:v3/org/project/repo%2Fother"},
		{name: "encoded dot-dot project", repoURL: "git@ssh.dev.azure.com:v3/org/%2E%2E/repo"},
		{name: "double encoded question mark", repoURL: "git@ssh.dev.azure.com:v3/org/project/repo%253F"},
		{name: "invalid escape", repoURL: "git@ssh.dev.azure.com:v3/org/project/repo%zz"},
		{name: "legacy host encoded query", repoURL: "myorg@vs-ssh.visualstudio.com:v3/myorg/project/repo%3Fapi-version%3D1"},
		{name: "literal query", repoURL: "git@ssh.dev.azure.com:v3/org/project/repo?x=1"},
		{name: "literal fragment", repoURL: "git@ssh.dev.azure.com:v3/org/project/repo#x"},
		{name: "too many segments", repoURL: "git@ssh.dev.azure.com:v3/org/project/repo/extra"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var requests atomic.Int32
			provider := newAzureDevOpsProvider(AzureDevOpsConfig{PAT: "azure-token-marker"})
			provider.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				requests.Add(1)
				return providerResponse(req, `{"value":[]}`), nil
			})
			_, err := provider.ListBranches(context.Background(), tt.repoURL)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidRepositoryURL)
			assert.Zero(t, requests.Load())
		})
	}
}

package gitprovider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRegistryValidatesGitHubTokenAllowlist(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		config            Config
		wantErr           bool
		wantAllowedRepos  int
		configurationMark string
	}{
		{
			name:   "tokenless empty allowlist succeeds",
			config: Config{},
		},
		{
			name: "token with valid allowlist succeeds",
			config: Config{GitHub: GitHubConfig{
				Token:               "github-token-secret-marker",
				AllowedRepositories: []string{"octo-org/octo-repo"},
			}},
			wantAllowedRepos: 1,
		},
		{
			name: "token with empty allowlist fails",
			config: Config{GitHub: GitHubConfig{
				Token: "github-token-secret-marker",
			}},
			wantErr:           true,
			configurationMark: "GitHub configuration",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			registry, err := invokeRegistryConstructor(t, tt.config)
			if tt.wantErr {
				if assert.Error(t, err) {
					assert.Contains(t, err.Error(), tt.configurationMark)
					assert.Contains(t, strings.ToLower(err.Error()), "allow")
					assert.NotContains(t, err.Error(), "github-token-secret-marker")
				}
				assert.Nil(t, registry, "invalid configuration must expose no registry, providers, or cache")
				return
			}

			require.NoError(t, err)
			require.NotNil(t, registry)
			require.NotNil(t, registry.github)
			assert.Empty(t, registry.cache)
			assert.Len(t, registry.github.allowedRepositories, tt.wantAllowedRepos)
		})
	}
}

func TestProviderBranchResponsesEnforceLimitsWithoutCaching(t *testing.T) {
	t.Parallel()

	azureTooMany := azureRefsResponse{Value: make([]azureRef, githubMaxBranches+1)}
	for index := range azureTooMany.Value {
		azureTooMany.Value[index].Name = "refs/heads/branch"
	}
	azureTooMany.Value[0].Name = "refs/heads/azure-too-many-response-secret-marker"
	azureTooManyBody, err := json.Marshal(azureTooMany)
	require.NoError(t, err)

	gitLabTooMany := make([]gitlabBranchResponse, githubMaxBranches+1)
	for index := range gitLabTooMany {
		gitLabTooMany[index].Name = "branch"
	}
	gitLabTooMany[0].Name = "gitlab-too-many-response-secret-marker"
	gitLabTooManyBody, err := json.Marshal(gitLabTooMany)
	require.NoError(t, err)

	tests := []struct {
		name           string
		provider       string
		repoURL        string
		body           []byte
		errorFragment  string
		responseMarker string
		newRegistry    func(*testing.T, *httptest.Server) *Registry
	}{
		{
			name:           "Azure DevOps oversized body",
			provider:       "azure_devops",
			repoURL:        "https://dev.azure.com/myorg/myproject/_git/myrepo",
			errorFragment:  "response exceeds size limit",
			responseMarker: "azure-response-secret-marker",
			body: []byte(`{"value":[{"name":"refs/heads/azure-response-secret-marker","padding":"` +
				strings.Repeat("x", githubMaxResponseBytes) + `"}]}`),
			newRegistry: func(t *testing.T, server *httptest.Server) *Registry {
				registry := mustInvokeRegistryConstructor(t, Config{AzureDevOps: AzureDevOpsConfig{PAT: "azure-pat-secret-marker"}})
				registry.azureDevOps = newTestAzureProvider(t, server)
				return registry
			},
		},
		{
			name:           "Azure DevOps too many branches",
			provider:       "azure_devops",
			repoURL:        "https://dev.azure.com/myorg/myproject/_git/myrepo",
			body:           azureTooManyBody,
			errorFragment:  "collection limit exceeded",
			responseMarker: "azure-too-many-response-secret-marker",
			newRegistry: func(t *testing.T, server *httptest.Server) *Registry {
				registry := mustInvokeRegistryConstructor(t, Config{AzureDevOps: AzureDevOpsConfig{PAT: "azure-pat-secret-marker"}})
				registry.azureDevOps = newTestAzureProvider(t, server)
				return registry
			},
		},
		{
			name:           "GitLab oversized body",
			provider:       "gitlab",
			repoURL:        "https://gitlab.com/mygroup/myproject",
			errorFragment:  "response exceeds size limit",
			responseMarker: "gitlab-response-secret-marker",
			body: []byte(`[{"name":"gitlab-response-secret-marker","default":true,"padding":"` +
				strings.Repeat("x", githubMaxResponseBytes) + `"}]`),
			newRegistry: func(t *testing.T, server *httptest.Server) *Registry {
				return mustInvokeRegistryConstructor(t, Config{GitLab: GitLabConfig{
					Token:   "gitlab-token-secret-marker",
					BaseURL: server.URL,
				}})
			},
		},
		{
			name:           "GitLab too many branches",
			provider:       "gitlab",
			repoURL:        "https://gitlab.com/mygroup/myproject",
			body:           gitLabTooManyBody,
			errorFragment:  "collection limit exceeded",
			responseMarker: "gitlab-too-many-response-secret-marker",
			newRegistry: func(t *testing.T, server *httptest.Server) *Registry {
				return mustInvokeRegistryConstructor(t, Config{GitLab: GitLabConfig{
					Token:   "gitlab-token-secret-marker",
					BaseURL: server.URL,
				}})
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var requestCount atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requestCount.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(tt.body)
			}))
			defer server.Close()

			registry := tt.newRegistry(t, server)
			branches, err := registry.ListBranches(context.Background(), tt.repoURL)

			assert.Zero(t, len(branches), "rejected provider responses must return no branches")
			if assert.Error(t, err) {
				assert.ErrorIs(t, err, ErrUpstreamFailure)
				assert.Contains(t, err.Error(), tt.errorFragment)
				assert.NotContains(t, err.Error(), tt.responseMarker)
				var providerErr *ProviderError
				if assert.True(t, errors.As(err, &providerErr), "limit error must be a ProviderError") {
					assert.Equal(t, tt.provider, providerErr.Provider)
				}
			}
			assert.Zero(t, len(registry.cache), "rejected provider responses must not be cached")
			assert.Equal(t, int32(1), requestCount.Load())
		})
	}
}

func TestRegistryReturnsButDoesNotCacheOverweightBranchResult(t *testing.T) {
	t.Parallel()

	var providerCalls atomic.Int32
	responseBody := []byte(`[{"name":"main"}]`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		providerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(responseBody)
	}))
	defer server.Close()

	registry := mustInvokeRegistryConstructor(t, Config{})
	registry.github.baseURL = server.URL
	registry.github.httpClient = server.Client()
	registry.cacheWeightLimit = 1
	repoURL := "https://github.com/octo-org/octo-repo"
	expected := []Branch{{Name: "main"}}

	first, err := registry.ListBranches(context.Background(), repoURL)
	require.NoError(t, err)
	assert.Equal(t, expected, first)
	assert.Empty(t, registry.cache)

	second, err := registry.ListBranches(context.Background(), repoURL)
	require.NoError(t, err)
	assert.Equal(t, expected, second)
	assert.Empty(t, registry.cache)
	assert.Equal(t, int32(2), providerCalls.Load())
}

func invokeRegistryConstructor(t *testing.T, config Config) (*Registry, error) {
	t.Helper()

	results := reflect.ValueOf(NewRegistry).Call([]reflect.Value{reflect.ValueOf(config)})
	require.NotEmpty(t, results)
	require.LessOrEqual(t, len(results), 2, "NewRegistry must return (*Registry, error)")

	var registry *Registry
	if !results[0].IsNil() {
		var ok bool
		registry, ok = results[0].Interface().(*Registry)
		require.True(t, ok, "NewRegistry first result must be *Registry")
	}
	if len(results) == 1 || results[1].IsNil() {
		return registry, nil
	}

	err, ok := results[1].Interface().(error)
	require.True(t, ok, "NewRegistry second result must be error")
	return registry, err
}

func mustInvokeRegistryConstructor(t *testing.T, config Config) *Registry {
	t.Helper()

	registry, err := invokeRegistryConstructor(t, config)
	require.NoError(t, err)
	require.NotNil(t, registry)
	return registry
}

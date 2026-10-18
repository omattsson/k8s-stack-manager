package gitprovider

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAzureDevOpsMixedCaseHostsMatchLowercaseBehavior(t *testing.T) {
	t.Parallel()

	registry := mustNewRegistry(t, Config{AzureDevOps: AzureDevOpsConfig{PAT: "test-pat"}})
	tests := []struct {
		name     string
		lowerURL string
		mixedURL string
	}{
		{
			name:     "dev azure exact host",
			lowerURL: "https://dev.azure.com/org/project/_git/repo",
			mixedURL: "https://DEV.AZURE.COM/org/project/_git/repo",
		},
		{
			name:     "visualstudio exact host",
			lowerURL: "https://myorg.visualstudio.com/project/_git/repo",
			mixedURL: "https://myorg.ViSuAlStUdIo.CoM/project/_git/repo",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			lowerInfo, err := parseAzureDevOpsURL(tt.lowerURL)
			require.NoError(t, err)
			mixedInfo, err := parseAzureDevOpsURL(tt.mixedURL)
			require.NoError(t, err)
			assert.Equal(t, lowerInfo, mixedInfo)

			lowerProvider, err := registry.detectProvider(tt.lowerURL)
			require.NoError(t, err)
			mixedProvider, err := registry.detectProvider(tt.mixedURL)
			require.NoError(t, err)
			assert.Equal(t, lowerProvider.ProviderType(), mixedProvider.ProviderType())
		})
	}
}

func TestDetectProviderRejectsMalformedAndDeceptiveGitLabURLs(t *testing.T) {
	t.Parallel()

	registry := mustNewRegistry(t, Config{GitLab: GitLabConfig{
		Token:   "test-token",
		BaseURL: "https://git.example.com",
	}})
	tests := []struct {
		name         string
		repoURL      string
		expectedHost string
		expectedKind error
		parseFails   bool
	}{
		{
			name:         "userinfo on GitLab host",
			repoURL:      "https://user:credential-marker@gitlab.com/group/repo",
			expectedHost: "gitlab.com",
			expectedKind: ErrInvalidRepositoryURL,
		},
		{
			name:         "unsupported FTP scheme",
			repoURL:      "ftp://gitlab.com/group/repo",
			expectedHost: "gitlab.com",
			expectedKind: ErrInvalidRepositoryURL,
		},
		{
			name:         "missing host",
			repoURL:      "https:///gitlab.com/group/repo",
			expectedKind: ErrUnsupportedProvider,
		},
		{
			name:         "malformed path escape",
			repoURL:      "https://gitlab.com/group/%zz",
			expectedKind: ErrInvalidRepositoryURL,
			parseFails:   true,
		},
		{
			name:         "GitLab domain embedded in credentials",
			repoURL:      "https://gitlab.com@evil.example/group/repo",
			expectedHost: "evil.example",
			expectedKind: ErrUnsupportedProvider,
		},
		{
			name:         "GitLab domain embedded in path",
			repoURL:      "https://example.com/gitlab.com/group/repo",
			expectedHost: "example.com",
			expectedKind: ErrUnsupportedProvider,
		},
		{
			name:         "GitLab domain embedded in query",
			repoURL:      "https://example.com/group/repo?provider=gitlab.com",
			expectedHost: "example.com",
			expectedKind: ErrUnsupportedProvider,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			parsed, parseErr := url.Parse(tt.repoURL)
			if tt.parseFails {
				require.Error(t, parseErr)
			} else {
				require.NoError(t, parseErr)
				assert.Equal(t, tt.expectedHost, parsed.Hostname())
			}

			provider, err := registry.detectProvider(tt.repoURL)
			require.Error(t, err)
			assert.Nil(t, provider)
			assert.ErrorIs(t, err, tt.expectedKind)
			var providerErr *ProviderError
			assert.ErrorAs(t, err, &providerErr)
			assert.NotContains(t, err.Error(), "credential-marker")
		})
	}
}

func TestDetectProviderAcceptsValidGitLabAndCustomHostURLs(t *testing.T) {
	t.Parallel()

	registry := mustNewRegistry(t, Config{GitLab: GitLabConfig{
		Token:   "test-token",
		BaseURL: "https://git.example.com",
	}})
	tests := []struct {
		name    string
		repoURL string
	}{
		{name: "gitlab.com", repoURL: "https://gitlab.com/group/repo"},
		{name: "configured custom host", repoURL: "https://git.example.com/group/repo"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			provider, err := registry.detectProvider(tt.repoURL)
			require.NoError(t, err)
			assert.Equal(t, "gitlab", provider.ProviderType())
		})
	}
}

func TestNewRegistryNormalizesGitLabBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		baseURL  string
		expected string
	}{
		{name: "HTTPS origin", baseURL: "  https://git.example.com/  ", expected: "https://git.example.com"},
		{name: "HTTP origin", baseURL: "  http://git.example.com/  ", expected: "http://git.example.com"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			registry, err := NewRegistry(Config{GitLab: GitLabConfig{
				Token:   "test-token",
				BaseURL: tt.baseURL,
			}})
			require.NoError(t, err)
			require.NotNil(t, registry)
			require.NotNil(t, registry.gitlab)
			assert.Equal(t, tt.expected, registry.gitlab.baseURL)
			assert.Equal(t, "git.example.com", registry.gitlabCustomHost)
		})
	}
}

func TestNewRegistryRejectsInvalidGitLabBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseURL string
	}{
		{name: "missing scheme", baseURL: "git.example.com"},
		{name: "non HTTP scheme", baseURL: "ftp://git.example.com"},
		{name: "userinfo", baseURL: "https://user:credential-marker@git.example.com"},
		{name: "missing hostname", baseURL: "https:///git.example.com"},
		{name: "path", baseURL: "https://git.example.com/gitlab"},
		{name: "query", baseURL: "https://git.example.com?credential-marker=value"},
		{name: "fragment", baseURL: "https://git.example.com#credential-marker"},
		{name: "malformed escape", baseURL: "https://git.example.com/%zz"},
		{name: "empty query", baseURL: "https://gitlab.example.com/?"},
		{name: "empty fragment", baseURL: "https://gitlab.example.com/#"},
		{name: "opaque", baseURL: "https:gitlab.example.com"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			registry, err := NewRegistry(Config{GitLab: GitLabConfig{
				Token:   "test-token",
				BaseURL: tt.baseURL,
			}})
			require.Error(t, err)
			assert.Nil(t, registry)
			assert.Contains(t, err.Error(), "invalid Git provider configuration")
			assert.NotContains(t, err.Error(), "credential-marker")
		})
	}
}

package gitprovider

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectProviderRejectsTrailingDotHostnames(t *testing.T) {
	t.Parallel()

	registry := mustNewRegistry(t, Config{
		AzureDevOps: AzureDevOpsConfig{PAT: "test-pat"},
		GitHub:      GitHubConfig{Token: "test-token", AllowedRepositories: []string{"user/repo"}},
		GitLab:      GitLabConfig{Token: "test-token"},
	})
	tests := []struct {
		name    string
		repoURL string
	}{
		{name: "GitHub HTTPS", repoURL: "https://github.com./user/repo"},
		{name: "GitHub SCP", repoURL: "git@github.com.:user/repo.git"},
		{name: "Azure dev.azure.com", repoURL: "https://dev.azure.com./org/project/_git/repo"},
		{name: "Azure visualstudio.com", repoURL: "https://org.visualstudio.com./project/_git/repo"},
		{name: "Azure SSH", repoURL: "git@ssh.dev.azure.com.:v3/org/project/repo"},
		{name: "GitLab HTTPS", repoURL: "https://gitlab.com./group/repo"},
		{name: "GitLab SCP", repoURL: "git@gitlab.com.:group/repo.git"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider, err := registry.detectProvider(tt.repoURL)
			require.Error(t, err)
			assert.Nil(t, provider)
			assert.ErrorIs(t, err, ErrUnsupportedProvider)
		})
	}
}

func TestAzureParserRejectsTrailingDotHostnames(t *testing.T) {
	t.Parallel()

	for _, repoURL := range []string{
		"https://dev.azure.com./org/project/_git/repo",
		"https://org.visualstudio.com./project/_git/repo",
		"ssh://git@ssh.dev.azure.com./v3/org/project/repo",
		"git@ssh.dev.azure.com.:v3/org/project/repo",
	} {
		repoURL := repoURL
		t.Run(repoURL, func(t *testing.T) {
			t.Parallel()
			_, err := parseAzureDevOpsURL(repoURL)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidRepositoryURL)
		})
	}
}

func TestNormalizeGitLabBaseURLRejectsTrailingDotHostname(t *testing.T) {
	t.Parallel()

	_, err := NormalizeGitLabBaseURL("https://git.example.com.")
	assert.Error(t, err)
}

func TestDetectProviderCustomGitLabOriginMatchesEffectivePort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		baseURL   string
		repoURL   string
		supported bool
	}{
		{name: "explicit port matches", baseURL: "https://git.example.com:8443", repoURL: "https://git.example.com:8443/g/r", supported: true},
		{name: "different explicit port", baseURL: "https://git.example.com:8443", repoURL: "https://git.example.com:9999/g/r"},
		{name: "http default port vs explicit base port", baseURL: "https://git.example.com:8443", repoURL: "http://git.example.com/g/r"},
		{name: "https default port vs explicit base port", baseURL: "https://git.example.com:8443", repoURL: "https://git.example.com/g/r"},
		{name: "scheme differs but port matches", baseURL: "https://git.example.com:8443", repoURL: "http://git.example.com:8443/g/r", supported: true},
		{name: "scp SSH matches on hostname", baseURL: "https://git.example.com:8443", repoURL: "git@git.example.com:g/r.git", supported: true},
		{name: "ssh URL matches on hostname", baseURL: "https://git.example.com:8443", repoURL: "ssh://git@git.example.com:2222/g/r.git", supported: true},
		{name: "default https base, explicit 443", baseURL: "https://git.example.com", repoURL: "https://git.example.com:443/g/r", supported: true},
		{name: "default https base, http repo", baseURL: "https://git.example.com", repoURL: "http://git.example.com/g/r"},
		{name: "default https base, other port", baseURL: "https://git.example.com", repoURL: "https://git.example.com:8443/g/r"},
		{name: "default http base, explicit 80", baseURL: "http://git.example.com", repoURL: "http://git.example.com:80/g/r", supported: true},
		{name: "default http base, https repo", baseURL: "http://git.example.com", repoURL: "https://git.example.com/g/r"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			registry, err := newRegistryWithLogger(Config{GitLab: GitLabConfig{Token: "test-token", BaseURL: tt.baseURL}}, nil)
			require.NoError(t, err)

			provider, err := registry.detectProvider(tt.repoURL)
			if tt.supported {
				require.NoError(t, err)
				assert.Equal(t, "gitlab", provider.ProviderType())
				return
			}
			require.Error(t, err)
			assert.Nil(t, provider)
			assert.ErrorIs(t, err, ErrUnsupportedProvider)
		})
	}
}

func TestParseGitLabProjectPathBrowserAndSSHURLForms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		repoURL   string
		expected  string
		expectErr bool
	}{
		{name: "tree URL", repoURL: "https://gitlab.com/g/r/-/tree/main", expected: "g/r"},
		{name: "blob URL in subgroup", repoURL: "https://gitlab.com/g/sub/r/-/blob/main/README.md", expected: "g/sub/r"},
		{name: "encoded branch after separator is ignored", repoURL: "https://gitlab.com/g/r/-/tree/feature%2Fx", expected: "g/r"},
		{name: "trailing .git slash", repoURL: "https://gitlab.com/g/r.git/", expected: "g/r"},
		{name: ".git before separator", repoURL: "https://gitlab.com/g/r.git/-/tree/main", expected: "g/r"},
		{name: "SCP trailing .git slash", repoURL: "git@gitlab.com:g/r.git/", expected: "g/r"},
		{name: "separator leaves one segment", repoURL: "https://gitlab.com/g/-/tree/main", expectErr: true},
		{name: "separator leaves nothing", repoURL: "https://gitlab.com/-/ide/project", expectErr: true},
		{name: "encoded dash segment", repoURL: "https://gitlab.com/g/%2D/r", expectErr: true},
		{name: "ssh URL with port", repoURL: "ssh://git@gitlab.example.com:2222/group/proj.git", expected: "group/proj"},
		{name: "ssh URL subgroup and encoded name", repoURL: "ssh://git@gitlab.example.com/group/sub/My%20Proj", expected: "group/sub/My Proj"},
		{name: "ssh URL without user", repoURL: "ssh://gitlab.example.com/group/proj", expected: "group/proj"},
		{name: "ssh URL password", repoURL: "ssh://git:credential-marker@gitlab.example.com/group/proj", expectErr: true},
		{name: "ssh URL query", repoURL: "ssh://git@gitlab.example.com/group/proj?x=1", expectErr: true},
		{name: "ssh URL fragment", repoURL: "ssh://git@gitlab.example.com/group/proj#x", expectErr: true},
		{name: "ssh URL empty fragment", repoURL: "ssh://git@gitlab.example.com/group/proj#", expectErr: true},
		{name: "ssh URL encoded query delimiter", repoURL: "ssh://git@gitlab.example.com/group/proj%3Fx", expectErr: true},
		{name: "ssh URL encoded slash", repoURL: "ssh://git@gitlab.example.com/group/proj%2Fx", expectErr: true},
		{name: "ssh URL single segment", repoURL: "ssh://git@gitlab.example.com/proj.git", expectErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			projectPath, err := parseGitLabProjectPath(tt.repoURL)
			if tt.expectErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrInvalidRepositoryURL)
				assert.NotContains(t, err.Error(), "credential-marker")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, projectPath)
		})
	}
}

func TestGitLabBrowserURLUsesProjectPathInAPIRequest(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	provider := newGitLabProvider(GitLabConfig{Token: "gitlab-token-marker"})
	provider.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		assert.Equal(t, "/api/v4/projects/g%2Fr/repository/branches", req.URL.EscapedPath())
		return providerResponse(req, `[]`), nil
	})
	_, err := provider.ListBranches(context.Background(), "https://gitlab.com/g/r/-/tree/main")
	require.NoError(t, err)
	assert.Equal(t, int32(1), requests.Load())
}

func TestDetectProviderRoutesGitLabSSHURLToCustomHost(t *testing.T) {
	t.Parallel()

	registry := mustNewRegistry(t, Config{GitLab: GitLabConfig{Token: "test-token", BaseURL: "https://gitlab.example.com"}})
	provider, err := registry.detectProvider("ssh://git@gitlab.example.com:2222/group/proj.git")
	require.NoError(t, err)
	assert.Equal(t, "gitlab", provider.ProviderType())
}

func TestParseAzureDevOpsURLLegacyAndSSHURLForms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		repoURL   string
		expected  *azureRepoInfo
		expectErr bool
	}{
		{
			name:     "DefaultCollection",
			repoURL:  "https://org.visualstudio.com/DefaultCollection/Proj/_git/repo",
			expected: &azureRepoInfo{Org: "org", Project: "Proj", Repo: "repo"},
		},
		{
			name:     "DefaultCollection lowercase with .git",
			repoURL:  "https://org.visualstudio.com/defaultcollection/Proj/_git/repo.git",
			expected: &azureRepoInfo{Org: "org", Project: "Proj", Repo: "repo"},
		},
		{
			name:      "DefaultCollection without _git",
			repoURL:   "https://org.visualstudio.com/DefaultCollection/Proj/repo/x",
			expectErr: true,
		},
		{
			name:      "other collection name is not skipped",
			repoURL:   "https://org.visualstudio.com/OtherCollection/Proj/_git/repo",
			expectErr: true,
		},
		{
			name:     "ssh URL dev.azure.com",
			repoURL:  "ssh://git@ssh.dev.azure.com/v3/org/proj/repo",
			expected: &azureRepoInfo{Org: "org", Project: "proj", Repo: "repo"},
		},
		{
			name:     "ssh URL .git and port 22",
			repoURL:  "ssh://git@ssh.dev.azure.com:22/v3/org/proj/repo.git",
			expected: &azureRepoInfo{Org: "org", Project: "proj", Repo: "repo"},
		},
		{
			name:     "ssh URL legacy host with encoded project",
			repoURL:  "ssh://myorg@vs-ssh.visualstudio.com/v3/myorg/My%20Project/repo",
			expected: &azureRepoInfo{Org: "myorg", Project: "My Project", Repo: "repo"},
		},
		{name: "ssh URL password", repoURL: "ssh://git:credential-marker@ssh.dev.azure.com/v3/org/proj/repo", expectErr: true},
		{name: "ssh URL no user", repoURL: "ssh://ssh.dev.azure.com/v3/org/proj/repo", expectErr: true},
		{name: "ssh URL other port", repoURL: "ssh://git@ssh.dev.azure.com:2222/v3/org/proj/repo", expectErr: true},
		{name: "ssh URL wrong host", repoURL: "ssh://git@dev.azure.com/v3/org/proj/repo", expectErr: true},
		{name: "ssh URL missing v3", repoURL: "ssh://git@ssh.dev.azure.com/org/proj/repo", expectErr: true},
		{name: "ssh URL query", repoURL: "ssh://git@ssh.dev.azure.com/v3/org/proj/repo?x=1", expectErr: true},
		{name: "ssh URL fragment", repoURL: "ssh://git@ssh.dev.azure.com/v3/org/proj/repo#", expectErr: true},
		{name: "ssh URL encoded query delimiter", repoURL: "ssh://git@ssh.dev.azure.com/v3/org/proj/repo%3Fx", expectErr: true},
		{name: "ssh URL encoded dot-dot", repoURL: "ssh://git@ssh.dev.azure.com/v3/org/%2E%2E/repo", expectErr: true},
		{name: "ssh URL too many segments", repoURL: "ssh://git@ssh.dev.azure.com/v3/org/proj/repo/extra", expectErr: true},
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
			assert.Equal(t, tt.expected, info)
		})
	}
}

func TestAzureDevOpsSSHURLFormIsDetectedAndEscapedInAPIPath(t *testing.T) {
	t.Parallel()

	const repoURL = "ssh://git@ssh.dev.azure.com/v3/myorg/My%20Project/repo.git"
	registry := mustNewRegistry(t, Config{AzureDevOps: AzureDevOpsConfig{PAT: "azure-token-marker"}})
	detected, err := registry.detectProvider(repoURL)
	require.NoError(t, err)
	assert.Equal(t, "azure_devops", detected.ProviderType())

	var requests atomic.Int32
	provider := newAzureDevOpsProvider(AzureDevOpsConfig{PAT: "azure-token-marker"})
	provider.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		assert.Equal(t, "/myorg/My%20Project/_apis/git/repositories/repo/refs", req.URL.EscapedPath())
		return providerResponse(req, `{"value":[]}`), nil
	})
	_, err = provider.ListBranches(context.Background(), repoURL)
	require.NoError(t, err)
	assert.Equal(t, int32(1), requests.Load())
}

func TestNewRegistryWarnsWhenGitLabTokenUsesPlainHTTP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  GitLabConfig
		warning bool
	}{
		{name: "http with token", config: GitLabConfig{Token: "gitlab-token-marker", BaseURL: "http://git.example.com"}, warning: true},
		{name: "https with token", config: GitLabConfig{Token: "gitlab-token-marker", BaseURL: "https://git.example.com"}},
		{name: "http without token", config: GitLabConfig{BaseURL: "http://git.example.com"}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))

			_, err := newRegistryWithLogger(Config{GitLab: tt.config}, logger)
			require.NoError(t, err)

			assert.NotContains(t, logs.String(), "gitlab-token-marker")
			if tt.warning {
				assert.Contains(t, logs.String(), "level=WARN")
				assert.Contains(t, logs.String(), "GitLab base URL uses plain HTTP; token is sent unencrypted")
				return
			}
			assert.Empty(t, logs.String())
		})
	}
}

func TestRegistryCancelledContextUsesCacheFilledAfterInitialMiss(t *testing.T) {
	t.Parallel()

	// The select in ListBranches would otherwise pick randomly between the
	// completed synthetic fill and the cancelled context; repeat to cover both.
	for attempt := 0; attempt < 50; attempt++ {
		var providerCalls atomic.Int32
		registry := mustNewRegistry(t, Config{})
		registry.github.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			providerCalls.Add(1)
			return providerResponse(req, `[]`), nil
		})
		repoURL := "https://github.com/octo-org/octo-repo"
		expected := []Branch{{Name: "main"}}
		registry.afterCacheMiss = func(key string) {
			registry.storeCacheEntry(key, expected)
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		branches, err := registry.ListBranches(ctx, repoURL)
		require.NoError(t, err)
		assert.Equal(t, expected, branches)
		assert.Zero(t, providerCalls.Load())
	}
}

func TestRegistrySyntheticCacheHitFillIsSafeToLeaveAndCancel(t *testing.T) {
	t.Parallel()

	registry := mustNewRegistry(t, Config{})
	repoURL := "https://github.com/octo-org/octo-repo"
	key := normalizeCacheKey(repoURL)
	registry.storeCacheEntry(key, []Branch{{Name: "main"}})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fill := registry.joinBranchFill(ctx, key, registry.github, repoURL)
	require.NotNil(t, fill.cancel)
	assert.NotPanics(t, func() {
		registry.leaveBranchFill(key, fill)
		fill.cancel()
	})
	select {
	case <-fill.done:
	default:
		require.FailNow(t, "synthetic fill must already be complete")
	}
	assert.Equal(t, []Branch{{Name: "main"}}, fill.branches)
}

func TestParseGitLabSCPURLDecodesSegments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		repoURL   string
		expected  string
		expectErr bool
	}{
		{name: "encoded space", repoURL: "git@gitlab.com:g/My%20Proj.git", expected: "g/My Proj"},
		{name: "encoded space subgroup", repoURL: "git@gitlab.com:g/My%20Sub/r", expected: "g/My Sub/r"},
		{name: "browser separator", repoURL: "git@gitlab.com:g/r/-/tree/main", expected: "g/r"},
		{name: "encoded slash", repoURL: "git@gitlab.com:g/r%2Fx.git", expectErr: true},
		{name: "encoded question mark", repoURL: "git@gitlab.com:g/r%3Fx.git", expectErr: true},
		{name: "encoded fragment", repoURL: "git@gitlab.com:g/r%23x.git", expectErr: true},
		{name: "encoded dot-dot", repoURL: "git@gitlab.com:g/%2E%2E/r.git", expectErr: true},
		{name: "encoded percent", repoURL: "git@gitlab.com:g/r%25x.git", expectErr: true},
		{name: "invalid escape", repoURL: "git@gitlab.com:g/r%zz.git", expectErr: true},
		{name: "leading slash", repoURL: "git@gitlab.com:/g/r.git", expectErr: true},
		{name: "single segment", repoURL: "git@gitlab.com:r.git", expectErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			projectPath, err := parseGitLabProjectPath(tt.repoURL)
			if tt.expectErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrInvalidRepositoryURL)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, projectPath)
		})
	}
}

func TestGitLabSCPURLWithSpaceIsEscapedInAPIPath(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	provider := newGitLabProvider(GitLabConfig{Token: "gitlab-token-marker"})
	provider.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		assert.Equal(t, "/api/v4/projects/g%2FMy%20Proj/repository/branches", req.URL.EscapedPath())
		return providerResponse(req, `[]`), nil
	})
	_, err := provider.ListBranches(context.Background(), "git@gitlab.com:g/My%20Proj.git")
	require.NoError(t, err)
	assert.Equal(t, int32(1), requests.Load())
}

func TestGitHubSCPHostIsCaseInsensitive(t *testing.T) {
	t.Parallel()

	registry := mustNewRegistry(t, Config{GitHub: GitHubConfig{Token: "test-token", AllowedRepositories: []string{"o/r"}}})
	tests := []struct {
		name      string
		repoURL   string
		expectErr bool
	}{
		{name: "uppercase host", repoURL: "git@GITHUB.COM:o/r"},
		{name: "mixed case host with .git", repoURL: "git@GitHub.com:o/r.git"},
		{name: "uppercase user stays rejected", repoURL: "GIT@github.com:o/r", expectErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			info, err := parseGitHubURL(tt.repoURL)
			if tt.expectErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrInvalidRepositoryURL)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, &githubRepoInfo{Owner: "o", Repo: "r"}, info)

			provider, err := registry.detectProvider(tt.repoURL)
			require.NoError(t, err)
			assert.Equal(t, "github", provider.ProviderType())
		})
	}
}

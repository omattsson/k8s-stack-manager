package gitprovider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectProvider(t *testing.T) {
	t.Parallel()
	registry := NewRegistry(Config{
		AzureDevOps: AzureDevOpsConfig{PAT: "test-pat"},
		GitLab:      GitLabConfig{Token: "test-token", BaseURL: "https://gitlab.com"},
	})
	tests := []struct {
		name         string
		url          string
		expectedType string
		expectErr    bool
	}{
		{"AzDO dev.azure.com", "https://dev.azure.com/myorg/myproject/_git/myrepo", "azure_devops", false},
		{"AzDO visualstudio.com", "https://myorg.visualstudio.com/myproject/_git/myrepo", "azure_devops", false},
		{"AzDO SSH", "myorg@vs-ssh.visualstudio.com:v3/myorg/myproject/myrepo", "azure_devops", false},
		{"GitLab HTTPS", "https://gitlab.com/mygroup/myproject", "gitlab", false},
		{"GitLab HTTPS .git", "https://gitlab.com/mygroup/myproject.git", "gitlab", false},
		{"GitLab SSH", "git@gitlab.com:mygroup/myproject.git", "gitlab", false},
		{"Unknown", "https://github.com/user/repo", "", true},
		{"Empty", "", "", true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider, err := registry.detectProvider(tt.url)
			if tt.expectErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expectedType, provider.ProviderType())
		})
	}
}

func TestDetectProviderCustomGitLabDomain(t *testing.T) {
	t.Parallel()
	registry := NewRegistry(Config{
		GitLab: GitLabConfig{Token: "test-token", BaseURL: "https://git.example.com"},
	})
	tests := []struct {
		name         string
		url          string
		expectedType string
		expectErr    bool
	}{
		{"Custom HTTPS", "https://git.example.com/group/project", "gitlab", false},
		{"Custom SSH", "git@git.example.com:group/project.git", "gitlab", false},
		{"gitlab.com also matched", "https://gitlab.com/group/project", "gitlab", false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider, err := registry.detectProvider(tt.url)
			if tt.expectErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expectedType, provider.ProviderType())
		})
	}
}

func TestDetectProviderUnconfigured(t *testing.T) {
	t.Parallel()
	registry := NewRegistry(Config{})
	tests := []struct {
		name string
		url  string
	}{
		{"AzDO unconfigured", "https://dev.azure.com/org/proj/_git/repo"},
		{"GitLab unconfigured", "https://gitlab.com/group/project"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := registry.detectProvider(tt.url)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "not configured")
		})
	}
}

func TestParseAzureDevOpsURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, url, org, prj, repo string
		expectErr                 bool
	}{
		{"dev.azure.com", "https://dev.azure.com/myorg/myproject/_git/myrepo", "myorg", "myproject", "myrepo", false},
		{"dev.azure.com .git", "https://dev.azure.com/myorg/myproject/_git/myrepo.git", "myorg", "myproject", "myrepo", false},
		{"visualstudio.com", "https://myorg.visualstudio.com/myproject/_git/myrepo", "myorg", "myproject", "myrepo", false},
		{"visualstudio.com .git", "https://myorg.visualstudio.com/myproject/_git/myrepo.git", "myorg", "myproject", "myrepo", false},
		{"SSH", "myorg@vs-ssh.visualstudio.com:v3/myorg/myproject/myrepo", "myorg", "myproject", "myrepo", false},
		{"no _git", "https://dev.azure.com/myorg/myproject/myrepo", "", "", "", true},
		{"too few parts", "https://dev.azure.com/myorg", "", "", "", true},
		{"SSH no v3", "myorg@vs-ssh.visualstudio.com:myorg/myproject/myrepo", "", "", "", true},
		{"not azure", "https://github.com/user/repo", "", "", "", true},
		{"whitespace", "  https://dev.azure.com/myorg/myproject/_git/myrepo  ", "myorg", "myproject", "myrepo", false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			info, err := parseAzureDevOpsURL(tt.url)
			if tt.expectErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.org, info.Org)
			assert.Equal(t, tt.prj, info.Project)
			assert.Equal(t, tt.repo, info.Repo)
		})
	}
}

func TestParseGitLabProjectPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, url, path string
		expectErr       bool
	}{
		{"HTTPS", "https://gitlab.com/mygroup/myproject", "mygroup/myproject", false},
		{"HTTPS .git", "https://gitlab.com/mygroup/myproject.git", "mygroup/myproject", false},
		{"subgroup", "https://gitlab.com/mygroup/subgroup/myproject", "mygroup/subgroup/myproject", false},
		{"SSH", "git@gitlab.com:mygroup/myproject.git", "mygroup/myproject", false},
		{"SSH no .git", "git@gitlab.com:mygroup/myproject", "mygroup/myproject", false},
		{"custom domain", "https://git.example.com/team/repo", "team/repo", false},
		{"custom SSH", "git@git.example.com:team/repo.git", "team/repo", false},
		{"HTTP", "http://gitlab.com/mygroup/myproject", "mygroup/myproject", false},
		{"no path", "https://gitlab.com", "", true},
		{"single segment", "https://gitlab.com/onlyone", "", true},
		{"SSH no colon", "git@gitlab.com", "", true},
		{"whitespace", "  https://gitlab.com/mygroup/myproject  ", "mygroup/myproject", false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path, err := parseGitLabProjectPath(tt.url)
			if tt.expectErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.path, path)
		})
	}
}

func TestGitLabHost(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, url, expected string
	}{
		{"HTTPS", "https://gitlab.com/group/project", "gitlab.com"},
		{"SSH", "git@gitlab.com:group/project.git", "gitlab.com"},
		{"custom", "https://git.example.com/group/project", "git.example.com"},
		{"custom SSH", "git@git.example.com:group/project.git", "git.example.com"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, gitlabHost(tt.url))
		})
	}
}

func TestGitLabListBranches(t *testing.T) {
	t.Parallel()
	glBranches := []gitlabBranchResponse{
		{Name: "main", Default: true},
		{Name: "develop", Default: false},
		{Name: "feature/new-thing", Default: false},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "test-token", r.Header.Get("PRIVATE-TOKEN"))
		assert.Contains(t, r.URL.Path, "/api/v4/projects/")
		assert.Contains(t, r.URL.Path, "/repository/branches")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(glBranches)
	}))
	defer server.Close()

	provider := &gitlabProvider{token: "test-token", baseURL: server.URL, httpClient: server.Client()}
	ctx := context.Background()
	branches, err := provider.ListBranches(ctx, "https://gitlab.com/mygroup/myproject")
	require.NoError(t, err)
	assert.Len(t, branches, 3)
	assert.Equal(t, "main", branches[0].Name)
	assert.True(t, branches[0].IsDefault)
	assert.Equal(t, "develop", branches[1].Name)
	assert.False(t, branches[1].IsDefault)
}

func TestGitLabGetDefaultBranch(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]gitlabBranchResponse{
			{Name: "develop", Default: false},
			{Name: "main", Default: true},
		})
	}))
	defer server.Close()

	provider := &gitlabProvider{token: "test-token", baseURL: server.URL, httpClient: server.Client()}
	ctx := context.Background()
	branch, err := provider.GetDefaultBranch(ctx, "https://gitlab.com/mygroup/myproject")
	require.NoError(t, err)
	assert.Equal(t, "main", branch)
}

func TestGitLabValidateBranch(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]gitlabBranchResponse{
			{Name: "main", Default: true},
			{Name: "develop", Default: false},
		})
	}))
	defer server.Close()

	provider := &gitlabProvider{token: "test-token", baseURL: server.URL, httpClient: server.Client()}
	ctx := context.Background()

	exists, err := provider.ValidateBranch(ctx, "https://gitlab.com/mygroup/myproject", "main")
	require.NoError(t, err)
	assert.True(t, exists)

	exists, err = provider.ValidateBranch(ctx, "https://gitlab.com/mygroup/myproject", "nonexistent")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestGitLabAPIErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		statusCode int
		errContain string
	}{
		{"Unauthorized", http.StatusUnauthorized, "authentication failed"},
		{"Not Found", http.StatusNotFound, "not found"},
		{"Server Error", http.StatusInternalServerError, "HTTP 500"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				w.Write([]byte("error"))
			}))
			defer server.Close()

			provider := &gitlabProvider{token: "test-token", baseURL: server.URL, httpClient: server.Client()}
			_, err := provider.ListBranches(context.Background(), "https://gitlab.com/mygroup/myproject")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errContain)
		})
	}
}

func TestGitLabSubgroupProject(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/api/v4/projects/")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]gitlabBranchResponse{{Name: "main", Default: true}})
	}))
	defer server.Close()

	provider := &gitlabProvider{token: "test-token", baseURL: server.URL, httpClient: server.Client()}
	branches, err := provider.ListBranches(context.Background(), "https://gitlab.com/group/subgroup/project")
	require.NoError(t, err)
	assert.Len(t, branches, 1)
}

func TestRegistryCacheHit(t *testing.T) {
	t.Parallel()
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]gitlabBranchResponse{{Name: "main", Default: true}})
	}))
	defer server.Close()

	registry := NewRegistry(Config{GitLab: GitLabConfig{Token: "test-token", BaseURL: server.URL}})
	registry.gitlab.baseURL = server.URL
	ctx := context.Background()
	repoURL := "https://gitlab.com/mygroup/myproject"

	b1, err := registry.ListBranches(ctx, repoURL)
	require.NoError(t, err)
	assert.Len(t, b1, 1)
	assert.Equal(t, 1, callCount)

	b2, err := registry.ListBranches(ctx, repoURL)
	require.NoError(t, err)
	assert.Len(t, b2, 1)
	assert.Equal(t, 1, callCount)
}

func TestRegistryCacheExpiry(t *testing.T) {
	t.Parallel()
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]gitlabBranchResponse{{Name: "main", Default: true}})
	}))
	defer server.Close()

	registry := NewRegistry(Config{GitLab: GitLabConfig{Token: "test-token", BaseURL: server.URL}})
	registry.gitlab.baseURL = server.URL
	now := time.Now()
	registry.nowFunc = func() time.Time { return now }
	ctx := context.Background()
	repoURL := "https://gitlab.com/mygroup/myproject"

	_, err := registry.ListBranches(ctx, repoURL)
	require.NoError(t, err)
	assert.Equal(t, 1, callCount)

	now = now.Add(cacheTTL + time.Second)
	_, err = registry.ListBranches(ctx, repoURL)
	require.NoError(t, err)
	assert.Equal(t, 2, callCount)
}

func TestRegistryInvalidateCache(t *testing.T) {
	t.Parallel()
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]gitlabBranchResponse{{Name: "main", Default: true}})
	}))
	defer server.Close()

	registry := NewRegistry(Config{GitLab: GitLabConfig{Token: "test-token", BaseURL: server.URL}})
	registry.gitlab.baseURL = server.URL
	ctx := context.Background()
	repoURL := "https://gitlab.com/mygroup/myproject"

	_, err := registry.ListBranches(ctx, repoURL)
	require.NoError(t, err)
	assert.Equal(t, 1, callCount)

	registry.InvalidateCache(repoURL)
	_, err = registry.ListBranches(ctx, repoURL)
	require.NoError(t, err)
	assert.Equal(t, 2, callCount)
}

func TestRegistryCacheNormalization(t *testing.T) {
	t.Parallel()
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]gitlabBranchResponse{{Name: "main", Default: true}})
	}))
	defer server.Close()

	registry := NewRegistry(Config{GitLab: GitLabConfig{Token: "test-token", BaseURL: server.URL}})
	registry.gitlab.baseURL = server.URL
	ctx := context.Background()

	_, err := registry.ListBranches(ctx, "https://gitlab.com/mygroup/myproject")
	require.NoError(t, err)
	assert.Equal(t, 1, callCount)

	_, err = registry.ListBranches(ctx, "https://gitlab.com/mygroup/myproject.git")
	require.NoError(t, err)
	assert.Equal(t, 1, callCount)
}

func TestRegistryValidateBranch(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]gitlabBranchResponse{
			{Name: "main", Default: true},
			{Name: "develop", Default: false},
		})
	}))
	defer server.Close()

	registry := NewRegistry(Config{GitLab: GitLabConfig{Token: "test-token", BaseURL: server.URL}})
	registry.gitlab.baseURL = server.URL
	ctx := context.Background()
	repoURL := "https://gitlab.com/mygroup/myproject"

	valid, err := registry.ValidateBranch(ctx, repoURL, "main")
	require.NoError(t, err)
	assert.True(t, valid)

	valid, err = registry.ValidateBranch(ctx, repoURL, "nonexistent")
	require.NoError(t, err)
	assert.False(t, valid)
}

func TestGetProviderStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		config         Config
		expectedAzure  bool
		expectedGitLab bool
	}{
		{"Both", Config{AzureDevOps: AzureDevOpsConfig{PAT: "p"}, GitLab: GitLabConfig{Token: "t"}}, true, true},
		{"Only AzDO", Config{AzureDevOps: AzureDevOpsConfig{PAT: "p"}}, true, false},
		{"Only GitLab", Config{GitLab: GitLabConfig{Token: "t"}}, false, true},
		{"None", Config{}, false, false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			statuses := NewRegistry(tt.config).GetProviderStatus()
			require.Len(t, statuses, 2)
			for _, s := range statuses {
				switch s.Type {
				case "azure_devops":
					assert.Equal(t, tt.expectedAzure, s.Available)
				case "gitlab":
					assert.Equal(t, tt.expectedGitLab, s.Available)
				}
			}
		})
	}
}

func TestNormalizeCacheKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, input, expected string
	}{
		{"Lowercase", "HTTPS://GITLAB.COM/Group/Project", "https://gitlab.com/group/project"},
		{"Strip .git", "https://gitlab.com/group/project.git", "https://gitlab.com/group/project"},
		{"Strip slash", "https://gitlab.com/group/project/", "https://gitlab.com/group/project"},
		{"Trim ws", "  https://gitlab.com/group/project  ", "https://gitlab.com/group/project"},
		{"Combined", "  HTTPS://GITLAB.COM/Group/Project.git/  ", "https://gitlab.com/group/project"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, normalizeCacheKey(tt.input))
		})
	}
}

func TestProviderTypes(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "azure_devops", newAzureDevOpsProvider(AzureDevOpsConfig{PAT: "p"}).ProviderType())
	assert.Equal(t, "gitlab", newGitLabProvider(GitLabConfig{Token: "t"}).ProviderType())
}

func TestGitLabDefaultBaseURL(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "https://gitlab.com", newGitLabProvider(GitLabConfig{Token: "t"}).baseURL)
	assert.Equal(t, "https://git.example.com", newGitLabProvider(GitLabConfig{Token: "t", BaseURL: "https://git.example.com/"}).baseURL)
}

package gitprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectProvider(t *testing.T) {
	t.Parallel()
	registry := mustNewRegistry(t, Config{
		AzureDevOps: AzureDevOpsConfig{PAT: "test-pat"},
		GitHub: GitHubConfig{
			Token:               "test-token",
			AllowedRepositories: []string{"user/repo"},
		},
		GitLab: GitLabConfig{Token: "test-token", BaseURL: "https://gitlab.com"},
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
		{"GitHub HTTPS", "https://github.com/user/repo", "github", false},
		{"GitHub HTTPS .git", "https://github.com/user/repo.git", "github", false},
		{"GitHub SSH", "git@github.com:user/repo.git", "github", false},
		{"GitHub query cannot reroute to GitLab", "https://github.com/user/repo?target=gitlab.com", "", true},
		{"GitHub credentials rejected", "https://user:secret@github.com/user/repo", "", true},
		{"Unknown", "https://example.com/user/repo", "", true},
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
	registry := mustNewRegistry(t, Config{
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
	registry := mustNewRegistry(t, Config{})
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

	registry := mustNewRegistry(t, Config{GitLab: GitLabConfig{Token: "test-token", BaseURL: server.URL}})
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

	registry := mustNewRegistry(t, Config{GitLab: GitLabConfig{Token: "test-token", BaseURL: server.URL}})
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

func TestRegistryConcurrentIdenticalMissesUseOneProviderCall(t *testing.T) {
	t.Parallel()

	const callerCount = 32
	var providerCalls atomic.Int32
	releaseProvider := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		providerCalls.Add(1)
		<-releaseProvider
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"main"},{"name":"develop"}]`))
	}))
	defer server.Close()

	registry := mustNewRegistry(t, Config{})
	registry.github.baseURL = server.URL
	registry.github.httpClient = server.Client()

	type result struct {
		branches []Branch
		err      error
	}
	results := make(chan result, callerCount)
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(callerCount)
	for range callerCount {
		go func() {
			ready.Done()
			<-start
			branches, err := registry.ListBranches(context.Background(), "https://github.com/octo-org/octo-repo")
			results <- result{branches: branches, err: err}
		}()
	}
	ready.Wait()
	close(start)

	key := normalizeCacheKey("https://github.com/octo-org/octo-repo")
	deadline := time.After(5 * time.Second)
	for {
		registry.fillMu.Lock()
		fill := registry.inflight[key]
		waiters := 0
		if fill != nil {
			waiters = fill.waiters
		}
		registry.fillMu.Unlock()
		if waiters == callerCount {
			break
		}
		select {
		case <-deadline:
			require.FailNow(t, "callers did not join shared fill", "joined %d of %d", waiters, callerCount)
		default:
			runtime.Gosched()
		}
	}
	close(releaseProvider)

	expected := []Branch{{Name: "main"}, {Name: "develop"}}
	for range callerCount {
		result := <-results
		assert.NoError(t, result.err)
		assert.Equal(t, expected, result.branches)
	}
	assert.Equal(t, int32(1), providerCalls.Load())
}

func TestRegistryCancelledCallerDoesNotPoisonRetry(t *testing.T) {
	t.Parallel()

	var providerCalls atomic.Int32
	firstRequestStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if providerCalls.Add(1) == 1 {
			close(firstRequestStarted)
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"main"}]`))
	}))
	defer server.Close()

	registry := mustNewRegistry(t, Config{})
	registry.github.baseURL = server.URL
	registry.github.httpClient = server.Client()
	ctx, cancel := context.WithCancel(context.Background())
	firstResult := make(chan error, 1)
	go func() {
		_, err := registry.ListBranches(ctx, "https://github.com/octo-org/octo-repo")
		firstResult <- err
	}()
	<-firstRequestStarted
	cancel()

	assert.ErrorIs(t, <-firstResult, context.Canceled)
	branches, err := registry.ListBranches(context.Background(), "https://github.com/octo-org/octo-repo")
	assert.NoError(t, err)
	assert.Equal(t, []Branch{{Name: "main"}}, branches)

	cached, err := registry.ListBranches(context.Background(), "https://github.com/octo-org/octo-repo")
	assert.NoError(t, err)
	assert.Equal(t, branches, cached)
	assert.Equal(t, int32(2), providerCalls.Load())
}

func TestRegistryFailedFillIsNotCached(t *testing.T) {
	t.Parallel()

	var providerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if providerCalls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"main"}]`))
	}))
	defer server.Close()

	registry := mustNewRegistry(t, Config{})
	registry.github.baseURL = server.URL
	registry.github.httpClient = server.Client()
	repoURL := "https://github.com/octo-org/octo-repo"

	_, err := registry.ListBranches(context.Background(), repoURL)
	assert.Error(t, err)
	branches, err := registry.ListBranches(context.Background(), repoURL)
	assert.NoError(t, err)
	assert.Equal(t, []Branch{{Name: "main"}}, branches)
	_, err = registry.ListBranches(context.Background(), repoURL)
	assert.NoError(t, err)
	assert.Equal(t, int32(2), providerCalls.Load())
}

func TestRegistryCacheIsBoundedAndRetainsTTL(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 25, 12, 0, 0, 0, time.UTC)
	registry := mustNewRegistry(t, Config{})
	registry.cacheCapacity = 3
	registry.nowFunc = func() time.Time { return now }

	for index := 0; index < registry.cacheCapacity+1; index++ {
		key := fmt.Sprintf("repository-%d", index)
		insertedAt := now
		registry.storeCacheEntry(key, []Branch{{Name: "main"}})
		assert.Equal(t, insertedAt.Add(cacheTTL), registry.cache[key].expiresAt)
		now = now.Add(time.Millisecond)
	}

	assert.Len(t, registry.cache, registry.cacheCapacity)
	assert.NotContains(t, registry.cache, "repository-0")
	assert.Contains(t, registry.cache, "repository-3")
}

func TestRegistryCacheRetainedBranchBudget(t *testing.T) {
	t.Parallel()

	const (
		repositoryCount      = 64
		retainedBranchBudget = 10 * githubMaxBranches
	)
	registry := mustNewRegistry(t, Config{})
	branches := make([]Branch, githubMaxBranches)
	for index := range branches {
		branches[index] = Branch{Name: fmt.Sprintf("branch-%d", index)}
	}

	for index := range repositoryCount {
		registry.storeCacheEntry(fmt.Sprintf("repository-%d", index), append([]Branch(nil), branches...))
	}

	retainedBranches := 0
	for _, entry := range registry.cache {
		retainedBranches += len(entry.branches)
	}
	assert.LessOrEqual(t, retainedBranches, retainedBranchBudget)
	assert.LessOrEqual(t, len(registry.cache), retainedBranchBudget/githubMaxBranches)
}

func TestRegistryCacheEvictsExpiredEntriesFirst(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 25, 12, 0, 0, 0, time.UTC)
	registry := mustNewRegistry(t, Config{})
	registry.cacheCapacity = 2
	registry.nowFunc = func() time.Time { return now }
	registry.cache["expired"] = cacheEntry{expiresAt: now.Add(-time.Second)}
	registry.cache["fresh"] = cacheEntry{expiresAt: now.Add(time.Minute)}

	registry.storeCacheEntry("new", []Branch{{Name: "main"}})

	assert.Len(t, registry.cache, 2)
	assert.NotContains(t, registry.cache, "expired")
	assert.Contains(t, registry.cache, "fresh")
	assert.Contains(t, registry.cache, "new")
}

func TestRegistryUnsupportedProviderErrorDoesNotReflectURL(t *testing.T) {
	t.Parallel()

	registry := mustNewRegistry(t, Config{})
	_, err := registry.detectProvider("https://user:credential-marker@example.com/owner/repo")
	require.Error(t, err)
	assert.EqualError(t, err, "unsupported Git provider URL")
	assert.NotContains(t, err.Error(), "credential-marker")
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

	registry := mustNewRegistry(t, Config{GitLab: GitLabConfig{Token: "test-token", BaseURL: server.URL}})
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

	registry := mustNewRegistry(t, Config{GitLab: GitLabConfig{Token: "test-token", BaseURL: server.URL}})
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

	registry := mustNewRegistry(t, Config{GitLab: GitLabConfig{Token: "test-token", BaseURL: server.URL}})
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
		name     string
		config   Config
		expected map[string]bool
	}{
		{"Both", Config{AzureDevOps: AzureDevOpsConfig{PAT: "p"}, GitLab: GitLabConfig{Token: "t"}}, map[string]bool{"azure_devops": true, "github": true, "gitlab": true}},
		{"Only AzDO", Config{AzureDevOps: AzureDevOpsConfig{PAT: "p"}}, map[string]bool{"azure_devops": true, "github": true, "gitlab": false}},
		{"Only GitLab", Config{GitLab: GitLabConfig{Token: "t"}}, map[string]bool{"azure_devops": false, "github": true, "gitlab": true}},
		{"None", Config{}, map[string]bool{"azure_devops": false, "github": true, "gitlab": false}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			statuses := mustNewRegistry(t, tt.config).GetProviderStatus()
			assert.Len(t, statuses, len(tt.expected))
			actual := make(map[string]bool, len(statuses))
			for _, status := range statuses {
				actual[status.Type] = status.Available
			}
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestGetProviderStatusRemainsExactWithGitHubConfig(t *testing.T) {
	t.Parallel()

	expected := []ProviderStatus{
		{Type: "azure_devops", Available: false},
		{Type: "github", Available: true},
		{Type: "gitlab", Available: false},
	}
	tests := []struct {
		name   string
		config GitHubConfig
	}{
		{name: "public access", config: GitHubConfig{}},
		{name: "token", config: GitHubConfig{Token: "github-token-marker", AllowedRepositories: []string{"octo-org/repo"}}},
		{name: "allowlist", config: GitHubConfig{AllowedRepositories: []string{"octo-org/repo"}}},
		{
			name: "token and allowlist",
			config: GitHubConfig{
				Token:               "github-token-marker",
				AllowedRepositories: []string{"octo-org/repo"},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, expected, mustNewRegistry(t, Config{GitHub: tt.config}).GetProviderStatus())
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

func TestRegistryRepositoryURLClassification(t *testing.T) {
	t.Parallel()

	registry := mustNewRegistry(t, Config{})
	_, err := registry.detectProvider("github.com/octo-org/octo-repo")
	assert.ErrorIs(t, err, ErrInvalidRepositoryURL)
	assert.NotErrorIs(t, err, ErrUnsupportedProvider)

	_, err = registry.detectProvider("https://credential-marker@example.com/owner/repo")
	assert.ErrorIs(t, err, ErrUnsupportedProvider)
	assert.NotContains(t, err.Error(), "credential-marker")

	var providerErr *ProviderError
	assert.True(t, errors.As(err, &providerErr))
}

func TestGitHubConfigValidateAllowedRepositories(t *testing.T) {
	t.Parallel()

	assert.NoError(t, (GitHubConfig{}).Validate())
	assert.NoError(t, (GitHubConfig{
		Token:               "github-token-marker",
		AllowedRepositories: []string{"octo-org/octo-repo"},
	}).Validate())

	missingAllowlist := GitHubConfig{Token: "github-token-marker"}
	err := missingAllowlist.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRepositoryNotAllowed)
	assert.NotContains(t, err.Error(), "github-token-marker")

	valid := GitHubConfig{AllowedRepositories: []string{"octo-org/octo-repo", " OCTO-ORG/OTHER_REPO "}}
	assert.NoError(t, valid.Validate())
	assert.NoError(t, (Config{GitHub: valid}).Validate())

	invalid := GitHubConfig{AllowedRepositories: []string{"https://credential-marker@github.com/octo-org/octo-repo"}}
	err = invalid.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRepositoryURL)
	assert.NotContains(t, err.Error(), "credential-marker")
}

func TestNormalizeGitHubAllowedRepositories(t *testing.T) {
	t.Parallel()

	allowed, err := normalizeGitHubAllowedRepositories([]string{
		" Octo-Org/Repo ",
		"octo-org/repo",
		"OTHER/Second_Repo",
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]struct{}{
		"octo-org/repo":     {},
		"other/second_repo": {},
	}, allowed)

	malformed := []string{
		"",
		"octo-org",
		"octo-org/repo/extra",
		"https://github.com/octo-org/repo",
		"credential-marker@github.com/octo-org/repo",
		"octo--org/repo",
		"octo-org/..",
	}
	for _, entry := range malformed {
		entry := entry
		t.Run(entry, func(t *testing.T) {
			t.Parallel()
			_, err := normalizeGitHubAllowedRepositories([]string{entry})
			assert.ErrorIs(t, err, ErrInvalidRepositoryURL)
			assert.NotContains(t, err.Error(), "credential-marker")
			if entry != "" {
				assert.NotContains(t, err.Error(), entry)
			}
		})
	}
}

func TestRegistryGitHubAllowlistAcceptsExactRepositoryURLForms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		repoURL string
	}{
		{name: "HTTPS", repoURL: "https://github.com/octo-org/octo-repo"},
		{name: "HTTPS dot git", repoURL: "https://github.com/octo-org/octo-repo.git"},
		{name: "SCP SSH", repoURL: "git@github.com:octo-org/octo-repo"},
		{name: "SCP SSH dot git", repoURL: "git@github.com:octo-org/octo-repo.git"},
		{name: "SSH scheme", repoURL: "ssh://git@github.com/octo-org/octo-repo"},
		{name: "SSH scheme dot git", repoURL: "ssh://git@github.com/octo-org/octo-repo.git"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`[{"name":"main"}]`))
			}))
			defer server.Close()

			registry := mustNewRegistry(t, Config{GitHub: GitHubConfig{AllowedRepositories: []string{"octo-org/octo-repo"}}})
			registry.github.baseURL = server.URL
			registry.github.httpClient = server.Client()
			branches, err := registry.ListBranches(context.Background(), tt.repoURL)
			require.NoError(t, err)
			assert.Equal(t, []Branch{{Name: "main"}}, branches)
			assert.Equal(t, int32(1), requests.Load())
		})
	}
}

func TestRegistryGitHubAllowlistIsExactAndPrecedesCache(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"main"}]`))
	}))
	defer server.Close()

	registry := mustNewRegistry(t, Config{GitHub: GitHubConfig{AllowedRepositories: []string{"octo-org/allowed-repo"}}})
	registry.github.baseURL = server.URL
	registry.github.httpClient = server.Client()
	deniedURL := "https://github.com/octo-org/different-repo"
	registry.storeCacheEntry(normalizeCacheKey(deniedURL), []Branch{{Name: "cached-secret"}})

	_, err := registry.ListBranches(context.Background(), deniedURL)
	assert.ErrorIs(t, err, ErrRepositoryNotAllowed)
	_, err = registry.ValidateBranch(context.Background(), deniedURL, "main")
	assert.ErrorIs(t, err, ErrRepositoryNotAllowed)
	_, err = registry.GetDefaultBranch(context.Background(), deniedURL)
	assert.ErrorIs(t, err, ErrRepositoryNotAllowed)
	assert.Zero(t, requests.Load())
	assert.NotContains(t, err.Error(), "different-repo")
}

func TestRegistryGitHubAllowlistIsCaseInsensitive(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"main"}]`))
	}))
	defer server.Close()

	registry := mustNewRegistry(t, Config{GitHub: GitHubConfig{AllowedRepositories: []string{"OCTO-ORG/OCTO-REPO"}}})
	registry.github.baseURL = server.URL
	registry.github.httpClient = server.Client()
	branches, err := registry.ListBranches(context.Background(), "https://github.com/octo-org/octo-repo")
	require.NoError(t, err)
	assert.Equal(t, []Branch{{Name: "main"}}, branches)
}

func TestRegistryCacheWeightAndCopies(t *testing.T) {
	t.Parallel()

	registry := mustNewRegistry(t, Config{})
	registry.cacheWeightLimit = branchCacheEntryOverhead + branchCacheBranchOverhead + len("small") + len("main")
	branches := []Branch{{Name: "main"}}
	registry.storeCacheEntry("small", branches)
	branches[0].Name = "mutated"
	assert.Equal(t, "main", registry.cache["small"].branches[0].Name)

	registry.storeCacheEntry("oversized", []Branch{{Name: strings.Repeat("x", registry.cacheWeightLimit)}})
	assert.NotContains(t, registry.cache, "oversized")
	assert.LessOrEqual(t, retainedCacheWeight(registry.cache), registry.cacheWeightLimit)
}

func mustNewRegistry(t *testing.T, config Config) *Registry {
	t.Helper()
	registry, err := NewRegistry(config)
	require.NoError(t, err)
	require.NotNil(t, registry)
	return registry
}

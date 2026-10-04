package gitprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseGitHubURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		repoURL   string
		owner     string
		repo      string
		expectErr bool
	}{
		{name: "HTTPS", repoURL: "https://github.com/octo-org/octo-repo", owner: "octo-org", repo: "octo-repo"},
		{name: "HTTPS .git", repoURL: "https://github.com/octo-org/octo-repo.git", owner: "octo-org", repo: "octo-repo"},
		{name: "SSH", repoURL: "git@github.com:octo-org/octo-repo", owner: "octo-org", repo: "octo-repo"},
		{name: "SSH .git", repoURL: "git@github.com:octo-org/octo-repo.git", owner: "octo-org", repo: "octo-repo"},
		{name: "SSH scheme no suffix", repoURL: "ssh://git@github.com/octo-org/octo-repo", owner: "octo-org", repo: "octo-repo"},
		{name: "SSH scheme", repoURL: "ssh://git@github.com/octo-org/octo-repo.git", owner: "octo-org", repo: "octo-repo"},
		{name: "SSH scheme port", repoURL: "ssh://git@github.com:22/octo-org/octo-repo.git", owner: "octo-org", repo: "octo-repo"},
		{name: "Repository punctuation", repoURL: "https://github.com/octo-org/octo_repo.go.git", owner: "octo-org", repo: "octo_repo.go"},
		{name: "Whitespace", repoURL: "  https://github.com/octo-org/octo-repo.git  ", owner: "octo-org", repo: "octo-repo"},
		{name: "Empty", repoURL: "", expectErr: true},
		{name: "Schemeless", repoURL: "github.com/octo-org/octo-repo", expectErr: true},
		{name: "Wrong host", repoURL: "https://github.example.com/octo-org/octo-repo", expectErr: true},
		{name: "HTTP", repoURL: "http://github.com/octo-org/octo-repo", expectErr: true},
		{name: "FTP", repoURL: "ftp://github.com/octo-org/octo-repo", expectErr: true},
		{name: "HTTPS userinfo", repoURL: "https://user@github.com/octo-org/octo-repo", expectErr: true},
		{name: "HTTPS credentials", repoURL: "https://user:secret@github.com/octo-org/octo-repo", expectErr: true},
		{name: "SSH credentials", repoURL: "ssh://git:secret@github.com/octo-org/octo-repo", expectErr: true},
		{name: "SSH unexpected user", repoURL: "ssh://octocat@github.com/octo-org/octo-repo", expectErr: true},
		{name: "HTTPS port", repoURL: "https://github.com:443/octo-org/octo-repo", expectErr: true},
		{name: "SSH unexpected port", repoURL: "ssh://git@github.com:2222/octo-org/octo-repo", expectErr: true},
		{name: "Query string", repoURL: "https://github.com/octo-org/octo-repo?token=secret", expectErr: true},
		{name: "Fragment", repoURL: "https://github.com/octo-org/octo-repo#main", expectErr: true},
		{name: "Malformed escape", repoURL: "https://github.com/octo-org/%zz", expectErr: true},
		{name: "Missing repo", repoURL: "https://github.com/octo-org", expectErr: true},
		{name: "Extra path", repoURL: "https://github.com/octo-org/octo-repo/tree/main", expectErr: true},
		{name: "Owner dot segment", repoURL: "https://github.com/../octo-repo", expectErr: true},
		{name: "Repo dot segment", repoURL: "https://github.com/octo-org/..", expectErr: true},
		{name: "Encoded dot segment", repoURL: "https://github.com/octo-org/%2e%2e", expectErr: true},
		{name: "Encoded path separator", repoURL: "https://github.com/octo-org%2fother/octo-repo", expectErr: true},
		{name: "Encoded owner character", repoURL: "https://github.com/octo%2dorg/octo-repo", expectErr: true},
		{name: "Owner starts hyphen", repoURL: "https://github.com/-octo/octo-repo", expectErr: true},
		{name: "Owner ends hyphen", repoURL: "https://github.com/octo-/octo-repo", expectErr: true},
		{name: "Owner consecutive hyphens", repoURL: "https://github.com/octo--org/octo-repo", expectErr: true},
		{name: "Owner underscore", repoURL: "https://github.com/octo_org/octo-repo", expectErr: true},
		{name: "Owner too long", repoURL: "https://github.com/abcdefghijklmnopqrstuvwxyzabcdefghijklmn/octo-repo", expectErr: true},
		{name: "Repo whitespace", repoURL: "https://github.com/octo-org/octo%20repo", expectErr: true},
		{name: "Repo colon", repoURL: "git@github.com:octo-org/octo:repo", expectErr: true},
		{name: "Repo too long", repoURL: "https://github.com/octo-org/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", expectErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			info, err := parseGitHubURL(tt.repoURL)
			if tt.expectErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.owner, info.Owner)
			assert.Equal(t, tt.repo, info.Repo)
		})
	}
}

func TestParseGitHubURLDoesNotReflectCredentials(t *testing.T) {
	t.Parallel()

	_, err := parseGitHubURL("https://octocat:credential-marker@github.com/octo-org/octo-repo")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "octocat")
	assert.NotContains(t, err.Error(), "credential-marker")
}

func TestGitHubListBranches(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/repos/octo-org/octo-repo/branches", r.URL.Path)
		assert.Equal(t, "100", r.URL.Query().Get("per_page"))
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		assert.Equal(t, "application/vnd.github+json", r.Header.Get("Accept"))
		assert.Equal(t, githubAPIVersion, r.Header.Get("X-GitHub-Api-Version"))
		assert.Equal(t, "k8s-stack-manager", r.Header.Get("User-Agent"))
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode([]githubBranchResponse{{Name: "main"}, {Name: "develop"}}))
	}))
	defer server.Close()

	provider := newTestGitHubProvider(server, "test-token")
	branches, err := provider.ListBranches(context.Background(), "https://github.com/octo-org/octo-repo.git")
	require.NoError(t, err)
	assert.Equal(t, []Branch{{Name: "main"}, {Name: "develop"}}, branches)
}

func TestGitHubListBranchesPagination(t *testing.T) {
	t.Parallel()

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page") {
		case "":
			w.Header().Set("Link", `<`+"http://"+r.Host+r.URL.Path+`?per_page=100&page=2>; rel="next", <`+"http://"+r.Host+r.URL.Path+`?per_page=100&page=2>; rel="last"`)
			_, _ = w.Write([]byte(`[{"name":"main"}]`))
		case "2":
			_, _ = w.Write([]byte(`[{"name":"release"}]`))
		default:
			t.Fatalf("unexpected page %q", r.URL.Query().Get("page"))
		}
	}))
	defer server.Close()

	provider := newTestGitHubProvider(server, "test-token")
	branches, err := provider.ListBranches(context.Background(), "https://github.com/octo-org/octo-repo")
	require.NoError(t, err)
	assert.Equal(t, []Branch{{Name: "main"}, {Name: "release"}}, branches)
	assert.Equal(t, 2, requestCount)
}

func TestGitHubListBranchesCanonicalRepositoryPagination(t *testing.T) {
	t.Parallel()

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/octo-org/octo-repo/branches":
			w.Header().Set("Link", `<http://`+r.Host+`/repositories/123456/branches?per_page=100&page=2>; rel="next"`)
			_, _ = w.Write([]byte(`[{"name":"main"}]`))
		case "/repositories/123456/branches":
			assert.Equal(t, "2", r.URL.Query().Get("page"))
			_, _ = w.Write([]byte(`[{"name":"release"}]`))
		default:
			t.Fatalf("unexpected pagination path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	provider := newTestGitHubProvider(server, "test-token")
	branches, err := provider.ListBranches(context.Background(), "https://github.com/octo-org/octo-repo")
	require.NoError(t, err)
	assert.Equal(t, []Branch{{Name: "main"}, {Name: "release"}}, branches)
	assert.Equal(t, 2, requestCount)
}

func TestGitHubListBranchesCanonicalRepositoryRemainsPinned(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		nextPath string
	}{
		{name: "canonical repository ID cannot change", nextPath: "/repositories/654321/branches"},
		{name: "canonical path cannot return to owner repository", nextPath: "/repos/octo-org/octo-repo/branches"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			requestedPaths := make(chan string, 3)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestedPaths <- r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/repos/octo-org/octo-repo/branches":
					w.Header().Set("Link", `<http://`+r.Host+`/repositories/123456/branches?page=2>; rel="next"`)
				case "/repositories/123456/branches":
					w.Header().Set("Link", `<http://`+r.Host+tt.nextPath+`?page=3>; rel="next"`)
				}
				_, _ = w.Write([]byte(`[{"name":"main"}]`))
			}))
			defer server.Close()

			provider := newTestGitHubProvider(server, "test-token")
			_, err := provider.ListBranches(context.Background(), "https://github.com/octo-org/octo-repo")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid GitHub pagination URL")
			assert.Equal(t, "/repos/octo-org/octo-repo/branches", <-requestedPaths)
			assert.Equal(t, "/repositories/123456/branches", <-requestedPaths)
			assert.Empty(t, requestedPaths)
		})
	}
}

func TestGitHubNextPageURLRejectsUnsafeForms(t *testing.T) {
	t.Parallel()

	const (
		baseURL      = "https://api.github.test"
		expectedPath = "/repos/octo-org/octo-repo/branches"
	)
	responseURL, err := url.Parse(baseURL + expectedPath + "?per_page=100")
	require.NoError(t, err)

	tests := []struct {
		name       string
		linkHeader string
		response   *url.URL
	}{
		{
			name:       "fragment",
			linkHeader: `<https://api.github.test/repos/octo-org/octo-repo/branches?page=2#credential-marker>; rel="next"`,
			response:   responseURL,
		},
		{
			name:       "encoded RawPath",
			linkHeader: `<https://api.github.test/repos/octo-org/octo-repo/%62ranches?page=2>; rel="next"`,
			response:   responseURL,
		},
		{
			name:       "malformed Link target",
			linkHeader: `<://invalid>; rel="next"`,
			response:   responseURL,
		},
		{
			name:       "encoded response RawPath",
			linkHeader: `<https://api.github.test/repos/octo-org/octo-repo/branches?page=2>; rel="next"`,
			response: &url.URL{
				Scheme: "https", Host: "api.github.test", Path: expectedPath,
				RawPath: "/repos/octo-org/octo-repo/%62ranches",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			next, paginationPath, err := githubNextPageURL(tt.linkHeader, baseURL, tt.response, expectedPath, expectedPath)
			assert.Error(t, err)
			assert.Empty(t, next)
			assert.Equal(t, expectedPath, paginationPath)
			assert.NotContains(t, err.Error(), "credential-marker")
		})
	}
}

func TestGitHubListBranchesRejectsCrossOriginPagination(t *testing.T) {
	t.Parallel()

	forwardedRequests := 0
	unexpectedServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		forwardedRequests++
	}))
	defer unexpectedServer.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Link", `<`+unexpectedServer.URL+`/branches?page=2>; rel="next"`)
		_, _ = w.Write([]byte(`[{"name":"main"}]`))
	}))
	defer server.Close()

	provider := newTestGitHubProvider(server, "secret-token")
	_, err := provider.ListBranches(context.Background(), "https://github.com/octo-org/octo-repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid GitHub pagination URL")
	assert.NotContains(t, err.Error(), unexpectedServer.URL)
	assert.NotContains(t, err.Error(), "secret-token")
	assert.Zero(t, forwardedRequests)
}

func TestGitHubListBranchesRejectsUnexpectedPaginationPath(t *testing.T) {
	t.Parallel()

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Link", `<http://`+r.Host+`/user?page=2>; rel="next"`)
		_, _ = w.Write([]byte(`[{"name":"main"}]`))
	}))
	defer server.Close()

	provider := newTestGitHubProvider(server, "secret-token")
	_, err := provider.ListBranches(context.Background(), "https://github.com/octo-org/octo-repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid GitHub pagination URL")
	assert.Equal(t, 1, requestCount)
}

func TestGitHubListBranchesRejectsPaginationCycle(t *testing.T) {
	t.Parallel()

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Link", `<http://`+r.Host+r.URL.Path+`?per_page=100>; rel="next"`)
		_, _ = w.Write([]byte(`[{"name":"main"}]`))
	}))
	defer server.Close()

	provider := newTestGitHubProvider(server, "test-token")
	_, err := provider.ListBranches(context.Background(), "https://github.com/octo-org/octo-repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid GitHub pagination URL")
	assert.Equal(t, 1, requestCount)
}

func TestGitHubListBranchesBoundsPagination(t *testing.T) {
	t.Parallel()

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		page := requestCount + 1
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Link", `<http://`+r.Host+r.URL.Path+`?per_page=100&page=`+strconv.Itoa(page)+`>; rel="next"`)
		_, _ = w.Write([]byte(`[{"name":"main"}]`))
	}))
	defer server.Close()

	provider := newTestGitHubProvider(server, "test-token")
	_, err := provider.ListBranches(context.Background(), "https://github.com/octo-org/octo-repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pagination limit exceeded")
	assert.Equal(t, githubMaxPages, requestCount)
}

func TestGitHubListBranchesAllowsExactPaginationLimit(t *testing.T) {
	t.Parallel()

	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := int(requestCount.Add(1))
		w.Header().Set("Content-Type", "application/json")
		if page < githubMaxPages {
			w.Header().Set("Link", `<http://`+r.Host+r.URL.Path+`?per_page=100&page=`+strconv.Itoa(page+1)+`>; rel="next"`)
		}
		_, _ = w.Write([]byte(`[{"name":"main"}]`))
	}))
	defer server.Close()

	provider := newTestGitHubProvider(server, "test-token")
	branches, err := provider.ListBranches(context.Background(), "https://github.com/octo-org/octo-repo")
	require.NoError(t, err)
	assert.Len(t, branches, githubMaxPages)
	assert.Equal(t, int32(githubMaxPages), requestCount.Load())
}

func TestGitHubListBranchesBoundsCollection(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		response := make([]githubBranchResponse, githubMaxBranches+1)
		for index := range response {
			response[index].Name = fmt.Sprintf("branch-%d", index)
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(response))
	}))
	defer server.Close()

	provider := newTestGitHubProvider(server, "test-token")
	_, err := provider.ListBranches(context.Background(), "https://github.com/octo-org/octo-repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collection limit exceeded")
}

func TestGitHubListBranchesAllowsExactCollectionLimit(t *testing.T) {
	t.Parallel()

	response := make([]githubBranchResponse, githubMaxBranches)
	for index := range response {
		response[index].Name = fmt.Sprintf("branch-%d", index)
	}
	body, err := json.Marshal(response)
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer server.Close()

	provider := newTestGitHubProvider(server, "test-token")
	branches, err := provider.ListBranches(context.Background(), "https://github.com/octo-org/octo-repo")
	require.NoError(t, err)
	assert.Len(t, branches, githubMaxBranches)
}

func TestGitHubResponseSizeLimit(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"marker":"response-secret-marker","padding":"` + strings.Repeat("x", githubMaxResponseBytes) + `"}`))
	}))
	defer server.Close()

	provider := newTestGitHubProvider(server, "token-secret-marker")
	_, err := provider.ListBranches(context.Background(), "https://github.com/octo-org/octo-repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "response exceeds size limit")
	assert.NotContains(t, err.Error(), "response-secret-marker")
	assert.NotContains(t, err.Error(), "token-secret-marker")
}

func TestGitHubOperationTimeout(t *testing.T) {
	t.Parallel()

	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		deadline, ok := req.Context().Deadline()
		require.True(t, ok)
		assert.LessOrEqual(t, time.Until(deadline), 50*time.Millisecond)
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}
	provider := newGitHubProviderWithClient(GitHubConfig{Token: "secret-token"}, "https://api.github.test", client)
	provider.timeout = 20 * time.Millisecond

	_, err := provider.ListBranches(context.Background(), "https://github.com/octo-org/octo-repo")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestGitHubRejectsCrossOriginRedirectWithoutForwardingAuthorization(t *testing.T) {
	t.Parallel()

	var forwardedRequests atomic.Int32
	var forwardedAuthorization atomic.Value
	untrusted := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		forwardedRequests.Add(1)
		forwardedAuthorization.Store(r.Header.Get("Authorization"))
	}))
	defer untrusted.Close()

	trusted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, untrusted.URL+"/capture?marker=redirect-secret-marker", http.StatusFound)
	}))
	defer trusted.Close()

	provider := newTestGitHubProvider(trusted, "token-secret-marker")
	_, err := provider.ListBranches(context.Background(), "https://github.com/octo-org/octo-repo")
	require.Error(t, err)
	assert.ErrorIs(t, err, errGitHubRedirect)
	assert.NotContains(t, err.Error(), untrusted.URL)
	assert.NotContains(t, err.Error(), "redirect-secret-marker")
	assert.NotContains(t, err.Error(), "token-secret-marker")
	assert.Zero(t, forwardedRequests.Load())
	assert.Nil(t, forwardedAuthorization.Load())
}

func TestGitHubAllowsSameOriginRedirect(t *testing.T) {
	t.Parallel()

	observations := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observations <- r.URL.Path + " " + r.Header.Get("Authorization")
		if r.URL.Path == "/repos/octo-org/octo-repo/branches" {
			http.Redirect(w, r, "/redirected-branches", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"main"}]`))
	}))
	defer server.Close()

	provider := newTestGitHubProvider(server, "test-token")
	branches, err := provider.ListBranches(context.Background(), "https://github.com/octo-org/octo-repo")
	require.NoError(t, err)
	assert.Equal(t, []Branch{{Name: "main"}}, branches)
	assert.Equal(t, "/repos/octo-org/octo-repo/branches Bearer test-token", <-observations)
	assert.Equal(t, "/redirected-branches Bearer test-token", <-observations)
}

func TestGitHubRedirectPolicyRejectsUnsafeTargets(t *testing.T) {
	t.Parallel()

	trustedRequest := &http.Request{URL: &url.URL{Scheme: "https", Host: "api.github.test", Path: "/branches"}}
	tests := []struct {
		name    string
		request *http.Request
		via     []*http.Request
	}{
		{
			name:    "scheme downgrade",
			request: &http.Request{URL: &url.URL{Scheme: "http", Host: "api.github.test", Path: "/branches"}},
		},
		{
			name:    "userinfo",
			request: &http.Request{URL: &url.URL{Scheme: "https", Host: "api.github.test", User: url.User("credential-marker"), Path: "/branches"}},
		},
		{
			name:    "cross origin",
			request: &http.Request{URL: &url.URL{Scheme: "https", Host: "untrusted.test", Path: "/branches"}},
		},
		{
			name:    "maximum hops",
			request: trustedRequest,
			via:     make([]*http.Request, 10),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := githubRedirectPolicy("https://api.github.test", nil)(tt.request, tt.via)
			assert.ErrorIs(t, err, errGitHubRedirect)
			assert.NotContains(t, err.Error(), "credential-marker")
		})
	}
}

func TestGitHubRedirectPolicyAppliesFallbackAfterTrustChecks(t *testing.T) {
	t.Parallel()

	fallbackErr := errors.New("fallback rejected redirect")
	called := false
	policy := githubRedirectPolicy("https://api.github.test", func(_ *http.Request, _ []*http.Request) error {
		called = true
		return fallbackErr
	})

	trusted := &http.Request{URL: &url.URL{Scheme: "https", Host: "API.GITHUB.TEST", Path: "/branches"}}
	assert.ErrorIs(t, policy(trusted, nil), fallbackErr)
	assert.True(t, called)

	called = false
	untrusted := &http.Request{URL: &url.URL{Scheme: "https", Host: "untrusted.test", Path: "/branches"}}
	assert.ErrorIs(t, policy(untrusted, nil), errGitHubRedirect)
	assert.False(t, called)
}

func TestGitHubSameOriginRedirectLoopIsBounded(t *testing.T) {
	t.Parallel()

	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requestCount.Add(1) <= 10 {
			http.Redirect(w, r, r.URL.RequestURI(), http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	provider := newTestGitHubProvider(server, "test-token")
	_, err := provider.ListBranches(context.Background(), "https://github.com/octo-org/octo-repo")

	assert.ErrorIs(t, err, errGitHubRedirect)
	assert.Equal(t, int32(10), requestCount.Load())
}

func TestGitHubOptionalAuthentication(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"main"}]`))
	}))
	defer server.Close()

	provider := newTestGitHubProvider(server, "")
	_, err := provider.ListBranches(context.Background(), "git@github.com:octo-org/octo-repo.git")
	require.NoError(t, err)
}

func TestGitHubListBranchesEmptyResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	provider := newTestGitHubProvider(server, "")
	branches, err := provider.ListBranches(context.Background(), "https://github.com/octo-org/empty-repo")
	require.NoError(t, err)
	assert.NotNil(t, branches)
	assert.Empty(t, branches)
}

func TestGitHubGetDefaultBranch(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/octo-org/octo-repo", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"default_branch":"trunk"}`))
	}))
	defer server.Close()

	provider := newTestGitHubProvider(server, "test-token")
	branch, err := provider.GetDefaultBranch(context.Background(), "https://github.com/octo-org/octo-repo")
	require.NoError(t, err)
	assert.Equal(t, "trunk", branch)
}

func TestGitHubGetDefaultBranchInvalidResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response string
		want     string
	}{
		{name: "Missing default branch", response: `{}`, want: "has no default branch"},
		{name: "Malformed JSON", response: `{`, want: "decode GitHub response"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()

			provider := newTestGitHubProvider(server, "test-token")
			_, err := provider.GetDefaultBranch(context.Background(), "https://github.com/octo-org/octo-repo")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestGitHubValidateBranch(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"main"},{"name":"feature/one"}]`))
	}))
	defer server.Close()

	provider := newTestGitHubProvider(server, "test-token")
	exists, err := provider.ValidateBranch(context.Background(), "https://github.com/octo-org/octo-repo", "feature/one")
	require.NoError(t, err)
	assert.True(t, exists)

	exists, err = provider.ValidateBranch(context.Background(), "https://github.com/octo-org/octo-repo", "missing")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestGitHubAPIErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
		headers    map[string]string
		want       string
	}{
		{name: "Unauthorized", statusCode: http.StatusUnauthorized, want: "check GITHUB_TOKEN"},
		{name: "Not found", statusCode: http.StatusNotFound, want: "repository not found"},
		{name: "Forbidden", statusCode: http.StatusForbidden, want: "check token permissions or rate limits"},
		{name: "Server error", statusCode: http.StatusInternalServerError, want: "HTTP 500"},
		{name: "Rate limit without valid reset", statusCode: http.StatusForbidden, headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "invalid"}, want: "retry later or configure GITHUB_TOKEN"},
		{name: "Rate limit without reset", statusCode: http.StatusForbidden, headers: map[string]string{"X-RateLimit-Remaining": "0"}, want: "retry later or configure GITHUB_TOKEN"},
		{name: "Forbidden with malformed remaining", statusCode: http.StatusForbidden, headers: map[string]string{"X-RateLimit-Remaining": "invalid"}, want: "check token permissions or rate limits"},
		{name: "Too many requests", statusCode: http.StatusTooManyRequests, want: "rate limit exceeded; retry later"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for name, value := range tt.headers {
					w.Header().Set(name, value)
				}
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte("secret-token-in-response"))
			}))
			defer server.Close()

			provider := newTestGitHubProvider(server, "secret-token")
			_, err := provider.ListBranches(context.Background(), "https://github.com/octo-org/octo-repo")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.NotContains(t, err.Error(), "secret-token")
		})
	}
}

func TestGitHubRateLimitReset(t *testing.T) {
	t.Parallel()

	reset := time.Date(2026, time.September, 25, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1790337600")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	provider := newTestGitHubProvider(server, "test-token")
	_, err := provider.ListBranches(context.Background(), "https://github.com/octo-org/octo-repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), reset.Format(time.RFC3339))
	assert.ErrorIs(t, err, ErrRateLimited)
	var rateErr *RateLimitError
	require.ErrorAs(t, err, &rateErr)
	assert.Equal(t, reset, rateErr.RetryAfter)
}

func TestGitHubRateLimitRetryAfterSeconds(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	provider := newTestGitHubProvider(server, "test-token")
	before := time.Now().Add(120 * time.Second)
	_, err := provider.ListBranches(context.Background(), "https://github.com/octo-org/octo-repo")
	after := time.Now().Add(120 * time.Second)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRateLimited)
	var rateErr *RateLimitError
	require.ErrorAs(t, err, &rateErr)
	assert.False(t, rateErr.RetryAfter.Before(before))
	assert.False(t, rateErr.RetryAfter.After(after))
}

func TestGitHubRetryAfterAndErrorClassification(t *testing.T) {
	t.Parallel()

	retryAt := time.Date(2026, time.October, 5, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name               string
		retryAfter         string
		rateLimitRemaining string
		kind               error
		statusCode         int
	}{
		{name: "authentication", statusCode: http.StatusUnauthorized, kind: ErrAuthentication},
		{name: "permission", statusCode: http.StatusForbidden, kind: ErrAuthentication},
		{name: "forbidden with retry after", statusCode: http.StatusForbidden, kind: ErrRateLimited, retryAfter: retryAt.Format(http.TimeFormat), rateLimitRemaining: "42"},
		{name: "not found", statusCode: http.StatusNotFound, kind: ErrRepositoryNotFound},
		{name: "rate limited", statusCode: http.StatusTooManyRequests, kind: ErrRateLimited, retryAfter: retryAt.Format(http.TimeFormat)},
		{name: "upstream", statusCode: http.StatusBadGateway, kind: ErrUpstreamFailure},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tt.retryAfter != "" {
					w.Header().Set("Retry-After", tt.retryAfter)
				}
				if tt.rateLimitRemaining != "" {
					w.Header().Set("X-RateLimit-Remaining", tt.rateLimitRemaining)
				}
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte("credential-marker"))
			}))
			defer server.Close()

			provider := newTestGitHubProvider(server, "token-marker")
			_, err := provider.ListBranches(context.Background(), "https://github.com/octo-org/octo-repo")
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.kind)
			assert.NotContains(t, err.Error(), "credential-marker")
			assert.NotContains(t, err.Error(), "token-marker")
			if tt.kind == ErrRateLimited {
				var rateErr *RateLimitError
				require.ErrorAs(t, err, &rateErr)
				assert.Equal(t, retryAt, rateErr.RetryAfter)
				return
			}
			var providerErr *ProviderError
			require.ErrorAs(t, err, &providerErr)
		})
	}
}

func TestRegistryGitHubCache(t *testing.T) {
	t.Parallel()

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"main"}]`))
	}))
	defer server.Close()

	registry := mustNewRegistry(t, Config{})
	registry.github.baseURL = server.URL
	registry.github.httpClient = server.Client()

	first, err := registry.ListBranches(context.Background(), "https://github.com/octo-org/octo-repo.git")
	require.NoError(t, err)
	second, err := registry.ListBranches(context.Background(), "https://github.com/octo-org/octo-repo")
	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.Equal(t, 1, callCount)
}

func TestRegistryGitHubValidationUsesCachedBranches(t *testing.T) {
	t.Parallel()

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"main"}]`))
	}))
	defer server.Close()

	registry := mustNewRegistry(t, Config{})
	registry.github.baseURL = server.URL
	registry.github.httpClient = server.Client()

	_, err := registry.ListBranches(context.Background(), "https://github.com/octo-org/octo-repo.git")
	require.NoError(t, err)
	exists, err := registry.ValidateBranch(context.Background(), "https://github.com/octo-org/octo-repo", "main")
	require.NoError(t, err)
	assert.True(t, exists)
	assert.Equal(t, 1, callCount)
}

func TestGitHubProviderType(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "github", newGitHubProvider(GitHubConfig{}).ProviderType())
}

func newTestGitHubProvider(server *httptest.Server, token string) *githubProvider {
	return newGitHubProviderWithClient(GitHubConfig{Token: token}, server.URL, server.Client())
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

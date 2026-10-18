package gitprovider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGitHubRejectsDisallowedAPIRedirects(t *testing.T) {
	t.Parallel()

	// Each server answers the first (authenticated) API request with a
	// redirect; every later request counts as a followed redirect.
	tests := []struct {
		name     string
		location string
		token    string
	}{
		{
			name:     "same origin redirect with encoded raw path",
			location: "/repos/octo-org/octo-repo%2Fbranches",
			token:    "encoded-path-token-marker",
		},
		{
			name:     "renamed repository redirect to unexpected canonical path",
			location: "/repositories/12345/other",
			token:    "redirect-token-marker",
		},
		{
			name:     "redirect to a different repository",
			location: "/repos/other-org/other-repo/branches",
			token:    "other-repo-token-marker",
		},
		{
			name:     "same path redirect with fragment",
			location: "/repos/octo-org/octo-repo/branches#fragment",
			token:    "fragment-token-marker",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var apiRequests, targetRequests atomic.Int32
			var targetAuthorization atomic.Value
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if apiRequests.Add(1) == 1 {
					assert.Equal(t, "/repos/octo-org/octo-repo/branches", r.URL.Path)
					assert.Empty(t, r.URL.RawPath)
					w.Header().Set("Location", tt.location)
					w.WriteHeader(http.StatusFound)
					return
				}

				targetRequests.Add(1)
				targetAuthorization.Store(r.Header.Get("Authorization"))
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`[{"name":"main"}]`))
			}))
			t.Cleanup(server.Close)

			provider := newTestGitHubProvider(server, tt.token)
			_, err := provider.ListBranches(context.Background(), "https://github.com/octo-org/octo-repo")
			require.Error(t, err)
			assert.ErrorIs(t, err, errGitHubRedirect)
			assert.NotContains(t, err.Error(), tt.token)
			assert.Zero(t, targetRequests.Load(), "redirect must not be followed")
			assert.Nil(t, targetAuthorization.Load(), "token must not be sent to the redirect target")
		})
	}
}

func TestGitHubFollowsRenamedRepositoryRedirectForBranchesWithPagination(t *testing.T) {
	t.Parallel()

	var originalRequests, canonicalRequests atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/repos/octo-org/octo-repo/branches":
			originalRequests.Add(1)
			http.Redirect(w, r, "/repositories/12345/branches?"+r.URL.RawQuery, http.StatusMovedPermanently)
		case "/repositories/12345/branches":
			canonicalRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Query().Get("page") == "2" {
				_, _ = w.Write([]byte(`[{"name":"develop"}]`))
				return
			}
			w.Header().Set("Link", "<"+server.URL+"/repositories/12345/branches?per_page=100&page=2>; rel=\"next\"")
			_, _ = w.Write([]byte(`[{"name":"main"}]`))
		default:
			assert.Fail(t, "unexpected path", "path: %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	provider := newTestGitHubProvider(server, "test-token")
	branches, err := provider.ListBranches(context.Background(), "https://github.com/octo-org/octo-repo")
	require.NoError(t, err)
	assert.Equal(t, []Branch{{Name: "main"}, {Name: "develop"}}, branches)
	assert.Equal(t, int32(1), originalRequests.Load())
	assert.Equal(t, int32(2), canonicalRequests.Load())
}

func TestGitHubFollowsRenamedRepositoryRedirectForDefaultBranch(t *testing.T) {
	t.Parallel()

	var canonicalRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/repos/octo-org/octo-repo":
			http.Redirect(w, r, "/repositories/12345", http.StatusMovedPermanently)
		case "/repositories/12345":
			canonicalRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"default_branch":"trunk"}`))
		default:
			assert.Fail(t, "unexpected path", "path: %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	provider := newTestGitHubProvider(server, "test-token")
	branch, err := provider.GetDefaultBranch(context.Background(), "https://github.com/octo-org/octo-repo")
	require.NoError(t, err)
	assert.Equal(t, "trunk", branch)
	assert.Equal(t, int32(1), canonicalRequests.Load())
}

func TestGitHubRedirectPolicyRenamedRepositoryRules(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		original string
		target   string
		allowed  bool
	}{
		{name: "repo to repository id", original: "/repos/o/r", target: "/repositories/42", allowed: true},
		{name: "branches to repository id branches", original: "/repos/o/r/branches", target: "/repositories/42/branches", allowed: true},
		{name: "same path", original: "/repos/o/r/branches", target: "/repos/o/r/branches", allowed: true},
		{name: "repo to repository id branches", original: "/repos/o/r", target: "/repositories/42/branches"},
		{name: "branches to repository id", original: "/repos/o/r/branches", target: "/repositories/42"},
		{name: "branches to other suffix", original: "/repos/o/r/branches", target: "/repositories/42/other"},
		{name: "zero id", original: "/repos/o/r", target: "/repositories/0"},
		{name: "leading zero id", original: "/repos/o/r", target: "/repositories/042"},
		{name: "non numeric id", original: "/repos/o/r", target: "/repositories/abc"},
		{name: "different repo", original: "/repos/o/r/branches", target: "/repos/other/repo/branches"},
		{name: "unrelated original", original: "/user", target: "/repositories/42"},
	}
	policy := githubRedirectPolicy("https://api.github.test", nil)
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			via := []*http.Request{{URL: &url.URL{Scheme: "https", Host: "api.github.test", Path: tt.original}}}
			req := &http.Request{URL: &url.URL{Scheme: "https", Host: "api.github.test", Path: tt.target}}
			err := policy(req, via)
			if tt.allowed {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, errGitHubRedirect)
		})
	}
}

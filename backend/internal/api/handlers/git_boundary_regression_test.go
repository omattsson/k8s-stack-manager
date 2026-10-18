package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"backend/internal/gitprovider"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGitLabMalformedRepositoryURLsReturnSanitizedBadRequest(t *testing.T) {
	t.Parallel()

	var upstreamRequests atomic.Int32
	branches := gitlabBranchesHandler([]map[string]interface{}{
		{"name": "main", "default": true},
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequests.Add(1)
		branches(w, r)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() {
		assert.Zero(t, upstreamRequests.Load(), "rejected URLs must not reach the GitLab server")
	})

	router := setupGitRouter(server.URL)
	// The GitLab provider is configured with the test server as custom base
	// URL, so only URLs on that origin (gitlab.com is no longer routed to a
	// custom GitLab) exercise the GitLab parser. They must be rejected there.
	origin, err := url.Parse(server.URL)
	require.NoError(t, err)
	gitLabHost := origin.Host
	tests := []struct {
		name            string
		repoURL         string
		expectedMessage string
	}{
		{
			name:            "userinfo on GitLab host",
			repoURL:         "http://user:credential-marker@" + gitLabHost + "/group/repo",
			expectedMessage: "Invalid repository URL",
		},
		{
			name:            "unsupported FTP scheme",
			repoURL:         "ftp://" + gitLabHost + "/group/repo",
			expectedMessage: "Invalid repository URL",
		},
		{
			name:            "missing host",
			repoURL:         "https:///gitlab.com/group/repo",
			expectedMessage: "Unsupported Git provider",
		},
		{
			name:            "malformed path escape",
			repoURL:         server.URL + "/group/%zz",
			expectedMessage: "Invalid repository URL",
		},
		{
			name:            "GitLab domain embedded in credentials",
			repoURL:         "https://gitlab.com@evil.example/group/repo",
			expectedMessage: "Unsupported Git provider",
		},
		{
			name:            "GitLab domain embedded in path",
			repoURL:         "https://example.com/gitlab.com/group/repo",
			expectedMessage: "Unsupported Git provider",
		},
		{
			name:            "GitLab domain embedded in query",
			repoURL:         "https://example.com/group/repo?provider=gitlab.com",
			expectedMessage: "Unsupported Git provider",
		},
		{
			name:            "gitlab.com is not routed to the custom GitLab host",
			repoURL:         "https://gitlab.com/group/repo",
			expectedMessage: "Unsupported Git provider",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet,
				"/api/v1/git/branches?repo="+url.QueryEscape(tt.repoURL), nil)
			router.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusBadRequest, recorder.Code)
			var response map[string]string
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			assert.Equal(t, tt.expectedMessage, response["error"])
			assert.NotContains(t, recorder.Body.String(), "credential-marker")
			assert.NotContains(t, recorder.Body.String(), tt.repoURL)
		})
	}
}

func TestCanonicalAzureAndGitLabRepositoryURLsReturnSanitizedBadRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		repoURL func(string) string
	}{
		{
			name: "Azure query",
			repoURL: func(string) string {
				return "https://dev.azure.com/org/project/_git/repo?version=credential-marker"
			},
		},
		{
			name: "Azure fragment",
			repoURL: func(string) string {
				return "https://dev.azure.com/org/project/_git/repo#credential-marker"
			},
		},
		{
			name: "GitLab query",
			repoURL: func(baseURL string) string {
				return baseURL + "/group/repo.git?version=credential-marker"
			},
		},
		{
			name: "GitLab fragment",
			repoURL: func(baseURL string) string {
				return baseURL + "/group/repo.git#credential-marker"
			},
		},
		{
			name: "custom GitLab query",
			repoURL: func(baseURL string) string {
				return baseURL + "/team/repo?version=credential-marker"
			},
		},
		{
			name: "custom GitLab fragment",
			repoURL: func(baseURL string) string {
				return baseURL + "/team/repo#credential-marker"
			},
		},
	}
	endpoints := []struct {
		name string
		path string
	}{
		{name: "list branches", path: "/api/v1/git/branches"},
		{name: "validate branch", path: "/api/v1/git/validate-branch"},
	}

	for _, endpoint := range endpoints {
		endpoint := endpoint
		t.Run(endpoint.name, func(t *testing.T) {
			t.Parallel()
			for _, tt := range tests {
				tt := tt
				t.Run(tt.name, func(t *testing.T) {
					t.Parallel()

					var upstreamRequests atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						upstreamRequests.Add(1)
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`[]`))
					}))
					t.Cleanup(server.Close)

					router := setupGitRouterWithConfig(gitprovider.Config{
						GitLab: gitprovider.GitLabConfig{
							Token:   "gitlab-token-marker",
							BaseURL: server.URL,
						},
					})
					requestURL := endpoint.path + "?repo=" + url.QueryEscape(tt.repoURL(server.URL))
					if endpoint.path == "/api/v1/git/validate-branch" {
						requestURL += "&branch=main"
					}
					response := httptest.NewRecorder()
					router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, requestURL, nil))

					assert.Equal(t, http.StatusBadRequest, response.Code)
					assert.JSONEq(t, `{"error":"Invalid repository URL"}`, response.Body.String())
					assert.NotContains(t, response.Body.String(), "credential-marker")
					assert.Zero(t, upstreamRequests.Load())
				})
			}
		})
	}
}

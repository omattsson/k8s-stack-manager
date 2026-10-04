package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/gitprovider"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupGitRouter creates a test gin engine backed by a real gitprovider.Registry
// that has been configured to proxy GitLab API calls to the provided base URL.
// Pass an empty baseURL to create a registry with no providers configured.
func setupGitRouter(baseURL string) *gin.Engine {
	config := gitprovider.Config{}
	if baseURL != "" {
		config = gitprovider.Config{
			GitLab: gitprovider.GitLabConfig{
				Token:   "test-token",
				BaseURL: baseURL,
			},
		}
	}
	return setupGitRouterWithConfig(config)
}

func setupGitRouterWithConfig(config gitprovider.Config) *gin.Engine {
	gin.SetMode(gin.TestMode)

	registry, err := gitprovider.NewRegistry(config)
	if err != nil {
		panic(err)
	}

	h := NewGitHandler(registry)

	r := gin.New()
	r.Use(middleware.RedactGitRepoQuery())
	git := r.Group("/api/v1/git")
	{
		git.GET("/branches", h.ListBranches)
		git.GET("/validate-branch", h.ValidateBranch)
		git.GET("/providers", h.GetProviders)
	}
	return r
}

func TestGitProviderErrorMappings(t *testing.T) {
	t.Parallel()

	// Provider authentication is a bad gateway response because our request is
	// valid but the configured upstream credentials are rejected. Provider and
	// transport failures are temporary service-unavailable responses.
	tests := []struct {
		name             string
		repoURL          string
		responseBody     string
		expectedMessage  string
		retryAfter       string
		allowedRepos     []string
		upstreamStatus   int
		expectedStatus   int
		expectRetryAfter bool
	}{
		{
			name:            "invalid repository",
			repoURL:         "https://user:credential-marker@github.com/octo-org/octo-repo",
			expectedStatus:  http.StatusBadRequest,
			expectedMessage: "Invalid repository URL",
		},
		{
			name:            "unsupported provider",
			repoURL:         "https://bitbucket.org/org/repo-marker",
			expectedStatus:  http.StatusBadRequest,
			expectedMessage: "Unsupported Git provider",
		},
		{
			name:            "repository not allowed",
			repoURL:         "https://github.com/octo-org/repo-marker",
			allowedRepos:    []string{"octo-org/permitted"},
			expectedStatus:  http.StatusForbidden,
			expectedMessage: "Repository is not permitted",
		},
		{
			name:            "repository not found",
			repoURL:         "https://gitlab.com/org/repo-marker",
			upstreamStatus:  http.StatusNotFound,
			responseBody:    "raw provider not-found credential-marker",
			expectedStatus:  http.StatusNotFound,
			expectedMessage: "Repository not found",
		},
		{
			name:             "rate limited",
			repoURL:          "https://gitlab.com/org/repo-marker",
			upstreamStatus:   http.StatusTooManyRequests,
			responseBody:     "raw provider rate-limit credential-marker",
			retryAfter:       "120",
			expectedStatus:   http.StatusTooManyRequests,
			expectedMessage:  "Git provider rate limit exceeded",
			expectRetryAfter: true,
		},
		{
			name:            "provider authentication failure",
			repoURL:         "https://gitlab.com/org/repo-marker",
			upstreamStatus:  http.StatusUnauthorized,
			responseBody:    "raw provider auth credential-marker",
			expectedStatus:  http.StatusBadGateway,
			expectedMessage: "Git provider authentication or permission denied",
		},
		{
			name:            "provider permission failure",
			repoURL:         "https://gitlab.com/org/repo-marker",
			upstreamStatus:  http.StatusForbidden,
			responseBody:    "raw provider permission credential-marker",
			expectedStatus:  http.StatusBadGateway,
			expectedMessage: "Git provider authentication or permission denied",
		},
		{
			name:            "provider upstream failure",
			repoURL:         "https://gitlab.com/org/repo-marker",
			upstreamStatus:  http.StatusBadGateway,
			responseBody:    "raw provider upstream credential-marker",
			expectedStatus:  http.StatusServiceUnavailable,
			expectedMessage: "Git provider is unavailable",
		},
		{
			name:            "provider unavailable",
			repoURL:         "https://gitlab.com/org/repo-marker",
			upstreamStatus:  http.StatusServiceUnavailable,
			responseBody:    "raw provider unavailable credential-marker",
			expectedStatus:  http.StatusServiceUnavailable,
			expectedMessage: "Git provider is unavailable",
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

					var server *httptest.Server
					config := gitprovider.Config{
						GitHub: gitprovider.GitHubConfig{AllowedRepositories: tt.allowedRepos},
					}
					if tt.upstreamStatus != 0 {
						server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
							w.Header().Set("X-Upstream-Debug", "credential-marker")
							if tt.retryAfter != "" {
								w.Header().Set("Retry-After", tt.retryAfter)
							}
							w.WriteHeader(tt.upstreamStatus)
							_, _ = w.Write([]byte(tt.responseBody))
						}))
						t.Cleanup(server.Close)
						config.GitLab = gitprovider.GitLabConfig{Token: "provider-token-marker", BaseURL: server.URL}
					}

					router := setupGitRouterWithConfig(config)
					requestURL := endpoint.path + "?repo=" + url.QueryEscape(tt.repoURL)
					if strings.Contains(endpoint.path, "validate-branch") {
						requestURL += "&branch=branch-credential-marker"
					}
					request := httptest.NewRequest(http.MethodGet, requestURL, nil)
					request.Header.Set("Authorization", "Bearer request-credential-marker")
					response := httptest.NewRecorder()

					router.ServeHTTP(response, request)

					assert.Equal(t, tt.expectedStatus, response.Code)
					var payload map[string]string
					require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
					assert.Equal(t, tt.expectedMessage, payload["error"])

					responseHeaders := fmt.Sprint(response.Header())
					for _, sensitive := range []string{
						"credential-marker", "repo-marker", "provider-token-marker", "request-credential-marker",
					} {
						assert.NotContains(t, response.Body.String(), sensitive)
						assert.NotContains(t, responseHeaders, sensitive)
					}

					if tt.expectRetryAfter {
						delay, err := strconv.Atoi(response.Header().Get("Retry-After"))
						require.NoError(t, err, "Retry-After must contain integer seconds")
						assert.Positive(t, delay)
						assert.LessOrEqual(t, delay, 120)
					} else {
						assert.Empty(t, response.Header().Get("Retry-After"))
					}
				})
			}
		})
	}
}

func TestGitProviderRateLimitHeaderClampsPastResetToZero(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.GET("/rate-limit", func(c *gin.Context) {
		writeGitProviderError(c, &gitprovider.RateLimitError{
			Provider:   "github",
			Message:    "sanitized rate limit",
			RetryAfter: time.Now().Add(-time.Minute),
		})
	})

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/rate-limit", nil)
	router.ServeHTTP(response, request)

	assert.Equal(t, http.StatusTooManyRequests, response.Code)
	assert.Equal(t, "0", response.Header().Get("Retry-After"))
	assert.JSONEq(t, `{"error":"Git provider rate limit exceeded"}`, response.Body.String())
}

// gitlabBranchesHandler returns a handler that serves a static list of branch names
// from the GitLab branches API path.
func gitlabBranchesHandler(branches []map[string]interface{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(branches)
	}
}

// ---- ListBranches ----

func TestListBranches(t *testing.T) {
	t.Parallel()

	t.Run("returns branch list for valid repo", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(gitlabBranchesHandler([]map[string]interface{}{
			{"name": "main", "default": true},
			{"name": "develop", "default": false},
		}))
		defer server.Close()

		router := setupGitRouter(server.URL)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/git/branches?repo=https://gitlab.com/org/repo", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var branches []gitprovider.Branch
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &branches))
		assert.Len(t, branches, 2)
		assert.Equal(t, "main", branches[0].Name)
		assert.True(t, branches[0].IsDefault)
	})

	t.Run("invalid credential-bearing GitHub repo returns 400 without exposing credentials", func(t *testing.T) {
		t.Parallel()
		router := setupGitRouter("")
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet,
			"/api/v1/git/branches?repo=https%3A%2F%2Fuser%3Asecret%40github.com%2Forg%2Frepo%3Ftoken%3Dnested-secret%23main", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.NotContains(t, w.Body.String(), "user")
		assert.NotContains(t, w.Body.String(), "secret")
		assert.NotContains(t, w.Body.String(), "token")
	})

	t.Run("malformed GitHub URL escape returns sanitized 400", func(t *testing.T) {
		t.Parallel()
		router := setupGitRouter("")
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet,
			"/api/v1/git/branches?repo=https%3A%2F%2Fgithub.com%2Forg%2F%25zz", nil)
		req.Header.Set("Authorization", "Bearer credential-marker")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		var resp map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "Invalid repository URL", resp["error"])
		assert.NotContains(t, w.Body.String(), "github.com")
		assert.NotContains(t, w.Body.String(), "%zz")
		assert.NotContains(t, w.Body.String(), "credential-marker")
	})

	t.Run("schemeless GitHub URL returns sanitized 400", func(t *testing.T) {
		t.Parallel()
		router := setupGitRouter("")
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet,
			"/api/v1/git/branches?repo=github.com%2Forg%2Frepo-marker", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		var resp map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "Invalid repository URL", resp["error"])
		assert.NotContains(t, w.Body.String(), "github.com")
		assert.NotContains(t, w.Body.String(), "repo-marker")
	})

	t.Run("missing repo param returns 400", func(t *testing.T) {
		t.Parallel()
		router := setupGitRouter("")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/git/branches", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		var resp map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Contains(t, resp["error"], "repo")
	})

	t.Run("unsupported provider returns sanitized client error", func(t *testing.T) {
		t.Parallel()
		// Bitbucket is unsupported, so detection fails before any network request.
		router := setupGitRouter("")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/git/branches?repo=https://bitbucket.org/org/repo-marker", nil)
		router.ServeHTTP(w, req)

		assert.GreaterOrEqual(t, w.Code, http.StatusBadRequest)
		assert.Less(t, w.Code, http.StatusInternalServerError)
		assert.NotContains(t, w.Body.String(), "bitbucket.org")
		assert.NotContains(t, w.Body.String(), "repo-marker")
	})

	t.Run("provider API error returns 503", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "internal error", http.StatusInternalServerError)
		}))
		defer server.Close()

		router := setupGitRouter(server.URL)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/git/branches?repo=https://gitlab.com/org/repo", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	})
}

// ---- ValidateBranch ----

func TestValidateBranch(t *testing.T) {
	t.Parallel()

	t.Run("branch exists returns valid true", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(gitlabBranchesHandler([]map[string]interface{}{
			{"name": "main", "default": true},
			{"name": "develop", "default": false},
		}))
		defer server.Close()

		router := setupGitRouter(server.URL)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet,
			"/api/v1/git/validate-branch?repo=https://gitlab.com/org/repo&branch=main", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, true, resp["valid"])
		assert.Equal(t, "main", resp["branch"])
	})

	t.Run("branch does not exist returns valid false", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(gitlabBranchesHandler([]map[string]interface{}{
			{"name": "main", "default": true},
		}))
		defer server.Close()

		router := setupGitRouter(server.URL)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet,
			"/api/v1/git/validate-branch?repo=https://gitlab.com/org/repo&branch=nonexistent", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, false, resp["valid"])
	})

	t.Run("missing repo param returns 400", func(t *testing.T) {
		t.Parallel()
		router := setupGitRouter("")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/git/validate-branch?branch=main", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("missing branch param returns 400", func(t *testing.T) {
		t.Parallel()
		router := setupGitRouter("")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/git/validate-branch?repo=https://gitlab.com/org/repo", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	for _, tt := range []struct {
		name    string
		repoURL string
	}{
		{name: "malformed GitHub URL escape returns sanitized 400", repoURL: "https://github.com/org/%zz"},
		{name: "schemeless GitHub URL returns sanitized 400", repoURL: "github.com/org/repo-marker"},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router := setupGitRouter("")
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet,
				"/api/v1/git/validate-branch?repo="+url.QueryEscape(tt.repoURL)+"&branch=main", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code)
			var resp map[string]string
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, "Invalid repository URL", resp["error"])
			assert.NotContains(t, w.Body.String(), "github.com")
			assert.NotContains(t, w.Body.String(), "repo-marker")
			assert.NotContains(t, w.Body.String(), "%zz")
		})
	}

	t.Run("unsupported provider returns sanitized client error", func(t *testing.T) {
		t.Parallel()
		// Bitbucket is unsupported, so detection fails before any network request.
		router := setupGitRouter("")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet,
			"/api/v1/git/validate-branch?repo=https://bitbucket.org/org/repo-marker&branch=main", nil)
		router.ServeHTTP(w, req)

		assert.GreaterOrEqual(t, w.Code, http.StatusBadRequest)
		assert.Less(t, w.Code, http.StatusInternalServerError)
		assert.NotContains(t, w.Body.String(), "bitbucket.org")
		assert.NotContains(t, w.Body.String(), "repo-marker")
	})
}

// ---- GetProviders ----

func TestGetProviders(t *testing.T) {
	t.Parallel()

	t.Run("returns status list when gitlab configured", func(t *testing.T) {
		t.Parallel()
		router := setupGitRouter("https://gitlab.com")

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/git/providers", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var statuses []gitprovider.ProviderStatus
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &statuses))
		require.Len(t, statuses, 3)

		providerMap := make(map[string]bool)
		for _, s := range statuses {
			providerMap[s.Type] = s.Available
		}
		assert.Equal(t, map[string]bool{
			"azure_devops": false,
			"github":       true,
			"gitlab":       true,
		}, providerMap)
	})

	t.Run("returns public github when no tokens configured", func(t *testing.T) {
		t.Parallel()
		router := setupGitRouter("")

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/git/providers", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var statuses []gitprovider.ProviderStatus
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &statuses))
		require.Len(t, statuses, 3)
		providerMap := make(map[string]bool)
		for _, s := range statuses {
			providerMap[s.Type] = s.Available
		}
		assert.Equal(t, map[string]bool{
			"azure_devops": false,
			"github":       true,
			"gitlab":       false,
		}, providerMap)
	})
}

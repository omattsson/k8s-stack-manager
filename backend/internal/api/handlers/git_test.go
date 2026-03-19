package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/internal/gitprovider"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupGitRouter creates a test gin engine backed by a real gitprovider.Registry
// that has been configured to proxy GitLab API calls to the provided base URL.
// Pass an empty baseURL to create a registry with no providers configured.
func setupGitRouter(baseURL string) *gin.Engine {
	gin.SetMode(gin.TestMode)

	var registry *gitprovider.Registry
	if baseURL != "" {
		registry = gitprovider.NewRegistry(gitprovider.Config{
			GitLab: gitprovider.GitLabConfig{
				Token:   "test-token",
				BaseURL: baseURL,
			},
		})
	} else {
		registry = gitprovider.NewRegistry(gitprovider.Config{})
	}

	h := NewGitHandler(registry)

	r := gin.New()
	git := r.Group("/api/v1/git")
	{
		git.GET("/branches", h.ListBranches)
		git.GET("/validate-branch", h.ValidateBranch)
		git.GET("/providers", h.GetProviders)
	}
	return r
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

	t.Run("unsupported provider returns 500", func(t *testing.T) {
		t.Parallel()
		// Registry has no providers configured; unsupported URL triggers an error.
		router := setupGitRouter("")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/git/branches?repo=https://github.com/org/repo", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})

	t.Run("provider API error returns 500", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "internal error", http.StatusInternalServerError)
		}))
		defer server.Close()

		router := setupGitRouter(server.URL)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/git/branches?repo=https://gitlab.com/org/repo", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
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

	t.Run("provider error returns 500", func(t *testing.T) {
		t.Parallel()
		// No provider configured for this URL pattern.
		router := setupGitRouter("")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet,
			"/api/v1/git/validate-branch?repo=https://github.com/org/repo&branch=main", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
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
		require.Len(t, statuses, 2)

		providerMap := make(map[string]bool)
		for _, s := range statuses {
			providerMap[s.Type] = s.Available
		}
		assert.False(t, providerMap["azure_devops"])
		assert.True(t, providerMap["gitlab"])
	})

	t.Run("returns unavailable when no providers configured", func(t *testing.T) {
		t.Parallel()
		router := setupGitRouter("")

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/git/providers", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var statuses []gitprovider.ProviderStatus
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &statuses))
		require.Len(t, statuses, 2)
		for _, s := range statuses {
			assert.False(t, s.Available)
		}
	})
}

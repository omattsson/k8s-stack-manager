package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

type gitRepoRequestObservation struct {
	url        string
	rawQuery   string
	requestURI string
}

func recordGitRepoRequest(observations map[string]gitRepoRequestObservation, observer string) gin.HandlerFunc {
	return func(c *gin.Context) {
		observations[observer] = gitRepoRequestObservation{
			url:        c.Request.URL.String(),
			rawQuery:   c.Request.URL.RawQuery,
			requestURI: c.Request.RequestURI,
		}
		c.Next()
	}
}

func TestRedactGitRepoQueryRunsBeforeTelemetryAndLoggingBoundaries(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		path       string
		wantRedact bool
	}{
		{name: "branches endpoint", path: "/api/v1/git/branches", wantRedact: true},
		{name: "validate endpoint", path: "/api/v1/git/validate-branch", wantRedact: true},
		{name: "exact Git root", path: "/api/v1/git", wantRedact: true},
		{name: "malformed deep Git subtree", path: "/api/v1/git//providers/%25zz/deep", wantRedact: true},
		{name: "Git prefix without path separator", path: "/api/v1/github", wantRedact: false},
		{name: "Git hyphen lookalike", path: "/api/v1/git-admin/branches", wantRedact: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			observations := make(map[string]gitRepoRequestObservation, 3)
			router := gin.New()
			router.Use(RedactGitRepoQuery())
			// These recorders occupy the same registration positions as
			// HTTPMetrics, otelgin, and Logger in routes.SetupRoutes.
			router.Use(recordGitRepoRequest(observations, "HTTPMetrics"))
			router.Use(recordGitRepoRequest(observations, "otelgin"))
			router.Use(recordGitRepoRequest(observations, "Logger"))
			router.NoRoute(func(c *gin.Context) { c.Status(http.StatusNoContent) })

			const encodedRepo = "https%3A%2F%2Fuser%3Acredential-marker%40github.com%2Forg%2Frepo%3Ftoken%3Dsecret-marker%23main"
			request := httptest.NewRequest(http.MethodGet, tt.path+"?repo="+encodedRepo+"&keep=value", nil)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			assert.Equal(t, http.StatusNoContent, response.Code)
			for _, observer := range []string{"HTTPMetrics", "otelgin", "Logger"} {
				observation := observations[observer]
				combined := observation.url + " " + observation.rawQuery + " " + observation.requestURI
				if tt.wantRedact {
					assert.Equal(t, "keep=value", observation.rawQuery, observer)
					assert.NotContains(t, combined, "repo=", observer)
					assert.NotContains(t, combined, "credential-marker", observer)
					assert.NotContains(t, combined, "secret-marker", observer)
				} else {
					assert.Contains(t, observation.rawQuery, "repo=", observer)
					assert.Contains(t, combined, "credential-marker", observer)
				}
			}
		})
	}
}

func TestRedactGitRepoQuery(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name         string
		target       string
		wantRepo     string
		wantRawQuery string
	}{
		{
			name:         "exact Git group root",
			target:       "/api/v1/git?repo=https%3A%2F%2Fuser%3Apassword%40github.com%2Forg%2Frepo.git&keep=value",
			wantRepo:     "https://user:password@github.com/org/repo.git",
			wantRawQuery: "keep=value",
		},
		{
			name:         "deep Git route descendant",
			target:       "/api/v1/git/providers/github/branches?repo=https%3A%2F%2Fuser%3Apassword%40github.com%2Forg%2Frepo.git&branch=main",
			wantRepo:     "https://user:password@github.com/org/repo.git",
			wantRawQuery: "branch=main",
		},
		{
			name:         "encoded credentials nested query and fragment",
			target:       "/api/v1/git/branches?repo=https%3A%2F%2Fuser%3Apassword%40github.com%2Forg%2Frepo%3Ftoken%3Dsecret%26scope%3Drepo%23main&branch=main&filter=a%2Bb",
			wantRepo:     "https://user:password@github.com/org/repo?token=secret&scope=repo#main",
			wantRawQuery: "branch=main&filter=a%2Bb",
		},
		{
			name:         "HTTPS repository",
			target:       "/api/v1/git/branches?keep=first&repo=https%3A%2F%2Fgithub.com%2Forg%2Frepo.git&keep=second",
			wantRepo:     "https://github.com/org/repo.git",
			wantRawQuery: "keep=first&keep=second",
		},
		{
			name:         "SSH repository",
			target:       "/api/v1/git/validate-branch?repo=ssh%3A%2F%2Fgit%40github.com%2Forg%2Frepo.git&branch=main",
			wantRepo:     "ssh://git@github.com/org/repo.git",
			wantRawQuery: "branch=main",
		},
		{
			name:         "SCP repository and encoded key",
			target:       "/api/v1/git/branches?r%65po=git%40github.com%3Aorg%2Frepo.git&x=%2f%2B",
			wantRepo:     "git@github.com:org/repo.git",
			wantRawQuery: "x=%2f%2B",
		},
		{
			name:         "empty first duplicate remains empty",
			target:       "/api/v1/git/branches?repo=&repo=git%40github.com%3Aorg%2Frepo.git&keep=value",
			wantRepo:     "",
			wantRawQuery: "keep=value",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodGet, tt.target, nil)
			var (
				seenRawQuery   string
				seenRequestURI string
				seenRepo       string
			)
			router := gin.New()
			router.Use(RedactGitRepoQuery())
			router.Use(func(c *gin.Context) {
				seenRawQuery = c.Request.URL.RawQuery
				seenRequestURI = c.Request.RequestURI
				c.Next()
			})
			router.GET(request.URL.Path, func(c *gin.Context) {
				seenRepo = GitRepoFromContext(c)
				c.Status(http.StatusOK)
			})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			assert.Equal(t, http.StatusOK, response.Code)
			assert.Equal(t, tt.wantRepo, seenRepo)
			assert.Equal(t, tt.wantRawQuery, seenRawQuery)
			assert.NotContains(t, seenRequestURI, "repo")
			assert.NotContains(t, seenRequestURI, "secret")
		})
	}
}

func TestRedactGitRepoQueryCoversGitRouteSubtree(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	const target = "/api/v1/git/branch?repo=https%3A%2F%2Fuser%3Apassword%40github.com%2Forg%2Frepo%3Ftoken%3Dsecret%26scope%3Drepo%23main&branch=main&filter=a%2Bb"
	var (
		seenURL        string
		seenRawQuery   string
		seenRequestURI string
		seenRepo       string
	)
	router := gin.New()
	router.Use(RedactGitRepoQuery())
	router.Use(func(c *gin.Context) {
		seenURL = c.Request.URL.String()
		seenRawQuery = c.Request.URL.RawQuery
		seenRequestURI = c.Request.RequestURI
		c.Next()
	})
	router.GET("/api/v1/git/branch", func(c *gin.Context) {
		seenRepo = GitRepoFromContext(c)
		c.Status(http.StatusOK)
	})

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	router.ServeHTTP(response, request)

	assert.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, "https://user:password@github.com/org/repo?token=secret&scope=repo#main", seenRepo)
	assert.Equal(t, "branch=main&filter=a%2Bb", seenRawQuery)
	assert.Equal(t, "/api/v1/git/branch?branch=main&filter=a%2Bb", seenURL)
	assert.Equal(t, seenURL, seenRequestURI)
	assert.NotContains(t, seenURL, "password")
	assert.NotContains(t, seenURL, "token")
	assert.NotContains(t, seenURL, "secret")
}

func TestRedactGitRepoQueryIgnoresOtherPaths(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	const rawQuery = "repo=https%3A%2F%2Fuser%3Asecret%40example.com%2Forg%2Frepo&keep=%2f%2B"
	tests := []struct {
		name string
		path string
	}{
		{name: "unrelated route", path: "/api/v1/stack-definitions"},
		{name: "github lookalike prefix", path: "/api/v1/github"},
		{name: "gitlab lookalike prefix", path: "/api/v1/gitlab"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var seenRawQuery string
			router := gin.New()
			router.Use(RedactGitRepoQuery())
			router.Use(func(c *gin.Context) {
				seenRawQuery = c.Request.URL.RawQuery
				c.Next()
			})
			router.GET(tt.path, func(c *gin.Context) { c.Status(http.StatusOK) })

			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, tt.path+"?"+rawQuery, nil)
			router.ServeHTTP(response, request)

			assert.Equal(t, http.StatusOK, response.Code)
			assert.Equal(t, rawQuery, seenRawQuery)
		})
	}
}

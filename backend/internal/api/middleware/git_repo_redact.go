package middleware

import (
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

const gitRepoContextKey = "git.repo"

const gitRoutePath = "/api/v1/git"

// RedactGitRepoQuery removes repository URLs before telemetry and logging run.
func RedactGitRepoQuery() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		if path != gitRoutePath && !strings.HasPrefix(path, gitRoutePath+"/") {
			c.Next()
			return
		}

		parts := strings.Split(c.Request.URL.RawQuery, "&")
		kept := parts[:0]
		var (
			repoURL      string
			repoValueSet bool
		)
		for _, part := range parts {
			rawKey, rawValue, _ := strings.Cut(part, "=")
			key, err := url.QueryUnescape(rawKey)
			if err != nil || key != "repo" {
				kept = append(kept, part)
				continue
			}

			if !repoValueSet {
				if value, err := url.QueryUnescape(rawValue); err == nil {
					repoURL = value
					repoValueSet = true
				}
			}
		}

		if len(kept) == len(parts) {
			c.Next()
			return
		}
		if repoURL != "" {
			c.Set(gitRepoContextKey, repoURL)
		}
		c.Request.URL.RawQuery = strings.Join(kept, "&")
		if c.Request.RequestURI != "" {
			c.Request.RequestURI = c.Request.URL.RequestURI()
		}
		c.Next()
	}
}

// GitRepoFromContext returns the repository URL extracted by RedactGitRepoQuery.
func GitRepoFromContext(c *gin.Context) string {
	value, _ := c.Get(gitRepoContextKey)
	repoURL, _ := value.(string)
	return repoURL
}

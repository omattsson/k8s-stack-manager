package config_test

import (
	"testing"

	"backend/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfigNormalizesGitLabBaseURL(t *testing.T) {
	// Not parallel: t.Setenv mutates process-wide environment variables.
	tests := []struct {
		name     string
		baseURL  string
		expected string
	}{
		{name: "HTTPS origin", baseURL: "  https://git.example.com/  ", expected: "https://git.example.com"},
		{name: "HTTP origin", baseURL: "  http://git.example.com/  ", expected: "http://git.example.com"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ENV_FILE", t.TempDir()+"/missing.env")
			t.Setenv("GITHUB_TOKEN", "")
			t.Setenv("GITHUB_ALLOWED_REPOSITORIES", "")
			t.Setenv("GITLAB_BASE_URL", tt.baseURL)

			cfg, err := config.LoadConfig()
			require.NoError(t, err)
			require.NotNil(t, cfg)
			assert.Equal(t, tt.expected, cfg.GitProvider.GitLabBaseURL)
		})
	}
}

func TestLoadConfigRejectsInvalidGitLabBaseURL(t *testing.T) {
	// Not parallel: t.Setenv mutates process-wide environment variables.
	tests := []struct {
		name    string
		baseURL string
	}{
		{name: "missing scheme", baseURL: "git.example.com"},
		{name: "non HTTP scheme", baseURL: "ftp://git.example.com"},
		{name: "userinfo", baseURL: "https://user:credential-marker@git.example.com"},
		{name: "missing hostname", baseURL: "https:///git.example.com"},
		{name: "path", baseURL: "https://git.example.com/gitlab"},
		{name: "query", baseURL: "https://git.example.com?credential-marker=value"},
		{name: "fragment", baseURL: "https://git.example.com#credential-marker"},
		{name: "malformed escape", baseURL: "https://git.example.com/%zz"},
		{name: "empty query", baseURL: "https://gitlab.example.com/?"},
		{name: "empty fragment", baseURL: "https://gitlab.example.com/#"},
		{name: "opaque", baseURL: "https:gitlab.example.com"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ENV_FILE", t.TempDir()+"/missing.env")
			t.Setenv("GITHUB_TOKEN", "")
			t.Setenv("GITHUB_ALLOWED_REPOSITORIES", "")
			t.Setenv("GITLAB_BASE_URL", tt.baseURL)

			cfg, err := config.LoadConfig()
			require.Error(t, err)
			assert.Nil(t, cfg)
			assert.Contains(t, err.Error(), "git provider config")
			assert.NotContains(t, err.Error(), "credential-marker")
		})
	}
}

func TestLoadConfigGitLabBaseURLErrorIncludesReason(t *testing.T) {
	// Not parallel: t.Setenv mutates process-wide environment variables.
	t.Setenv("ENV_FILE", t.TempDir()+"/missing.env")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GITHUB_ALLOWED_REPOSITORIES", "")
	t.Setenv("GITLAB_BASE_URL", "https://git.example.com/gitlab")

	cfg, err := config.LoadConfig()
	require.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "GITLAB_BASE_URL is invalid: GitLab base URL must not include a path")
}

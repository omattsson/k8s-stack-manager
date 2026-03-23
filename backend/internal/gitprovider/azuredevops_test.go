package gitprovider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAzureDevOpsListBranches(t *testing.T) {
	t.Parallel()

	refs := azureRefsResponse{
		Value: []azureRef{
			{Name: "refs/heads/main", ObjectID: "abc123"},
			{Name: "refs/heads/develop", ObjectID: "def456"},
			{Name: "refs/heads/feature/new-thing", ObjectID: "ghi789"},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Contains(t, r.Header.Get("Authorization"), "Basic ")
		assert.Contains(t, r.URL.Path, "/refs")
		assert.Contains(t, r.URL.RawQuery, "filter=heads/")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(refs)
	}))
	defer server.Close()

	ctx := context.Background()
	serverProvider := newTestAzureProvider(t, server)

	branches, err := serverProvider.ListBranches(ctx, "https://dev.azure.com/myorg/myproject/_git/myrepo")
	require.NoError(t, err)
	assert.Len(t, branches, 3)
	assert.Equal(t, "main", branches[0].Name)
	assert.False(t, branches[0].IsDefault)
	assert.Equal(t, "develop", branches[1].Name)
	assert.Equal(t, "feature/new-thing", branches[2].Name)
}

func TestAzureDevOpsListBranches_EmptyResult(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(azureRefsResponse{Value: []azureRef{}})
	}))
	defer server.Close()

	provider := newTestAzureProvider(t, server)
	ctx := context.Background()

	branches, err := provider.ListBranches(ctx, "https://dev.azure.com/myorg/myproject/_git/myrepo")
	require.NoError(t, err)
	assert.Empty(t, branches)
}

func TestAzureDevOpsListBranches_Unauthorized(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("Unauthorized"))
	}))
	defer server.Close()

	provider := newTestAzureProvider(t, server)
	ctx := context.Background()

	_, err := provider.ListBranches(ctx, "https://dev.azure.com/myorg/myproject/_git/myrepo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication failed")
}

func TestAzureDevOpsListBranches_NotFound(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("Not Found"))
	}))
	defer server.Close()

	provider := newTestAzureProvider(t, server)
	ctx := context.Background()

	_, err := provider.ListBranches(ctx, "https://dev.azure.com/myorg/myproject/_git/myrepo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repository not found")
}

func TestAzureDevOpsListBranches_ServerError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Internal Server Error"))
	}))
	defer server.Close()

	provider := newTestAzureProvider(t, server)
	ctx := context.Background()

	_, err := provider.ListBranches(ctx, "https://dev.azure.com/myorg/myproject/_git/myrepo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 500")
}

func TestAzureDevOpsListBranches_InvalidJSON(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("not-valid-json"))
	}))
	defer server.Close()

	provider := newTestAzureProvider(t, server)
	ctx := context.Background()

	_, err := provider.ListBranches(ctx, "https://dev.azure.com/myorg/myproject/_git/myrepo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode Azure DevOps response")
}

func TestAzureDevOpsListBranches_InvalidURL(t *testing.T) {
	t.Parallel()

	provider := &azureDevOpsProvider{pat: "test-pat", httpClient: http.DefaultClient}
	ctx := context.Background()

	_, err := provider.ListBranches(ctx, "https://github.com/user/repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not an Azure DevOps URL")
}

func TestAzureDevOpsGetDefaultBranch(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The GetDefaultBranch endpoint hits the repository endpoint, not refs.
		assert.NotContains(t, r.URL.Path, "/refs")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"defaultBranch": "refs/heads/main",
		})
	}))
	defer server.Close()

	provider := newTestAzureProvider(t, server)
	ctx := context.Background()

	branch, err := provider.GetDefaultBranch(ctx, "https://dev.azure.com/myorg/myproject/_git/myrepo")
	require.NoError(t, err)
	assert.Equal(t, "main", branch)
}

func TestAzureDevOpsGetDefaultBranch_APIError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	provider := newTestAzureProvider(t, server)
	ctx := context.Background()

	_, err := provider.GetDefaultBranch(ctx, "https://dev.azure.com/myorg/myproject/_git/myrepo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 403")
}

func TestAzureDevOpsGetDefaultBranch_InvalidURL(t *testing.T) {
	t.Parallel()

	provider := &azureDevOpsProvider{pat: "test-pat", httpClient: http.DefaultClient}
	ctx := context.Background()

	_, err := provider.GetDefaultBranch(ctx, "https://github.com/user/repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not an Azure DevOps URL")
}

func TestAzureDevOpsGetDefaultBranch_InvalidJSON(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{invalid"))
	}))
	defer server.Close()

	provider := newTestAzureProvider(t, server)
	ctx := context.Background()

	_, err := provider.GetDefaultBranch(ctx, "https://dev.azure.com/myorg/myproject/_git/myrepo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode Azure DevOps response")
}

func TestAzureDevOpsValidateBranch(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(azureRefsResponse{
			Value: []azureRef{
				{Name: "refs/heads/main", ObjectID: "abc123"},
				{Name: "refs/heads/develop", ObjectID: "def456"},
			},
		})
	}))
	defer server.Close()

	provider := newTestAzureProvider(t, server)
	ctx := context.Background()

	tests := []struct {
		name   string
		branch string
		want   bool
	}{
		{"existing branch", "main", true},
		{"another existing branch", "develop", true},
		{"nonexistent branch", "feature/does-not-exist", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Not parallel: subtests share the httptest server from parent scope.
			exists, err := provider.ValidateBranch(ctx, "https://dev.azure.com/myorg/myproject/_git/myrepo", tt.branch)
			require.NoError(t, err)
			assert.Equal(t, tt.want, exists)
		})
	}
}

func TestAzureDevOpsValidateBranch_InvalidURL(t *testing.T) {
	t.Parallel()

	provider := &azureDevOpsProvider{pat: "test-pat", httpClient: http.DefaultClient}
	ctx := context.Background()

	_, err := provider.ValidateBranch(ctx, "https://github.com/user/repo", "main")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not an Azure DevOps URL")
}

func TestAzureDevOpsProviderType(t *testing.T) {
	t.Parallel()
	p := newAzureDevOpsProvider(AzureDevOpsConfig{PAT: "test"})
	assert.Equal(t, "azure_devops", p.ProviderType())
}

func TestParseAzureSSH_TooFewParts(t *testing.T) {
	t.Parallel()
	_, err := parseAzureSSH("org@vs-ssh.visualstudio.com:v3/org/proj")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid Azure DevOps SSH URL path")
}

func TestParseAzureVSURL_InvalidHost(t *testing.T) {
	t.Parallel()
	_, err := parseAzureVSURL("https://.visualstudio.com/project/_git/repo")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot extract org from host")
}

func TestParseAzureVSURL_TooFewParts(t *testing.T) {
	t.Parallel()
	_, err := parseAzureVSURL("https://myorg.visualstudio.com/project/repo")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid Azure DevOps visualstudio.com URL format")
}

// newTestAzureProvider creates an azureDevOpsProvider that routes all API calls
// to the given test server by replacing "https://dev.azure.com" in the URL
// construction with the test server's URL. Since the provider builds API URLs
// internally, we use an HTTP transport that intercepts and redirects requests.
func newTestAzureProvider(t *testing.T, server *httptest.Server) *azureDevOpsProvider {
	t.Helper()

	transport := &urlRewritingTransport{
		base:      server.Client().Transport,
		targetURL: server.URL,
	}

	return &azureDevOpsProvider{
		pat:        "test-pat",
		httpClient: &http.Client{Transport: transport},
	}
}

// urlRewritingTransport intercepts HTTP requests and rewrites the host to point
// to a test server while preserving the path and query string.
type urlRewritingTransport struct {
	base      http.RoundTripper
	targetURL string
}

func (t *urlRewritingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	newReq := req.Clone(req.Context())
	fullURL := t.targetURL + req.URL.Path
	if req.URL.RawQuery != "" {
		fullURL += "?" + req.URL.RawQuery
	}
	newURL, err := req.URL.Parse(fullURL)
	if err != nil {
		return nil, err
	}
	newReq.URL = newURL

	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(newReq)
}

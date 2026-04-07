package gitprovider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// redirectTransport redirects all requests to the given test server URL,
// preserving the original path and query string.
type redirectTransport struct {
	targetURL string
	wrapped   http.RoundTripper
}

func (rt *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = "http"
	// Extract host from targetURL (strip scheme).
	host := rt.targetURL[len("http://"):]
	req.URL.Host = host
	return rt.wrapped.RoundTrip(req)
}

func TestRegistryHealthCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setupAzure  func() (*httptest.Server, *azureDevOpsProvider)
		setupGitLab func() (*httptest.Server, *gitlabProvider)
		wantErr     string
	}{
		{
			name:    "no providers configured returns nil",
			wantErr: "",
		},
		{
			name: "azure devops reachable returns nil",
			setupAzure: func() (*httptest.Server, *azureDevOpsProvider) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
				p := &azureDevOpsProvider{
					pat: "test-pat",
					httpClient: &http.Client{
						Transport: &redirectTransport{targetURL: srv.URL, wrapped: http.DefaultTransport},
					},
				}
				return srv, p
			},
		},
		{
			name: "gitlab reachable returns nil",
			setupGitLab: func() (*httptest.Server, *gitlabProvider) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
				p := &gitlabProvider{
					token:      "test-token",
					baseURL:    srv.URL,
					httpClient: srv.Client(),
				}
				return srv, p
			},
		},
		{
			name: "azure fails but gitlab succeeds returns nil",
			setupAzure: func() (*httptest.Server, *azureDevOpsProvider) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
				}))
				p := &azureDevOpsProvider{
					pat: "test-pat",
					httpClient: &http.Client{
						Transport: &redirectTransport{targetURL: srv.URL, wrapped: http.DefaultTransport},
					},
				}
				return srv, p
			},
			setupGitLab: func() (*httptest.Server, *gitlabProvider) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
				p := &gitlabProvider{
					token:      "test-token",
					baseURL:    srv.URL,
					httpClient: srv.Client(),
				}
				return srv, p
			},
		},
		{
			name: "all providers fail returns error",
			setupAzure: func() (*httptest.Server, *azureDevOpsProvider) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusServiceUnavailable)
				}))
				p := &azureDevOpsProvider{
					pat: "test-pat",
					httpClient: &http.Client{
						Transport: &redirectTransport{targetURL: srv.URL, wrapped: http.DefaultTransport},
					},
				}
				return srv, p
			},
			setupGitLab: func() (*httptest.Server, *gitlabProvider) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusServiceUnavailable)
				}))
				p := &gitlabProvider{
					token:      "test-token",
					baseURL:    srv.URL,
					httpClient: srv.Client(),
				}
				return srv, p
			},
			wantErr: "all configured git providers are unreachable",
		},
		{
			name: "gitlab fails but azure succeeds returns nil",
			setupAzure: func() (*httptest.Server, *azureDevOpsProvider) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
				p := &azureDevOpsProvider{
					pat: "test-pat",
					httpClient: &http.Client{
						Transport: &redirectTransport{targetURL: srv.URL, wrapped: http.DefaultTransport},
					},
				}
				return srv, p
			},
			setupGitLab: func() (*httptest.Server, *gitlabProvider) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusServiceUnavailable)
				}))
				p := &gitlabProvider{
					token:      "test-token",
					baseURL:    srv.URL,
					httpClient: srv.Client(),
				}
				return srv, p
			},
		},
		{
			name: "HTTP 401 treated as failure",
			setupAzure: func() (*httptest.Server, *azureDevOpsProvider) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusUnauthorized)
				}))
				p := &azureDevOpsProvider{
					pat: "bad-pat",
					httpClient: &http.Client{
						Transport: &redirectTransport{targetURL: srv.URL, wrapped: http.DefaultTransport},
					},
				}
				return srv, p
			},
			wantErr: "all configured git providers are unreachable",
		},
		{
			name: "only gitlab configured and fails returns error",
			setupGitLab: func() (*httptest.Server, *gitlabProvider) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
				}))
				p := &gitlabProvider{
					token:      "test-token",
					baseURL:    srv.URL,
					httpClient: srv.Client(),
				}
				return srv, p
			},
			wantErr: "all configured git providers are unreachable",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := &Registry{
				cache:   make(map[string]cacheEntry),
				nowFunc: nil,
			}

			if tt.setupAzure != nil {
				srv, p := tt.setupAzure()
				defer srv.Close()
				r.azureDevOps = p
			}
			if tt.setupGitLab != nil {
				srv, p := tt.setupGitLab()
				defer srv.Close()
				r.gitlab = p
			}

			err := r.HealthCheck(context.Background())
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestRegistryHealthCheck_CancelledContext(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	r := &Registry{
		cache: make(map[string]cacheEntry),
		azureDevOps: &azureDevOpsProvider{
			pat: "test-pat",
			httpClient: &http.Client{
				Transport: &redirectTransport{targetURL: srv.URL, wrapped: http.DefaultTransport},
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := r.HealthCheck(ctx)
	require.Error(t, err, "cancelled context should cause health check to fail")
	assert.Contains(t, err.Error(), "unreachable")
}

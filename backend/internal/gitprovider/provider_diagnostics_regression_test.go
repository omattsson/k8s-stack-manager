package gitprovider

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// syncBuffer is a goroutine-safe log sink for tests.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newBufferLogger() (*slog.Logger, *syncBuffer) {
	sink := &syncBuffer{}
	return slog.New(slog.NewTextHandler(sink, nil)), sink
}

const azureTestRepoURL = "https://dev.azure.com/org/project/_git/repo"

func TestRegistryLogsTokenSourceFailureCauseOnceAndKeepsErrorSanitized(t *testing.T) {
	t.Parallel()

	logger, logs := newBufferLogger()
	registry, err := newRegistryWithLogger(Config{AzureDevOps: AzureDevOpsConfig{
		TokenSource: func(context.Context) (string, error) {
			return "", errors.New("AADSTS700016: application not found; correlation-id-marker")
		},
	}}, logger)
	require.NoError(t, err)

	_, err = registry.ListBranches(context.Background(), azureTestRepoURL)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAuthentication)
	// The error returned to handlers carries only the sanitized message.
	assert.Equal(t, "Azure DevOps token could not be obtained", err.Error())
	assert.NotContains(t, err.Error(), "AADSTS")

	output := logs.String()
	assert.Equal(t, 1, strings.Count(output, "git provider operation failed"), output)
	assert.Contains(t, output, "level=WARN")
	assert.Contains(t, output, "provider=azure_devops")
	assert.Contains(t, output, "operation=list_branches")
	assert.Contains(t, output, "kind=\""+ErrAuthentication.Error()+"\"")
	assert.Contains(t, output, "AADSTS700016")
	assert.Contains(t, output, "correlation-id-marker")
}

func TestRegistryLogsSharedFillFailureOnceForConcurrentCallers(t *testing.T) {
	t.Parallel()

	logger, logs := newBufferLogger()
	release := make(chan struct{})
	var tokenCalls atomic.Int32
	registry, err := newRegistryWithLogger(Config{AzureDevOps: AzureDevOpsConfig{
		TokenSource: func(context.Context) (string, error) {
			tokenCalls.Add(1)
			<-release
			return "", errors.New("token-source-cause-marker")
		},
	}}, logger)
	require.NoError(t, err)

	const callers = 5
	var started, done sync.WaitGroup
	started.Add(callers)
	done.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer done.Done()
			started.Done()
			_, callErr := registry.ListBranches(context.Background(), azureTestRepoURL)
			assert.ErrorIs(t, callErr, ErrAuthentication)
		}()
	}
	started.Wait()
	require.Eventually(t, func() bool {
		registry.fillMu.Lock()
		defer registry.fillMu.Unlock()
		fill := registry.inflight[normalizeCacheKey(azureTestRepoURL)]
		return fill != nil && fill.waiters == callers
	}, 2*time.Second, 5*time.Millisecond)
	close(release)
	done.Wait()

	assert.Equal(t, int32(1), tokenCalls.Load())
	assert.Equal(t, 1, strings.Count(logs.String(), "token-source-cause-marker"))
}

func TestRegistryFailureLogNeverContainsPATOrAuthorizationHeader(t *testing.T) {
	t.Parallel()

	const pat = "azure-pat-secret-marker"
	logger, logs := newBufferLogger()
	registry, err := newRegistryWithLogger(Config{AzureDevOps: AzureDevOpsConfig{PAT: pat}}, logger)
	require.NoError(t, err)
	registry.azureDevOps.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("dial failure marker")
	})

	_, err = registry.ListBranches(context.Background(), azureTestRepoURL)
	require.ErrorIs(t, err, ErrUpstreamFailure)

	output := logs.String()
	assert.Contains(t, output, "dial failure marker")
	assert.NotContains(t, output, pat)
	assert.NotContains(t, output, base64.StdEncoding.EncodeToString([]byte(":"+pat)))
	assert.NotContains(t, output, "Authorization")
}

func TestRegistryHealthCheckWarningIncludesTokenSourceCause(t *testing.T) {
	t.Parallel()

	logger, logs := newBufferLogger()
	registry, err := newRegistryWithLogger(Config{AzureDevOps: AzureDevOpsConfig{
		TokenSource: func(context.Context) (string, error) {
			return "", errors.New("AADSTS50013 health-cause-marker")
		},
	}}, logger)
	require.NoError(t, err)

	err = registry.HealthCheck(context.Background())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "health-cause-marker")
	output := logs.String()
	assert.Contains(t, output, "all configured git providers unreachable")
	assert.Contains(t, output, "health-cause-marker")
}

func TestAzureDevOpsTokenSourceIsBoundedByTokenTimeout(t *testing.T) {
	t.Parallel()

	registry, err := newRegistryWithLogger(Config{AzureDevOps: AzureDevOpsConfig{
		TokenSource: func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	}}, slog.New(slog.NewTextHandler(&syncBuffer{}, nil)))
	require.NoError(t, err)
	registry.azureDevOps.tokenTimeout = 50 * time.Millisecond

	start := time.Now()
	// The registry fill context is detached from the caller, so only the
	// token timeout can end this call.
	_, err = registry.ListBranches(context.Background(), azureTestRepoURL)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAuthentication)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 5*time.Second)
}

func stallingTransport() roundTripFunc {
	return func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	}
}

func TestProviderOperationTimeoutEndsStalledUpstream(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		repoURL string
		setup   func(*Registry)
	}{
		{
			name:    "Azure DevOps",
			repoURL: azureTestRepoURL,
			setup: func(r *Registry) {
				r.azureDevOps = newAzureDevOpsProviderWithClient(AzureDevOpsConfig{PAT: "pat"}, &http.Client{Transport: stallingTransport()})
				r.azureDevOps.operationTimeout = 50 * time.Millisecond
			},
		},
		{
			name:    "GitLab",
			repoURL: "https://gitlab.com/group/repo",
			setup: func(r *Registry) {
				r.gitlab = newGitLabProviderWithClient(GitLabConfig{Token: "token"}, &http.Client{Transport: stallingTransport()})
				r.gitlab.operationTimeout = 50 * time.Millisecond
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			registry, err := newRegistryWithLogger(Config{
				AzureDevOps: AzureDevOpsConfig{PAT: "pat"},
				GitLab:      GitLabConfig{Token: "token"},
			}, slog.New(slog.NewTextHandler(&syncBuffer{}, nil)))
			require.NoError(t, err)
			tt.setup(registry)

			start := time.Now()
			_, err = registry.ListBranches(context.Background(), tt.repoURL)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrUpstreamFailure)
			assert.Less(t, time.Since(start), 5*time.Second)
		})
	}
}

func TestAzureDevOpsGetDefaultBranchIsBoundedByOperationTimeout(t *testing.T) {
	t.Parallel()

	provider := newAzureDevOpsProviderWithClient(AzureDevOpsConfig{PAT: "pat"}, &http.Client{Transport: stallingTransport()})
	provider.operationTimeout = 50 * time.Millisecond
	_, err := provider.GetDefaultBranch(context.Background(), azureTestRepoURL)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUpstreamFailure)
}

func TestRegistryHealthCheckAzureDevOpsTokenSource(t *testing.T) {
	t.Parallel()

	tokenSource := func(context.Context) (string, error) { return "bearer-token-marker", nil }

	t.Run("sends bearer token to escaped org path", func(t *testing.T) {
		t.Parallel()

		var authorization, path atomic.Value
		apiServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authorization.Store(r.Header.Get("Authorization"))
			path.Store(r.URL.EscapedPath())
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(apiServer.Close)

		registry := mustNewRegistry(t, Config{AzureDevOps: AzureDevOpsConfig{TokenSource: tokenSource}})
		registry.azureDevOps = newAzureDevOpsProviderWithClient(
			AzureDevOpsConfig{TokenSource: tokenSource, DefaultOrg: "my org"},
			&http.Client{Transport: azureAPIOriginTransport(apiServer.URL)},
		)

		require.NoError(t, registry.HealthCheck(context.Background()))
		assert.Equal(t, "Bearer bearer-token-marker", authorization.Load())
		assert.Equal(t, "/my%20org/_apis/connectionData", path.Load())
	})

	t.Run("does not follow redirects", func(t *testing.T) {
		t.Parallel()

		var targetRequests atomic.Int32
		targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			targetRequests.Add(1)
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(targetServer.Close)

		var sameOriginTargetRequests atomic.Int32
		apiServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/same-origin-target":
				sameOriginTargetRequests.Add(1)
				w.WriteHeader(http.StatusOK)
			case "/org/_apis/connectionData":
				w.Header().Set("Location", targetServer.URL+"/target")
				w.WriteHeader(http.StatusFound)
			default:
				w.Header().Set("Location", "/same-origin-target")
				w.WriteHeader(http.StatusFound)
			}
		}))
		t.Cleanup(apiServer.Close)

		for _, org := range []string{"org", "other"} {
			registry := mustNewRegistry(t, Config{AzureDevOps: AzureDevOpsConfig{TokenSource: tokenSource}})
			registry.azureDevOps = newAzureDevOpsProviderWithClient(
				AzureDevOpsConfig{TokenSource: tokenSource, DefaultOrg: org},
				&http.Client{Transport: azureAPIOriginTransport(apiServer.URL)},
			)
			err := registry.HealthCheck(context.Background())
			require.Error(t, err, org)
			assert.NotContains(t, err.Error(), "bearer-token-marker")
		}
		assert.Zero(t, targetRequests.Load())
		assert.Zero(t, sameOriginTargetRequests.Load())
	})
}

func TestGitLabSCPURLAcceptsAnySSHUsername(t *testing.T) {
	t.Parallel()

	registry := mustNewRegistry(t, Config{GitLab: GitLabConfig{Token: "test-token", BaseURL: "https://git.example.com"}})
	provider, err := registry.detectProvider("gitlab@git.example.com:g/p")
	require.NoError(t, err)
	assert.Equal(t, "gitlab", provider.ProviderType())

	tests := []struct {
		name      string
		repoURL   string
		expected  string
		expectErr bool
	}{
		{name: "custom user", repoURL: "gitlab@git.example.com:g/p", expected: "g/p"},
		{name: "custom user with .git", repoURL: "deploy-user@git.example.com:g/sub/p.git", expected: "g/sub/p"},
		{name: "empty user", repoURL: "@git.example.com:g/p", expectErr: true},
		{name: "user containing colon", repoURL: "us:er@git.example.com:g/p", expectErr: true},
		{name: "double at", repoURL: "a@b@git.example.com:g/p", expectErr: true},
		{name: "empty host", repoURL: "git@:g/p", expectErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			projectPath, err := parseGitLabProjectPath(tt.repoURL)
			if tt.expectErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrInvalidRepositoryURL)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, projectPath)
		})
	}
}

func TestCustomGitLabOriginNormalizesPortsNumerically(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		baseURL   string
		repoURL   string
		supported bool
	}{
		{name: "leading zero repo port", baseURL: "https://git.example.com", repoURL: "https://git.example.com:0443/g/r", supported: true},
		{name: "leading zero base port", baseURL: "https://git.example.com:0443", repoURL: "https://git.example.com/g/r", supported: true},
		{name: "leading zeros both", baseURL: "https://git.example.com:08443", repoURL: "https://git.example.com:008443/g/r", supported: true},
		{name: "out of range port", baseURL: "https://git.example.com", repoURL: "https://git.example.com:65979/g/r"},
		{name: "port zero", baseURL: "https://git.example.com", repoURL: "https://git.example.com:0/g/r"},
		{name: "non-numeric port", baseURL: "https://git.example.com", repoURL: "https://git.example.com:https/g/r"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			registry, err := newRegistryWithLogger(Config{GitLab: GitLabConfig{Token: "test-token", BaseURL: tt.baseURL}}, nil)
			require.NoError(t, err)
			provider, err := registry.detectProvider(tt.repoURL)
			if tt.supported {
				require.NoError(t, err)
				assert.Equal(t, "gitlab", provider.ProviderType())
				return
			}
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrUnsupportedProvider)
		})
	}
}

func TestNormalizeGitLabBaseURLRejectsOutOfRangePorts(t *testing.T) {
	t.Parallel()

	for _, baseURL := range []string{"https://git.example.com:0", "https://git.example.com:65536", "https://git.example.com:99999"} {
		_, err := NormalizeGitLabBaseURL(baseURL)
		assert.Error(t, err, baseURL)
	}
	normalized, err := NormalizeGitLabBaseURL("https://git.example.com:65535")
	require.NoError(t, err)
	assert.Equal(t, "https://git.example.com:65535", normalized)
}

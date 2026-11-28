package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/internal/cluster"
	"backend/internal/database"
	"backend/internal/deployer"
	"backend/internal/helm"
	"backend/internal/imagedigest"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests of #811 R1: a failed imageDigest lookup renders the digest that the
// newest successful deploy of the instance rendered for the same reference,
// so the pod annotation does not change; "" when there is none. Every deploy
// stores the digests it rendered on its deploy log.

const prevDigest = "sha256:abababababababababababababababababababababababababababababababab"

// downDigestRegistry answers /v2/ and 404 for every manifest: each lookup
// fails without a retry.
func downDigestRegistry(t *testing.T) (string, http.RoundTripper) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/manifests/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "https://"), srv.Client().Transport
}

// fallbackFixture seeds inst-1 on test-cluster (registry host clusterHost)
// whose chart app has values, and builds a handler whose deploy manager and
// handler share logs.
func fallbackFixture(t *testing.T, clusterHost, values string, rt http.RoundTripper, logs *MockDeploymentLogRepository) (*InstanceHandler, *recordingHelmExecutor) {
	t.Helper()
	f := newSVFixture(t, "test-cluster")
	require.NoError(t, f.clusterRepo.Create(context.Background(), &models.Cluster{
		ID: "test-cluster", Name: "test-cluster", RegistryURL: "https://" + clusterHost,
	}))
	ch, err := f.ccRepo.FindByID(context.Background(), "c1")
	require.NoError(t, err)
	ch.DefaultValues = values
	require.NoError(t, f.ccRepo.Update(context.Background(), ch))

	rec := newRecordingHelmExecutor()
	mgr := deployer.NewManager(deployer.ManagerConfig{
		Registry:      cluster.NewRegistryForTest("test-cluster", nil, rec),
		InstanceRepo:  f.instRepo,
		DeployLogRepo: logs,
		Hub:           &MockBroadcastSender{},
		MaxConcurrent: 4,
	})
	h, err := NewInstanceHandlerWithDeployer(
		f.instRepo, f.ovRepo, f.boRepo, f.defRepo, f.ccRepo,
		NewMockStackTemplateRepository(), f.tcRepo,
		helm.NewValuesGenerator(), NewMockUserRepository(),
		mgr, nil, nil, logs, f.clusterRepo,
		0,
		&mockHandlerTxRunner{repos: database.TxRepos{StackInstance: f.instRepo, ValueOverride: f.ovRepo, BranchOverride: f.boRepo}},
	)
	require.NoError(t, err)
	return h.WithImageDigests(imagedigest.NewService(nil, imagedigest.WithTransport(rt), imagedigest.WithTimeout(2*time.Second))), rec
}

// seedDeployLog stores a finished deploy log of inst-1 with image digests.
func seedDeployLog(t *testing.T, logs *MockDeploymentLogRepository, id, status string, age time.Duration, digests map[string]string) {
	t.Helper()
	require.NoError(t, logs.Create(context.Background(), &models.DeploymentLog{
		ID: id, StackInstanceID: "inst-1", Action: models.DeployActionDeploy, Status: status,
		StartedAt: time.Now().UTC().Add(-age), ImageDigests: models.EncodeImageDigests(digests),
	}))
}

// newDeployLog returns the deploy log that the deploy under test created.
func newDeployLog(t *testing.T, logs *MockDeploymentLogRepository, logID string) *models.DeploymentLog {
	t.Helper()
	l, err := logs.FindByID(context.Background(), logID)
	require.NoError(t, err)
	return l
}

func storedDigests(t *testing.T, l *models.DeploymentLog) map[string]string {
	t.Helper()
	if l.ImageDigests == "" {
		return nil
	}
	var m map[string]string
	require.NoError(t, json.Unmarshal([]byte(l.ImageDigests), &m))
	return m
}

func TestImageDigest_FallbackToLastDeployedDigest(t *testing.T) {
	t.Parallel()

	tag := helm.SanitizeImageTag("master")
	tests := []struct {
		name string
		// seed seeds the earlier deploy logs; key is the reference key of
		// the chart value.
		seed       func(t *testing.T, logs *MockDeploymentLogRepository, key string)
		otherHost  bool // the value names a host that is not allowed
		registryUp bool
		want       func(reg *digestRegistry) string
		wantReads  int // LatestImageDigests calls
	}{
		{
			name: "registry down: last successful deploy digest",
			seed: func(t *testing.T, logs *MockDeploymentLogRepository, key string) {
				seedDeployLog(t, logs, "old-newest", models.DeployLogSuccess, time.Hour, map[string]string{key: prevDigest})
				seedDeployLog(t, logs, "old-older", models.DeployLogSuccess, 2*time.Hour, map[string]string{key: "sha256:" + strings.Repeat("c", 64)})
			},
			want:      func(*digestRegistry) string { return prevDigest },
			wantReads: 1,
		},
		{
			name:      "registry down, no earlier deploy: empty",
			want:      func(*digestRegistry) string { return "" },
			wantReads: 1,
		},
		{
			name: "registry down, only a failed earlier deploy: empty",
			seed: func(t *testing.T, logs *MockDeploymentLogRepository, key string) {
				seedDeployLog(t, logs, "old-failed", models.DeployLogError, time.Hour, map[string]string{key: prevDigest})
			},
			want:      func(*digestRegistry) string { return "" },
			wantReads: 1,
		},
		{
			name: "registry up: resolved digest, no log read",
			seed: func(t *testing.T, logs *MockDeploymentLogRepository, key string) {
				seedDeployLog(t, logs, "old-newest", models.DeployLogSuccess, time.Hour, map[string]string{key: prevDigest})
			},
			registryUp: true,
			want:       func(reg *digestRegistry) string { return reg.digest },
			wantReads:  0,
		},
		{
			name: "host not allowed: empty, no fallback",
			seed: func(t *testing.T, logs *MockDeploymentLogRepository, key string) {
				seedDeployLog(t, logs, "old-newest", models.DeployLogSuccess, time.Hour, map[string]string{key: prevDigest})
			},
			otherHost: true,
			want:      func(*digestRegistry) string { return "" },
			wantReads: 0,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var (
				host string
				rt   http.RoundTripper
				reg  *digestRegistry
			)
			if tt.registryUp {
				reg = newDigestRegistry(t, tag)
				host, rt = reg.host, reg.transport
			} else {
				host, rt = downDigestRegistry(t)
			}
			valueHost := host
			if tt.otherHost {
				valueHost = "other.example.com"
			}
			key := valueHost + "/dev/app:" + tag
			logs := NewMockDeploymentLogRepository()
			if tt.seed != nil {
				tt.seed(t, logs, key)
			}
			h, rec := fallbackFixture(t, host, digestValues(valueHost), rt, logs)
			if tt.registryUp {
				// The fixture cluster has no credentials; this registry
				// needs them.
				cl, err := h.clusterRepo.FindByID(context.Background(), "test-cluster")
				require.NoError(t, err)
				cl.RegistryUsername, cl.RegistryPassword = digestTestUser, digestTestPass
				require.NoError(t, h.clusterRepo.Update(context.Background(), cl))
			}
			want := tt.want(reg)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/stack-instances/inst-1/deploy", nil)
			svRouter(http.MethodPost, "/api/v1/stack-instances/:id/deploy", h.DeployInstance).ServeHTTP(w, req)
			require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
			var resp struct {
				LogID string `json:"log_id"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			require.NotEmpty(t, resp.LogID)

			require.Eventually(t, func() bool {
				_, ok := rec.get("app")
				return ok
			}, 3*time.Second, 20*time.Millisecond)
			app, _ := rec.get("app")
			assert.Equal(t, want, annotation(t, app), "pod annotation")

			stored := storedDigests(t, newDeployLog(t, logs, resp.LogID))
			if want == "" {
				assert.Empty(t, stored)
			} else {
				assert.Equal(t, map[string]string{key: want}, stored, "the deploy log stores the rendered digest")
			}
			logs.mu.RLock()
			reads := logs.digestCalls
			logs.mu.RUnlock()
			assert.Equal(t, tt.wantReads, reads)
		})
	}
}

// TestImageDigest_FallbackInPreviewAndBulk: deploy preview and bulk deploy
// render the last deployed digest too; the bulk deploy log stores it.
func TestImageDigest_FallbackInPreviewAndBulk(t *testing.T) {
	t.Parallel()

	tag := helm.SanitizeImageTag("master")
	tests := []struct {
		name string
		run  func(t *testing.T, h *InstanceHandler, rec *recordingHelmExecutor, logs *MockDeploymentLogRepository)
	}{
		{name: "deploy preview", run: func(t *testing.T, h *InstanceHandler, _ *recordingHelmExecutor, _ *MockDeploymentLogRepository) {
			code, charts := previewValues(t, h)
			require.Equal(t, http.StatusOK, code)
			ann, ok := charts["app"]["podAnnotations"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, prevDigest, ann["deploy/image-digest"])
		}},
		{name: "bulk deploy", run: func(t *testing.T, h *InstanceHandler, rec *recordingHelmExecutor, logs *MockDeploymentLogRepository) {
			resp := postBulkDeploy(t, h, context.Background(), []string{"inst-1"})
			require.Equal(t, 1, resp.Succeeded, resp.Results)
			require.Eventually(t, func() bool {
				_, ok := rec.get("app")
				return ok
			}, 3*time.Second, 20*time.Millisecond)
			app, _ := rec.get("app")
			assert.Equal(t, prevDigest, annotation(t, app))
			stored := storedDigests(t, newDeployLog(t, logs, resp.Results[0].LogID))
			assert.Len(t, stored, 1)
			for _, v := range stored {
				assert.Equal(t, prevDigest, v)
			}
		}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			host, rt := downDigestRegistry(t)
			logs := NewMockDeploymentLogRepository()
			seedDeployLog(t, logs, "old-newest", models.DeployLogSuccess, time.Hour, map[string]string{host + "/dev/app:" + tag: prevDigest})
			h, rec := fallbackFixture(t, host, digestValues(host), rt, logs)
			tt.run(t, h, rec, logs)
		})
	}
}

// TestImageDigest_FallbackReadErrorRendersEmpty: a failed read of the earlier
// deploy logs renders "" and the deploy goes on.
func TestImageDigest_FallbackReadErrorRendersEmpty(t *testing.T) {
	t.Parallel()

	host, rt := downDigestRegistry(t)
	logs := NewMockDeploymentLogRepository()
	h, _ := fallbackFixture(t, host, digestValues(host), rt, logs)
	logs.digestsErr = errors.New("db down")

	code, charts := previewValues(t, h)
	require.Equal(t, http.StatusOK, code)
	ann, ok := charts["app"]["podAnnotations"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "", ann["deploy/image-digest"])
}

// TestImageDigest_ExportDoesNotReadDeployLogs: export renders "" and reads
// no deploy logs.
func TestImageDigest_ExportDoesNotReadDeployLogs(t *testing.T) {
	t.Parallel()

	host, rt := downDigestRegistry(t)
	logs := NewMockDeploymentLogRepository()
	seedDeployLog(t, logs, "old-newest", models.DeployLogSuccess, time.Hour, map[string]string{host + "/dev/app:" + helm.SanitizeImageTag("master"): prevDigest})
	h, _ := fallbackFixture(t, host, digestValues(host), rt, logs)

	w := httptest.NewRecorder()
	svRouter(http.MethodGet, "/api/v1/stack-instances/:id/values/:chartId", h.ExportChartValues).
		ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/stack-instances/inst-1/values/c1", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "", annotation(t, w.Body.String()))
	assert.Zero(t, logs.digestCalls)
}

package handlers

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/cluster"
	"backend/internal/database"
	"backend/internal/deployer"
	"backend/internal/helm"
	"backend/internal/imagedigest"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for #811: the imageDigest template function resolves in deploy,
// bulk deploy, quick deploy and deploy preview, and renders "" without a
// registry request in export and compare.

const (
	digestTestUser = "puller"
	digestTestPass = "pull-secret"
)

// digestRegistry is a TLS fake registry that requires the cluster
// credentials and counts the manifest requests that carry them.
type digestRegistry struct {
	srv       *httptest.Server
	host      string
	transport http.RoundTripper
	digest    string
	manifests atomic.Int32 // authenticated manifest requests
	requests  atomic.Int32 // all requests after the push
}

// newDigestRegistry starts a registry with image dev/app:<tag>.
func newDigestRegistry(t *testing.T, tag string) *digestRegistry {
	t.Helper()
	d := &digestRegistry{}
	reg := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	d.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.requests.Add(1)
		user, pass, ok := r.BasicAuth()
		if !ok || user != digestTestUser || pass != digestTestPass {
			w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if strings.Contains(r.URL.Path, "/manifests/") {
			d.manifests.Add(1)
		}
		reg.ServeHTTP(w, r)
	}))
	t.Cleanup(d.srv.Close)
	d.host = strings.TrimPrefix(d.srv.URL, "https://")
	pool := x509.NewCertPool()
	pool.AddCert(d.srv.Certificate())
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	d.transport = tr

	img, err := random.Image(64, 1)
	require.NoError(t, err)
	ref, err := name.NewTag(d.host + "/dev/app:" + tag)
	require.NoError(t, err)
	require.NoError(t, remote.Write(ref, img, remote.WithTransport(tr),
		remote.WithAuth(&authn.Basic{Username: digestTestUser, Password: digestTestPass})))
	dg, err := img.Digest()
	require.NoError(t, err)
	d.digest = dg.String()
	d.manifests.Store(0)
	d.requests.Store(0)
	return d
}

func (d *digestRegistry) service() *imagedigest.Service {
	return imagedigest.NewService(nil, imagedigest.WithTransport(d.transport), imagedigest.WithTimeout(5*time.Second))
}

func (d *digestRegistry) cluster(id string) *models.Cluster {
	return &models.Cluster{
		ID: id, Name: id, RegistryURL: "https://" + d.host,
		RegistryUsername: digestTestUser, RegistryPassword: digestTestPass,
	}
}

// digestValues is a chart default that uses imageDigest twice with the same
// reference: the second call comes from the cache.
func digestValues(host string) string {
	return "podAnnotations:\n" +
		"  deploy/image-digest: '{{ imageDigest \"" + host + "/dev/app\" .ImageTag }}'\n" +
		"again: '{{ imageDigest \"" + host + "/dev/app\" .ImageTag }}'\n"
}

func annotation(t *testing.T, values string) any {
	t.Helper()
	ann, ok := parseValues(t, values)["podAnnotations"].(map[string]any)
	require.True(t, ok, values)
	return ann["deploy/image-digest"]
}

// newDigestFixture seeds the shared values fixture with an imageDigest
// default on chart app and the cluster test-cluster with the registry.
func newDigestFixture(t *testing.T, reg *digestRegistry) *svFixture {
	t.Helper()
	f := newSVFixture(t, "test-cluster")
	require.NoError(t, f.clusterRepo.Create(context.Background(), reg.cluster("test-cluster")))
	ch, err := f.ccRepo.FindByID(context.Background(), "c1")
	require.NoError(t, err)
	ch.DefaultValues = digestValues(reg.host)
	require.NoError(t, f.ccRepo.Update(context.Background(), ch))
	return f
}

func TestImageDigest_DeployPreviewResolves(t *testing.T) {
	t.Parallel()

	reg := newDigestRegistry(t, helm.SanitizeImageTag("master"))
	f := newDigestFixture(t, reg)
	h := f.handler(t, f.svRepo, nil, nil).WithImageDigests(reg.service())

	code, charts := previewValues(t, h)
	require.Equal(t, http.StatusOK, code)
	ann, ok := charts["app"]["podAnnotations"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, reg.digest, ann["deploy/image-digest"])
	assert.Equal(t, reg.digest, charts["app"]["again"])
	assert.EqualValues(t, 1, reg.manifests.Load(), "one authenticated lookup per reference per build")
}

func TestImageDigest_DeployPathsResolve(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		url  string
		body any
		call func(h *InstanceHandler) gin.HandlerFunc
		want int
	}{
		{name: "single deploy", path: "/api/v1/stack-instances/:id/deploy", url: "/api/v1/stack-instances/inst-1/deploy", call: func(h *InstanceHandler) gin.HandlerFunc { return h.DeployInstance }, want: http.StatusAccepted},
		{name: "bulk deploy", path: "/api/v1/stack-instances/bulk/deploy", url: "/api/v1/stack-instances/bulk/deploy", body: BulkOperationRequest{InstanceIDs: []string{"inst-1"}}, call: func(h *InstanceHandler) gin.HandlerFunc { return h.BulkDeploy }, want: http.StatusOK},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reg := newDigestRegistry(t, helm.SanitizeImageTag("master"))
			f := newDigestFixture(t, reg)
			rec := newRecordingHelmExecutor()
			mgr := deployer.NewManager(deployer.ManagerConfig{
				Registry:      cluster.NewRegistryForTest("test-cluster", nil, rec),
				InstanceRepo:  f.instRepo,
				DeployLogRepo: NewMockDeploymentLogRepository(),
				Hub:           &MockBroadcastSender{},
				MaxConcurrent: 2,
			})
			h := f.handler(t, f.svRepo, nil, mgr).WithImageDigests(reg.service())

			var body []byte
			if tt.body != nil {
				body, _ = json.Marshal(tt.body)
			}
			req := httptest.NewRequest(http.MethodPost, tt.url, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			svRouter(http.MethodPost, tt.path, tt.call(h)).ServeHTTP(w, req)
			require.Equal(t, tt.want, w.Code, w.Body.String())

			require.Eventually(t, func() bool {
				_, ok := rec.get("app")
				return ok
			}, 3*time.Second, 20*time.Millisecond)
			app, _ := rec.get("app")
			assert.Equal(t, reg.digest, annotation(t, app))
			assert.EqualValues(t, 1, reg.manifests.Load())
		})
	}
}

func TestImageDigest_ExportAndCompareDoNotResolve(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		route string
		url   string
		get   func(h *InstanceHandler) gin.HandlerFunc
		check func(t *testing.T, body []byte)
	}{
		{
			name:  "export chart values",
			route: "/api/v1/stack-instances/:id/values/:chartId",
			url:   "/api/v1/stack-instances/inst-1/values/c1",
			get:   func(h *InstanceHandler) gin.HandlerFunc { return h.ExportChartValues },
			check: func(t *testing.T, body []byte) { assert.Equal(t, "", annotation(t, string(body))) },
		},
		{
			name:  "export all values",
			route: "/api/v1/stack-instances/:id/values",
			url:   "/api/v1/stack-instances/inst-1/values",
			get:   func(h *InstanceHandler) gin.HandlerFunc { return h.ExportAllValues },
			check: func(t *testing.T, body []byte) {
				zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
				require.NoError(t, err)
				for _, zf := range zr.File {
					if zf.Name != "app/values.yaml" {
						continue
					}
					rc, err := zf.Open()
					require.NoError(t, err)
					data, err := io.ReadAll(rc)
					require.NoError(t, err)
					require.NoError(t, rc.Close())
					assert.Equal(t, "", annotation(t, string(data)))
					return
				}
				t.Fatal("app/values.yaml missing")
			},
		},
		{
			name:  "compare",
			route: "/api/v1/stack-instances/compare",
			url:   "/api/v1/stack-instances/compare?left=inst-1&right=inst-1",
			get:   func(h *InstanceHandler) gin.HandlerFunc { return h.CompareInstances },
			check: func(t *testing.T, body []byte) {
				var resp CompareInstancesResponse
				require.NoError(t, json.Unmarshal(body, &resp))
				for _, ch := range resp.Charts {
					if ch.ChartName == "app" {
						require.NotNil(t, ch.LeftValues)
						assert.Equal(t, "", annotation(t, *ch.LeftValues))
						return
					}
				}
				t.Fatal("chart app missing")
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reg := newDigestRegistry(t, helm.SanitizeImageTag("master"))
			f := newDigestFixture(t, reg)
			h := f.handler(t, f.svRepo, nil, nil).WithImageDigests(reg.service())

			w := httptest.NewRecorder()
			svRouter(http.MethodGet, tt.route, tt.get(h)).ServeHTTP(w, httptest.NewRequest(http.MethodGet, tt.url, nil))
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			tt.check(t, w.Body.Bytes())
			assert.Zero(t, reg.requests.Load(), "export and compare make no registry request")
		})
	}
}

func TestImageDigest_WithoutServiceRendersEmpty(t *testing.T) {
	t.Parallel()

	reg := newDigestRegistry(t, helm.SanitizeImageTag("master"))
	f := newDigestFixture(t, reg)
	h := f.handler(t, f.svRepo, nil, nil) // no WithImageDigests

	code, charts := previewValues(t, h)
	require.Equal(t, http.StatusOK, code)
	ann, ok := charts["app"]["podAnnotations"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "", ann["deploy/image-digest"])
	assert.Zero(t, reg.requests.Load())
}

func TestImageDigest_QuickDeployResolves(t *testing.T) {
	t.Parallel()

	reg := newDigestRegistry(t, "main")
	tmplRepo := NewMockStackTemplateRepository()
	tmplChartRepo := NewMockTemplateChartConfigRepository()
	defRepo := NewMockStackDefinitionRepository()
	ccRepo := NewMockChartConfigRepository()
	instRepo := NewMockStackInstanceRepository()
	boRepo := NewMockChartBranchOverrideRepository()
	ovRepo := NewMockValueOverrideRepository()
	clusterRepo := NewMockClusterRepository()
	require.NoError(t, clusterRepo.Create(context.Background(), reg.cluster("test-cluster")))

	require.NoError(t, tmplRepo.Create(context.Background(), &models.StackTemplate{
		ID: "t1", Name: "Template", IsPublished: true, DefaultBranch: "main", OwnerID: "owner-1",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}))
	require.NoError(t, tmplChartRepo.Create(context.Background(), &models.TemplateChartConfig{
		ID: "tc1", StackTemplateID: "t1", ChartName: "svc",
		RepositoryURL: "oci://example.com/charts/svc", DeployOrder: 1,
		DefaultValues: digestValues(reg.host),
	}))

	rec := newRecordingHelmExecutor()
	clReg := cluster.NewRegistryForTest("test-cluster", nil, rec)
	mgr := deployer.NewManager(deployer.ManagerConfig{
		Registry:      clReg,
		InstanceRepo:  instRepo,
		DeployLogRepo: NewMockDeploymentLogRepository(),
		Hub:           &MockBroadcastSender{},
		MaxConcurrent: 2,
	})
	h, err := NewQuickDeployHandler(
		tmplRepo, tmplChartRepo, defRepo, ccRepo,
		instRepo, boRepo, ovRepo, helm.NewValuesGenerator(),
		mgr, NewMockUserRepository(), nil, NewMockAuditLogRepository(),
		&MockBroadcastSender{}, clReg, nil,
		0,
		&mockHandlerTxRunner{repos: database.TxRepos{
			StackDefinition: defRepo, ChartConfig: ccRepo, StackInstance: instRepo,
			ValueOverride: ovRepo, BranchOverride: boRepo,
		}},
	)
	require.NoError(t, err)
	h.WithTemplateVersions(newWorkingCopyReleaseRepo(tmplRepo, tmplChartRepo)).
		WithClusterRepo(clusterRepo).WithImageDigests(reg.service())

	body, _ := json.Marshal(quickDeployRequest{InstanceName: "digest-test"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/templates/t1/quick-deploy", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	svRouter(http.MethodPost, "/api/v1/templates/:id/quick-deploy", h.QuickDeploy).ServeHTTP(w, req)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())

	require.Eventually(t, func() bool {
		_, ok := rec.get("svc")
		return ok
	}, 3*time.Second, 20*time.Millisecond)
	svc, _ := rec.get("svc")
	assert.Equal(t, reg.digest, annotation(t, svc))
	assert.EqualValues(t, 1, reg.manifests.Load())
}

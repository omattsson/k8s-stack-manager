package handlers

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/cluster"
	"backend/internal/deployer"
	"backend/internal/helm"
	"backend/internal/imagedigest"
	"backend/internal/models"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests of review round 4 of #811 in the handlers: bulk deploy shares the
// digest lookups of the request, bulk deploy fails closed on a cancel, and
// the cluster registry fallbacks of instanceRegistry.

// stubClusterResolver is a clusterIDResolver with a fixed answer.
type stubClusterResolver struct {
	err error
	id  string
}

func (s stubClusterResolver) ResolveClusterID(string) (string, error) { return s.id, s.err }

func TestInstanceRegistry_Fallbacks(t *testing.T) {
	t.Parallel()

	withCreds := func(id string) *models.Cluster {
		return &models.Cluster{ID: id, Name: id, RegistryURL: "https://" + id + ".example.com", RegistryUsername: "u-" + id, RegistryPassword: "p-" + id}
	}
	regOf := func(id string) imagedigest.Registry {
		return imagedigest.Registry{URL: "https://" + id + ".example.com", Username: "u-" + id, Password: "p-" + id}
	}
	tests := []struct {
		name      string
		clusterID string
		resolver  clusterIDResolver
		setup     func(m *MockClusterRepository)
		nilRepo   bool
		want      imagedigest.Registry
	}{
		{name: "instance cluster", clusterID: "c1", want: regOf("c1")},
		{name: "no cluster: resolver default", resolver: stubClusterResolver{id: "c2"}, want: regOf("c2")},
		{name: "no cluster: no default cluster in resolver", resolver: stubClusterResolver{err: cluster.ErrNoDefaultCluster}, want: imagedigest.Registry{}},
		{name: "no cluster: resolver error", resolver: stubClusterResolver{err: errors.New("boom")}, want: imagedigest.Registry{}},
		{name: "no cluster, no resolver: FindDefault", setup: func(m *MockClusterRepository) { m.defaultID = "c3" }, want: regOf("c3")},
		{name: "no cluster, no resolver, no default", want: imagedigest.Registry{}},
		{name: "FindDefault error", setup: func(m *MockClusterRepository) { m.defaultErr = errors.New("db down") }, want: imagedigest.Registry{}},
		{name: "FindByID error: no cluster registry", clusterID: "c1", setup: func(m *MockClusterRepository) { m.fetchErr = errors.New("db down") }, want: imagedigest.Registry{}},
		{name: "unknown cluster", clusterID: "missing", want: imagedigest.Registry{}},
		{name: "no cluster repository", clusterID: "c1", nilRepo: true, want: imagedigest.Registry{}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := NewMockClusterRepository()
			for _, id := range []string{"c1", "c2", "c3"} {
				require.NoError(t, repo.Create(context.Background(), withCreds(id)))
			}
			if tt.setup != nil {
				tt.setup(repo)
			}
			var cr models.ClusterRepository = repo
			if tt.nilRepo {
				cr = nil
			}
			inst := &models.StackInstance{ID: "inst-1", ClusterID: tt.clusterID}
			assert.Equal(t, tt.want, instanceRegistry(context.Background(), tt.resolver, cr, inst))
		})
	}
}

// anonRegistry is a TLS fake registry without auth with image app:<tag>. It
// counts the manifest requests that carry an Authorization header.
type anonRegistry struct {
	transport http.RoundTripper
	host      string
	digest    string
	manifests atomic.Int32
	withAuth  atomic.Int32
}

func newAnonRegistry(t *testing.T, tag string) *anonRegistry {
	t.Helper()
	a := &anonRegistry{}
	reg := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/manifests/") {
			a.manifests.Add(1)
			if r.Header.Get("Authorization") != "" {
				a.withAuth.Add(1)
			}
		}
		reg.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	a.host = strings.TrimPrefix(srv.URL, "https://")
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	a.transport = tr

	img, err := random.Image(64, 1)
	require.NoError(t, err)
	ref, err := name.NewTag(a.host + "/app:" + tag)
	require.NoError(t, err)
	require.NoError(t, remote.Write(ref, img, remote.WithTransport(tr)))
	dg, err := img.Digest()
	require.NoError(t, err)
	a.digest = dg.String()
	a.manifests.Store(0)
	a.withAuth.Store(0)
	return a
}

// TestValuesBuilder_ClusterLookupErrorUsesAllowlistOnly (S8): when the
// cluster of the instance cannot be read, only IMAGE_DIGEST_REGISTRIES hosts
// resolve, anonymously; the cluster registry is not used.
func TestValuesBuilder_ClusterLookupErrorUsesAllowlistOnly(t *testing.T) {
	t.Parallel()

	allowed := newAnonRegistry(t, "v1")
	clusterReg := newDigestRegistry(t, "v1")
	repo := NewMockClusterRepository()
	require.NoError(t, repo.Create(context.Background(), clusterReg.cluster("c1")))
	repo.fetchErr = errors.New("db down")

	svc := imagedigest.NewService([]string{allowed.host}, imagedigest.WithTransport(allowed.transport))
	inst := &models.StackInstance{ID: "inst-1", ClusterID: "c1", Name: "a", Branch: "main", OwnerID: "uid-1"}
	def := &models.StackDefinition{ID: "d1", Name: "def"}
	charts := []models.ChartConfig{{
		ID: "c1", ChartName: "app",
		DefaultValues: "allowed: '{{ imageDigest \"" + allowed.host + "/app\" \"v1\" }}'\n" +
			"cluster: '{{ imageDigest \"" + clusterReg.host + "/dev/app\" \"v1\" }}'\n",
	}}
	b := &valuesBuilder{valuesGen: helm.NewValuesGenerator(), clusterRepo: repo}
	values, _, err := b.withDigests(svc, repo, nil, inst).build(context.Background(), inst, def, charts, "alice")
	require.NoError(t, err)

	got := parseValues(t, values["app"])
	assert.Equal(t, allowed.digest, got["allowed"])
	assert.Equal(t, "", got["cluster"], "the cluster registry is not allowed without the cluster row")
	assert.Positive(t, allowed.manifests.Load())
	assert.Zero(t, allowed.withAuth.Load(), "anonymous")
	assert.Zero(t, clusterReg.requests.Load())
}

// bulkDeployFixture seeds instances inst-1..inst-n on test-cluster with the
// imageDigest chart default and returns the handler and the Helm recorder.
func bulkDeployFixture(t *testing.T, n int, registryURL string, svc *imagedigest.Service) (*InstanceHandler, *recordingHelmExecutor, *svFixture, []string) {
	t.Helper()
	f := newSVFixture(t, "test-cluster")
	require.NoError(t, f.clusterRepo.Create(context.Background(), &models.Cluster{
		ID: "test-cluster", Name: "test-cluster", RegistryURL: "https://" + registryURL,
		RegistryUsername: digestTestUser, RegistryPassword: digestTestPass,
	}))
	ch, err := f.ccRepo.FindByID(context.Background(), "c1")
	require.NoError(t, err)
	ch.DefaultValues = digestValues(registryURL)
	require.NoError(t, f.ccRepo.Update(context.Background(), ch))
	ids := []string{"inst-1"}
	for i := 2; i <= n; i++ {
		id := "inst-" + string(rune('0'+i))
		inst := seedInstance(t, f.instRepo, id, "stack-"+id, "d1", "uid-1", models.StackStatusDraft)
		inst.ClusterID = "test-cluster"
		require.NoError(t, f.instRepo.Update(context.Background(), inst))
		ids = append(ids, id)
	}
	rec := newRecordingHelmExecutor()
	mgr := deployer.NewManager(deployer.ManagerConfig{
		Registry:      cluster.NewRegistryForTest("test-cluster", nil, rec),
		InstanceRepo:  f.instRepo,
		DeployLogRepo: NewMockDeploymentLogRepository(),
		Hub:           &MockBroadcastSender{},
		MaxConcurrent: 10,
	})
	return f.handler(t, f.svRepo, nil, mgr).WithImageDigests(svc), rec, f, ids
}

func postBulkDeploy(t *testing.T, h *InstanceHandler, ctx context.Context, ids []string) BulkOperationResponse {
	t.Helper()
	body, _ := json.Marshal(BulkOperationRequest{InstanceIDs: ids})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/stack-instances/bulk/deploy", bytes.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	svRouter(http.MethodPost, "/api/v1/stack-instances/bulk/deploy", h.BulkDeploy).ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp BulkOperationResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	return resp
}

// TestBulkDeploy_SharesDigestLookups (S7): N instances on one cluster with
// the same image reference make one registry request.
func TestBulkDeploy_SharesDigestLookups(t *testing.T) {
	t.Parallel()

	reg := newDigestRegistry(t, helm.SanitizeImageTag("master"))
	h, rec, _, ids := bulkDeployFixture(t, 4, reg.host, reg.service())

	resp := postBulkDeploy(t, h, context.Background(), ids)
	assert.Equal(t, 4, resp.Succeeded, resp.Results)
	assert.EqualValues(t, 1, reg.manifests.Load(), "one lookup for the whole bulk request")
	require.Eventually(t, func() bool {
		_, ok := rec.get("app")
		return ok
	}, 3*time.Second, 20*time.Millisecond)
	app, _ := rec.get("app")
	assert.Equal(t, reg.digest, annotation(t, app))
}

// TestBulkDeploy_HangingRegistryCostsOneTimeout (S7): with a registry that
// never answers, the bulk request waits about one lookup timeout, not one per
// instance (the host is skipped after its timeout for the whole request).
func TestBulkDeploy_HangingRegistryCostsOneTimeout(t *testing.T) {
	t.Parallel()

	host, arrived, rt := hangingDigestRegistry(t)
	const timeout = 500 * time.Millisecond
	svc := imagedigest.NewService(nil, imagedigest.WithTransport(rt), imagedigest.WithTimeout(timeout))
	h, _, _, ids := bulkDeployFixture(t, 4, host, svc)

	begin := time.Now()
	resp := postBulkDeploy(t, h, context.Background(), ids)
	elapsed := time.Since(begin)
	assert.Equal(t, 4, resp.Succeeded, resp.Results)
	assert.Less(t, elapsed, 3*timeout, "about one timeout for the request, not one per instance")
	assert.Len(t, arrived, 1, "one registry request for the request")
}

// TestBulkDeploy_CancelDuringLookupFailsClosed (T3): a client that leaves
// during a digest lookup of a bulk deploy gets "request cancelled" for the
// instance, and nothing is deployed.
func TestBulkDeploy_CancelDuringLookupFailsClosed(t *testing.T) {
	t.Parallel()

	host, arrived, rt := hangingDigestRegistry(t)
	h, rec, f, ids := bulkDeployFixture(t, 1, host, imagedigest.NewService(nil, imagedigest.WithTransport(rt)))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-arrived
		cancel()
	}()
	resp := postBulkDeploy(t, h, ctx, ids)
	require.Len(t, resp.Results, 1)
	assert.Equal(t, "error", resp.Results[0].Status)
	assert.Equal(t, "request cancelled", resp.Results[0].Error)
	assert.Equal(t, 1, resp.Failed)

	// Deploy sets the instance to deploying before it returns, so a Draft
	// instance shows that no deploy started; no wait is needed.
	_, installed := rec.get("app")
	assert.False(t, installed, "nothing may be deployed")
	inst, err := f.instRepo.FindByID(context.Background(), "inst-1")
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusDraft, inst.Status)
}

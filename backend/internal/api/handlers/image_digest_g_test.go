package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests of review round 4 findings G1 (bulk deploy limits per instance) and
// G2 (rollback logs carry the image digests) of #811.

// tagRegistry answers a manifest HEAD of any repository and tag with the
// digest that digestFor returns; "" answers 404. It counts the manifest
// requests.
type tagRegistry struct {
	transport http.RoundTripper
	digestFor atomic.Pointer[func(tag string) string]
	host      string
	requests  atomic.Int32
}

func newTagRegistry(t *testing.T, digestFor func(tag string) string) *tagRegistry {
	t.Helper()
	tr := &tagRegistry{}
	tr.set(digestFor)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := strings.Index(r.URL.Path, "/manifests/")
		if i < 0 {
			w.WriteHeader(http.StatusOK)
			return
		}
		tr.requests.Add(1)
		d := (*tr.digestFor.Load())(r.URL.Path[i+len("/manifests/"):])
		if d == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		w.Header().Set("Content-Length", "2")
		w.Header().Set("Docker-Content-Digest", d)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	tr.host = strings.TrimPrefix(srv.URL, "https://")
	tr.transport = srv.Client().Transport
	return tr
}

func (tr *tagRegistry) set(f func(tag string) string) { tr.digestFor.Store(&f) }

// digestOfTag is a fixed digest per tag.
func digestOfTag(tag string) string {
	sum := sha256.Sum256([]byte(tag))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// multiFixture holds n instances inst-00..inst-(n-1) of definition d1 on
// test-cluster with branch b<i>, a handler and a deploy manager that share
// logs.
type multiFixture struct {
	f    *svFixture
	h    *InstanceHandler
	logs *MockDeploymentLogRepository
	ids  []string
}

func newMultiFixture(t *testing.T, n int, reg *tagRegistry, svc *imagedigest.Service) *multiFixture {
	t.Helper()
	f := newSVFixture(t, "test-cluster")
	require.NoError(t, f.instRepo.Delete(context.Background(), "inst-1"))
	require.NoError(t, f.clusterRepo.Create(context.Background(), &models.Cluster{
		ID: "test-cluster", Name: "test-cluster", RegistryURL: "https://" + reg.host,
	}))
	ch, err := f.ccRepo.FindByID(context.Background(), "c1")
	require.NoError(t, err)
	ch.DefaultValues = digestValues(reg.host)
	require.NoError(t, f.ccRepo.Update(context.Background(), ch))
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("inst-%02d", i)
		inst := seedInstance(t, f.instRepo, id, "stack-"+id, "d1", "uid-1", models.StackStatusDraft)
		inst.ClusterID = "test-cluster"
		inst.Branch = fmt.Sprintf("b%d", i)
		require.NoError(t, f.instRepo.Update(context.Background(), inst))
		ids = append(ids, id)
	}
	logs := NewMockDeploymentLogRepository()
	mgr := deployer.NewManager(deployer.ManagerConfig{
		Registry:      cluster.NewRegistryForTest("test-cluster", nil, newRecordingHelmExecutor()),
		InstanceRepo:  f.instRepo,
		DeployLogRepo: logs,
		Hub:           &MockBroadcastSender{},
		MaxConcurrent: 10,
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
	return &multiFixture{f: f, h: h.WithImageDigests(svc), logs: logs, ids: ids}
}

// TestBulkDeploy_LimitsPerInstance (G1): a bulk deploy of 40 instances with
// distinct tags on a healthy registry renders the real digest of every
// instance (the lookup cap of 32 is per instance), also when one instance's
// override loops over many references.
func TestBulkDeploy_LimitsPerInstance(t *testing.T) {
	t.Parallel()

	reg := newTagRegistry(t, digestOfTag)
	svc := imagedigest.NewService(nil, imagedigest.WithTransport(reg.transport), imagedigest.WithTimeout(5*time.Second))
	mf := newMultiFixture(t, 40, reg, svc)
	// inst-00 loops over 200 references: it uses up its own cap only.
	require.NoError(t, mf.f.ovRepo.Create(context.Background(), &models.ValueOverride{
		ID: "ov-loop", StackInstanceID: "inst-00", ChartConfigID: "c1",
		Values: `loop: '{{ range $i := 200 }}{{ imageDigest "` + reg.host + `/dev/loop" (printf "t%d" $i) }}{{ end }}'`,
	}))

	resp := postBulkDeploy(t, mf.h, context.Background(), mf.ids)
	require.Equal(t, 40, resp.Succeeded, resp.Results)
	for i, r := range resp.Results {
		if r.InstanceID == "inst-00" {
			// Values render in map order, so the loop may use up
			// inst-00's own cap before its app reference: not checked.
			continue
		}
		tag := helm.SanitizeImageTag(fmt.Sprintf("b%d", i))
		l := newDeployLog(t, mf.logs, r.LogID)
		stored := storedDigests(t, l)
		assert.Equal(t, digestOfTag(tag), stored[reg.host+"/dev/app:"+tag], "instance %s renders its real digest", r.InstanceID)
	}
	// inst-00: 32 requests (its own cap, whichever of its references
	// renders first), every other instance one.
	assert.EqualValues(t, imagedigest.MaxLookups+39, reg.requests.Load())
}

// TestBulkDeploy_LimitStopIsNotShared (#811 N1): instance 1 uses up its
// lookup cap before a base image reference that instance 2 also uses; the
// stop is not cached for the request, so instance 2 renders the real base
// digest.
func TestBulkDeploy_LimitStopIsNotShared(t *testing.T) {
	t.Parallel()

	reg := newTagRegistry(t, digestOfTag)
	svc := imagedigest.NewService(nil, imagedigest.WithTransport(reg.transport), imagedigest.WithTimeout(5*time.Second))
	mf := newMultiFixture(t, 2, reg, svc)
	base := `{{ imageDigest "` + reg.host + `/dev/base" "shared" }}`
	require.NoError(t, mf.f.ovRepo.Create(context.Background(), &models.ValueOverride{
		ID: "ov-0", StackInstanceID: "inst-00", ChartConfigID: "c1",
		// One string: the template runs in order (the keys of a values
		// map render in random order).
		Values: `base: '{{ range $i := 40 }}{{ imageDigest "` + reg.host + `/dev/loop" (printf "t%d" $i) }}{{ end }}` + base + "'\n",
	}))
	require.NoError(t, mf.f.ovRepo.Create(context.Background(), &models.ValueOverride{
		ID: "ov-1", StackInstanceID: "inst-01", ChartConfigID: "c1",
		Values: "base: '" + base + "'\n",
	}))

	resp := postBulkDeploy(t, mf.h, context.Background(), mf.ids)
	require.Equal(t, 2, resp.Succeeded, resp.Results)
	baseKey := reg.host + "/dev/base:shared"
	first := storedDigests(t, newDeployLog(t, mf.logs, resp.Results[0].LogID))
	_, ok := first[baseKey]
	assert.False(t, ok, "instance 1 hit its cap before the base reference")
	second := storedDigests(t, newDeployLog(t, mf.logs, resp.Results[1].LogID))
	assert.Equal(t, digestOfTag("shared"), second[baseKey], "instance 2 looks the base reference up itself")
}

// TestRollback_ImageDigestsFollowTheRollback (G2): deploy A (digest dA),
// deploy B (dB), roll back (to A, or one revision), then a deploy while the
// registry is down renders dA.
func TestRollback_ImageDigestsFollowTheRollback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		toTarget bool
	}{
		{name: "rollback to the target deploy", toTarget: true},
		{name: "rollback by one revision", toTarget: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dA, dB := digestOfTag("A"), digestOfTag("B")
			reg := newTagRegistry(t, func(string) string { return dA })
			svc := imagedigest.NewService(nil, imagedigest.WithTransport(reg.transport), imagedigest.WithTimeout(5*time.Second))
			mf := newMultiFixture(t, 1, reg, svc)
			id := mf.ids[0]
			tag := helm.SanitizeImageTag("b0")
			key := reg.host + "/dev/app:" + tag

			deploy := func() string {
				w := httptest.NewRecorder()
				svRouter(http.MethodPost, "/api/v1/stack-instances/:id/deploy", mf.h.DeployInstance).
					ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/stack-instances/"+id+"/deploy", nil))
				require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
				var resp struct {
					LogID string `json:"log_id"`
				}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				waitLogSuccess(t, mf.logs, resp.LogID)
				return resp.LogID
			}
			logA := deploy()
			reg.set(func(string) string { return dB })
			logB := deploy()
			assert.Equal(t, map[string]string{key: dB}, storedDigests(t, newDeployLog(t, mf.logs, logB)))

			body := []byte(`{}`)
			if tt.toTarget {
				body = []byte(`{"target_log_id":"` + logA + `"}`)
			}
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/stack-instances/"+id+"/rollback", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			svRouter(http.MethodPost, "/api/v1/stack-instances/:id/rollback", mf.h.RollbackInstance).ServeHTTP(w, req)
			require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
			var rb RollbackResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &rb))
			waitLogSuccess(t, mf.logs, rb.LogID)
			assert.Equal(t, map[string]string{key: dA}, storedDigests(t, newDeployLog(t, mf.logs, rb.LogID)),
				"the rollback log holds the digests of the deploy it returns to")

			reg.set(func(string) string { return "" }) // registry down
			logC := deploy()
			assert.Equal(t, map[string]string{key: dA}, storedDigests(t, newDeployLog(t, mf.logs, logC)),
				"the deploy after the rollback renders the digest that runs")
		})
	}
}

// waitLogSuccess waits until the deploy log logID succeeded.
func waitLogSuccess(t *testing.T, logs *MockDeploymentLogRepository, logID string) {
	t.Helper()
	require.Eventually(t, func() bool {
		l, err := logs.FindByID(context.Background(), logID)
		return err == nil && l.Status == models.DeployLogSuccess
	}, 5*time.Second, 10*time.Millisecond)
}

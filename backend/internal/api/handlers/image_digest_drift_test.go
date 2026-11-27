package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/helm"
	"backend/internal/imagedigest"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for #811 AC 10: the values drift checks mask the imageDigest output,
// so a deployed digest alone is no drift, and they make no registry request.

const (
	driftDigestA = "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	driftDigestB = "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func TestDriftValuesEqual(t *testing.T) {
	t.Parallel()

	m := helm.ImageDigestDriftMask
	tests := []struct {
		name     string
		current  string
		deployed string
		want     bool
	}{
		{name: "digest matches mask", current: "d: " + m + "\n", deployed: "d: " + driftDigestA + "\n", want: true},
		{name: "empty digest matches mask", current: "d: " + m + "\n", deployed: `d: ""`, want: true},
		{name: "prefix around mask", current: "d: x-" + m + "\n", deployed: "d: x-" + driftDigestB + "\n", want: true},
		{name: "two masks", current: "d: " + m + "/" + m + "\n", deployed: "d: " + driftDigestA + "/\n", want: true},
		{name: "nested map and list", current: "a:\n  b:\n    - " + m + "\n    - 1\n", deployed: "a:\n  b:\n    - " + driftDigestA + "\n    - 1\n", want: true},
		{name: "other value changed", current: "d: " + m + "\nr: 2\n", deployed: "d: " + driftDigestA + "\nr: 1\n", want: false},
		{name: "non-digest string at mask", current: "d: " + m + "\n", deployed: "d: latest\n", want: false},
		{name: "short digest at mask", current: "d: " + m + "\n", deployed: "d: sha256:abc\n", want: false},
		{name: "upper case digest at mask", current: "d: " + m + "\n", deployed: "d: " + strings.ToUpper(driftDigestA) + "\n", want: false},
		{name: "prefix differs", current: "d: x-" + m + "\n", deployed: "d: y-" + driftDigestA + "\n", want: false},
		{name: "key missing on deployed", current: "d: " + m + "\ne: 1\n", deployed: "d: " + driftDigestA + "\n", want: false},
		{name: "extra key on deployed", current: "d: " + m + "\n", deployed: "d: " + driftDigestA + "\ne: 1\n", want: false},
		{name: "number at mask", current: "d: " + m + "\n", deployed: "d: 1\n", want: false},
		{name: "regexp characters around mask are literal", current: "d: '.*" + m + "'\n", deployed: "d: abc\n", want: false},
		{name: "no mask uses plain compare", current: "a: 1\nb: 2\n", deployed: "b: 2\na: 1", want: true},
		{name: "no mask and a difference", current: "a: 1", deployed: "a: 2", want: false},
		{name: "invalid deployed YAML", current: "d: " + m + "\n", deployed: "d: : :", want: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, driftValuesEqual(tt.current, tt.deployed))
		})
	}
}

// countingRegistry counts every request it gets.
func countingRegistry(t *testing.T) (string, *atomic.Int32, *imagedigest.Service) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "https://")
	return host, &n, imagedigest.NewService([]string{host}, imagedigest.WithTransport(srv.Client().Transport))
}

// seedDigestRollbackInstance is seedRollbackInstance with an imageDigest
// value on chart web. The stored values render replicas: 2.
func seedDigestRollbackInstance(t *testing.T, env *lifecycleEnv, host string) {
	t.Helper()
	seedRollbackInstance(t, env)
	ch, err := env.cc.FindByID(context.Background(), "cc1")
	require.NoError(t, err)
	ch.DefaultValues = "replicas: 1\npodAnnotations:\n  deploy/image-digest: 'x-{{ imageDigest \"" + host + "/dev/app\" .ImageTag }}'\n"
	require.NoError(t, env.cc.Update(context.Background(), ch))
}

func digestDeployed(replicas, annotation string) string {
	values := "replicas: " + replicas + "\npodAnnotations:\n  deploy/image-digest: " + annotation + "\n"
	out, _ := json.Marshal(map[string]string{"web": values})
	return string(out)
}

func TestValuesDrift_ImageDigestMasked(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		running   string
		wantDrift bool
	}{
		{name: "deployed digest and nothing else changed", running: digestDeployed("2", "x-"+driftDigestA), wantDrift: false},
		{name: "deployed empty digest and nothing else changed", running: digestDeployed("2", "x-"), wantDrift: false},
		{name: "deployed digest and another value changed", running: digestDeployed("1", "x-"+driftDigestA), wantDrift: true},
		{name: "non-digest string at the masked path", running: digestDeployed("2", "x-latest"), wantDrift: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			host, requests, svc := countingRegistry(t)
			env := newLifecycleEnv()
			env.imageDigests = svc
			seedDigestRollbackInstance(t, env, host)
			inst, err := env.inst.FindByID(context.Background(), "i1")
			require.NoError(t, err)
			inst.LastDeployedValues = tt.running
			require.NoError(t, env.inst.Update(context.Background(), inst))
			seedLog(t, env, models.DeploymentLog{ID: "rb", StackInstanceID: "i1", Action: models.DeployActionRollback, Status: models.DeployLogSuccess})

			w := serve(env.router(t, nil, "uid-1", "alice", "user"), http.MethodGet, "/api/v1/stack-instances/i1", "")
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			var got map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
			drift, _ := got["values_drift"].(bool)
			assert.Equal(t, tt.wantDrift, drift)
			assert.Zero(t, requests.Load(), "the drift check makes no registry request")
		})
	}
}

func TestRollbackTarget_ImageDigestMasked(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		snapshot  string
		wantDrift bool
	}{
		{name: "target digest and nothing else changed", snapshot: digestDeployed("2", "x-"+driftDigestB), wantDrift: false},
		{name: "target digest and another value changed", snapshot: digestDeployed("1", "x-"+driftDigestB), wantDrift: true},
		{name: "non-digest string at the masked path", snapshot: digestDeployed("2", "x-v1"), wantDrift: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			host, requests, svc := countingRegistry(t)
			env := newLifecycleEnv()
			env.imageDigests = svc
			seedDigestRollbackInstance(t, env, host)
			seedLog(t, env, models.DeploymentLog{ID: "dep-t", StackInstanceID: "i1", Action: models.DeployActionDeploy, Status: models.DeployLogSuccess, ValuesSnapshot: tt.snapshot, StartedAt: time.Now().Add(-time.Hour)})
			mgr := newTestManager(env.inst, env.logs)

			w := serve(env.router(t, mgr, "uid-1", "alice", "user"), http.MethodPost, "/api/v1/stack-instances/i1/rollback", `{"target_log_id":"dep-t"}`)
			require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
			var resp RollbackResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			require.NotNil(t, resp.ValuesDrift)
			assert.Equal(t, tt.wantDrift, *resp.ValuesDrift)
			assert.Equal(t, tt.wantDrift, resp.Warning != "")
			assert.Zero(t, requests.Load(), "the drift check makes no registry request")
		})
	}
}

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/cluster"
	"backend/internal/config"
	"backend/internal/helm"
	"backend/internal/k8s"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

// hintPod is a ready pod of a release with the given containers. It does not
// automount the service-account token.
func hintPod(name, release string, containers ...string) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "stack-ns", Labels: map[string]string{"app.kubernetes.io/instance": release}},
		Spec:       corev1.PodSpec{AutomountServiceAccountToken: boolPtr(false)},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
	for _, c := range containers {
		p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: c})
	}
	return p
}

type hintFixture struct {
	*execFixture
	router  *gin.Engine
	watcher *k8s.Watcher
	cs      *fake.Clientset
}

type hintFixtureOpts struct {
	exec    execFixtureOpts
	objs    []runtime.Object
	real    bool // answer ExecContainers with the real client logic
	watcher bool // serve /status from the watcher cache
	noHints bool // do not wire the hint provider
}

// newHintFixture wires /status and /pods of an InstanceHandler with the exec
// hints of a StreamHandler. The caller comes from the headers X-Test-User and
// X-Test-Role.
func newHintFixture(t *testing.T, o hintFixtureOpts) *hintFixture {
	t.Helper()
	f := &hintFixture{execFixture: newExecFixture(t, o.exec)}
	objs := append([]runtime.Object{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-ns"}}}, o.objs...)
	f.cs = fake.NewSimpleClientset(objs...)
	client := k8s.NewClientFromInterface(f.cs)
	if o.real {
		f.exec.inner = client
	}
	registry := cluster.NewRegistryForTest("cl-1", client, nil)
	if o.watcher {
		f.watcher = k8s.NewWatcher(registry, f.instRepo, nil, 10*time.Millisecond)
		f.watcher.Start(context.Background())
		t.Cleanup(f.watcher.Stop)
	}
	h, err := NewInstanceHandlerWithDeployer(
		f.instRepo, NewMockValueOverrideRepository(), nil, NewMockStackDefinitionRepository(), f.charts,
		NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), helm.NewValuesGenerator(),
		NewMockUserRepository(), nil, f.watcher, registry, NewMockDeploymentLogRepository(), nil,
		0, &mockHandlerTxRunner{},
	)
	require.NoError(t, err)
	if !o.noHints {
		h.WithExecHints(f.handler)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userID", c.GetHeader("X-Test-User"))
		c.Set("role", c.GetHeader("X-Test-Role"))
		method := "jwt"
		if m := c.GetHeader("X-Test-Auth"); m != "" {
			method = m
		}
		c.Set("authMethod", method)
		c.Next()
	})
	r.GET("/api/v1/stack-instances/:id/status", h.GetInstanceStatus)
	r.GET("/api/v1/stack-instances/:id/pods", h.GetInstancePods)
	f.router = r
	if o.watcher {
		require.Eventually(t, func() bool { _, ok := f.watcher.GetStatus("inst-1"); return ok }, 5*time.Second, 5*time.Millisecond)
	}
	return f
}

// get calls the route as a user and returns the status and the raw body.
func (f *hintFixture) get(t *testing.T, path, user, role string) (int, []byte) {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Test-User", user)
	req.Header.Set("X-Test-Role", role)
	f.router.ServeHTTP(w, req)
	return w.Code, w.Body.Bytes()
}

func decodeStatus(t *testing.T, body []byte) (k8s.NamespaceStatus, map[string]any) {
	t.Helper()
	var st k8s.NamespaceStatus
	require.NoError(t, json.Unmarshal(body, &st), string(body))
	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	return st, raw
}

func chartOf(st k8s.NamespaceStatus, release string) *k8s.ChartStatus {
	for i := range st.Charts {
		if st.Charts[i].ReleaseName == release {
			return &st.Charts[i]
		}
	}
	return nil
}

func TestExecHints_Status(t *testing.T) {
	t.Parallel()
	pods := []runtime.Object{hintPod("web-0", "web", "web", "worker", "sidecar"), hintPod("foreign-0", "foreign", "web")}
	cannedWeb := map[string]k8s.ReleaseExecContainers{
		"web":     {Containers: []string{"web", "worker"}, Default: "web"},
		"foreign": {Containers: []string{"web"}, Default: "web"}, // not a chart of the definition
	}
	execOff := execStreamCfg()
	execOff.Exec.Enabled = false
	emptyList := execStreamCfg()
	emptyList.Exec.AllowedContainers = nil

	tests := []struct {
		name      string
		opts      hintFixtureOpts
		query     string
		user      string
		role      string
		wantHints bool
		wantWeb   []string
		wantDef   string
		wantCalls int
		wantRel   []string
	}{
		{name: "no query: unchanged, no hint calls", query: "", user: "owner-1", role: "user", wantCalls: 0},
		{name: "false: unchanged", query: "?exec_hints=false", user: "owner-1", role: "user", wantCalls: 0},
		{name: "not a boolean: off", query: "?exec_hints=maybe", user: "owner-1", role: "user", wantCalls: 0},
		{name: "owner", query: "?exec_hints=true", user: "owner-1", role: "user", wantHints: true, wantWeb: []string{"web", "worker"}, wantDef: "web", wantCalls: 1, wantRel: []string{"web"}},
		{name: "1 is true", query: "?exec_hints=1", user: "owner-1", role: "user", wantHints: true, wantWeb: []string{"web", "worker"}, wantDef: "web", wantCalls: 1, wantRel: []string{"web"}},
		{name: "devops", query: "?exec_hints=true", user: "other", role: "devops", wantHints: true, wantWeb: []string{"web", "worker"}, wantDef: "web", wantCalls: 1, wantRel: []string{"web"}},
		{name: "admin", query: "?exec_hints=true", user: "other", role: "admin", wantHints: true, wantWeb: []string{"web", "worker"}, wantDef: "web", wantCalls: 1, wantRel: []string{"web"}},
		{name: "another user gets none", query: "?exec_hints=true", user: "other", role: "user", wantCalls: 0},
		{name: "exec off", opts: hintFixtureOpts{exec: execFixtureOpts{cfg: execOff}}, query: "?exec_hints=true", user: "owner-1", role: "user", wantCalls: 0},
		{name: "cluster switch off", opts: hintFixtureOpts{exec: execFixtureOpts{cluster: &models.Cluster{ID: "cl-1", AllowExec: false}}}, query: "?exec_hints=true", user: "owner-1", role: "user", wantCalls: 0},
		{name: "empty allowed list: flag, no lists, no cluster calls", opts: hintFixtureOpts{exec: execFixtureOpts{cfg: emptyList}}, query: "?exec_hints=true", user: "owner-1", role: "user", wantHints: true, wantCalls: 0},
		{name: "instance not live: flag, no lists", opts: hintFixtureOpts{exec: execFixtureOpts{instance: &models.StackInstance{ID: "inst-1", OwnerID: "owner-1", Namespace: "stack-ns", ClusterID: "cl-1", StackDefinitionID: "def-1", Status: models.StackStatusStopped}}}, query: "?exec_hints=true", user: "owner-1", role: "user", wantHints: true, wantCalls: 0},
	}
	for _, tt := range tests {
		tt := tt
		for _, path := range []string{"direct", "watcher", "pods"} {
			path := path
			t.Run(tt.name+"/"+path, func(t *testing.T) {
				t.Parallel()
				o := tt.opts
				// The repository mocks write to what they store: copy.
				if o.exec.cluster != nil {
					cl := *o.exec.cluster
					o.exec.cluster = &cl
				}
				if o.exec.instance != nil {
					in := *o.exec.instance
					o.exec.instance = &in
				}
				o.objs = pods
				o.watcher = path == "watcher" && o.exec.instance == nil
				f := newHintFixture(t, o)
				f.exec.hints = cannedWeb
				route := "status"
				if path == "pods" {
					route = "pods"
				}
				code, body := f.get(t, "/api/v1/stack-instances/inst-1/"+route+tt.query, tt.user, tt.role)
				require.Equal(t, http.StatusOK, code, string(body))
				st, raw := decodeStatus(t, body)
				assert.Equal(t, "stack-ns", st.Namespace)
				assert.Equal(t, tt.wantHints, st.ExecHints)
				if !tt.wantHints {
					assert.NotContains(t, raw, "exec_hints")
				}
				assert.Equal(t, tt.wantCalls, f.exec.calledCount("hints"))
				for _, ch := range st.Charts {
					if ch.ReleaseName == "web" {
						assert.Equal(t, tt.wantWeb, ch.ExecContainers)
						assert.Equal(t, tt.wantDef, ch.ExecDefaultContainer)
					} else {
						assert.Empty(t, ch.ExecContainers, ch.ReleaseName)
						assert.Empty(t, ch.ExecDefaultContainer, ch.ReleaseName)
					}
				}
				if tt.wantCalls > 0 {
					assert.Equal(t, tt.wantRel, f.exec.hintReleases)
				}
			})
		}
	}
}

func TestExecHints_FailuresNeverFailTheStatus(t *testing.T) {
	t.Parallel()
	pods := []runtime.Object{hintPod("web-0", "web", "web")}
	shared := &models.StackInstance{ID: "inst-2", OwnerID: "x", Namespace: "stack-ns", ClusterID: "cl-1", StackDefinitionID: "def-9", Status: models.StackStatusRunning}
	tests := []struct {
		name   string
		mutate func(f *hintFixture)
	}{
		{"exec client error", func(f *hintFixture) { f.exec.hintsErr = errors.New("boom") }},
		{"namespace check fails", func(f *hintFixture) { f.exec.nsErr = errors.New("not a stack namespace") }},
		{"namespace shared lookup fails", func(f *hintFixture) { f.instRepo.namespaceSharedErr = errors.New("db down") }},
		{"namespace shared with another instance", func(f *hintFixture) { require.NoError(t, f.instRepo.Create(context.Background(), shared)) }},
	}
	for _, tt := range tests {
		tt := tt
		for _, route := range []string{"status", "pods"} {
			route := route
			t.Run(tt.name+"/"+route, func(t *testing.T) {
				t.Parallel()
				f := newHintFixture(t, hintFixtureOpts{objs: pods})
				f.exec.hints = map[string]k8s.ReleaseExecContainers{"web": {Containers: []string{"web"}, Default: "web"}}
				tt.mutate(f)
				code, body := f.get(t, "/api/v1/stack-instances/inst-1/"+route+"?exec_hints=true", "owner-1", "user")
				// A shared namespace may also fail the status mock; the
				// status itself must still be answered.
				require.Equal(t, http.StatusOK, code, string(body))
				st, _ := decodeStatus(t, body)
				assert.True(t, st.ExecHints)
				for _, ch := range st.Charts {
					assert.Empty(t, ch.ExecContainers)
					assert.Empty(t, ch.ExecDefaultContainer)
				}
			})
		}
	}
}

func TestExecHints_NotWired(t *testing.T) {
	t.Parallel()
	f := newHintFixture(t, hintFixtureOpts{noHints: true, objs: []runtime.Object{hintPod("web-0", "web", "web")}})
	code, body := f.get(t, "/api/v1/stack-instances/inst-1/status?exec_hints=true", "owner-1", "user")
	require.Equal(t, http.StatusOK, code)
	st, _ := decodeStatus(t, body)
	assert.False(t, st.ExecHints)
}

// With the real client logic: the allowed list, the ready pods and the
// service-account-token rule decide the lists.
func TestExecHints_RealSelection(t *testing.T) {
	t.Parallel()
	tokenVolume := []corev1.Volume{{Name: "tok", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
		Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "token"}}},
	}}}}
	// "web" (the default container) holds a token, "worker" does not.
	tokenDefault := hintPod("web-0", "web", "sidecar", "web", "worker")
	tokenDefault.Spec.Volumes = tokenVolume
	tokenDefault.Spec.Containers[1].VolumeMounts = []corev1.VolumeMount{{Name: "tok", MountPath: "/tok"}}
	plain := hintPod("web-0", "web", "sidecar", "worker", "web")
	notReady := hintPod("web-0", "web", "web")
	notReady.Status.Conditions[0].Status = corev1.ConditionFalse

	allowToken := execStreamCfg()
	allowToken.Exec.AllowServiceAccountToken = true

	tests := []struct {
		name    string
		cfg     config.StreamConfig
		pod     *corev1.Pod
		wantWeb []string
		wantDef string
	}{
		{"sidecar outside the list is left out", execStreamCfg(), plain, []string{"worker", "web"}, "worker"},
		{"default container holds a token: no default, the other is listed", execStreamCfg(), tokenDefault, []string{"worker"}, ""},
		{"service-account token allowed", allowToken, tokenDefault, []string{"web", "worker"}, "web"},
		{"pod not ready", execStreamCfg(), notReady, nil, ""},
	}
	for _, tt := range tests {
		tt := tt
		for _, watcher := range []bool{false, true} {
			watcher := watcher
			src := "direct"
			if watcher {
				src = "watcher"
			}
			t.Run(tt.name+"/"+src, func(t *testing.T) {
				t.Parallel()
				f := newHintFixture(t, hintFixtureOpts{exec: execFixtureOpts{cfg: tt.cfg}, objs: []runtime.Object{tt.pod}, real: true, watcher: watcher})
				code, body := f.get(t, "/api/v1/stack-instances/inst-1/status?exec_hints=true", "owner-1", "user")
				require.Equal(t, http.StatusOK, code, string(body))
				st, _ := decodeStatus(t, body)
				assert.True(t, st.ExecHints)
				ch := chartOf(st, "web")
				require.NotNil(t, ch)
				assert.Equal(t, tt.wantWeb, ch.ExecContainers)
				assert.Equal(t, tt.wantDef, ch.ExecDefaultContainer)
				assert.Equal(t, tt.cfg.Exec.AllowServiceAccountToken, f.exec.lastHintReq.AllowServiceAccountToken)
			})
		}
	}
}

// The watcher cache and the shared status read hand the same object to every
// caller: a call with hints must not change it, and a later call without
// rights must not see the lists.
func TestExecHints_DoNotMutateSharedStatus(t *testing.T) {
	t.Parallel()
	f := newHintFixture(t, hintFixtureOpts{watcher: true, real: true, objs: []runtime.Object{hintPod("web-0", "web", "web", "worker")}})

	cached, ok := f.watcher.GetStatus("inst-1")
	require.True(t, ok)
	before, err := json.Marshal(cached)
	require.NoError(t, err)

	_, body := f.get(t, "/api/v1/stack-instances/inst-1/status?exec_hints=true", "owner-1", "user")
	st, _ := decodeStatus(t, body)
	require.NotNil(t, chartOf(st, "web"))
	assert.NotEmpty(t, chartOf(st, "web").ExecContainers)

	_, body = f.get(t, "/api/v1/stack-instances/inst-1/status?exec_hints=true", "other", "user")
	st, raw := decodeStatus(t, body)
	assert.False(t, st.ExecHints)
	assert.NotContains(t, raw, "exec_hints")
	assert.Empty(t, chartOf(st, "web").ExecContainers)

	after, err := json.Marshal(cached)
	require.NoError(t, err)
	assert.JSONEq(t, string(before), string(after))
	// The watcher swaps in a fresh object on each refresh, so the check is on
	// the object that was read before, not on the pointer in the cache.
}

func TestWithExecHints_CopiesTheStatus(t *testing.T) {
	t.Parallel()
	f := newHintFixture(t, hintFixtureOpts{})
	f.exec.hints = map[string]k8s.ReleaseExecContainers{"web": {Containers: []string{"web"}, Default: "web"}}
	h := &InstanceHandler{execHints: f.handler}
	inst, err := f.instRepo.FindByID(context.Background(), "inst-1")
	require.NoError(t, err)
	ns := &k8s.NamespaceStatus{Namespace: "stack-ns", Charts: []k8s.ChartStatus{{ReleaseName: "web"}}}

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/x?exec_hints=true", nil)
	c.Set("userID", "owner-1")
	c.Set("role", "user")
	out, ok := h.withExecHints(c, inst, ns)
	require.True(t, ok)
	assert.NotSame(t, ns, out)
	assert.True(t, out.ExecHints)
	assert.Equal(t, []string{"web"}, out.Charts[0].ExecContainers)
	assert.False(t, ns.ExecHints)
	assert.Empty(t, ns.Charts[0].ExecContainers)
	assert.Empty(t, ns.Charts[0].ExecDefaultContainer)
}

func TestExecHints_CancelledRequest(t *testing.T) {
	t.Parallel()
	f := newHintFixture(t, hintFixtureOpts{objs: []runtime.Object{hintPod("web-0", "web", "web")}})
	f.exec.hintDelay = make(chan struct{}) // never released
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/stack-instances/inst-1/pods?exec_hints=true", nil).WithContext(ctx)
	req.Header.Set("X-Test-User", "owner-1")
	req.Header.Set("X-Test-Role", "user")
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { f.router.ServeHTTP(w, req); close(done) }()
	require.Eventually(t, func() bool { return f.exec.calledCount("hints") == 1 }, 5*time.Second, 5*time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the request did not end after the cancel")
	}
	assert.Equal(t, statusClientClosedRequest, w.Code)
}

// joinCounter counts the callers that joined the flight and closes all once n
// have.
func joinCounter(n int) (hook func(), all <-chan struct{}) {
	var joined atomic.Int32
	ch := make(chan struct{})
	return func() {
		if int(joined.Add(1)) == n {
			close(ch)
		}
	}, ch
}

func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: " + what)
	}
}

// Concurrent calls for one instance share one computation. The computation is
// held until every caller has joined the flight (no sleeps).
func TestExecHints_ConcurrentCallsShareOneComputation(t *testing.T) {
	t.Parallel()
	f := newHintFixture(t, hintFixtureOpts{objs: []runtime.Object{hintPod("web-0", "web", "web")}})
	f.exec.hints = map[string]k8s.ReleaseExecContainers{"web": {Containers: []string{"web"}, Default: "web"}}
	f.exec.hintDelay = make(chan struct{})
	const n = 4
	hook, allJoined := joinCounter(n)
	f.handler.hintJoined = hook

	type result struct {
		code int
		body []byte
	}
	results := make(chan result, n)
	for i := 0; i < n; i++ {
		go func() {
			code, body := f.get(t, "/api/v1/stack-instances/inst-1/status?exec_hints=true", "owner-1", "user")
			results <- result{code, body}
		}()
	}
	waitClosed(t, allJoined, "the callers did not all join the flight")
	close(f.exec.hintDelay)
	for i := 0; i < n; i++ {
		r := <-results
		require.Equal(t, http.StatusOK, r.code, string(r.body))
		st, _ := decodeStatus(t, r.body)
		assert.Equal(t, []string{"web"}, chartOf(st, "web").ExecContainers)
	}
	assert.Equal(t, 1, f.exec.calledCount("hints"))
}

// The first caller leaves; the flight is detached from it, so a joiner still
// gets its hints.
func TestExecHints_FirstCallerCancelsJoinerStillGetsHints(t *testing.T) {
	t.Parallel()
	f := newHintFixture(t, hintFixtureOpts{objs: []runtime.Object{hintPod("web-0", "web", "web")}})
	f.exec.hints = map[string]k8s.ReleaseExecContainers{"web": {Containers: []string{"web"}, Default: "web"}}
	f.exec.hintDelay = make(chan struct{})
	hook, bothJoined := joinCounter(2)
	f.handler.hintJoined = hook

	do := func(ctx context.Context) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/stack-instances/inst-1/status?exec_hints=true", nil).WithContext(ctx)
		req.Header.Set("X-Test-User", "owner-1")
		req.Header.Set("X-Test-Role", "user")
		w := httptest.NewRecorder()
		f.router.ServeHTTP(w, req)
		return w
	}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- do(firstCtx) }()
	require.Eventually(t, func() bool { return f.exec.calledCount("hints") == 1 }, 5*time.Second, time.Millisecond)
	second := make(chan *httptest.ResponseRecorder, 1)
	go func() { second <- do(context.Background()) }()
	waitClosed(t, bothJoined, "the joiner did not join the flight")

	cancelFirst()
	select {
	case w := <-first:
		assert.Equal(t, statusClientClosedRequest, w.Code)
	case <-time.After(5 * time.Second):
		t.Fatal("the first request did not end after the cancel")
	}
	close(f.exec.hintDelay)
	var w *httptest.ResponseRecorder
	select {
	case w = <-second:
	case <-time.After(5 * time.Second):
		t.Fatal("the joiner did not end")
	}
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	st, _ := decodeStatus(t, w.Body.Bytes())
	assert.True(t, st.ExecHints)
	assert.Equal(t, []string{"web"}, chartOf(st, "web").ExecContainers)
	assert.Equal(t, 1, f.exec.calledCount("hints"))
}

// A caller waits at most hintWait. It gets exec_hints=true without lists; the
// flight goes on and a later call joins it.
func TestExecHints_WaitTimeout(t *testing.T) {
	t.Parallel()
	f := newHintFixture(t, hintFixtureOpts{objs: []runtime.Object{hintPod("web-0", "web", "web")}})
	f.exec.hints = map[string]k8s.ReleaseExecContainers{"web": {Containers: []string{"web"}, Default: "web"}}
	f.exec.hintDelay = make(chan struct{})
	f.handler.hintWait = 30 * time.Millisecond

	start := time.Now()
	code, body := f.get(t, "/api/v1/stack-instances/inst-1/status?exec_hints=true", "owner-1", "user")
	require.Equal(t, http.StatusOK, code, string(body))
	assert.Less(t, time.Since(start), 4*time.Second)
	st, _ := decodeStatus(t, body)
	assert.True(t, st.ExecHints)
	assert.Empty(t, chartOf(st, "web").ExecContainers)
	assert.Empty(t, chartOf(st, "web").ExecDefaultContainer)

	// A later call joins the running flight and uses its result.
	f.handler.hintWait = 5 * time.Second
	hook, joined := joinCounter(1)
	f.handler.hintJoined = hook
	later := make(chan []byte, 1)
	go func() {
		_, b := f.get(t, "/api/v1/stack-instances/inst-1/status?exec_hints=true", "owner-1", "user")
		later <- b
	}()
	waitClosed(t, joined, "the later call did not join the flight")
	close(f.exec.hintDelay)
	select {
	case b := <-later:
		st, _ := decodeStatus(t, b)
		assert.Equal(t, []string{"web"}, chartOf(st, "web").ExecContainers)
	case <-time.After(5 * time.Second):
		t.Fatal("the later call did not end")
	}
	assert.Equal(t, 1, f.exec.calledCount("hints"))
}

// A panic in the computation is recovered: the status is answered with
// exec_hints=true and no lists.
func TestExecHints_PanicIsRecovered(t *testing.T) {
	t.Parallel()
	f := newHintFixture(t, hintFixtureOpts{objs: []runtime.Object{hintPod("web-0", "web", "web")}})
	f.exec.hintsPanic = true
	code, body := f.get(t, "/api/v1/stack-instances/inst-1/status?exec_hints=true", "owner-1", "user")
	require.Equal(t, http.StatusOK, code, string(body))
	st, _ := decodeStatus(t, body)
	assert.True(t, st.ExecHints)
	assert.Empty(t, chartOf(st, "web").ExecContainers)
}

// A cluster lookup that fails with something else than 404 gives no hints.
func TestExecHints_ClusterLookupError(t *testing.T) {
	t.Parallel()
	f := newHintFixture(t, hintFixtureOpts{objs: []runtime.Object{hintPod("web-0", "web", "web")}})
	f.clusters.fetchErr = errors.New("db down")
	f.exec.hints = map[string]k8s.ReleaseExecContainers{"web": {Containers: []string{"web"}, Default: "web"}}
	hints, ok := f.callHints(t, "owner-1", "user")
	assert.False(t, ok)
	assert.Nil(t, hints)
	assert.Zero(t, f.exec.calledCount("hints"))

	code, body := f.get(t, "/api/v1/stack-instances/inst-1/status?exec_hints=true", "owner-1", "user")
	require.Equal(t, http.StatusOK, code, string(body))
	st, raw := decodeStatus(t, body)
	assert.False(t, st.ExecHints)
	assert.NotContains(t, raw, "exec_hints")
}

// callHints calls ExecHints for inst-1 as the user.
func (f *hintFixture) callHints(t *testing.T, user, role string) (map[string]k8s.ReleaseExecContainers, bool) {
	t.Helper()
	inst, err := f.instRepo.FindByID(context.Background(), "inst-1")
	require.NoError(t, err)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/x", nil)
	c.Set("userID", user)
	c.Set("role", role)
	hints, ok, err := f.handler.ExecHints(c, inst)
	require.NoError(t, err)
	return hints, ok
}

// Missing repositories must not panic: no hints.
func TestExecHints_NilRepositories(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cut  func(h *StreamHandler)
	}{
		{"cluster repo", func(h *StreamHandler) { h.clusterRepo = nil }},
		{"instance repo", func(h *StreamHandler) { h.instanceRepo = nil }},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newHintFixture(t, hintFixtureOpts{objs: []runtime.Object{hintPod("web-0", "web", "web")}})
			f.exec.hints = map[string]k8s.ReleaseExecContainers{"web": {Containers: []string{"web"}, Default: "web"}}
			tt.cut(f.handler)
			var hints map[string]k8s.ReleaseExecContainers
			var ok bool
			require.NotPanics(t, func() { hints, ok = f.callHints(t, "owner-1", "user") })
			assert.False(t, ok)
			assert.Nil(t, hints)
		})
	}
}

// A chart name that the route refuses (invalid_chart) gets no hint and is not
// asked for.
func TestExecHints_InvalidChartNameGetsNoHint(t *testing.T) {
	t.Parallel()
	f := newHintFixture(t, hintFixtureOpts{objs: []runtime.Object{hintPod("web-0", "web", "web")}})
	seedChartConfig(t, f.charts, "cc-bad", "def-1", "bad_name")
	seedChartConfig(t, f.charts, "cc-long", "def-1", strings.Repeat("a", maxExecChartNameLength+1))
	f.exec.hints = map[string]k8s.ReleaseExecContainers{
		"web":      {Containers: []string{"web"}, Default: "web"},
		"bad_name": {Containers: []string{"web"}, Default: "web"},
	}
	hints, ok := f.callHints(t, "owner-1", "user")
	require.True(t, ok)
	assert.Contains(t, hints, "web")
	assert.NotContains(t, hints, "bad_name")
	assert.Equal(t, []string{"web"}, f.exec.hintReleases)
}

func TestValidExecChartName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"plain", "web", true},
		{"dots and dashes", "my-app.v2", true},
		{"empty", "", false},
		{"underscore", "bad_name", false},
		{"upper case", "Web", false},
		{"53 characters", strings.Repeat("a", 53), true},
		{"54 characters", strings.Repeat("a", 54), false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, validExecChartName(tt.in))
		})
	}
}

// The exec route refuses an API key unless STACK_STREAM_ALLOW_API_KEYS is on;
// the hints follow it.
func TestExecHints_APIKey(t *testing.T) {
	t.Parallel()
	allowKeys := execStreamCfg()
	allowKeys.AllowAPIKeys = true
	tests := []struct {
		name     string
		opts     hintFixtureOpts
		auth     string
		wantHint bool
	}{
		{"API key refused", hintFixtureOpts{}, "api_key", false},
		{"API key allowed", hintFixtureOpts{exec: execFixtureOpts{cfg: allowKeys}}, "api_key", true},
		{"session", hintFixtureOpts{}, "jwt", true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			o := tt.opts
			o.objs = []runtime.Object{hintPod("web-0", "web", "web")}
			f := newHintFixture(t, o)
			f.exec.hints = map[string]k8s.ReleaseExecContainers{"web": {Containers: []string{"web"}, Default: "web"}}
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/stack-instances/inst-1/status?exec_hints=true", nil)
			req.Header.Set("X-Test-User", "owner-1")
			req.Header.Set("X-Test-Role", "user")
			req.Header.Set("X-Test-Auth", tt.auth)
			f.router.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code)
			st, _ := decodeStatus(t, w.Body.Bytes())
			assert.Equal(t, tt.wantHint, st.ExecHints)
		})
	}
}

// A cluster that does not exist allows nothing: no hints at all.
func TestExecHints_MissingCluster(t *testing.T) {
	t.Parallel()
	f := newHintFixture(t, hintFixtureOpts{})
	inst := &models.StackInstance{ID: "x", OwnerID: "owner-1", Namespace: "stack-ns", ClusterID: "gone", StackDefinitionID: "def-1", Status: models.StackStatusRunning}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/x", nil)
	c.Set("userID", "owner-1")
	c.Set("role", "user")
	hints, ok, err := f.handler.ExecHints(c, inst)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Nil(t, hints)
}

package k8s

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// countActions returns how many actions of verb on resource the fake saw.
func countActions(cs *fake.Clientset, verb, resource string) int {
	n := 0
	for _, a := range cs.Actions() {
		if a.Matches(verb, resource) {
			n++
		}
	}
	return n
}

func TestExecContainers(t *testing.T) {
	t.Parallel()
	inst := map[string]string{releaseLabelInstance: "web"}
	helm := map[string]string{releaseLabelHelm: "web"}
	both := map[string]string{releaseLabelInstance: "web", releaseLabelHelm: "web"}

	terminating := execPod("a-term", inst, "app")
	now := metav1.Now()
	terminating.DeletionTimestamp = &now
	notReady := execPod("a-notready", inst, "app")
	notReady.Status.Conditions[0].Status = corev1.ConditionFalse

	// automount on: the container holds a token.
	withToken := func(name string, containers ...string) *corev1.Pod {
		p := execPod(name, inst, containers...)
		p.Spec.AutomountServiceAccountToken = nil
		p.Spec.ServiceAccountName = "robot"
		return p
	}
	// "app" holds a projected token, "worker" does not.
	mixed := execPod("m", inst, "app", "worker")
	mixed.Spec.Volumes = []corev1.Volume{{Name: "tok", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
		Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "token"}}},
	}}}}
	mixed.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "tok", MountPath: "/tok"}}

	shared := execPod("s", inst, "app", "worker")
	shared.Spec.ShareProcessNamespace = boolPtr(true)
	shared.Spec.Volumes = mixed.Spec.Volumes
	shared.Spec.Containers[1].VolumeMounts = []corev1.VolumeMount{{Name: "tok", MountPath: "/tok"}}

	allow := allowList("app", "worker")
	tests := []struct {
		name string
		objs []runtime.Object
		req  ExecContainersRequest
		want map[string]ReleaseExecContainers
	}{
		{name: "pods by name, containers in spec order, first occurrence",
			objs: []runtime.Object{execPod("b", inst, "worker", "app", "side"), execPod("a", helm, "app", "extra")},
			req:  ExecContainersRequest{AllowedContainer: allowList("app", "worker", "extra")},
			want: map[string]ReleaseExecContainers{"web": {Containers: []string{"app", "extra", "worker"}, Default: "app"}}},
		{name: "both label keys, a pod with both is one pod",
			objs: []runtime.Object{execPod("a", inst, "app"), execPod("b", helm, "worker"), execPod("c", both, "app")},
			req:  ExecContainersRequest{AllowedContainer: allow},
			want: map[string]ReleaseExecContainers{"web": {Containers: []string{"app", "worker"}, Default: "app"}}},
		{name: "sidecar not in the list is left out",
			objs: []runtime.Object{execPod("a", inst, "istio-proxy", "app")},
			req:  ExecContainersRequest{AllowedContainer: allow},
			want: map[string]ReleaseExecContainers{"web": {Containers: []string{"app"}, Default: "app"}}},
		{name: "only a sidecar: no entry",
			objs: []runtime.Object{execPod("a", inst, "istio-proxy")},
			req:  ExecContainersRequest{AllowedContainer: allow}, want: map[string]ReleaseExecContainers{}},
		{name: "not ready and terminating pods are left out",
			objs: []runtime.Object{terminating, notReady},
			req:  ExecContainersRequest{AllowedContainer: allow}, want: map[string]ReleaseExecContainers{}},
		{name: "nil allow func allows nothing",
			objs: []runtime.Object{execPod("a", inst, "app")},
			req:  ExecContainersRequest{}, want: map[string]ReleaseExecContainers{}},
		{name: "default container holds a token: no default, the other is listed",
			objs: []runtime.Object{mixed},
			req:  ExecContainersRequest{AllowedContainer: allow},
			want: map[string]ReleaseExecContainers{"web": {Containers: []string{"worker"}}}},
		{name: "allow switch lists the token container",
			objs: []runtime.Object{mixed},
			req:  ExecContainersRequest{AllowedContainer: allow, AllowServiceAccountToken: true},
			want: map[string]ReleaseExecContainers{"web": {Containers: []string{"app", "worker"}, Default: "app"}}},
		{name: "pod automounts through its service account",
			objs: []runtime.Object{withToken("a", "app")},
			req:  ExecContainersRequest{AllowedContainer: allow}, want: map[string]ReleaseExecContainers{}},
		{name: "shared process namespace: a token in any container leaves all out",
			objs: []runtime.Object{shared},
			req:  ExecContainersRequest{AllowedContainer: allow}, want: map[string]ReleaseExecContainers{}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cs := fake.NewSimpleClientset(append(tt.objs, &corev1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{Name: "robot", Namespace: "ns"}, AutomountServiceAccountToken: boolPtr(true),
			})...)
			got, _, err := NewClientFromInterface(cs).ExecContainers(context.Background(), "ns", []string{"web"}, tt.req)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.LessOrEqual(t, countActions(cs, "list", "pods"), 1)
		})
	}
}

func TestExecContainers_OnePodListForManyReleases(t *testing.T) {
	t.Parallel()
	objs := []runtime.Object{
		execPod("a", map[string]string{releaseLabelInstance: "web"}, "app"),
		execPod("b", map[string]string{releaseLabelHelm: "api"}, "app"),
		execPod("c", map[string]string{releaseLabelInstance: "other"}, "app"),
	}
	cs := fake.NewSimpleClientset(objs...)
	got, _, err := NewClientFromInterface(cs).ExecContainers(context.Background(), "ns",
		[]string{"web", "api", "db", "web"}, ExecContainersRequest{AllowedContainer: allowList("app")})
	require.NoError(t, err)
	assert.Equal(t, 1, countActions(cs, "list", "pods"))
	assert.Len(t, got, 2)
	assert.Equal(t, []string{"app"}, got["web"].Containers)
	assert.Equal(t, []string{"app"}, got["api"].Containers)
}

func TestExecContainers_NoReleasesNoCalls(t *testing.T) {
	t.Parallel()
	cs := fake.NewSimpleClientset()
	got, _, err := NewClientFromInterface(cs).ExecContainers(context.Background(), "ns", nil, ExecContainersRequest{AllowedContainer: allowList("app")})
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Empty(t, cs.Actions())
}

func TestExecContainers_ListError(t *testing.T) {
	t.Parallel()
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("boom")
	})
	_, _, err := NewClientFromInterface(cs).ExecContainers(context.Background(), "ns", []string{"web"}, ExecContainersRequest{AllowedContainer: allowList("app")})
	require.Error(t, err)
}

// Each ServiceAccount and each Secret is read once per call, however many
// pods, containers and releases use it.
func TestExecContainers_TokenReadsAreDeduplicated(t *testing.T) {
	t.Parallel()
	mk := func(name, release string) *corev1.Pod {
		p := execPod(name, map[string]string{releaseLabelInstance: release}, "app", "worker")
		p.Spec.AutomountServiceAccountToken = nil // read the ServiceAccount
		p.Spec.ServiceAccountName = "robot"
		p.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "shared"}}}}
		p.Spec.Containers[1].EnvFrom = p.Spec.Containers[0].EnvFrom
		return p
	}
	cs := fake.NewSimpleClientset(mk("a", "web"), mk("b", "web"), mk("c", "api"),
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "robot", Namespace: "ns"}, AutomountServiceAccountToken: boolPtr(false)},
		secretObj("shared", corev1.SecretTypeOpaque))
	got, _, err := NewClientFromInterface(cs).ExecContainers(context.Background(), "ns", []string{"web", "api"},
		ExecContainersRequest{AllowedContainer: allowList("app", "worker")})
	require.NoError(t, err)
	assert.Equal(t, []string{"app", "worker"}, got["web"].Containers)
	assert.Equal(t, []string{"app", "worker"}, got["api"].Containers)
	assert.Equal(t, 1, countActions(cs, "get", "serviceaccounts"))
	assert.Equal(t, 1, countActions(cs, "get", "secrets"))
}

// A refused read leaves the container out (the route fails closed too), and
// the refusal is read once.
func TestExecContainers_ForbiddenLeavesOut(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, resource string }{
		{"service account", "serviceaccounts"},
		{"secret", "secrets"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := execPod("a", map[string]string{releaseLabelInstance: "web"}, "app", "worker")
			p.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "s"}}}}
			if tt.resource == "serviceaccounts" {
				p.Spec.AutomountServiceAccountToken = nil
			}
			cs := fake.NewSimpleClientset(p)
			cs.PrependReactor("get", tt.resource, func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, k8serrors.NewForbidden(schema.GroupResource{Resource: tt.resource}, "x", errors.New("no rights"))
			})
			got, _, err := NewClientFromInterface(cs).ExecContainers(context.Background(), "ns", []string{"web"},
				ExecContainersRequest{AllowedContainer: allowList("app", "worker")})
			require.NoError(t, err)
			if tt.resource == "serviceaccounts" {
				assert.Empty(t, got) // both containers need the ServiceAccount
				assert.Equal(t, 1, countActions(cs, "get", "serviceaccounts"))
				return
			}
			// Only "app" reads the Secret; "worker" has none and is listed.
			assert.Equal(t, ReleaseExecContainers{Containers: []string{"worker"}}, got["web"])
		})
	}
}

// secretPod is a pod with one container per secret name; each container reads
// its own Secret (the pod does not automount, so the Secret is the only read).
func envSecretPod(name, release string, secrets ...string) (*corev1.Pod, []string) {
	var names []string
	p := execPod(name, map[string]string{releaseLabelInstance: release})
	for i, sec := range secrets {
		ctr := fmt.Sprintf("c%02d", i)
		names = append(names, ctr)
		p.Spec.Containers = append(p.Spec.Containers, corev1.Container{
			Name:    ctr,
			EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: sec}}}},
		})
	}
	return p, names
}

func TestExecContainers_ReadBudget(t *testing.T) {
	t.Parallel()
	any := func(string) bool { return true }
	distinct := func(n int) []string {
		var out []string
		for i := 0; i < n; i++ {
			out = append(out, fmt.Sprintf("sec%03d", i))
		}
		return out
	}

	t.Run("budget cut leaves containers out and is reported", func(t *testing.T) {
		t.Parallel()
		// Missing Secrets (NotFound) count as reads and are not tokens.
		p, names := envSecretPod("a", "web", distinct(maxExecHintTokenReads+5)...)
		cs := fake.NewSimpleClientset(p)
		got, stats, err := NewClientFromInterface(cs).ExecContainers(context.Background(), "ns", []string{"web"}, ExecContainersRequest{AllowedContainer: any})
		require.NoError(t, err)
		assert.Equal(t, names[:maxExecHintTokenReads], got["web"].Containers)
		assert.Equal(t, ExecHintStats{Reads: maxExecHintTokenReads, BudgetCut: true}, stats)
		assert.Equal(t, maxExecHintTokenReads, countActions(cs, "get", "secrets"))
	})

	t.Run("exactly the budget is not a cut", func(t *testing.T) {
		t.Parallel()
		p, names := envSecretPod("a", "web", distinct(maxExecHintTokenReads)...)
		got, stats, err := NewClientFromInterface(fake.NewSimpleClientset(p)).ExecContainers(context.Background(), "ns", []string{"web"}, ExecContainersRequest{AllowedContainer: any})
		require.NoError(t, err)
		assert.Equal(t, names, got["web"].Containers)
		assert.Equal(t, ExecHintStats{Reads: maxExecHintTokenReads}, stats)
	})

	t.Run("memo hits cost nothing", func(t *testing.T) {
		t.Parallel()
		// Many containers share one Secret: one read, no cut.
		shared := make([]string, maxExecHintTokenReads*3)
		for i := range shared {
			shared[i] = "same"
		}
		p, names := envSecretPod("a", "web", shared...)
		cs := fake.NewSimpleClientset(p, secretObj("same", corev1.SecretTypeOpaque))
		got, stats, err := NewClientFromInterface(cs).ExecContainers(context.Background(), "ns", []string{"web"}, ExecContainersRequest{AllowedContainer: any})
		require.NoError(t, err)
		assert.Equal(t, names, got["web"].Containers)
		assert.Equal(t, ExecHintStats{Reads: 1}, stats)
		assert.Equal(t, 1, countActions(cs, "get", "secrets"))
	})

	t.Run("a spent budget does not starve a later chart that hits the memo", func(t *testing.T) {
		t.Parallel()
		first, _ := envSecretPod("a", "web", distinct(maxExecHintTokenReads+3)...)
		// "api" reads the Secret that "web" read first (memo hit) and one that
		// is beyond the budget (cut).
		second, _ := envSecretPod("b", "api", "sec000", fmt.Sprintf("sec%03d", maxExecHintTokenReads+2))
		cs := fake.NewSimpleClientset(first, second)
		got, stats, err := NewClientFromInterface(cs).ExecContainers(context.Background(), "ns", []string{"web", "api"}, ExecContainersRequest{AllowedContainer: any})
		require.NoError(t, err)
		assert.Equal(t, []string{"c00"}, got["api"].Containers)
		assert.True(t, stats.BudgetCut)
		assert.Equal(t, maxExecHintTokenReads, stats.Reads)
	})

	t.Run("no token check, no budget", func(t *testing.T) {
		t.Parallel()
		p, names := envSecretPod("a", "web", distinct(maxExecHintTokenReads+5)...)
		cs := fake.NewSimpleClientset(p)
		got, stats, err := NewClientFromInterface(cs).ExecContainers(context.Background(), "ns", []string{"web"},
			ExecContainersRequest{AllowedContainer: any, AllowServiceAccountToken: true})
		require.NoError(t, err)
		assert.Equal(t, names, got["web"].Containers)
		assert.Equal(t, ExecHintStats{}, stats)
		assert.Zero(t, countActions(cs, "get", "secrets"))
	})
}

func TestExecContainers_ReportsForbidden(t *testing.T) {
	t.Parallel()
	p, _ := envSecretPod("a", "web", "s1", "s2")
	cs := fake.NewSimpleClientset(p)
	cs.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, k8serrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "x", errors.New("no rights"))
	})
	got, stats, err := NewClientFromInterface(cs).ExecContainers(context.Background(), "ns", []string{"web"},
		ExecContainersRequest{AllowedContainer: func(string) bool { return true }})
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.True(t, stats.Forbidden)
	assert.False(t, stats.BudgetCut)
	assert.Equal(t, 2, stats.Reads)
}

func TestExecContainers_CancelledContext(t *testing.T) {
	t.Parallel()
	p := execPod("a", map[string]string{releaseLabelInstance: "web"}, "app")
	p.Spec.AutomountServiceAccountToken = nil
	cs := fake.NewSimpleClientset(p)
	ctx, cancel := context.WithCancel(context.Background())
	cs.PrependReactor("get", "serviceaccounts", func(k8stesting.Action) (bool, runtime.Object, error) {
		cancel()
		return true, nil, context.Canceled
	})
	_, _, err := NewClientFromInterface(cs).ExecContainers(ctx, "ns", []string{"web"}, ExecContainersRequest{AllowedContainer: allowList("app")})
	require.ErrorIs(t, err, context.Canceled)
}

// The hint and the route must agree: every listed container is accepted by
// ResolveExecTarget, the default is what it picks without a name, and an
// allowed container that is not listed (or no default) is refused.
func TestExecContainers_AgreesWithResolveExecTarget(t *testing.T) {
	t.Parallel()
	inst := map[string]string{releaseLabelInstance: "web"}
	tokenVol := []corev1.Volume{{Name: "tok", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
		Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "token"}}},
	}}}}
	a := execPod("a", inst, "side")
	b := execPod("b", inst, "app", "worker", "web")
	b.Spec.Volumes = tokenVol
	b.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "tok", MountPath: "/tok"}}
	cs := fake.NewSimpleClientset(a, b)
	c := NewClientFromInterface(cs)
	allow := allowList("app", "worker", "web", "absent")

	got, _, err := c.ExecContainers(context.Background(), "ns", []string{"web"}, ExecContainersRequest{AllowedContainer: allow})
	require.NoError(t, err)
	listed := map[string]bool{}
	for _, n := range got["web"].Containers {
		listed[n] = true
	}
	for _, n := range []string{"app", "worker", "web", "absent", "side"} {
		tgt, err := c.ResolveExecTarget(context.Background(), "ns", ExecTargetRequest{Release: "web", Container: n, AllowedContainer: allow})
		assert.Equal(t, listed[n], err == nil, "container %s", n)
		if err == nil {
			assert.NotNil(t, tgt)
		}
	}
	tgt, err := c.ResolveExecTarget(context.Background(), "ns", ExecTargetRequest{Release: "web", AllowedContainer: allow})
	if got["web"].Default == "" {
		require.Error(t, err)
	} else {
		require.NoError(t, err)
		assert.Equal(t, got["web"].Default, tgt.Container)
	}
	// "app" (token) is the default and is refused; the others are listed.
	assert.Equal(t, ReleaseExecContainers{Containers: []string{"worker", "web"}}, got["web"])
}

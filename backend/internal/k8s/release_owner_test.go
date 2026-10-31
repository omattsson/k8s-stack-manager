package k8s

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

// helmAnnotations are the annotations that Helm sets on an object of
// release in namespace ns.
func helmAnnotations(release, ns string) map[string]string {
	return map[string]string{helmReleaseNameAnnotation: release, helmReleaseNamespaceAnnotation: ns}
}

// annotatedDeployment is a Deployment that only the Helm annotations tie to
// release (like the podinfo chart: no app.kubernetes.io/instance label).
func annotatedDeployment(name, release, ns string) *appsv1.Deployment {
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: ns,
		Labels:      map[string]string{"app.kubernetes.io/name": name},
		Annotations: helmAnnotations(release, ns),
	}}
}

// replicaSetPod makes p a pod of the ReplicaSet "<deployment>-<hash>".
func replicaSetPod(p *corev1.Pod, deployment, hash string) *corev1.Pod {
	isController := true
	if p.Labels == nil {
		p.Labels = map[string]string{}
	}
	p.Labels[podTemplateHashLabel] = hash
	p.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: "apps/v1", Kind: "ReplicaSet", Name: deployment + "-" + hash, Controller: &isController,
	}}
	return p
}

func TestObjectRelease(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		lbls map[string]string
		ann  map[string]string
		want string
	}{
		{name: "instance label", lbls: map[string]string{releaseLabelInstance: "web"}, ann: helmAnnotations("other", "ns"), want: "web"},
		{name: "helm.sh/release label", lbls: map[string]string{releaseLabelHelm: "web"}, want: "web"},
		{name: "Helm annotations", ann: helmAnnotations("web", "ns"), want: "web"},
		{name: "release name without namespace annotation", ann: map[string]string{helmReleaseNameAnnotation: "web"}, want: "web"},
		{name: "release of another namespace", ann: helmAnnotations("web", "other-ns")},
		{name: "nothing"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			obj := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Labels: tt.lbls, Annotations: tt.ann}}
			assert.Equal(t, tt.want, objectRelease("ns", obj))
		})
	}
}

func TestPodRelease(t *testing.T) {
	t.Parallel()
	byDeployment := map[string]string{"podinfo": "podinfo", "api": "backend"}
	isController := true
	tests := []struct {
		name string
		pod  *corev1.Pod
		want string
	}{
		{name: "release label wins", pod: replicaSetPod(execPod("a", map[string]string{releaseLabelInstance: "web"}), "api", "abc"), want: "web"},
		{name: "pod of a Deployment", pod: replicaSetPod(execPod("podinfo-5b55565bf-x", nil), "podinfo", "5b55565bf"), want: "podinfo"},
		{name: "Deployment name differs from the release", pod: replicaSetPod(execPod("api-1-x", nil), "api", "1"), want: "backend"},
		{name: "unknown Deployment", pod: replicaSetPod(execPod("other-1-x", nil), "other", "1")},
		{name: "no controller", pod: execPod("bare", nil)},
		{name: "ReplicaSet name without the hash", pod: func() *corev1.Pod {
			p := replicaSetPod(execPod("p", nil), "podinfo", "5b55565bf")
			p.Labels[podTemplateHashLabel] = "other"
			return p
		}()},
		{name: "StatefulSet pod", pod: func() *corev1.Pod {
			p := execPod("db-0", nil)
			p.OwnerReferences = []metav1.OwnerReference{{Kind: "StatefulSet", Name: "podinfo", Controller: &isController}}
			return p
		}()},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, podRelease(tt.pod, byDeployment))
		})
	}
}

// The podinfo chart labels its Deployment, Service and pods only with
// app.kubernetes.io/name: the Helm annotations and the pod's ReplicaSet put
// them into the chart, not into "_unmanaged".
func TestGetNamespaceStatus_HelmAnnotationsAndOwner(t *testing.T) {
	t.Parallel()
	ns := "stack-hello"
	pod := replicaSetPod(execPod("podinfo-5b55565bf-x", map[string]string{"app.kubernetes.io/name": "podinfo"}, "podinfo"), "podinfo", "5b55565bf")
	pod.Namespace = ns
	orphan := execPod("orphan", nil, "app")
	orphan.Namespace = ns
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "podinfo", Namespace: ns, Annotations: helmAnnotations("podinfo", ns)}}
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
		annotatedDeployment("podinfo", "podinfo", ns), pod, orphan, svc,
	)

	status, err := NewClientFromInterface(cs).GetNamespaceStatus(context.Background(), ns, StatusOptions{})
	require.NoError(t, err)
	byRelease := map[string]ChartStatus{}
	for _, c := range status.Charts {
		byRelease[c.ReleaseName] = c
	}
	require.Contains(t, byRelease, "podinfo")
	assert.Len(t, byRelease["podinfo"].Deployments, 1)
	require.Len(t, byRelease["podinfo"].Pods, 1)
	assert.Equal(t, "podinfo-5b55565bf-x", byRelease["podinfo"].Pods[0].Name)
	assert.Len(t, byRelease["podinfo"].Services, 1)
	require.Contains(t, byRelease, "_unmanaged")
	require.Len(t, byRelease["_unmanaged"].Pods, 1)
	assert.Equal(t, "orphan", byRelease["_unmanaged"].Pods[0].Name)
}

func TestResolveExecTarget_OwnerFallback(t *testing.T) {
	t.Parallel()
	objs := []runtime.Object{
		annotatedDeployment("podinfo", "podinfo", "ns"),
		replicaSetPod(execPod("podinfo-5b55565bf-x", nil, "podinfo"), "podinfo", "5b55565bf"),
		execPod("web-1", map[string]string{releaseLabelInstance: "web"}, "app"),
	}

	t.Run("a release without labelled pods uses its Deployment's pods", func(t *testing.T) {
		t.Parallel()
		cs := fake.NewSimpleClientset(objs...)
		target, err := NewClientFromInterface(cs).ResolveExecTarget(context.Background(), "ns",
			ExecTargetRequest{Release: "podinfo", AllowedContainer: allowList("podinfo")})
		require.NoError(t, err)
		assert.Equal(t, &ExecTarget{Pod: "podinfo-5b55565bf-x", Container: "podinfo"}, target)
	})
	t.Run("a release with labelled pods lists no Deployments", func(t *testing.T) {
		t.Parallel()
		cs := fake.NewSimpleClientset(objs...)
		_, err := NewClientFromInterface(cs).ResolveExecTarget(context.Background(), "ns",
			ExecTargetRequest{Release: "web", AllowedContainer: allowList("app")})
		require.NoError(t, err)
		assert.Equal(t, 0, countActions(cs, "list", "deployments"))
	})
	t.Run("an unknown release stays without a pod", func(t *testing.T) {
		t.Parallel()
		cs := fake.NewSimpleClientset(objs...)
		_, err := NewClientFromInterface(cs).ResolveExecTarget(context.Background(), "ns",
			ExecTargetRequest{Release: "nope", AllowedContainer: allowList("app", "podinfo")})
		assert.ErrorIs(t, err, ErrNoReadyPod)
	})
}

func TestExecContainers_OwnerFallback(t *testing.T) {
	t.Parallel()
	objs := []runtime.Object{
		annotatedDeployment("podinfo", "podinfo", "ns"),
		replicaSetPod(execPod("podinfo-5b55565bf-x", nil, "podinfo"), "podinfo", "5b55565bf"),
		execPod("web-1", map[string]string{releaseLabelInstance: "web"}, "app"),
	}
	cs := fake.NewSimpleClientset(objs...)
	client := NewClientFromInterface(cs)
	allowed := ExecContainersRequest{AllowedContainer: allowList("app", "podinfo")}

	got, _, err := client.ExecContainers(context.Background(), "ns", []string{"podinfo", "web"}, allowed)
	require.NoError(t, err)
	assert.Equal(t, []string{"podinfo"}, got["podinfo"].Containers)
	assert.Equal(t, "podinfo", got["podinfo"].Default)
	assert.Equal(t, []string{"app"}, got["web"].Containers)
	assert.Equal(t, 1, countActions(cs, "list", "deployments"))

	// The hint agrees with the exec route.
	target, err := client.ResolveExecTarget(context.Background(), "ns", ExecTargetRequest{Release: "podinfo", AllowedContainer: allowList("app", "podinfo")})
	require.NoError(t, err)
	assert.Equal(t, got["podinfo"].Default, target.Container)

	// Every release has labelled pods: no Deployment list.
	cs2 := fake.NewSimpleClientset(objs...)
	_, _, err = NewClientFromInterface(cs2).ExecContainers(context.Background(), "ns", []string{"web"}, allowed)
	require.NoError(t, err)
	assert.Equal(t, 0, countActions(cs2, "list", "deployments"))
}

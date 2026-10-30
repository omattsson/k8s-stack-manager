package k8s

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func readyPod(name string, lbls map[string]string, ports ...corev1.ContainerPort) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", Labels: lbls},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Ports: ports}}},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
}

func svc(mut func(*corev1.Service)) *corev1.Service {
	s := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "ns"},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "web"},
			Ports:    []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt32(8080)}},
		},
	}
	if mut != nil {
		mut(s)
	}
	return s
}

func TestResolveServiceTarget(t *testing.T) {
	t.Parallel()
	app := map[string]string{"app": "web"}
	terminating := readyPod("a-terminating", app)
	now := metav1.Now()
	terminating.DeletionTimestamp = &now
	notReady := readyPod("a-notready", app)
	notReady.Status.Conditions[0].Status = corev1.ConditionFalse
	pending := readyPod("a-pending", app)
	pending.Status.Phase = corev1.PodPending

	tests := []struct {
		name       string
		objs       []runtime.Object
		service    string
		port       int32
		wantPod    string
		wantTarget int32
		wantErr    error
	}{
		{name: "numeric target port", objs: []runtime.Object{svc(nil), readyPod("p1", app)}, service: "web", port: 80, wantPod: "p1", wantTarget: 8080},
		{
			name: "named target port from the pod",
			objs: []runtime.Object{
				svc(func(s *corev1.Service) { s.Spec.Ports[0].TargetPort = intstr.FromString("http") }),
				readyPod("p1", app, corev1.ContainerPort{Name: "http", ContainerPort: 9090}),
			},
			service: "web", port: 80, wantPod: "p1", wantTarget: 9090,
		},
		{
			name: "named target port missing in the pod",
			objs: []runtime.Object{
				svc(func(s *corev1.Service) { s.Spec.Ports[0].TargetPort = intstr.FromString("http") }),
				readyPod("p1", app, corev1.ContainerPort{Name: "other", ContainerPort: 9090}),
			},
			service: "web", port: 80, wantErr: ErrPortNotFound,
		},
		{
			name:    "empty target port is the service port",
			objs:    []runtime.Object{svc(func(s *corev1.Service) { s.Spec.Ports[0].TargetPort = intstr.IntOrString{} }), readyPod("p1", app)},
			service: "web", port: 80, wantPod: "p1", wantTarget: 80,
		},
		{name: "first ready pod by name", objs: []runtime.Object{svc(nil), readyPod("p2", app), readyPod("p1", app), terminating, notReady, pending}, service: "web", port: 80, wantPod: "p1", wantTarget: 8080},
		{name: "service not found", objs: []runtime.Object{readyPod("p1", app)}, service: "web", port: 80, wantErr: ErrServiceNotFound},
		{name: "unknown port", objs: []runtime.Object{svc(nil), readyPod("p1", app)}, service: "web", port: 81, wantErr: ErrPortNotFound},
		{
			name:    "udp port is not forwardable",
			objs:    []runtime.Object{svc(func(s *corev1.Service) { s.Spec.Ports[0].Protocol = corev1.ProtocolUDP }), readyPod("p1", app)},
			service: "web", port: 80, wantErr: ErrPortNotFound,
		},
		{name: "external name", objs: []runtime.Object{svc(func(s *corev1.Service) { s.Spec.Type = corev1.ServiceTypeExternalName })}, service: "web", port: 80, wantErr: ErrServiceExternalName},
		{name: "no selector", objs: []runtime.Object{svc(func(s *corev1.Service) { s.Spec.Selector = nil })}, service: "web", port: 80, wantErr: ErrServiceNoSelector},
		{name: "no pod", objs: []runtime.Object{svc(nil)}, service: "web", port: 80, wantErr: ErrNoReadyPod},
		{name: "only terminating or not ready pods", objs: []runtime.Object{svc(nil), terminating, notReady, pending}, service: "web", port: 80, wantErr: ErrNoReadyPod},
		{name: "pod of another selector", objs: []runtime.Object{svc(nil), readyPod("p1", map[string]string{"app": "other"})}, service: "web", port: 80, wantErr: ErrNoReadyPod},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := NewClientFromInterface(fake.NewSimpleClientset(tt.objs...))
			got, err := c.ResolveServiceTarget(context.Background(), "ns", tt.service, tt.port)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, &ServiceTarget{Service: tt.service, Pod: tt.wantPod, ServicePort: tt.port, TargetPort: tt.wantTarget}, got)
		})
	}
}

func TestCheckStreamNamespace(t *testing.T) {
	t.Parallel()
	ns := func(name string, lbls map[string]string, terminating bool) *corev1.Namespace {
		n := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: lbls}}
		if terminating {
			now := metav1.Now()
			n.DeletionTimestamp = &now
		}
		return n
	}
	managed := map[string]string{ManagedByLabelKey: ManagedByLabelValue}
	tests := []struct {
		name      string
		objs      []runtime.Object
		namespace string
		backendNS string
		wantErr   error
	}{
		{name: "managed", objs: []runtime.Object{ns("stack-a", managed, false)}, namespace: "stack-a", backendNS: "manager"},
		{name: "managed, backend outside a pod", objs: []runtime.Object{ns("stack-a", managed, false)}, namespace: "stack-a"},
		{name: "missing label", objs: []runtime.Object{ns("kube-system", nil, false)}, namespace: "kube-system", wantErr: ErrNamespaceNotAllowed},
		{name: "wrong label value", objs: []runtime.Object{ns("x", map[string]string{ManagedByLabelKey: "helm"}, false)}, namespace: "x", wantErr: ErrNamespaceNotAllowed},
		{name: "backend namespace even when labelled", objs: []runtime.Object{ns("manager", managed, false)}, namespace: "manager", backendNS: "manager", wantErr: ErrNamespaceNotAllowed},
		{name: "empty namespace", namespace: "", wantErr: ErrNamespaceNotAllowed},
		{name: "not found", namespace: "gone", wantErr: ErrNamespaceNotFound},
		{name: "terminating", objs: []runtime.Object{ns("stack-a", managed, true)}, namespace: "stack-a", wantErr: ErrNamespaceNotFound},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := NewClientFromInterface(fake.NewSimpleClientset(tt.objs...))
			err := c.CheckStreamNamespace(context.Background(), tt.namespace, tt.backendNS)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestSelectReadyPod(t *testing.T) {
	t.Parallel()
	now := metav1.Now()
	ready := func(name string) corev1.Pod { return *readyPod(name, nil) }
	term := ready("a")
	term.DeletionTimestamp = &now
	noCond := ready("b")
	noCond.Status.Conditions = nil
	succeeded := ready("c")
	succeeded.Status.Phase = corev1.PodSucceeded

	tests := []struct {
		name string
		pods []corev1.Pod
		want string
	}{
		{"empty", nil, ""},
		{"none usable", []corev1.Pod{term, noCond, succeeded}, ""},
		{"first by name", []corev1.Pod{ready("z"), ready("m"), term, ready("n")}, "m"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := SelectReadyPod(tt.pods)
			if tt.want == "" {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tt.want, got.Name)
		})
	}
	assert.False(t, PodReady(nil))
}

func TestDialPodPort(t *testing.T) {
	t.Parallel()

	t.Run("no rest config", func(t *testing.T) {
		t.Parallel()
		c := NewClientFromInterface(fake.NewSimpleClientset())
		_, err := c.DialPodPort(context.Background(), "ns", "p", 80)
		assert.ErrorIs(t, err, ErrRESTConfigUnavailable)
	})

	t.Run("api server refuses the upgrade", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "nope", http.StatusForbidden)
		}))
		t.Cleanup(srv.Close)
		cfg := &rest.Config{Host: srv.URL}
		cs, err := kubernetes.NewForConfig(cfg)
		require.NoError(t, err)
		c := &Client{clientset: cs, restConfig: cfg}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err = c.DialPodPort(ctx, "ns", "p", 80)
		assert.ErrorIs(t, err, ErrDialFailed)
	})

	t.Run("cancelled context", func(t *testing.T) {
		t.Parallel()
		cfg := &rest.Config{Host: "http://127.0.0.1:1"}
		cs, err := kubernetes.NewForConfig(cfg)
		require.NoError(t, err)
		c := &Client{clientset: cs, restConfig: cfg}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = c.DialPodPort(ctx, "ns", "p", 80)
		assert.ErrorIs(t, err, context.Canceled)
	})
}

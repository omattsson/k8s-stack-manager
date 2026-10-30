package k8s

import (
	"context"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"
)

func boolPtr(b bool) *bool { return &b }

// execPod builds a ready pod of the release with the given containers. The pod
// does not automount the service-account token unless a test changes it.
func execPod(name string, lbls map[string]string, containers ...string) *corev1.Pod {
	p := readyPod(name, lbls)
	p.Spec.Containers = nil
	for _, c := range containers {
		p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: c})
	}
	p.Spec.AutomountServiceAccountToken = boolPtr(false)
	return p
}

func allowList(names ...string) func(string) bool {
	return func(n string) bool {
		for _, a := range names {
			if a == n {
				return true
			}
		}
		return false
	}
}

func TestResolveExecTarget(t *testing.T) {
	t.Parallel()
	inst := map[string]string{releaseLabelInstance: "web"}
	helm := map[string]string{releaseLabelHelm: "web"}
	other := map[string]string{releaseLabelInstance: "db"}

	terminating := execPod("a-term", inst, "app")
	now := metav1.Now()
	terminating.DeletionTimestamp = &now
	notReady := execPod("a-notready", inst, "app")
	notReady.Status.Conditions[0].Status = corev1.ConditionFalse

	withToken := execPod("p", inst, "app")
	withToken.Spec.AutomountServiceAccountToken = nil
	projected := execPod("p", inst, "app")
	projected.Spec.Volumes = []corev1.Volume{{Name: "tok", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
		Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "token"}}},
	}}}}
	projected.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "tok", MountPath: "/tok"}}
	projectedOther := execPod("p", inst, "app", "side")
	projectedOther.Spec.Volumes = projected.Spec.Volumes
	projectedOther.Spec.Containers[1].VolumeMounts = []corev1.VolumeMount{{Name: "tok", MountPath: "/tok"}}
	saNamed := execPod("p", inst, "app")
	saNamed.Spec.AutomountServiceAccountToken = nil
	saNamed.Spec.ServiceAccountName = "robot"

	sa := func(name string, automount *bool) *corev1.ServiceAccount {
		return &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"}, AutomountServiceAccountToken: automount}
	}

	tests := []struct {
		name     string
		objs     []runtime.Object
		req      ExecTargetRequest
		saErr    error
		wantPod  string
		wantCtr  string
		wantErr  error
		anyError bool
	}{
		{name: "instance label, container omitted", objs: []runtime.Object{execPod("b", inst, "app")},
			req: ExecTargetRequest{Release: "web", AllowedContainer: allowList("app")}, wantPod: "b", wantCtr: "app"},
		{name: "helm label", objs: []runtime.Object{execPod("b", helm, "app")},
			req: ExecTargetRequest{Release: "web", AllowedContainer: allowList("app")}, wantPod: "b", wantCtr: "app"},
		{name: "both labels on one pod are one pod", objs: []runtime.Object{execPod("b", map[string]string{releaseLabelHelm: "web", releaseLabelInstance: "web"}, "app")},
			req: ExecTargetRequest{Release: "web", AllowedContainer: allowList("app")}, wantPod: "b", wantCtr: "app"},
		{name: "first pod by name", objs: []runtime.Object{execPod("z", inst, "app"), execPod("a", helm, "app")},
			req: ExecTargetRequest{Release: "web", AllowedContainer: allowList("app")}, wantPod: "a", wantCtr: "app"},
		{name: "container given", objs: []runtime.Object{execPod("b", inst, "app", "worker")},
			req: ExecTargetRequest{Release: "web", Container: "worker", AllowedContainer: allowList("app", "worker")}, wantPod: "b", wantCtr: "worker"},
		{name: "omitted takes the first allowed in spec order", objs: []runtime.Object{execPod("b", inst, "sidecar", "worker", "app")},
			req: ExecTargetRequest{Release: "web", AllowedContainer: allowList("app", "worker")}, wantPod: "b", wantCtr: "worker"},
		{name: "omitted skips a pod without an allowed container", objs: []runtime.Object{execPod("a", inst, "sidecar"), execPod("b", inst, "app")},
			req: ExecTargetRequest{Release: "web", AllowedContainer: allowList("app")}, wantPod: "b", wantCtr: "app"},
		{name: "given runs only on the later pod", objs: []runtime.Object{execPod("a", inst, "app"), execPod("b", inst, "app", "worker")},
			req: ExecTargetRequest{Release: "web", Container: "worker", AllowedContainer: allowList("worker")}, wantPod: "b", wantCtr: "worker"},
		{name: "no pod", req: ExecTargetRequest{Release: "web", AllowedContainer: allowList("app")}, wantErr: ErrNoReadyPod},
		{name: "pod of another release", objs: []runtime.Object{execPod("b", other, "app")},
			req: ExecTargetRequest{Release: "web", AllowedContainer: allowList("app")}, wantErr: ErrNoReadyPod},
		{name: "terminating and not ready pods", objs: []runtime.Object{terminating, notReady},
			req: ExecTargetRequest{Release: "web", AllowedContainer: allowList("app")}, wantErr: ErrNoReadyPod},
		{name: "container not found", objs: []runtime.Object{execPod("b", inst, "app")},
			req: ExecTargetRequest{Release: "web", Container: "worker", AllowedContainer: allowList("app", "worker")}, wantErr: ErrContainerNotFound},
		{name: "container given but not allowed", objs: []runtime.Object{execPod("b", inst, "app")},
			req: ExecTargetRequest{Release: "web", Container: "app", AllowedContainer: allowList("worker")}, wantErr: ErrContainerNotAllowed},
		{name: "omitted, none allowed", objs: []runtime.Object{execPod("b", inst, "app")},
			req: ExecTargetRequest{Release: "web", AllowedContainer: allowList("worker")}, wantErr: ErrContainerNotAllowed},
		{name: "nil allow func allows nothing", objs: []runtime.Object{execPod("b", inst, "app")},
			req: ExecTargetRequest{Release: "web"}, wantErr: ErrContainerNotAllowed},
		{name: "pod automount true", objs: []runtime.Object{func() *corev1.Pod { p := execPod("b", inst, "app"); p.Spec.AutomountServiceAccountToken = boolPtr(true); return p }()},
			req: ExecTargetRequest{Release: "web", AllowedContainer: allowList("app")}, wantErr: ErrServiceAccountToken},
		{name: "pod field unset, service account missing counts as true", objs: []runtime.Object{withToken},
			req: ExecTargetRequest{Release: "web", AllowedContainer: allowList("app")}, wantErr: ErrServiceAccountToken},
		{name: "pod field unset, default service account automount unset", objs: []runtime.Object{withToken, sa("default", nil)},
			req: ExecTargetRequest{Release: "web", AllowedContainer: allowList("app")}, wantErr: ErrServiceAccountToken},
		{name: "pod field unset, default service account off", objs: []runtime.Object{withToken, sa("default", boolPtr(false))},
			req: ExecTargetRequest{Release: "web", AllowedContainer: allowList("app")}, wantPod: "p", wantCtr: "app"},
		{name: "named service account off", objs: []runtime.Object{saNamed, sa("robot", boolPtr(false)), sa("default", boolPtr(true))},
			req: ExecTargetRequest{Release: "web", AllowedContainer: allowList("app")}, wantPod: "p", wantCtr: "app"},
		{name: "named service account on", objs: []runtime.Object{saNamed, sa("robot", boolPtr(true)), sa("default", boolPtr(false))},
			req: ExecTargetRequest{Release: "web", AllowedContainer: allowList("app")}, wantErr: ErrServiceAccountToken},
		{name: "service account read error fails closed", objs: []runtime.Object{withToken}, saErr: errors.New("boom"),
			req: ExecTargetRequest{Release: "web", AllowedContainer: allowList("app")}, anyError: true},
		{name: "projected token mounted in the container", objs: []runtime.Object{projected},
			req: ExecTargetRequest{Release: "web", AllowedContainer: allowList("app")}, wantErr: ErrServiceAccountToken},
		{name: "projected token mounted in another container only", objs: []runtime.Object{projectedOther},
			req: ExecTargetRequest{Release: "web", Container: "app", AllowedContainer: allowList("app", "side")}, wantPod: "p", wantCtr: "app"},
		{name: "allow switch skips the token rule", objs: []runtime.Object{projected},
			req: ExecTargetRequest{Release: "web", AllowedContainer: allowList("app"), AllowServiceAccountToken: true}, wantPod: "p", wantCtr: "app"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cs := fake.NewSimpleClientset(tt.objs...)
			if tt.saErr != nil {
				cs.PrependReactor("get", "serviceaccounts", func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, tt.saErr
				})
			}
			c := NewClientFromInterface(cs)
			got, err := c.ResolveExecTarget(context.Background(), "ns", tt.req)
			switch {
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)
			case tt.anyError:
				require.Error(t, err)
				assert.NotErrorIs(t, err, ErrServiceAccountToken)
			default:
				require.NoError(t, err)
				assert.Equal(t, &ExecTarget{Pod: tt.wantPod, Container: tt.wantCtr}, got)
			}
		})
	}
}

func TestResolveExecTarget_ListError(t *testing.T) {
	t.Parallel()
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("down")
	})
	_, err := NewClientFromInterface(cs).ResolveExecTarget(context.Background(), "ns", ExecTargetRequest{Release: "web"})
	require.Error(t, err)
}

type fakeExecutor struct{ err error }

func (f fakeExecutor) Stream(opts remotecommand.StreamOptions) error {
	return f.StreamWithContext(context.Background(), opts)
}

func (f fakeExecutor) StreamWithContext(_ context.Context, opts remotecommand.StreamOptions) error {
	_, _ = io.WriteString(opts.Stdout, "out")
	_, _ = io.WriteString(opts.Stderr, "err")
	return f.err
}

func TestStreamExec_ExitCodes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		err      error
		wantCode int
		wantErr  bool
	}{
		{name: "zero", wantCode: 0},
		{name: "nonzero", err: utilexec.CodeExitError{Err: errors.New("command terminated"), Code: 3}, wantCode: 3},
		{name: "wrapped nonzero", err: errors.Join(errors.New("x"), utilexec.CodeExitError{Err: errors.New("c"), Code: 7}), wantCode: 7},
		{name: "stream failure", err: errors.New("stream broke"), wantCode: -1, wantErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var so, se strings.Builder
			code, err := streamExec(context.Background(), fakeExecutor{err: tt.err}, &so, &se)
			assert.Equal(t, tt.wantCode, code)
			assert.Equal(t, tt.wantErr, err != nil)
			assert.Equal(t, "out", so.String())
			assert.Equal(t, "err", se.String())
		})
	}
}

func TestExecCommand_RequiresRESTConfig(t *testing.T) {
	t.Parallel()
	c := NewClientFromInterface(fake.NewSimpleClientset())
	_, err := c.ExecCommand(context.Background(), "ns", ExecTarget{Pod: "p", Container: "c"}, []string{"ls"}, io.Discard, io.Discard)
	assert.ErrorIs(t, err, ErrRESTConfigUnavailable)
}

// The exec transports must verify the API server certificate like every other
// client-go call: a kubeconfig without a CA checks the system roots, so an
// untrusted server never sees the credentials, and the CA of the kubeconfig
// applies. Both the WebSocket executor and the SPDY executor are checked, and
// the fallback executor that ExecCommand uses.
func TestNewExecExecutor_TLS(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		withCA    bool
		wantReach bool
	}{
		{name: "no CA checks the system roots", withCA: false, wantReach: false},
		{name: "kubeconfig CA is used", withCA: true, wantReach: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reached := make(chan string, 8)
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case reached <- r.Header.Get("Authorization"):
				default:
				}
				w.WriteHeader(http.StatusBadRequest) // never upgrades
			}))
			srv.Config.ErrorLog = log.New(io.Discard, "", 0)
			srv.StartTLS()
			t.Cleanup(srv.Close)

			cfg := &rest.Config{Host: srv.URL, BearerToken: "secret-token"}
			if tt.withCA {
				cfg.TLSClientConfig.CAData = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
			}
			u, err := url.Parse(srv.URL + "/api/v1/namespaces/ns/pods/p/exec?command=ls&stdout=true")
			require.NoError(t, err)

			spdyExec, err := remotecommand.NewSPDYExecutor(cfg, "POST", u)
			require.NoError(t, err)
			wsExec, err := remotecommand.NewWebSocketExecutor(cfg, "GET", u.String())
			require.NoError(t, err)
			fallback, err := newExecExecutor(cfg, u)
			require.NoError(t, err)

			for name, ex := range map[string]remotecommand.Executor{"spdy": spdyExec, "websocket": wsExec, "fallback": fallback} {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				err := ex.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: io.Discard})
				cancel()
				require.Error(t, err, name)
				if !tt.wantReach {
					assert.Contains(t, err.Error(), "x509", name)
				} else {
					assert.NotContains(t, err.Error(), "x509", name)
				}
			}
			if tt.wantReach {
				require.NotEmpty(t, reached)
				assert.Equal(t, "Bearer secret-token", <-reached)
			} else {
				assert.Empty(t, reached, "the untrusted server must not see the request")
			}
		})
	}
}

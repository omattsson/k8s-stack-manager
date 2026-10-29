package k8s

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// Errors of the stream layer. The handler maps each one to a response code
// (see the port-forward contract): ErrNamespaceNotFound 409
// instance_not_deployed, ErrNamespaceNotAllowed 403 namespace_not_allowed,
// ErrServiceNotFound 404 service_not_found, ErrPortNotFound 404
// port_not_found, ErrServiceExternalName 409 service_external_name,
// ErrServiceNoSelector 409 service_no_selector, ErrNoReadyPod 503
// no_ready_pod, ErrDialFailed 502 dial_failed, ErrRESTConfigUnavailable 500.
var (
	ErrNamespaceNotFound   = errors.New("namespace not found")
	ErrNamespaceNotAllowed = errors.New("namespace is not a stack namespace")
	ErrServiceNotFound     = errors.New("service not found")
	ErrPortNotFound        = errors.New("service has no such port")
	ErrServiceExternalName = errors.New("service is of type ExternalName")
	ErrServiceNoSelector   = errors.New("service has no selector")
	ErrNoReadyPod          = errors.New("no ready pod for the service")
	ErrDialFailed          = errors.New("dial to the pod failed")
)

// ServiceTarget is the pod and port that a Service port resolves to.
type ServiceTarget struct {
	Service     string
	Pod         string
	ServicePort int32
	// TargetPort is the container port on the pod.
	TargetPort int32
}

// PodPortStream is one open port-forward stream pair to a pod port. It
// replaces the local listener of kubectl port-forward: the caller copies
// bytes between its own connection and Data.
type PodPortStream interface {
	// Data is the data stream. Read returns the bytes of the pod, Write sends
	// bytes to it. Closing it half-closes the direction to the pod.
	Data() io.ReadWriteCloser
	// Errors is the error stream of the pod. It yields the error text of the
	// kubelet and ends (EOF) when the forward ends.
	Errors() io.Reader
	// Close resets both streams and closes the connection to the API server.
	// It is safe to call more than once.
	Close() error
}

// PortForwarder is what a stream handler needs from the cluster client.
// *Client implements it; tests inject a fake.
type PortForwarder interface {
	// CheckStreamNamespace refuses a namespace that is not a stack namespace.
	CheckStreamNamespace(ctx context.Context, namespace, backendNamespace string) error
	// ResolveServiceTarget picks the pod and target port of a Service port.
	ResolveServiceTarget(ctx context.Context, namespace, service string, port int32) (*ServiceTarget, error)
	// DialPodPort opens the stream pair to a port of a pod.
	DialPodPort(ctx context.Context, namespace, pod string, port int32) (PodPortStream, error)
}

var _ PortForwarder = (*Client)(nil)

// CheckStreamNamespace is the namespace guard of the stream routes. The
// namespace must carry managed-by=k8s-stack-manager and must not be the
// namespace of the backend (backendNamespace, empty outside a pod).
// ErrNamespaceNotFound means the namespace does not exist or is terminating.
func (c *Client) CheckStreamNamespace(ctx context.Context, namespace, backendNamespace string) (err error) {
	ctx, span := startSpan(ctx, "k8s.CheckStreamNamespace", attribute.String("k8s.namespace", namespace))
	defer func() { endSpan(span, err) }()

	if namespace == "" || (backendNamespace != "" && namespace == backendNamespace) {
		return ErrNamespaceNotAllowed
	}
	ns, err := c.clientset.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return ErrNamespaceNotFound
		}
		return fmt.Errorf("get namespace %s: %w", namespace, err)
	}
	if ns.DeletionTimestamp != nil {
		return ErrNamespaceNotFound
	}
	if ns.Labels[ManagedByLabelKey] != ManagedByLabelValue {
		return ErrNamespaceNotAllowed
	}
	return nil
}

// ResolveServiceTarget resolves a Service port to a pod, as kubectl
// port-forward svc/NAME does. It refuses ExternalName Services and Services
// without a selector. The pod is the first Running and Ready pod by name
// (SelectReadyPod). A numeric targetPort is used as is, a named targetPort is
// looked up in the containers of the chosen pod, an empty targetPort is the
// Service port.
func (c *Client) ResolveServiceTarget(ctx context.Context, namespace, service string, port int32) (t *ServiceTarget, err error) {
	ctx, span := startSpan(ctx, "k8s.ResolveServiceTarget",
		attribute.String("k8s.namespace", namespace), attribute.String("k8s.service", service))
	defer func() { endSpan(span, err) }()

	svc, err := c.clientset.CoreV1().Services(namespace).Get(ctx, service, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, ErrServiceNotFound
		}
		return nil, fmt.Errorf("get service %s/%s: %w", namespace, service, err)
	}
	if svc.Spec.Type == corev1.ServiceTypeExternalName {
		return nil, ErrServiceExternalName
	}
	if len(svc.Spec.Selector) == 0 {
		return nil, ErrServiceNoSelector
	}

	var sp *corev1.ServicePort
	for i := range svc.Spec.Ports {
		p := &svc.Spec.Ports[i]
		if p.Port == port && (p.Protocol == "" || p.Protocol == corev1.ProtocolTCP) {
			sp = p
			break
		}
	}
	if sp == nil {
		return nil, ErrPortNotFound
	}

	pods, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(svc.Spec.Selector).String(),
	})
	if err != nil {
		return nil, fmt.Errorf("list pods for service %s/%s: %w", namespace, service, err)
	}
	pod := SelectReadyPod(pods.Items)
	if pod == nil {
		return nil, ErrNoReadyPod
	}

	target, err := resolveTargetPort(sp, pod)
	if err != nil {
		return nil, err
	}
	span.SetAttributes(attribute.String("k8s.pod", pod.Name))
	return &ServiceTarget{Service: service, Pod: pod.Name, ServicePort: port, TargetPort: target}, nil
}

func resolveTargetPort(sp *corev1.ServicePort, pod *corev1.Pod) (int32, error) {
	switch {
	case sp.TargetPort.Type == intstr.Int && sp.TargetPort.IntVal > 0:
		return sp.TargetPort.IntVal, nil
	case sp.TargetPort.Type == intstr.String && sp.TargetPort.StrVal != "":
		name := sp.TargetPort.StrVal
		// Like kubectl: the regular containers, and an init container only
		// when it is a native sidecar (restartPolicy Always, it keeps
		// running). A classic init container has ended by then.
		for _, ctr := range targetPortContainers(pod) {
			for _, cp := range ctr.Ports {
				if cp.Name == name && (cp.Protocol == "" || cp.Protocol == corev1.ProtocolTCP) {
					return cp.ContainerPort, nil
				}
			}
		}
		return 0, fmt.Errorf("%w: named target port %q is not a container port of pod %s", ErrPortNotFound, name, pod.Name)
	default:
		return sp.Port, nil
	}
}

// targetPortContainers returns the containers whose ports a named targetPort
// can name: the regular containers and the native sidecars.
func targetPortContainers(pod *corev1.Pod) []corev1.Container {
	out := append([]corev1.Container(nil), pod.Spec.Containers...)
	for _, ctr := range pod.Spec.InitContainers {
		if ctr.RestartPolicy != nil && *ctr.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			out = append(out, ctr)
		}
	}
	return out
}

// podPortStream is the PodPortStream over a SPDY (or tunneled) connection.
type podPortStream struct {
	conn httpstream.Connection
	data httpstream.Stream
	errs httpstream.Stream
	once sync.Once
	err  error
}

func (s *podPortStream) Data() io.ReadWriteCloser { return s.data }
func (s *podPortStream) Errors() io.Reader        { return s.errs }

func (s *podPortStream) Close() error {
	s.once.Do(func() {
		_ = s.data.Reset()
		_ = s.errs.Reset()
		s.err = s.conn.Close()
	})
	return s.err
}

package k8s

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"
)

// Errors of the exec target resolution. The handler maps each one to a response
// code (see the exec contract): ErrNoReadyPod 503 no_ready_pod,
// ErrContainerNotFound 404 container_not_found, ErrContainerNotAllowed 403
// container_not_allowed, ErrServiceAccountToken 403 service_account_token.
var (
	ErrContainerNotFound   = errors.New("no ready pod runs the container")
	ErrContainerNotAllowed = errors.New("container is not allowed for exec")
	ErrServiceAccountToken = errors.New("container holds a service-account token")
)

// Label keys that name the Helm release of a pod.
const (
	releaseLabelInstance = "app.kubernetes.io/instance"
	releaseLabelHelm     = "helm.sh/release"
)

// ExecTargetRequest are the inputs of ResolveExecTarget.
type ExecTargetRequest struct {
	// Release is the Helm release name (the chart name of the instance).
	Release string
	// Container is the container the caller asked for. Empty: the first
	// allowed container of the first pod that has one.
	Container string
	// AllowedContainer reports whether a container name may be used for exec.
	// Required: nil allows nothing.
	AllowedContainer func(name string) bool
	// AllowServiceAccountToken turns off the service-account-token refusal.
	AllowServiceAccountToken bool
}

// ExecTarget is the pod and container that a command runs in.
type ExecTarget struct {
	Pod       string
	Container string
}

// ExecClient is what the exec handler needs from the cluster client.
// *Client implements it; tests inject a fake.
type ExecClient interface {
	// CheckStreamNamespace refuses a namespace that is not a stack namespace.
	CheckStreamNamespace(ctx context.Context, namespace, backendNamespace string) error
	// ResolveExecTarget picks the pod and the container of a command.
	ResolveExecTarget(ctx context.Context, namespace string, req ExecTargetRequest) (*ExecTarget, error)
	// ExecCommand runs the command without stdin and without a TTY and copies
	// the output to stdout and stderr. It returns the exit code of the process.
	// A nil error means the process ended (the exit code may be non-zero); any
	// other error is a failure of the stream.
	ExecCommand(ctx context.Context, namespace string, target ExecTarget, command []string, stdout, stderr io.Writer) (int, error)
}

var _ ExecClient = (*Client)(nil)

// ResolveExecTarget finds the pod and the container for a command. The pod
// comes only from a list in namespace: the pods that carry the release in the
// label app.kubernetes.io/instance or helm.sh/release, Running, Ready and not
// terminating, sorted by name. Nothing of the request names a pod.
func (c *Client) ResolveExecTarget(ctx context.Context, namespace string, req ExecTargetRequest) (t *ExecTarget, err error) {
	ctx, span := startSpan(ctx, "k8s.ResolveExecTarget", attribute.String("k8s.namespace", namespace))
	defer func() { endSpan(span, err) }()

	pods, err := c.releasePods(ctx, namespace, req.Release)
	if err != nil {
		return nil, err
	}
	var ready []corev1.Pod
	for i := range pods {
		if PodReady(&pods[i]) {
			ready = append(ready, pods[i])
		}
	}
	if len(ready) == 0 {
		return nil, ErrNoReadyPod
	}
	SortPodsByName(ready)

	allowed := req.AllowedContainer
	if allowed == nil {
		allowed = func(string) bool { return false }
	}
	var pod *corev1.Pod
	var ctr *corev1.Container
	for i := range ready {
		p := &ready[i]
		for j := range p.Spec.Containers {
			cc := &p.Spec.Containers[j]
			if req.Container != "" && cc.Name != req.Container {
				continue
			}
			if allowed(cc.Name) {
				pod, ctr = p, cc
				break
			}
		}
		if pod != nil {
			break
		}
	}
	if pod == nil {
		if req.Container == "" {
			return nil, ErrContainerNotAllowed
		}
		for i := range ready {
			for _, cc := range ready[i].Spec.Containers {
				if cc.Name == req.Container {
					return nil, ErrContainerNotAllowed // runs, but is not in the list
				}
			}
		}
		return nil, ErrContainerNotFound
	}

	if !req.AllowServiceAccountToken {
		holds, err := c.holdsServiceAccountToken(ctx, namespace, pod, ctr)
		if err != nil {
			return nil, err
		}
		if holds {
			return nil, ErrServiceAccountToken
		}
	}
	span.SetAttributes(attribute.String("k8s.pod", pod.Name), attribute.String("k8s.container", ctr.Name))
	return &ExecTarget{Pod: pod.Name, Container: ctr.Name}, nil
}

// releasePods lists the pods of a Helm release by both label keys and merges
// the results by name.
func (c *Client) releasePods(ctx context.Context, namespace, release string) ([]corev1.Pod, error) {
	seen := map[string]bool{}
	var out []corev1.Pod
	for _, key := range []string{releaseLabelInstance, releaseLabelHelm} {
		list, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: labels.SelectorFromSet(labels.Set{key: release}).String(),
		})
		if err != nil {
			return nil, fmt.Errorf("list pods of release %s in %s: %w", release, namespace, err)
		}
		for _, p := range list.Items {
			if !seen[p.Name] {
				seen[p.Name] = true
				out = append(out, p)
			}
		}
	}
	return out, nil
}

// holdsServiceAccountToken reports whether a command in the container can read
// a service-account token: the effective automount is not false (the pod field,
// else the field of the ServiceAccount, else true), the container mounts a
// volume with a projected serviceAccountToken source, or it reads a Secret of
// the legacy type kubernetes.io/service-account-token. A failed read of the
// ServiceAccount fails closed (the error is returned); a missing ServiceAccount
// counts as automount true.
func (c *Client) holdsServiceAccountToken(ctx context.Context, namespace string, pod *corev1.Pod, ctr *corev1.Container) (bool, error) {
	automount := true
	switch {
	case pod.Spec.AutomountServiceAccountToken != nil:
		automount = *pod.Spec.AutomountServiceAccountToken
	default:
		name := pod.Spec.ServiceAccountName
		if name == "" {
			name = pod.Spec.DeprecatedServiceAccount
		}
		if name == "" {
			name = "default"
		}
		sa, err := c.clientset.CoreV1().ServiceAccounts(namespace).Get(ctx, name, metav1.GetOptions{})
		switch {
		case err == nil:
			if sa.AutomountServiceAccountToken != nil {
				automount = *sa.AutomountServiceAccountToken
			}
		case k8serrors.IsNotFound(err):
			// automount stays true
		default:
			return false, fmt.Errorf("get service account %s/%s: %w", namespace, name, err)
		}
	}
	if automount {
		return true, nil
	}
	mounted := map[string]bool{}
	for _, vm := range ctr.VolumeMounts {
		mounted[vm.Name] = true
	}
	// Secrets that the container reads: mounted secret volumes (also as a
	// projected source), env valueFrom.secretKeyRef and envFrom.secretRef.
	var secretNames []string
	seen := map[string]bool{}
	addSecret := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			secretNames = append(secretNames, name)
		}
	}
	for _, v := range pod.Spec.Volumes {
		if !mounted[v.Name] {
			continue
		}
		if v.Secret != nil {
			addSecret(v.Secret.SecretName)
		}
		if v.Projected == nil {
			continue
		}
		for _, src := range v.Projected.Sources {
			if src.ServiceAccountToken != nil {
				return true, nil
			}
			if src.Secret != nil {
				addSecret(src.Secret.Name)
			}
		}
	}
	for _, e := range ctr.Env {
		if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
			addSecret(e.ValueFrom.SecretKeyRef.Name)
		}
	}
	for _, ef := range ctr.EnvFrom {
		if ef.SecretRef != nil {
			addSecret(ef.SecretRef.Name)
		}
	}
	// A legacy service-account token is a Secret of the type
	// kubernetes.io/service-account-token. One read for each distinct name; a
	// missing Secret is not a token, any other error fails closed.
	for _, name := range secretNames {
		sec, err := c.clientset.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
		switch {
		case err == nil:
			if sec.Type == corev1.SecretTypeServiceAccountToken {
				return true, nil
			}
		case k8serrors.IsNotFound(err):
		default:
			return false, fmt.Errorf("get secret %s/%s: %w", namespace, name, err)
		}
	}
	return false, nil
}

// ExecCommand runs command in the container of the pod (no stdin, no TTY) and
// copies the output to stdout and stderr. See ExecClient.
func (c *Client) ExecCommand(ctx context.Context, namespace string, target ExecTarget, command []string, stdout, stderr io.Writer) (code int, err error) {
	ctx, span := startSpan(ctx, "k8s.ExecCommand",
		attribute.String("k8s.namespace", namespace), attribute.String("k8s.pod", target.Pod),
		attribute.String("k8s.container", target.Container))
	defer func() {
		span.SetAttributes(attribute.Int("exec.exit_code", code))
		// The error is sanitized below: the raw error of the stream holds the
		// exec URL with every argument of the command.
		endSpan(span, err)
	}()

	if c.restConfig == nil {
		return -1, ErrRESTConfigUnavailable
	}
	u := c.clientset.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(namespace).Name(target.Pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: target.Container, Command: command, Stdout: true, Stderr: true,
		}, scheme.ParameterCodec).URL()
	exec, err := execExecutorFactory(rest.CopyConfig(c.restConfig), u)
	if err != nil {
		return -1, SanitizeExecError(err)
	}
	code, err = streamExec(ctx, exec, stdout, stderr)
	return code, SanitizeExecError(err)
}

// execExecutorFactory builds the executor of ExecCommand. A variable so that
// tests can inject a fake.
var execExecutorFactory = newExecExecutor

// ExecStreamError is a failure of the exec stream whose text is safe to show:
// it holds no URL (the exec URL carries every argument of the command). Message
// goes to the client; Detail (also URL-free) is for the server log.
type ExecStreamError struct {
	Message string
	Detail  string
	// Forbidden is true when the API server refused the exec (RBAC or an
	// admission policy).
	Forbidden bool
}

func (e *ExecStreamError) Error() string { return e.Message }

// ErrExecRefused is the client text for a refused exec.
const execRefusedMessage = "The cluster refused the exec"

var urlPattern = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://\S+`)

// SanitizeExecError turns an error of the exec stream into an *ExecStreamError
// without any URL: a *url.Error is replaced by its cause, and any URL left in
// the text becomes "<url>". A Forbidden answer gets a fixed client text. nil
// stays nil; a context error stays a context error.
func SanitizeExecError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, ErrRESTConfigUnavailable) {
		return err
	}
	cur := err
	for {
		var ue *url.Error
		if !errors.As(cur, &ue) {
			break
		}
		cur = ue.Err
		if cur == nil {
			cur = errors.New("request failed")
			break
		}
	}
	detail := urlPattern.ReplaceAllString(cur.Error(), "<url>")
	if len(detail) > 500 {
		detail = detail[:500]
	}
	if k8serrors.IsForbidden(cur) || strings.Contains(strings.ToLower(detail), "forbidden") {
		return &ExecStreamError{Message: execRefusedMessage, Detail: detail, Forbidden: true}
	}
	return &ExecStreamError{Message: detail, Detail: detail}
}

// streamExec runs the executor and maps the end of the process to an exit code.
func streamExec(ctx context.Context, exec remotecommand.Executor, stdout, stderr io.Writer) (int, error) {
	err := exec.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: stdout, Stderr: stderr})
	if err == nil {
		return 0, nil
	}
	var ee utilexec.ExitError
	if errors.As(err, &ee) && ee.Exited() {
		return ee.ExitStatus(), nil
	}
	return -1, err
}

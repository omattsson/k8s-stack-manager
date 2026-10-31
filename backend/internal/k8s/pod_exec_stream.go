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
	// ErrTokenCheckForbidden: the backend may not read the ServiceAccount or a
	// Secret of the pod, so the service-account-token check cannot run.
	ErrTokenCheckForbidden = errors.New("the service-account-token check may not read the pod's service account or secrets")
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
	// ExecContainers lists, for each release, the containers that
	// ResolveExecTarget would accept for the release. See ExecContainersRequest.
	ExecContainers(ctx context.Context, namespace string, releases []string, req ExecContainersRequest) (map[string]ReleaseExecContainers, ExecHintStats, error)
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
	ready := readySortedPods(pods)
	if len(ready) == 0 {
		return nil, ErrNoReadyPod
	}

	pod, ctr := pickExecContainer(ready, req.Container, req.AllowedContainer)
	if pod == nil {
		return nil, execPickError(ready, req.Container)
	}

	if !req.AllowServiceAccountToken {
		holds, err := c.holdsServiceAccountToken(ctx, namespace, pod, ctr, nil)
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

// readySortedPods keeps the pods that are Running, Ready and not terminating,
// sorted by name. The exec route and the exec hints share it.
func readySortedPods(pods []corev1.Pod) []corev1.Pod {
	var ready []corev1.Pod
	for i := range pods {
		if PodReady(&pods[i]) {
			ready = append(ready, pods[i])
		}
	}
	SortPodsByName(ready)
	return ready
}

// pickExecContainer chooses the pod and the container of a command from the
// ready pods (sorted by name): the first allowed container, in spec order, of
// the first pod that has one; with a requested name only a container of that
// name counts. It returns nil, nil when there is none. A nil allowed func
// allows nothing.
func pickExecContainer(ready []corev1.Pod, requested string, allowed func(string) bool) (*corev1.Pod, *corev1.Container) {
	if allowed == nil {
		allowed = func(string) bool { return false }
	}
	for i := range ready {
		p := &ready[i]
		for j := range p.Spec.Containers {
			cc := &p.Spec.Containers[j]
			if requested != "" && cc.Name != requested {
				continue
			}
			if allowed(cc.Name) {
				return p, cc
			}
		}
	}
	return nil, nil
}

// execPickError is the error when pickExecContainer found nothing: the
// container runs but is not allowed, or no ready pod runs it.
func execPickError(ready []corev1.Pod, requested string) error {
	if requested == "" {
		return ErrContainerNotAllowed
	}
	for i := range ready {
		for _, cc := range ready[i].Spec.Containers {
			if cc.Name == requested {
				return ErrContainerNotAllowed // runs, but is not in the list
			}
		}
	}
	return ErrContainerNotFound
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
// the legacy type kubernetes.io/service-account-token. With a shared process
// namespace a command also reads the files and the environment of every other
// running container (/proc/<pid>/root, /proc/<pid>/environ), so then every
// running container of the pod is checked. A failed read of the ServiceAccount
// or of a Secret fails closed (the error is returned; a Forbidden read wraps
// ErrTokenCheckForbidden); a missing ServiceAccount counts as automount true.
//
// memo (may be nil) keeps the reads of one batch of checks: each ServiceAccount
// and each Secret is read at most once.
func (c *Client) holdsServiceAccountToken(ctx context.Context, namespace string, pod *corev1.Pod, ctr *corev1.Container, memo *tokenMemo) (bool, error) {
	if memo == nil {
		memo = newTokenMemo()
	}
	automount, err := c.effectiveAutomount(ctx, namespace, pod, memo)
	if err != nil || automount {
		return automount, err
	}
	readers := []tokenReader{{ctr.VolumeMounts, ctr.Env, ctr.EnvFrom}}
	if pod.Spec.ShareProcessNamespace != nil && *pod.Spec.ShareProcessNamespace {
		readers = runningContainerReaders(pod)
	}
	secretNames, projectedToken := referencedSecrets(pod, readers)
	if projectedToken {
		return true, nil
	}
	// A legacy service-account token is a Secret of the type
	// kubernetes.io/service-account-token. One read for each distinct name; a
	// missing Secret is not a token, any other error fails closed.
	for _, name := range secretNames {
		isToken, err := c.secretIsToken(ctx, namespace, name, memo)
		if err != nil {
			return false, err
		}
		if isToken {
			return true, nil
		}
	}
	return false, nil
}

// secretIsToken reports whether the Secret has the type
// kubernetes.io/service-account-token. A missing Secret is not a token; any
// other error is returned. The answer is kept in memo.
func (c *Client) secretIsToken(ctx context.Context, namespace, name string, memo *tokenMemo) (bool, error) {
	if r, ok := memo.secrets[name]; ok {
		return r.value, r.err
	}
	if !memo.takeRead() {
		return false, errTokenReadBudget
	}
	var r memoResult
	sec, err := c.clientset.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	switch {
	case err == nil:
		r.value = sec.Type == corev1.SecretTypeServiceAccountToken
	case k8serrors.IsNotFound(err):
	default:
		r.err = tokenCheckError("secret", namespace, name, err)
	}
	memo.secrets[name] = r
	return r.value, r.err
}

// memoResult is one kept answer of a token check read (an error is kept too,
// so a refused read is not repeated).
type memoResult struct {
	value bool
	err   error
}

// tokenMemo keeps the ServiceAccount and Secret reads of one batch of token
// checks, by name. It is not safe for concurrent use.
type tokenMemo struct {
	serviceAccounts map[string]memoResult
	secrets         map[string]memoResult
	// reads counts the real Kubernetes reads (memo misses; a NotFound counts).
	// With maxReads > 0 a further read is refused with errTokenReadBudget.
	reads    int
	maxReads int
}

// errTokenReadBudget is returned by a token check that would exceed the read
// budget of the memo. It is not kept in the memo and it is not Forbidden.
var errTokenReadBudget = errors.New("the read budget of the service-account-token check is spent")

// takeRead counts one real read; it reports false when the budget is spent.
func (m *tokenMemo) takeRead() bool {
	if m.maxReads > 0 && m.reads >= m.maxReads {
		return false
	}
	m.reads++
	return true
}

func newTokenMemo() *tokenMemo {
	return &tokenMemo{serviceAccounts: map[string]memoResult{}, secrets: map[string]memoResult{}}
}

// effectiveAutomount is the automountServiceAccountToken of the pod: the pod
// field, else the field of its ServiceAccount, else true.
func (c *Client) effectiveAutomount(ctx context.Context, namespace string, pod *corev1.Pod, memo *tokenMemo) (bool, error) {
	if pod.Spec.AutomountServiceAccountToken != nil {
		return *pod.Spec.AutomountServiceAccountToken, nil
	}
	name := pod.Spec.ServiceAccountName
	if name == "" {
		name = pod.Spec.DeprecatedServiceAccount
	}
	if name == "" {
		name = "default"
	}
	if r, ok := memo.serviceAccounts[name]; ok {
		return r.value, r.err
	}
	if !memo.takeRead() {
		return false, errTokenReadBudget
	}
	r := memoResult{value: true} // a missing ServiceAccount counts as automount true
	sa, err := c.clientset.CoreV1().ServiceAccounts(namespace).Get(ctx, name, metav1.GetOptions{})
	switch {
	case err == nil:
		if sa.AutomountServiceAccountToken != nil {
			r.value = *sa.AutomountServiceAccountToken
		}
	case k8serrors.IsNotFound(err):
	default:
		r = memoResult{err: tokenCheckError("service account", namespace, name, err)}
	}
	memo.serviceAccounts[name] = r
	return r.value, r.err
}

// tokenCheckError wraps a failed read of the token check. A Forbidden read
// also wraps ErrTokenCheckForbidden: the identity of the backend lacks get on
// serviceaccounts or secrets in the namespace.
func tokenCheckError(kind, namespace, name string, err error) error {
	if k8serrors.IsForbidden(err) {
		return fmt.Errorf("get %s %s/%s: %w: %w", kind, namespace, name, ErrTokenCheckForbidden, err)
	}
	return fmt.Errorf("get %s %s/%s: %w", kind, namespace, name, err)
}

// tokenReader is what a container reads a token through: its volume mounts and
// its environment.
type tokenReader struct {
	mounts  []corev1.VolumeMount
	env     []corev1.EnvVar
	envFrom []corev1.EnvFromSource
}

// runningContainerReaders lists the containers that can run next to the
// selected one: the regular containers, the sidecars (init containers with
// restartPolicy Always) and the ephemeral containers. Other init containers
// have ended before the pod is ready.
func runningContainerReaders(pod *corev1.Pod) []tokenReader {
	var out []tokenReader
	for i := range pod.Spec.Containers {
		cc := &pod.Spec.Containers[i]
		out = append(out, tokenReader{cc.VolumeMounts, cc.Env, cc.EnvFrom})
	}
	for i := range pod.Spec.InitContainers {
		cc := &pod.Spec.InitContainers[i]
		if cc.RestartPolicy != nil && *cc.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			out = append(out, tokenReader{cc.VolumeMounts, cc.Env, cc.EnvFrom})
		}
	}
	for i := range pod.Spec.EphemeralContainers {
		cc := &pod.Spec.EphemeralContainers[i].EphemeralContainerCommon
		out = append(out, tokenReader{cc.VolumeMounts, cc.Env, cc.EnvFrom})
	}
	return out
}

// referencedSecrets returns the distinct Secrets that the readers read (mounted
// secret volumes, also as a projected source, env valueFrom.secretKeyRef and
// envFrom.secretRef), and whether one of them mounts a projected
// serviceAccountToken source.
func referencedSecrets(pod *corev1.Pod, readers []tokenReader) ([]string, bool) {
	mounted := map[string]bool{}
	var names []string
	seen := map[string]bool{}
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	for _, r := range readers {
		for _, vm := range r.mounts {
			mounted[vm.Name] = true
		}
		for _, e := range r.env {
			if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
				add(e.ValueFrom.SecretKeyRef.Name)
			}
		}
		for _, ef := range r.envFrom {
			if ef.SecretRef != nil {
				add(ef.SecretRef.Name)
			}
		}
	}
	for _, v := range pod.Spec.Volumes {
		if !mounted[v.Name] {
			continue
		}
		if v.Secret != nil {
			add(v.Secret.SecretName)
		}
		if v.Projected == nil {
			continue
		}
		for _, src := range v.Projected.Sources {
			if src.ServiceAccountToken != nil {
				return nil, true
			}
			if src.Secret != nil {
				add(src.Secret.Name)
			}
		}
	}
	return names, false
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

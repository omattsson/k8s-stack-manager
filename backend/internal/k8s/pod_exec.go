package k8s

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

// ErrRESTConfigUnavailable is returned by pod-exec helpers when the Client
// was constructed without a *rest.Config (e.g. from a fake clientset in tests).
var ErrRESTConfigUnavailable = errors.New("REST config unavailable: pod exec requires a real cluster connection")

// ExecInDeploymentPod runs `command` inside the first Ready pod owned by the
// Deployment identified by namespace + deploymentName, returning the combined
// stdout/stderr. The deployment's pod template labels are used as the pod
// selector so this works with any Deployment regardless of release layout.
//
// Returns ErrRESTConfigUnavailable when the Client has no rest.Config
// (typically in unit tests that use fake.NewSimpleClientset).
func (c *Client) ExecInDeploymentPod(
	ctx context.Context,
	namespace, deploymentName string,
	container string,
	command []string,
) (string, error) {
	if c.restConfig == nil {
		return "", ErrRESTConfigUnavailable
	}

	deploy, err := c.clientset.AppsV1().Deployments(namespace).Get(ctx, deploymentName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get deployment %s/%s: %w", namespace, deploymentName, err)
	}
	selector := metav1.FormatLabelSelector(deploy.Spec.Selector)

	pods, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return "", fmt.Errorf("list pods for %s/%s: %w", namespace, deploymentName, err)
	}

	var targetPod *corev1.Pod
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, cond := range p.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				targetPod = p
				break
			}
		}
		if targetPod != nil {
			break
		}
	}
	if targetPod == nil {
		return "", fmt.Errorf("no ready pod found for deployment %s/%s", namespace, deploymentName)
	}

	// Fall back to the first container when the caller did not request a
	// specific one. Redis charts typically have a single container so this
	// is the common path.
	if container == "" && len(targetPod.Spec.Containers) > 0 {
		container = targetPod.Spec.Containers[0].Name
	}

	req := c.clientset.CoreV1().RESTClient().
		Post().
		Resource("pods").
		Name(targetPod.Name).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   command,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(c.restConfig, "POST", req.URL())
	if err != nil {
		return "", fmt.Errorf("build SPDY exec for %s/%s: %w", namespace, targetPod.Name, err)
	}

	var stdout, stderr bytes.Buffer
	if err := exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	}); err != nil {
		combined := combineBuffers(stdout.String(), stderr.String())
		return combined, fmt.Errorf("exec in %s/%s: %w", namespace, targetPod.Name, err)
	}

	combined := combineBuffers(stdout.String(), stderr.String())
	slog.Debug("exec completed",
		"namespace", namespace, "pod", targetPod.Name, "container", container,
		"stdout_len", stdout.Len(), "stderr_len", stderr.Len())
	return combined, nil
}

func combineBuffers(stdout, stderr string) string {
	switch {
	case stdout != "" && stderr != "":
		return stdout + "\n" + stderr
	case stderr != "":
		return stderr
	default:
		return stdout
	}
}

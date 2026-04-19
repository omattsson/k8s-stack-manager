package k8s

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

// ErrDeploymentNotFound is returned by scale/wait helpers when the target
// Deployment does not exist in the namespace. Callers that want "skip if
// missing" semantics should check with errors.Is against this sentinel.
var ErrDeploymentNotFound = errors.New("deployment not found")

// defaultPollInterval is used by the wait helpers when polling the API server.
// Kept short enough that a step advance is near-real-time without burning
// API quota on fast transitions.
const defaultPollInterval = 2 * time.Second

// deploymentPodSelector returns the LabelSelector string for a Deployment's
// own pods, rejecting a nil / all-empty selector. A Deployment with no
// selector is a server-side-apply edge case (or a malformed object) — treating
// the empty selector as "match anything in the namespace" would let scale /
// exec helpers act on unrelated workloads, so we fail loudly instead.
func deploymentPodSelector(deploy *appsv1.Deployment) (string, error) {
	sel := deploy.Spec.Selector
	if sel == nil || (len(sel.MatchLabels) == 0 && len(sel.MatchExpressions) == 0) {
		return "", fmt.Errorf(
			"deployment %s/%s has no pod selector (spec.selector is empty); refusing to list pods",
			deploy.Namespace, deploy.Name,
		)
	}
	return metav1.FormatLabelSelector(sel), nil
}

// ScaleDeployment sets the target Deployment's replicas to the provided value.
// Returns ErrDeploymentNotFound (wrapped) when the Deployment is missing so
// callers can choose to skip silently (e.g. optional app deployments that
// aren't part of every stack).
//
// We read then write the Deployment directly rather than using the /scale
// subresource because fake clientsets in unit tests don't implement the
// Scale shim; both forms produce the same effect on a real API server for
// the use cases we care about (no HPA co-ordination needed here).
func (c *Client) ScaleDeployment(ctx context.Context, namespace, name string, replicas int32) error {
	deploy, err := c.clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return fmt.Errorf("%w: %s/%s", ErrDeploymentNotFound, namespace, name)
		}
		return fmt.Errorf("get deployment %s/%s: %w", namespace, name, err)
	}

	current := int32(0)
	if deploy.Spec.Replicas != nil {
		current = *deploy.Spec.Replicas
	}
	if current == replicas {
		slog.Debug("deployment already at target replicas",
			"namespace", namespace, "deployment", name, "replicas", replicas)
		return nil
	}

	r := replicas
	deploy.Spec.Replicas = &r
	if _, err := c.clientset.AppsV1().Deployments(namespace).Update(ctx, deploy, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update replicas for %s/%s to %d: %w", namespace, name, replicas, err)
	}

	slog.Info("scaled deployment",
		"namespace", namespace, "deployment", name, "replicas", replicas)
	return nil
}

// WaitForDeploymentPodsGone polls the namespace until no pods owned by the
// Deployment's selector remain, or the provided timeout elapses. Returns nil
// on success (including when the Deployment doesn't exist), or a wrapped
// error on timeout.
func (c *Client) WaitForDeploymentPodsGone(ctx context.Context, namespace, name string, timeout time.Duration) error {
	deploy, err := c.clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			// No Deployment means no pods to wait for.
			return nil
		}
		return fmt.Errorf("get deployment %s/%s: %w", namespace, name, err)
	}

	selector, err := deploymentPodSelector(deploy)
	if err != nil {
		return err
	}

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	err = wait.PollUntilContextCancel(waitCtx, defaultPollInterval, true, func(pollCtx context.Context) (bool, error) {
		pods, err := c.clientset.CoreV1().Pods(namespace).List(pollCtx, metav1.ListOptions{
			LabelSelector: selector,
		})
		if err != nil {
			return false, fmt.Errorf("list pods for %s/%s: %w", namespace, name, err)
		}
		return len(pods.Items) == 0, nil
	})
	if err != nil {
		return fmt.Errorf("waiting for pods of %s/%s to terminate: %w", namespace, name, err)
	}
	return nil
}

// WaitForDeploymentAvailable polls the Deployment until its Available
// condition is True and observed generation matches, or the timeout elapses.
func (c *Client) WaitForDeploymentAvailable(ctx context.Context, namespace, name string, timeout time.Duration) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	err := wait.PollUntilContextCancel(waitCtx, defaultPollInterval, true, func(pollCtx context.Context) (bool, error) {
		deploy, err := c.clientset.AppsV1().Deployments(namespace).Get(pollCtx, name, metav1.GetOptions{})
		if err != nil {
			if k8serrors.IsNotFound(err) {
				// Not yet observed; keep polling.
				return false, nil
			}
			return false, fmt.Errorf("get deployment %s/%s: %w", namespace, name, err)
		}

		// Ensure the controller has observed the latest spec (so we don't
		// accept a pre-scale-up "Available" from before we flipped replicas).
		if deploy.Status.ObservedGeneration < deploy.Generation {
			return false, nil
		}
		for _, cond := range deploy.Status.Conditions {
			if cond.Type == appsv1.DeploymentAvailable && cond.Status == corev1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("waiting for deployment %s/%s to become available: %w", namespace, name, err)
	}
	return nil
}

// PVCCleanupJobRequest bundles the inputs required to create and wait on a
// short-lived Job that wipes a single PVC's contents.
type PVCCleanupJobRequest struct {
	Namespace string
	JobName   string
	PVCName   string
	// Image runs `sh -c "rm -rf /var/lib/mysql/* /var/lib/mysql/.[!.]* 2>/dev/null || true; echo done"`
	// against the mounted PVC. Keep small (alpine) to minimise pull time.
	Image   string
	Timeout time.Duration
}

// RunPVCCleanupJob creates a short-lived Job that mounts the given PVC at
// /var/lib/mysql and removes its contents so the MySQL init container will
// re-extract the golden dataset on next boot. The Job is waited on up to
// req.Timeout, then deleted regardless of outcome.
func (c *Client) RunPVCCleanupJob(ctx context.Context, req PVCCleanupJobRequest) error {
	if req.Namespace == "" || req.JobName == "" || req.PVCName == "" || req.Image == "" {
		return fmt.Errorf("RunPVCCleanupJob: namespace, job name, PVC name and image are required")
	}
	if req.Timeout <= 0 {
		req.Timeout = 3 * time.Minute
	}

	// Best-effort pre-delete in case a previous invocation left stale state.
	_ = c.deleteJob(ctx, req.Namespace, req.JobName)

	backoffLimit := int32(0)
	ttl := int32(60) // Let K8s garbage-collect the Job 60s after completion as a safety net.
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.JobName,
			Namespace: req.Namespace,
			Labels: map[string]string{
				"managed-by":                 "k8s-stack-manager",
				"k8s-stack-manager/action":   "pvc-cleanup",
				"k8s-stack-manager/pvc-name": req.PVCName,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:    "cleanup",
						Image:   req.Image,
						Command: []string{"sh", "-c"},
						Args: []string{
							"rm -rf /var/lib/mysql/* /var/lib/mysql/.[!.]* 2>/dev/null || true; echo done",
						},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "data",
							MountPath: "/var/lib/mysql",
						}},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("50m"),
								corev1.ResourceMemory: resource.MustParse("64Mi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("200m"),
								corev1.ResourceMemory: resource.MustParse("128Mi"),
							},
						},
					}},
					Volumes: []corev1.Volume{{
						Name: "data",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: req.PVCName,
							},
						},
					}},
				},
			},
		},
	}

	if _, err := c.clientset.BatchV1().Jobs(req.Namespace).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create cleanup Job %s/%s: %w", req.Namespace, req.JobName, err)
	}
	slog.Info("created PVC cleanup Job",
		"namespace", req.Namespace, "job", req.JobName, "pvc", req.PVCName)

	// Always attempt to delete the Job on exit (defensive: TTL will also GC
	// it eventually, but we want the namespace tidy and the Job slot freed
	// promptly). Use a bounded context so cleanup cannot hang indefinitely
	// if the API server or network is unhealthy — a stuck delete would
	// otherwise block the calling goroutine and the shutdown WaitGroup.
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()

		if err := c.deleteJob(cleanupCtx, req.Namespace, req.JobName); err != nil {
			slog.Warn("failed to delete cleanup Job after run",
				"namespace", req.Namespace, "job", req.JobName, "error", err)
		}
	}()

	waitCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

	err := wait.PollUntilContextCancel(waitCtx, defaultPollInterval, true, func(pollCtx context.Context) (bool, error) {
		j, err := c.clientset.BatchV1().Jobs(req.Namespace).Get(pollCtx, req.JobName, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Errorf("get Job %s/%s: %w", req.Namespace, req.JobName, err)
		}
		for _, cond := range j.Status.Conditions {
			if cond.Type == batchv1.JobComplete && cond.Status == corev1.ConditionTrue {
				return true, nil
			}
			if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue {
				return false, fmt.Errorf("cleanup Job %s/%s failed: %s", req.Namespace, req.JobName, cond.Message)
			}
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("waiting for cleanup Job %s/%s: %w", req.Namespace, req.JobName, err)
	}
	return nil
}

// DeleteJob removes a Job by name, including the pods it owns, and waits for
// the Job to be fully gone from the API before returning. Returns nil when the
// Job is already absent so callers can use it safely after another process may
// have already cleaned up.
//
// Callers rely on the Job being absent (so a subsequent Helm hook can recreate
// it) — hence foreground propagation + a short poll loop, rather than the
// fire-and-forget background policy.
func (c *Client) DeleteJob(ctx context.Context, namespace, name string) error {
	return c.deleteJob(ctx, namespace, name)
}

// deleteJob deletes a Job with foreground propagation and blocks until the
// Job is NotFound (or ctx is cancelled / the short poll budget elapses).
// Foreground propagation causes the API to keep the Job object around until
// its owned pods are gone, so polling Get until NotFound gives us a reliable
// "the Job and its pods are fully removed" signal.
func (c *Client) deleteJob(ctx context.Context, namespace, name string) error {
	policy := metav1.DeletePropagationForeground
	err := c.clientset.BatchV1().Jobs(namespace).Delete(ctx, name, metav1.DeleteOptions{
		PropagationPolicy: &policy,
	})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("delete Job %s/%s: %w", namespace, name, err)
	}

	// Poll until the Job is gone. 30s is ample — foreground deletion of a
	// completed single-pod Job typically finishes in a few seconds.
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		_, getErr := c.clientset.BatchV1().Jobs(namespace).Get(waitCtx, name, metav1.GetOptions{})
		if k8serrors.IsNotFound(getErr) {
			slog.Debug("deleted Job", "namespace", namespace, "job", name)
			return nil
		}
		if getErr != nil && waitCtx.Err() == nil {
			return fmt.Errorf("poll deleted Job %s/%s: %w", namespace, name, getErr)
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("delete Job %s/%s: timed out waiting for removal: %w", namespace, name, waitCtx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

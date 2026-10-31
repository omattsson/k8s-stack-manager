package k8s

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Annotations that Helm (3.2 or later) sets on every object of a release.
// Unlike the labels app.kubernetes.io/instance and helm.sh/release, they do
// not depend on the chart. Helm does not set them on pods: the ReplicaSet
// controller creates those.
const (
	helmReleaseNameAnnotation      = "meta.helm.sh/release-name"
	helmReleaseNamespaceAnnotation = "meta.helm.sh/release-namespace"
	podTemplateHashLabel           = appsv1.DefaultDeploymentUniqueLabelKey
)

// labelRelease returns the release named by the release labels, or "".
func labelRelease(lbls map[string]string) string {
	for _, key := range helmReleaseLabels {
		if v, ok := lbls[key]; ok {
			return v
		}
	}
	return ""
}

// objectRelease returns the Helm release of an object of namespace: the
// release labels, else the Helm annotations (only when the release
// namespace annotation, if set, is namespace). "" when neither names one.
func objectRelease(namespace string, obj metav1.Object) string {
	if r := labelRelease(obj.GetLabels()); r != "" {
		return r
	}
	ann := obj.GetAnnotations()
	r := ann[helmReleaseNameAnnotation]
	if r == "" {
		return ""
	}
	if ns, ok := ann[helmReleaseNamespaceAnnotation]; ok && ns != namespace {
		return ""
	}
	return r
}

// deploymentReleases maps the name of each Deployment of namespace to its
// Helm release (objectRelease); Deployments without one are left out.
func deploymentReleases(namespace string, deployments []appsv1.Deployment) map[string]string {
	out := make(map[string]string, len(deployments))
	for i := range deployments {
		if r := objectRelease(namespace, &deployments[i]); r != "" {
			out[deployments[i].Name] = r
		}
	}
	return out
}

// podRelease returns the Helm release of a pod: the release labels, else
// the release of the Deployment that owns it. A Deployment pod's controller
// is a ReplicaSet named "<deployment>-<pod-template-hash>", so the
// Deployment name follows from the pod without a ReplicaSet read. byDeployment
// is the result of deploymentReleases. "" when the release is unknown.
func podRelease(p *corev1.Pod, byDeployment map[string]string) string {
	if r := labelRelease(p.Labels); r != "" {
		return r
	}
	ref := metav1.GetControllerOf(p)
	if ref == nil || ref.Kind != "ReplicaSet" {
		return ""
	}
	hash := p.Labels[podTemplateHashLabel]
	if hash == "" {
		return ""
	}
	name, ok := strings.CutSuffix(ref.Name, "-"+hash)
	if !ok {
		return ""
	}
	return byDeployment[name]
}

// ownerReleasePods returns the pods of namespace without a release label
// whose Deployment belongs to release (podRelease). It is the fallback of
// releasePods for charts that do not label their pods (for example
// podinfo): one Deployment list and one pod list.
func (c *Client) ownerReleasePods(ctx context.Context, namespace, release string) ([]corev1.Pod, error) {
	deps, err := c.clientset.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list deployments in %s: %w", namespace, err)
	}
	byDeployment := deploymentReleases(namespace, deps.Items)
	found := false
	for _, r := range byDeployment {
		if r == release {
			found = true
			break
		}
	}
	if !found {
		return nil, nil
	}
	list, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list pods in %s: %w", namespace, err)
	}
	var out []corev1.Pod
	for i := range list.Items {
		p := &list.Items[i]
		if labelRelease(p.Labels) == "" && podRelease(p, byDeployment) == release {
			out = append(out, *p)
		}
	}
	return out, nil
}

// addOwnerReleasePods adds to byRelease (release -> pods) the pods of pods
// without a release label whose Deployment belongs to a release of byRelease
// that has no pod yet, like releasePods falls back to ownerReleasePods only
// for a release without a labelled pod. It lists the Deployments only when
// such a release and such a pod exist.
func (c *Client) addOwnerReleasePods(ctx context.Context, namespace string, pods []corev1.Pod, byRelease map[string][]corev1.Pod) error {
	empty := map[string]bool{}
	for r, ps := range byRelease {
		if len(ps) == 0 {
			empty[r] = true
		}
	}
	unlabelled := false
	for i := range pods {
		if labelRelease(pods[i].Labels) == "" {
			unlabelled = true
			break
		}
	}
	if len(empty) == 0 || !unlabelled {
		return nil
	}
	deps, err := c.clientset.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list deployments in %s: %w", namespace, err)
	}
	byDeployment := deploymentReleases(namespace, deps.Items)
	for i := range pods {
		p := &pods[i]
		if labelRelease(p.Labels) != "" {
			continue
		}
		if r := podRelease(p, byDeployment); empty[r] {
			byRelease[r] = append(byRelease[r], *p)
		}
	}
	return nil
}

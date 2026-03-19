package k8s

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Status constants for namespace and chart health.
const (
	StatusHealthy     = "healthy"
	StatusProgressing = "progressing"
	StatusDegraded    = "degraded"
	StatusError       = "error"
	StatusNotFound    = "not_found"
)

// degradedRestartThreshold is the minimum restart count at which a pod is
// considered degraded rather than progressing.
const degradedRestartThreshold int32 = 5

// helmReleaseLabels are the standard labels used to identify Helm releases.
var helmReleaseLabels = []string{
	"app.kubernetes.io/instance",
	"helm.sh/release",
}

// NamespaceStatus represents the health of all resources in a namespace.
type NamespaceStatus struct {
	LastChecked time.Time     `json:"last_checked"`
	Namespace   string        `json:"namespace"`
	Status      string        `json:"status"`
	Charts      []ChartStatus `json:"charts"`
}

// ChartStatus represents the status of a single Helm release's resources.
type ChartStatus struct {
	ReleaseName string           `json:"release_name"`
	ChartName   string           `json:"chart_name"`
	Status      string           `json:"status"`
	Deployments []DeploymentInfo `json:"deployments"`
	Pods        []PodInfo        `json:"pods"`
	Services    []ServiceInfo    `json:"services"`
}

// DeploymentInfo summarizes a Kubernetes Deployment.
type DeploymentInfo struct {
	Name            string `json:"name"`
	ReadyReplicas   int32  `json:"ready_replicas"`
	DesiredReplicas int32  `json:"desired_replicas"`
	UpdatedReplicas int32  `json:"updated_replicas"`
	Available       bool   `json:"available"`
}

// PodInfo summarizes a Kubernetes Pod.
type PodInfo struct {
	Name         string `json:"name"`
	Phase        string `json:"phase"`
	Image        string `json:"image"`
	RestartCount int32  `json:"restart_count"`
	Ready        bool   `json:"ready"`
}

// ServiceInfo summarizes a Kubernetes Service.
type ServiceInfo struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	ClusterIP string `json:"cluster_ip"`
}

// GetNamespaceStatus queries the K8s API for the status of all resources in a namespace.
func (c *Client) GetNamespaceStatus(ctx context.Context, namespace string) (*NamespaceStatus, error) {
	exists, err := c.NamespaceExists(ctx, namespace)
	if err != nil {
		return nil, fmt.Errorf("check namespace: %w", err)
	}
	if !exists {
		return &NamespaceStatus{
			Namespace:   namespace,
			Status:      StatusNotFound,
			LastChecked: time.Now().UTC(),
		}, nil
	}

	// Fetch all deployments.
	deployList, err := c.clientset.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list deployments in %q: %w", namespace, err)
	}

	// Fetch all pods.
	podList, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list pods in %q: %w", namespace, err)
	}

	// Fetch all services.
	svcList, err := c.clientset.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list services in %q: %w", namespace, err)
	}

	// Group resources by Helm release label.
	type chartResources struct {
		deployments []DeploymentInfo
		pods        []PodInfo
		services    []ServiceInfo
	}
	charts := make(map[string]*chartResources)

	getRelease := func(labels map[string]string) string {
		for _, key := range helmReleaseLabels {
			if v, ok := labels[key]; ok {
				return v
			}
		}
		return ""
	}

	ensureChart := func(release string) *chartResources {
		if _, ok := charts[release]; !ok {
			charts[release] = &chartResources{}
		}
		return charts[release]
	}

	for _, d := range deployList.Items {
		release := getRelease(d.Labels)
		if release == "" {
			release = "_unmanaged"
		}
		cr := ensureChart(release)

		available := false
		for _, cond := range d.Status.Conditions {
			if cond.Type == appsv1.DeploymentAvailable && cond.Status == corev1.ConditionTrue {
				available = true
				break
			}
		}

		cr.deployments = append(cr.deployments, DeploymentInfo{
			Name:            d.Name,
			DesiredReplicas: ptrInt32OrDefault(d.Spec.Replicas, 1),
			ReadyReplicas:   d.Status.ReadyReplicas,
			UpdatedReplicas: d.Status.UpdatedReplicas,
			Available:       available,
		})
	}

	for _, p := range podList.Items {
		release := getRelease(p.Labels)
		if release == "" {
			release = "_unmanaged"
		}
		cr := ensureChart(release)

		ready := true
		var restarts int32
		for _, cs := range p.Status.ContainerStatuses {
			if !cs.Ready {
				ready = false
			}
			restarts += cs.RestartCount
		}

		image := ""
		if len(p.Spec.Containers) > 0 {
			image = p.Spec.Containers[0].Image
		}

		cr.pods = append(cr.pods, PodInfo{
			Name:         p.Name,
			Phase:        string(p.Status.Phase),
			Ready:        ready,
			RestartCount: restarts,
			Image:        image,
		})
	}

	for _, s := range svcList.Items {
		release := getRelease(s.Labels)
		if release == "" {
			release = "_unmanaged"
		}
		cr := ensureChart(release)

		cr.services = append(cr.services, ServiceInfo{
			Name:      s.Name,
			Type:      string(s.Spec.Type),
			ClusterIP: s.Spec.ClusterIP,
		})
	}

	// Build chart statuses.
	var chartStatuses []ChartStatus
	for release, cr := range charts {
		cs := ChartStatus{
			ReleaseName: release,
			ChartName:   release, // Chart name defaults to release name; can be refined via labels.
			Deployments: cr.deployments,
			Pods:        cr.pods,
			Services:    cr.services,
			Status:      determineChartStatus(cr.deployments, cr.pods),
		}
		chartStatuses = append(chartStatuses, cs)
	}

	overall := determineOverallStatus(chartStatuses)

	slog.Debug("Namespace status checked",
		"namespace", namespace,
		"status", overall,
		"charts", len(chartStatuses),
	)

	return &NamespaceStatus{
		Namespace:   namespace,
		Status:      overall,
		Charts:      chartStatuses,
		LastChecked: time.Now().UTC(),
	}, nil
}

// determineChartStatus evaluates the health of a single chart release.
func determineChartStatus(deployments []DeploymentInfo, pods []PodInfo) string {
	hasError := false
	hasDegraded := false
	hasProgressing := false

	for _, p := range pods {
		switch {
		case p.Phase == "Failed":
			hasError = true
		case p.RestartCount >= degradedRestartThreshold:
			hasDegraded = true
		case p.Phase == "Pending" || !p.Ready:
			hasProgressing = true
		}
	}

	for _, d := range deployments {
		if d.ReadyReplicas < d.DesiredReplicas {
			if d.UpdatedReplicas < d.DesiredReplicas {
				hasProgressing = true
			} else {
				hasDegraded = true
			}
		}
	}

	switch {
	case hasError:
		return StatusError
	case hasDegraded:
		return StatusDegraded
	case hasProgressing:
		return StatusProgressing
	default:
		return StatusHealthy
	}
}

// determineOverallStatus returns the worst status across all charts.
func determineOverallStatus(charts []ChartStatus) string {
	if len(charts) == 0 {
		return StatusHealthy
	}

	// Priority: error > degraded > progressing > healthy.
	priority := map[string]int{
		StatusHealthy:     0,
		StatusProgressing: 1,
		StatusDegraded:    2,
		StatusError:       3,
	}

	worst := StatusHealthy
	for _, c := range charts {
		if priority[c.Status] > priority[worst] {
			worst = c.Status
		}
	}
	return worst
}

// ptrInt32OrDefault dereferences an *int32 pointer, returning the default value
// if the pointer is nil.
func ptrInt32OrDefault(p *int32, def int32) int32 {
	if p == nil {
		return def
	}
	return *p
}

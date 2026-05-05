package k8s

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
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

// NamespaceInfo provides basic metadata about a Kubernetes namespace.
type NamespaceInfo struct {
	CreatedAt time.Time `json:"created_at"`
	Name      string    `json:"name"`
	Phase     string    `json:"phase"`
}

// ResourceCounts summarizes the number of key resource types in a namespace.
type ResourceCounts struct {
	Pods        int `json:"pods"`
	Deployments int `json:"deployments"`
	Services    int `json:"services"`
}

// ListStackNamespaces returns all namespaces whose names start with "stack-".
func (c *Client) ListStackNamespaces(ctx context.Context) ([]NamespaceInfo, error) {
	nsList, err := c.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}

	var result []NamespaceInfo
	for _, ns := range nsList.Items {
		if strings.HasPrefix(ns.Name, "stack-") {
			result = append(result, NamespaceInfo{
				Name:      ns.Name,
				CreatedAt: ns.CreationTimestamp.Time,
				Phase:     string(ns.Status.Phase),
			})
		}
	}

	slog.Debug("Listed stack namespaces", "count", len(result))
	return result, nil
}

// GetResourceCounts returns the number of pods, deployments, and services in a namespace.
func (c *Client) GetResourceCounts(ctx context.Context, namespace string) (*ResourceCounts, error) {
	pods, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list pods in %q: %w", namespace, err)
	}

	deployments, err := c.clientset.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list deployments in %q: %w", namespace, err)
	}

	services, err := c.clientset.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list services in %q: %w", namespace, err)
	}

	return &ResourceCounts{
		Pods:        len(pods.Items),
		Deployments: len(deployments.Items),
		Services:    len(services.Items),
	}, nil
}

// IngressInfo represents a discovered Ingress resource with constructed access URL.
type IngressInfo struct {
	Name string `json:"name"`
	Host string `json:"host"`
	Path string `json:"path"`
	URL  string `json:"url"`
	TLS  bool   `json:"tls"`
}

// NamespaceStatus represents the health of all resources in a namespace.
type NamespaceStatus struct {
	LastChecked time.Time     `json:"last_checked"`
	Namespace   string        `json:"namespace"`
	Status      string        `json:"status"`
	Charts      []ChartStatus `json:"charts"`
	Ingresses   []IngressInfo `json:"ingresses,omitempty"`
	Events      []PodEvent    `json:"events,omitempty"`
}

// PodEvent represents a Kubernetes event associated with a pod or other resource
// in the namespace.
type PodEvent struct {
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Type      string    `json:"type"` // "Normal", "Warning"
	Reason    string    `json:"reason"`
	Message   string    `json:"message"`
	Object    string    `json:"object"` // "Pod/my-pod-xyz"
	Count     int32     `json:"count"`
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
	StartTime       *time.Time           `json:"start_time,omitempty"`
	ContainerStates []ContainerStateInfo `json:"container_states"`
	Conditions      []PodConditionInfo   `json:"conditions,omitempty"`
	Name            string               `json:"name"`
	Phase           string               `json:"phase"`
	Image           string               `json:"image"`
	NodeName        string               `json:"node_name,omitempty"`
	RestartCount    int32                `json:"restart_count"`
	Ready           bool                 `json:"ready"`
}

// ContainerStateInfo provides detailed state information for a single container
// within a pod, including its running/waiting/terminated state and reason.
type ContainerStateInfo struct {
	ExitCode     *int32 `json:"exit_code,omitempty"`
	Name         string `json:"name"`
	State        string `json:"state"` // "running", "waiting", "terminated", "unknown"
	Reason       string `json:"reason,omitempty"`
	Message      string `json:"message,omitempty"`
	Image        string `json:"image"`
	RestartCount int32  `json:"restart_count"`
	Ready        bool   `json:"ready"`
}

// PodConditionInfo represents a single condition reported by the kubelet for a pod.
type PodConditionInfo struct {
	Type    string `json:"type"`   // "Ready", "ContainersReady", "Initialized", "PodScheduled"
	Status  string `json:"status"` // "True", "False", "Unknown"
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// ServiceInfo summarizes a Kubernetes Service.
type ServiceInfo struct {
	Name         string   `json:"name"`
	Type         string   `json:"type"`
	ClusterIP    string   `json:"cluster_ip"`
	ExternalIP   string   `json:"external_ip,omitempty"`
	Ports        []string `json:"ports,omitempty"`
	NodePorts    []int32  `json:"node_ports,omitempty"`
	IngressHosts []string `json:"ingress_hosts,omitempty"`
}

// StatusOptions controls optional data included in GetNamespaceStatus.
type StatusOptions struct {
	IncludeEvents bool
}

// GetNamespaceStatus queries the K8s API for the status of all resources in a namespace.
func (c *Client) GetNamespaceStatus(ctx context.Context, namespace string, opts StatusOptions) (*NamespaceStatus, error) {
	exists, err := c.NamespaceExists(ctx, namespace)
	if err != nil {
		return nil, fmt.Errorf("check namespace: %w", err)
	}
	if !exists {
		return &NamespaceStatus{
			Namespace:   namespace,
			Status:      StatusNotFound,
			Charts:      []ChartStatus{},
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
			charts[release] = &chartResources{
				deployments: []DeploymentInfo{},
				pods:        []PodInfo{},
				services:    []ServiceInfo{},
			}
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
		var containerStates []ContainerStateInfo
		// Include init containers so that init-phase failures (CrashLoopBackOff,
		// ImagePullBackOff) are visible in the pod health response.
		allContainers := append(p.Status.InitContainerStatuses, p.Status.ContainerStatuses...) //nolint:gocritic
		for _, cs := range allContainers {
			if !cs.Ready {
				ready = false
			}
			restarts += cs.RestartCount

			state := ContainerStateInfo{
				Name:         cs.Name,
				RestartCount: cs.RestartCount,
				Ready:        cs.Ready,
				Image:        cs.Image,
			}
			if cs.State.Running != nil {
				state.State = "running"
			} else if cs.State.Waiting != nil {
				state.State = "waiting"
				state.Reason = cs.State.Waiting.Reason
				state.Message = cs.State.Waiting.Message
			} else if cs.State.Terminated != nil {
				state.State = "terminated"
				state.Reason = cs.State.Terminated.Reason
				state.Message = cs.State.Terminated.Message
				exitCode := cs.State.Terminated.ExitCode
				state.ExitCode = &exitCode
			} else {
				state.State = "unknown"
			}
			containerStates = append(containerStates, state)
		}
		if containerStates == nil {
			containerStates = []ContainerStateInfo{}
		}

		var conditions []PodConditionInfo
		for _, cond := range p.Status.Conditions {
			conditions = append(conditions, PodConditionInfo{
				Type:    string(cond.Type),
				Status:  string(cond.Status),
				Reason:  cond.Reason,
				Message: cond.Message,
			})
		}

		image := ""
		if len(p.Spec.Containers) > 0 {
			image = p.Spec.Containers[0].Image
		}

		podInfo := PodInfo{
			Name:            p.Name,
			Phase:           string(p.Status.Phase),
			Ready:           ready,
			RestartCount:    restarts,
			Image:           image,
			ContainerStates: containerStates,
			Conditions:      conditions,
			NodeName:        p.Spec.NodeName,
		}
		if p.Status.StartTime != nil {
			t := p.Status.StartTime.Time
			podInfo.StartTime = &t
		}

		cr.pods = append(cr.pods, podInfo)
	}

	for _, s := range svcList.Items {
		release := getRelease(s.Labels)
		if release == "" {
			release = "_unmanaged"
		}
		cr := ensureChart(release)

		si := ServiceInfo{
			Name:      s.Name,
			Type:      string(s.Spec.Type),
			ClusterIP: s.Spec.ClusterIP,
		}

		// Populate service ports.
		for _, p := range s.Spec.Ports {
			si.Ports = append(si.Ports, fmt.Sprintf("%d/%s", p.Port, p.Protocol))
		}

		// Extract ExternalIP for LoadBalancer services.
		if s.Spec.Type == corev1.ServiceTypeLoadBalancer && len(s.Status.LoadBalancer.Ingress) > 0 {
			lbIngress := s.Status.LoadBalancer.Ingress[0]
			if lbIngress.IP != "" {
				si.ExternalIP = lbIngress.IP
			} else if lbIngress.Hostname != "" {
				si.ExternalIP = lbIngress.Hostname
			}
		}

		// Extract NodePorts for NodePort services.
		if s.Spec.Type == corev1.ServiceTypeNodePort || s.Spec.Type == corev1.ServiceTypeLoadBalancer {
			for _, port := range s.Spec.Ports {
				if port.NodePort != 0 {
					si.NodePorts = append(si.NodePorts, port.NodePort)
				}
			}
		}

		cr.services = append(cr.services, si)
	}

	// Fetch Ingresses (non-fatal on failure).
	var ingresses []IngressInfo
	ingressList, err := c.clientset.NetworkingV1().Ingresses(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		slog.Warn("Failed to list ingresses, skipping", "namespace", namespace, "error", err)
	} else {
		// Build a set of TLS hosts for quick lookups.
		tlsHosts := make(map[string]bool)
		// Map from backend service name → ingress hosts that reference it.
		svcIngressHosts := make(map[string][]string)

		for _, ing := range ingressList.Items {
			for _, tlsEntry := range ing.Spec.TLS {
				for _, h := range tlsEntry.Hosts {
					tlsHosts[h] = true
				}
			}

			for _, rule := range ing.Spec.Rules {
				host := rule.Host
				if rule.HTTP == nil {
					continue
				}
				for _, p := range rule.HTTP.Paths {
					tls := tlsHosts[host]
					path := p.Path
					ingresses = append(ingresses, IngressInfo{
						Name: ing.Name,
						Host: host,
						Path: path,
						TLS:  tls,
						URL:  constructIngressURL(host, path, tls),
					})

					// Track which services are referenced by ingress rules.
					if p.Backend.Service != nil {
						svcName := p.Backend.Service.Name
						svcIngressHosts[svcName] = appendUnique(svcIngressHosts[svcName], host)
					}
				}
			}
		}

		// Attach ingress hosts to matching service entries.
		for release, cr := range charts {
			for i, svc := range cr.services {
				if hosts, ok := svcIngressHosts[svc.Name]; ok {
					charts[release].services[i].IngressHosts = hosts
				}
			}
		}
	}

	// Fetch namespace events only when explicitly requested (non-fatal on failure).
	// Only include Warning events or events from the last hour.
	var events []PodEvent
	if opts.IncludeEvents {
		var eventsTimeoutSeconds int64 = 5
		var eventListLimit int64 = 200
		eventList, evErr := c.clientset.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
			TimeoutSeconds: &eventsTimeoutSeconds,
			Limit:          eventListLimit,
		})
		if evErr != nil {
			slog.Warn("Failed to list events, skipping", "namespace", namespace, "error", evErr)
		} else {
			oneHourAgo := time.Now().UTC().Add(-1 * time.Hour)
			for _, e := range eventList.Items {
				lastSeen := e.LastTimestamp.Time
				if lastSeen.IsZero() {
					lastSeen = e.CreationTimestamp.Time
				}
				firstSeen := e.FirstTimestamp.Time
				if firstSeen.IsZero() {
					firstSeen = e.CreationTimestamp.Time
				}

				// Include Warning events regardless of age, or any event from the last hour.
				if e.Type != "Warning" && lastSeen.Before(oneHourAgo) {
					continue
				}

				objectRef := ""
				if e.InvolvedObject.Kind != "" {
					objectRef = e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name
				}

				events = append(events, PodEvent{
					Type:      e.Type,
					Reason:    e.Reason,
					Message:   e.Message,
					Object:    objectRef,
					Count:     e.Count,
					FirstSeen: firstSeen,
					LastSeen:  lastSeen,
				})
			}
			// Sort by most recent first and cap at 50 to bound payload size.
			sort.Slice(events, func(i, j int) bool {
				return events[i].LastSeen.After(events[j].LastSeen)
			})
			if len(events) > 50 {
				events = events[:50]
			}
		}
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
		Ingresses:   ingresses,
		Events:      events,
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
		case p.Phase == "Succeeded":
			// Completed job pods — not an error, not progressing.
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

// constructIngressURL builds a full URL from an Ingress rule's host and path.
func constructIngressURL(host, path string, tls bool) string {
	scheme := "http"
	if tls {
		scheme = "https"
	}
	if path == "" || path == "/" {
		return fmt.Sprintf("%s://%s", scheme, host)
	}
	return fmt.Sprintf("%s://%s%s", scheme, host, path)
}

// appendUnique appends val to slice only if it is not already present.
func appendUnique(slice []string, val string) []string {
	for _, s := range slice {
		if s == val {
			return slice
		}
	}
	return append(slice, val)
}

// ClusterSummary provides a high-level overview of cluster health and capacity.
type ClusterSummary struct {
	NodeCount         int    `json:"node_count"`
	ReadyNodeCount    int    `json:"ready_node_count"`
	TotalCPU          string `json:"total_cpu"`
	TotalMemory       string `json:"total_memory"`
	AllocatableCPU    string `json:"allocatable_cpu"`
	AllocatableMemory string `json:"allocatable_memory"`
	NamespaceCount    int    `json:"namespace_count"`
}

// NodeStatus represents the health and capacity of a single cluster node.
type NodeStatus struct {
	Name        string           `json:"name"`
	Status      string           `json:"status"` // "Ready" or "NotReady"
	Conditions  []NodeCondition  `json:"conditions"`
	Capacity    ResourceQuantity `json:"capacity"`
	Allocatable ResourceQuantity `json:"allocatable"`
	PodCount    int              `json:"pod_count"`
}

// NodeCondition represents a single node condition (Ready, MemoryPressure, etc.).
type NodeCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"` // "True", "False", "Unknown"
	Message string `json:"message,omitempty"`
}

// ResourceQuantity holds CPU, memory, and pod capacity values as strings.
type ResourceQuantity struct {
	CPU    string `json:"cpu"`
	Memory string `json:"memory"`
	Pods   string `json:"pods,omitempty"`
}

// GetClusterSummary returns a high-level overview of cluster health and capacity.
func (c *Client) GetClusterSummary(ctx context.Context) (*ClusterSummary, error) {
	nodeList, err := c.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}

	var readyCount int
	var totalCPUMillis, allocatableCPUMillis int64
	var totalMemBytes, allocatableMemBytes int64

	for _, node := range nodeList.Items {
		for _, cond := range node.Status.Conditions {
			if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
				readyCount++
				break
			}
		}

		if cpu, ok := node.Status.Capacity[corev1.ResourceCPU]; ok {
			totalCPUMillis += cpu.MilliValue()
		}
		if mem, ok := node.Status.Capacity[corev1.ResourceMemory]; ok {
			totalMemBytes += mem.Value()
		}
		if cpu, ok := node.Status.Allocatable[corev1.ResourceCPU]; ok {
			allocatableCPUMillis += cpu.MilliValue()
		}
		if mem, ok := node.Status.Allocatable[corev1.ResourceMemory]; ok {
			allocatableMemBytes += mem.Value()
		}
	}

	// Count stack-* namespaces.
	nsList, err := c.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}
	var nsCount int
	for _, ns := range nsList.Items {
		if strings.HasPrefix(ns.Name, "stack-") {
			nsCount++
		}
	}

	return &ClusterSummary{
		NodeCount:         len(nodeList.Items),
		ReadyNodeCount:    readyCount,
		TotalCPU:          fmt.Sprintf("%dm", totalCPUMillis),
		TotalMemory:       formatMemoryBytes(totalMemBytes),
		AllocatableCPU:    fmt.Sprintf("%dm", allocatableCPUMillis),
		AllocatableMemory: formatMemoryBytes(allocatableMemBytes),
		NamespaceCount:    nsCount,
	}, nil
}

// GetNodeStatuses returns per-node health, conditions, and capacity.
func (c *Client) GetNodeStatuses(ctx context.Context) ([]NodeStatus, error) {
	nodeList, err := c.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}

	// List all pods once and count per node to avoid N+1 API calls.
	podCountByNode := make(map[string]int, len(nodeList.Items))
	allPods, err := c.clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		slog.Warn("Failed to list pods for node counts", "error", err)
	} else {
		for _, p := range allPods.Items {
			if p.Spec.NodeName != "" {
				podCountByNode[p.Spec.NodeName]++
			}
		}
	}

	result := make([]NodeStatus, 0, len(nodeList.Items))
	for _, node := range nodeList.Items {
		status := "NotReady"
		var conditions []NodeCondition
		for _, cond := range node.Status.Conditions {
			if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
				status = "Ready"
			}
			conditions = append(conditions, NodeCondition{
				Type:    string(cond.Type),
				Status:  string(cond.Status),
				Message: cond.Message,
			})
		}

		capacity := ResourceQuantity{
			CPU:    node.Status.Capacity.Cpu().String(),
			Memory: node.Status.Capacity.Memory().String(),
			Pods:   node.Status.Capacity.Pods().String(),
		}
		allocatable := ResourceQuantity{
			CPU:    node.Status.Allocatable.Cpu().String(),
			Memory: node.Status.Allocatable.Memory().String(),
			Pods:   node.Status.Allocatable.Pods().String(),
		}

		result = append(result, NodeStatus{
			Name:        node.Name,
			Status:      status,
			Conditions:  conditions,
			Capacity:    capacity,
			Allocatable: allocatable,
			PodCount:    podCountByNode[node.Name],
		})
	}

	// Sort by name for deterministic output.
	sortNodeStatuses(result)
	return result, nil
}

// sortNodeStatuses sorts node statuses by name in ascending order.
func sortNodeStatuses(nodes []NodeStatus) {
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Name < nodes[j].Name
	})
}

// formatMemoryBytes formats a byte count as a human-readable string (Mi or Gi).
func formatMemoryBytes(bytes int64) string {
	const gi = 1024 * 1024 * 1024
	const mi = 1024 * 1024
	if bytes >= gi {
		return fmt.Sprintf("%.1fGi", float64(bytes)/float64(gi))
	}
	return fmt.Sprintf("%dMi", bytes/mi)
}

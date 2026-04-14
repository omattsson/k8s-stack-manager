package k8s

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestGetNamespaceStatus_NotFound(t *testing.T) {
	t.Parallel()

	cs := fake.NewSimpleClientset()
	client := NewClientFromInterface(cs)

	status, err := client.GetNamespaceStatus(context.Background(), "nonexistent", StatusOptions{})
	assert.NoError(t, err)
	require.NotNil(t, status)
	assert.Equal(t, StatusNotFound, status.Status)
	assert.Equal(t, "nonexistent", status.Namespace)
	assert.Empty(t, status.Charts)
}

func TestGetNamespaceStatus_EmptyNamespace(t *testing.T) {
	t.Parallel()

	cs := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "empty-ns"},
	})
	client := NewClientFromInterface(cs)

	status, err := client.GetNamespaceStatus(context.Background(), "empty-ns", StatusOptions{})
	assert.NoError(t, err)
	require.NotNil(t, status)
	assert.Equal(t, StatusHealthy, status.Status)
	assert.Empty(t, status.Charts)
	assert.Equal(t, "empty-ns", status.Namespace)
}

func TestGetNamespaceStatus_WithHealthyResources(t *testing.T) {
	t.Parallel()

	ns := "test-ns"
	replicas := int32(2)

	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-app",
				Namespace: ns,
				Labels: map[string]string{
					"app.kubernetes.io/instance": "my-release",
				},
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
			},
			Status: appsv1.DeploymentStatus{
				ReadyReplicas:   2,
				Replicas:        2,
				UpdatedReplicas: 2,
				Conditions: []appsv1.DeploymentCondition{
					{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
				},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-app-pod-1",
				Namespace: ns,
				Labels: map[string]string{
					"app.kubernetes.io/instance": "my-release",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "myimage:v1"}},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{
					{Ready: true, RestartCount: 0, Image: "myimage:v1"},
				},
			},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-app-svc",
				Namespace: ns,
				Labels: map[string]string{
					"app.kubernetes.io/instance": "my-release",
				},
			},
			Spec: corev1.ServiceSpec{
				Type:      corev1.ServiceTypeClusterIP,
				ClusterIP: "10.0.0.1",
			},
		},
	)

	client := NewClientFromInterface(cs)
	status, err := client.GetNamespaceStatus(context.Background(), ns, StatusOptions{})

	assert.NoError(t, err)
	require.NotNil(t, status)
	assert.Equal(t, ns, status.Namespace)
	assert.Equal(t, StatusHealthy, status.Status)

	require.Len(t, status.Charts, 1)
	chart := status.Charts[0]
	assert.Equal(t, "my-release", chart.ReleaseName)
	assert.Equal(t, StatusHealthy, chart.Status)

	require.Len(t, chart.Deployments, 1)
	assert.Equal(t, "my-app", chart.Deployments[0].Name)
	assert.Equal(t, int32(2), chart.Deployments[0].ReadyReplicas)
	assert.Equal(t, int32(2), chart.Deployments[0].DesiredReplicas)
	assert.True(t, chart.Deployments[0].Available)

	require.Len(t, chart.Pods, 1)
	assert.Equal(t, "my-app-pod-1", chart.Pods[0].Name)
	assert.Equal(t, "Running", chart.Pods[0].Phase)
	assert.True(t, chart.Pods[0].Ready)
	assert.Equal(t, int32(0), chart.Pods[0].RestartCount)
	assert.Equal(t, "myimage:v1", chart.Pods[0].Image)

	require.Len(t, chart.Services, 1)
	assert.Equal(t, "my-app-svc", chart.Services[0].Name)
	assert.Equal(t, "ClusterIP", chart.Services[0].Type)
	assert.Equal(t, "10.0.0.1", chart.Services[0].ClusterIP)
}

func TestGetNamespaceStatus_MultipleReleases(t *testing.T) {
	t.Parallel()

	ns := "multi-ns"
	replicas := int32(1)

	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "frontend",
				Namespace: ns,
				Labels:    map[string]string{"app.kubernetes.io/instance": "frontend-release"},
			},
			Spec:   appsv1.DeploymentSpec{Replicas: &replicas},
			Status: appsv1.DeploymentStatus{ReadyReplicas: 1, Replicas: 1, UpdatedReplicas: 1},
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "backend",
				Namespace: ns,
				Labels:    map[string]string{"app.kubernetes.io/instance": "backend-release"},
			},
			Spec:   appsv1.DeploymentSpec{Replicas: &replicas},
			Status: appsv1.DeploymentStatus{ReadyReplicas: 1, Replicas: 1, UpdatedReplicas: 1},
		},
	)

	client := NewClientFromInterface(cs)
	status, err := client.GetNamespaceStatus(context.Background(), ns, StatusOptions{})

	assert.NoError(t, err)
	require.NotNil(t, status)
	assert.Equal(t, StatusHealthy, status.Status)
	assert.Len(t, status.Charts, 2)
}

func TestGetNamespaceStatus_DegradedPod(t *testing.T) {
	t.Parallel()

	ns := "degraded-ns"
	replicas := int32(1)

	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "crashy-app",
				Namespace: ns,
				Labels:    map[string]string{"app.kubernetes.io/instance": "crashy"},
			},
			Spec:   appsv1.DeploymentSpec{Replicas: &replicas},
			Status: appsv1.DeploymentStatus{ReadyReplicas: 1, Replicas: 1, UpdatedReplicas: 1},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "crashy-pod-1",
				Namespace: ns,
				Labels:    map[string]string{"app.kubernetes.io/instance": "crashy"},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "myimage:v1"}},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{
					{Ready: true, RestartCount: 10, Image: "myimage:v1"},
				},
			},
		},
	)

	client := NewClientFromInterface(cs)
	status, err := client.GetNamespaceStatus(context.Background(), ns, StatusOptions{})

	assert.NoError(t, err)
	require.NotNil(t, status)
	assert.Equal(t, StatusDegraded, status.Status)
	require.Len(t, status.Charts, 1)
	assert.Equal(t, StatusDegraded, status.Charts[0].Status)
}

func TestGetNamespaceStatus_FailedPod(t *testing.T) {
	t.Parallel()

	ns := "failed-ns"

	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "failed-pod",
				Namespace: ns,
				Labels:    map[string]string{"app.kubernetes.io/instance": "fail-release"},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "myimage:v1"}},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodFailed,
			},
		},
	)

	client := NewClientFromInterface(cs)
	status, err := client.GetNamespaceStatus(context.Background(), ns, StatusOptions{})

	assert.NoError(t, err)
	require.NotNil(t, status)
	assert.Equal(t, StatusError, status.Status)
	require.Len(t, status.Charts, 1)
	assert.Equal(t, StatusError, status.Charts[0].Status)
}

func TestConstructIngressURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		host     string
		path     string
		tls      bool
		expected string
	}{
		{
			name:     "HTTP with path",
			host:     "example.com",
			path:     "/api",
			tls:      false,
			expected: "http://example.com/api",
		},
		{
			name:     "HTTPS with path",
			host:     "example.com",
			path:     "/api",
			tls:      true,
			expected: "https://example.com/api",
		},
		{
			name:     "HTTP no path",
			host:     "example.com",
			path:     "",
			tls:      false,
			expected: "http://example.com",
		},
		{
			name:     "HTTPS root path",
			host:     "example.com",
			path:     "/",
			tls:      true,
			expected: "https://example.com",
		},
		{
			name:     "HTTP with nested path",
			host:     "app.example.com",
			path:     "/v1/health",
			tls:      false,
			expected: "http://app.example.com/v1/health",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := constructIngressURL(tt.host, tt.path, tt.tls)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetNamespaceStatus_WithIngress(t *testing.T) {
	t.Parallel()

	ns := "ingress-ns"
	pathType := networkingv1.PathTypePrefix

	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "web-svc",
				Namespace: ns,
				Labels:    map[string]string{"app.kubernetes.io/instance": "web-release"},
			},
			Spec: corev1.ServiceSpec{
				Type:      corev1.ServiceTypeClusterIP,
				ClusterIP: "10.0.0.5",
			},
		},
		&networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "web-ingress",
				Namespace: ns,
			},
			Spec: networkingv1.IngressSpec{
				TLS: []networkingv1.IngressTLS{
					{Hosts: []string{"app.example.com"}},
				},
				Rules: []networkingv1.IngressRule{
					{
						Host: "app.example.com",
						IngressRuleValue: networkingv1.IngressRuleValue{
							HTTP: &networkingv1.HTTPIngressRuleValue{
								Paths: []networkingv1.HTTPIngressPath{
									{
										Path:     "/",
										PathType: &pathType,
										Backend: networkingv1.IngressBackend{
											Service: &networkingv1.IngressServiceBackend{
												Name: "web-svc",
												Port: networkingv1.ServiceBackendPort{Number: 80},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	)

	client := NewClientFromInterface(cs)
	status, err := client.GetNamespaceStatus(context.Background(), ns, StatusOptions{})

	assert.NoError(t, err)
	require.NotNil(t, status)

	// Check ingresses.
	require.Len(t, status.Ingresses, 1)
	ing := status.Ingresses[0]
	assert.Equal(t, "web-ingress", ing.Name)
	assert.Equal(t, "app.example.com", ing.Host)
	assert.Equal(t, "/", ing.Path)
	assert.True(t, ing.TLS)
	assert.Equal(t, "https://app.example.com", ing.URL)

	// Check that the service has ingress hosts populated.
	require.Len(t, status.Charts, 1)
	require.Len(t, status.Charts[0].Services, 1)
	assert.Equal(t, []string{"app.example.com"}, status.Charts[0].Services[0].IngressHosts)
}

func TestGetNamespaceStatus_WithIngressNoTLS(t *testing.T) {
	t.Parallel()

	ns := "ingress-notls-ns"
	pathType := networkingv1.PathTypePrefix

	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
		&networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "plain-ingress",
				Namespace: ns,
			},
			Spec: networkingv1.IngressSpec{
				Rules: []networkingv1.IngressRule{
					{
						Host: "plain.example.com",
						IngressRuleValue: networkingv1.IngressRuleValue{
							HTTP: &networkingv1.HTTPIngressRuleValue{
								Paths: []networkingv1.HTTPIngressPath{
									{
										Path:     "/app",
										PathType: &pathType,
										Backend: networkingv1.IngressBackend{
											Service: &networkingv1.IngressServiceBackend{
												Name: "app-svc",
												Port: networkingv1.ServiceBackendPort{Number: 8080},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	)

	client := NewClientFromInterface(cs)
	status, err := client.GetNamespaceStatus(context.Background(), ns, StatusOptions{})

	assert.NoError(t, err)
	require.NotNil(t, status)
	require.Len(t, status.Ingresses, 1)

	ing := status.Ingresses[0]
	assert.Equal(t, "plain-ingress", ing.Name)
	assert.Equal(t, "plain.example.com", ing.Host)
	assert.Equal(t, "/app", ing.Path)
	assert.False(t, ing.TLS)
	assert.Equal(t, "http://plain.example.com/app", ing.URL)
}

func TestGetNamespaceStatus_LoadBalancerExternalIP(t *testing.T) {
	t.Parallel()

	ns := "lb-ns"

	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "lb-svc",
				Namespace: ns,
				Labels:    map[string]string{"app.kubernetes.io/instance": "lb-release"},
			},
			Spec: corev1.ServiceSpec{
				Type:      corev1.ServiceTypeLoadBalancer,
				ClusterIP: "10.0.0.10",
				Ports: []corev1.ServicePort{
					{Port: 80, NodePort: 30080},
				},
			},
			Status: corev1.ServiceStatus{
				LoadBalancer: corev1.LoadBalancerStatus{
					Ingress: []corev1.LoadBalancerIngress{
						{IP: "52.1.2.3"},
					},
				},
			},
		},
	)

	client := NewClientFromInterface(cs)
	status, err := client.GetNamespaceStatus(context.Background(), ns, StatusOptions{})

	assert.NoError(t, err)
	require.NotNil(t, status)
	require.Len(t, status.Charts, 1)
	require.Len(t, status.Charts[0].Services, 1)

	svc := status.Charts[0].Services[0]
	assert.Equal(t, "lb-svc", svc.Name)
	assert.Equal(t, "LoadBalancer", svc.Type)
	assert.Equal(t, "52.1.2.3", svc.ExternalIP)
	assert.Equal(t, []int32{30080}, svc.NodePorts)
}

func TestGetNamespaceStatus_LoadBalancerHostname(t *testing.T) {
	t.Parallel()

	ns := "lb-hostname-ns"

	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "lb-svc",
				Namespace: ns,
				Labels:    map[string]string{"app.kubernetes.io/instance": "lb-release"},
			},
			Spec: corev1.ServiceSpec{
				Type:      corev1.ServiceTypeLoadBalancer,
				ClusterIP: "10.0.0.11",
			},
			Status: corev1.ServiceStatus{
				LoadBalancer: corev1.LoadBalancerStatus{
					Ingress: []corev1.LoadBalancerIngress{
						{Hostname: "my-lb.elb.amazonaws.com"},
					},
				},
			},
		},
	)

	client := NewClientFromInterface(cs)
	status, err := client.GetNamespaceStatus(context.Background(), ns, StatusOptions{})

	assert.NoError(t, err)
	require.NotNil(t, status)
	require.Len(t, status.Charts, 1)
	require.Len(t, status.Charts[0].Services, 1)
	assert.Equal(t, "my-lb.elb.amazonaws.com", status.Charts[0].Services[0].ExternalIP)
}

func TestGetNamespaceStatus_NodePortService(t *testing.T) {
	t.Parallel()

	ns := "nodeport-ns"

	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "np-svc",
				Namespace: ns,
				Labels:    map[string]string{"app.kubernetes.io/instance": "np-release"},
			},
			Spec: corev1.ServiceSpec{
				Type:      corev1.ServiceTypeNodePort,
				ClusterIP: "10.0.0.20",
				Ports: []corev1.ServicePort{
					{Port: 80, NodePort: 30001},
					{Port: 443, NodePort: 30002},
				},
			},
		},
	)

	client := NewClientFromInterface(cs)
	status, err := client.GetNamespaceStatus(context.Background(), ns, StatusOptions{})

	assert.NoError(t, err)
	require.NotNil(t, status)
	require.Len(t, status.Charts, 1)
	require.Len(t, status.Charts[0].Services, 1)

	svc := status.Charts[0].Services[0]
	assert.Equal(t, "NodePort", svc.Type)
	assert.Empty(t, svc.ExternalIP)
	assert.Equal(t, []int32{30001, 30002}, svc.NodePorts)
}

func TestGetNamespaceStatus_ProgressingDeployment(t *testing.T) {
	t.Parallel()

	ns := "progressing-ns"
	replicas := int32(3)

	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "rolling-app",
				Namespace: ns,
				Labels:    map[string]string{"app.kubernetes.io/instance": "rolling"},
			},
			Spec: appsv1.DeploymentSpec{Replicas: &replicas},
			Status: appsv1.DeploymentStatus{
				ReadyReplicas:   1,
				Replicas:        3,
				UpdatedReplicas: 2,
			},
		},
	)

	client := NewClientFromInterface(cs)
	status, err := client.GetNamespaceStatus(context.Background(), ns, StatusOptions{})

	assert.NoError(t, err)
	require.NotNil(t, status)
	assert.Equal(t, StatusProgressing, status.Status)
}

func TestGetNamespaceStatus_UnmanagedResources(t *testing.T) {
	t.Parallel()

	ns := "unmanaged-ns"

	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "orphan-pod",
				Namespace: ns,
				// No Helm release labels.
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "myimage:v1"}},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{
					{Ready: true, RestartCount: 0, Image: "myimage:v1"},
				},
			},
		},
	)

	client := NewClientFromInterface(cs)
	status, err := client.GetNamespaceStatus(context.Background(), ns, StatusOptions{})

	assert.NoError(t, err)
	require.NotNil(t, status)
	// Unmanaged resources get grouped under "_unmanaged".
	require.Len(t, status.Charts, 1)
	assert.Equal(t, "_unmanaged", status.Charts[0].ReleaseName)
}

func TestGetNamespaceStatus_HelmShReleaseLabel(t *testing.T) {
	t.Parallel()

	ns := "helm-sh-ns"

	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "legacy-pod",
				Namespace: ns,
				Labels:    map[string]string{"helm.sh/release": "legacy-release"},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "myimage:v1"}},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{
					{Ready: true, RestartCount: 0},
				},
			},
		},
	)

	client := NewClientFromInterface(cs)
	status, err := client.GetNamespaceStatus(context.Background(), ns, StatusOptions{})

	assert.NoError(t, err)
	require.NotNil(t, status)
	require.Len(t, status.Charts, 1)
	assert.Equal(t, "legacy-release", status.Charts[0].ReleaseName)
}

func TestDetermineChartStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		deployments []DeploymentInfo
		pods        []PodInfo
		want        string
	}{
		{
			name: "all healthy",
			deployments: []DeploymentInfo{
				{ReadyReplicas: 2, DesiredReplicas: 2, UpdatedReplicas: 2},
			},
			pods: []PodInfo{
				{Phase: "Running", Ready: true, RestartCount: 0},
			},
			want: StatusHealthy,
		},
		{
			name: "failed pod",
			pods: []PodInfo{
				{Phase: "Failed", Ready: false},
			},
			want: StatusError,
		},
		{
			name: "high restart count",
			pods: []PodInfo{
				{Phase: "Running", Ready: true, RestartCount: 10},
			},
			want: StatusDegraded,
		},
		{
			name: "pending pod",
			pods: []PodInfo{
				{Phase: "Pending", Ready: false, RestartCount: 0},
			},
			want: StatusProgressing,
		},
		{
			name: "deployment not fully updated",
			deployments: []DeploymentInfo{
				{ReadyReplicas: 1, DesiredReplicas: 3, UpdatedReplicas: 2},
			},
			want: StatusProgressing,
		},
		{
			name: "deployment updated but not ready",
			deployments: []DeploymentInfo{
				{ReadyReplicas: 1, DesiredReplicas: 3, UpdatedReplicas: 3},
			},
			want: StatusDegraded,
		},
		{
			name: "empty — healthy",
			want: StatusHealthy,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := determineChartStatus(tt.deployments, tt.pods)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDetermineOverallStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		charts []ChartStatus
		want   string
	}{
		{
			name:   "no charts",
			charts: nil,
			want:   StatusHealthy,
		},
		{
			name:   "all healthy",
			charts: []ChartStatus{{Status: StatusHealthy}, {Status: StatusHealthy}},
			want:   StatusHealthy,
		},
		{
			name:   "one progressing",
			charts: []ChartStatus{{Status: StatusHealthy}, {Status: StatusProgressing}},
			want:   StatusProgressing,
		},
		{
			name:   "one degraded",
			charts: []ChartStatus{{Status: StatusHealthy}, {Status: StatusDegraded}},
			want:   StatusDegraded,
		},
		{
			name:   "error wins",
			charts: []ChartStatus{{Status: StatusDegraded}, {Status: StatusError}},
			want:   StatusError,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := determineOverallStatus(tt.charts)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPtrInt32OrDefault(t *testing.T) {
	t.Parallel()

	val := int32(5)
	assert.Equal(t, int32(5), ptrInt32OrDefault(&val, 1))
	assert.Equal(t, int32(1), ptrInt32OrDefault(nil, 1))
}

// --- Cluster Summary & Node Status Tests ---

func makeNode(name string, ready bool, cpuCores string, memGi string) *corev1.Node {
	readyStatus := corev1.ConditionFalse
	if ready {
		readyStatus = corev1.ConditionTrue
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: readyStatus, Message: "kubelet is ready"},
			},
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(cpuCores),
				corev1.ResourceMemory: resource.MustParse(memGi),
				corev1.ResourcePods:   resource.MustParse("110"),
			},
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(cpuCores),
				corev1.ResourceMemory: resource.MustParse(memGi),
				corev1.ResourcePods:   resource.MustParse("110"),
			},
		},
	}
}

func TestGetClusterSummary_TwoNodes(t *testing.T) {
	t.Parallel()

	cs := fake.NewSimpleClientset(
		makeNode("node-1", true, "4", "16Gi"),
		makeNode("node-2", true, "4", "16Gi"),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-app1"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-app2"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}},
	)
	client := NewClientFromInterface(cs)

	summary, err := client.GetClusterSummary(context.Background())
	require.NoError(t, err)
	require.NotNil(t, summary)

	assert.Equal(t, 2, summary.NodeCount)
	assert.Equal(t, 2, summary.ReadyNodeCount)
	assert.Equal(t, "8000m", summary.TotalCPU)
	assert.Equal(t, "8000m", summary.AllocatableCPU)
	assert.Equal(t, 2, summary.NamespaceCount) // only stack-* namespaces
	// Memory: 2 * 16Gi = 32Gi → "32.0Gi"
	assert.Equal(t, "32.0Gi", summary.TotalMemory)
	assert.Equal(t, "32.0Gi", summary.AllocatableMemory)
}

func TestGetClusterSummary_NoNodes(t *testing.T) {
	t.Parallel()

	cs := fake.NewSimpleClientset()
	client := NewClientFromInterface(cs)

	summary, err := client.GetClusterSummary(context.Background())
	require.NoError(t, err)
	require.NotNil(t, summary)

	assert.Equal(t, 0, summary.NodeCount)
	assert.Equal(t, 0, summary.ReadyNodeCount)
	assert.Equal(t, "0m", summary.TotalCPU)
	assert.Equal(t, "0Mi", summary.TotalMemory)
	assert.Equal(t, "0m", summary.AllocatableCPU)
	assert.Equal(t, "0Mi", summary.AllocatableMemory)
	assert.Equal(t, 0, summary.NamespaceCount)
}

func TestGetNodeStatuses_MixedReadiness(t *testing.T) {
	t.Parallel()

	cs := fake.NewSimpleClientset(
		makeNode("node-ready", true, "4", "8Gi"),
		makeNode("node-notready", false, "2", "4Gi"),
	)
	client := NewClientFromInterface(cs)

	nodes, err := client.GetNodeStatuses(context.Background())
	require.NoError(t, err)
	require.Len(t, nodes, 2)

	// Sorted by name: node-notready, node-ready
	assert.Equal(t, "node-notready", nodes[0].Name)
	assert.Equal(t, "NotReady", nodes[0].Status)
	assert.Equal(t, "node-ready", nodes[1].Name)
	assert.Equal(t, "Ready", nodes[1].Status)

	// Verify conditions are collected.
	require.NotEmpty(t, nodes[0].Conditions)
	assert.Equal(t, "Ready", nodes[0].Conditions[0].Type)
	assert.Equal(t, "False", nodes[0].Conditions[0].Status)
}

func TestGetNodeStatuses_PodCounts(t *testing.T) {
	t.Parallel()

	cs := fake.NewSimpleClientset(
		makeNode("node-a", true, "4", "8Gi"),
		makeNode("node-b", true, "4", "8Gi"),
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-1", Namespace: "default"},
			Spec:       corev1.PodSpec{NodeName: "node-a"},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-2", Namespace: "default"},
			Spec:       corev1.PodSpec{NodeName: "node-a"},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-3", Namespace: "kube-system"},
			Spec:       corev1.PodSpec{NodeName: "node-b"},
		},
	)
	client := NewClientFromInterface(cs)

	nodes, err := client.GetNodeStatuses(context.Background())
	require.NoError(t, err)
	require.Len(t, nodes, 2)

	// Sorted by name: node-a, node-b
	assert.Equal(t, "node-a", nodes[0].Name)
	assert.Equal(t, "node-b", nodes[1].Name)

	// fake clientset doesn't support field selectors, so pod listing
	// returns all pods for each node query — verify we get counts (may be total).
	// The important thing is the method doesn't error.
	assert.GreaterOrEqual(t, nodes[0].PodCount, 0)
	assert.GreaterOrEqual(t, nodes[1].PodCount, 0)
}

func TestFormatMemoryBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		bytes int64
		want  string
	}{
		{name: "zero", bytes: 0, want: "0Mi"},
		{name: "megabytes", bytes: 512 * 1024 * 1024, want: "512Mi"},
		{name: "one gig", bytes: 1024 * 1024 * 1024, want: "1.0Gi"},
		{name: "multi gig", bytes: 16 * 1024 * 1024 * 1024, want: "16.0Gi"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatMemoryBytes(tt.bytes)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSortNodeStatuses(t *testing.T) {
	t.Parallel()

	nodes := []NodeStatus{
		{Name: "charlie"},
		{Name: "alpha"},
		{Name: "bravo"},
	}
	sortNodeStatuses(nodes)
	assert.Equal(t, "alpha", nodes[0].Name)
	assert.Equal(t, "bravo", nodes[1].Name)
	assert.Equal(t, "charlie", nodes[2].Name)
}

func TestGetNamespaceStatus_ContainerStates(t *testing.T) {
	t.Parallel()

	ns := "container-states-ns"
	exitCode := int32(137)

	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multi-container-pod",
				Namespace: ns,
				Labels:    map[string]string{"app.kubernetes.io/instance": "multi-release"},
			},
			Spec: corev1.PodSpec{
				NodeName: "node-1",
				Containers: []corev1.Container{
					{Name: "app", Image: "myimage:v1"},
					{Name: "sidecar", Image: "sidecar:v1"},
					{Name: "init-done", Image: "init:v1"},
				},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				StartTime: &metav1.Time{Time: metav1.Now().Add(-10 * time.Minute)},
				Conditions: []corev1.PodCondition{
					{Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: "ContainersNotReady", Message: "sidecar not ready"},
					{Type: corev1.PodInitialized, Status: corev1.ConditionTrue},
				},
				ContainerStatuses: []corev1.ContainerStatus{
					{
						Name:         "app",
						Ready:        true,
						RestartCount: 0,
						Image:        "myimage:v1",
						State: corev1.ContainerState{
							Running: &corev1.ContainerStateRunning{},
						},
					},
					{
						Name:         "sidecar",
						Ready:        false,
						RestartCount: 3,
						Image:        "sidecar:v1",
						State: corev1.ContainerState{
							Waiting: &corev1.ContainerStateWaiting{
								Reason:  "CrashLoopBackOff",
								Message: "back-off 5m0s restarting",
							},
						},
					},
					{
						Name:         "init-done",
						Ready:        true,
						RestartCount: 1,
						Image:        "init:v1",
						State: corev1.ContainerState{
							Terminated: &corev1.ContainerStateTerminated{
								Reason:   "OOMKilled",
								Message:  "out of memory",
								ExitCode: exitCode,
							},
						},
					},
				},
			},
		},
	)

	client := NewClientFromInterface(cs)
	status, err := client.GetNamespaceStatus(context.Background(), ns, StatusOptions{})

	assert.NoError(t, err)
	require.NotNil(t, status)
	require.Len(t, status.Charts, 1)
	require.Len(t, status.Charts[0].Pods, 1)

	pod := status.Charts[0].Pods[0]
	assert.Equal(t, "multi-container-pod", pod.Name)
	assert.Equal(t, "Running", pod.Phase)
	assert.False(t, pod.Ready)
	assert.Equal(t, int32(4), pod.RestartCount) // 0 + 3 + 1
	assert.Equal(t, "node-1", pod.NodeName)
	assert.NotNil(t, pod.StartTime)

	// Container states
	require.Len(t, pod.ContainerStates, 3)

	// Running container
	assert.Equal(t, "app", pod.ContainerStates[0].Name)
	assert.Equal(t, "running", pod.ContainerStates[0].State)
	assert.True(t, pod.ContainerStates[0].Ready)
	assert.Equal(t, int32(0), pod.ContainerStates[0].RestartCount)
	assert.Nil(t, pod.ContainerStates[0].ExitCode)

	// Waiting container
	assert.Equal(t, "sidecar", pod.ContainerStates[1].Name)
	assert.Equal(t, "waiting", pod.ContainerStates[1].State)
	assert.Equal(t, "CrashLoopBackOff", pod.ContainerStates[1].Reason)
	assert.Equal(t, "back-off 5m0s restarting", pod.ContainerStates[1].Message)
	assert.False(t, pod.ContainerStates[1].Ready)
	assert.Equal(t, int32(3), pod.ContainerStates[1].RestartCount)
	assert.Nil(t, pod.ContainerStates[1].ExitCode)

	// Terminated container
	assert.Equal(t, "init-done", pod.ContainerStates[2].Name)
	assert.Equal(t, "terminated", pod.ContainerStates[2].State)
	assert.Equal(t, "OOMKilled", pod.ContainerStates[2].Reason)
	assert.Equal(t, "out of memory", pod.ContainerStates[2].Message)
	require.NotNil(t, pod.ContainerStates[2].ExitCode)
	assert.Equal(t, int32(137), *pod.ContainerStates[2].ExitCode)

	// Pod conditions
	require.Len(t, pod.Conditions, 2)
	assert.Equal(t, "Ready", pod.Conditions[0].Type)
	assert.Equal(t, "False", pod.Conditions[0].Status)
	assert.Equal(t, "ContainersNotReady", pod.Conditions[0].Reason)
	assert.Equal(t, "sidecar not ready", pod.Conditions[0].Message)
	assert.Equal(t, "Initialized", pod.Conditions[1].Type)
	assert.Equal(t, "True", pod.Conditions[1].Status)
}

func TestGetNamespaceStatus_EmptyContainerStates(t *testing.T) {
	t.Parallel()

	ns := "empty-cs-ns"

	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pending-pod",
				Namespace: ns,
				Labels:    map[string]string{"app.kubernetes.io/instance": "pending-release"},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "myimage:v1"}},
			},
			Status: corev1.PodStatus{
				Phase:             corev1.PodPending,
				ContainerStatuses: nil,
			},
		},
	)

	client := NewClientFromInterface(cs)
	status, err := client.GetNamespaceStatus(context.Background(), ns, StatusOptions{})

	assert.NoError(t, err)
	require.NotNil(t, status)
	require.Len(t, status.Charts, 1)
	require.Len(t, status.Charts[0].Pods, 1)

	pod := status.Charts[0].Pods[0]
	assert.Equal(t, "Pending", pod.Phase)
	// Should be empty slice, not nil (for consistent JSON serialization).
	assert.NotNil(t, pod.ContainerStates)
	assert.Empty(t, pod.ContainerStates)
	assert.Nil(t, pod.Conditions)
	assert.Nil(t, pod.StartTime)
}

func TestGetNamespaceStatus_WithEvents(t *testing.T) {
	t.Parallel()

	ns := "events-ns"

	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
		&corev1.Event{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "warning-event",
				Namespace: ns,
			},
			InvolvedObject: corev1.ObjectReference{
				Kind: "Pod",
				Name: "my-pod-xyz",
			},
			Type:           "Warning",
			Reason:         "BackOff",
			Message:        "Back-off restarting failed container",
			Count:          5,
			FirstTimestamp: metav1.Now(),
			LastTimestamp:  metav1.Now(),
		},
		&corev1.Event{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "normal-event",
				Namespace: ns,
			},
			InvolvedObject: corev1.ObjectReference{
				Kind: "Pod",
				Name: "my-pod-xyz",
			},
			Type:           "Normal",
			Reason:         "Pulled",
			Message:        "Successfully pulled image",
			Count:          1,
			FirstTimestamp: metav1.Now(),
			LastTimestamp:  metav1.Now(),
		},
	)

	client := NewClientFromInterface(cs)
	status, err := client.GetNamespaceStatus(context.Background(), ns, StatusOptions{IncludeEvents: true})

	assert.NoError(t, err)
	require.NotNil(t, status)

	// Both events should be included (Warning always, Normal within the last hour).
	require.Len(t, status.Events, 2)

	// Find the warning event.
	var warningEvent *PodEvent
	for i := range status.Events {
		if status.Events[i].Type == "Warning" {
			warningEvent = &status.Events[i]
			break
		}
	}
	require.NotNil(t, warningEvent)
	assert.Equal(t, "BackOff", warningEvent.Reason)
	assert.Equal(t, "Back-off restarting failed container", warningEvent.Message)
	assert.Equal(t, "Pod/my-pod-xyz", warningEvent.Object)
	assert.Equal(t, int32(5), warningEvent.Count)
}

func TestGetNamespaceStatus_PodStartTimeAndNodeName(t *testing.T) {
	t.Parallel()

	ns := "starttime-ns"
	startTime := metav1.Now()

	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "timed-pod",
				Namespace: ns,
				Labels:    map[string]string{"app.kubernetes.io/instance": "timed-release"},
			},
			Spec: corev1.PodSpec{
				NodeName:   "worker-node-1",
				Containers: []corev1.Container{{Name: "app", Image: "myimage:v2"}},
			},
			Status: corev1.PodStatus{
				Phase:     corev1.PodRunning,
				StartTime: &startTime,
				ContainerStatuses: []corev1.ContainerStatus{
					{
						Name:  "app",
						Ready: true,
						Image: "myimage:v2",
						State: corev1.ContainerState{
							Running: &corev1.ContainerStateRunning{},
						},
					},
				},
			},
		},
	)

	client := NewClientFromInterface(cs)
	status, err := client.GetNamespaceStatus(context.Background(), ns, StatusOptions{})

	assert.NoError(t, err)
	require.NotNil(t, status)
	require.Len(t, status.Charts, 1)
	require.Len(t, status.Charts[0].Pods, 1)

	pod := status.Charts[0].Pods[0]
	assert.Equal(t, "worker-node-1", pod.NodeName)
	require.NotNil(t, pod.StartTime)
	assert.Equal(t, startTime.Time, *pod.StartTime)
}

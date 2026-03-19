package k8s

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestGetNamespaceStatus_NotFound(t *testing.T) {
	t.Parallel()

	cs := fake.NewSimpleClientset()
	client := NewClientFromInterface(cs)

	status, err := client.GetNamespaceStatus(context.Background(), "nonexistent")
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

	status, err := client.GetNamespaceStatus(context.Background(), "empty-ns")
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
	status, err := client.GetNamespaceStatus(context.Background(), ns)

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
	status, err := client.GetNamespaceStatus(context.Background(), ns)

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
	status, err := client.GetNamespaceStatus(context.Background(), ns)

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
	status, err := client.GetNamespaceStatus(context.Background(), ns)

	assert.NoError(t, err)
	require.NotNil(t, status)
	assert.Equal(t, StatusError, status.Status)
	require.Len(t, status.Charts, 1)
	assert.Equal(t, StatusError, status.Charts[0].Status)
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
	status, err := client.GetNamespaceStatus(context.Background(), ns)

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
	status, err := client.GetNamespaceStatus(context.Background(), ns)

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
	status, err := client.GetNamespaceStatus(context.Background(), ns)

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

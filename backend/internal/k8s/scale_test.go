package k8s

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func newTestDeployment(namespace, name string, replicas int32, matchLabels map[string]string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			Generation: 1,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: matchLabels},
		},
	}
}

func TestScaleDeployment_UpdatesReplicas(t *testing.T) {
	t.Parallel()

	labels := map[string]string{"app": "kvk-core"}
	deploy := newTestDeployment("ns1", "kvk-core", 1, labels)
	cs := fake.NewSimpleClientset(deploy)
	c := NewClientFromInterface(cs)

	err := c.ScaleDeployment(context.Background(), "ns1", "kvk-core", 0)
	require.NoError(t, err)

	got, err := cs.AppsV1().Deployments("ns1").Get(context.Background(), "kvk-core", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, got.Spec.Replicas)
	assert.Equal(t, int32(0), *got.Spec.Replicas)
}

func TestScaleDeployment_NoOpWhenAlreadyAtTarget(t *testing.T) {
	t.Parallel()

	labels := map[string]string{"app": "kvk-core"}
	deploy := newTestDeployment("ns1", "kvk-core", 0, labels)
	cs := fake.NewSimpleClientset(deploy)
	c := NewClientFromInterface(cs)

	err := c.ScaleDeployment(context.Background(), "ns1", "kvk-core", 0)
	assert.NoError(t, err)
}

func TestScaleDeployment_NotFound(t *testing.T) {
	t.Parallel()

	cs := fake.NewSimpleClientset()
	c := NewClientFromInterface(cs)

	err := c.ScaleDeployment(context.Background(), "ns1", "missing", 0)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrDeploymentNotFound))
}

func TestWaitForDeploymentPodsGone_NoPods(t *testing.T) {
	t.Parallel()

	labels := map[string]string{"app": "kvk-mysql"}
	deploy := newTestDeployment("ns2", "kvk-mysql", 0, labels)
	cs := fake.NewSimpleClientset(deploy)
	c := NewClientFromInterface(cs)

	err := c.WaitForDeploymentPodsGone(context.Background(), "ns2", "kvk-mysql", 2*time.Second)
	assert.NoError(t, err)
}

func TestWaitForDeploymentPodsGone_MissingDeployment(t *testing.T) {
	t.Parallel()

	cs := fake.NewSimpleClientset()
	c := NewClientFromInterface(cs)

	// No deployment => nothing to wait for => no error.
	err := c.WaitForDeploymentPodsGone(context.Background(), "ns", "ghost", time.Second)
	assert.NoError(t, err)
}

func TestWaitForDeploymentPodsGone_Timeout(t *testing.T) {
	t.Parallel()

	labels := map[string]string{"app": "kvk-mysql"}
	deploy := newTestDeployment("ns3", "kvk-mysql", 1, labels)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kvk-mysql-abc",
			Namespace: "ns3",
			Labels:    labels,
		},
	}
	cs := fake.NewSimpleClientset(deploy, pod)
	c := NewClientFromInterface(cs)

	err := c.WaitForDeploymentPodsGone(context.Background(), "ns3", "kvk-mysql", 100*time.Millisecond)
	assert.Error(t, err)
}

func TestWaitForDeploymentAvailable_Success(t *testing.T) {
	t.Parallel()

	labels := map[string]string{"app": "kvk-mysql"}
	deploy := newTestDeployment("ns4", "kvk-mysql", 1, labels)
	deploy.Status.ObservedGeneration = 1
	deploy.Status.Conditions = []appsv1.DeploymentCondition{{
		Type:   appsv1.DeploymentAvailable,
		Status: corev1.ConditionTrue,
	}}
	cs := fake.NewSimpleClientset(deploy)
	c := NewClientFromInterface(cs)

	err := c.WaitForDeploymentAvailable(context.Background(), "ns4", "kvk-mysql", 2*time.Second)
	assert.NoError(t, err)
}

func TestWaitForDeploymentAvailable_TimeoutWhenUnobserved(t *testing.T) {
	t.Parallel()

	labels := map[string]string{"app": "kvk-mysql"}
	deploy := newTestDeployment("ns5", "kvk-mysql", 1, labels)
	deploy.Status.ObservedGeneration = 0 // controller hasn't caught up
	cs := fake.NewSimpleClientset(deploy)
	c := NewClientFromInterface(cs)

	err := c.WaitForDeploymentAvailable(context.Background(), "ns5", "kvk-mysql", 100*time.Millisecond)
	assert.Error(t, err)
}

func TestRunPVCCleanupJob_CompletesAndDeletes(t *testing.T) {
	t.Parallel()

	cs := fake.NewSimpleClientset()

	// fake.Clientset does not run the batch controller, so we simulate it:
	// after RunPVCCleanupJob creates the Job, update its status to Complete
	// from a background goroutine. The poller in RunPVCCleanupJob will then
	// observe completion and return.
	done := make(chan struct{})
	go func() {
		defer close(done)
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			j, err := cs.BatchV1().Jobs("ns-pvc").Get(context.Background(), "kvk-mysql-pvc-cleanup", metav1.GetOptions{})
			if err == nil {
				j.Status.Conditions = []batchv1.JobCondition{{
					Type:   batchv1.JobComplete,
					Status: corev1.ConditionTrue,
				}}
				_, _ = cs.BatchV1().Jobs("ns-pvc").UpdateStatus(context.Background(), j, metav1.UpdateOptions{})
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()

	c := NewClientFromInterface(cs)
	err := c.RunPVCCleanupJob(context.Background(), PVCCleanupJobRequest{
		Namespace: "ns-pvc",
		JobName:   "kvk-mysql-pvc-cleanup",
		PVCName:   "kvk-mysql-data",
		Image:     "alpine:3.20",
		Timeout:   3 * time.Second,
	})
	<-done
	assert.NoError(t, err)

	// Job should have been deleted after completion.
	_, err = cs.BatchV1().Jobs("ns-pvc").Get(context.Background(), "kvk-mysql-pvc-cleanup", metav1.GetOptions{})
	assert.Error(t, err, "expected cleanup Job to have been deleted after completion")
}

func TestRunPVCCleanupJob_ValidatesInput(t *testing.T) {
	t.Parallel()

	cs := fake.NewSimpleClientset()
	c := NewClientFromInterface(cs)

	err := c.RunPVCCleanupJob(context.Background(), PVCCleanupJobRequest{
		Namespace: "",
		JobName:   "j",
		PVCName:   "p",
		Image:     "i",
	})
	assert.Error(t, err)
}

func TestDeleteJob_NotFoundIsNoOp(t *testing.T) {
	t.Parallel()

	cs := fake.NewSimpleClientset()
	c := NewClientFromInterface(cs)

	err := c.DeleteJob(context.Background(), "ns", "nope")
	assert.NoError(t, err)
}

func TestDeleteJob_DeletesExisting(t *testing.T) {
	t.Parallel()

	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "j1", Namespace: "ns"}}
	cs := fake.NewSimpleClientset(job)
	c := NewClientFromInterface(cs)

	err := c.DeleteJob(context.Background(), "ns", "j1")
	assert.NoError(t, err)

	_, err = cs.BatchV1().Jobs("ns").Get(context.Background(), "j1", metav1.GetOptions{})
	assert.Error(t, err)
}

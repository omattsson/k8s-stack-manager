package deployer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/k8s"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
)

const testQuotaMessage = "Error creating: pods \"sync-abc\" is forbidden: exceeded quota: stack-manager-quota, requested: limits.cpu=200m, used: limits.cpu=3990m, limited: limits.cpu=4"

func problem(key, reason, object, msg string) k8s.PodProblem {
	return k8s.PodProblem{Key: key, Reason: reason, Object: object, Message: msg}
}

func TestPodEventWatch_Record(t *testing.T) {
	t.Parallel()

	manyProblems := func(n int) []k8s.PodProblem {
		out := make([]k8s.PodProblem, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, problem(fmt.Sprintf("k%d", i), "FailedScheduling", fmt.Sprintf("pod/p%d", i), "0/3 nodes are available"))
		}
		return out
	}

	tests := []struct {
		name      string
		polls     [][]k8s.PodProblem
		wantLines int
		wantBest  string
		check     func(t *testing.T, lines []string)
	}{
		{
			name: "duplicates are written once",
			polls: [][]k8s.PodProblem{
				{problem("a", "FailedCreate", "job/sync", testQuotaMessage)},
				{problem("a", "FailedCreate", "job/sync", testQuotaMessage)},
			},
			wantLines: 1,
			wantBest:  "FailedCreate job/sync: " + testQuotaMessage,
		},
		{
			name: "quota problem stays the most relevant",
			polls: [][]k8s.PodProblem{
				{problem("a", "FailedCreate", "job/sync", testQuotaMessage)},
				{problem("b", "ImagePullBackOff", "pod/web-1 container app", "Back-off pulling image")},
			},
			wantLines: 2,
			wantBest:  "FailedCreate job/sync: " + testQuotaMessage,
		},
		{
			name: "a later problem with a higher score replaces an earlier one",
			polls: [][]k8s.PodProblem{
				{problem("b", "CrashLoopBackOff", "pod/web-1 container app", "back-off 10s")},
				{problem("c", "FailedScheduling", "pod/web-2", "0/3 nodes are available")},
			},
			wantLines: 2,
			wantBest:  "FailedScheduling pod/web-2: 0/3 nodes are available",
		},
		{
			name:      "one poll writes at most maxPodEventLinesPerPoll lines, the next poll the rest",
			polls:     [][]k8s.PodProblem{manyProblems(8), manyProblems(8)},
			wantLines: 8,
		},
		{
			name: "the operation writes at most maxPodEventLines lines and one notice",
			polls: func() [][]k8s.PodProblem {
				var polls [][]k8s.PodProblem
				for i := 0; i < 20; i++ {
					polls = append(polls, manyProblems(60))
				}
				return polls
			}(),
			wantLines: maxPodEventLines + 1,
			check: func(t *testing.T, lines []string) {
				assert.Equal(t, "Pod events: more problems are not shown", lines[len(lines)-1])
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var emitted []string
			w := &podEventWatch{seen: map[string]struct{}{}, emit: func(l string) {
				mu.Lock()
				emitted = append(emitted, l)
				mu.Unlock()
			}}
			for _, p := range tt.polls {
				w.record(p)
			}
			drained := strings.Split(strings.TrimSuffix(w.drain(), "\n"), "\n")
			assert.Len(t, emitted, tt.wantLines)
			assert.Equal(t, emitted, drained, "stored lines and streamed lines must match")
			assert.Empty(t, w.drain(), "drain empties the pending lines")
			if tt.wantBest != "" {
				assert.Equal(t, tt.wantBest, w.best)
			}
			if tt.check != nil {
				tt.check(t, emitted)
			}
		})
	}
}

func TestFormatPodProblem(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", maxPodEventMessageLen+50)
	tests := []struct {
		name string
		in   k8s.PodProblem
		want string
	}{
		{"message", problem("k", "FailedCreate", "job/sync", "line one\n  line two"), "FailedCreate job/sync: line one line two"},
		{"no message", problem("k", "CrashLoopBackOff", "pod/a container b", ""), "CrashLoopBackOff pod/a container b"},
		{"long message is cut", problem("k", "Failed", "pod/a", long), "Failed pod/a: " + long[:maxPodEventMessageLen] + "..."},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, formatPodProblem(tt.in))
		})
	}
}

func TestWithPodEvent_Sanitize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		err   error
		event string
		want  string
	}{
		{"nil error stays nil", nil, "x", ""},
		{"no event keeps the error", errors.New("all charts failed to deploy: web"), "", "all charts failed to deploy: web"},
		{"event is added to a chart error", errors.New("all charts failed to deploy: web"), "FailedCreate job/sync: exceeded quota", "all charts failed to deploy: web (pod event: FailedCreate job/sync: exceeded quota)"},
		{"internal error stays generic", errors.New("rolling back chart \"web\" to revision 2: exec: helm failed at /tmp/x"), "BackoffLimitExceeded job/sync: Job has reached the specified backoff limit", "rolling back chart \"web\" to revision 2: operation failed (pod event: BackoffLimitExceeded job/sync: Job has reached the specified backoff limit)"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := withPodEvent(tt.err, tt.event)
			if tt.err == nil {
				assert.NoError(t, got)
				return
			}
			assert.ErrorIs(t, got, tt.err)
			assert.Equal(t, tt.want, sanitizeDeployError(got))
		})
	}
}

// deployLogLines returns the deployment.log lines of the broadcasts.
func deployLogLines(hub *mockBroadcaster) []string {
	var lines []string
	for _, msg := range hub.getMessages() {
		var env struct {
			Type    string `json:"type"`
			Payload struct {
				Line string `json:"line"`
			} `json:"payload"`
		}
		if json.Unmarshal(msg, &env) == nil && env.Type == "deployment.log" {
			lines = append(lines, env.Payload.Line)
		}
	}
	return lines
}

func countContaining(lines []string, sub string) int {
	n := 0
	for _, l := range lines {
		if strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

// TestManager_Deploy_ReportsQuotaEvent: a Helm hook job cannot create its
// pod (exceeded ResourceQuota) and Helm fails after a wait. The quota event
// shows in the stream while Helm waits, once in the stored log, and in the
// instance error message.
func TestManager_Deploy_ReportsQuotaEvent(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}

	inst := &models.StackInstance{
		ID: "inst-quota-event", StackDefinitionID: "def-1", Name: "quota-event",
		Namespace: "stack-quota-event", OwnerID: "user-1", Branch: "main",
		Status: models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	cs := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: inst.Namespace}})
	k8sClient := k8s.NewClientFromInterface(cs)

	streamedWhileHelmRuns := make(chan bool, 1)
	helmMock := &mockHelmExecutor{
		installFunc: func(ctx context.Context, req InstallRequest) (string, error) {
			_, err := cs.CoreV1().Events(req.Namespace).Create(ctx, &corev1.Event{
				ObjectMeta:     metav1.ObjectMeta{Name: "sync.1", Namespace: req.Namespace},
				Type:           corev1.EventTypeWarning,
				Reason:         "FailedCreate",
				Message:        testQuotaMessage,
				InvolvedObject: corev1.ObjectReference{Kind: "Job", Name: "sync"},
				LastTimestamp:  metav1.NewTime(time.Now().UTC()),
			}, metav1.CreateOptions{})
			if err != nil {
				return "", err
			}
			// Helm waits for the hook job; the watch polls meanwhile.
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) && countContaining(deployLogLines(hub), "exceeded quota") == 0 {
				time.Sleep(5 * time.Millisecond)
			}
			streamedWhileHelmRuns <- countContaining(deployLogLines(hub), "exceeded quota") > 0
			time.Sleep(50 * time.Millisecond) // more polls: the event must stay de-duplicated
			return "Error: UPGRADE FAILED: post-upgrade hooks failed: job sync failed: DeadlineExceeded",
				errors.New("helm command failed: exit status 1")
		},
	}

	mgr := NewManager(ManagerConfig{
		Registry:             &mockClusterResolver{helm: helmMock, k8sClient: k8sClient},
		InstanceRepo:         instanceRepo,
		DeployLogRepo:        logRepo,
		TxRunner:             &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:                  hub,
		MaxConcurrent:        2,
		PodEventPollInterval: 5 * time.Millisecond,
	})

	logID, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1", Name: "test-def"},
		Charts:     []ChartDeployInfo{{ChartConfig: models.ChartConfig{ChartName: "storefront", DeployOrder: 1}}},
	})
	require.NoError(t, err)
	waitForTerminalStatus(t, instanceRepo, inst.ID)

	assert.True(t, <-streamedWhileHelmRuns, "the quota event must stream while Helm waits")
	assert.Equal(t, 1, countContaining(deployLogLines(hub), "Pod event: FailedCreate job/sync"), "streamed once")

	final, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusError, final.Status)
	assert.Contains(t, final.ErrorMessage, "all charts failed to deploy: storefront")
	assert.Contains(t, final.ErrorMessage, "exceeded quota: stack-manager-quota")

	finalLog, err := logRepo.FindByID(context.Background(), logID)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(finalLog.Output, "Pod event: FailedCreate job/sync"), "stored once")
	assert.Contains(t, finalLog.ErrorMessage, "exceeded quota")
}

// TestManager_Deploy_NoPodEvents_NoChange: without pod problems the deploy
// output and the error message do not change.
func TestManager_Deploy_NoPodEvents_NoChange(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	inst := &models.StackInstance{
		ID: "inst-no-events", StackDefinitionID: "def-1", Name: "no-events",
		Namespace: "stack-no-events", OwnerID: "user-1", Status: models.StackStatusDraft,
	}
	require.NoError(t, instanceRepo.Create(inst))

	mgr := NewManager(ManagerConfig{
		Registry:             &mockClusterResolver{helm: &mockHelmExecutor{}},
		InstanceRepo:         instanceRepo,
		DeployLogRepo:        logRepo,
		TxRunner:             &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:                  &mockBroadcaster{},
		MaxConcurrent:        2,
		PodEventPollInterval: 5 * time.Millisecond,
	})
	logID, err := mgr.Deploy(context.Background(), DeployRequest{
		Instance:   inst,
		Definition: &models.StackDefinition{ID: "def-1"},
		Charts:     []ChartDeployInfo{{ChartConfig: models.ChartConfig{ChartName: "web", DeployOrder: 1}}},
	})
	require.NoError(t, err)
	waitForTerminalStatus(t, instanceRepo, inst.ID)

	final, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusRunning, final.Status)
	finalLog, err := logRepo.FindByID(context.Background(), logID)
	require.NoError(t, err)
	assert.NotContains(t, finalLog.Output, "Pod event")
}

// TestManager_Rollback_ReportsPodEvent: a rollback that fails while a
// container cannot pull its image names the image problem.
func TestManager_Rollback_ReportsPodEvent(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	hub := &mockBroadcaster{}
	inst := &models.StackInstance{
		ID: "inst-rb-event", StackDefinitionID: "def-1", Name: "rb-event",
		Namespace: "stack-rb-event", OwnerID: "user-1", Status: models.StackStatusRunning,
	}
	require.NoError(t, instanceRepo.Create(inst))

	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: inst.Namespace}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: inst.Namespace},
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "app",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ErrImagePull", Message: "manifest unknown"}},
			}}},
		},
	)

	helmMock := &mockHelmExecutor{
		historyFunc: func(_ context.Context, _ string, _ string, _ int) ([]ReleaseRevision, error) {
			return []ReleaseRevision{{Revision: 1, Status: "deployed"}, {Revision: 2, Status: "deployed"}}, nil
		},
		rollbackFunc: func(_ context.Context, _ string, _ string, _ int) (string, error) {
			return "Error: timed out waiting for the condition", errors.New("helm command failed: exit status 1")
		},
	}
	mgr := NewManager(ManagerConfig{
		Registry:             &mockClusterResolver{helm: helmMock, k8sClient: k8s.NewClientFromInterface(cs)},
		InstanceRepo:         instanceRepo,
		DeployLogRepo:        logRepo,
		TxRunner:             &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:                  hub,
		MaxConcurrent:        2,
		PodEventPollInterval: 5 * time.Millisecond,
	})

	logID, err := mgr.Rollback(context.Background(), RollbackRequest{
		Instance: inst,
		Charts:   []ChartDeployInfo{{ChartConfig: models.ChartConfig{ChartName: "web", DeployOrder: 1}}},
	})
	require.NoError(t, err)
	waitForTerminalStatus(t, instanceRepo, inst.ID)

	final, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusError, final.Status)
	assert.Contains(t, final.ErrorMessage, "pod event: ErrImagePull pod/web-1 container app: manifest unknown")

	finalLog, err := logRepo.FindByID(context.Background(), logID)
	require.NoError(t, err)
	assert.Contains(t, finalLog.Output, "Pod event: ErrImagePull pod/web-1 container app")
	assert.Equal(t, 1, countContaining(deployLogLines(hub), "Pod event: ErrImagePull"))
}

func TestPodEventWatch_LogPollError(t *testing.T) {
	t.Parallel()

	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "events"}, "", errors.New("no list"))
	tests := []struct {
		name          string
		errs          []error
		wantForbidden bool
	}{
		{"forbidden is remembered once", []error{forbidden, forbidden, forbidden}, true},
		{"other errors are not forbidden", []error{errors.New("timeout"), context.Canceled}, false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := &podEventWatch{namespace: "ns", seen: map[string]struct{}{}}
			for _, err := range tt.errs {
				w.logPollError(err)
			}
			assert.Equal(t, tt.wantForbidden, w.forbiddenLogged)
		})
	}
}

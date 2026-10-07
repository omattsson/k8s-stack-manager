package deployer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func releaseSecret(name, namespace string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
}

func TestRecoverPendingRelease(t *testing.T) {
	t.Parallel()

	const ns = "stack-demo"
	const release = "app-core"

	tests := []struct {
		name          string
		history       []ReleaseRevision
		historyErr    error
		deleteErr     error
		wantMsg       string
		wantErr       bool
		wantRemaining []string
	}{
		{
			name:          "pending-upgrade removes only the stuck revision",
			history:       []ReleaseRevision{{Revision: 3, Status: "pending-upgrade"}},
			wantMsg:       `revision 3 was stuck in pending-upgrade`,
			wantRemaining: []string{"sh.helm.release.v1.app-core.v1", "sh.helm.release.v1.app-core.v2"},
		},
		{
			name:          "pending-install removes revision 1 and keeps other secrets",
			history:       []ReleaseRevision{{Revision: 1, Status: "pending-install"}},
			wantMsg:       `revision 1 was stuck in pending-install`,
			wantRemaining: []string{"sh.helm.release.v1.app-core.v2", "sh.helm.release.v1.app-core.v3"},
		},
		{
			name:          "pending-rollback is recovered",
			history:       []ReleaseRevision{{Revision: 2, Status: "pending-rollback"}},
			wantMsg:       `revision 2 was stuck in pending-rollback`,
			wantRemaining: []string{"sh.helm.release.v1.app-core.v1", "sh.helm.release.v1.app-core.v3"},
		},
		{
			name:          "deployed release is left alone",
			history:       []ReleaseRevision{{Revision: 3, Status: "deployed"}},
			wantRemaining: []string{"sh.helm.release.v1.app-core.v1", "sh.helm.release.v1.app-core.v2", "sh.helm.release.v1.app-core.v3"},
		},
		{
			name:          "failed release is left alone",
			history:       []ReleaseRevision{{Revision: 3, Status: "failed"}},
			wantRemaining: []string{"sh.helm.release.v1.app-core.v1", "sh.helm.release.v1.app-core.v2", "sh.helm.release.v1.app-core.v3"},
		},
		{
			name:          "missing release is a no-op",
			historyErr:    errors.New("release: not found"),
			wantRemaining: []string{"sh.helm.release.v1.app-core.v1", "sh.helm.release.v1.app-core.v2", "sh.helm.release.v1.app-core.v3"},
		},
		{
			name:      "delete failure is returned",
			history:   []ReleaseRevision{{Revision: 3, Status: "pending-upgrade"}},
			deleteErr: errors.New("forbidden"),
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cs := fake.NewSimpleClientset(
				releaseSecret("sh.helm.release.v1.app-core.v1", ns),
				releaseSecret("sh.helm.release.v1.app-core.v2", ns),
				releaseSecret("sh.helm.release.v1.app-core.v3", ns),
			)
			if tt.deleteErr != nil {
				cs.PrependReactor("delete", "secrets", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
					return true, nil, tt.deleteErr
				})
			}
			helm := &mockHelmExecutor{
				historyFunc: func(_ context.Context, name, namespace string, max int) ([]ReleaseRevision, error) {
					assert.Equal(t, release, name)
					assert.Equal(t, ns, namespace)
					assert.Equal(t, 1, max)
					return tt.history, tt.historyErr
				},
			}

			msg, err := recoverPendingRelease(context.Background(), helm, cs, release, ns)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tt.wantMsg == "" {
				assert.Empty(t, msg)
			} else {
				assert.Contains(t, msg, tt.wantMsg)
			}

			list, err := cs.CoreV1().Secrets(ns).List(context.Background(), metav1.ListOptions{})
			require.NoError(t, err)
			var names []string
			for _, s := range list.Items {
				names = append(names, s.Name)
			}
			assert.ElementsMatch(t, tt.wantRemaining, names)
		})
	}
}

func TestRecoverPendingRelease_NilClientset(t *testing.T) {
	t.Parallel()

	called := false
	helm := &mockHelmExecutor{
		historyFunc: func(context.Context, string, string, int) ([]ReleaseRevision, error) {
			called = true
			return nil, nil
		},
	}
	msg, err := recoverPendingRelease(context.Background(), helm, nil, "app-core", "ns")
	require.NoError(t, err)
	assert.Empty(t, msg)
	assert.False(t, called, "no helm call without a clientset")
}

func TestDeployBudget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		helm   time.Duration
		charts int
		want   time.Duration
	}{
		{name: "one chart", helm: 10 * time.Minute, charts: 1, want: 11 * time.Minute},
		{name: "eight charts", helm: 10 * time.Minute, charts: 8, want: 81 * time.Minute},
		{name: "zero charts counts as one", helm: 5 * time.Minute, charts: 0, want: 6 * time.Minute},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, deployBudget(tt.helm, tt.charts))
		})
	}
}

// TestHelmClient_CancelSendsSIGTERM checks that a cancelled context stops helm
// with SIGTERM, so helm can mark the release failed instead of pending-*.
func TestHelmClient_CancelSendsSIGTERM(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM is not available on Windows")
	}

	dir := t.TempDir()
	marker := filepath.Join(dir, "terminated")
	ready := filepath.Join(dir, "ready")
	script := filepath.Join(dir, "fake-helm")
	body := "#!/bin/sh\ntrap 'echo sigterm > " + marker + "; exit 1' TERM\ntouch " + ready + "\nwhile :; do sleep 0.1; done\n"
	require.NoError(t, os.WriteFile(script, []byte(body), 0o700)) //nolint:gosec // test helper script

	h := NewHelmClient(script, "", time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancel only after the trap is installed; a fixed timeout is flaky under load.
	go func() {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(ready); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()

	start := time.Now()
	_, err := h.run(ctx, []string{"upgrade"})
	require.Error(t, err)
	assert.Less(t, time.Since(start), helmTerminateGrace, "helm must exit on SIGTERM, not wait for the kill")

	data, readErr := os.ReadFile(marker) //nolint:gosec // path from t.TempDir
	require.NoError(t, readErr, "the fake helm did not receive SIGTERM")
	assert.Equal(t, "sigterm", strings.TrimSpace(string(data)))
}

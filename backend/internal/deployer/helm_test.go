package deployer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewHelmClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		binary        string
		kubeconfig    string
		timeout       time.Duration
		expectBinary  string
		expectTimeout time.Duration
	}{
		{
			name:          "default binary and timeout",
			binary:        "",
			kubeconfig:    "",
			timeout:       0,
			expectBinary:  "helm",
			expectTimeout: 5 * time.Minute,
		},
		{
			name:          "explicit binary",
			binary:        "helm",
			kubeconfig:    "",
			timeout:       10 * time.Minute,
			expectBinary:  "helm",
			expectTimeout: 10 * time.Minute,
		},
		{
			name:          "custom binary and kubeconfig",
			binary:        "/usr/local/bin/helm",
			kubeconfig:    "/home/user/.kube/config",
			timeout:       5 * time.Minute,
			expectBinary:  "/usr/local/bin/helm",
			expectTimeout: 5 * time.Minute,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := NewHelmClient(tt.binary, tt.kubeconfig, tt.timeout)
			assert.NotNil(t, client)
			assert.Equal(t, tt.expectBinary, client.binaryPath)
			assert.Equal(t, tt.kubeconfig, client.kubeconfig)
			assert.Equal(t, tt.expectTimeout, client.timeout)
		})
	}
}

func TestHelmClient_Install_InvalidBinary(t *testing.T) {
	t.Parallel()

	client := NewHelmClient("/nonexistent/helm", "", 1*time.Minute)

	output, err := client.Install(context.Background(), InstallRequest{
		ReleaseName: "test-release",
		ChartPath:   "oci://example.com/charts/nginx",
		Namespace:   "test-ns",
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "helm command failed")
	// Output may be empty since the binary doesn't exist.
	_ = output
}

func TestHelmClient_Install_WithValuesFile(t *testing.T) {
	t.Parallel()

	// This tests that Install doesn't panic with a values file specified,
	// even though the binary doesn't exist.
	client := NewHelmClient("/nonexistent/helm", "", 1*time.Minute)

	_, err := client.Install(context.Background(), InstallRequest{
		ReleaseName: "test-release",
		ChartPath:   "oci://example.com/charts/nginx",
		ValuesFile:  "/tmp/values.yaml",
		Namespace:   "test-ns",
	})

	assert.Error(t, err)
}

func TestHelmClient_Uninstall_InvalidBinary(t *testing.T) {
	t.Parallel()

	client := NewHelmClient("/nonexistent/helm", "", 1*time.Minute)

	output, err := client.Uninstall(context.Background(), UninstallRequest{
		ReleaseName: "test-release",
		Namespace:   "test-ns",
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "helm command failed")
	_ = output
}

func TestHelmClient_Status_InvalidBinary(t *testing.T) {
	t.Parallel()

	client := NewHelmClient("/nonexistent/helm", "", 1*time.Minute)

	status, err := client.Status(context.Background(), "my-release", "test-ns")
	assert.Error(t, err)
	assert.Nil(t, status)
	assert.Contains(t, err.Error(), "helm status")
}

func TestHelmClient_Status_InvalidJSON(t *testing.T) {
	t.Parallel()

	// Use "echo" as the helm binary to return invalid JSON.
	client := NewHelmClient("echo", "", 1*time.Minute)

	status, err := client.Status(context.Background(), "my-release", "test-ns")
	assert.Error(t, err)
	assert.Nil(t, status)
	assert.Contains(t, err.Error(), "parsing helm status output")
}

func TestHelmClient_Install_ContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	// Use "sleep" as the binary to simulate a long-running command.
	client := NewHelmClient("sleep", "", 1*time.Minute)

	_, err := client.Install(ctx, InstallRequest{
		ReleaseName: "test-release",
		ChartPath:   "oci://example.com/charts/nginx",
		Namespace:   "test-ns",
	})

	assert.Error(t, err)
}

func TestValidatePositionalArg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		argName string
		value   string
		wantErr bool
	}{
		{"valid release name", "release name", "my-release", false},
		{"valid chart path", "chart path", "oci://registry/chart", false},
		{"valid namespace", "namespace", "test-ns", false},
		{"single dash prefix", "release name", "-malicious", true},
		{"double dash prefix", "release name", "--kubeconfig=/etc/secret", true},
		{"flag injection via chart path", "chart path", "--post-renderer=evil", true},
		{"flag injection via namespace", "namespace", "--namespace=default", true},
		{"empty string is valid", "release name", "", false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validatePositionalArg(tt.argName, tt.value)
			if tt.wantErr {
				assert.Error(t, err)
				assert.True(t, errors.Is(err, errArgDashPrefix))
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestHelmClient_Install_ArgumentInjection(t *testing.T) {
	t.Parallel()

	client := NewHelmClient("helm", "", 1*time.Minute)

	tests := []struct {
		name string
		req  InstallRequest
	}{
		{
			name: "release name with flag prefix",
			req: InstallRequest{
				ReleaseName: "--kubeconfig=/etc/secret",
				ChartPath:   "nginx",
				Namespace:   "test-ns",
			},
		},
		{
			name: "chart path with flag prefix",
			req: InstallRequest{
				ReleaseName: "my-release",
				ChartPath:   "--post-renderer=evil",
				Namespace:   "test-ns",
			},
		},
		{
			name: "namespace with flag prefix",
			req: InstallRequest{
				ReleaseName: "my-release",
				ChartPath:   "nginx",
				Namespace:   "--namespace=default",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := client.Install(context.Background(), tt.req)
			assert.Error(t, err)
			assert.True(t, errors.Is(err, errArgDashPrefix))
		})
	}
}

func TestHelmClient_Uninstall_ArgumentInjection(t *testing.T) {
	t.Parallel()

	client := NewHelmClient("helm", "", 1*time.Minute)

	_, err := client.Uninstall(context.Background(), UninstallRequest{
		ReleaseName: "--kubeconfig=/etc/secret",
		Namespace:   "test-ns",
	})
	assert.Error(t, err)
	assert.True(t, errors.Is(err, errArgDashPrefix))
}

func TestHelmClient_Status_ArgumentInjection(t *testing.T) {
	t.Parallel()

	client := NewHelmClient("helm", "", 1*time.Minute)

	_, err := client.Status(context.Background(), "--kubeconfig=/etc/secret", "test-ns")
	assert.Error(t, err)
	assert.True(t, errors.Is(err, errArgDashPrefix))
}

// ---- History tests ----

func TestHelmClient_History(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		binary  string
		release string
		ns      string
		max     int
		wantErr bool
		errMsg  string
	}{
		{
			name:    "invalid binary returns error",
			binary:  "/nonexistent/helm",
			release: "my-release",
			ns:      "test-ns",
			max:     10,
			wantErr: true,
			errMsg:  "helm history",
		},
		{
			name:    "invalid JSON output returns parse error",
			binary:  "echo",
			release: "my-release",
			ns:      "test-ns",
			max:     10,
			wantErr: true,
			errMsg:  "parsing helm history output",
		},
		{
			name:    "zero max uses default 256",
			binary:  "echo",
			release: "my-release",
			ns:      "test-ns",
			max:     0, // should default to 256
			wantErr: true,
			errMsg:  "parsing helm history output",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := NewHelmClient(tt.binary, "", 1*time.Minute)

			revisions, err := client.History(context.Background(), tt.release, tt.ns, tt.max)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
				assert.Nil(t, revisions)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestHelmClient_History_ArgumentInjection(t *testing.T) {
	t.Parallel()

	client := NewHelmClient("helm", "", 1*time.Minute)

	tests := []struct {
		name    string
		release string
	}{
		{
			name:    "release name with single dash prefix",
			release: "-malicious",
		},
		{
			name:    "release name with double dash prefix",
			release: "--kubeconfig=/etc/secret",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := client.History(context.Background(), tt.release, "test-ns", 10)
			assert.Error(t, err)
			assert.True(t, errors.Is(err, errArgDashPrefix))
		})
	}
}

// ---- Rollback tests ----

func TestHelmClient_Rollback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		binary   string
		release  string
		ns       string
		revision int
		wantErr  bool
		errMsg   string
	}{
		{
			name:     "invalid binary returns error",
			binary:   "/nonexistent/helm",
			release:  "my-release",
			ns:       "test-ns",
			revision: 1,
			wantErr:  true,
			errMsg:   "helm command failed",
		},
		{
			name:     "revision 0 rolls back to previous",
			binary:   "/nonexistent/helm",
			release:  "my-release",
			ns:       "test-ns",
			revision: 0,
			wantErr:  true,
			errMsg:   "helm command failed",
		},
		{
			name:     "specific revision",
			binary:   "/nonexistent/helm",
			release:  "my-release",
			ns:       "test-ns",
			revision: 3,
			wantErr:  true,
			errMsg:   "helm command failed",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := NewHelmClient(tt.binary, "", 1*time.Minute)

			output, err := client.Rollback(context.Background(), tt.release, tt.ns, tt.revision)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
				_ = output
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestHelmClient_Rollback_ArgumentInjection(t *testing.T) {
	t.Parallel()

	client := NewHelmClient("helm", "", 1*time.Minute)

	tests := []struct {
		name    string
		release string
	}{
		{
			name:    "release name with single dash prefix",
			release: "-malicious",
		},
		{
			name:    "release name with flag injection",
			release: "--post-renderer=evil",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := client.Rollback(context.Background(), tt.release, "test-ns", 1)
			assert.Error(t, err)
			assert.True(t, errors.Is(err, errArgDashPrefix))
		})
	}
}

// ---- GetValues tests ----

func TestHelmClient_GetValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		binary   string
		release  string
		ns       string
		revision int
		wantErr  bool
		errMsg   string
	}{
		{
			name:     "invalid binary returns error",
			binary:   "/nonexistent/helm",
			release:  "my-release",
			ns:       "test-ns",
			revision: 1,
			wantErr:  true,
			errMsg:   "helm get values",
		},
		{
			name:     "revision 0 omits --revision flag",
			binary:   "/nonexistent/helm",
			release:  "my-release",
			ns:       "test-ns",
			revision: 0, // no --revision flag added
			wantErr:  true,
			errMsg:   "helm get values",
		},
		{
			name:     "specific revision passes --revision flag",
			binary:   "/nonexistent/helm",
			release:  "my-release",
			ns:       "test-ns",
			revision: 5,
			wantErr:  true,
			errMsg:   "helm get values",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := NewHelmClient(tt.binary, "", 1*time.Minute)

			output, err := client.GetValues(context.Background(), tt.release, tt.ns, tt.revision)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
				assert.Empty(t, output)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestHelmClient_GetValues_ArgumentInjection(t *testing.T) {
	t.Parallel()

	client := NewHelmClient("helm", "", 1*time.Minute)

	tests := []struct {
		name    string
		release string
	}{
		{
			name:    "release name with single dash prefix",
			release: "-malicious",
		},
		{
			name:    "release name with flag injection",
			release: "--kubeconfig=/etc/secret",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := client.GetValues(context.Background(), tt.release, "test-ns", 0)
			assert.Error(t, err)
			assert.True(t, errors.Is(err, errArgDashPrefix))
		})
	}
}

func TestHelmClient_GetValues_RevisionZeroOmitsFlag(t *testing.T) {
	t.Parallel()

	// Use "echo" as the binary: it prints its args to stdout and exits 0.
	// When revision is 0, "--revision" should NOT appear in the args.
	// When revision > 0, "--revision" should appear.
	// Because echo exits 0 the output will just be the args — we can inspect whether
	// --revision is present to verify branching behaviour.
	client := NewHelmClient("echo", "", 1*time.Minute)

	t.Run("revision 0 — no --revision flag in output", func(t *testing.T) {
		t.Parallel()
		output, err := client.GetValues(context.Background(), "my-release", "test-ns", 0)
		assert.NoError(t, err)
		assert.NotContains(t, output, "--revision")
	})

	t.Run("revision 5 — --revision flag appears in output", func(t *testing.T) {
		t.Parallel()
		output, err := client.GetValues(context.Background(), "my-release", "test-ns", 5)
		assert.NoError(t, err)
		assert.Contains(t, output, "--revision")
	})
}

// ---- Streaming & arg builder tests ----

// Compile-time interface compliance check: HelmClient must implement StreamingHelmExecutor.
var _ StreamingHelmExecutor = (*HelmClient)(nil)

func TestHelmClient_BuildInstallArgs(t *testing.T) {
	t.Parallel()

	client := NewHelmClient("helm", "/home/user/.kube/config", 3*time.Minute)

	tests := []struct {
		name        string
		req         InstallRequest
		wantErr     bool
		errContains string
		wantArgs    []string // subset of expected args (checked with Contains)
	}{
		{
			name: "minimal valid request",
			req: InstallRequest{
				ReleaseName: "my-app",
				ChartPath:   "nginx",
				Namespace:   "default",
			},
			wantArgs: []string{"upgrade", "--install", "my-app", "nginx", "-n", "default", "--create-namespace", "--timeout", "3m0s"},
		},
		{
			name: "full request with repo, version, values, skip-crds",
			req: InstallRequest{
				ReleaseName: "my-app",
				ChartPath:   "nginx",
				RepoURL:     "https://charts.example.com",
				Version:     "1.2.3",
				ValuesFile:  "/tmp/values.yaml",
				Namespace:   "prod",
				SkipCRDs:    true,
			},
			wantArgs: []string{"upgrade", "--install", "my-app", "nginx", "-n", "prod", "--repo", "https://charts.example.com", "--version", "1.2.3", "-f", "/tmp/values.yaml", "--skip-crds"},
		},
		{
			name: "optional fields omitted when empty",
			req: InstallRequest{
				ReleaseName: "my-app",
				ChartPath:   "nginx",
				Namespace:   "default",
				RepoURL:     "",
				Version:     "",
				ValuesFile:  "",
				SkipCRDs:    false,
			},
			wantArgs: []string{"upgrade", "--install", "my-app", "nginx", "-n", "default"},
		},
		{
			name: "dash-prefixed release name rejected",
			req: InstallRequest{
				ReleaseName: "--evil",
				ChartPath:   "nginx",
				Namespace:   "default",
			},
			wantErr:     true,
			errContains: "release name",
		},
		{
			name: "dash-prefixed chart path rejected",
			req: InstallRequest{
				ReleaseName: "ok",
				ChartPath:   "-bad-chart",
				Namespace:   "default",
			},
			wantErr:     true,
			errContains: "chart path",
		},
		{
			name: "dash-prefixed namespace rejected",
			req: InstallRequest{
				ReleaseName: "ok",
				ChartPath:   "nginx",
				Namespace:   "--admin",
			},
			wantErr:     true,
			errContains: "namespace",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			args, err := client.buildInstallArgs(tt.req)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, args)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				assert.True(t, errors.Is(err, errArgDashPrefix))
			} else {
				assert.NoError(t, err)
				for _, want := range tt.wantArgs {
					assert.Contains(t, args, want)
				}
			}
		})
	}
}

func TestHelmClient_BuildUninstallArgs(t *testing.T) {
	t.Parallel()

	client := NewHelmClient("helm", "", 2*time.Minute)

	tests := []struct {
		name        string
		req         UninstallRequest
		wantErr     bool
		errContains string
		wantArgs    []string
	}{
		{
			name: "valid request",
			req: UninstallRequest{
				ReleaseName: "my-app",
				Namespace:   "default",
			},
			wantArgs: []string{"uninstall", "my-app", "-n", "default"},
		},
		{
			name: "dash-prefixed release name rejected",
			req: UninstallRequest{
				ReleaseName: "--kubeconfig=/etc/secret",
				Namespace:   "default",
			},
			wantErr:     true,
			errContains: "release name",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			args, err := client.buildUninstallArgs(tt.req)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, args)
				assert.True(t, errors.Is(err, errArgDashPrefix))
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantArgs, args)
			}
		})
	}
}

func TestHelmClient_BuildRollbackArgs(t *testing.T) {
	t.Parallel()

	client := NewHelmClient("helm", "", 4*time.Minute)

	tests := []struct {
		name        string
		release     string
		namespace   string
		revision    int
		wantErr     bool
		errContains string
		wantArgs    []string
	}{
		{
			name:      "valid rollback",
			release:   "my-app",
			namespace: "prod",
			revision:  3,
			wantArgs:  []string{"rollback", "my-app", "3", "-n", "prod", "--timeout", "4m0s"},
		},
		{
			name:      "revision zero",
			release:   "my-app",
			namespace: "default",
			revision:  0,
			wantArgs:  []string{"rollback", "my-app", "0", "-n", "default", "--timeout", "4m0s"},
		},
		{
			name:        "dash-prefixed release name rejected",
			release:     "-evil",
			namespace:   "default",
			revision:    1,
			wantErr:     true,
			errContains: "release name",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			args, err := client.buildRollbackArgs(tt.release, tt.namespace, tt.revision)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, args)
				assert.True(t, errors.Is(err, errArgDashPrefix))
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantArgs, args)
			}
		})
	}
}

func TestHelmClient_WithLineHandler_ReturnsValidExecutor(t *testing.T) {
	t.Parallel()

	client := NewHelmClient("helm", "/home/user/.kube/config", 7*time.Minute)

	var captured []string
	executor := client.WithLineHandler(func(line string) {
		captured = append(captured, line)
	})

	// The returned executor must not be nil.
	assert.NotNil(t, executor)

	// It should be a *streamingHelmClient under the hood.
	streaming, ok := executor.(*streamingHelmClient)
	assert.True(t, ok, "WithLineHandler should return a *streamingHelmClient")
	assert.NotNil(t, streaming.onLine)

	// Timeout should delegate to the underlying HelmClient.
	assert.Equal(t, 7*time.Minute, executor.Timeout())
}

func TestHelmClient_WithLineHandler_OnLineFuncIsCalled(t *testing.T) {
	t.Parallel()

	client := NewHelmClient("helm", "", 1*time.Minute)

	var captured []string
	executor := client.WithLineHandler(func(line string) {
		captured = append(captured, line)
	})

	// Verify the onLine callback is the one we passed.
	streaming := executor.(*streamingHelmClient)
	streaming.onLine("hello")
	streaming.onLine("world")
	assert.Equal(t, []string{"hello", "world"}, captured)
}

func TestHelmClient_WithLineHandler_DifferentHandlersAreIndependent(t *testing.T) {
	t.Parallel()

	client := NewHelmClient("helm", "", 1*time.Minute)

	var captured1, captured2 []string
	exec1 := client.WithLineHandler(func(line string) {
		captured1 = append(captured1, line)
	})
	exec2 := client.WithLineHandler(func(line string) {
		captured2 = append(captured2, line)
	})

	// Each executor has its own handler.
	exec1.(*streamingHelmClient).onLine("only-for-1")
	exec2.(*streamingHelmClient).onLine("only-for-2")

	assert.Equal(t, []string{"only-for-1"}, captured1)
	assert.Equal(t, []string{"only-for-2"}, captured2)
}

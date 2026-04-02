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

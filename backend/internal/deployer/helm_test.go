package deployer

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewHelmClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		binary         string
		kubeconfig     string
		timeout        time.Duration
		expectBinary   string
		expectTimeout  time.Duration
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

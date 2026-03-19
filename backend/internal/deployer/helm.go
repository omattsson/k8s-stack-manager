// Package deployer provides Helm CLI wrapping and async deployment management
// for deploying and undeploying Helm charts to Kubernetes clusters.
package deployer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"time"
)

// HelmClient wraps the helm CLI binary for install, uninstall, and status operations.
type HelmClient struct {
	binaryPath string
	kubeconfig string
	timeout    time.Duration
}

// InstallRequest contains the parameters for a helm upgrade --install operation.
type InstallRequest struct {
	ReleaseName string
	ChartPath   string
	ValuesFile  string
	Namespace   string
}

// UninstallRequest contains the parameters for a helm uninstall operation.
type UninstallRequest struct {
	ReleaseName string
	Namespace   string
}

// ReleaseStatus holds parsed output from helm status.
type ReleaseStatus struct {
	Name      string      `json:"name"`
	Namespace string      `json:"namespace"`
	Info      releaseInfo `json:"info"`
}

// releaseInfo is a nested struct within helm status JSON output.
type releaseInfo struct {
	Status string `json:"status"`
}

// NewHelmClient creates a new HelmClient with the given binary path, kubeconfig,
// and default timeout for helm operations.
func NewHelmClient(binaryPath, kubeconfig string, timeout time.Duration) *HelmClient {
	if binaryPath == "" {
		binaryPath = "helm"
	}
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	return &HelmClient{
		binaryPath: binaryPath,
		kubeconfig: kubeconfig,
		timeout:    timeout,
	}
}

// Timeout returns the configured timeout duration for helm operations.
func (h *HelmClient) Timeout() time.Duration {
	return h.timeout
}

// Install runs: helm upgrade --install <release> <chart> -f <valuesFile> -n <namespace> --create-namespace --timeout <timeout>
// Returns combined stdout+stderr output and error.
func (h *HelmClient) Install(ctx context.Context, req InstallRequest) (string, error) {
	args := []string{
		"upgrade", "--install",
		req.ReleaseName,
		req.ChartPath,
		"-n", req.Namespace,
		"--create-namespace",
		"--timeout", h.timeout.String(),
	}
	if req.ValuesFile != "" {
		args = append(args, "-f", req.ValuesFile)
	}

	return h.run(ctx, args)
}

// Uninstall runs: helm uninstall <release> -n <namespace>
// Returns combined stdout+stderr output and error.
func (h *HelmClient) Uninstall(ctx context.Context, req UninstallRequest) (string, error) {
	args := []string{
		"uninstall",
		req.ReleaseName,
		"-n", req.Namespace,
	}

	return h.run(ctx, args)
}

// Status runs: helm status <release> -n <namespace> -o json
// Returns the parsed release status.
func (h *HelmClient) Status(ctx context.Context, release, namespace string) (*ReleaseStatus, error) {
	args := []string{
		"status",
		release,
		"-n", namespace,
		"-o", "json",
	}

	output, err := h.run(ctx, args)
	if err != nil {
		return nil, fmt.Errorf("helm status: %w", err)
	}

	var status ReleaseStatus
	if err := json.Unmarshal([]byte(output), &status); err != nil {
		return nil, fmt.Errorf("parsing helm status output: %w", err)
	}

	return &status, nil
}

// run executes a helm command with the given arguments and returns the combined output.
func (h *HelmClient) run(ctx context.Context, args []string) (string, error) {
	slog.Info("executing helm command",
		"binary", h.binaryPath,
		"args", args,
	)

	cmd := exec.CommandContext(ctx, h.binaryPath, args...)

	if h.kubeconfig != "" {
		cmd.Env = append(cmd.Environ(), "KUBECONFIG="+h.kubeconfig)
	}

	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined

	err := cmd.Run()
	output := combined.String()

	if err != nil {
		return output, fmt.Errorf("helm command failed: %w\noutput: %s", err, output)
	}

	return output, nil
}

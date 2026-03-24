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
	"strings"
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
	ChartPath   string // chart name (e.g. "ingress-nginx") or local path
	RepoURL     string // optional: helm repository URL (passed as --repo)
	Version     string // optional: chart version (passed as --version)
	ValuesFile  string
	Namespace   string
	SkipCRDs    bool   // skip CRD installation (avoids conflicts when CRDs already exist)
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
	if req.RepoURL != "" {
		args = append(args, "--repo", req.RepoURL)
	}
	if req.Version != "" {
		args = append(args, "--version", req.Version)
	}
	if req.ValuesFile != "" {
		args = append(args, "-f", req.ValuesFile)
	}
	if req.SkipCRDs {
		args = append(args, "--skip-crds")
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

// ListReleases runs: helm list -n <namespace> -q
// Returns a list of release names in the given namespace.
func (h *HelmClient) ListReleases(ctx context.Context, namespace string) ([]string, error) {
	args := []string{
		"list",
		"-n", namespace,
		"-q",
	}

	output, err := h.run(ctx, args)
	if err != nil {
		return nil, fmt.Errorf("helm list: %w", err)
	}

	var releases []string
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			releases = append(releases, line)
		}
	}

	return releases, nil
}

// run executes a helm command with the given arguments and returns the combined output.
func (h *HelmClient) run(ctx context.Context, args []string) (string, error) {
	// Prepend --kubeconfig flag so helm always uses the configured kubeconfig
	// regardless of the process environment.
	if h.kubeconfig != "" {
		args = append([]string{"--kubeconfig", h.kubeconfig}, args...)
	}

	slog.Info("executing helm command",
		"binary", h.binaryPath,
		"args", args,
	)

	cmd := exec.CommandContext(ctx, h.binaryPath, args...)

	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined

	err := cmd.Run()
	output := combined.String()

	if err != nil {
		return output, fmt.Errorf("helm command failed: %w", err)
	}

	return output, nil
}

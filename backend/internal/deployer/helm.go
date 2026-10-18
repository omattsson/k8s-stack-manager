// Package deployer provides Helm CLI wrapping and async deployment management
// for deploying and undeploying Helm charts to Kubernetes clusters.
package deployer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// helmTerminateGrace is how long a helm process gets after SIGTERM before it is
// killed. On SIGTERM helm marks an in-flight install, upgrade or rollback as
// failed. A SIGKILL leaves the release in pending-*, which blocks the next
// operation with "another operation (install/upgrade/rollback) is in progress".
const helmTerminateGrace = 30 * time.Second

// newHelmCommand returns an exec.Cmd that stops helm with SIGTERM (not SIGKILL)
// when ctx is done, and kills it only after helmTerminateGrace.
func newHelmCommand(ctx context.Context, binaryPath string, args []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, binaryPath, args...) //nolint:gosec // G204: binaryPath is admin-configured (not user input); all positional args are validated by validatePositionalArg to prevent argument injection; flag values are safe (consumed by pflag, not parsed as flags); exec.Command uses argv directly (no shell).
	cmd.Cancel = func() error {
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = helmTerminateGrace
	return cmd
}

// errArgDashPrefix is returned when an argument starts with a dash, which could
// cause the helm CLI to interpret it as a flag (argument injection).
var errArgDashPrefix = errors.New("must not start with a dash")

// validatePositionalArg checks that a value intended as a positional argument
// to the helm CLI does not start with a dash. exec.Command calls the binary
// directly (no shell), so shell injection is not possible, but a positional
// argument starting with "-" could be misinterpreted as a helm flag.
func validatePositionalArg(name, value string) error {
	if strings.HasPrefix(value, "-") {
		return fmt.Errorf("invalid %s %q: %w", name, value, errArgDashPrefix)
	}
	return nil
}

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
	SkipCRDs    bool // skip CRD installation (avoids conflicts when CRDs already exist)
	// NamespaceReady is true when the deployer already created the namespace
	// (k8s.Client.EnsureNamespace, with its labels). Helm then gets no
	// --create-namespace: Helm always sends a namespace create with that flag,
	// and an admission policy that requires the managed-by label refuses it
	// before the API server answers "already exists".
	NamespaceReady bool
}

// defaultUninstallTimeout limits the --wait of one helm uninstall when the
// request sets no Timeout.
const defaultUninstallTimeout = 5 * time.Minute

// UninstallRequest contains the parameters for a helm uninstall operation.
type UninstallRequest struct {
	ReleaseName string
	Namespace   string
	// Timeout limits the wait until Kubernetes has deleted the objects of the
	// release (helm uninstall --wait --timeout). Zero means
	// defaultUninstallTimeout.
	Timeout time.Duration
	// NoWait skips --wait: for a clean (the namespace delete removes every
	// object anyway) and for the orphaned-namespace cleanup (an HTTP request).
	NoWait bool
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

// Install runs: helm upgrade --install <release> <chart> -f <valuesFile> -n <namespace> [--create-namespace] --timeout <timeout>
// (--create-namespace only when req.NamespaceReady is false).
// Returns combined stdout+stderr output and error.
func (h *HelmClient) Install(ctx context.Context, req InstallRequest) (string, error) {
	args, err := h.buildInstallArgs(req)
	if err != nil {
		return "", err
	}
	return h.run(ctx, args)
}

func (h *HelmClient) buildInstallArgs(req InstallRequest) ([]string, error) {
	// Validate positional arguments to prevent argument injection. Namespace
	// is validated upstream (RFC 1123 in StackInstance.Validate), but we
	// re-check here as defense-in-depth since it is security-critical.
	for _, check := range []struct{ name, value string }{
		{"release name", req.ReleaseName},
		{"chart path", req.ChartPath},
		{"namespace", req.Namespace},
	} {
		if err := validatePositionalArg(check.name, check.value); err != nil {
			return nil, err
		}
	}

	args := []string{
		"upgrade", "--install",
		req.ReleaseName,
		req.ChartPath,
		"-n", req.Namespace,
	}
	if !req.NamespaceReady {
		args = append(args, "--create-namespace")
	}
	args = append(args, "--timeout", h.timeout.String())
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
	return args, nil
}

// Uninstall runs: helm uninstall <release> -n <namespace>
// Returns combined stdout+stderr output and error.
func (h *HelmClient) Uninstall(ctx context.Context, req UninstallRequest) (string, error) {
	args, err := h.buildUninstallArgs(req)
	if err != nil {
		return "", err
	}
	return h.run(ctx, args)
}

func (h *HelmClient) buildUninstallArgs(req UninstallRequest) ([]string, error) {
	if err := validatePositionalArg("release name", req.ReleaseName); err != nil {
		return nil, err
	}
	args := []string{"uninstall", req.ReleaseName, "-n", req.Namespace}
	if req.NoWait {
		return args, nil
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = defaultUninstallTimeout
	}
	// --wait: return only when Kubernetes has deleted the objects of the
	// release. Without it, a stop ended while a PVC was still Terminating, and
	// a deploy right after the stop missed that PVC (issue #563).
	return append(args, "--wait", "--timeout", timeout.String()), nil
}

// Status runs: helm status <release> -n <namespace> -o json
// Returns the parsed release status.
func (h *HelmClient) Status(ctx context.Context, release, namespace string) (*ReleaseStatus, error) {
	if err := validatePositionalArg("release name", release); err != nil {
		return nil, err
	}

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

	releases := []string{}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			releases = append(releases, line)
		}
	}

	return releases, nil
}

// History runs: helm history <release> -n <namespace> --max <max> -o json
// Returns parsed release revision history.
func (h *HelmClient) History(ctx context.Context, releaseName, namespace string, max int) ([]ReleaseRevision, error) {
	if err := validatePositionalArg("release name", releaseName); err != nil {
		return nil, err
	}

	if max <= 0 {
		max = 256
	}

	args := []string{
		"history",
		releaseName,
		"-n", namespace,
		"--max", strconv.Itoa(max),
		"-o", "json",
	}

	output, err := h.run(ctx, args)
	if err != nil {
		return nil, fmt.Errorf("helm history: %w", err)
	}

	var revisions []ReleaseRevision
	if err := json.Unmarshal([]byte(output), &revisions); err != nil {
		return nil, fmt.Errorf("parsing helm history output: %w", err)
	}

	return revisions, nil
}

// Rollback runs: helm rollback <release> <revision> -n <namespace>
// Returns combined stdout+stderr output and error.
func (h *HelmClient) Rollback(ctx context.Context, releaseName, namespace string, revision int) (string, error) {
	args, err := h.buildRollbackArgs(releaseName, namespace, revision)
	if err != nil {
		return "", err
	}
	return h.run(ctx, args)
}

func (h *HelmClient) buildRollbackArgs(releaseName, namespace string, revision int) ([]string, error) {
	if err := validatePositionalArg("release name", releaseName); err != nil {
		return nil, err
	}
	return []string{
		"rollback",
		releaseName,
		strconv.Itoa(revision),
		"-n", namespace,
		"--timeout", h.timeout.String(),
	}, nil
}

// GetValues runs: helm get values <release> -n <namespace> --revision <revision> -o yaml
// Returns the values YAML for the given release revision.
func (h *HelmClient) GetValues(ctx context.Context, releaseName, namespace string, revision int) (string, error) {
	if err := validatePositionalArg("release name", releaseName); err != nil {
		return "", err
	}

	args := []string{
		"get", "values",
		releaseName,
		"-n", namespace,
		"-o", "yaml",
	}
	if revision > 0 {
		args = append(args, "--revision", strconv.Itoa(revision))
	}

	output, err := h.run(ctx, args)
	if err != nil {
		return "", fmt.Errorf("helm get values: %w", err)
	}

	return output, nil
}

// RegistryLogin runs: helm registry login <host> --username <user> --password-stdin
// The login state persists in the helm config dir for the lifetime of the process.
func (h *HelmClient) RegistryLogin(ctx context.Context, host, username, password string) error {
	if host == "" || username == "" {
		return nil
	}
	if err := validatePositionalArg("registry host", host); err != nil {
		return err
	}
	if err := validatePositionalArg("registry username", username); err != nil {
		return err
	}

	args := []string{
		"registry", "login",
		host,
		"--username", username,
		"--password-stdin",
	}

	slog.Info("executing helm registry login", "host", host, "username", username)

	cmd := newHelmCommand(ctx, h.binaryPath, args)
	cmd.Stdin = strings.NewReader(password)

	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined

	if err := cmd.Run(); err != nil {
		slog.Debug("helm registry login failed", "host", host, "output", combined.String())
		return fmt.Errorf("helm registry login %s failed: %w", host, err)
	}

	slog.Info("helm registry login succeeded", "host", host)
	return nil
}

// command returns the helm command for args with the kubeconfig of the
// cluster. With a kubeconfig path, it prepends --kubeconfig, so helm always
// uses that file regardless of the process environment. Without one (an
// in-cluster cluster), it sets KUBECONFIG to an empty file: helm then reads
// neither $KUBECONFIG nor ~/.kube/config and uses the service account of the
// pod, like k8s.NewClient(""). Outside a pod it cannot reach any cluster
// (#582).
func (h *HelmClient) command(ctx context.Context, args []string) (*exec.Cmd, []string) {
	if h.kubeconfig != "" {
		args = append([]string{"--kubeconfig", h.kubeconfig}, args...)
	}
	cmd := newHelmCommand(ctx, h.binaryPath, args)
	if h.kubeconfig == "" {
		// The last value of a duplicate key wins (os/exec).
		cmd.Env = append(os.Environ(), "KUBECONFIG="+os.DevNull)
	}
	return cmd, args
}

// run executes a helm command with the given arguments and returns the combined output.
func (h *HelmClient) run(ctx context.Context, args []string) (string, error) {
	cmd, args := h.command(ctx, args)

	slog.Info("executing helm command",
		"binary", h.binaryPath,
		"args", args,
	)

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

const maxScannerLineSize = 256 * 1024

// runStreaming executes a helm command, calling onLine for each line of combined
// stdout/stderr output as it is produced. The full output is still accumulated
// and returned so callers can store it in the deployment log.
// Stdout and stderr are merged via io.MultiReader into a single scanner to
// preserve deterministic line ordering (same approach as the non-streaming run()).
func (h *HelmClient) runStreaming(ctx context.Context, args []string, onLine func(string)) (string, error) {
	cmd, args := h.command(ctx, args)

	slog.Info("executing helm command",
		"binary", h.binaryPath,
		"args", args,
		"streaming", true,
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("creating stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("creating stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("starting helm command: %w", err)
	}

	var combined strings.Builder
	scanner := bufio.NewScanner(io.MultiReader(stdout, stderr))
	scanner.Buffer(make([]byte, 0, maxScannerLineSize), maxScannerLineSize)
	for scanner.Scan() {
		line := scanner.Text()
		combined.WriteString(line)
		combined.WriteByte('\n')
		onLine(line)
	}
	if err := scanner.Err(); err != nil {
		slog.Warn("scanner error reading helm output", "error", err)
		onLine(fmt.Sprintf("[scanner error: %v]", err))
	}

	err = cmd.Wait()
	output := combined.String()
	if err != nil {
		return output, fmt.Errorf("helm command failed: %w", err)
	}
	return output, nil
}

// WithLineHandler returns a new HelmExecutor that streams each output line to fn.
// Implements StreamingHelmExecutor. Methods that don't produce significant
// streaming output (Status, ListReleases, History, GetValues) pass through
// to the underlying HelmClient unchanged.
func (h *HelmClient) WithLineHandler(fn func(string)) HelmExecutor {
	return &streamingHelmClient{HelmClient: h, onLine: fn}
}

// streamingHelmClient wraps a HelmClient and streams each output line via onLine.
type streamingHelmClient struct {
	*HelmClient
	onLine func(string)
}

func (s *streamingHelmClient) Install(ctx context.Context, req InstallRequest) (string, error) {
	args, err := s.buildInstallArgs(req)
	if err != nil {
		return "", err
	}
	return s.runStreaming(ctx, args, s.onLine)
}

func (s *streamingHelmClient) Uninstall(ctx context.Context, req UninstallRequest) (string, error) {
	args, err := s.buildUninstallArgs(req)
	if err != nil {
		return "", err
	}
	return s.runStreaming(ctx, args, s.onLine)
}

func (s *streamingHelmClient) Rollback(ctx context.Context, releaseName, namespace string, revision int) (string, error) {
	args, err := s.buildRollbackArgs(releaseName, namespace, revision)
	if err != nil {
		return "", err
	}
	return s.runStreaming(ctx, args, s.onLine)
}

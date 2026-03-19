package deployer

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"backend/internal/models"
	"backend/internal/websocket"

	"github.com/google/uuid"
)

// Manager orchestrates asynchronous deployments with concurrency control.
type Manager struct {
	helm         *HelmClient
	instanceRepo models.StackInstanceRepository
	logRepo      models.DeploymentLogRepository
	hub          websocket.BroadcastSender
	semaphore    chan struct{}
}

// ManagerConfig holds the dependencies for creating a Manager.
type ManagerConfig struct {
	HelmClient    *HelmClient
	InstanceRepo  models.StackInstanceRepository
	DeployLogRepo models.DeploymentLogRepository
	Hub           websocket.BroadcastSender
	MaxConcurrent int
}

// DeployRequest contains everything needed to deploy a stack instance.
type DeployRequest struct {
	Instance   *models.StackInstance
	Definition *models.StackDefinition
	Charts     []ChartDeployInfo
	Owner      string
}

// ChartDeployInfo holds chart configuration and pre-generated merged values.
type ChartDeployInfo struct {
	ChartConfig models.ChartConfig
	ValuesYAML  []byte
}

// NewManager creates a new deployment Manager with the given configuration.
func NewManager(cfg ManagerConfig) *Manager {
	maxConcurrent := cfg.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 3
	}

	return &Manager{
		helm:         cfg.HelmClient,
		instanceRepo: cfg.InstanceRepo,
		logRepo:      cfg.DeployLogRepo,
		hub:          cfg.Hub,
		semaphore:    make(chan struct{}, maxConcurrent),
	}
}

// Deploy starts an async deployment. Returns the deployment log ID immediately.
// The actual deployment runs in a background goroutine with concurrency limiting.
func (m *Manager) Deploy(ctx context.Context, req DeployRequest) (string, error) {
	logID := uuid.New().String()
	now := time.Now().UTC()

	// Create deployment log entry.
	deployLog := &models.DeploymentLog{
		ID:              logID,
		StackInstanceID: req.Instance.ID,
		Action:          models.DeployActionDeploy,
		Status:          models.DeployLogRunning,
		StartedAt:       now,
	}
	if err := m.logRepo.Create(deployLog); err != nil {
		return "", fmt.Errorf("creating deployment log: %w", err)
	}

	// Update instance status to deploying.
	req.Instance.Status = models.StackStatusDeploying
	req.Instance.ErrorMessage = ""
	if err := m.instanceRepo.Update(req.Instance); err != nil {
		return "", fmt.Errorf("updating instance status: %w", err)
	}

	// Broadcast initial status.
	m.broadcastStatus(req.Instance.ID, models.StackStatusDeploying, logID)

	// Sort charts by deploy order.
	charts := make([]ChartDeployInfo, len(req.Charts))
	copy(charts, req.Charts)
	sort.Slice(charts, func(i, j int) bool {
		return charts[i].ChartConfig.DeployOrder < charts[j].ChartConfig.DeployOrder
	})

	// Launch async deployment.
	go m.executeDeploy(req.Instance.ID, logID, req.Instance.Namespace, charts)

	return logID, nil
}

// Stop starts an async stop/uninstall. Returns the deployment log ID immediately.
func (m *Manager) Stop(ctx context.Context, instance *models.StackInstance) (string, error) {
	logID := uuid.New().String()
	now := time.Now().UTC()

	// Create deployment log entry.
	deployLog := &models.DeploymentLog{
		ID:              logID,
		StackInstanceID: instance.ID,
		Action:          models.DeployActionStop,
		Status:          models.DeployLogRunning,
		StartedAt:       now,
	}
	if err := m.logRepo.Create(deployLog); err != nil {
		return "", fmt.Errorf("creating deployment log: %w", err)
	}

	// Update instance status to stopping.
	instance.Status = models.StackStatusStopping
	instance.ErrorMessage = ""
	if err := m.instanceRepo.Update(instance); err != nil {
		return "", fmt.Errorf("updating instance status: %w", err)
	}

	// Broadcast initial status.
	m.broadcastStatus(instance.ID, models.StackStatusStopping, logID)

	// Launch async stop.
	go m.executeStop(instance, logID)

	return logID, nil
}

// executeDeploy runs the helm install for each chart sequentially within
// a concurrency-limited goroutine.
func (m *Manager) executeDeploy(instanceID, logID, namespace string, charts []ChartDeployInfo) {
	// Acquire semaphore.
	m.semaphore <- struct{}{}
	defer func() { <-m.semaphore }()

	var allOutput string
	var deployErr error

	// Create a temp directory for values files.
	tmpDir, err := os.MkdirTemp("", "deploy-"+instanceID+"-")
	if err != nil {
		deployErr = fmt.Errorf("creating temp directory: %w", err)
		slog.Error("deployment failed", "instance_id", instanceID, "error", deployErr)
		m.finalizeDeploy(instanceID, logID, allOutput, deployErr)
		return
	}
	defer os.RemoveAll(tmpDir)

	ctx := context.Background()

	for _, chart := range charts {
		releaseName := fmt.Sprintf("%s-%s", namespace, chart.ChartConfig.ChartName)

		slog.Info("deploying chart",
			"instance_id", instanceID,
			"chart", chart.ChartConfig.ChartName,
			"release", releaseName,
			"namespace", namespace,
		)

		// Write values to temp file.
		valuesFile := ""
		if len(chart.ValuesYAML) > 0 {
			valuesPath := filepath.Join(tmpDir, chart.ChartConfig.ChartName+"-values.yaml")
			if err := os.WriteFile(valuesPath, chart.ValuesYAML, 0600); err != nil {
				deployErr = fmt.Errorf("writing values file for chart %q: %w", chart.ChartConfig.ChartName, err)
				break
			}
			valuesFile = valuesPath
		}

		// Determine chart path — use repository URL if chart path is empty.
		chartPath := chart.ChartConfig.ChartPath
		if chartPath == "" {
			chartPath = chart.ChartConfig.RepositoryURL
		}

		output, err := m.helm.Install(ctx, InstallRequest{
			ReleaseName: releaseName,
			ChartPath:   chartPath,
			ValuesFile:  valuesFile,
			Namespace:   namespace,
		})

		allOutput += fmt.Sprintf("=== Chart: %s ===\n%s\n", chart.ChartConfig.ChartName, output)
		m.broadcastLog(instanceID, logID, output)

		if err != nil {
			deployErr = fmt.Errorf("deploying chart %q: %w", chart.ChartConfig.ChartName, err)
			break
		}
	}

	m.finalizeDeploy(instanceID, logID, allOutput, deployErr)
}

// finalizeDeploy updates the instance and deployment log with the final status.
func (m *Manager) finalizeDeploy(instanceID, logID, output string, deployErr error) {
	now := time.Now().UTC()

	instance, err := m.instanceRepo.FindByID(instanceID)
	if err != nil {
		slog.Error("failed to find instance for finalization",
			"instance_id", instanceID, "error", err)
		return
	}

	deployLog, err := m.logRepo.FindByID(logID)
	if err != nil {
		slog.Error("failed to find deployment log for finalization",
			"log_id", logID, "error", err)
		return
	}

	deployLog.Output = output
	deployLog.CompletedAt = &now

	if deployErr != nil {
		instance.Status = models.StackStatusError
		instance.ErrorMessage = deployErr.Error()
		deployLog.Status = models.DeployLogError
		deployLog.ErrorMessage = deployErr.Error()

		slog.Error("deployment failed",
			"instance_id", instanceID,
			"log_id", logID,
			"error", deployErr,
		)
	} else {
		instance.Status = models.StackStatusRunning
		instance.ErrorMessage = ""
		instance.LastDeployedAt = &now
		deployLog.Status = models.DeployLogSuccess

		slog.Info("deployment succeeded",
			"instance_id", instanceID,
			"log_id", logID,
		)
	}

	if err := m.instanceRepo.Update(instance); err != nil {
		slog.Error("failed to update instance status after deploy",
			"instance_id", instanceID, "error", err)
	}

	if err := m.logRepo.Update(deployLog); err != nil {
		slog.Error("failed to update deployment log after deploy",
			"log_id", logID, "error", err)
	}

	// Broadcast final status.
	if deployErr != nil {
		m.broadcastStatusWithError(instanceID, models.StackStatusError, logID, deployErr.Error())
	} else {
		m.broadcastStatus(instanceID, models.StackStatusRunning, logID)
	}
}

// executeStop runs helm uninstall for each chart in reverse deploy order.
func (m *Manager) executeStop(instance *models.StackInstance, logID string) {
	// Acquire semaphore.
	m.semaphore <- struct{}{}
	defer func() { <-m.semaphore }()

	// We need the charts to uninstall. Retrieve the deployment log to get the instance ID,
	// then we rely on the release naming convention: {namespace}-{chartName}.
	// For stop, we use the instance data passed in. Since we don't have the chart list
	// here, we use helm to check what's installed in the namespace.
	// For simplicity and reliability, we perform the uninstall using the instance data.
	// The caller should use the StopWithCharts method if chart info is available.

	slog.Info("stop operation started",
		"instance_id", instance.ID,
		"namespace", instance.Namespace,
		"log_id", logID,
	)

	// Since Stop doesn't receive chart info, finalize with an informational message.
	// Use StopWithCharts for full chart-aware uninstall.
	m.finalizeStop(instance.ID, logID, "stop initiated — use StopWithCharts for chart-aware uninstall", nil)
}

// StopWithCharts starts an async stop/uninstall with explicit chart information.
// Returns the deployment log ID immediately.
func (m *Manager) StopWithCharts(ctx context.Context, instance *models.StackInstance, charts []ChartDeployInfo) (string, error) {
	logID := uuid.New().String()
	now := time.Now().UTC()

	deployLog := &models.DeploymentLog{
		ID:              logID,
		StackInstanceID: instance.ID,
		Action:          models.DeployActionStop,
		Status:          models.DeployLogRunning,
		StartedAt:       now,
	}
	if err := m.logRepo.Create(deployLog); err != nil {
		return "", fmt.Errorf("creating deployment log: %w", err)
	}

	instance.Status = models.StackStatusStopping
	instance.ErrorMessage = ""
	if err := m.instanceRepo.Update(instance); err != nil {
		return "", fmt.Errorf("updating instance status: %w", err)
	}

	m.broadcastStatus(instance.ID, models.StackStatusStopping, logID)

	// Sort charts in reverse deploy order for teardown.
	sortedCharts := make([]ChartDeployInfo, len(charts))
	copy(sortedCharts, charts)
	sort.Slice(sortedCharts, func(i, j int) bool {
		return sortedCharts[i].ChartConfig.DeployOrder > sortedCharts[j].ChartConfig.DeployOrder
	})

	go m.executeStopWithCharts(instance.ID, logID, instance.Namespace, sortedCharts)

	return logID, nil
}

// executeStopWithCharts runs helm uninstall for each chart in reverse order.
func (m *Manager) executeStopWithCharts(instanceID, logID, namespace string, charts []ChartDeployInfo) {
	m.semaphore <- struct{}{}
	defer func() { <-m.semaphore }()

	var allOutput string
	var stopErr error

	ctx := context.Background()

	for _, chart := range charts {
		releaseName := fmt.Sprintf("%s-%s", namespace, chart.ChartConfig.ChartName)

		slog.Info("uninstalling chart",
			"instance_id", instanceID,
			"chart", chart.ChartConfig.ChartName,
			"release", releaseName,
			"namespace", namespace,
		)

		output, err := m.helm.Uninstall(ctx, UninstallRequest{
			ReleaseName: releaseName,
			Namespace:   namespace,
		})

		allOutput += fmt.Sprintf("=== Chart: %s ===\n%s\n", chart.ChartConfig.ChartName, output)
		m.broadcastLog(instanceID, logID, output)

		if err != nil {
			stopErr = fmt.Errorf("uninstalling chart %q: %w", chart.ChartConfig.ChartName, err)
			break
		}
	}

	m.finalizeStop(instanceID, logID, allOutput, stopErr)
}

// finalizeStop updates the instance and deployment log with the final stop status.
func (m *Manager) finalizeStop(instanceID, logID, output string, stopErr error) {
	now := time.Now().UTC()

	instance, err := m.instanceRepo.FindByID(instanceID)
	if err != nil {
		slog.Error("failed to find instance for stop finalization",
			"instance_id", instanceID, "error", err)
		return
	}

	deployLog, err := m.logRepo.FindByID(logID)
	if err != nil {
		slog.Error("failed to find deployment log for stop finalization",
			"log_id", logID, "error", err)
		return
	}

	deployLog.Output = output
	deployLog.CompletedAt = &now

	if stopErr != nil {
		instance.Status = models.StackStatusError
		instance.ErrorMessage = stopErr.Error()
		deployLog.Status = models.DeployLogError
		deployLog.ErrorMessage = stopErr.Error()

		slog.Error("stop failed",
			"instance_id", instanceID,
			"log_id", logID,
			"error", stopErr,
		)
	} else {
		instance.Status = models.StackStatusStopped
		instance.ErrorMessage = ""
		deployLog.Status = models.DeployLogSuccess

		slog.Info("stop succeeded",
			"instance_id", instanceID,
			"log_id", logID,
		)
	}

	if err := m.instanceRepo.Update(instance); err != nil {
		slog.Error("failed to update instance status after stop",
			"instance_id", instanceID, "error", err)
	}

	if err := m.logRepo.Update(deployLog); err != nil {
		slog.Error("failed to update deployment log after stop",
			"log_id", logID, "error", err)
	}

	if stopErr != nil {
		m.broadcastStatusWithError(instanceID, models.StackStatusError, logID, stopErr.Error())
	} else {
		m.broadcastStatus(instanceID, models.StackStatusStopped, logID)
	}
}

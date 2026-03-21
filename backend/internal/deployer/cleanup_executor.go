package deployer

import (
	"context"
	"fmt"

	"backend/internal/models"
)

// CleanupExecutor implements scheduler.ActionExecutor by delegating to the
// deploy Manager. It resolves definitions and charts for each instance.
type CleanupExecutor struct {
	manager         *Manager
	definitionRepo  models.StackDefinitionRepository
	chartConfigRepo models.ChartConfigRepository
	instanceRepo    models.StackInstanceRepository
}

// NewCleanupExecutor creates a CleanupExecutor.
func NewCleanupExecutor(
	m *Manager,
	defRepo models.StackDefinitionRepository,
	ccRepo models.ChartConfigRepository,
	instRepo models.StackInstanceRepository,
) *CleanupExecutor {
	return &CleanupExecutor{
		manager:         m,
		definitionRepo:  defRepo,
		chartConfigRepo: ccRepo,
		instanceRepo:    instRepo,
	}
}

// StopInstance resolves charts and initiates an async Helm uninstall.
func (e *CleanupExecutor) StopInstance(ctx context.Context, inst *models.StackInstance) error {
	charts, err := e.resolveCharts(inst)
	if err != nil {
		return err
	}
	var chartInfos []ChartDeployInfo
	for _, ch := range charts {
		chartInfos = append(chartInfos, ChartDeployInfo{ChartConfig: ch})
	}
	_, err = e.manager.StopWithCharts(ctx, inst, chartInfos)
	return err
}

// CleanInstance resolves charts and initiates Helm uninstall + namespace deletion.
func (e *CleanupExecutor) CleanInstance(ctx context.Context, inst *models.StackInstance) error {
	charts, err := e.resolveCharts(inst)
	if err != nil {
		return err
	}
	_, err = e.manager.Clean(ctx, inst, charts)
	return err
}

// DeleteInstance stops/cleans if needed, then deletes the instance from the database.
func (e *CleanupExecutor) DeleteInstance(ctx context.Context, inst *models.StackInstance) error {
	// If the instance is running, stop it first.
	if inst.Status == models.StackStatusRunning || inst.Status == models.StackStatusDeploying {
		if err := e.StopInstance(ctx, inst); err != nil {
			return fmt.Errorf("stopping before delete: %w", err)
		}
	}
	return e.instanceRepo.Delete(inst.ID)
}

func (e *CleanupExecutor) resolveCharts(inst *models.StackInstance) ([]models.ChartConfig, error) {
	def, err := e.definitionRepo.FindByID(inst.StackDefinitionID)
	if err != nil {
		return nil, fmt.Errorf("finding definition: %w", err)
	}
	charts, err := e.chartConfigRepo.ListByDefinition(def.ID)
	if err != nil {
		return nil, fmt.Errorf("listing charts: %w", err)
	}
	if len(charts) == 0 {
		return nil, fmt.Errorf("no charts configured for definition %s", def.ID)
	}
	return charts, nil
}

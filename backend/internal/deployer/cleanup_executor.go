package deployer

import (
	"context"
	"fmt"

	"backend/internal/database"
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

// DeleteInstance deletes the instance from the database.
// It refuses deletion while the instance is running or in the middle of an async
// stop/clean operation, because those workflows need to read/update the record.
// Callers should ensure the instance is stopped/cleaned before requesting deletion.
func (e *CleanupExecutor) DeleteInstance(ctx context.Context, inst *models.StackInstance) error {
	// Do not start a delete when the caller stopped (for example the cleanup
	// scheduler at the end of a leadership term).
	if err := ctx.Err(); err != nil {
		return err
	}
	switch inst.Status {
	case models.StackStatusRunning, models.StackStatusPartial, models.StackStatusDeploying, models.StackStatusStabilizing,
		models.StackStatusStopping, models.StackStatusCleaning:
		return fmt.Errorf("cannot delete instance %s while status is %s; stop/clean must complete first", inst.ID, inst.Status)
	}
	// With a transaction runner, delete the branch overrides and the quick
	// deploy definition owned by the instance in the same transaction.
	if e.manager != nil && e.manager.txRunner != nil {
		return database.DeleteInstanceWithOwnedDefinition(e.manager.txRunner, inst)
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

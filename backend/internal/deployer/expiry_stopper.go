package deployer

import (
	"context"
	"fmt"

	"backend/internal/models"
)

// ExpiryStopper wraps a Manager with the repos needed to stop an instance
// when it expires. It implements the ttl.InstanceStopper interface.
type ExpiryStopper struct {
	manager         *Manager
	definitionRepo  models.StackDefinitionRepository
	chartConfigRepo models.ChartConfigRepository
}

// NewExpiryStopper creates an ExpiryStopper.
func NewExpiryStopper(
	m *Manager,
	defRepo models.StackDefinitionRepository,
	ccRepo models.ChartConfigRepository,
) *ExpiryStopper {
	return &ExpiryStopper{
		manager:         m,
		definitionRepo:  defRepo,
		chartConfigRepo: ccRepo,
	}
}

// StopInstance resolves charts for the instance and initiates an async Helm uninstall.
func (s *ExpiryStopper) StopInstance(ctx context.Context, inst *models.StackInstance) error {
	def, err := s.definitionRepo.FindByID(inst.StackDefinitionID)
	if err != nil {
		return fmt.Errorf("finding definition: %w", err)
	}

	charts, err := s.chartConfigRepo.ListByDefinition(def.ID)
	if err != nil {
		return fmt.Errorf("listing charts: %w", err)
	}

	if len(charts) == 0 {
		return fmt.Errorf("no charts configured for definition %s", def.ID)
	}

	var chartInfos []ChartDeployInfo
	for _, ch := range charts {
		chartInfos = append(chartInfos, ChartDeployInfo{ChartConfig: ch})
	}

	_, err = s.manager.StopWithCharts(ctx, inst, chartInfos)
	return err
}

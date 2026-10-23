package handlers

import (
	"context"

	"backend/internal/models"
)

// hookChartConfigRepo runs the hook of hooks with "before:Chart.<Method>" and
// "after:Chart.<Method>" around the calls of MockChartConfigRepository, which
// fails on a done context (#656).
type hookChartConfigRepo struct {
	*MockChartConfigRepository
	hooks *hookInstanceRepo
}

func newHookChartConfigRepo(hooks *hookInstanceRepo) *hookChartConfigRepo {
	return &hookChartConfigRepo{MockChartConfigRepository: NewMockChartConfigRepository(), hooks: hooks}
}

func (r *hookChartConfigRepo) Create(ctx context.Context, c *models.ChartConfig) error {
	r.hooks.run("before:Chart.Create", ctx)
	defer r.hooks.run("after:Chart.Create", ctx)
	return r.MockChartConfigRepository.Create(ctx, c)
}

func (r *hookChartConfigRepo) FindByID(ctx context.Context, id string) (*models.ChartConfig, error) {
	r.hooks.run("before:Chart.FindByID", ctx)
	defer r.hooks.run("after:Chart.FindByID", ctx)
	return r.MockChartConfigRepository.FindByID(ctx, id)
}

func (r *hookChartConfigRepo) Update(ctx context.Context, c *models.ChartConfig) error {
	r.hooks.run("before:Chart.Update", ctx)
	defer r.hooks.run("after:Chart.Update", ctx)
	return r.MockChartConfigRepository.Update(ctx, c)
}

func (r *hookChartConfigRepo) Delete(ctx context.Context, id string) error {
	r.hooks.run("before:Chart.Delete", ctx)
	defer r.hooks.run("after:Chart.Delete", ctx)
	return r.MockChartConfigRepository.Delete(ctx, id)
}

func (r *hookChartConfigRepo) ListByDefinition(ctx context.Context, definitionID string) ([]models.ChartConfig, error) {
	r.hooks.run("before:Chart.ListByDefinition", ctx)
	defer r.hooks.run("after:Chart.ListByDefinition", ctx)
	return r.MockChartConfigRepository.ListByDefinition(ctx, definitionID)
}

func (r *hookChartConfigRepo) CountByDefinitionIDs(ctx context.Context, ids []string) (map[string]int, error) {
	r.hooks.run("before:Chart.CountByDefinitionIDs", ctx)
	defer r.hooks.run("after:Chart.CountByDefinitionIDs", ctx)
	return r.MockChartConfigRepository.CountByDefinitionIDs(ctx, ids)
}

func (r *hookChartConfigRepo) DeleteByDefinition(ctx context.Context, definitionID string) error {
	r.hooks.run("before:Chart.DeleteByDefinition", ctx)
	defer r.hooks.run("after:Chart.DeleteByDefinition", ctx)
	return r.MockChartConfigRepository.DeleteByDefinition(ctx, definitionID)
}

// hookBranchOverrideRepo runs the hook of hooks with "before:Branch.<Method>"
// and "after:Branch.<Method>" around the calls of
// MockChartBranchOverrideRepository, which fails on a done context (#656).
type hookBranchOverrideRepo struct {
	*MockChartBranchOverrideRepository
	hooks *hookInstanceRepo
}

func newHookBranchOverrideRepo(hooks *hookInstanceRepo) *hookBranchOverrideRepo {
	return &hookBranchOverrideRepo{MockChartBranchOverrideRepository: NewMockChartBranchOverrideRepository(), hooks: hooks}
}

func (r *hookBranchOverrideRepo) List(ctx context.Context, instanceID string) ([]*models.ChartBranchOverride, error) {
	r.hooks.run("before:Branch.List", ctx)
	defer r.hooks.run("after:Branch.List", ctx)
	return r.MockChartBranchOverrideRepository.List(ctx, instanceID)
}

func (r *hookBranchOverrideRepo) Get(ctx context.Context, instanceID, chartConfigID string) (*models.ChartBranchOverride, error) {
	r.hooks.run("before:Branch.Get", ctx)
	defer r.hooks.run("after:Branch.Get", ctx)
	return r.MockChartBranchOverrideRepository.Get(ctx, instanceID, chartConfigID)
}

func (r *hookBranchOverrideRepo) Set(ctx context.Context, o *models.ChartBranchOverride) error {
	r.hooks.run("before:Branch.Set", ctx)
	defer r.hooks.run("after:Branch.Set", ctx)
	return r.MockChartBranchOverrideRepository.Set(ctx, o)
}

func (r *hookBranchOverrideRepo) Delete(ctx context.Context, instanceID, chartConfigID string) error {
	r.hooks.run("before:Branch.Delete", ctx)
	defer r.hooks.run("after:Branch.Delete", ctx)
	return r.MockChartBranchOverrideRepository.Delete(ctx, instanceID, chartConfigID)
}

func (r *hookBranchOverrideRepo) DeleteByInstance(ctx context.Context, instanceID string) error {
	r.hooks.run("before:Branch.DeleteByInstance", ctx)
	defer r.hooks.run("after:Branch.DeleteByInstance", ctx)
	return r.MockChartBranchOverrideRepository.DeleteByInstance(ctx, instanceID)
}

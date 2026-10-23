package handlers

import (
	"context"

	"backend/internal/models"
)

// hookTemplateRepo runs the hook of hooks with "before:Template.<Method>" and
// "after:Template.<Method>" around the calls of MockStackTemplateRepository,
// which fails on a done context (#656).
type hookTemplateRepo struct {
	*MockStackTemplateRepository
	hooks *hookInstanceRepo
}

func newHookTemplateRepo(hooks *hookInstanceRepo) *hookTemplateRepo {
	return &hookTemplateRepo{MockStackTemplateRepository: NewMockStackTemplateRepository(), hooks: hooks}
}

func (r *hookTemplateRepo) Create(ctx context.Context, t *models.StackTemplate) error {
	r.hooks.run("before:Template.Create", ctx)
	defer r.hooks.run("after:Template.Create", ctx)
	return r.MockStackTemplateRepository.Create(ctx, t)
}

func (r *hookTemplateRepo) FindByID(ctx context.Context, id string) (*models.StackTemplate, error) {
	r.hooks.run("before:Template.FindByID", ctx)
	defer r.hooks.run("after:Template.FindByID", ctx)
	return r.MockStackTemplateRepository.FindByID(ctx, id)
}

func (r *hookTemplateRepo) FindByIDForUpdate(ctx context.Context, id string) (*models.StackTemplate, error) {
	r.hooks.run("before:Template.FindByIDForUpdate", ctx)
	defer r.hooks.run("after:Template.FindByIDForUpdate", ctx)
	return r.MockStackTemplateRepository.FindByIDForUpdate(ctx, id)
}

func (r *hookTemplateRepo) Update(ctx context.Context, t *models.StackTemplate) error {
	r.hooks.run("before:Template.Update", ctx)
	defer r.hooks.run("after:Template.Update", ctx)
	return r.MockStackTemplateRepository.Update(ctx, t)
}

func (r *hookTemplateRepo) Delete(ctx context.Context, id string) error {
	r.hooks.run("before:Template.Delete", ctx)
	defer r.hooks.run("after:Template.Delete", ctx)
	return r.MockStackTemplateRepository.Delete(ctx, id)
}

func (r *hookTemplateRepo) List(ctx context.Context) ([]models.StackTemplate, error) {
	r.hooks.run("before:Template.List", ctx)
	defer r.hooks.run("after:Template.List", ctx)
	return r.MockStackTemplateRepository.List(ctx)
}

func (r *hookTemplateRepo) ListPaged(ctx context.Context, limit, offset int, name string) ([]models.StackTemplate, int64, error) {
	r.hooks.run("before:Template.ListPaged", ctx)
	defer r.hooks.run("after:Template.ListPaged", ctx)
	return r.MockStackTemplateRepository.ListPaged(ctx, limit, offset, name)
}

func (r *hookTemplateRepo) ListPublished(ctx context.Context) ([]models.StackTemplate, error) {
	r.hooks.run("before:Template.ListPublished", ctx)
	defer r.hooks.run("after:Template.ListPublished", ctx)
	return r.MockStackTemplateRepository.ListPublished(ctx)
}

func (r *hookTemplateRepo) ListPublishedPaged(ctx context.Context, limit, offset int, name string) ([]models.StackTemplate, int64, error) {
	r.hooks.run("before:Template.ListPublishedPaged", ctx)
	defer r.hooks.run("after:Template.ListPublishedPaged", ctx)
	return r.MockStackTemplateRepository.ListPublishedPaged(ctx, limit, offset, name)
}

func (r *hookTemplateRepo) ListByOwner(ctx context.Context, ownerID string) ([]models.StackTemplate, error) {
	r.hooks.run("before:Template.ListByOwner", ctx)
	defer r.hooks.run("after:Template.ListByOwner", ctx)
	return r.MockStackTemplateRepository.ListByOwner(ctx, ownerID)
}

func (r *hookTemplateRepo) Count(ctx context.Context) (int64, error) {
	r.hooks.run("before:Template.Count", ctx)
	defer r.hooks.run("after:Template.Count", ctx)
	return r.MockStackTemplateRepository.Count(ctx)
}

// hookTemplateChartRepo runs the hook of hooks with
// "before:TemplateChart.<Method>" and "after:TemplateChart.<Method>" around
// the calls of MockTemplateChartConfigRepository, which fails on a done
// context (#656).
type hookTemplateChartRepo struct {
	*MockTemplateChartConfigRepository
	hooks *hookInstanceRepo
}

func newHookTemplateChartRepo(hooks *hookInstanceRepo) *hookTemplateChartRepo {
	return &hookTemplateChartRepo{MockTemplateChartConfigRepository: NewMockTemplateChartConfigRepository(), hooks: hooks}
}

func (r *hookTemplateChartRepo) Create(ctx context.Context, c *models.TemplateChartConfig) error {
	r.hooks.run("before:TemplateChart.Create", ctx)
	defer r.hooks.run("after:TemplateChart.Create", ctx)
	return r.MockTemplateChartConfigRepository.Create(ctx, c)
}

func (r *hookTemplateChartRepo) FindByID(ctx context.Context, id string) (*models.TemplateChartConfig, error) {
	r.hooks.run("before:TemplateChart.FindByID", ctx)
	defer r.hooks.run("after:TemplateChart.FindByID", ctx)
	return r.MockTemplateChartConfigRepository.FindByID(ctx, id)
}

func (r *hookTemplateChartRepo) Update(ctx context.Context, c *models.TemplateChartConfig) error {
	r.hooks.run("before:TemplateChart.Update", ctx)
	defer r.hooks.run("after:TemplateChart.Update", ctx)
	return r.MockTemplateChartConfigRepository.Update(ctx, c)
}

func (r *hookTemplateChartRepo) Delete(ctx context.Context, id string) error {
	r.hooks.run("before:TemplateChart.Delete", ctx)
	defer r.hooks.run("after:TemplateChart.Delete", ctx)
	return r.MockTemplateChartConfigRepository.Delete(ctx, id)
}

func (r *hookTemplateChartRepo) ListByTemplate(ctx context.Context, templateID string) ([]models.TemplateChartConfig, error) {
	r.hooks.run("before:TemplateChart.ListByTemplate", ctx)
	defer r.hooks.run("after:TemplateChart.ListByTemplate", ctx)
	return r.MockTemplateChartConfigRepository.ListByTemplate(ctx, templateID)
}

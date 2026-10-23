package handlers

import (
	"context"
	"sync"

	"backend/internal/models"
)

// hookValueOverrideRepo runs the hook of hooks with "before:Override.<Method>"
// and "after:Override.<Method>" around the calls of
// MockValueOverrideRepository, which fails on a done context (#656).
type hookValueOverrideRepo struct {
	*MockValueOverrideRepository
	hooks *hookInstanceRepo
}

func newHookValueOverrideRepo(hooks *hookInstanceRepo) *hookValueOverrideRepo {
	return &hookValueOverrideRepo{MockValueOverrideRepository: NewMockValueOverrideRepository(), hooks: hooks}
}

func (r *hookValueOverrideRepo) Create(ctx context.Context, v *models.ValueOverride) error {
	r.hooks.run("before:Override.Create", ctx)
	defer r.hooks.run("after:Override.Create", ctx)
	return r.MockValueOverrideRepository.Create(ctx, v)
}

func (r *hookValueOverrideRepo) FindByID(ctx context.Context, id string) (*models.ValueOverride, error) {
	r.hooks.run("before:Override.FindByID", ctx)
	defer r.hooks.run("after:Override.FindByID", ctx)
	return r.MockValueOverrideRepository.FindByID(ctx, id)
}

func (r *hookValueOverrideRepo) FindByInstanceAndChart(ctx context.Context, instanceID, chartConfigID string) (*models.ValueOverride, error) {
	r.hooks.run("before:Override.FindByInstanceAndChart", ctx)
	defer r.hooks.run("after:Override.FindByInstanceAndChart", ctx)
	return r.MockValueOverrideRepository.FindByInstanceAndChart(ctx, instanceID, chartConfigID)
}

func (r *hookValueOverrideRepo) Update(ctx context.Context, v *models.ValueOverride) error {
	r.hooks.run("before:Override.Update", ctx)
	defer r.hooks.run("after:Override.Update", ctx)
	return r.MockValueOverrideRepository.Update(ctx, v)
}

func (r *hookValueOverrideRepo) Delete(ctx context.Context, id string) error {
	r.hooks.run("before:Override.Delete", ctx)
	defer r.hooks.run("after:Override.Delete", ctx)
	return r.MockValueOverrideRepository.Delete(ctx, id)
}

func (r *hookValueOverrideRepo) ListByInstance(ctx context.Context, instanceID string) ([]models.ValueOverride, error) {
	r.hooks.run("before:Override.ListByInstance", ctx)
	defer r.hooks.run("after:Override.ListByInstance", ctx)
	return r.MockValueOverrideRepository.ListByInstance(ctx, instanceID)
}

func (r *hookValueOverrideRepo) DeleteByInstance(ctx context.Context, instanceID string) error {
	r.hooks.run("before:Override.DeleteByInstance", ctx)
	defer r.hooks.run("after:Override.DeleteByInstance", ctx)
	return r.MockValueOverrideRepository.DeleteByInstance(ctx, instanceID)
}

// hookSharedValuesRepo runs the hook of hooks with "before:Shared.<Method>"
// and "after:Shared.<Method>" around the calls of mockSharedValuesRepo, which
// fails on a done context (#656). The mutex guards the map of the mock: a
// deploy reads it in its own goroutine.
type hookSharedValuesRepo struct {
	repo  *mockSharedValuesRepo
	hooks *hookInstanceRepo
	mu    sync.Mutex
}

func newHookSharedValuesRepo(hooks *hookInstanceRepo) *hookSharedValuesRepo {
	return &hookSharedValuesRepo{repo: newMockSharedValuesRepo(), hooks: hooks}
}

// stored returns the stored record, or nil when it does not exist.
func (r *hookSharedValuesRepo) stored(id string) *models.SharedValues {
	r.mu.Lock()
	defer r.mu.Unlock()
	sv, ok := r.repo.items[id]
	if !ok {
		return nil
	}
	cp := *sv
	return &cp
}

func (r *hookSharedValuesRepo) Create(ctx context.Context, sv *models.SharedValues) error {
	r.hooks.run("before:Shared.Create", ctx)
	defer r.hooks.run("after:Shared.Create", ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.repo.Create(ctx, sv)
}

func (r *hookSharedValuesRepo) FindByID(ctx context.Context, id string) (*models.SharedValues, error) {
	r.hooks.run("before:Shared.FindByID", ctx)
	defer r.hooks.run("after:Shared.FindByID", ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.repo.FindByID(ctx, id)
}

func (r *hookSharedValuesRepo) FindByClusterAndID(ctx context.Context, clusterID, id string) (*models.SharedValues, error) {
	r.hooks.run("before:Shared.FindByClusterAndID", ctx)
	defer r.hooks.run("after:Shared.FindByClusterAndID", ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.repo.FindByClusterAndID(ctx, clusterID, id)
}

func (r *hookSharedValuesRepo) Update(ctx context.Context, sv *models.SharedValues) error {
	r.hooks.run("before:Shared.Update", ctx)
	defer r.hooks.run("after:Shared.Update", ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.repo.Update(ctx, sv)
}

func (r *hookSharedValuesRepo) Delete(ctx context.Context, id string) error {
	r.hooks.run("before:Shared.Delete", ctx)
	defer r.hooks.run("after:Shared.Delete", ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.repo.Delete(ctx, id)
}

func (r *hookSharedValuesRepo) ListByCluster(ctx context.Context, clusterID string) ([]models.SharedValues, error) {
	r.hooks.run("before:Shared.ListByCluster", ctx)
	defer r.hooks.run("after:Shared.ListByCluster", ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.repo.ListByCluster(ctx, clusterID)
}

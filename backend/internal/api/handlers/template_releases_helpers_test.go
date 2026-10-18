package handlers

import (
	"context"
	"encoding/json"
	"errors"

	"backend/internal/models"
	"backend/pkg/dberrors"
)

// workingCopyReleaseRepo emulates templates that were published with their
// current working copy (the state after the snapshot backfill migration).
// When a published template has no stored snapshot, GetLatestByTemplate
// returns a snapshot of the working copy at call time. Tests that seed a
// published template and its charts directly use it; tests of the snapshot
// rules use MockTemplateVersionRepository directly.
type workingCopyReleaseRepo struct {
	*MockTemplateVersionRepository
	templates *MockStackTemplateRepository
	charts    *MockTemplateChartConfigRepository
}

func newWorkingCopyReleaseRepo(templates *MockStackTemplateRepository, charts *MockTemplateChartConfigRepository) *workingCopyReleaseRepo {
	return &workingCopyReleaseRepo{
		MockTemplateVersionRepository: NewMockTemplateVersionRepository(),
		templates:                     templates,
		charts:                        charts,
	}
}

func (r *workingCopyReleaseRepo) GetLatestByTemplate(ctx context.Context, templateID string) (*models.TemplateVersion, error) {
	v, err := r.MockTemplateVersionRepository.GetLatestByTemplate(ctx, templateID)
	if err == nil || !errors.Is(err, dberrors.ErrNotFound) {
		return v, err
	}
	tmpl, tErr := r.templates.FindByID(templateID)
	if tErr != nil || !tmpl.IsPublished {
		return nil, err
	}
	var charts []models.TemplateChartConfig
	if r.charts != nil {
		var cErr error
		if charts, cErr = r.charts.ListByTemplate(templateID); cErr != nil {
			return nil, cErr
		}
	}
	b, mErr := json.Marshal(models.NewTemplateSnapshot(tmpl, charts))
	if mErr != nil {
		return nil, mErr
	}
	return &models.TemplateVersion{
		ID:         "working-release-" + templateID,
		TemplateID: templateID,
		Version:    tmpl.Version,
		Snapshot:   string(b),
	}, nil
}

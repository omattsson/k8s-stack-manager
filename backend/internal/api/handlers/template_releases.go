package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"backend/internal/models"
	"backend/pkg/dberrors"
)

// Template life cycle ("draft and release"):
//
//   - The template row and its template chart configs are the working copy.
//     Edits change the working copy only, also while the template is
//     published.
//   - POST /templates/:id/publish stores the working copy as a snapshot
//     (template_versions) with a version string that is new for the template.
//   - Use Template, Quick Deploy, definition upgrades and the template locked
//     values read the latest snapshot, never the working copy.
//   - An unpublished template keeps its history but cannot be used.

// msgNoPublishedVersion is the 409 message when a template cannot be used
// because it is not published or has no snapshot.
const msgNoPublishedVersion = "Template has no published version"

// errNoPublishedVersion means that a template has no snapshot.
var errNoPublishedVersion = errors.New("template has no published version")

// templateRelease is the released content of a template: its latest snapshot.
type templateRelease struct {
	Version  *models.TemplateVersion
	Snapshot models.TemplateSnapshot
	// Charts are the snapshot charts as template chart configs. ID is the
	// template chart config ID stored in the snapshot (for a legacy snapshot:
	// the ID of the working copy chart with the same name, or empty).
	// CreatedAt is not set.
	Charts []models.TemplateChartConfig
}

// latestTemplateRelease returns the latest snapshot of a template. It returns
// errNoPublishedVersion when the template has no snapshot. chartRepo is
// optional; it fills the chart fields that legacy snapshots do not hold.
func latestTemplateRelease(
	ctx context.Context,
	versionRepo models.TemplateVersionRepository,
	chartRepo models.TemplateChartConfigRepository,
	templateID string,
) (*templateRelease, error) {
	if versionRepo == nil {
		return nil, errNoPublishedVersion
	}
	latest, err := versionRepo.GetLatestByTemplate(ctx, templateID)
	if err != nil {
		if errors.Is(err, dberrors.ErrNotFound) {
			return nil, errNoPublishedVersion
		}
		return nil, fmt.Errorf("get latest template version: %w", err)
	}
	var snapshot models.TemplateSnapshot
	if err := json.Unmarshal([]byte(latest.Snapshot), &snapshot); err != nil {
		return nil, fmt.Errorf("unmarshal template version %s: %w", latest.ID, err)
	}
	var working []models.TemplateChartConfig
	if snapshot.SchemaVersion < 1 && chartRepo != nil {
		working, err = chartRepo.ListByTemplate(templateID)
		if err != nil {
			return nil, fmt.Errorf("list template charts: %w", err)
		}
	}
	return &templateRelease{
		Version:  latest,
		Snapshot: snapshot,
		Charts:   snapshot.ChartConfigs(templateID, working),
	}, nil
}

// releasedTemplateCharts returns the template charts that definitions of a
// template use for locked values and required charts: the charts of the
// latest snapshot. A template without any snapshot (legacy data from before
// snapshots were required) falls back to the working copy.
func releasedTemplateCharts(
	ctx context.Context,
	versionRepo models.TemplateVersionRepository,
	chartRepo models.TemplateChartConfigRepository,
	templateID string,
) ([]models.TemplateChartConfig, error) {
	release, err := latestTemplateRelease(ctx, versionRepo, chartRepo, templateID)
	if err == nil {
		return release.Charts, nil
	}
	if !errors.Is(err, errNoPublishedVersion) {
		return nil, err
	}
	if chartRepo == nil {
		return nil, nil
	}
	return chartRepo.ListByTemplate(templateID)
}

// usableTemplateRelease returns the release that Use Template and Quick
// Deploy apply. The template must be published and have a snapshot;
// otherwise it returns errNoPublishedVersion.
func usableTemplateRelease(
	ctx context.Context,
	tmpl *models.StackTemplate,
	versionRepo models.TemplateVersionRepository,
	chartRepo models.TemplateChartConfigRepository,
) (*templateRelease, error) {
	if !tmpl.IsPublished {
		return nil, errNoPublishedVersion
	}
	return latestTemplateRelease(ctx, versionRepo, chartRepo, tmpl.ID)
}

// resolveChartOverrides maps the chart_overrides of Use Template to chart
// names of the release. A key may be:
//  1. the ID of a release chart (published_charts[].id of GET /templates/:id),
//  2. the ID of a working copy chart (charts[].id) — mapped by chart name,
//  3. a chart name of the release.
//
// Keys that match no chart of the release are ignored (for example a chart
// that exists only in the working copy). working is loaded only when a key
// is not a release chart ID or name.
func resolveChartOverrides(
	overrides map[string]string,
	release []models.TemplateChartConfig,
	loadWorking func() ([]models.TemplateChartConfig, error),
) (map[string]string, error) {
	out := make(map[string]string, len(overrides))
	if len(overrides) == 0 {
		return out, nil
	}
	byID := make(map[string]string, len(release))
	names := make(map[string]struct{}, len(release))
	for _, ch := range release {
		if ch.ID != "" {
			byID[ch.ID] = ch.ChartName
		}
		names[ch.ChartName] = struct{}{}
	}
	var workingByID map[string]string
	for key, values := range overrides {
		if name, ok := byID[key]; ok {
			out[name] = values
			continue
		}
		if _, ok := names[key]; ok {
			out[key] = values
			continue
		}
		if workingByID == nil {
			workingByID = make(map[string]string)
			if loadWorking != nil {
				working, err := loadWorking()
				if err != nil {
					return nil, err
				}
				for _, w := range working {
					workingByID[w.ID] = w.ChartName
				}
			}
		}
		if name, ok := workingByID[key]; ok {
			if _, inRelease := names[name]; inRelease {
				out[name] = values
			}
		}
	}
	return out, nil
}

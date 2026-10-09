package handlers

import (
	"context"
	"fmt"

	"backend/internal/helm"
	"backend/internal/models"
)

// valuesBuilder is the single Helm values pipeline. Every values generation
// path (deploy, bulk deploy, quick deploy, deploy preview, export, compare)
// uses it, so they all render the same values. Merge order per chart:
// cluster shared values (by priority) <- chart defaults <- instance value
// overrides <- template locked values; the per-chart branch override sets
// .Branch and .ImageTag.
//
// Any lookup error is returned: callers fail closed, because values rendered
// with a missing layer would be wrong.
type valuesBuilder struct {
	overrideRepo       models.ValueOverrideRepository
	branchOverrideRepo models.ChartBranchOverrideRepository
	templateChartRepo  models.TemplateChartConfigRepository
	versionRepo        models.TemplateVersionRepository
	userRepo           models.UserRepository
	valuesGen          *helm.ValuesGenerator
	sharedValuesRepo   models.SharedValuesRepository
	resolver           clusterIDResolver
	clusterRepo        models.ClusterRepository
}

// build renders the merged values of each chart. It returns chart name ->
// YAML and the branch overrides used (chart config ID -> branch). ownerName
// sets {{.Owner}}; when empty it is resolved from the instance owner.
func (b *valuesBuilder) build(ctx context.Context, inst *models.StackInstance, def *models.StackDefinition, charts []models.ChartConfig, ownerName string) (map[string]string, map[string]string, error) {
	layers, templateVars, branchMap, err := b.layers(ctx, inst, def, charts, ownerName)
	if err != nil {
		return nil, nil, err
	}

	result := make(map[string]string, len(layers))
	for _, cv := range layers {
		yamlData, err := b.valuesGen.GenerateValues(ctx, helm.GenerateParams{
			ChartName:      cv.ChartName,
			DefaultValues:  cv.DefaultValues,
			LockedValues:   cv.LockedValues,
			OverrideValues: cv.OverrideValues,
			SharedValues:   cv.SharedValues,
			ChartBranch:    cv.ChartBranch,
			TemplateVars:   templateVars,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("generate values for chart %s: %w", cv.ChartName, err)
		}
		result[cv.ChartName] = string(yamlData)
	}
	return result, branchMap, nil
}

// layers collects the values layers of each chart of an instance: shared
// values, chart defaults, value overrides, locked values and the branch
// override. It returns the layers in chart order, the template variables and
// the branch overrides keyed by chart config ID.
func (b *valuesBuilder) layers(ctx context.Context, inst *models.StackInstance, def *models.StackDefinition, charts []models.ChartConfig, ownerName string) ([]helm.ChartValues, helm.TemplateVars, map[string]string, error) {
	lockedMap, err := b.lockedValues(ctx, def)
	if err != nil {
		return nil, helm.TemplateVars{}, nil, fmt.Errorf("build locked values: %w", err)
	}

	sharedValues, err := loadSharedValues(b.sharedValuesRepo, b.resolver, b.clusterRepo, inst)
	if err != nil {
		return nil, helm.TemplateVars{}, nil, fmt.Errorf("load shared values: %w", err)
	}

	overridesMap := make(map[string]string)
	if b.overrideRepo != nil {
		overrides, err := b.overrideRepo.ListByInstance(inst.ID)
		if err != nil {
			return nil, helm.TemplateVars{}, nil, fmt.Errorf("list value overrides: %w", err)
		}
		for _, ov := range overrides {
			overridesMap[ov.ChartConfigID] = ov.Values
		}
	}

	branchMap := make(map[string]string)
	if b.branchOverrideRepo != nil {
		branchOverrides, err := b.branchOverrideRepo.List(inst.ID)
		if err != nil {
			return nil, helm.TemplateVars{}, nil, fmt.Errorf("list branch overrides: %w", err)
		}
		for _, bo := range branchOverrides {
			branchMap[bo.ChartConfigID] = bo.Branch
		}
	}

	if ownerName == "" {
		ownerName = resolveOwnerName(b.userRepo, inst.OwnerID)
	}
	templateVars := helm.TemplateVars{
		Branch:       inst.Branch,
		ImageTag:     helm.SanitizeImageTag(inst.Branch),
		Namespace:    inst.Namespace,
		InstanceName: inst.Name,
		StackName:    def.Name,
		Owner:        ownerName,
	}

	layers := make([]helm.ChartValues, 0, len(charts))
	for _, ch := range charts {
		layers = append(layers, helm.ChartValues{
			ChartName:      ch.ChartName,
			DefaultValues:  ch.DefaultValues,
			LockedValues:   lockedMap[ch.ChartName],
			OverrideValues: overridesMap[ch.ID],
			SharedValues:   sharedValues,
			ChartBranch:    branchMap[ch.ID],
		})
	}
	return layers, templateVars, branchMap, nil
}

// lockedValues returns chart name -> locked values of the definition's
// source template (empty without a template). The locked values come from the
// latest published snapshot, so edits to the working copy apply only after
// the next publish (see releasedTemplateCharts).
func (b *valuesBuilder) lockedValues(ctx context.Context, def *models.StackDefinition) (map[string]string, error) {
	lockedMap := make(map[string]string)
	if def.SourceTemplateID != "" && b.templateChartRepo != nil {
		templateCharts, err := releasedTemplateCharts(ctx, b.versionRepo, b.templateChartRepo, def.SourceTemplateID)
		if err != nil {
			return nil, fmt.Errorf("list template chart configs: %w", err)
		}
		for _, tc := range templateCharts {
			lockedMap[tc.ChartName] = tc.LockedValues
		}
	}
	return lockedMap, nil
}

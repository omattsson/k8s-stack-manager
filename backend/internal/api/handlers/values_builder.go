package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"backend/internal/cluster"
	"backend/internal/helm"
	"backend/internal/imagedigest"
	"backend/internal/models"
	"backend/pkg/dberrors"
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
//
// The imageDigest template function resolves only when digests is set
// (withDigests): deploy, bulk deploy, quick deploy, redeploy and deploy
// preview. Export and compare render it as ""; the values drift checks render
// helm.ImageDigestDriftMask (withDriftMask). Neither makes a registry request.
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
	// digests resolves imageDigest; nil renders "" (see withDigests).
	digests helm.DigestResolver
}

// withDigests makes the build resolve imageDigest through svc, with the
// registry of the cluster of the instance: its host is always allowed and
// gets the cluster credentials. clusterRepo reads the cluster (with the
// decrypted registry password); resolver finds the default cluster of an
// instance without one. The cluster is read at the first imageDigest call
// only, and one resolver serves the whole build, so each reference is looked
// up once. A nil svc keeps imageDigest at "". Returns b for chaining.
func (b *valuesBuilder) withDigests(svc *imagedigest.Service, clusterRepo models.ClusterRepository, inst *models.StackInstance) *valuesBuilder {
	if svc == nil {
		return b
	}
	resolver := b.resolver
	b.digests = &lazyDigests{build: func(ctx context.Context) *imagedigest.Resolver {
		return svc.ForRegistry(instanceRegistry(ctx, resolver, clusterRepo, inst))
	}}
	return b
}

// withDriftMask makes imageDigest render helm.ImageDigestDriftMask, with no
// registry request, for the values drift checks (driftValuesEqual). Returns b
// for chaining.
func (b *valuesBuilder) withDriftMask() *valuesBuilder {
	b.digests = helm.DriftMaskDigests{}
	return b
}

// lazyDigests builds the digest resolver at the first imageDigest call, so a
// build without imageDigest reads no cluster.
type lazyDigests struct {
	build    func(ctx context.Context) *imagedigest.Resolver
	resolver *imagedigest.Resolver
	once     sync.Once
}

func (l *lazyDigests) Digest(ctx context.Context, repository, tag string) string {
	l.once.Do(func() { l.resolver = l.build(ctx) })
	return l.resolver.Digest(ctx, repository, tag)
}

// instanceRegistry returns the registry settings of the cluster of inst (the
// default cluster when inst has none). A failed lookup is logged and gives no
// cluster registry: only IMAGE_DIGEST_REGISTRIES hosts stay allowed,
// anonymously.
func instanceRegistry(ctx context.Context, resolver clusterIDResolver, clusterRepo models.ClusterRepository, inst *models.StackInstance) imagedigest.Registry {
	if clusterRepo == nil || inst == nil {
		return imagedigest.Registry{}
	}
	var (
		cl  *models.Cluster
		err error
	)
	switch {
	case inst.ClusterID != "":
		cl, err = clusterRepo.FindByID(ctx, inst.ClusterID)
	case resolver != nil:
		var id string
		id, err = resolver.ResolveClusterID("")
		if err == nil {
			cl, err = clusterRepo.FindByID(ctx, id)
		}
	default:
		cl, err = clusterRepo.FindDefault(ctx)
	}
	if err != nil {
		if !errors.Is(err, cluster.ErrNoDefaultCluster) && !errors.Is(err, dberrors.ErrNotFound) {
			slog.Warn("imageDigest: cluster lookup failed, the cluster registry is not used", logKeyInstanceID, inst.ID, "error", err)
		}
		return imagedigest.Registry{}
	}
	return imagedigest.Registry{URL: cl.RegistryURL, Username: cl.RegistryUsername, Password: cl.RegistryPassword}
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
			Digests:        b.digests,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("generate values for chart %s: %w", cv.ChartName, err)
		}
		result[cv.ChartName] = string(yamlData)
	}
	// A cancelled build renders "" for imageDigest; deploying that would
	// restart every pod. Fail closed instead (#656, #811).
	if b.digests != nil {
		if err := ctx.Err(); err != nil {
			return nil, nil, fmt.Errorf("resolve image digests: %w", err)
		}
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

	sharedValues, err := loadSharedValues(ctx, b.sharedValuesRepo, b.resolver, b.clusterRepo, inst)
	if err != nil {
		return nil, helm.TemplateVars{}, nil, fmt.Errorf("load shared values: %w", err)
	}

	overridesMap := make(map[string]string)
	if b.overrideRepo != nil {
		overrides, err := b.overrideRepo.ListByInstance(ctx, inst.ID)
		if err != nil {
			return nil, helm.TemplateVars{}, nil, fmt.Errorf("list value overrides: %w", err)
		}
		for _, ov := range overrides {
			overridesMap[ov.ChartConfigID] = ov.Values
		}
	}

	branchMap := make(map[string]string)
	if b.branchOverrideRepo != nil {
		branchOverrides, err := b.branchOverrideRepo.List(ctx, inst.ID)
		if err != nil {
			return nil, helm.TemplateVars{}, nil, fmt.Errorf("list branch overrides: %w", err)
		}
		for _, bo := range branchOverrides {
			branchMap[bo.ChartConfigID] = bo.Branch
		}
	}

	if ownerName == "" {
		ownerName = resolveOwnerName(ctx, b.userRepo, inst.OwnerID)
		// A cancelled lookup falls back to the owner ID: fail closed instead
		// of rendering a wrong Owner variable (#656).
		if err := ctx.Err(); err != nil {
			return nil, helm.TemplateVars{}, nil, fmt.Errorf("resolve owner name: %w", err)
		}
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

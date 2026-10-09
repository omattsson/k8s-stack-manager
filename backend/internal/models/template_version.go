package models

import (
	"context"
	"sort"
	"strings"
	"time"
)

// TemplateVersion represents a versioned snapshot of a stack template at the time of publish.
// The template row and its chart configs are the working copy (draft). A
// snapshot is the released content: Use Template, Quick Deploy, definition
// upgrades and template locked values read the latest snapshot.
type TemplateVersion struct {
	ID            string    `json:"id" gorm:"primaryKey;size:36"`
	TemplateID    string    `json:"template_id" gorm:"size:36;index;not null"`
	Version       string    `json:"version" gorm:"size:50;not null"`
	Snapshot      string    `json:"-" gorm:"type:longtext;not null"`
	ChangeSummary string    `json:"change_summary" gorm:"type:text"`
	CreatedBy     string    `json:"created_by" gorm:"size:100"`
	CreatedAt     time.Time `json:"created_at"`
}

// TemplateSnapshotSchemaVersion is the current snapshot format. Version 0
// (no schema_version key) is the legacy format without source_repo_url,
// build_pipeline_id, chart_path and chart_version on the charts.
const TemplateSnapshotSchemaVersion = 1

// TemplateSnapshot is the serialized structure stored in the Snapshot field.
type TemplateSnapshot struct {
	SchemaVersion int                         `json:"schema_version,omitempty"`
	Template      TemplateSnapshotData        `json:"template"`
	Charts        []TemplateChartSnapshotData `json:"charts"`
}

// TemplateSnapshotData holds the template fields captured at publish time.
type TemplateSnapshotData struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	Category      string `json:"category"`
	DefaultBranch string `json:"default_branch"`
	IsPublished   bool   `json:"is_published"`
	Version       string `json:"version"`
}

// TemplateChartSnapshotData holds the chart config fields captured at publish time.
type TemplateChartSnapshotData struct {
	ChartName     string `json:"chart_name"`
	RepoURL       string `json:"repo_url"`
	DefaultValues string `json:"default_values"`
	LockedValues  string `json:"locked_values"`
	IsRequired    bool   `json:"is_required"`
	SortOrder     int    `json:"sort_order"`
	// Fields below exist from schema version 1.
	// ID is the template chart config ID in the working copy at publish time.
	// It is not content: SameTemplateContent ignores it.
	ID              string `json:"id,omitempty"`
	SourceRepoURL   string `json:"source_repo_url,omitempty"`
	BuildPipelineID string `json:"build_pipeline_id,omitempty"`
	ChartPath       string `json:"chart_path,omitempty"`
	ChartVersion    string `json:"chart_version,omitempty"`
}

// NewTemplateSnapshot captures the working copy of a template and its charts
// in the current snapshot format. Charts are ordered by deploy order, then
// chart name, so equal content gives an equal snapshot.
func NewTemplateSnapshot(tmpl *StackTemplate, charts []TemplateChartConfig) TemplateSnapshot {
	chartSnapshots := make([]TemplateChartSnapshotData, 0, len(charts))
	for _, ch := range charts {
		chartSnapshots = append(chartSnapshots, TemplateChartSnapshotData{
			ID:              ch.ID,
			ChartName:       ch.ChartName,
			RepoURL:         ch.RepositoryURL,
			DefaultValues:   ch.DefaultValues,
			LockedValues:    ch.LockedValues,
			IsRequired:      ch.Required,
			SortOrder:       ch.DeployOrder,
			SourceRepoURL:   ch.SourceRepoURL,
			BuildPipelineID: ch.BuildPipelineID,
			ChartPath:       ch.ChartPath,
			ChartVersion:    ch.ChartVersion,
		})
	}
	sortSnapshotCharts(chartSnapshots)
	return TemplateSnapshot{
		SchemaVersion: TemplateSnapshotSchemaVersion,
		Template: TemplateSnapshotData{
			Name:          tmpl.Name,
			Description:   tmpl.Description,
			Category:      tmpl.Category,
			DefaultBranch: tmpl.DefaultBranch,
			IsPublished:   tmpl.IsPublished,
			Version:       tmpl.Version,
		},
		Charts: chartSnapshots,
	}
}

// sortSnapshotCharts orders charts by sort order, then chart name.
func sortSnapshotCharts(charts []TemplateChartSnapshotData) {
	sort.SliceStable(charts, func(i, j int) bool {
		if charts[i].SortOrder != charts[j].SortOrder {
			return charts[i].SortOrder < charts[j].SortOrder
		}
		return charts[i].ChartName < charts[j].ChartName
	})
}

// SameTemplateContent reports whether two snapshots have the same content:
// the template fields (name, description, category, default branch, version)
// and the charts. The publish state is not content. When one snapshot uses
// the legacy format, the chart fields that the legacy format does not hold
// are not compared. Values are compared after NormalizeValues.
func SameTemplateContent(a, b TemplateSnapshot) bool {
	if a.Template.Name != b.Template.Name ||
		a.Template.Description != b.Template.Description ||
		a.Template.Category != b.Template.Category ||
		a.Template.DefaultBranch != b.Template.DefaultBranch ||
		a.Template.Version != b.Template.Version {
		return false
	}
	if len(a.Charts) != len(b.Charts) {
		return false
	}
	legacy := a.SchemaVersion < 1 || b.SchemaVersion < 1
	ac := append([]TemplateChartSnapshotData(nil), a.Charts...)
	bc := append([]TemplateChartSnapshotData(nil), b.Charts...)
	sortSnapshotCharts(ac)
	sortSnapshotCharts(bc)
	for i := range ac {
		x, y := ac[i], bc[i]
		if x.ChartName != y.ChartName ||
			x.RepoURL != y.RepoURL ||
			NormalizeValues(x.DefaultValues) != NormalizeValues(y.DefaultValues) ||
			NormalizeValues(x.LockedValues) != NormalizeValues(y.LockedValues) ||
			x.IsRequired != y.IsRequired ||
			x.SortOrder != y.SortOrder {
			return false
		}
		if legacy {
			continue
		}
		if x.SourceRepoURL != y.SourceRepoURL ||
			x.BuildPipelineID != y.BuildPipelineID ||
			x.ChartPath != y.ChartPath ||
			x.ChartVersion != y.ChartVersion {
			return false
		}
	}
	return true
}

// NormalizeValues removes trailing spaces, tabs and line breaks from a
// values string. Every content comparison of template values (publish,
// has_unpublished_changes, version diff, upgrade diff) uses it.
func NormalizeValues(s string) string {
	return strings.TrimRight(s, " \t\r\n")
}

// ChartConfigs converts the snapshot charts to template chart configs. The ID
// is the template chart config ID stored in the snapshot. For a legacy
// snapshot the fields that the legacy format does not hold (ID included) come
// from the working copy chart with the same name, if there is one.
func (s TemplateSnapshot) ChartConfigs(templateID string, working []TemplateChartConfig) []TemplateChartConfig {
	workingByName := make(map[string]TemplateChartConfig, len(working))
	for _, w := range working {
		workingByName[w.ChartName] = w
	}
	out := make([]TemplateChartConfig, 0, len(s.Charts))
	for _, ch := range s.Charts {
		tc := TemplateChartConfig{
			ID:              ch.ID,
			StackTemplateID: templateID,
			ChartName:       ch.ChartName,
			RepositoryURL:   ch.RepoURL,
			SourceRepoURL:   ch.SourceRepoURL,
			BuildPipelineID: ch.BuildPipelineID,
			ChartPath:       ch.ChartPath,
			ChartVersion:    ch.ChartVersion,
			DefaultValues:   ch.DefaultValues,
			LockedValues:    ch.LockedValues,
			DeployOrder:     ch.SortOrder,
			Required:        ch.IsRequired,
		}
		if s.SchemaVersion < 1 {
			if w, ok := workingByName[ch.ChartName]; ok {
				tc.ID = w.ID
				tc.SourceRepoURL = w.SourceRepoURL
				tc.BuildPipelineID = w.BuildPipelineID
				tc.ChartPath = w.ChartPath
				tc.ChartVersion = w.ChartVersion
			}
		}
		out = append(out, tc)
	}
	return out
}

// TemplateVersionRepository defines data access operations for template versions.
type TemplateVersionRepository interface {
	Create(ctx context.Context, version *TemplateVersion) error
	ListByTemplate(ctx context.Context, templateID string) ([]TemplateVersion, error)
	GetByID(ctx context.Context, templateID, id string) (*TemplateVersion, error)
	GetLatestByTemplate(ctx context.Context, templateID string) (*TemplateVersion, error)
}

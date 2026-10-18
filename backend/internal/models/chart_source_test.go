package models

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeChartSourcePrefix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "oci", raw: "oci://registry.example.com/charts", want: "oci://registry.example.com/charts"},
		{name: "trailing slash and spaces", raw: " https://charts.example.com/ ", want: "https://charts.example.com"},
		{name: "http", raw: "http://charts.local", want: "http://charts.local"},
		{name: "no scheme", raw: "registry.example.com/charts", wantErr: true},
		{name: "no host", raw: "oci://", wantErr: true},
		{name: "bad scheme", raw: "file:///charts", wantErr: true},
		{name: "dot dot", raw: "oci://registry.example.com/charts/..", wantErr: true},
		{name: "user info", raw: "https://user@charts.example.com", wantErr: true},
		{name: "query", raw: "https://charts.example.com?x=1", wantErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := NormalizeChartSourcePrefix(tt.raw)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCheckChartSourceAgainstAllowlist(t *testing.T) {
	t.Parallel()
	allowed := []string{"oci://registry.example.com/charts", "https://charts.example.com"}
	tests := []struct {
		name      string
		allowed   []string
		repo      string
		chartPath string
		chartName string
		wantErr   bool
	}{
		{name: "no allowlist allows all", allowed: nil, repo: "https://evil.example.net", chartName: "x"},
		{name: "oci repo in list", allowed: allowed, repo: "oci://registry.example.com/charts", chartName: "web"},
		{name: "oci repo subpath", allowed: allowed, repo: "oci://registry.example.com/charts/team/", chartPath: "web"},
		{name: "oci full ref in chart path", allowed: allowed, chartPath: "oci://registry.example.com/charts/web"},
		{name: "https repo with chart name", allowed: allowed, repo: "https://charts.example.com", chartName: "web"},
		{name: "other host", allowed: allowed, repo: "oci://registry.example.net/charts", chartName: "web", wantErr: true},
		{name: "prefix without path boundary", allowed: allowed, repo: "oci://registry.example.com/charts-evil", chartName: "web", wantErr: true},
		{name: "host prefix trick", allowed: allowed, repo: "https://charts.example.com.evil.net", chartName: "web", wantErr: true},
		{name: "dot dot in chart path", allowed: allowed, repo: "oci://registry.example.com/charts", chartPath: "../other/web", wantErr: true},
		{name: "absolute chart path with repo", allowed: allowed, repo: "https://charts.example.com", chartPath: "oci://evil.example.net/web", wantErr: true},
		{name: "local path without repo", allowed: allowed, chartPath: "/charts/web", wantErr: true},
		{name: "repo alias without repo", allowed: allowed, chartPath: "bitnami/redis", wantErr: true},
		{name: "chart archive url outside", allowed: allowed, chartPath: "https://evil.example.net/web-1.0.0.tgz", wantErr: true},
		{name: "chart archive url inside", allowed: allowed, chartPath: "https://charts.example.com/web-1.0.0.tgz"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := checkChartSource(tt.allowed, tt.repo, tt.chartPath, tt.chartName)
			if tt.wantErr {
				assert.True(t, errors.Is(err, ErrChartSourceNotAllowed), "got %v", err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestChartReference(t *testing.T) {
	t.Parallel()
	ref, repo := ChartReference("oci://r.example/c/", "app", "web")
	assert.Equal(t, "oci://r.example/c/app", ref)
	assert.Empty(t, repo)
	ref, repo = ChartReference("https://charts.example", "", "web")
	assert.Equal(t, "web", ref)
	assert.Equal(t, "https://charts.example", repo)
}

// TestChartConfigValidateUsesAllowlist sets the process allowlist, so it does
// not run in parallel (parallel tests wait until the sequential tests end).
func TestChartConfigValidateUsesAllowlist(t *testing.T) {
	SetAllowedChartSources([]string{"oci://registry.example.com/charts"})
	t.Cleanup(func() { SetAllowedChartSources(nil) })

	ok := &ChartConfig{StackDefinitionID: "d", ChartName: "web", RepositoryURL: "oci://registry.example.com/charts"}
	assert.NoError(t, ok.Validate())
	bad := &ChartConfig{StackDefinitionID: "d", ChartName: "web", RepositoryURL: "https://evil.example.net"}
	assert.ErrorIs(t, bad.Validate(), ErrChartSourceNotAllowed)
	tbad := &TemplateChartConfig{StackTemplateID: "t", ChartName: "web", RepositoryURL: "https://evil.example.net"}
	assert.ErrorIs(t, tbad.Validate(), ErrChartSourceNotAllowed)

	SetAllowedChartSources(nil)
	assert.NoError(t, bad.Validate())
}

package config

import (
	"fmt"
	"strings"

	"backend/internal/models"
)

// ParseAllowedChartRepos parses ALLOWED_CHART_REPOS: a comma-separated list
// of chart repository URL prefixes (for example
// "oci://registry.example.com/charts,https://charts.example.com"). Each entry
// must be an oci, https or http URL with a host (see
// models.NormalizeChartSourcePrefix). Empty input returns nil: every chart
// source is allowed.
func ParseAllowedChartRepos(raw string) ([]string, error) {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		p, err := models.NormalizeChartSourcePrefix(part)
		if err != nil {
			return nil, fmt.Errorf("ALLOWED_CHART_REPOS: %w", err)
		}
		out = append(out, p)
	}
	return out, nil
}

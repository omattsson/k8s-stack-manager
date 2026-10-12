package models

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
)

// ErrChartSourceNotAllowed is returned when a chart location is not under
// one of the allowed chart repositories (ALLOWED_CHART_REPOS).
var ErrChartSourceNotAllowed = errors.New("chart source not allowed")

// allowedChartSources holds the normalized ALLOWED_CHART_REPOS prefixes.
// nil or empty = every chart source is allowed (the default).
var allowedChartSources atomic.Pointer[[]string]

// SetAllowedChartSources sets the chart repository allowlist for the process.
// The prefixes must be normalized with NormalizeChartSourcePrefix. An empty
// list allows every chart source. main calls it once at startup.
func SetAllowedChartSources(prefixes []string) {
	cp := append([]string(nil), prefixes...)
	allowedChartSources.Store(&cp)
}

// AllowedChartSources returns the configured allowlist (nil = all allowed).
func AllowedChartSources() []string {
	p := allowedChartSources.Load()
	if p == nil {
		return nil
	}
	return *p
}

// NormalizeChartSourcePrefix checks one ALLOWED_CHART_REPOS entry and returns
// it without a trailing slash. An entry must be an absolute URL with a scheme
// and a host (for example oci://registry.example.com/charts or
// https://charts.example.com), without "..", whitespace, a query or a
// fragment.
func NormalizeChartSourcePrefix(raw string) (string, error) {
	p := strings.TrimRight(strings.TrimSpace(raw), "/")
	scheme, rest, ok := strings.Cut(p, "://")
	if !ok || scheme == "" || rest == "" {
		return "", fmt.Errorf("%q: must be a URL with a scheme and a host, for example oci://registry.example.com/charts", raw)
	}
	switch strings.ToLower(scheme) {
	case "oci", "https", "http":
	default:
		return "", fmt.Errorf("%q: scheme must be oci, https or http", raw)
	}
	if strings.ContainsAny(p, " \t\r\n?#@\\") || strings.Contains(p, "..") {
		return "", fmt.Errorf("%q: must not contain whitespace, '?', '#', '@', '\\' or '..'", raw)
	}
	return p, nil
}

// ChartReference returns the chart reference and the --repo value that the
// deployer passes to helm. An OCI repository URL becomes part of the chart
// reference; any other repository URL is passed as --repo.
func ChartReference(repositoryURL, chartPath, chartName string) (chartRef, repoURL string) {
	chartRef = chartPath
	if chartRef == "" {
		chartRef = chartName
	}
	repoURL = repositoryURL
	if strings.HasPrefix(repoURL, "oci://") {
		chartRef = strings.TrimRight(repoURL, "/") + "/" + chartRef
		repoURL = ""
	}
	return chartRef, repoURL
}

// CheckChartSource checks the chart location of a chart config against the
// ALLOWED_CHART_REPOS allowlist. Without an allowlist every location passes.
//
// The location is the chart reference that helm resolves: the OCI reference,
// the --repo URL plus the chart name, or the chart path alone (a local path,
// an OCI reference or a chart archive URL). It must equal an allowed prefix or
// start with the prefix followed by "/". A chart path with a scheme or a
// leading "/" next to a repository URL, and any ".." are rejected, so a chart
// cannot leave the allowed repository.
func CheckChartSource(repositoryURL, chartPath, chartName string) error {
	return checkChartSource(AllowedChartSources(), repositoryURL, chartPath, chartName)
}

// checkChartSource is CheckChartSource with an explicit allowlist.
func checkChartSource(allowed []string, repositoryURL, chartPath, chartName string) error {
	if len(allowed) == 0 {
		return nil
	}
	location, err := chartLocation(repositoryURL, chartPath, chartName)
	if err != nil {
		return err
	}
	for _, prefix := range allowed {
		if location == prefix || strings.HasPrefix(location, prefix+"/") {
			return nil
		}
	}
	return fmt.Errorf("%w: %q is not in an allowed chart repository", ErrChartSourceNotAllowed, location)
}

// chartLocation builds the location that CheckChartSource compares.
func chartLocation(repositoryURL, chartPath, chartName string) (string, error) {
	repo := strings.TrimSpace(repositoryURL)
	ref := chartPath
	if ref == "" {
		ref = chartName
	}
	if repo != "" && (strings.Contains(ref, "://") || strings.HasPrefix(ref, "/")) {
		return "", fmt.Errorf("%w: chart_path must be relative to repository_url", ErrChartSourceNotAllowed)
	}
	var location string
	if repo == "" {
		location = ref
	} else {
		location = strings.TrimRight(repo, "/") + "/" + ref
	}
	if location == "" {
		return "", fmt.Errorf("%w: no chart location", ErrChartSourceNotAllowed)
	}
	if strings.Contains(location, "..") || strings.ContainsAny(location, " \t\r\n\\") {
		return "", fmt.Errorf("%w: chart location must not contain '..', '\\' or whitespace", ErrChartSourceNotAllowed)
	}
	return location, nil
}

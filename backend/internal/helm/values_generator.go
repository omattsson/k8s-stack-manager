package helm

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

// TemplateVars holds the variables available for substitution in YAML string values.
type TemplateVars struct {
	Branch       string
	Namespace    string
	InstanceName string
	StackName    string
	Owner        string
}

// GenerateParams holds parameters for single-chart values generation.
type GenerateParams struct {
	ChartName      string
	DefaultValues  string
	LockedValues   string
	OverrideValues string
	ChartBranch    string // Per-chart branch override; if non-empty, overrides TemplateVars.Branch.
	TemplateVars   TemplateVars
}

// ChartValues holds the value layers for a single chart.
type ChartValues struct {
	ChartName      string
	DefaultValues  string
	LockedValues   string
	OverrideValues string
	ChartBranch    string // Per-chart branch override; passed through to GenerateParams.ChartBranch.
}

// GenerateAllParams holds parameters for multi-chart values generation.
type GenerateAllParams struct {
	Charts       []ChartValues
	TemplateVars TemplateVars
}

// ValuesGenerator merges YAML value layers and substitutes template variables.
type ValuesGenerator struct{}

// NewValuesGenerator creates a new ValuesGenerator.
func NewValuesGenerator() *ValuesGenerator {
	return &ValuesGenerator{}
}

// GenerateValues produces a merged values.yaml for a single chart.
// Merge order: defaults ← overrides ← locked (locked always wins).
// Template variables are substituted after merging.
func (g *ValuesGenerator) GenerateValues(_ context.Context, params GenerateParams) ([]byte, error) {
	defaults, err := parseYAML(params.DefaultValues)
	if err != nil {
		return nil, fmt.Errorf("parsing default values: %w", err)
	}

	overrides, err := parseYAML(params.OverrideValues)
	if err != nil {
		return nil, fmt.Errorf("parsing override values: %w", err)
	}

	locked, err := parseYAML(params.LockedValues)
	if err != nil {
		return nil, fmt.Errorf("parsing locked values: %w", err)
	}

	// Merge: defaults ← overrides ← locked
	merged := deepMerge(defaults, overrides)
	merged = deepMerge(merged, locked)

	// Apply per-chart branch override if specified.
	vars := params.TemplateVars
	if params.ChartBranch != "" {
		vars.Branch = params.ChartBranch
	}

	// Substitute template variables in all string values
	merged, err = substituteVars(merged, vars)
	if err != nil {
		return nil, fmt.Errorf("substituting template variables: %w", err)
	}

	return marshalYAML(merged)
}

// GenerateAllValues produces merged values for every chart, returning chartName → YAML bytes.
func (g *ValuesGenerator) GenerateAllValues(ctx context.Context, params GenerateAllParams) (map[string][]byte, error) {
	result := make(map[string][]byte, len(params.Charts))

	for _, chart := range params.Charts {
		data, err := g.GenerateValues(ctx, GenerateParams{
			ChartName:      chart.ChartName,
			DefaultValues:  chart.DefaultValues,
			LockedValues:   chart.LockedValues,
			OverrideValues: chart.OverrideValues,
			ChartBranch:    chart.ChartBranch,
			TemplateVars:   params.TemplateVars,
		})
		if err != nil {
			return nil, fmt.Errorf("generating values for chart %q: %w", chart.ChartName, err)
		}
		result[chart.ChartName] = data
	}

	return result, nil
}

// ExportAsZip returns a zip archive with one values.yaml per chart in chartname/ directories.
func (g *ValuesGenerator) ExportAsZip(ctx context.Context, params GenerateAllParams) ([]byte, error) {
	allValues, err := g.GenerateAllValues(ctx, params)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	for chartName, data := range allValues {
		fw, err := zw.Create(chartName + "/values.yaml")
		if err != nil {
			return nil, fmt.Errorf("creating zip entry for %q: %w", chartName, err)
		}
		if _, err := fw.Write(data); err != nil {
			return nil, fmt.Errorf("writing zip entry for %q: %w", chartName, err)
		}
	}

	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("closing zip writer: %w", err)
	}

	return buf.Bytes(), nil
}

// parseYAML parses a YAML string into a map. Returns empty map for empty input.
func parseYAML(s string) (map[string]interface{}, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return map[string]interface{}{}, nil
	}

	var m map[string]interface{}
	if err := yaml.Unmarshal([]byte(s), &m); err != nil {
		return nil, err
	}
	if m == nil {
		return map[string]interface{}{}, nil
	}
	return m, nil
}

// deepMerge merges src into dst recursively.
// - Maps are deep-merged (src keys override dst keys).
// - Arrays/scalars from src replace dst entirely.
// - nil values in src remove the key from dst.
func deepMerge(dst, src map[string]interface{}) map[string]interface{} {
	if dst == nil {
		dst = map[string]interface{}{}
	}
	if src == nil {
		return dst
	}

	out := make(map[string]interface{}, len(dst))
	for k, v := range dst {
		out[k] = v
	}

	for k, srcVal := range src {
		if srcVal == nil {
			delete(out, k)
			continue
		}

		dstVal, exists := out[k]
		if !exists {
			out[k] = srcVal
			continue
		}

		srcMap, srcIsMap := toMap(srcVal)
		dstMap, dstIsMap := toMap(dstVal)

		if srcIsMap && dstIsMap {
			out[k] = deepMerge(dstMap, srcMap)
		} else {
			out[k] = srcVal
		}
	}

	return out
}

// toMap attempts to convert an interface{} to map[string]interface{}.
func toMap(v interface{}) (map[string]interface{}, bool) {
	switch m := v.(type) {
	case map[string]interface{}:
		return m, true
	case map[interface{}]interface{}:
		// yaml.v3 can produce map[string]interface{} but handle this for safety
		result := make(map[string]interface{}, len(m))
		for k, val := range m {
			result[fmt.Sprintf("%v", k)] = val
		}
		return result, true
	}
	return nil, false
}

// substituteVars walks the merged map and applies Go template substitution to all string values.
func substituteVars(m map[string]interface{}, vars TemplateVars) (map[string]interface{}, error) {
	result := make(map[string]interface{}, len(m))
	for k, v := range m {
		substituted, err := substituteValue(v, vars)
		if err != nil {
			return nil, fmt.Errorf("key %q: %w", k, err)
		}
		result[k] = substituted
	}
	return result, nil
}

// substituteValue recursively substitutes template variables in a value.
func substituteValue(v interface{}, vars TemplateVars) (interface{}, error) {
	switch val := v.(type) {
	case string:
		if !strings.Contains(val, "{{") {
			return val, nil
		}
		tmpl, err := template.New("val").Option("missingkey=error").Parse(val)
		if err != nil {
			return nil, fmt.Errorf("parsing template %q: %w", val, err)
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, vars); err != nil {
			return nil, fmt.Errorf("executing template %q: %w", val, err)
		}
		return buf.String(), nil

	case map[string]interface{}:
		return substituteVars(val, vars)

	case []interface{}:
		out := make([]interface{}, len(val))
		for i, item := range val {
			substituted, err := substituteValue(item, vars)
			if err != nil {
				return nil, err
			}
			out[i] = substituted
		}
		return out, nil

	default:
		return v, nil
	}
}

// marshalYAML marshals a map to clean, readable YAML bytes.
func marshalYAML(m map[string]interface{}) ([]byte, error) {
	if len(m) == 0 {
		return []byte("{}\n"), nil
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(m); err != nil {
		return nil, fmt.Errorf("marshaling YAML: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("closing YAML encoder: %w", err)
	}
	return buf.Bytes(), nil
}

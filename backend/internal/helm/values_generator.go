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
	ImageTag     string // Docker-safe tag derived from Branch (slashes→dashes, truncated, lowercase)
	Namespace    string
	InstanceName string
	StackName    string
	Owner        string
}

// SanitizeImageTag converts a git branch name to a valid Docker image tag.
// Docker tags: [a-zA-Z0-9_.-], max 128 chars, cannot start with '.' or '-'.
func SanitizeImageTag(branch string) string {
	tag := strings.ToLower(branch)
	tag = strings.NewReplacer(
		"/", "-",
		" ", "-",
		"_", "-",
	).Replace(tag)

	// Remove any characters not valid in Docker tags
	var cleaned strings.Builder
	for _, r := range tag {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.' {
			cleaned.WriteRune(r)
		}
	}
	tag = cleaned.String()

	// Strip leading dots/dashes
	tag = strings.TrimLeft(tag, "-.")

	// Truncate to 128 chars (Docker tag limit)
	if len(tag) > 128 {
		tag = tag[:128]
	}

	if tag == "" {
		tag = "latest"
	}
	return tag
}

// GenerateParams holds parameters for single-chart values generation.
type GenerateParams struct {
	ChartName      string
	DefaultValues  string
	LockedValues   string
	OverrideValues string
	SharedValues   []string // YAML strings, already sorted by priority (low→high)
	ChartBranch    string   // Per-chart branch override; if non-empty, overrides TemplateVars.Branch.
	TemplateVars   TemplateVars
}

// ChartValues holds the value layers for a single chart.
type ChartValues struct {
	ChartName      string
	DefaultValues  string
	LockedValues   string
	OverrideValues string
	SharedValues   []string // YAML strings, already sorted by priority (low→high)
	ChartBranch    string   // Per-chart branch override; passed through to GenerateParams.ChartBranch.
}

// GenerateAllParams holds parameters for multi-chart values generation.
type GenerateAllParams struct {
	Charts       []ChartValues
	TemplateVars TemplateVars
}

// ValuesGenerator merges YAML value layers and substitutes template variables.
// funcMap holds user-registered template functions available from YAML values.
// Register functions at startup — concurrent registration while generating is
// not safe (map reads during Execute assume no writer).
type ValuesGenerator struct {
	funcMap template.FuncMap
}

// NewValuesGenerator creates a new ValuesGenerator with no custom funcs.
// Callers extend the generator via RegisterFunc before putting it into use.
func NewValuesGenerator() *ValuesGenerator {
	return &ValuesGenerator{funcMap: template.FuncMap{}}
}

// RegisterFunc attaches a user-defined template function callable from YAML
// string values (e.g. `image: "{{ .Owner | dnsify }}"`). Returns g for chaining.
//
// Register at startup, before the generator is used by any request. fn must
// match one of the function signatures accepted by text/template (see the
// text/template docs) — otherwise Execute will fail at render time.
//
// Built-in variables on TemplateVars (.Branch, .ImageTag, .Namespace,
// .InstanceName, .StackName, .Owner) are always available; registered funcs
// supplement them.
func (g *ValuesGenerator) RegisterFunc(name string, fn any) *ValuesGenerator {
	if g.funcMap == nil {
		g.funcMap = template.FuncMap{}
	}
	g.funcMap[name] = fn
	return g
}

// GenerateValues produces a merged values.yaml for a single chart.
// Merge order: shared values ← defaults ← overrides ← locked (locked always wins).
// Template variables are substituted after merging.
func (g *ValuesGenerator) GenerateValues(_ context.Context, params GenerateParams) ([]byte, error) {
	// Start with shared values (lowest priority, applied first).
	merged := make(map[string]interface{})
	for _, sv := range params.SharedValues {
		parsed, err := parseYAML(sv)
		if err != nil {
			continue // skip invalid shared values YAML
		}
		merged = deepMerge(merged, parsed)
	}

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

	// Merge: shared ← defaults ← overrides ← locked
	merged = deepMerge(merged, defaults)
	merged = deepMerge(merged, overrides)
	merged = deepMerge(merged, locked)

	// Apply per-chart branch override if specified.
	vars := params.TemplateVars
	if params.ChartBranch != "" {
		vars.Branch = params.ChartBranch
	}

	// Substitute template variables in all string values
	merged, err = substituteVars(merged, vars, g.funcMap)
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
			SharedValues:   chart.SharedValues,
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
// funcs may be nil; when non-nil, registered functions become callable from templates.
func substituteVars(m map[string]interface{}, vars TemplateVars, funcs template.FuncMap) (map[string]interface{}, error) {
	result := make(map[string]interface{}, len(m))
	for k, v := range m {
		substituted, err := substituteValue(v, vars, funcs)
		if err != nil {
			return nil, fmt.Errorf("key %q: %w", k, err)
		}
		result[k] = substituted
	}
	return result, nil
}

// substituteValue recursively substitutes template variables in a value.
func substituteValue(v interface{}, vars TemplateVars, funcs template.FuncMap) (interface{}, error) {
	switch val := v.(type) {
	case string:
		if !strings.Contains(val, "{{") {
			return val, nil
		}
		tmpl := template.New("val").Option("missingkey=error")
		if len(funcs) > 0 {
			tmpl = tmpl.Funcs(funcs)
		}
		tmpl, err := tmpl.Parse(val)
		if err != nil {
			return nil, fmt.Errorf("parsing template %q: %w", val, err)
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, vars); err != nil {
			return nil, fmt.Errorf("executing template %q: %w", val, err)
		}
		return buf.String(), nil

	case map[string]interface{}:
		return substituteVars(val, vars, funcs)

	case []interface{}:
		out := make([]interface{}, len(val))
		for i, item := range val {
			substituted, err := substituteValue(item, vars, funcs)
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

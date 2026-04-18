package helm

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRegisterFunc_CustomFunctionsCallableFromTemplates covers the
// RegisterFunc extension point: users can attach helpers (e.g. for DNS
// sanitisation, secret references) and call them from any YAML string.
func TestRegisterFunc_CustomFunctionsCallableFromTemplates(t *testing.T) {
	t.Parallel()

	gen := NewValuesGenerator().
		RegisterFunc("dnsify", func(s string) string {
			return strings.ToLower(strings.ReplaceAll(s, "_", "-"))
		}).
		RegisterFunc("upper", strings.ToUpper)

	params := GenerateParams{
		ChartName:    "web",
		DefaultValues: `host: "{{ .Owner | dnsify }}.example.com"
label: "{{ upper .InstanceName }}"
`,
		TemplateVars: TemplateVars{
			InstanceName: "demo",
			Owner:        "Alice_Smith",
		},
	}
	got, err := gen.GenerateValues(context.Background(), params)
	require.NoError(t, err)

	out := string(got)
	assert.Contains(t, out, "host: alice-smith.example.com")
	assert.Contains(t, out, "label: DEMO")
}

// TestRegisterFunc_NilGeneratorIsStillUsable guards against accidental
// regressions: a generator built with NewValuesGenerator() (no custom funcs)
// must still render built-in variables.
func TestRegisterFunc_NilFuncMapDoesNotBreakBuiltins(t *testing.T) {
	t.Parallel()

	gen := NewValuesGenerator()
	params := GenerateParams{
		ChartName:    "web",
		DefaultValues: `owner: "{{ .Owner }}"`,
		TemplateVars: TemplateVars{Owner: "alice"},
	}
	got, err := gen.GenerateValues(context.Background(), params)
	require.NoError(t, err)
	assert.Equal(t, "owner: alice\n", string(got))
}

// TestRegisterFunc_UnknownFunctionParseError surfaces a template parse error
// when a template references a function that was never registered.
func TestRegisterFunc_UnknownFunctionParseError(t *testing.T) {
	t.Parallel()

	gen := NewValuesGenerator()
	_, err := gen.GenerateValues(context.Background(), GenerateParams{
		ChartName:     "web",
		DefaultValues: `x: "{{ nope .Owner }}"`,
		TemplateVars:  TemplateVars{Owner: "alice"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nope")
}

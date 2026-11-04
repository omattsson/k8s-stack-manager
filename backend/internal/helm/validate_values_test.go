package helm

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// overrideValuesCase is one row of testdata/override_values_cases.json. The
// browser check reads the same file (frontend/src/utils/__tests__/yamlValidation.test.ts):
// a value that this parser accepts must not be blocked there.
type overrideValuesCase struct {
	Name    string `json:"name"`
	Input   string `json:"input"`
	Browser string `json:"browser"`
	API     bool   `json:"api"`
}

// TestValidateValues records what the deploy parser does with override values,
// and that the check agrees with the parse step of GenerateValues.
func TestValidateValues(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("testdata/override_values_cases.json")
	require.NoError(t, err)
	var tests []overrideValuesCase
	require.NoError(t, json.Unmarshal(data, &tests))
	require.NotEmpty(t, tests)

	for _, tt := range tests {
		tt := tt
		t.Run(tt.Name, func(t *testing.T) {
			t.Parallel()
			err := ValidateValues(tt.Input)
			if err != nil {
				t.Logf("%q: %v", tt.Input, err)
			}
			assert.Equal(t, tt.API, err == nil, "ValidateValues(%q): %v", tt.Input, err)

			_, genErr := NewValuesGenerator().GenerateValues(context.Background(), GenerateParams{OverrideValues: tt.Input})
			if tt.API {
				assert.NoError(t, genErr, "GenerateValues(%q)", tt.Input)
			} else {
				assert.Error(t, genErr, "GenerateValues(%q)", tt.Input)
			}
		})
	}
}

// TestValidateValuesMessage checks the text that the API returns to the user.
func TestValidateValuesMessage(t *testing.T) {
	t.Parallel()

	const quoteHint = `Put a template value in quotes, for example tag: "{{.Branch}}"`
	tests := []struct {
		name        string
		input       string
		contains    []string
		notContains []string
		wantHint    bool
	}{
		{name: "scalar", input: "secret-looking-value", contains: []string{"must be a map"}, notContains: []string{"secret-looking"}},
		{name: "list", input: "- secret-looking", contains: []string{"must be a map"}, notContains: []string{"secret-looking"}},
		{name: "unquoted template", input: "tag: {{.Branch}}", contains: []string{"invalid map key"}, wantHint: true},
		{name: "template key", input: "{{.Branch}}: x", contains: []string{"line 1", "a map key must be a plain value"}, wantHint: true},
		{name: "duplicate keys", input: "a: 1\na: 2", contains: []string{`mapping key "a" already defined`}},
		{name: "broken", input: "foo: [unclosed\n  bar: : baz", contains: []string{"line 1"}},
		{name: "alias to a secret", input: "password: *Abc123", contains: []string{"unknown anchor referenced"}, notContains: []string{"Abc123"}},
		{name: "tagged secret", input: "password: !!int hunter2", contains: []string{"!!str as a !!int"}, notContains: []string{"hunter2"}},
		{name: "tagged secret with a backtick", input: "password: !!int abc`def", contains: []string{"!!str as a !!int"}, notContains: []string{"abc", "def", "`"}},
		{name: "tagged secret with backticks", input: "password: !!int a`b`c`secretrest", contains: []string{"!!str as a !!int"}, notContains: []string{"secretrest", "`"}},
		{name: "duplicate keys beside a template in a quoted string", input: "a: \"x: {{.Branch}}\"\na: 2", contains: []string{"already defined"}},
		{name: "broken value beside a template in a comment", input: "# tag: {{.Branch}}\na: b: c", contains: []string{"mapping values"}},
		{name: "unquoted template beside a quoted one", input: "a: \"{{.Branch}}\"\nb: {{.Branch}}", contains: []string{"invalid map key"}, wantHint: true},
		{name: "duplicate keys beside a quoted template", input: "a: \"{{.Branch}}\"\na: 2", contains: []string{"already defined"}},
		{name: "broken value beside a template in a scalar", input: "image: repo:{{.Branch}}\nb: c: d", contains: []string{"mapping values"}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateValues(tt.input)
			require.Error(t, err)
			for _, want := range tt.contains {
				assert.Contains(t, err.Error(), want)
			}
			for _, unwanted := range tt.notContains {
				assert.NotContains(t, err.Error(), unwanted)
			}
			if tt.wantHint {
				assert.Contains(t, err.Error(), quoteHint)
			} else {
				assert.NotContains(t, err.Error(), "Put a template value")
			}
			assert.NotContains(t, err.Error(), "interface {}")
			assert.NotContains(t, err.Error(), "map[string]")
			assert.NotContains(t, err.Error(), "cannot unmarshal")
			assert.False(t, strings.HasPrefix(err.Error(), "yaml:"))
		})
	}
}

// TestValidateValuesTemplateSubstitution shows that a quoted template value
// that passes the check is replaced at deploy time.
func TestValidateValuesTemplateSubstitution(t *testing.T) {
	t.Parallel()

	const input = `tag: "{{.Branch}}"`
	assert.NoError(t, ValidateValues(input))

	out, err := NewValuesGenerator().GenerateValues(context.Background(), GenerateParams{
		OverrideValues: input,
		TemplateVars:   TemplateVars{Branch: "feature-x"},
	})
	assert.NoError(t, err)
	assert.Equal(t, "tag: feature-x\n", string(out))
}

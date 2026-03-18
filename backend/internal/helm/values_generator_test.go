package helm

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateValues(t *testing.T) {
	t.Parallel()

	gen := NewValuesGenerator()
	ctx := context.Background()

	tests := []struct {
		name     string
		params   GenerateParams
		wantYAML string
		wantErr  string
	}{
		{
			name: "basic merge defaults and overrides",
			params: GenerateParams{
				DefaultValues:  "replicas: 1\nimage: nginx",
				OverrideValues: "replicas: 3",
			},
			wantYAML: "image: nginx\nreplicas: 3\n",
		},
		{
			name: "deep merge nested maps",
			params: GenerateParams{
				DefaultValues:  "server:\n  host: localhost\n  port: 8080\n  tls:\n    enabled: false\n    cert: default.pem\n",
				OverrideValues: "server:\n  port: 9090\n  tls:\n    enabled: true\n",
			},
			wantYAML: "server:\n  host: localhost\n  port: 9090\n  tls:\n    cert: default.pem\n    enabled: true\n",
		},
		{
			name: "array replacement",
			params: GenerateParams{
				DefaultValues:  "tags:\n  - v1\n  - v2\n  - v3",
				OverrideValues: "tags:\n  - latest",
			},
			wantYAML: "tags:\n  - latest\n",
		},
		{
			name: "locked values override both defaults and user overrides",
			params: GenerateParams{
				DefaultValues:  "replicas: 1\nmemory: 256Mi",
				OverrideValues: "replicas: 10\nmemory: 1Gi",
				LockedValues:   "replicas: 2",
			},
			wantYAML: "memory: 1Gi\nreplicas: 2\n",
		},
		{
			name: "template variable substitution",
			params: GenerateParams{
				DefaultValues: "namespace: \"{{.Namespace}}\"\nbranch: \"{{.Branch}}\"\ninstance: \"{{.InstanceName}}\"\nstack: \"{{.StackName}}\"\nowner: \"{{.Owner}}\"\n",
				TemplateVars: TemplateVars{
					Branch:       "feature/login",
					Namespace:    "stack-myapp-alice",
					InstanceName: "myapp",
					StackName:    "full-stack",
					Owner:        "alice",
				},
			},
			wantYAML: "branch: feature/login\ninstance: myapp\nnamespace: stack-myapp-alice\nowner: alice\nstack: full-stack\n",
		},
		{
			name: "variables in nested values",
			params: GenerateParams{
				DefaultValues: "global:\n  environment:\n    BRANCH: \"{{.Branch}}\"\n    NAMESPACE: \"{{.Namespace}}\"\n  labels:\n    owner: \"{{.Owner}}\"\n",
				TemplateVars: TemplateVars{
					Branch:    "main",
					Namespace: "stack-test-bob",
					Owner:     "bob",
				},
			},
			wantYAML: "global:\n  environment:\n    BRANCH: main\n    NAMESPACE: stack-test-bob\n  labels:\n    owner: bob\n",
		},
		{
			name: "empty defaults",
			params: GenerateParams{
				DefaultValues:  "",
				OverrideValues: "replicas: 3",
			},
			wantYAML: "replicas: 3\n",
		},
		{
			name: "empty overrides",
			params: GenerateParams{
				DefaultValues:  "replicas: 1",
				OverrideValues: "",
			},
			wantYAML: "replicas: 1\n",
		},
		{
			name: "all empty inputs",
			params: GenerateParams{
				DefaultValues:  "",
				OverrideValues: "",
				LockedValues:   "",
			},
			wantYAML: "{}\n",
		},
		{
			name: "invalid default YAML",
			params: GenerateParams{
				DefaultValues: "{{invalid: yaml: [",
			},
			wantErr: "parsing default values",
		},
		{
			name: "invalid override YAML",
			params: GenerateParams{
				DefaultValues:  "replicas: 1",
				OverrideValues: "{{bad yaml",
			},
			wantErr: "parsing override values",
		},
		{
			name: "invalid locked YAML",
			params: GenerateParams{
				DefaultValues: "replicas: 1",
				LockedValues:  ": :\nbad",
			},
			wantErr: "parsing locked values",
		},
		{
			name: "locked values cannot be overridden by user",
			params: GenerateParams{
				DefaultValues:  "resources:\n  limits:\n    cpu: 500m\n    memory: 256Mi\n  requests:\n    cpu: 100m\n",
				OverrideValues: "resources:\n  limits:\n    cpu: 4000m\n    memory: 4Gi\n",
				LockedValues:   "resources:\n  limits:\n    cpu: 1000m\n",
			},
			wantYAML: "resources:\n  limits:\n    cpu: 1000m\n    memory: 4Gi\n  requests:\n    cpu: 100m\n",
		},
		{
			name: "null override removes key",
			params: GenerateParams{
				DefaultValues:  "a: 1\nb: 2\nc: 3",
				OverrideValues: "b: null",
			},
			wantYAML: "a: 1\nc: 3\n",
		},
		{
			name: "template vars in arrays",
			params: GenerateParams{
				DefaultValues: "hosts:\n  - \"{{.InstanceName}}.example.com\"\n  - \"{{.InstanceName}}.internal\"\n",
				TemplateVars: TemplateVars{
					InstanceName: "myapp",
				},
			},
			wantYAML: "hosts:\n  - myapp.example.com\n  - myapp.internal\n",
		},
		{
			name: "non-string values preserved",
			params: GenerateParams{
				DefaultValues:  "replicas: 3\nenabled: true\nweight: 1.5\ntags:\n  - v1\n",
				OverrideValues: "replicas: 5\nenabled: false\n",
			},
			wantYAML: "enabled: false\nreplicas: 5\ntags:\n  - v1\nweight: 1.5\n",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := gen.GenerateValues(ctx, tt.params)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantYAML, string(got))
		})
	}
}

func TestGenerateAllValues(t *testing.T) {
	t.Parallel()

	gen := NewValuesGenerator()
	ctx := context.Background()

	tests := []struct {
		name       string
		params     GenerateAllParams
		wantCharts map[string]string
		wantErr    string
	}{
		{
			name: "multiple charts",
			params: GenerateAllParams{
				Charts: []ChartValues{
					{
						ChartName:      "frontend",
						DefaultValues:  "replicas: 1\nimage: nginx",
						OverrideValues: "replicas: 2",
					},
					{
						ChartName:      "backend",
						DefaultValues:  "replicas: 1\nport: 8080",
						OverrideValues: "replicas: 3",
					},
				},
			},
			wantCharts: map[string]string{
				"frontend": "image: nginx\nreplicas: 2\n",
				"backend":  "port: 8080\nreplicas: 3\n",
			},
		},
		{
			name: "template vars applied to all charts",
			params: GenerateAllParams{
				Charts: []ChartValues{
					{
						ChartName:     "svc-a",
						DefaultValues: "ns: \"{{.Namespace}}\"",
					},
					{
						ChartName:     "svc-b",
						DefaultValues: "ns: \"{{.Namespace}}\"",
					},
				},
				TemplateVars: TemplateVars{
					Namespace: "stack-test-ns",
				},
			},
			wantCharts: map[string]string{
				"svc-a": "ns: stack-test-ns\n",
				"svc-b": "ns: stack-test-ns\n",
			},
		},
		{
			name: "error in one chart propagates",
			params: GenerateAllParams{
				Charts: []ChartValues{
					{
						ChartName:     "good",
						DefaultValues: "a: 1",
					},
					{
						ChartName:     "bad",
						DefaultValues: "{{invalid",
					},
				},
			},
			wantErr: `generating values for chart "bad"`,
		},
		{
			name: "empty charts list",
			params: GenerateAllParams{
				Charts: []ChartValues{},
			},
			wantCharts: map[string]string{},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := gen.GenerateAllValues(ctx, tt.params)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}

			require.NoError(t, err)
			require.Len(t, got, len(tt.wantCharts))
			for chartName, wantYAML := range tt.wantCharts {
				assert.Equal(t, wantYAML, string(got[chartName]), "chart %s", chartName)
			}
		})
	}
}

func TestExportAsZip(t *testing.T) {
	t.Parallel()

	gen := NewValuesGenerator()
	ctx := context.Background()

	tests := []struct {
		name      string
		params    GenerateAllParams
		wantFiles map[string]string
		wantErr   string
	}{
		{
			name: "zip with multiple charts",
			params: GenerateAllParams{
				Charts: []ChartValues{
					{
						ChartName:     "frontend",
						DefaultValues: "replicas: 2",
					},
					{
						ChartName:     "backend",
						DefaultValues: "port: 8080",
					},
				},
			},
			wantFiles: map[string]string{
				"frontend/values.yaml": "replicas: 2\n",
				"backend/values.yaml":  "port: 8080\n",
			},
		},
		{
			name: "zip with template vars",
			params: GenerateAllParams{
				Charts: []ChartValues{
					{
						ChartName:     "app",
						DefaultValues: "branch: \"{{.Branch}}\"",
					},
				},
				TemplateVars: TemplateVars{
					Branch: "main",
				},
			},
			wantFiles: map[string]string{
				"app/values.yaml": "branch: main\n",
			},
		},
		{
			name: "zip with invalid chart returns error",
			params: GenerateAllParams{
				Charts: []ChartValues{
					{
						ChartName:     "bad",
						DefaultValues: "{{invalid",
					},
				},
			},
			wantErr: `generating values for chart "bad"`,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := gen.ExportAsZip(ctx, tt.params)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}

			require.NoError(t, err)
			require.NotEmpty(t, got)

			zr, err := zip.NewReader(bytes.NewReader(got), int64(len(got)))
			require.NoError(t, err)

			files := make(map[string]string, len(zr.File))
			for _, f := range zr.File {
				rc, err := f.Open()
				require.NoError(t, err)
				data, err := io.ReadAll(rc)
				require.NoError(t, err)
				require.NoError(t, rc.Close())
				files[f.Name] = string(data)
			}

			assert.Len(t, files, len(tt.wantFiles))
			for path, wantContent := range tt.wantFiles {
				assert.Equal(t, wantContent, files[path], "file %s", path)
			}
		})
	}
}

func TestDeepMerge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		dst  map[string]interface{}
		src  map[string]interface{}
		want map[string]interface{}
	}{
		{
			name: "nil dst",
			dst:  nil,
			src:  map[string]interface{}{"a": 1},
			want: map[string]interface{}{"a": 1},
		},
		{
			name: "nil src",
			dst:  map[string]interface{}{"a": 1},
			src:  nil,
			want: map[string]interface{}{"a": 1},
		},
		{
			name: "both nil",
			dst:  nil,
			src:  nil,
			want: map[string]interface{}{},
		},
		{
			name: "scalar override",
			dst:  map[string]interface{}{"a": 1, "b": 2},
			src:  map[string]interface{}{"b": 3},
			want: map[string]interface{}{"a": 1, "b": 3},
		},
		{
			name: "nested map merge",
			dst: map[string]interface{}{
				"outer": map[string]interface{}{
					"keep":   "yes",
					"change": "old",
				},
			},
			src: map[string]interface{}{
				"outer": map[string]interface{}{
					"change": "new",
					"add":    "added",
				},
			},
			want: map[string]interface{}{
				"outer": map[string]interface{}{
					"keep":   "yes",
					"change": "new",
					"add":    "added",
				},
			},
		},
		{
			name: "nil value removes key",
			dst:  map[string]interface{}{"a": 1, "b": 2},
			src:  map[string]interface{}{"a": nil},
			want: map[string]interface{}{"b": 2},
		},
		{
			name: "array replacement not append",
			dst:  map[string]interface{}{"list": []interface{}{1, 2, 3}},
			src:  map[string]interface{}{"list": []interface{}{4}},
			want: map[string]interface{}{"list": []interface{}{4}},
		},
		{
			name: "map replaces scalar",
			dst:  map[string]interface{}{"a": "string"},
			src:  map[string]interface{}{"a": map[string]interface{}{"nested": true}},
			want: map[string]interface{}{"a": map[string]interface{}{"nested": true}},
		},
		{
			name: "scalar replaces map",
			dst:  map[string]interface{}{"a": map[string]interface{}{"nested": true}},
			src:  map[string]interface{}{"a": "string"},
			want: map[string]interface{}{"a": "string"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := deepMerge(tt.dst, tt.src)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSubstituteVars(t *testing.T) {
	t.Parallel()

	vars := TemplateVars{
		Branch:       "feature/test",
		Namespace:    "stack-app-user",
		InstanceName: "app",
		StackName:    "mystack",
		Owner:        "user",
	}

	tests := []struct {
		name    string
		input   map[string]interface{}
		want    map[string]interface{}
		wantErr bool
	}{
		{
			name: "simple substitution",
			input: map[string]interface{}{
				"branch": "{{.Branch}}",
				"ns":     "{{.Namespace}}",
			},
			want: map[string]interface{}{
				"branch": "feature/test",
				"ns":     "stack-app-user",
			},
		},
		{
			name: "no templates unchanged",
			input: map[string]interface{}{
				"plain": "hello",
				"num":   42,
			},
			want: map[string]interface{}{
				"plain": "hello",
				"num":   42,
			},
		},
		{
			name: "nested substitution",
			input: map[string]interface{}{
				"outer": map[string]interface{}{
					"inner": "{{.Owner}}",
				},
			},
			want: map[string]interface{}{
				"outer": map[string]interface{}{
					"inner": "user",
				},
			},
		},
		{
			name: "array substitution",
			input: map[string]interface{}{
				"hosts": []interface{}{"{{.InstanceName}}.local", "{{.InstanceName}}.dev"},
			},
			want: map[string]interface{}{
				"hosts": []interface{}{"app.local", "app.dev"},
			},
		},
		{
			name: "invalid template variable",
			input: map[string]interface{}{
				"bad": "{{.Invalid}}",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := substituteVars(tt.input, vars)

			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

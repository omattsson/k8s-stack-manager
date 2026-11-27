package helm

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeDigests records the imageDigest calls.
type fakeDigests struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeDigests) Digest(_ context.Context, repository, tag string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, repository+":"+tag)
	return "sha256:" + tag
}

func TestGenerateValues_ImageDigest(t *testing.T) {
	t.Parallel()

	const values = `podAnnotations:
  deploy/image-digest: '{{ imageDigest "reg.example.com/dev/app" .ImageTag }}'
`
	tests := []struct {
		name      string
		digests   DigestResolver
		branch    string
		want      string
		wantCalls []string
	}{
		{name: "nil resolver renders empty", want: `""`},
		{name: "resolver gets repository and image tag", digests: &fakeDigests{}, want: "sha256:feature-x", wantCalls: []string{"reg.example.com/dev/app:feature-x"}},
		{name: "chart branch sets the tag", digests: &fakeDigests{}, branch: "Fix/Bug_1", want: "sha256:fix-bug-1", wantCalls: []string{"reg.example.com/dev/app:fix-bug-1"}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := NewValuesGenerator().GenerateValues(context.Background(), GenerateParams{
				ChartName:     "app",
				DefaultValues: values,
				ChartBranch:   tt.branch,
				TemplateVars:  TemplateVars{Branch: "feature/x", ImageTag: "feature-x"},
				Digests:       tt.digests,
			})
			require.NoError(t, err)
			assert.Equal(t, "podAnnotations:\n  deploy/image-digest: "+tt.want+"\n", string(got))
			if f, ok := tt.digests.(*fakeDigests); ok {
				assert.Equal(t, tt.wantCalls, f.calls)
			}
		})
	}
}

// TestGenerateAllValues_ImageDigestRendersEmpty covers export: there is no
// resolver, and a template with imageDigest still renders.
func TestGenerateAllValues_ImageDigestRendersEmpty(t *testing.T) {
	t.Parallel()

	out, err := NewValuesGenerator().ExportAsZip(context.Background(), GenerateAllParams{
		Charts:       []ChartValues{{ChartName: "app", DefaultValues: `d: '{{ imageDigest "r.io/a" .ImageTag }}'`}},
		TemplateVars: TemplateVars{ImageTag: "main"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, out)

	all, err := NewValuesGenerator().GenerateAllValues(context.Background(), GenerateAllParams{
		Charts:       []ChartValues{{ChartName: "app", DefaultValues: `d: '{{ imageDigest "r.io/a" .ImageTag }}'`}},
		TemplateVars: TemplateVars{ImageTag: "main"},
	})
	require.NoError(t, err)
	assert.Equal(t, "d: \"\"\n", string(all["app"]))
}

// TestRegisterFunc_ImageDigestIsBuiltIn checks that a registered func cannot
// replace the built-in imageDigest.
func TestRegisterFunc_ImageDigestIsBuiltIn(t *testing.T) {
	t.Parallel()

	gen := NewValuesGenerator().RegisterFunc(ImageDigestFunc, func(string, string) string { return "custom" })
	got, err := gen.GenerateValues(context.Background(), GenerateParams{
		ChartName:     "app",
		DefaultValues: `d: '{{ imageDigest "r.io/a" "v1" }}'`,
	})
	require.NoError(t, err)
	assert.Equal(t, "d: \"\"\n", string(got))
}

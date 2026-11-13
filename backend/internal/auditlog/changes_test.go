package auditlog

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type diffSample struct {
	Name      string    `json:"name"`
	AllowExec bool      `json:"allow_exec"`
	Count     *int      `json:"ttl_minutes"`
	Note      string    `json:"note"`
	Hidden    string    `json:"-"`
	Computed  string    `json:"computed" gorm:"-"`
	ReadOnly  string    `json:"read_only" gorm:"->"`
	Display   string    `json:"display" readonly:"true"`
	UpdatedAt string    `json:"updated_at"`
	Version   uint      `json:"version"`
	Tags      []string  `json:"tags"`
	Inner     diffInner `json:"inner"`
}

type diffInner struct {
	X int `json:"x"`
}

func ptr[T any](v T) *T { return &v }

func TestFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		before diffSample
		after  diffSample
		want   []models.AuditChange
	}{
		{name: "bool allow-listed", before: diffSample{}, after: diffSample{AllowExec: true},
			want: []models.AuditChange{{Field: "allow_exec", Old: false, New: true}}},
		{name: "allow-listed string", before: diffSample{Name: "a"}, after: diffSample{Name: "b"},
			want: []models.AuditChange{{Field: "name", Old: "a", New: "b"}}},
		{name: "long allow-listed string gives name only", before: diffSample{Name: "a"}, after: diffSample{Name: strings.Repeat("n", 300)},
			want: []models.AuditChange{{Field: "name"}}},
		{name: "not allow-listed gives name only", before: diffSample{Note: "a"}, after: diffSample{Note: "b"},
			want: []models.AuditChange{{Field: "note"}}},
		{name: "skipped fields", before: diffSample{},
			after: diffSample{Hidden: "x", Computed: "x", ReadOnly: "x", Display: "x", UpdatedAt: "x"}},
		{name: "template version is a real field", before: diffSample{}, after: diffSample{Version: 2},
			want: []models.AuditChange{{Field: "version"}}},
		{name: "nil pointer to zero", before: diffSample{}, after: diffSample{Count: ptr(0)},
			want: []models.AuditChange{{Field: "ttl_minutes", New: 0}}},
		{name: "pointer value change", before: diffSample{Count: ptr(5)}, after: diffSample{Count: ptr(6)},
			want: []models.AuditChange{{Field: "ttl_minutes", Old: 5, New: 6}}},
		{name: "equal pointers", before: diffSample{Count: ptr(5)}, after: diffSample{Count: ptr(5)}},
		{name: "slice and struct give name only", before: diffSample{}, after: diffSample{Tags: []string{"a"}, Inner: diffInner{X: 1}},
			want: []models.AuditChange{{Field: "tags"}, {Field: "inner"}}},
		{name: "order follows struct", before: diffSample{}, after: diffSample{Name: "n", AllowExec: true, Note: "x"},
			want: []models.AuditChange{{Field: "name", Old: "", New: "n"}, {Field: "allow_exec", Old: false, New: true}, {Field: "note"}}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, Fields(tt.before, tt.after))
			assert.Equal(t, tt.want, Fields(&tt.before, &tt.after), "pointers to structs")
		})
	}
}

func TestFields_RealModelsSkipComputed(t *testing.T) {
	t.Parallel()
	n := 3
	before := models.StackDefinition{ID: "d1", Name: "a"}
	after := before
	after.OwnerUsername = "alice"
	after.ChartCount = &n
	assert.Empty(t, Fields(before, after))

	tb := models.StackTemplate{ID: "t1"}
	ta := tb
	ta.OwnerUsername = "bob"
	assert.Empty(t, Fields(tb, ta))
}

func TestFields_ValuesOnlyForAllowList(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		before  any
		after   any
		want    []models.AuditChange
		secrets []string
	}{
		{name: "default values", before: models.ChartConfig{}, after: models.ChartConfig{DefaultValues: "password: hunter2"},
			want: []models.AuditChange{{Field: "default_values"}}, secrets: []string{"hunter2"}},
		{name: "repository url", before: models.ChartConfig{}, after: models.ChartConfig{RepositoryURL: "https://u:tok@host/x"},
			want: []models.AuditChange{{Field: "repository_url"}}, secrets: []string{"tok"}},
		{name: "kubeconfig is json:-", before: models.Cluster{}, after: models.Cluster{KubeconfigData: "secret-kc", RegistryPassword: "secret-pw"},
			secrets: []string{"secret-kc", "secret-pw"}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Fields(tt.before, tt.after)
			assert.Equal(t, tt.want, got)
			raw, err := json.Marshal(got)
			require.NoError(t, err)
			for _, s := range tt.secrets {
				assert.NotContains(t, string(raw), s)
			}
		})
	}
}

func TestRedacted(t *testing.T) {
	t.Parallel()
	assert.Equal(t, models.AuditChange{Field: "kubeconfig_data", Redacted: true}, Redacted("kubeconfig_data"))
}

func TestChangedKeyPaths(t *testing.T) {
	t.Parallel()
	var many, manyNew strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&many, "k%03d: 1\n", i)
		fmt.Fprintf(&manyNew, "k%03d: 2\n", i)
	}
	tests := []struct {
		name    string
		prefix  string
		old     string
		new     string
		want    []string
		wantLen int
		wantErr bool
	}{
		{name: "scalar change", old: "a: 1", new: "a: 2", want: []string{"a"}},
		{name: "nested add with prefix", prefix: "default_values.", old: "", new: "x:\n  y: 1", want: []string{"default_values.x.y"}},
		{name: "remove", old: "a: 1\nb: 2", new: "a: 1", want: []string{"b"}},
		{name: "identical", old: "a: 1", new: "a: 1"},
		{name: "list is one leaf", old: "l: [1, 2]", new: "l: [1, 3]", want: []string{"l"}},
		{name: "map to scalar", old: "a:\n  b: 1", new: "a: 1", want: []string{"a", "a.b"}},
		{name: "empty map added", old: "", new: "a: {}", want: []string{"a"}},
		{name: "sorted", old: "", new: "b: 1\na: 1", want: []string{"a", "b"}},
		{name: "cap at 200", old: many.String(), new: manyNew.String(), wantLen: 201},
		{name: "broken yaml", old: "", new: "a: [", wantErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ChangedKeyPaths(tt.prefix, tt.old, tt.new)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tt.wantLen > 0 {
				require.Len(t, got, tt.wantLen)
				assert.Equal(t, "…", got[len(got)-1].Field)
				return
			}
			var fields []string
			for _, c := range got {
				fields = append(fields, c.Field)
				assert.Nil(t, c.Old)
				assert.Nil(t, c.New)
			}
			assert.Equal(t, tt.want, fields)
		})
	}
}

func TestChangedKeyPaths_NoValues(t *testing.T) {
	t.Parallel()
	got, err := ChangedKeyPaths("", "", "password: hunter2")
	require.NoError(t, err)
	assert.Equal(t, []models.AuditChange{{Field: "password"}}, got)
	raw, err := json.Marshal(got)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "hunter2")
}

func TestChangedKeyPaths_LongKeyCut(t *testing.T) {
	t.Parallel()
	got, err := ChangedKeyPaths("", "", strings.Repeat("k", 400)+": 1")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.LessOrEqual(t, len(got[0].Field), 256)
}

// TestChangedKeyPaths_DottedKeys: a key with a dot differs from a nested key
// (PR 790 review).
func TestChangedKeyPaths_DottedKeys(t *testing.T) {
	t.Parallel()
	got, err := ChangedKeyPaths("", `{"a.b": 1}`, "a:\n  b: 1")
	require.NoError(t, err)
	var fields []string
	for _, c := range got {
		fields = append(fields, c.Field)
	}
	assert.Equal(t, []string{`a.b`, `a\.b`}, fields)

	got, err = ChangedKeyPaths("", "", "a:\n  b: 1\n'a.b': 2\n'c\\d': 3")
	require.NoError(t, err)
	fields = nil
	for _, c := range got {
		fields = append(fields, c.Field)
	}
	assert.Equal(t, []string{`a.b`, `a\.b`, `c\\d`}, fields)
}

func TestChangedKeyPaths_CutKeepsEscapes(t *testing.T) {
	t.Parallel()
	// The escaped key is 255 "k", then "\." at bytes 255 and 256: a cut at 256
	// would end in a lone backslash.
	got, err := ChangedKeyPaths("", "", "'"+strings.Repeat("k", 255)+".z': 1")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.LessOrEqual(t, len(got[0].Field), 256)
	assert.False(t, strings.HasSuffix(got[0].Field, `\`), got[0].Field[240:])
}

package hooks

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewActionRegistry_UIMetadataValidation(t *testing.T) {
	t.Parallel()

	base := func(mod func(a *ActionSubscription)) []ActionSubscription {
		a := ActionSubscription{Name: "x", URL: "https://e/h"}
		mod(&a)
		return []ActionSubscription{a}
	}

	tests := []struct {
		name      string
		subs      []ActionSubscription
		expectErr string
	}{
		{"valid full metadata", base(func(a *ActionSubscription) {
			a.Label = "Refresh DB"
			a.Confirm = "This wipes the database."
			a.LogPath = "/jobs/{job_id}/log"
			a.Parameters = []ActionParameter{
				{Name: "image", Type: "string", Default: "golden"},
				{Name: "dry_run", Type: "bool", Default: false},
				{Name: "market", Type: "enum", Options: []string{"a", "b"}, Default: "a", Required: true},
				{Name: "reason"},
			}
		}), ""},
		{"label too long", base(func(a *ActionSubscription) { a.Label = string(make([]byte, 101)) }), "label must be at most"},
		{"confirm too long", base(func(a *ActionSubscription) { a.Confirm = string(make([]byte, 1001)) }), "confirm must be at most"},
		{"log_path relative", base(func(a *ActionSubscription) { a.LogPath = "jobs/{job_id}/log" }), "absolute path"},
		{"log_path with query", base(func(a *ActionSubscription) { a.LogPath = "/jobs/{job_id}/log?x=1" }), "absolute path"},
		{"log_path with encoded chars", base(func(a *ActionSubscription) { a.LogPath = "/jobs/%2e%2e/{job_id}" }), "absolute path"},
		{"log_path with host", base(func(a *ActionSubscription) { a.LogPath = "//evil.example/{job_id}" }), "empty segments"},
		{"log_path without placeholder", base(func(a *ActionSubscription) { a.LogPath = "/jobs/log" }), "exactly once"},
		{"log_path placeholder twice", base(func(a *ActionSubscription) { a.LogPath = "/{job_id}/{job_id}" }), "exactly once"},
		{"log_path unknown placeholder", base(func(a *ActionSubscription) { a.LogPath = "/{job_id}/{name}" }), "unknown placeholder"},
		{"log_path dot-dot", base(func(a *ActionSubscription) { a.LogPath = "/jobs/../{job_id}" }), ". or .. segments"},
		{"param bad name", base(func(a *ActionSubscription) { a.Parameters = []ActionParameter{{Name: "a b"}} }), "name must match"},
		{"param duplicate", base(func(a *ActionSubscription) { a.Parameters = []ActionParameter{{Name: "a"}, {Name: "a"}} }), "duplicate name"},
		{"param unknown type", base(func(a *ActionSubscription) { a.Parameters = []ActionParameter{{Name: "a", Type: "int"}} }), "type must be"},
		{"enum without options", base(func(a *ActionSubscription) { a.Parameters = []ActionParameter{{Name: "a", Type: "enum"}} }), "at least one option"},
		{"enum duplicate option", base(func(a *ActionSubscription) {
			a.Parameters = []ActionParameter{{Name: "a", Type: "enum", Options: []string{"x", "x"}}}
		}), "duplicate option"},
		{"options on string", base(func(a *ActionSubscription) {
			a.Parameters = []ActionParameter{{Name: "a", Options: []string{"x"}}}
		}), "only allowed for type enum"},
		{"bool default not bool", base(func(a *ActionSubscription) {
			a.Parameters = []ActionParameter{{Name: "a", Type: "bool", Default: "yes"}}
		}), "invalid default"},
		{"enum default not an option", base(func(a *ActionSubscription) {
			a.Parameters = []ActionParameter{{Name: "a", Type: "enum", Options: []string{"x"}, Default: "y"}}
		}), "invalid default"},
		{"string default number", base(func(a *ActionSubscription) {
			a.Parameters = []ActionParameter{{Name: "a", Default: float64(3)}}
		}), "invalid default"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewActionRegistry(tt.subs, nil)
			if tt.expectErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectErr)
		})
	}
}

func TestNewActionRegistry_NormalizesParamTypeWithoutChangingInput(t *testing.T) {
	t.Parallel()
	in := []ActionSubscription{{Name: "x", URL: "https://e/h", Parameters: []ActionParameter{{Name: "a"}}}}
	r, err := NewActionRegistry(in, nil)
	require.NoError(t, err)
	got, ok := r.Lookup("x")
	require.True(t, ok)
	assert.Equal(t, ParamTypeString, got.Parameters[0].Type)
	assert.Empty(t, in[0].Parameters[0].Type, "input slice must stay unchanged")
}

func TestActionSubscription_ValidateParameters(t *testing.T) {
	t.Parallel()

	r, err := NewActionRegistry([]ActionSubscription{{
		Name: "x", URL: "https://e/h",
		Parameters: []ActionParameter{
			{Name: "image", Required: true},
			{Name: "dry_run", Type: "bool"},
			{Name: "market", Type: "enum", Options: []string{"a", "b"}},
		},
	}, {Name: "plain", URL: "https://e/h"}}, nil)
	require.NoError(t, err)
	sub, _ := r.Lookup("x")
	plain, _ := r.Lookup("plain")

	tests := []struct {
		name      string
		sub       ActionSubscription
		params    map[string]any
		expectErr string
	}{
		{"all valid", sub, map[string]any{"image": "golden", "dry_run": true, "market": "b"}, ""},
		{"undeclared passes through", sub, map[string]any{"image": "golden", "extra": 42}, ""},
		{"required missing", sub, map[string]any{"dry_run": true}, `parameter "image" is required`},
		{"required empty string", sub, map[string]any{"image": "  "}, `parameter "image" is required`},
		{"required nil", sub, map[string]any{"image": nil}, `parameter "image" is required`},
		{"nil params with required", sub, nil, `parameter "image" is required`},
		{"bool wrong type", sub, map[string]any{"image": "g", "dry_run": "true"}, "must be a boolean"},
		{"string wrong type", sub, map[string]any{"image": 3}, "must be a string"},
		{"string too long", sub, map[string]any{"image": string(make([]byte, 1025))}, "at most 1024"},
		{"enum not an option", sub, map[string]any{"image": "g", "market": "c"}, "must be one of: a, b"},
		{"no schema accepts anything", plain, map[string]any{"k": []any{1}}, ""},
		{"no schema nil params", plain, nil, ""},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.sub.ValidateParameters(tt.params)
			if tt.expectErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			var inv ErrInvalidParameters
			assert.True(t, errors.As(err, &inv))
			assert.Contains(t, err.Error(), tt.expectErr)
		})
	}
}

func TestActionRegistry_ListSortedByName(t *testing.T) {
	t.Parallel()
	r, err := NewActionRegistry([]ActionSubscription{
		{Name: "seed-data", URL: "https://e/h"},
		{Name: "refresh-db", URL: "https://e/h", LogPath: "/jobs/{job_id}/log"},
	}, nil)
	require.NoError(t, err)
	list := r.List()
	require.Len(t, list, 2)
	assert.Equal(t, "refresh-db", list[0].Name)
	assert.True(t, list[0].HasJobLog())
	assert.Equal(t, "seed-data", list[1].Name)
	assert.False(t, list[1].HasJobLog())
}

func TestLoadConfigFile_ParsesActionUIMetadata(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
  "actions": [
    {
      "name": "refresh-db",
      "url": "http://refresh-db.example:8080/",
      "label": "Refresh database",
      "confirm": "This replaces the database of the stack.",
      "log_path": "/jobs/{job_id}/log",
      "parameters": [
        {"name": "image", "label": "Image", "type": "string", "default": "golden"},
        {"name": "dry_run", "type": "bool", "default": true},
        {"name": "market", "type": "enum", "options": ["a", "b"], "required": true}
      ]
    },
    {"name": "old-style", "url": "http://old.example/"}
  ]
}`), 0o600))

	_, actions, err := LoadConfigFile(path)
	require.NoError(t, err)
	require.Len(t, actions, 2)
	act := actions[0]
	assert.Equal(t, "Refresh database", act.Label)
	assert.Equal(t, "This replaces the database of the stack.", act.Confirm)
	assert.Equal(t, "/jobs/{job_id}/log", act.LogPath)
	require.Len(t, act.Parameters, 3)
	assert.Equal(t, "golden", act.Parameters[0].Default)
	assert.Equal(t, true, act.Parameters[1].Default)
	assert.Equal(t, []string{"a", "b"}, act.Parameters[2].Options)
	assert.True(t, act.Parameters[2].Required)

	assert.Empty(t, actions[1].Label, "old configs stay valid without UI metadata")
	_, err = NewActionRegistry(actions, nil)
	require.NoError(t, err)
}

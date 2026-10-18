package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseNamespaceLabels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    map[string]string
		wantErr string
	}{
		{
			name: "valid list",
			raw:  "pod-security.kubernetes.io/enforce=baseline,pod-security.kubernetes.io/warn=restricted",
			want: map[string]string{
				"pod-security.kubernetes.io/enforce": "baseline",
				"pod-security.kubernetes.io/warn":    "restricted",
			},
		},
		{name: "empty setting", raw: "", want: nil},
		{name: "only spaces and commas", raw: " , ,", want: nil},
		{
			name: "spaces around entries, keys and values",
			raw:  "  team = a ,  env=dev , ",
			want: map[string]string{"team": "a", "env": "dev"},
		},
		{name: "empty value is a valid label value", raw: "team=", want: map[string]string{"team": ""}},
		{name: "entry without equals sign", raw: "team", wantErr: `entry "team" is not in the form key=value`},
		{name: "invalid key characters", raw: "bad key=a", wantErr: `label key "bad key" is not valid`},
		{name: "invalid key prefix", raw: "Bad_Prefix/name=a", wantErr: `label key "Bad_Prefix/name" is not valid`},
		{name: "empty key", raw: "=a", wantErr: `label key "" is not valid`},
		{name: "key name too long", raw: strings.Repeat("a", 64) + "=a", wantErr: "is not valid"},
		{name: "invalid value characters", raw: "team=a b", wantErr: `value "a b" of label "team" is not valid`},
		{name: "value too long", raw: "team=" + strings.Repeat("a", 64), wantErr: `of label "team" is not valid`},
		{name: "reserved managed-by", raw: "managed-by=other", wantErr: `label key "managed-by" is reserved`},
		{
			name:    "reserved instance-name",
			raw:     "team=a,k8s-stack-manager.io/instance-name=foo",
			wantErr: `label key "k8s-stack-manager.io/instance-name" is reserved`,
		},
		{name: "duplicate key", raw: "team=a,team=b", wantErr: `label key "team" is set more than once`},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseNamespaceLabels(tt.raw)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestLoadConfig_NamespaceLabels checks that LoadConfig parses
// NAMESPACE_LABELS and refuses an invalid value at startup. Not parallel:
// t.Setenv.
func TestLoadConfig_NamespaceLabels(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    map[string]string
		wantErr string
	}{
		{name: "unset", raw: "", want: nil},
		{
			name: "valid",
			raw:  "pod-security.kubernetes.io/enforce=baseline",
			want: map[string]string{"pod-security.kubernetes.io/enforce": "baseline"},
		},
		{name: "invalid is refused at startup", raw: "managed-by=x", wantErr: "invalid configuration: NAMESPACE_LABELS: "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ENV_FILE", "/nonexistent/.env")
			t.Setenv("NAMESPACE_LABELS", tt.raw)
			cfg, err := LoadConfig()
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.Deployment.NamespaceLabels)
		})
	}
}

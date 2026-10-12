package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseStackAccess(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		role    string
		sa      string
		want    StackAccessConfig
		wantErr bool
	}{
		{name: "both empty = off", want: StackAccessConfig{}},
		{name: "valid", role: "ksm-stack-access", sa: "ksm/ksm-backend", want: StackAccessConfig{ClusterRole: "ksm-stack-access", ServiceAccount: "ksm-backend", ServiceAccountNamespace: "ksm"}},
		{name: "role only", role: "ksm-stack-access", wantErr: true},
		{name: "sa only", sa: "ksm/ksm-backend", wantErr: true},
		{name: "sa without namespace", role: "r", sa: "ksm-backend", wantErr: true},
		{name: "bad namespace", role: "r", sa: "Bad_NS/sa", wantErr: true},
		{name: "bad role", role: "Bad Role", sa: "ns/sa", wantErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseStackAccess(tt.role, tt.sa)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.want.ClusterRole != "", got.Enabled())
		})
	}
}

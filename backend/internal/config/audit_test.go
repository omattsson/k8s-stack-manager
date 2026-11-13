package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseAuditConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		raw     string
		want    bool
		wantErr bool
	}{
		{name: "empty", raw: "", want: false},
		{name: "true", raw: "true", want: true},
		{name: "false", raw: "false", want: false},
		{name: "spaces", raw: " true ", want: true},
		{name: "invalid", raw: "yes", wantErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseAuditConfig(tt.raw)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "AUDIT_LOG_STDOUT")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.Stdout)
		})
	}
}

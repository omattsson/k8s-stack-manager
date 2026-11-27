package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseImageDigestRegistries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		raw     string
		want    []string
		wantErr bool
	}{
		{name: "empty", raw: "", want: nil},
		{name: "blanks only", raw: " , ", want: nil},
		{name: "hosts and port", raw: "ghcr.io, Registry.Example.com:5000", want: []string{"ghcr.io", "registry.example.com:5000"}},
		{name: "docker hub", raw: "docker.io", want: []string{"index.docker.io"}},
		{name: "scheme refused", raw: "https://ghcr.io", wantErr: true},
		{name: "path refused", raw: "ghcr.io/org", wantErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseImageDigestRegistries(tt.raw)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

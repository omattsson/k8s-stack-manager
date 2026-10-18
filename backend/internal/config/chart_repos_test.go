package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseAllowedChartRepos(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		raw     string
		want    []string
		wantErr bool
	}{
		{name: "empty", raw: "", want: nil},
		{name: "blanks only", raw: " , ", want: nil},
		{name: "two entries", raw: "oci://registry.example.com/charts/, https://charts.example.com", want: []string{"oci://registry.example.com/charts", "https://charts.example.com"}},
		{name: "invalid entry", raw: "oci://registry.example.com/charts,charts.example.com", wantErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseAllowedChartRepos(tt.raw)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

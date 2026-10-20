package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePublicURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr string
	}{
		{name: "empty is unset", raw: "", want: ""},
		{name: "blank is unset", raw: "   ", want: ""},
		{name: "valid https host", raw: "https://stacks.example.com", want: "https://stacks.example.com"},
		{name: "trailing slash removed", raw: "https://stacks.example.com/", want: "https://stacks.example.com"},
		{name: "path prefix kept", raw: "https://example.com/stacks/", want: "https://example.com/stacks"},
		{name: "port kept", raw: "https://stacks.example.com:8443", want: "https://stacks.example.com:8443"},
		{name: "http refused", raw: "http://stacks.example.com", wantErr: "must use https"},
		{name: "same-origin path refused", raw: "/stacks", wantErr: "not a path"},
		{name: "protocol-relative refused", raw: "//stacks.example.com", wantErr: "not a path"},
		{name: "javascript refused", raw: "javascript:alert(1)", wantErr: "https URL"},
		{name: "no host refused", raw: "https://", wantErr: "must have a host"},
		{name: "user info refused", raw: "https://user:pw@stacks.example.com", wantErr: "user info"},
		{name: "query refused", raw: "https://stacks.example.com/?a=b", wantErr: "query or a fragment"},
		{name: "fragment refused", raw: "https://stacks.example.com/#x", wantErr: "query or a fragment"},
		{name: "space refused", raw: "https://stacks.example.com/a b", wantErr: "spaces"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParsePublicURL(tt.raw)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Contains(t, err.Error(), "APP_PUBLIC_URL")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestAppConfigValidate_PublicURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "valid and normalized", raw: "https://stacks.example.com/", want: "https://stacks.example.com"},
		{name: "empty", raw: "", want: ""},
		{name: "http stops the start", raw: "http://stacks.example.com", wantErr: true},
		{name: "path stops the start", raw: "/ui", wantErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := AppConfig{Name: "backend-api", Environment: "test", PublicURL: tt.raw}
			err := c.Validate()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, c.PublicURL)
		})
	}
}

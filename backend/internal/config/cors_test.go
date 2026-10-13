package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCORSConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		origins      string
		wantAllowAll bool
		wantErr      bool
	}{
		{name: "wildcard", origins: "*", wantAllowAll: true},
		{name: "empty means wildcard", origins: "", wantAllowAll: true},
		{name: "wildcard with spaces", origins: " * ", wantAllowAll: true},
		{name: "same-origin", origins: CORSSameOrigin},
		{name: "one origin", origins: "https://portal.example.com"},
		{name: "list of origins", origins: "https://a.example.com, https://b.example.com"},
		{name: "same-origin with a trailing comma", origins: "same-origin,"},
		{name: "wildcard with a trailing comma", origins: "*,", wantAllowAll: true},
		{name: "same-origin mixed with an origin", origins: "same-origin,https://a.example.com", wantErr: true},
		{name: "wildcard mixed with an origin", origins: "https://a.example.com, *", wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := CORSConfig{AllowedOrigins: tt.origins}
			assert.Equal(t, tt.wantAllowAll, c.AllowsAllOrigins())
			if tt.wantErr {
				assert.Error(t, c.Validate())
			} else {
				assert.NoError(t, c.Validate())
			}
		})
	}
}

// TestLoadConfig_CORS checks the default and that a mixed value stops the
// startup. Not parallel: t.Setenv.
func TestLoadConfig_CORS(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{name: "unset gives the development default", value: "", want: "*"},
		{name: "same-origin", value: "same-origin", want: "same-origin"},
		{name: "mixed value is refused at startup", value: "same-origin,https://a.example.com", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ENV_FILE", "/nonexistent/.env")
			t.Setenv("CORS_ALLOWED_ORIGINS", tt.value)
			cfg, err := LoadConfig()
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "CORS_ALLOWED_ORIGINS")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.CORS.AllowedOrigins)
		})
	}
}

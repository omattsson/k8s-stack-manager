package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBrandingConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      BrandingConfig
		want    BrandingConfig
		wantErr string
	}{
		{
			name: "empty values get the defaults",
			in:   BrandingConfig{},
			want: BrandingConfig{Title: DefaultAppTitle, FaviconURL: DefaultAppFaviconURL},
		},
		{
			name: "title is trimmed",
			in:   BrandingConfig{Title: "  Platform Portal  "},
			want: BrandingConfig{Title: "Platform Portal", FaviconURL: DefaultAppFaviconURL},
		},
		{
			name: "same-origin paths",
			in:   BrandingConfig{Title: "X", LogoURL: "/branding/logo.svg", FaviconURL: "/branding/favicon.png?v=2"},
			want: BrandingConfig{Title: "X", LogoURL: "/branding/logo.svg", FaviconURL: "/branding/favicon.png?v=2"},
		},
		{
			name: "https URLs",
			in:   BrandingConfig{Title: "X", LogoURL: "https://cdn.example.com/logo.svg", FaviconURL: "https://intranet.example.com:8443/icon.ico"},
			want: BrandingConfig{Title: "X", LogoURL: "https://cdn.example.com/logo.svg", FaviconURL: "https://intranet.example.com:8443/icon.ico"},
		},
		{name: "http logo URL", in: BrandingConfig{LogoURL: "http://cdn.example.com/logo.svg"}, wantErr: "APP_LOGO_URL must use https"},
		{name: "http favicon URL", in: BrandingConfig{FaviconURL: "http://cdn.example.com/f.ico"}, wantErr: "APP_FAVICON_URL must use https"},
		{name: "javascript scheme", in: BrandingConfig{LogoURL: "javascript:alert(1)"}, wantErr: "APP_LOGO_URL must be a same-origin path"},
		{name: "mixed-case javascript scheme", in: BrandingConfig{FaviconURL: "JavaScript:alert(1)"}, wantErr: "APP_FAVICON_URL must be a same-origin path"},
		{name: "data image URL", in: BrandingConfig{LogoURL: "data:image/svg+xml;base64,PHN2Zy8+"}, wantErr: "APP_LOGO_URL must be a same-origin path"},
		{name: "protocol-relative URL", in: BrandingConfig{LogoURL: "//evil.example.com/x.svg"}, wantErr: "protocol-relative"},
		{name: "relative path without slash", in: BrandingConfig{LogoURL: "branding/logo.svg"}, wantErr: "APP_LOGO_URL must be a same-origin path"},
		{name: "backslash", in: BrandingConfig{LogoURL: `/\evil.example.com`}, wantErr: "backslash"},
		{name: "space", in: BrandingConfig{LogoURL: "/a b.svg"}, wantErr: "spaces"},
		{name: "user info", in: BrandingConfig{LogoURL: "https://user:pw@example.com/x.svg"}, wantErr: "user info"},
		{name: "no host", in: BrandingConfig{LogoURL: "https:///x.svg"}, wantErr: "host"},
		{name: "ftp scheme", in: BrandingConfig{FaviconURL: "ftp://example.com/x.ico"}, wantErr: "APP_FAVICON_URL"},
		{name: "too long URL", in: BrandingConfig{LogoURL: "/" + strings.Repeat("a", maxBrandingURLLength)}, wantErr: "at most"},
		{name: "too long title", in: BrandingConfig{Title: strings.Repeat("t", maxAppTitleLength+1)}, wantErr: "APP_TITLE must be at most"},
		{name: "control character in title", in: BrandingConfig{Title: "a\x07b"}, wantErr: "control characters"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := tt.in
			err := c.Validate()
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, c)
		})
	}
}

// TestLoadConfig_Branding checks the APP_* variables and that LoadConfig
// refuses an invalid URL at startup. Not parallel: t.Setenv.
func TestLoadConfig_Branding(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    BrandingConfig
		wantErr string
	}{
		{
			name: "defaults",
			want: BrandingConfig{Title: DefaultAppTitle, FaviconURL: DefaultAppFaviconURL},
		},
		{
			name: "values from the environment",
			env: map[string]string{
				"APP_TITLE":       "Platform Portal",
				"APP_LOGO_URL":    "/branding/logo.svg",
				"APP_FAVICON_URL": "https://cdn.example.com/favicon.ico",
			},
			want: BrandingConfig{Title: "Platform Portal", LogoURL: "/branding/logo.svg", FaviconURL: "https://cdn.example.com/favicon.ico"},
		},
		{
			name:    "invalid logo URL is refused at startup",
			env:     map[string]string{"APP_LOGO_URL": "javascript:alert(1)"},
			wantErr: "branding config: APP_LOGO_URL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ENV_FILE", "/nonexistent/.env")
			for _, k := range []string{"APP_TITLE", "APP_LOGO_URL", "APP_FAVICON_URL"} {
				t.Setenv(k, "")
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			cfg, err := LoadConfig()
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.Branding)
		})
	}
}

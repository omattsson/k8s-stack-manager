package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/internal/config"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// uiConfigSchema is the JSON schema of GET /api/v1/ui-config. No other fields
// are allowed: the endpoint is public.
const uiConfigSchema = `{
"type": "object",
"required": ["title", "logo_url", "favicon_url", "default_instance_ttl_minutes"],
"additionalProperties": false,
"properties": {
"title": {"type": "string", "minLength": 1},
"logo_url": {"type": "string"},
"favicon_url": {"type": "string", "minLength": 1},
"default_instance_ttl_minutes": {"type": "integer", "minimum": 0, "maximum": 43200}
}
}`

func TestGetUIConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		branding   config.BrandingConfig
		defaultTTL int
		want       UIConfigResponse
	}{
		{
			name:     "empty config gives the defaults",
			branding: config.BrandingConfig{},
			want:     UIConfigResponse{Title: config.DefaultAppTitle, LogoURL: "", FaviconURL: config.DefaultAppFaviconURL},
		},
		{
			name: "configured values",
			branding: config.BrandingConfig{
				Title: "Platform Portal", LogoURL: "/branding/logo.svg", FaviconURL: "https://cdn.example.com/favicon.ico",
			},
			want: UIConfigResponse{Title: "Platform Portal", LogoURL: "/branding/logo.svg", FaviconURL: "https://cdn.example.com/favicon.ico"},
		},
		{
			name:       "TTL default",
			defaultTTL: 480,
			want:       UIConfigResponse{Title: config.DefaultAppTitle, FaviconURL: config.DefaultAppFaviconURL, DefaultInstanceTTLMinutes: 480},
		},
		{
			name:       "TTL default at the maximum",
			defaultTTL: MaxTTLMinutes,
			want:       UIConfigResponse{Title: config.DefaultAppTitle, FaviconURL: config.DefaultAppFaviconURL, DefaultInstanceTTLMinutes: MaxTTLMinutes},
		},
		{
			name:       "negative TTL default is sent as 0",
			defaultTTL: -5,
			want:       UIConfigResponse{Title: config.DefaultAppTitle, FaviconURL: config.DefaultAppFaviconURL},
		},
		{
			name:       "TTL default above the maximum is sent as 0",
			defaultTTL: MaxTTLMinutes + 1,
			want:       UIConfigResponse{Title: config.DefaultAppTitle, FaviconURL: config.DefaultAppFaviconURL},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gin.SetMode(gin.TestMode)
			r := gin.New()
			r.GET("/api/v1/ui-config", NewUIConfigHandler(tt.branding, tt.defaultTTL))

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/ui-config", nil)
			r.ServeHTTP(w, req)

			require.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, "public, max-age=300", w.Header().Get("Cache-Control"))
			assert.True(t, validateJSONSchema(t, uiConfigSchema, w.Body.Bytes()), w.Body.String())
			var got UIConfigResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
			assert.Equal(t, tt.want, got)
		})
	}
}

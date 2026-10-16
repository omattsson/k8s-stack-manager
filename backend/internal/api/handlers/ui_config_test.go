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
"required": ["title", "logo_url", "favicon_url", "default_instance_ttl_minutes", "max_instance_ttl_days"],
"additionalProperties": false,
"properties": {
"title": {"type": "string", "minLength": 1},
"logo_url": {"type": "string"},
"favicon_url": {"type": "string", "minLength": 1},
"default_instance_ttl_minutes": {"type": "integer", "minimum": 0, "maximum": 525600},
"max_instance_ttl_days": {"type": "integer", "minimum": 1, "maximum": 365}
}
}`

func TestGetUIConfig(t *testing.T) {
	t.Parallel()

	const sevenDays = 7 * 24 * 60
	defaults := func(r UIConfigResponse) UIConfigResponse {
		if r.Title == "" {
			r.Title = config.DefaultAppTitle
		}
		if r.FaviconURL == "" {
			r.FaviconURL = config.DefaultAppFaviconURL
		}
		if r.MaxInstanceTTLDays == 0 {
			r.MaxInstanceTTLDays = 30
		}
		return r
	}

	tests := []struct {
		name       string
		branding   config.BrandingConfig
		defaultTTL int
		maxDays    int
		want       UIConfigResponse
	}{
		{
			name:     "empty config gives the defaults",
			branding: config.BrandingConfig{},
			want:     defaults(UIConfigResponse{}),
		},
		{
			name: "configured values",
			branding: config.BrandingConfig{
				Title: "Platform Portal", LogoURL: "/branding/logo.svg", FaviconURL: "https://cdn.example.com/favicon.ico",
			},
			want: defaults(UIConfigResponse{Title: "Platform Portal", LogoURL: "/branding/logo.svg", FaviconURL: "https://cdn.example.com/favicon.ico"}),
		},
		{
			name:       "TTL default",
			defaultTTL: 480,
			want:       defaults(UIConfigResponse{DefaultInstanceTTLMinutes: 480}),
		},
		{
			name:       "TTL default at the maximum",
			defaultTTL: MaxTTLMinutes,
			want:       defaults(UIConfigResponse{DefaultInstanceTTLMinutes: MaxTTLMinutes}),
		},
		{
			name:       "negative TTL default is sent as 0",
			defaultTTL: -5,
			want:       defaults(UIConfigResponse{}),
		},
		{
			name:       "TTL default above the maximum is sent as 0",
			defaultTTL: MaxTTLMinutes + 1,
			want:       defaults(UIConfigResponse{}),
		},
		{
			name:    "configured maximum",
			maxDays: 7,
			want:    defaults(UIConfigResponse{MaxInstanceTTLDays: 7}),
		},
		{
			name:       "TTL default at a configured maximum",
			defaultTTL: sevenDays,
			maxDays:    7,
			want:       defaults(UIConfigResponse{DefaultInstanceTTLMinutes: sevenDays, MaxInstanceTTLDays: 7}),
		},
		{
			name:       "TTL default above a configured maximum is sent as 0",
			defaultTTL: sevenDays + 1,
			maxDays:    7,
			want:       defaults(UIConfigResponse{MaxInstanceTTLDays: 7}),
		},
		{
			name:       "TTL default above 30 days with a larger maximum",
			defaultTTL: MaxTTLMinutes + 60,
			maxDays:    90,
			want:       defaults(UIConfigResponse{DefaultInstanceTTLMinutes: MaxTTLMinutes + 60, MaxInstanceTTLDays: 90}),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gin.SetMode(gin.TestMode)
			r := gin.New()
			r.GET("/api/v1/ui-config", NewUIConfigHandler(tt.branding, tt.defaultTTL, tt.maxDays))

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

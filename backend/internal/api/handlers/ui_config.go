package handlers

import (
	"net/http"

	"backend/internal/config"

	"github.com/gin-gonic/gin"
)

// uiConfigCacheControl lets browsers and proxies keep the UI config for five
// minutes. A branding change shows after at most that time.
const uiConfigCacheControl = "public, max-age=300"

// UIConfigResponse holds the display values and form defaults of the web UI.
// It contains no secret and no user data: the endpoint is public.
type UIConfigResponse struct {
	// Title is the product name (APP_TITLE).
	Title string `json:"title" example:"K8s Stack Manager"`
	// LogoURL is the logo image (APP_LOGO_URL). Empty: the built-in logo.
	LogoURL string `json:"logo_url" example:""`
	// FaviconURL is the browser tab icon (APP_FAVICON_URL).
	FaviconURL string `json:"favicon_url" example:"/favicon.svg"`
	// DefaultInstanceTTLMinutes is the start value of the TTL field in the
	// create forms (DEFAULT_INSTANCE_TTL_MINUTES). 0: no expiry.
	DefaultInstanceTTLMinutes int `json:"default_instance_ttl_minutes" example:"480"`
	// MaxInstanceTTLDays is the maximum TTL and the maximum distance of a
	// stop time from now (MAX_INSTANCE_TTL_DAYS). The forms limit the
	// TTL field and the stop-time picker with it.
	MaxInstanceTTLDays int `json:"max_instance_ttl_days" example:"30"`
	// PortForwardEnabled is true when the port-forward tunnels are on
	// (STACK_PORT_FORWARD_ENABLED). The UI shows the stackctl hint then.
	PortForwardEnabled bool `json:"port_forward_enabled" example:"false"`
}

// NewUIConfigHandler returns the handler of GET /api/v1/ui-config. The
// branding values come from config.BrandingConfig, which LoadConfig validates
// at startup. An empty title or favicon URL gets the default. defaultTTLMinutes
// is DEFAULT_INSTANCE_TTL_MINUTES, maxTTLDays is MAX_INSTANCE_TTL_DAYS (a
// value below 1 gives the default of 30 days). A default TTL below 1 or above
// the limit is sent as 0: the create endpoints ignore a value below 1 and
// reject a TTL above the limit, so the forms must not start with it.
func NewUIConfigHandler(b config.BrandingConfig, defaultTTLMinutes, maxTTLDays int, portForwardEnabled bool) gin.HandlerFunc {
	maxTTL := maxTTLMinutesFromDays(maxTTLDays)
	resp := UIConfigResponse{
		Title:              b.Title,
		LogoURL:            b.LogoURL,
		FaviconURL:         b.FaviconURL,
		MaxInstanceTTLDays: maxTTL / (24 * 60),
		PortForwardEnabled: portForwardEnabled,
	}
	if defaultTTLMinutes > 0 && defaultTTLMinutes <= maxTTL {
		resp.DefaultInstanceTTLMinutes = defaultTTLMinutes
	}
	if resp.Title == "" {
		resp.Title = config.DefaultAppTitle
	}
	if resp.FaviconURL == "" {
		resp.FaviconURL = config.DefaultAppFaviconURL
	}
	return func(c *gin.Context) {
		getUIConfig(c, resp)
	}
}

// getUIConfig godoc
// @Summary      Web UI display config
// @Description  Returns the display values of the web UI: product title, logo URL and favicon URL (APP_TITLE, APP_LOGO_URL, APP_FAVICON_URL), the start value of the TTL field in the create forms (default_instance_ttl_minutes, DEFAULT_INSTANCE_TTL_MINUTES; 0 means no expiry), the maximum TTL and stop-time distance in days (max_instance_ttl_days, MAX_INSTANCE_TTL_DAYS), and whether port-forward is on (port_forward_enabled, STACK_PORT_FORWARD_ENABLED). Public: no authentication, so the login page can use it. An empty logo_url means the built-in logo. A URL is a same-origin path or an https URL. Cached for 5 minutes.
// @Tags         ui
// @Produce      json
// @Success      200  {object}  UIConfigResponse
// @Failure      429  {object}  map[string]string
// @Router       /api/v1/ui-config [get]
func getUIConfig(c *gin.Context, resp UIConfigResponse) {
	c.Header("Cache-Control", uiConfigCacheControl)
	c.JSON(http.StatusOK, resp)
}

package handlers

import (
	"net/http"

	"backend/internal/config"

	"github.com/gin-gonic/gin"
)

// uiConfigCacheControl lets browsers and proxies keep the UI config for five
// minutes. A branding change shows after at most that time.
const uiConfigCacheControl = "public, max-age=300"

// UIConfigResponse holds the display values of the web UI. It contains no
// secret and no user data: the endpoint is public.
type UIConfigResponse struct {
	// Title is the product name (APP_TITLE).
	Title string `json:"title" example:"K8s Stack Manager"`
	// LogoURL is the logo image (APP_LOGO_URL). Empty: the built-in logo.
	LogoURL string `json:"logo_url" example:""`
	// FaviconURL is the browser tab icon (APP_FAVICON_URL).
	FaviconURL string `json:"favicon_url" example:"/favicon.svg"`
}

// NewUIConfigHandler returns the handler of GET /api/v1/ui-config. The values
// come from config.BrandingConfig, which LoadConfig validates at startup. An
// empty title or favicon URL gets the default.
func NewUIConfigHandler(b config.BrandingConfig) gin.HandlerFunc {
	resp := UIConfigResponse{Title: b.Title, LogoURL: b.LogoURL, FaviconURL: b.FaviconURL}
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
// @Description  Returns the display values of the web UI: product title, logo URL and favicon URL (APP_TITLE, APP_LOGO_URL, APP_FAVICON_URL). Public: no authentication, so the login page can use it. An empty logo_url means the built-in logo. A URL is a same-origin path or an https URL. Cached for 5 minutes.
// @Tags         ui
// @Produce      json
// @Success      200  {object}  UIConfigResponse
// @Failure      429  {object}  map[string]string
// @Router       /api/v1/ui-config [get]
func getUIConfig(c *gin.Context, resp UIConfigResponse) {
	c.Header("Cache-Control", uiConfigCacheControl)
	c.JSON(http.StatusOK, resp)
}

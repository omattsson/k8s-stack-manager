package config

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Branding defaults. The frontend build ships /favicon.svg and a built-in
// logo; an empty logo URL selects the built-in logo.
const (
	DefaultAppTitle      = "K8s Stack Manager"
	DefaultAppFaviconURL = "/favicon.svg"

	maxAppTitleLength    = 100
	maxBrandingURLLength = 2048
)

// BrandingConfig holds the display values of the web UI. GET /api/v1/ui-config
// serves them without authentication, so the login page can use them.
type BrandingConfig struct {
	// Title is the product name in the browser tab, sidebar, app bar,
	// login page and setup wizard (APP_TITLE).
	Title string
	// LogoURL is the logo image (APP_LOGO_URL). Empty: the built-in logo.
	LogoURL string
	// FaviconURL is the browser tab icon (APP_FAVICON_URL).
	FaviconURL string
}

func loadBrandingConfig() BrandingConfig {
	return BrandingConfig{
		Title:      strings.TrimSpace(getEnv("APP_TITLE", DefaultAppTitle)),
		LogoURL:    strings.TrimSpace(getEnv("APP_LOGO_URL", "")),
		FaviconURL: strings.TrimSpace(getEnv("APP_FAVICON_URL", DefaultAppFaviconURL)),
	}
}

// Validate checks the branding values. An empty title or favicon URL gets the
// default.
//
// URL rule: a same-origin path ("/branding/logo.svg") or an absolute https
// URL with a host. Every other form is refused: http (mixed content on an
// HTTPS page, and an image fetched in clear text), javascript: and other
// schemes, all data: URLs (an SVG data URL can carry script; serve inline
// files through the Helm value branding.files instead), protocol-relative
// URLs ("//host/x"), relative paths without a leading slash (they resolve
// against the current page), user info, spaces and control characters.
func (c *BrandingConfig) Validate() error {
	c.Title = strings.TrimSpace(c.Title)
	if c.Title == "" {
		c.Title = DefaultAppTitle
	}
	if utf8.RuneCountInString(c.Title) > maxAppTitleLength {
		return fmt.Errorf("APP_TITLE must be at most %d characters", maxAppTitleLength)
	}
	if strings.IndexFunc(c.Title, unicode.IsControl) >= 0 {
		return fmt.Errorf("APP_TITLE must not contain control characters")
	}

	if c.FaviconURL == "" {
		c.FaviconURL = DefaultAppFaviconURL
	}
	if err := validateBrandingURL(c.LogoURL); err != nil {
		return fmt.Errorf("APP_LOGO_URL %w", err)
	}
	if err := validateBrandingURL(c.FaviconURL); err != nil {
		return fmt.Errorf("APP_FAVICON_URL %w", err)
	}
	return nil
}

// validateBrandingURL checks one branding URL. Empty is valid.
func validateBrandingURL(raw string) error {
	if raw == "" {
		return nil
	}
	if len(raw) > maxBrandingURLLength {
		return fmt.Errorf("must be at most %d characters", maxBrandingURLLength)
	}
	if strings.IndexFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return fmt.Errorf("must not contain spaces or control characters")
	}
	if strings.Contains(raw, `\`) {
		return fmt.Errorf("must not contain a backslash")
	}
	if strings.HasPrefix(raw, "/") {
		if strings.HasPrefix(raw, "//") {
			return fmt.Errorf("must not be a protocol-relative URL (//host/...); use https://host/...")
		}
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("is not a valid URL")
	}
	if u.Scheme == "http" {
		return fmt.Errorf("must use https (http is not allowed); or use a same-origin path (/...)")
	}
	if u.Scheme != "https" {
		return fmt.Errorf("must be a same-origin path (/...) or an https URL")
	}
	if u.Host == "" {
		return fmt.Errorf("must have a host")
	}
	if u.User != nil {
		return fmt.Errorf("must not contain user info")
	}
	return nil
}

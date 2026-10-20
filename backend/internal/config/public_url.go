package config

import (
	"fmt"
	"net/url"
	"strings"
)

// ParsePublicURL checks APP_PUBLIC_URL, the public base URL of the web UI
// (for example https://stacks.example.com). Notification channel cards link
// to the instance page under it. Empty means unset and is valid.
//
// It uses the https rules of the branding URLs (validateBrandingURL): an
// absolute https URL with a host, without user info, spaces, control
// characters or a backslash. A same-origin path is refused, because a
// receiver outside the web UI cannot resolve it. A query or a fragment is
// refused too. The result has no trailing slash.
func ParsePublicURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("APP_PUBLIC_URL must be an absolute https URL with a host, not a path")
	}
	if err := validateBrandingURL(raw); err != nil {
		return "", fmt.Errorf("APP_PUBLIC_URL %w", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("APP_PUBLIC_URL is not a valid URL")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return "", fmt.Errorf("APP_PUBLIC_URL must not contain a query or a fragment")
	}
	return strings.TrimRight(raw, "/"), nil
}

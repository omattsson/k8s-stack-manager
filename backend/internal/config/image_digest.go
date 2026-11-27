package config

import (
	"fmt"
	"strings"

	"backend/internal/imagedigest"
)

// ParseImageDigestRegistries parses IMAGE_DIGEST_REGISTRIES: a comma-separated
// list of registry hosts with an optional port (for example
// "ghcr.io,registry.example.com:5000") that the imageDigest values template
// function may contact, in addition to the registry host of the target
// cluster, which is always allowed. Entries are lower case and normalized as
// go-containerregistry names them (docker.io becomes index.docker.io).
// Empty input returns nil: only the cluster registry is allowed.
func ParseImageDigestRegistries(raw string) ([]string, error) {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		host, err := imagedigest.ParseHost(part)
		if err != nil {
			return nil, fmt.Errorf("IMAGE_DIGEST_REGISTRIES: %w", err)
		}
		out = append(out, host)
	}
	return out, nil
}

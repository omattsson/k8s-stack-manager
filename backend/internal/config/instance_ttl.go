package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Limits of MAX_INSTANCE_TTL_DAYS.
const (
	// DefaultMaxInstanceTTLDays is the maximum TTL and stop-time distance
	// when MAX_INSTANCE_TTL_DAYS is not set.
	DefaultMaxInstanceTTLDays = 30
	// maxInstanceTTLDaysLimit is the highest allowed MAX_INSTANCE_TTL_DAYS.
	maxInstanceTTLDaysLimit = 365
)

// ParseMaxInstanceTTLDays parses MAX_INSTANCE_TTL_DAYS: a whole number of
// days from 1 to 365. An empty value gives DefaultMaxInstanceTTLDays.
func ParseMaxInstanceTTLDays(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultMaxInstanceTTLDays, nil
	}
	days, err := strconv.Atoi(raw)
	if err != nil || days < 1 || days > maxInstanceTTLDaysLimit {
		return 0, fmt.Errorf("MAX_INSTANCE_TTL_DAYS must be a whole number from 1 to %d, got %q", maxInstanceTTLDaysLimit, raw)
	}
	return days, nil
}

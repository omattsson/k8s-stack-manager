package config

import (
	"fmt"
	"strconv"
	"strings"
)

// AuditConfig holds the audit log settings.
type AuditConfig struct {
	// Stdout writes each audit entry as one JSON line to stdout (env:
	// AUDIT_LOG_STDOUT, default false), for a log pipeline to a SIEM.
	Stdout bool
}

// ParseAuditConfig reads AUDIT_LOG_STDOUT. An invalid value is an error.
func ParseAuditConfig(stdout string) (AuditConfig, error) {
	raw := strings.TrimSpace(stdout)
	if raw == "" {
		return AuditConfig{}, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return AuditConfig{}, fmt.Errorf("AUDIT_LOG_STDOUT: %q is not a boolean", raw)
	}
	return AuditConfig{Stdout: v}, nil
}

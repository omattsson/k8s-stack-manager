package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"backend/internal/models"
)

// InstanceFilter holds parsed filter criteria for matching stack instances.
type InstanceFilter struct {
	Status     string // e.g. "stopped"
	IdleDays   int    // days since last deploy
	AgeDays    int    // days since creation
	TTLExpired bool   // instances past their TTL
}

// ParseCondition parses a condition string into an InstanceFilter.
// Format: key:value pairs separated by commas.
// Examples: "idle_days:7", "status:stopped,age_days:14", "ttl_expired"
func ParseCondition(condition string) (*InstanceFilter, error) {
	filter := &InstanceFilter{}
	parts := strings.Split(condition, ",")
	for _, part := range parts {
		kv := strings.SplitN(strings.TrimSpace(part), ":", 2)
		key := strings.TrimSpace(kv[0])
		value := ""
		if len(kv) > 1 {
			value = strings.TrimSpace(kv[1])
		}
		switch key {
		case "idle_days":
			days, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid idle_days value: %s", value)
			}
			filter.IdleDays = days
		case "status":
			filter.Status = value
		case "age_days":
			days, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid age_days value: %s", value)
			}
			filter.AgeDays = days
		case "ttl_expired":
			filter.TTLExpired = true
		default:
			return nil, fmt.Errorf("unknown condition key: %s", key)
		}
	}
	return filter, nil
}

// MatchesInstance checks if an instance matches the filter criteria.
// All non-zero filter fields must match (AND logic).
func (f *InstanceFilter) MatchesInstance(inst *models.StackInstance) bool {
	now := time.Now()

	if f.Status != "" && inst.Status != f.Status {
		return false
	}

	if f.IdleDays > 0 {
		lastActive := inst.LastDeployedAt
		if lastActive == nil {
			lastActive = &inst.CreatedAt
		}
		if now.Sub(*lastActive).Hours() < float64(f.IdleDays*24) {
			return false
		}
	}

	if f.AgeDays > 0 {
		if now.Sub(inst.CreatedAt).Hours() < float64(f.AgeDays*24) {
			return false
		}
	}

	if f.TTLExpired {
		if inst.ExpiresAt == nil || !inst.ExpiresAt.Before(now) {
			return false
		}
	}

	return true
}

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
	Status      string // e.g. "stopped"
	IdleDays    int    // days since last deploy (or creation when never deployed); skips a running or partial stack with a future expiry
	AgeDays     int    // days since creation
	StoppedDays int    // days since the last successful stop (StoppedAt)
	CleanedDays int    // days since the last successful clean (CleanedAt)
	TTLExpired  bool   // instances past their TTL
	// ExpiresWithinHours: no expiry, or the expiry is at most this many hours away.
	ExpiresWithinHours int
	// hasStoppedDays is true when the condition contains stopped_days, so
	// that stopped_days:0 still requires a stopped instance with StoppedAt.
	hasStoppedDays bool
	// hasCleanedDays is the same for cleaned_days.
	hasCleanedDays bool
	// hasExpiresWithin is the same for expires_within_hours.
	hasExpiresWithin bool
}

// maxExpiresWithinHours caps expires_within_hours at one year. A larger value
// adds nothing (an expiry is never that far away) and would overflow
// time.Duration.
const maxExpiresWithinHours = 365 * 24

// ParseCondition parses a condition string into an InstanceFilter.
// Format: key:value pairs separated by commas (all must match).
// Keys:
//   - status:<status>  the instance status equals <status>
//   - idle_days:<N>    no deploy for N days (creation time when never deployed).
//     A running or partial instance whose TTL expiry is still in the future
//     never matches: its owner chose (or extended) that expiry, and the TTL
//     reaper stops it then.
//   - age_days:<N>     created more than N days ago (creation age, not stop time)
//   - stopped_days:<N> stopped, and the last stop finished N or more days ago;
//     instances without a recorded stop time never match
//   - cleaned_days:<N> a draft, and the last clean finished N or more days ago;
//     instances without a recorded clean time never match
//   - ttl_expired      the TTL expiry time is in the past
//   - expires_within_hours:<N> no expiry, or the expiry is at most N hours
//     from now (a past expiry too). An instance whose owner set a later
//     expiry (extended TTL, custom TTL) does not match. An instance without
//     an expiry (TTL 0) matches. Use N a little above the default TTL, so
//     stacks with the normal TTL still match, and combine it with status:.
//
// Examples: "idle_days:7", "status:stopped,age_days:14", "stopped_days:3", "ttl_expired",
// "status:running,expires_within_hours:12"
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
		case "stopped_days":
			days, err := strconv.Atoi(value)
			if err != nil || days < 0 {
				return nil, fmt.Errorf("invalid stopped_days value: %s", value)
			}
			filter.StoppedDays = days
			filter.hasStoppedDays = true
		case "cleaned_days":
			days, err := strconv.Atoi(value)
			if err != nil || days < 0 {
				return nil, fmt.Errorf("invalid cleaned_days value: %s", value)
			}
			filter.CleanedDays = days
			filter.hasCleanedDays = true
		case "ttl_expired":
			filter.TTLExpired = true
		case "expires_within_hours":
			hours, err := strconv.Atoi(value)
			if err != nil || hours < 0 || hours > maxExpiresWithinHours {
				return nil, fmt.Errorf("invalid expires_within_hours value: %s (0 to %d)", value, maxExpiresWithinHours)
			}
			filter.ExpiresWithinHours = hours
			filter.hasExpiresWithin = true
		default:
			return nil, fmt.Errorf("unknown condition key: %s", key)
		}
	}
	return filter, nil
}

// MatchesInstance checks if an instance matches the filter criteria.
// All non-zero filter fields must match (AND logic).
func (f *InstanceFilter) MatchesInstance(inst *models.StackInstance) bool {
	return f.matchesAt(inst, time.Now())
}

// matchesAt is MatchesInstance at the time now (tests use a fixed time).
func (f *InstanceFilter) matchesAt(inst *models.StackInstance, now time.Time) bool {

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
		// A future expiry is a deliberate choice (for example a TTL extended
		// for a demo), and the TTL reaper stops a running or partial instance
		// at that expiry. Other statuses keep matching: the reaper does not
		// stop them, and a stop or clean keeps expires_at.
		if (inst.Status == models.StackStatusRunning || inst.Status == models.StackStatusPartial) &&
			inst.ExpiresAt != nil && inst.ExpiresAt.After(now) {
			return false
		}
	}

	if f.AgeDays > 0 {
		if now.Sub(inst.CreatedAt).Hours() < float64(f.AgeDays*24) {
			return false
		}
	}

	if f.hasStoppedDays {
		if inst.Status != models.StackStatusStopped || inst.StoppedAt == nil {
			return false
		}
		if now.Sub(*inst.StoppedAt).Hours() < float64(f.StoppedDays*24) {
			return false
		}
	}

	if f.hasCleanedDays {
		if inst.Status != models.StackStatusDraft || inst.CleanedAt == nil {
			return false
		}
		if now.Sub(*inst.CleanedAt).Hours() < float64(f.CleanedDays*24) {
			return false
		}
	}

	if f.TTLExpired {
		if inst.ExpiresAt == nil || !inst.ExpiresAt.Before(now) {
			return false
		}
	}

	if f.hasExpiresWithin && inst.ExpiresAt != nil &&
		inst.ExpiresAt.After(now.Add(time.Duration(f.ExpiresWithinHours)*time.Hour)) {
		return false
	}

	return true
}

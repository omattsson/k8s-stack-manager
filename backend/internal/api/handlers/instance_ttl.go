package handlers

import (
	"fmt"
	"time"
)

// Messages of the TTL and stop-time rules.
const (
	msgTTLAndStopTime  = "Send either ttl_minutes or expires_at, not both"
	msgStopTimePassed  = "The stop time has passed. Set a new stop time or a TTL."
	msgStopTimeNotNext = "expires_at must be in the future"
)

// maxTTLMinutesFromDays converts MAX_INSTANCE_TTL_DAYS to minutes. A value
// below 1 gives MaxTTLMinutes (30 days), the default of the setting.
func maxTTLMinutesFromDays(days int) int {
	if days < 1 {
		return MaxTTLMinutes
	}
	return days * 24 * 60
}

// daysText formats a limit in minutes as days, for example "30 days".
func daysText(maxMinutes int) string {
	days := maxMinutes / (24 * 60)
	if days == 1 {
		return "1 day"
	}
	return fmt.Sprintf("%d days", days)
}

// ttlExceedsMessage is the 400 message for a TTL above the limit.
func ttlExceedsMessage(maxMinutes int) string {
	return fmt.Sprintf("TTL must not exceed %d minutes (%s)", maxMinutes, daysText(maxMinutes))
}

// stopTimeError checks a fixed stop time (expires_at). It returns "" when the
// stop time is after now and not later than now plus the limit.
func stopTimeError(stop, now time.Time, maxMinutes int) string {
	if !stop.After(now) {
		return msgStopTimeNotNext
	}
	if stop.After(now.Add(time.Duration(maxMinutes) * time.Minute)) {
		return fmt.Sprintf("expires_at must not be later than %s from now", daysText(maxMinutes))
	}
	return ""
}

// hasFixedStopTime reports whether ttlMinutes and expiresAt describe a fixed
// stop time: no TTL, but an expiry time.
func hasFixedStopTime(ttlMinutes int, expiresAt *time.Time) bool {
	return ttlMinutes == 0 && expiresAt != nil
}

// stopTimePassed reports whether an instance has a fixed stop time that is not
// in the future. A deploy then gives 409: the TTL reaper would stop the stack
// again at once.
func stopTimePassed(ttlMinutes int, expiresAt *time.Time, now time.Time) bool {
	return hasFixedStopTime(ttlMinutes, expiresAt) && !expiresAt.After(now)
}

// requestedExpiry resolves ttl_minutes and expires_at of a create request.
// defaultTTL applies only when both are omitted. It returns the TTL, the
// expiry time and a 400 message ("" when the request is valid).
func requestedExpiry(ttlMinutes *int, expiresAt *time.Time, defaultTTL, maxMinutes int, now time.Time) (int, *time.Time, string) {
	if expiresAt != nil {
		if ttlMinutes != nil {
			return 0, nil, msgTTLAndStopTime
		}
		stop := expiresAt.UTC()
		if msg := stopTimeError(stop, now, maxMinutes); msg != "" {
			return 0, nil, msg
		}
		return 0, &stop, ""
	}

	ttl := 0
	switch {
	case ttlMinutes != nil:
		ttl = *ttlMinutes
	case defaultTTL > 0:
		ttl = defaultTTL
	}
	if ttl < 0 {
		return 0, nil, "ttl_minutes must be non-negative"
	}
	if ttl > maxMinutes {
		return 0, nil, ttlExceedsMessage(maxMinutes)
	}
	if ttl == 0 {
		return 0, nil, ""
	}
	exp := now.Add(time.Duration(ttl) * time.Minute)
	return ttl, &exp, ""
}

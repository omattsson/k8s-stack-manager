package config

import (
	"fmt"
	"strconv"
	"strings"
)

// minHelmHistoryMax is the lowest allowed HELM_HISTORY_MAX. A plain rollback
// (helm rollback to the previous revision) needs that revision, so a release
// must keep the current revision and one before it.
const minHelmHistoryMax = 2

// ParseHelmHistoryMax parses HELM_HISTORY_MAX: the number of revisions Helm
// keeps for each release (--history-max), a whole number of 2 or more. An empty
// value gives 0: no --history-max, so the Helm default (10) applies.
func ParseHelmHistoryMax(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < minHelmHistoryMax {
		return 0, fmt.Errorf("HELM_HISTORY_MAX must be a whole number of %d or more, got %q", minHelmHistoryMax, raw)
	}
	return n, nil
}

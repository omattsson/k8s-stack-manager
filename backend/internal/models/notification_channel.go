package models

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
)

// NotificationChannel represents an outgoing webhook channel for system notifications.
type NotificationChannel struct {
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	ID         string    `json:"id" gorm:"primaryKey;size:36"`
	Name       string    `json:"name" gorm:"size:255;uniqueIndex;not null"`
	WebhookURL string    `json:"webhook_url" gorm:"size:2048;not null"`
	Secret     string    `json:"-" gorm:"type:text"`
	// Filters limits the events of the channel to some stack instances.
	// Empty filters (the default) let all events through.
	Filters NotificationChannelFilters `json:"filters" gorm:"type:text"`
	// Enabled has no GORM default on purpose: with default:true GORM
	// writes true for a new channel with enabled=false (zero value). The
	// handler sets true when the request omits it; the column keeps its
	// database default (migration 40).
	Enabled bool `json:"enabled"`
}

// NotificationChannelSubscription links a channel to a specific event type.
type NotificationChannelSubscription struct {
	ID        string `json:"id" gorm:"primaryKey;size:36"`
	ChannelID string `json:"channel_id" gorm:"size:36;uniqueIndex:idx_channel_event;not null"`
	EventType string `json:"event_type" gorm:"size:50;uniqueIndex:idx_channel_event;not null"`
}

// NotificationDeliveryLog records each webhook delivery attempt.
type NotificationDeliveryLog struct {
	CreatedAt    time.Time `json:"created_at" gorm:"index"`
	ID           string    `json:"id" gorm:"primaryKey;size:36"`
	ChannelID    string    `json:"channel_id" gorm:"size:36;index;not null"`
	ChannelName  string    `json:"channel_name" gorm:"size:255"`
	EventType    string    `json:"event_type" gorm:"size:50"`
	Status       string    `json:"status" gorm:"size:20;not null"`
	StatusCode   int       `json:"status_code"`
	ErrorMessage string    `json:"error_message,omitempty" gorm:"type:text"`
}

// NotificationChannelRepository defines persistence operations for notification channels.
type NotificationChannelRepository interface {
	CreateChannel(ctx context.Context, channel *NotificationChannel) error
	GetChannel(ctx context.Context, id string) (*NotificationChannel, error)
	UpdateChannel(ctx context.Context, channel *NotificationChannel, secretChanged bool) error
	DeleteChannel(ctx context.Context, id string) error
	ListChannels(ctx context.Context) ([]NotificationChannel, error)
	ListEnabledChannels(ctx context.Context) ([]NotificationChannel, error)
	SetSubscriptions(ctx context.Context, channelID string, eventTypes []string) error
	GetSubscriptions(ctx context.Context, channelID string) ([]NotificationChannelSubscription, error)
	CountSubscriptionsByChannel(ctx context.Context) (map[string]int, error)
	FindChannelsByEvent(ctx context.Context, eventType string) ([]NotificationChannel, error)
	CreateDeliveryLog(ctx context.Context, log *NotificationDeliveryLog) error
	ListDeliveryLogs(ctx context.Context, channelID string, limit, offset int) ([]NotificationDeliveryLog, int64, error)
}

// Limits for NotificationChannelFilters.
const (
	// MaxChannelFilterValues is the maximum number of values in one filter.
	MaxChannelFilterValues = 50
	// MaxChannelFilterPatternLength is the maximum length of a name pattern.
	MaxChannelFilterPatternLength = 100
	// maxChannelFilterIDLength is the maximum length of an ID in a filter.
	maxChannelFilterIDLength = 36
)

// NotificationChannelFilters limits the events of a notification channel to
// some stack instances. Each set filter must match (AND). Inside one filter,
// one matching value is enough (OR). An empty filter matches all instances.
//
// An event without an instance (for example a quota warning) goes to a
// channel only when all filters are empty. An event with more than one
// instance (a cleanup policy run) goes to the channel when at least one
// instance matches.
type NotificationChannelFilters struct {
	// InstanceNamePatterns are glob patterns (path.Match syntax: *, ?,
	// [a-z]) for the instance name, for example "rdbtest-*". The match
	// ignores case.
	InstanceNamePatterns []string `json:"instance_name_patterns,omitempty"`
	// OwnerIDs are user IDs of instance owners.
	OwnerIDs []string `json:"owner_ids,omitempty"`
	// DefinitionIDs are stack definition IDs.
	DefinitionIDs []string `json:"definition_ids,omitempty"`
	// ClusterIDs are cluster IDs.
	ClusterIDs []string `json:"cluster_ids,omitempty"`
}

// IsEmpty reports whether no filter is set.
func (f NotificationChannelFilters) IsEmpty() bool {
	return len(f.InstanceNamePatterns) == 0 && len(f.OwnerIDs) == 0 &&
		len(f.DefinitionIDs) == 0 && len(f.ClusterIDs) == 0
}

// Matches reports whether the instance of target matches all set filters.
// Empty filters match every target.
func (f NotificationChannelFilters) Matches(target NotificationTarget) bool {
	if len(f.InstanceNamePatterns) > 0 && !matchAnyPattern(f.InstanceNamePatterns, target.InstanceName) {
		return false
	}
	if len(f.OwnerIDs) > 0 && !containsString(f.OwnerIDs, target.OwnerID) {
		return false
	}
	if len(f.DefinitionIDs) > 0 && !containsString(f.DefinitionIDs, target.DefinitionID) {
		return false
	}
	if len(f.ClusterIDs) > 0 && !containsString(f.ClusterIDs, target.ClusterID) {
		return false
	}
	return true
}

// MatchesAny reports whether an event with these instances goes to a channel
// with the filters f. Empty filters match all events. With a filter set, an
// event without an instance does not match, and an event with instances
// matches when at least one instance matches.
func (f NotificationChannelFilters) MatchesAny(targets []NotificationTarget) bool {
	if f.IsEmpty() {
		return true
	}
	for _, t := range targets {
		if f.Matches(t) {
			return true
		}
	}
	return false
}

func matchAnyPattern(patterns []string, name string) bool {
	if name == "" {
		return false
	}
	name = strings.ToLower(name)
	for _, p := range patterns {
		// Normalize validates the patterns; a bad pattern does not match.
		if ok, err := path.Match(p, name); err == nil && ok {
			return true
		}
	}
	return false
}

func containsString(values []string, v string) bool {
	if v == "" {
		return false
	}
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}

// Normalize trims the values, removes empty values and duplicates, and
// writes the name patterns in lowercase. It returns an error when a pattern
// is not valid (path.Match syntax), when a value is too long, or when a
// filter has more than MaxChannelFilterValues values.
func (f *NotificationChannelFilters) Normalize() error {
	var err error
	if f.InstanceNamePatterns, err = normalizeFilterValues("instance_name_patterns", f.InstanceNamePatterns, MaxChannelFilterPatternLength, true); err != nil {
		return err
	}
	for _, p := range f.InstanceNamePatterns {
		if _, matchErr := path.Match(p, ""); matchErr != nil {
			return fmt.Errorf("instance_name_patterns: %q is not a valid pattern", p)
		}
	}
	if f.OwnerIDs, err = normalizeFilterValues("owner_ids", f.OwnerIDs, maxChannelFilterIDLength, false); err != nil {
		return err
	}
	if f.DefinitionIDs, err = normalizeFilterValues("definition_ids", f.DefinitionIDs, maxChannelFilterIDLength, false); err != nil {
		return err
	}
	if f.ClusterIDs, err = normalizeFilterValues("cluster_ids", f.ClusterIDs, maxChannelFilterIDLength, false); err != nil {
		return err
	}
	return nil
}

func normalizeFilterValues(field string, values []string, maxLen int, lower bool) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if lower {
			v = strings.ToLower(v)
		}
		if v == "" || seen[v] {
			continue
		}
		if len(v) > maxLen {
			return nil, fmt.Errorf("%s: a value is longer than %d characters", field, maxLen)
		}
		seen[v] = true
		out = append(out, v)
	}
	if len(out) > MaxChannelFilterValues {
		return nil, fmt.Errorf("%s: at most %d values are allowed", field, MaxChannelFilterValues)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// Value stores the filters as JSON text. Empty filters are stored as NULL.
func (f NotificationChannelFilters) Value() (driver.Value, error) {
	if f.IsEmpty() {
		return nil, nil
	}
	b, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// Scan reads the filters from JSON text. NULL and an empty string give
// empty filters.
func (f *NotificationChannelFilters) Scan(value interface{}) error {
	*f = NotificationChannelFilters{}
	var raw []byte
	switch v := value.(type) {
	case nil:
		return nil
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return errors.New("notification channel filters: unsupported column type")
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	return json.Unmarshal(raw, f)
}

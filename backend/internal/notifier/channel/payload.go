package channel

import "time"

// EventPayload is the generic JSON payload sent to all notification channels.
// Extensions (teams-notifier, slack-notifier, etc.) parse and format this for
// their platform.
type EventPayload struct {
	EventType       string            `json:"event_type"`
	Timestamp       time.Time         `json:"timestamp"`
	Title           string            `json:"title"`
	Message         string            `json:"message"`
	UserDisplayName string            `json:"user_display_name,omitempty"`
	EntityType      string            `json:"entity_type,omitempty"`
	EntityID        string            `json:"entity_id,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

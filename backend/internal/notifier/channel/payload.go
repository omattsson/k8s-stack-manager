package channel

import (
	"time"

	"backend/internal/hooks"
	"backend/internal/models"
)

// EventPayload is the generic JSON payload sent to all notification channels.
// Extensions (teams-notifier, slack-notifier, etc.) parse and format this for
// their platform. The fields after Metadata (issue #642) are omitted when
// empty; the older fields keep their order and meaning.
type EventPayload struct {
	EventType string    `json:"event_type"`
	Timestamp time.Time `json:"timestamp"`
	Title     string    `json:"title"`
	Message   string    `json:"message"`
	// UserDisplayName is the display name of the instance owner for an
	// instance event ("System" for a system event).
	//
	// Deprecated: kept for compatibility. It is not the user who started
	// the operation: use OwnerDisplayName for the owner and Trigger for the
	// actor.
	UserDisplayName string            `json:"user_display_name,omitempty"`
	EntityType      string            `json:"entity_type,omitempty"`
	EntityID        string            `json:"entity_id,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	// Trigger is what started the operation of the event, with the same
	// JSON names as the trigger of the hook envelope (type, id, name). Nil
	// when the caller does not know it (for example an operation that a
	// server restart interrupted, or an expiry warning).
	Trigger *hooks.Trigger `json:"trigger,omitempty"`
	// OwnerDisplayName is the display name (else the username) of the
	// owner of the instance.
	OwnerDisplayName string `json:"owner_display_name,omitempty"`
	// Namespace, Branch, ClusterID and ClusterName describe the instance.
	// An instance on the default cluster has no cluster ID.
	Namespace   string `json:"namespace,omitempty"`
	Branch      string `json:"branch,omitempty"`
	ClusterID   string `json:"cluster_id,omitempty"`
	ClusterName string `json:"cluster_name,omitempty"`
	// SiteURL is the primary URL of the stack (the Ingress with the
	// annotation k8s-stack-manager.io/primary-url, else the first Ingress
	// by name). Only deployment.success, deployment.partial and
	// rollback.completed carry it.
	SiteURL string `json:"site_url,omitempty"`
	// InstanceURL is the instance page in the web UI
	// (<APP_PUBLIC_URL>/stack-instances/<id>). Empty without APP_PUBLIC_URL
	// and for instance.deleted.
	InstanceURL string `json:"instance_url,omitempty"`
	// Instances are the stack instances of the event, for the channel
	// filters (see models.NotificationChannelFilters). They are not sent.
	// Empty for an event without an instance.
	Instances []models.NotificationTarget `json:"-"`
}

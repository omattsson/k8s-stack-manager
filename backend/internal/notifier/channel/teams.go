package channel

import (
	"encoding/json"
	"time"

	"backend/internal/hooks"
	"backend/internal/models"
)

// Constants of the Microsoft Teams Adaptive Card envelope.
const (
	teamsMessageType     = "message"
	teamsCardContentType = "application/vnd.microsoft.card.adaptive"
	teamsCardSchema      = "http://adaptivecards.io/schemas/adaptive-card.json"
	teamsCardType        = "AdaptiveCard"
	teamsCardVersion     = "1.5"
	teamsTextBlockType   = "TextBlock"
	teamsFactSetType     = "FactSet"
	teamsOpenURLType     = "Action.OpenUrl"
	teamsTitleWeight     = "Bolder"
	teamsTitleSize       = "Medium"
	teamsTitleStyle      = "heading"
	teamsFactEventTitle  = "Event"
	teamsFactUserTitle   = "By"
	teamsFactTimeTitle   = "Time"
)

// Colours of the title of an instance card (Adaptive Card TextBlock color).
const (
	teamsColorDefault   = "Default"
	teamsColorGood      = "Good"
	teamsColorWarning   = "Warning"
	teamsColorAttention = "Attention"
)

// Facts and actions of an instance card.
const (
	teamsFactNamespace   = "Namespace"
	teamsFactBranch      = "Branch"
	teamsFactCluster     = "Cluster"
	teamsFactTriggeredBy = "Triggered by"
	teamsFactOwner       = "Owner"
	teamsActionOpenSite  = "Open site"
	teamsActionInstance  = "Stack Manager"

	triggeredByTTL       = "TTL expiry"
	triggeredByPolicy    = "Cleanup policy"
	triggeredByAUser     = "a user"
	teamsTitleSeparator  = " \u2014 "
	entityTypeInstance   = "stack_instance"
	eventInstanceDeleted = "instance.deleted"
)

// teamsInstanceEvent is the card title of an instance event.
type teamsInstanceEvent struct {
	Mark  string
	Title string
	Color string
}

// teamsInstanceEvents maps the event type of an instance event to the mark,
// title and colour of its card. An instance event that is not in the map
// uses the event title without a mark, in the default colour.
var teamsInstanceEvents = map[string]teamsInstanceEvent{
	"deployment.success":   {Mark: "\u2705", Title: "Deploy succeeded", Color: teamsColorGood},
	"deployment.partial":   {Mark: "\u26a0\ufe0f", Title: "Deploy partly succeeded", Color: teamsColorWarning},
	"deployment.warning":   {Mark: "\u26a0\ufe0f", Title: "Deploy succeeded with a warning", Color: teamsColorWarning},
	"deployment.error":     {Mark: "\u274c", Title: "Deploy failed", Color: teamsColorAttention},
	"deploy.timeout":       {Mark: "\u23f1", Title: "Deploy timed out", Color: teamsColorAttention},
	"deployment.stopped":   {Mark: "\u23f9", Title: "Stack stopped", Color: teamsColorDefault},
	"stop.error":           {Mark: "\u274c", Title: "Stop failed", Color: teamsColorAttention},
	"clean.completed":      {Mark: "\U0001f9f9", Title: "Stack cleaned", Color: teamsColorDefault},
	"clean.error":          {Mark: "\u274c", Title: "Clean failed", Color: teamsColorAttention},
	"instance.created":     {Mark: "\U0001f195", Title: "Stack created", Color: teamsColorDefault},
	eventInstanceDeleted:   {Mark: "\U0001f5d1", Title: "Stack deleted", Color: teamsColorDefault},
	"rollback.completed":   {Mark: "\u21a9", Title: "Rollback succeeded", Color: teamsColorGood},
	"rollback.error":       {Mark: "\u274c", Title: "Rollback failed", Color: teamsColorAttention},
	"stack.expiring":       {Mark: "\u23f3", Title: "Stack expires soon", Color: teamsColorWarning},
	"stack.expired":        {Mark: "\u23f9", Title: "Stack expired", Color: teamsColorWarning},
	"cleanup.policy.stop":  {Mark: "\U0001f9f9", Title: "Stopped by a cleanup policy", Color: teamsColorDefault},
	"cleanup.policy.clean": {Mark: "\U0001f9f9", Title: "Cleaned by a cleanup policy", Color: teamsColorDefault},
}

// teamsEnvelope is the body that a Teams workflow made from the template
// "Post to a channel when a webhook request is received" expects.
type teamsEnvelope struct {
	Type        string            `json:"type"`
	Attachments []teamsAttachment `json:"attachments"`
}

type teamsAttachment struct {
	ContentType string    `json:"contentType"`
	Content     teamsCard `json:"content"`
}

type teamsCard struct {
	Schema  string `json:"$schema"`
	Type    string `json:"type"`
	Version string `json:"version"`
	// Body holds teamsTextBlock and teamsFactSet elements.
	Body []any `json:"body"`
	// Actions are the buttons of an instance card; omitted when empty.
	Actions []teamsAction `json:"actions,omitempty"`
}

type teamsTextBlock struct {
	Type   string `json:"type"`
	Text   string `json:"text"`
	Weight string `json:"weight,omitempty"`
	Size   string `json:"size,omitempty"`
	Color  string `json:"color,omitempty"`
	Style  string `json:"style,omitempty"`
	Wrap   bool   `json:"wrap"`
}

type teamsFactSet struct {
	Type  string      `json:"type"`
	Facts []teamsFact `json:"facts"`
}

type teamsFact struct {
	Title string `json:"title"`
	Value string `json:"value"`
}

type teamsAction struct {
	Type  string `json:"type"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

// buildTeamsEnvelope wraps the event in a Teams Adaptive Card envelope. An
// instance event gets the instance card (buildTeamsInstanceBody); other
// events (cleanup.policy.executed, quota.warning, secret.expiring, test)
// get the system card (buildTeamsSystemBody).
func buildTeamsEnvelope(payload EventPayload) teamsEnvelope {
	var body []any
	var actions []teamsAction
	if isInstanceEvent(payload) {
		body, actions = buildTeamsInstanceBody(payload)
	} else {
		body = buildTeamsSystemBody(payload)
	}
	return teamsEnvelope{
		Type: teamsMessageType,
		Attachments: []teamsAttachment{{
			ContentType: teamsCardContentType,
			Content: teamsCard{
				Schema:  teamsCardSchema,
				Type:    teamsCardType,
				Version: teamsCardVersion,
				Body:    body,
				Actions: actions,
			},
		}},
	}
}

// isInstanceEvent reports whether payload is the event of one stack
// instance (notifier.NotifyInstance).
func isInstanceEvent(payload EventPayload) bool {
	return payload.EntityType == entityTypeInstance && len(payload.Instances) == 1
}

// buildTeamsSystemBody is the card of an event without one instance. The
// title is a bold text block (the event type when the title is empty). An
// empty message gives no text block. Empty facts (Event, By, Time) are
// omitted, and a fact set without facts is omitted.
func buildTeamsSystemBody(payload EventPayload) []any {
	title := payload.Title
	if title == "" {
		title = payload.EventType
	}
	body := []any{
		teamsTextBlock{Type: teamsTextBlockType, Text: title, Weight: teamsTitleWeight, Size: teamsTitleSize, Wrap: true},
	}
	if payload.Message != "" {
		body = append(body, teamsTextBlock{Type: teamsTextBlockType, Text: payload.Message, Wrap: true})
	}

	facts := make([]teamsFact, 0, 3)
	if payload.EventType != "" {
		facts = append(facts, teamsFact{Title: teamsFactEventTitle, Value: payload.EventType})
	}
	if payload.UserDisplayName != "" {
		facts = append(facts, teamsFact{Title: teamsFactUserTitle, Value: payload.UserDisplayName})
	}
	if !payload.Timestamp.IsZero() {
		facts = append(facts, teamsFact{Title: teamsFactTimeTitle, Value: payload.Timestamp.UTC().Format(time.RFC3339)})
	}
	if len(facts) > 0 {
		body = append(body, teamsFactSet{Type: teamsFactSetType, Facts: facts})
	}
	return body
}

// buildTeamsInstanceBody is the card of an instance event:
//   - title "<mark> <title> — <stack name>", heading style, bold, medium, in
//     the colour of the event;
//   - the message;
//   - the facts Namespace, Branch, Cluster, Triggered by and Owner, each only
//     when it has a value;
//   - the buttons "Open site" (site_url) and "Stack Manager" (instance_url),
//     each only when it has a value. instance.deleted has no "Stack Manager"
//     button (the page is gone).
func buildTeamsInstanceBody(payload EventPayload) ([]any, []teamsAction) {
	ev, known := teamsInstanceEvents[payload.EventType]
	title := ev.Title
	color := ev.Color
	if !known {
		title = payload.Title
		if title == "" {
			title = payload.EventType
		}
		color = teamsColorDefault
	}
	if ev.Mark != "" {
		title = ev.Mark + " " + title
	}
	if name := payload.Instances[0].InstanceName; name != "" {
		title += teamsTitleSeparator + name
	}

	body := []any{
		teamsTextBlock{
			Type: teamsTextBlockType, Text: title, Weight: teamsTitleWeight, Size: teamsTitleSize,
			Color: color, Style: teamsTitleStyle, Wrap: true,
		},
	}
	if payload.Message != "" {
		body = append(body, teamsTextBlock{Type: teamsTextBlockType, Text: payload.Message, Wrap: true})
	}

	facts := make([]teamsFact, 0, 5)
	addFact := func(title, value string) {
		if value != "" {
			facts = append(facts, teamsFact{Title: title, Value: value})
		}
	}
	addFact(teamsFactNamespace, payload.Namespace)
	addFact(teamsFactBranch, payload.Branch)
	addFact(teamsFactCluster, payload.ClusterName)
	addFact(teamsFactTriggeredBy, triggeredBy(payload.Trigger))
	addFact(teamsFactOwner, payload.OwnerDisplayName)
	if len(facts) > 0 {
		body = append(body, teamsFactSet{Type: teamsFactSetType, Facts: facts})
	}

	var actions []teamsAction
	if payload.SiteURL != "" {
		actions = append(actions, teamsAction{Type: teamsOpenURLType, Title: teamsActionOpenSite, URL: payload.SiteURL})
	}
	if payload.InstanceURL != "" && payload.EventType != eventInstanceDeleted {
		actions = append(actions, teamsAction{Type: teamsOpenURLType, Title: teamsActionInstance, URL: payload.InstanceURL})
	}
	return body, actions
}

// triggeredBy is the "Triggered by" fact of a trigger: the username, "TTL
// expiry", "Cleanup policy <name>", or "a user" when the username is
// unknown. Nil or an unknown type without a name gives "" (no fact).
func triggeredBy(t *hooks.Trigger) string {
	if t == nil {
		return ""
	}
	switch t.Type {
	case hooks.TriggerTTL:
		return triggeredByTTL
	case hooks.TriggerCleanupPolicy:
		if t.Name == "" {
			return triggeredByPolicy
		}
		return triggeredByPolicy + " " + t.Name
	case hooks.TriggerUser:
		if t.Name == "" {
			return triggeredByAUser
		}
		return t.Name
	default:
		return t.Name
	}
}

// encodeBody returns the request body of the payload in the channel format.
// encoding/json escapes all text. An unknown or empty format gives the
// generic event JSON.
func encodeBody(format string, payload EventPayload) ([]byte, error) {
	if format == models.NotificationChannelFormatTeams {
		return json.Marshal(buildTeamsEnvelope(payload))
	}
	return json.Marshal(payload)
}

// bodyFormat returns the format key of a channel: teams, or generic for
// every other value.
func bodyFormat(ch *models.NotificationChannel) string {
	if ch.Format == models.NotificationChannelFormatTeams {
		return models.NotificationChannelFormatTeams
	}
	return models.NotificationChannelFormatGeneric
}

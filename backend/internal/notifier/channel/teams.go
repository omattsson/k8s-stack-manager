package channel

import (
	"encoding/json"
	"time"

	"backend/internal/models"
)

// Constants of the Microsoft Teams Adaptive Card envelope.
const (
	teamsMessageType     = "message"
	teamsCardContentType = "application/vnd.microsoft.card.adaptive"
	teamsCardSchema      = "http://adaptivecards.io/schemas/adaptive-card.json"
	teamsCardType        = "AdaptiveCard"
	teamsCardVersion     = "1.4"
	teamsTextBlockType   = "TextBlock"
	teamsFactSetType     = "FactSet"
	teamsTitleWeight     = "Bolder"
	teamsTitleSize       = "Medium"
	teamsFactEventTitle  = "Event"
	teamsFactUserTitle   = "By"
	teamsFactTimeTitle   = "Time"
)

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
}

type teamsTextBlock struct {
	Type   string `json:"type"`
	Text   string `json:"text"`
	Weight string `json:"weight,omitempty"`
	Size   string `json:"size,omitempty"`
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

// buildTeamsEnvelope wraps the event in a Teams Adaptive Card envelope. The
// title is a bold text block (the event type when the title is empty). An
// empty message gives no text block. Empty facts are omitted, and a fact set
// without facts is omitted.
func buildTeamsEnvelope(payload EventPayload) teamsEnvelope {
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

	return teamsEnvelope{
		Type: teamsMessageType,
		Attachments: []teamsAttachment{{
			ContentType: teamsCardContentType,
			Content: teamsCard{
				Schema:  teamsCardSchema,
				Type:    teamsCardType,
				Version: teamsCardVersion,
				Body:    body,
			},
		}},
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

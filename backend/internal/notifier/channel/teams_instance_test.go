package channel

import (
	"testing"
	"time"

	"backend/internal/hooks"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// instancePayload returns a full instance event payload for the Teams card
// tests.
func instancePayload(eventType string) EventPayload {
	return EventPayload{
		EventType:        eventType,
		Timestamp:        time.Date(2026, 10, 14, 12, 30, 0, 0, time.UTC),
		Title:            "Deployment succeeded",
		Message:          "Deployment of web completed successfully",
		UserDisplayName:  "Jane Owner",
		OwnerDisplayName: "Jane Owner",
		EntityType:       "stack_instance",
		EntityID:         "inst-1",
		Trigger:          &hooks.Trigger{Type: hooks.TriggerUser, ID: "u-2", Name: "devops-dan"},
		Namespace:        "stack-web-jane",
		Branch:           "feature/x",
		ClusterID:        "c-1",
		ClusterName:      "prod",
		SiteURL:          "https://web.example.com",
		InstanceURL:      "https://stacks.example.com/stack-instances/inst-1",
		Instances:        []models.NotificationTarget{{InstanceID: "inst-1", InstanceName: "web", OwnerID: "u-1"}},
	}
}

func actionsOf(actions []map[string]interface{}) map[string]string {
	out := make(map[string]string, len(actions))
	for _, a := range actions {
		out[a["title"].(string)] = a["url"].(string)
	}
	return out
}

func TestEncodeBody_TeamsInstanceCard(t *testing.T) {
	t.Parallel()

	allFacts := map[string]string{
		"Namespace": "stack-web-jane", "Branch": "feature/x", "Cluster": "prod",
		"Triggered by": "devops-dan", "Owner": "Jane Owner",
	}
	bothActions := map[string]string{
		"Open site":     "https://web.example.com",
		"Stack Manager": "https://stacks.example.com/stack-instances/inst-1",
	}

	tests := []struct {
		name        string
		mutate      func(p *EventPayload)
		wantTitle   string
		wantColor   string
		wantFacts   map[string]string
		wantActions map[string]string // nil: no actions
	}{
		{
			name:        "deploy success by a devops user on another user's stack",
			mutate:      func(*EventPayload) {},
			wantTitle:   "✅ Deploy succeeded — web",
			wantColor:   "Good",
			wantFacts:   allFacts,
			wantActions: bothActions,
		},
		{
			name: "TTL reaper stop",
			mutate: func(p *EventPayload) {
				p.EventType = "stack.expired"
				p.Trigger = &hooks.Trigger{Type: hooks.TriggerTTL}
				p.SiteURL = ""
			},
			wantTitle: "⏹ Stack expired — web",
			wantColor: "Warning",
			wantFacts: map[string]string{
				"Namespace": "stack-web-jane", "Branch": "feature/x", "Cluster": "prod",
				"Triggered by": "TTL expiry", "Owner": "Jane Owner",
			},
			wantActions: map[string]string{"Stack Manager": "https://stacks.example.com/stack-instances/inst-1"},
		},
		{
			name: "cleanup policy stop",
			mutate: func(p *EventPayload) {
				p.EventType = "cleanup.policy.stop"
				p.Trigger = &hooks.Trigger{Type: hooks.TriggerCleanupPolicy, ID: "p-1", Name: "nightly-stop"}
				p.SiteURL = ""
			},
			wantTitle: "\U0001f9f9 Stopped by a cleanup policy — web",
			wantColor: "Default",
			wantFacts: map[string]string{
				"Namespace": "stack-web-jane", "Branch": "feature/x", "Cluster": "prod",
				"Triggered by": "Cleanup policy nightly-stop", "Owner": "Jane Owner",
			},
			wantActions: map[string]string{"Stack Manager": "https://stacks.example.com/stack-instances/inst-1"},
		},
		{
			name: "cleanup policy without a name",
			mutate: func(p *EventPayload) {
				p.EventType = "cleanup.policy.clean"
				p.Trigger = &hooks.Trigger{Type: hooks.TriggerCleanupPolicy, ID: "p-1"}
			},
			wantTitle: "\U0001f9f9 Cleaned by a cleanup policy — web",
			wantColor: "Default",
			wantFacts: map[string]string{
				"Namespace": "stack-web-jane", "Branch": "feature/x", "Cluster": "prod",
				"Triggered by": "Cleanup policy", "Owner": "Jane Owner",
			},
			wantActions: bothActions,
		},
		{
			name:        "user trigger without a name says a user",
			mutate:      func(p *EventPayload) { p.Trigger = &hooks.Trigger{Type: hooks.TriggerUser} },
			wantTitle:   "✅ Deploy succeeded — web",
			wantColor:   "Good",
			wantFacts:   map[string]string{"Namespace": "stack-web-jane", "Branch": "feature/x", "Cluster": "prod", "Triggered by": "a user", "Owner": "Jane Owner"},
			wantActions: bothActions,
		},
		{
			name: "no trigger (interrupted operation) gives no Triggered by fact",
			mutate: func(p *EventPayload) {
				p.EventType = "deployment.error"
				p.Trigger = nil
				p.SiteURL = ""
			},
			wantTitle:   "❌ Deploy failed — web",
			wantColor:   "Attention",
			wantFacts:   map[string]string{"Namespace": "stack-web-jane", "Branch": "feature/x", "Cluster": "prod", "Owner": "Jane Owner"},
			wantActions: map[string]string{"Stack Manager": "https://stacks.example.com/stack-instances/inst-1"},
		},
		{
			name:        "no site URL gives no Open site button",
			mutate:      func(p *EventPayload) { p.SiteURL = "" },
			wantTitle:   "✅ Deploy succeeded — web",
			wantColor:   "Good",
			wantFacts:   allFacts,
			wantActions: map[string]string{"Stack Manager": "https://stacks.example.com/stack-instances/inst-1"},
		},
		{
			name:        "no APP_PUBLIC_URL gives no Stack Manager button",
			mutate:      func(p *EventPayload) { p.InstanceURL = "" },
			wantTitle:   "✅ Deploy succeeded — web",
			wantColor:   "Good",
			wantFacts:   allFacts,
			wantActions: map[string]string{"Open site": "https://web.example.com"},
		},
		{
			name: "no URLs gives no actions",
			mutate: func(p *EventPayload) {
				p.InstanceURL = ""
				p.SiteURL = ""
			},
			wantTitle: "✅ Deploy succeeded — web",
			wantColor: "Good",
			wantFacts: allFacts,
		},
		{
			name: "instance.deleted has no Stack Manager button",
			mutate: func(p *EventPayload) {
				p.EventType = "instance.deleted"
				p.SiteURL = ""
			},
			wantTitle: "\U0001f5d1 Stack deleted — web",
			wantColor: "Default",
			wantFacts: allFacts,
		},
		{
			name: "rollback completed",
			mutate: func(p *EventPayload) {
				p.EventType = "rollback.completed"
			},
			wantTitle:   "↩ Rollback succeeded — web",
			wantColor:   "Good",
			wantFacts:   allFacts,
			wantActions: bothActions,
		},
		{
			name: "empty facts are omitted",
			mutate: func(p *EventPayload) {
				p.EventType = "deployment.partial"
				p.Branch = ""
				p.ClusterName = ""
				p.ClusterID = ""
			},
			wantTitle:   "⚠️ Deploy partly succeeded — web",
			wantColor:   "Warning",
			wantFacts:   map[string]string{"Namespace": "stack-web-jane", "Triggered by": "devops-dan", "Owner": "Jane Owner"},
			wantActions: bothActions,
		},
		{
			name: "unknown instance event uses its title without a mark",
			mutate: func(p *EventPayload) {
				p.EventType = "instance.renamed"
				p.Title = "Stack renamed"
			},
			wantTitle:   "Stack renamed — web",
			wantColor:   "Default",
			wantFacts:   allFacts,
			wantActions: bothActions,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			payload := instancePayload("deployment.success")
			tt.mutate(&payload)
			body, err := encodeBody(models.NotificationChannelFormatTeams, payload)
			require.NoError(t, err)
			blocks, actions := decodeTeamsCardWithActions(t, body)
			require.Len(t, blocks, 3, "title, message, facts")

			title := blocks[0]
			assert.Equal(t, "TextBlock", title["type"])
			assert.Equal(t, tt.wantTitle, title["text"])
			assert.Equal(t, "heading", title["style"])
			assert.Equal(t, "Bolder", title["weight"])
			assert.Equal(t, "Medium", title["size"])
			assert.Equal(t, tt.wantColor, title["color"])

			assert.Equal(t, payload.Message, blocks[1]["text"])

			facts := factsOf(t, blocks[2])
			assert.Equal(t, tt.wantFacts, facts)
			assert.NotContains(t, facts, "By", "the owner is no longer shown as By")
			assert.NotContains(t, facts, "Event")

			if tt.wantActions == nil {
				assert.Nil(t, actions)
				return
			}
			for _, a := range actions {
				assert.Equal(t, "Action.OpenUrl", a["type"])
			}
			assert.Equal(t, tt.wantActions, actionsOf(actions))
		})
	}
}

// TestEncodeBody_TeamsSystemEventsKeepLayout checks that the events without
// one instance keep the card with the facts Event, By and Time.
func TestEncodeBody_TeamsSystemEventsKeepLayout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload EventPayload
	}{
		{name: "cleanup.policy.executed with instances", payload: EventPayload{
			EventType: "cleanup.policy.executed", Title: "Cleanup policy ran", UserDisplayName: "System",
			EntityType: "cleanup_policy", EntityID: "p-1",
			Instances: []models.NotificationTarget{{InstanceID: "a"}, {InstanceID: "b"}},
		}},
		{name: "quota.warning", payload: EventPayload{EventType: "quota.warning", Title: "Quota", UserDisplayName: "System", EntityType: "cluster"}},
		{name: "secret.expiring", payload: EventPayload{EventType: "secret.expiring", Title: "Secret", UserDisplayName: "System"}},
		{name: "stack_instance entity without instance data", payload: EventPayload{EventType: "deployment.success", Title: "Deployed", UserDisplayName: "System", EntityType: "stack_instance"}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			body, err := encodeBody(models.NotificationChannelFormatTeams, tt.payload)
			require.NoError(t, err)
			blocks := decodeTeamsCard(t, body)
			require.Len(t, blocks, 2)
			assert.Equal(t, tt.payload.Title, blocks[0]["text"])
			facts := factsOf(t, blocks[1])
			assert.Equal(t, tt.payload.EventType, facts["Event"])
			assert.Equal(t, "System", facts["By"])
		})
	}
}

func TestTriggeredBy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		trigger *hooks.Trigger
		want    string
	}{
		{name: "nil", trigger: nil, want: ""},
		{name: "user", trigger: &hooks.Trigger{Type: hooks.TriggerUser, ID: "u", Name: "alice"}, want: "alice"},
		{name: "user without name", trigger: &hooks.Trigger{Type: hooks.TriggerUser, ID: "u"}, want: "a user"},
		{name: "ttl", trigger: &hooks.Trigger{Type: hooks.TriggerTTL}, want: "TTL expiry"},
		{name: "policy", trigger: &hooks.Trigger{Type: hooks.TriggerCleanupPolicy, Name: "nightly"}, want: "Cleanup policy nightly"},
		{name: "unknown type with name", trigger: &hooks.Trigger{Type: "other", Name: "x"}, want: "x"},
		{name: "unknown type without name", trigger: &hooks.Trigger{Type: "other"}, want: ""},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, triggeredBy(tt.trigger))
		})
	}
}

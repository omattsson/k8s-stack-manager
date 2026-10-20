package notifier

import (
	"context"
	"encoding/json"
	"testing"

	"backend/internal/hooks"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNotifyInstance_StoresTrigger checks that each in-app notification of
// an instance event stores the trigger of the action (#652), and that the
// channel payload gets the same trigger.
func TestNotifyInstance_StoresTrigger(t *testing.T) {
	t.Parallel()

	userTrigger := hooks.Trigger{Type: hooks.TriggerUser, ID: "devops-1", Name: "dan"}
	policyTrigger := hooks.Trigger{Type: hooks.TriggerCleanupPolicy, ID: "p-1", Name: "nightly"}

	tests := []struct {
		name        string
		ctxTrigger  *hooks.Trigger
		opts        []InstanceOption
		wantRow     models.Trigger
		wantPayload *hooks.Trigger
	}{
		{
			name:        "user trigger option",
			opts:        []InstanceOption{WithTrigger(&userTrigger)},
			wantRow:     models.Trigger{TriggerType: "user", TriggerID: "devops-1", TriggerName: "dan"},
			wantPayload: &userTrigger,
		},
		{
			name:        "user trigger without a name gets the username",
			opts:        []InstanceOption{WithTrigger(&hooks.Trigger{Type: hooks.TriggerUser, ID: "devops-1"})},
			wantRow:     models.Trigger{TriggerType: "user", TriggerID: "devops-1", TriggerName: "dan"},
			wantPayload: &userTrigger,
		},
		{
			name:        "ttl reaper",
			opts:        []InstanceOption{WithTrigger(&hooks.Trigger{Type: hooks.TriggerTTL})},
			wantRow:     models.Trigger{TriggerType: "ttl"},
			wantPayload: &hooks.Trigger{Type: hooks.TriggerTTL},
		},
		{
			name:        "cleanup policy",
			opts:        []InstanceOption{WithTrigger(&policyTrigger)},
			wantRow:     models.Trigger{TriggerType: "cleanup-policy", TriggerID: "p-1", TriggerName: "nightly"},
			wantPayload: &policyTrigger,
		},
		{
			name: "no trigger",
		},
		{
			name: "nil trigger option and no context trigger",
			opts: []InstanceOption{WithTrigger(nil)},
		},
		{
			name:        "context trigger without an option",
			ctxTrigger:  &policyTrigger,
			wantRow:     models.Trigger{TriggerType: "cleanup-policy", TriggerID: "p-1", TriggerName: "nightly"},
			wantPayload: &policyTrigger,
		},
		{
			name:        "context trigger after a nil trigger option",
			ctxTrigger:  &hooks.Trigger{Type: hooks.TriggerUser, ID: "devops-1"},
			opts:        []InstanceOption{WithTrigger(nil)},
			wantRow:     models.Trigger{TriggerType: "user", TriggerID: "devops-1", TriggerName: "dan"},
			wantPayload: &userTrigger,
		},
		{
			name:        "the option wins over the context trigger",
			ctxTrigger:  &policyTrigger,
			opts:        []InstanceOption{WithTrigger(&userTrigger)},
			wantRow:     models.Trigger{TriggerType: "user", TriggerID: "devops-1", TriggerName: "dan"},
			wantPayload: &userTrigger,
		},
		{
			name:       "context trigger without a type is none",
			ctxTrigger: &hooks.Trigger{ID: "x", Name: "y"},
		},
		{
			name:        "unknown user keeps the ID without a name",
			opts:        []InstanceOption{WithTrigger(&hooks.Trigger{Type: hooks.TriggerUser, ID: "gone"})},
			wantRow:     models.Trigger{TriggerType: "user", TriggerID: "gone"},
			wantPayload: &hooks.Trigger{Type: hooks.TriggerUser, ID: "gone"},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			n := newPayloadNotifier("")
			repo := n.repo.(*mockNotificationRepository)
			ctx := context.Background()
			if tt.ctxTrigger != nil {
				ctx = hooks.WithTrigger(ctx, *tt.ctxTrigger)
			}
			target := payloadTarget("owner-1")
			target.FollowerIDs = []string{"follower-1"}

			require.NoError(t, n.NotifyInstance(ctx, target, "deployment.stopped", "Stack stopped", "Stack web has been stopped", tt.opts...))

			rows := repo.Created()
			require.Len(t, rows, 2, "owner and follower")
			for _, row := range rows {
				assert.Equal(t, tt.wantRow, row.Trigger, "row of %s", row.UserID)
				assert.Equal(t, "Stack stopped", row.Title, "the title stays as it is")
				assert.Equal(t, "Stack web has been stopped", row.Message, "the message stays as it is")
			}
			require.Len(t, n.dispatchQueue, 1)
			assert.Equal(t, tt.wantPayload, (<-n.dispatchQueue).Trigger)
		})
	}
}

// TestNotify_NoTrigger checks that Notify and NotifySystem keep rows without
// a trigger (#652 stores it for instance events only).
func TestNotify_NoTrigger(t *testing.T) {
	t.Parallel()
	repo := newMockNotificationRepo()
	users := &mockUserRepository{users: []models.User{{ID: "admin-1", Role: "admin"}}}
	n := NewNotifier(repo, nil, users)
	ctx := hooks.WithTrigger(context.Background(), hooks.Trigger{Type: hooks.TriggerUser, ID: "u1", Name: "alice"})

	require.NoError(t, n.Notify(ctx, "u2", "deployment.success", "T", "M", "stack_instance", "i1"))
	require.NoError(t, n.NotifySystem(ctx, "quota.warning", "T", "M", "cluster", "c1"))

	rows := repo.Created()
	require.Len(t, rows, 2)
	for _, row := range rows {
		assert.Equal(t, models.Trigger{}, row.Trigger)
	}
}

// TestNotification_TriggerJSON checks the JSON field trigger of the API and
// the WebSocket message: {"type","id","name"}, without the empty keys, and
// no field when the notification has no trigger.
func TestNotification_TriggerJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		trigger     models.Trigger
		wantTrigger map[string]any
	}{
		{
			name:        "user",
			trigger:     models.Trigger{TriggerType: "user", TriggerID: "u1", TriggerName: "alice"},
			wantTrigger: map[string]any{"type": "user", "id": "u1", "name": "alice"},
		},
		{
			name:        "cleanup policy",
			trigger:     models.Trigger{TriggerType: "cleanup-policy", TriggerID: "p-1", TriggerName: "nightly"},
			wantTrigger: map[string]any{"type": "cleanup-policy", "id": "p-1", "name": "nightly"},
		},
		{
			name:        "ttl has no id or name",
			trigger:     models.Trigger{TriggerType: "ttl"},
			wantTrigger: map[string]any{"type": "ttl"},
		},
		{
			name: "empty trigger is omitted",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := models.Notification{ID: "n1", UserID: "u2", Type: "deployment.stopped", Title: "T", Trigger: tt.trigger}
			data, err := json.Marshal(in)
			require.NoError(t, err)

			var got map[string]any
			require.NoError(t, json.Unmarshal(data, &got))
			for _, key := range []string{"trigger_type", "trigger_id", "trigger_name"} {
				assert.NotContains(t, got, key, "the columns are not top-level fields")
			}
			if tt.wantTrigger == nil {
				assert.NotContains(t, got, "trigger")
			} else {
				assert.Equal(t, tt.wantTrigger, got["trigger"])
			}
			assert.Equal(t, "deployment.stopped", got["type"], "the notification type stays")

			var back models.Notification
			require.NoError(t, json.Unmarshal(data, &back))
			assert.Equal(t, tt.trigger, back.Trigger, "round trip")
		})
	}
}

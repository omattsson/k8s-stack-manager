package notifier

import (
	"context"
	"errors"
	"testing"

	"backend/internal/hooks"
	"backend/internal/models"
	"backend/internal/notifier/channel"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// usersByID is a user repository that finds users by ID.
type usersByID struct {
	mockUserRepository
	byID map[string]models.User
}

func (u *usersByID) FindByID(_ context.Context, id string) (*models.User, error) {
	user, ok := u.byID[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return &user, nil
}

// newPayloadNotifier returns a notifier whose channel dispatches go to a
// buffered queue (no worker), so the test reads the payloads.
func newPayloadNotifier(publicURL string) *Notifier {
	users := &usersByID{byID: map[string]models.User{
		"owner-1":  {ID: "owner-1", Username: "jane", DisplayName: "Jane Owner"},
		"owner-2":  {ID: "owner-2", Username: "bob"},
		"devops-1": {ID: "devops-1", Username: "dan", DisplayName: "Dan DevOps"},
	}}
	n := NewNotifier(newMockNotificationRepo(), nil, users).WithPublicURL(publicURL)
	n.channelDispatcher = &channel.Dispatcher{}
	n.dispatchQueue = make(chan channel.EventPayload, 10)
	return n
}

func payloadTarget(ownerID string) models.NotificationTarget {
	return models.NotificationTarget{
		InstanceID: "inst-1", InstanceName: "web", OwnerID: ownerID, ClusterID: "c-1",
		Namespace: "stack-web-jane", Branch: "feature/x", FollowerIDs: []string{},
	}
}

// TestNotifyInstance_ChannelPayload checks the new payload fields for the
// triggers of the callers in issue #642: the deployer (user), the TTL
// reaper, the cleanup scheduler, the interrupt recovery and the warner (no
// trigger), and the handler create and delete (user).
func TestNotifyInstance_ChannelPayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		ownerID         string
		event           string
		publicURL       string
		opts            []InstanceOption
		wantTrigger     *hooks.Trigger
		wantOwner       string
		wantUserDisplay string
		wantSiteURL     string
		wantInstanceURL string
	}{
		{
			name: "deployer deploy by a devops user on another user's stack", ownerID: "owner-1", event: "deployment.success",
			publicURL: "https://stacks.example.com/",
			opts: []InstanceOption{
				WithTrigger(&hooks.Trigger{Type: hooks.TriggerUser, ID: "devops-1", Name: "dan"}),
				WithSiteURL("https://web.example.com"),
			},
			wantTrigger: &hooks.Trigger{Type: hooks.TriggerUser, ID: "devops-1", Name: "dan"},
			wantOwner:   "Jane Owner", wantUserDisplay: "Jane Owner",
			wantSiteURL:     "https://web.example.com",
			wantInstanceURL: "https://stacks.example.com/stack-instances/inst-1",
		},
		{
			name: "user trigger without a name gets the username", ownerID: "owner-1", event: "deployment.stopped",
			opts:        []InstanceOption{WithTrigger(&hooks.Trigger{Type: hooks.TriggerUser, ID: "devops-1"})},
			wantTrigger: &hooks.Trigger{Type: hooks.TriggerUser, ID: "devops-1", Name: "dan"},
			wantOwner:   "Jane Owner", wantUserDisplay: "Jane Owner",
		},
		{
			name: "ttl reaper", ownerID: "owner-1", event: "stack.expired", publicURL: "https://stacks.example.com",
			opts:        []InstanceOption{WithTrigger(&hooks.Trigger{Type: hooks.TriggerTTL})},
			wantTrigger: &hooks.Trigger{Type: hooks.TriggerTTL},
			wantOwner:   "Jane Owner", wantUserDisplay: "Jane Owner",
			wantInstanceURL: "https://stacks.example.com/stack-instances/inst-1",
		},
		{
			name: "cleanup scheduler", ownerID: "owner-1", event: "cleanup.policy.stop",
			opts:        []InstanceOption{WithTrigger(&hooks.Trigger{Type: hooks.TriggerCleanupPolicy, ID: "p-1", Name: "nightly"})},
			wantTrigger: &hooks.Trigger{Type: hooks.TriggerCleanupPolicy, ID: "p-1", Name: "nightly"},
			wantOwner:   "Jane Owner", wantUserDisplay: "Jane Owner",
		},
		{
			name: "interrupt recovery and warner pass no trigger", ownerID: "owner-1", event: "deployment.error",
			wantOwner: "Jane Owner", wantUserDisplay: "Jane Owner",
		},
		{
			name: "nil trigger option sets none", ownerID: "owner-1", event: "stack.expiring",
			opts:      []InstanceOption{WithTrigger(nil), nil},
			wantOwner: "Jane Owner", wantUserDisplay: "Jane Owner",
		},
		{
			name: "owner without display name: owner_display_name is the username", ownerID: "owner-2", event: "instance.created",
			publicURL:       "https://stacks.example.com",
			opts:            []InstanceOption{WithTrigger(&hooks.Trigger{Type: hooks.TriggerUser, ID: "owner-2", Name: "bob"})},
			wantTrigger:     &hooks.Trigger{Type: hooks.TriggerUser, ID: "owner-2", Name: "bob"},
			wantOwner:       "bob",
			wantInstanceURL: "https://stacks.example.com/stack-instances/inst-1",
		},
		{
			name: "instance.deleted has no instance_url", ownerID: "owner-1", event: "instance.deleted",
			publicURL:   "https://stacks.example.com",
			opts:        []InstanceOption{WithTrigger(&hooks.Trigger{Type: hooks.TriggerUser, ID: "devops-1", Name: "dan"})},
			wantTrigger: &hooks.Trigger{Type: hooks.TriggerUser, ID: "devops-1", Name: "dan"},
			wantOwner:   "Jane Owner", wantUserDisplay: "Jane Owner",
		},
		{
			name: "unknown owner", ownerID: "gone", event: "deployment.success",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			n := newPayloadNotifier(tt.publicURL)
			require.NoError(t, n.NotifyInstance(context.Background(), payloadTarget(tt.ownerID), tt.event, "T", "M", tt.opts...))
			require.Len(t, n.dispatchQueue, 1)
			p := <-n.dispatchQueue

			assert.Equal(t, tt.event, p.EventType)
			assert.Equal(t, tt.wantTrigger, p.Trigger)
			assert.Equal(t, tt.wantOwner, p.OwnerDisplayName)
			assert.Equal(t, tt.wantUserDisplay, p.UserDisplayName, "user_display_name keeps the owner's display name")
			assert.Equal(t, "stack-web-jane", p.Namespace)
			assert.Equal(t, "feature/x", p.Branch)
			assert.Equal(t, "c-1", p.ClusterID)
			assert.Empty(t, p.ClusterName, "the dispatch worker sets the cluster name")
			assert.Equal(t, tt.wantSiteURL, p.SiteURL)
			assert.Equal(t, tt.wantInstanceURL, p.InstanceURL)
		})
	}
}

func TestNotifier_WithClusterName(t *testing.T) {
	t.Parallel()

	resolver := hooks.NewClusterNameResolver(func(_ context.Context, ids []string) (map[string]string, error) {
		return map[string]string{"c-1": "prod"}, nil
	})
	t.Cleanup(resolver.Stop)

	tests := []struct {
		name     string
		resolver *hooks.ClusterNameResolver
		payload  channel.EventPayload
		want     string
	}{
		{name: "resolved", resolver: resolver, payload: channel.EventPayload{ClusterID: "c-1"}, want: "prod"},
		{name: "unknown cluster", resolver: resolver, payload: channel.EventPayload{ClusterID: "c-x"}, want: ""},
		{name: "default cluster (no ID)", resolver: resolver, payload: channel.EventPayload{}, want: ""},
		{name: "set name kept", resolver: resolver, payload: channel.EventPayload{ClusterID: "c-1", ClusterName: "given"}, want: "given"},
		{name: "no resolver", payload: channel.EventPayload{ClusterID: "c-1"}, want: ""},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			n := NewNotifier(newMockNotificationRepo(), nil, nil).WithClusterNames(tt.resolver)
			got := n.withClusterName(context.Background(), tt.payload)
			assert.Equal(t, tt.want, got.ClusterName)
		})
	}
}

func TestResolveInstanceOptions(t *testing.T) {
	t.Parallel()

	in := &hooks.Trigger{Type: hooks.TriggerUser, ID: "u", Name: "n"}
	o := ResolveInstanceOptions(WithTrigger(in), WithSiteURL("https://x.example.com"))
	require.NotNil(t, o.Trigger)
	assert.Equal(t, *in, *o.Trigger)
	in.Name = "changed"
	assert.Equal(t, "n", o.Trigger.Name, "the option copies the trigger")
	assert.Equal(t, "https://x.example.com", o.SiteURL)
	assert.Equal(t, InstanceOptions{}, ResolveInstanceOptions())
}

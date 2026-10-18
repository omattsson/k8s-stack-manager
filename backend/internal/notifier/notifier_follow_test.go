package notifier

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"backend/internal/models"
	"backend/internal/notifier/channel"
	"backend/internal/websocket"

	gorilla "github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockFollowerRepo is an InstanceFollowerRepository with a fixed follower
// list per instance.
type mockFollowerRepo struct {
	mu        sync.Mutex
	followers map[string][]string
	listErr   error
	listCalls int
}

func (m *mockFollowerRepo) Follow(context.Context, string, string) error   { return nil }
func (m *mockFollowerRepo) Unfollow(context.Context, string, string) error { return nil }
func (m *mockFollowerRepo) FollowState(context.Context, string, string) (bool, int64, error) {
	return false, 0, nil
}
func (m *mockFollowerRepo) DeleteByInstance(context.Context, string) error { return nil }
func (m *mockFollowerRepo) DeleteByUser(context.Context, string) error     { return nil }
func (m *mockFollowerRepo) ListUserIDsByInstance(_ context.Context, instanceID string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listCalls++
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.followers[instanceID], nil
}

func receivers(repo *mockNotificationRepository) []string {
	var ids []string
	for _, n := range repo.Created() {
		ids = append(ids, n.UserID)
	}
	sort.Strings(ids)
	return ids
}

func TestNotifyInstance_Receivers(t *testing.T) {
	t.Parallel()

	base := models.NotificationTarget{InstanceID: "i1", InstanceName: "shared", OwnerID: "owner"}

	tests := []struct {
		name          string
		followers     map[string][]string
		listErr       error
		noFollowRepo  bool
		captured      []string // NotificationTarget.FollowerIDs
		disabled      map[string]map[string]bool
		disabledErr   error
		wantReceivers []string
		wantListCalls int
	}{
		{name: "owner only without followers", followers: map[string][]string{},
			wantReceivers: []string{"owner"}, wantListCalls: 1},
		{name: "owner and followers", followers: map[string][]string{"i1": {"f1", "f2"}},
			wantReceivers: []string{"f1", "f2", "owner"}, wantListCalls: 1},
		{name: "owner who also follows gets one notification", followers: map[string][]string{"i1": {"owner", "f1"}},
			wantReceivers: []string{"f1", "owner"}, wantListCalls: 1},
		{name: "follower who ran the operation is notified (no actor exclusion, as for the owner)",
			followers: map[string][]string{"i1": {"actor"}}, wantReceivers: []string{"actor", "owner"}, wantListCalls: 1},
		{name: "follower with the event type switched off gets nothing", followers: map[string][]string{"i1": {"f1", "f2"}},
			disabled:      map[string]map[string]bool{"deployment.success": {"f1": true}},
			wantReceivers: []string{"f2", "owner"}, wantListCalls: 1},
		{name: "owner with the event type switched off gets nothing", followers: map[string][]string{"i1": {"f1"}},
			disabled:      map[string]map[string]bool{"deployment.success": {"owner": true}},
			wantReceivers: []string{"f1"}, wantListCalls: 1},
		{name: "a preference for another event type does not matter", followers: map[string][]string{"i1": {"f1"}},
			disabled:      map[string]map[string]bool{"deployment.error": {"f1": true}},
			wantReceivers: []string{"f1", "owner"}, wantListCalls: 1},
		{name: "preference lookup error notifies all (fail open)", followers: map[string][]string{"i1": {"f1"}},
			disabledErr: errors.New("db down"), wantReceivers: []string{"f1", "owner"}, wantListCalls: 1},
		{name: "follower lookup error notifies the owner", listErr: errors.New("db down"),
			wantReceivers: []string{"owner"}, wantListCalls: 1},
		{name: "captured followers (instance delete) are used without a lookup",
			followers: map[string][]string{}, captured: []string{"f9"},
			wantReceivers: []string{"f9", "owner"}, wantListCalls: 0},
		{name: "captured empty list means no followers", followers: map[string][]string{"i1": {"f1"}}, captured: []string{},
			wantReceivers: []string{"owner"}, wantListCalls: 0},
		{name: "without a follower repository only the owner", noFollowRepo: true,
			wantReceivers: []string{"owner"}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := newMockNotificationRepo()
			repo.disabled = tt.disabled
			repo.disabledErr = tt.disabledErr
			followers := &mockFollowerRepo{followers: tt.followers, listErr: tt.listErr}
			n := NewNotifier(repo, nil, nil)
			if !tt.noFollowRepo {
				n.WithFollowers(followers)
			}

			target := base
			target.FollowerIDs = tt.captured
			require.NoError(t, n.NotifyInstance(context.Background(), target, "deployment.success", "Deployed", "done"))

			assert.Equal(t, tt.wantReceivers, receivers(repo))
			for _, created := range repo.Created() {
				assert.Equal(t, "stack_instance", created.EntityType)
				assert.Equal(t, "i1", created.EntityID)
				assert.Equal(t, "deployment.success", created.Type)
			}
			assert.Equal(t, tt.wantListCalls, followers.listCalls)
		})
	}
}

func TestNotifyInstance_OwnerInsertErrorIsReturned(t *testing.T) {
	t.Parallel()
	repo := newMockNotificationRepo()
	repo.SetCreateError(errors.New("insert failed"))
	n := NewNotifier(repo, nil, nil)
	err := n.NotifyInstance(context.Background(), models.NotificationTarget{InstanceID: "i1", OwnerID: "owner", FollowerIDs: []string{}},
		"deployment.success", "t", "m")
	assert.Error(t, err)
}

// TestNotifyInstance_ChannelDispatchOnce checks that the event goes once to
// the channel queue, with the instance for the channel filters, also when
// some receivers switched the event type off.
func TestNotifyInstance_ChannelDispatchOnce(t *testing.T) {
	t.Parallel()
	repo := newMockNotificationRepo()
	repo.disabled = map[string]map[string]bool{"deployment.success": {"owner": true}}
	n := NewNotifier(repo, nil, &mockUserRepository{})
	n.channelDispatcher = &channel.Dispatcher{}
	n.dispatchQueue = make(chan channel.EventPayload, 10)

	target := models.NotificationTarget{InstanceID: "i1", InstanceName: "team-a-1", OwnerID: "owner", ClusterID: "c1",
		FollowerIDs: []string{"f1", "f2"}}
	require.NoError(t, n.NotifyInstance(context.Background(), target, "deployment.success", "Deployed", "done"))

	require.Len(t, n.dispatchQueue, 1, "one channel dispatch per event, not per receiver")
	payload := <-n.dispatchQueue
	assert.Equal(t, "deployment.success", payload.EventType)
	assert.Equal(t, "stack_instance", payload.EntityType)
	assert.Equal(t, "i1", payload.EntityID)
	require.Len(t, payload.Instances, 1)
	assert.Equal(t, "team-a-1", payload.Instances[0].InstanceName)
	assert.Equal(t, "c1", payload.Instances[0].ClusterID)
}

func TestNotifySystemForInstances_PassesInstances(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		instances []models.NotificationTarget
	}{
		{name: "policy run with affected instances", instances: []models.NotificationTarget{{InstanceID: "i1"}, {InstanceID: "i2"}}},
		{name: "system event without instances"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			n := NewNotifier(newMockNotificationRepo(), nil, &mockUserRepository{users: []models.User{{ID: "admin-1", Role: "admin"}}})
			n.channelDispatcher = &channel.Dispatcher{}
			n.dispatchQueue = make(chan channel.EventPayload, 10)

			require.NoError(t, n.NotifySystemForInstances(context.Background(), "cleanup.policy.executed", "t", "m", "cleanup_policy", "p1", tt.instances))
			require.Len(t, n.dispatchQueue, 1)
			payload := <-n.dispatchQueue
			assert.Equal(t, tt.instances, payload.Instances)
		})
	}
}

// TestNotifyInstance_WebSocketToOwnerAndFollowers checks that each receiver
// gets notification.new on the own sockets only (BroadcastToUser per
// receiver), and other users get nothing.
func TestNotifyInstance_WebSocketToOwnerAndFollowers(t *testing.T) {
	t.Parallel()

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(hub.Shutdown)

	upgrader := gorilla.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		identity := websocket.ClientIdentity{UserID: r.URL.Query().Get("user")}
		if _, err := websocket.NewClientWithIdentity(hub, conn, identity); err != nil {
			_ = conn.Close()
		}
	}))
	t.Cleanup(srv.Close)

	owner := dialUser(t, srv, hub, "owner")
	follower := dialUser(t, srv, hub, "follower")
	other := dialUser(t, srv, hub, "other")
	require.Eventually(t, func() bool { return hub.ClientCount() == 3 }, 2*time.Second, 5*time.Millisecond)

	n := NewNotifier(newMockNotificationRepo(), hub, nil).
		WithFollowers(&mockFollowerRepo{followers: map[string][]string{"i1": {"follower"}}})
	require.NoError(t, n.NotifyInstance(context.Background(),
		models.NotificationTarget{InstanceID: "i1", OwnerID: "owner"}, "deployment.success", "Deployed", "done"))

	gotOwner := readMessage(owner, 2*time.Second)
	assert.Contains(t, gotOwner, `"user_id":"owner"`)
	gotFollower := readMessage(follower, 2*time.Second)
	assert.Contains(t, gotFollower, `"type":"notification.new"`)
	assert.Contains(t, gotFollower, `"user_id":"follower"`)
	assert.Empty(t, readMessage(other, 300*time.Millisecond), "a user who does not follow gets nothing")
}

func TestNotifySystem_AppliesPreferences(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		disabled    map[string]map[string]bool
		disabledErr error
		want        []string
	}{
		{name: "all admins and devops users get it", want: []string{"admin-1", "devops-1"}},
		{name: "a user who switched the event off gets nothing",
			disabled: map[string]map[string]bool{"cleanup.policy.executed": {"devops-1": true}}, want: []string{"admin-1"}},
		{name: "another event type does not matter",
			disabled: map[string]map[string]bool{"quota.warning": {"devops-1": true}}, want: []string{"admin-1", "devops-1"}},
		{name: "preference lookup error notifies all (fail open)", disabledErr: errors.New("db down"),
			want: []string{"admin-1", "devops-1"}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := newMockNotificationRepo()
			repo.disabled = tt.disabled
			repo.disabledErr = tt.disabledErr
			n := NewNotifier(repo, nil, &mockUserRepository{users: []models.User{
				{ID: "admin-1", Role: "admin"}, {ID: "devops-1", Role: "devops"},
			}})
			require.NoError(t, n.NotifySystemForInstances(context.Background(), "cleanup.policy.executed", "t", "m", "cleanup_policy", "p1", nil))
			assert.Equal(t, tt.want, receivers(repo))
		})
	}
}

package notifier

import (
	"context"
	"errors"
	"sync"
	"testing"

	"backend/internal/hooks"
	"backend/internal/models"
	"backend/internal/notifier/channel"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type notifyCtxKey struct{}

// ctxUsers records the method, the context marker and the context error of
// each user lookup.
type ctxUsers struct {
	usersByID
	mu   sync.Mutex
	seen []string
	errs []error
}

func (u *ctxUsers) record(method string, ctx context.Context) {
	marker, _ := ctx.Value(notifyCtxKey{}).(string)
	u.mu.Lock()
	defer u.mu.Unlock()
	u.seen = append(u.seen, method+"="+marker)
	u.errs = append(u.errs, ctx.Err())
}

func (u *ctxUsers) FindByID(ctx context.Context, id string) (*models.User, error) {
	u.record("FindByID", ctx)
	return u.usersByID.FindByID(ctx, id)
}

func (u *ctxUsers) ListByRoles(ctx context.Context, _ []string) ([]models.User, error) {
	u.record("ListByRoles", ctx)
	return []models.User{{ID: "devops-1", Role: models.RoleDevOps}}, nil
}

func (u *ctxUsers) calls() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.seen...)
}

func (u *ctxUsers) ctxErrs() []error {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]error(nil), u.errs...)
}

// TestNotifier_UserLookupsUseCallerContext checks that the user lookups of
// the notifier (owner and trigger names, display name, admin list) run with
// the context of the caller, not a new background context (#656). With a
// cancelled caller context the lookups see the cancel, so a change to
// context.WithoutCancel or context.Background fails the test.
func TestNotifier_UserLookupsUseCallerContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		notify    func(ctx context.Context, n *Notifier) error
		name      string
		wantCalls []string
	}{
		{
			name: "NotifyInstance: trigger name and owner",
			notify: func(ctx context.Context, n *Notifier) error {
				return n.NotifyInstance(ctx, models.NotificationTarget{InstanceID: "inst-1", OwnerID: "owner-1", FollowerIDs: []string{}},
					"deployment.success", "Deployed", "msg",
					WithTrigger(&hooks.Trigger{Type: hooks.TriggerUser, ID: "devops-1"}))
			},
			wantCalls: []string{"FindByID=caller", "FindByID=caller"},
		},
		{
			name: "Notify: display name",
			notify: func(ctx context.Context, n *Notifier) error {
				return n.Notify(ctx, "owner-1", "deployment.success", "Deployed", "msg", "stack_instance", "inst-1")
			},
			wantCalls: []string{"FindByID=caller"},
		},
		{
			name: "NotifySystem: admin and devops list",
			notify: func(ctx context.Context, n *Notifier) error {
				return n.NotifySystem(ctx, "quota.warning", "Quota", "msg", "cluster", "c-1")
			},
			wantCalls: []string{"ListByRoles=caller"},
		},
	}

	for _, tt := range tests {
		for _, cancelled := range []bool{false, true} {
			tt, cancelled := tt, cancelled
			name := tt.name
			if cancelled {
				name += ", cancelled caller"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				users := &ctxUsers{usersByID: usersByID{byID: map[string]models.User{
					"owner-1":  {ID: "owner-1", Username: "jane"},
					"devops-1": {ID: "devops-1", Username: "dan"},
				}}}
				n := NewNotifier(newMockNotificationRepo(), nil, users)
				n.channelDispatcher = &channel.Dispatcher{}
				n.dispatchQueue = make(chan channel.EventPayload, 10)

				ctx, cancel := context.WithCancel(context.WithValue(context.Background(), notifyCtxKey{}, "caller"))
				defer cancel()
				var wantErr error
				if cancelled {
					cancel()
					wantErr = context.Canceled
				}
				require.NoError(t, tt.notify(ctx, n))

				assert.Equal(t, tt.wantCalls, users.calls())
				errs := users.ctxErrs()
				require.Len(t, errs, len(tt.wantCalls))
				for _, err := range errs {
					assert.Equal(t, wantErr, err, "the lookup gets the caller context")
				}
			})
		}
	}
}

// TestNotifySystemForInstances_AdminListError checks that a failed admin and
// devops list still sends the event to the channels once, and that the
// error is returned.
func TestNotifySystemForInstances_AdminListError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		listErr     error
		name        string
		wantCreated int
	}{
		{name: "list works", wantCreated: 1},
		{name: "list fails", listErr: errors.New("db down")},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			users := &mockUserRepository{
				users:          []models.User{{ID: "admin-1", Role: models.RoleAdmin}},
				listByRolesErr: tt.listErr,
			}
			repo := newMockNotificationRepo()
			n := NewNotifier(repo, nil, users)
			n.channelDispatcher = &channel.Dispatcher{}
			n.dispatchQueue = make(chan channel.EventPayload, 10)
			instances := []models.NotificationTarget{{InstanceID: "inst-1", OwnerID: "owner-1"}}

			err := n.NotifySystemForInstances(context.Background(), "cleanup.policy.executed", "Cleanup", "msg",
				"cleanup_policy", "p-1", instances)

			if tt.listErr != nil {
				assert.ErrorIs(t, err, tt.listErr)
			} else {
				assert.NoError(t, err)
			}
			assert.Len(t, repo.Created(), tt.wantCreated)
			require.Len(t, n.dispatchQueue, 1, "the channels get the event once")
			p := <-n.dispatchQueue
			assert.Equal(t, "cleanup.policy.executed", p.EventType)
			assert.Equal(t, "System", p.UserDisplayName)
			assert.Equal(t, instances, p.Instances)
		})
	}
}

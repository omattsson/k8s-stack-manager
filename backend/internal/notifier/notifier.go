// Package notifier provides a service for creating user notifications
// and optionally broadcasting them via WebSocket and external channels.
package notifier

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"backend/internal/hooks"
	"backend/internal/models"
	"backend/internal/notifier/channel"
	"backend/internal/websocket"

	"github.com/google/uuid"
)

// dispatchQueueSize is the intake queue of all channel events. Its worker
// only finds and queues per channel, so it stays short (#649).
const dispatchQueueSize = 1000

// stopDrainTimeout is the longest time that Stop lets the queued channel
// events go out before it cancels the deliveries (#649).
const stopDrainTimeout = 2 * time.Second

// Notifier creates notification records and broadcasts them in real time.
type Notifier struct {
	repo              models.NotificationRepository
	hub               *websocket.Hub
	userRepo          models.UserRepository
	followerRepo      models.InstanceFollowerRepository
	channelDispatcher *channel.Dispatcher
	clusterNames      *hooks.ClusterNameResolver
	dispatchQueue     chan channel.EventPayload
	done              chan struct{}
	workerExited      chan struct{}
	cancelDispatch    context.CancelFunc
	// stopDrain: 0 means stopDrainTimeout; tests change it.
	stopDrain time.Duration
	// queueMu: enqueueDispatch sends under RLock; Stop closes done under
	// Lock, so no event enters the queue after Stop has started.
	queueMu sync.RWMutex
	// publicURL is APP_PUBLIC_URL without a trailing slash; empty: no
	// instance_url in the channel payload.
	publicURL string
	initOnce  sync.Once
	stopOnce  sync.Once
}

// NewNotifier creates a new Notifier. hub and userRepo may be nil if real-time
// broadcasting or system-wide notifications are not needed.
func NewNotifier(repo models.NotificationRepository, hub *websocket.Hub, userRepo models.UserRepository) *Notifier {
	return &Notifier{
		repo:     repo,
		hub:      hub,
		userRepo: userRepo,
	}
}

// WithFollowers sets the instance follower repository. NotifyInstance then
// also notifies the followers of the instance. Without it only the owner
// gets instance notifications. Returns n for chaining.
func (n *Notifier) WithFollowers(repo models.InstanceFollowerRepository) *Notifier {
	n.followerRepo = repo
	return n
}

// WithClusterNames sets the resolver of cluster_name in the channel payload
// of an instance event. The dispatch worker resolves the name, so the caller
// never waits for the lookup. Without it the payload has no cluster_name.
// Call it before the first event. Returns n for chaining.
func (n *Notifier) WithClusterNames(r *hooks.ClusterNameResolver) *Notifier {
	n.clusterNames = r
	return n
}

// WithPublicURL sets the public base URL of the web UI (APP_PUBLIC_URL,
// validated by config.ParsePublicURL). The channel payload of an instance
// event then has instance_url = <publicURL>/stack-instances/<id> (not for
// instance.deleted). Call it before the first event. Returns n for chaining.
func (n *Notifier) WithPublicURL(publicURL string) *Notifier {
	n.publicURL = strings.TrimRight(publicURL, "/")
	return n
}

// WithChannelDispatcher sets the external channel dispatcher for webhook
// delivery and starts the intake worker. The intake worker finds the
// channels of each event and queues it to the worker of each channel, so a
// slow channel does not delay the others. Safe to call once; subsequent
// calls are no-ops.
func (n *Notifier) WithChannelDispatcher(d *channel.Dispatcher) *Notifier {
	if d == nil {
		return n
	}
	n.initOnce.Do(func() {
		n.channelDispatcher = d
		n.dispatchQueue = make(chan channel.EventPayload, dispatchQueueSize)
		n.done = make(chan struct{})
		n.workerExited = make(chan struct{})
		// Stop cancels ctx, so a channel or cluster name lookup does not
		// hold up the shutdown.
		ctx, cancel := context.WithCancel(context.Background())
		n.cancelDispatch = cancel
		go n.dispatchWorker(ctx)
	})
	return n
}

// dispatchWorker passes each queued event to the dispatcher. After done it
// passes the rest of the queue (Stop accepts no new event), then returns. It
// returns at once when ctx ends.
func (n *Notifier) dispatchWorker(ctx context.Context) {
	defer close(n.workerExited)
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return
		case payload := <-n.dispatchQueue:
			n.channelDispatcher.Dispatch(ctx, n.withClusterName(ctx, payload))
		case <-n.done:
			for ctx.Err() == nil {
				select {
				case payload := <-n.dispatchQueue:
					n.channelDispatcher.Dispatch(ctx, n.withClusterName(ctx, payload))
				default:
					return
				}
			}
			return
		}
	}
}

// withClusterName returns payload with cluster_name from the resolver. It
// runs in the dispatch worker, so the caller of NotifyInstance never waits
// for the lookup. Without a resolver, without a cluster ID or when the name
// is set already, payload is returned as it is.
func (n *Notifier) withClusterName(ctx context.Context, payload channel.EventPayload) channel.EventPayload {
	if payload.ClusterName == "" && payload.ClusterID != "" {
		payload.ClusterName = n.clusterNames.NameFor(ctx, payload.ClusterID)
	}
	return payload
}

// Stop shuts down the intake worker and the channel workers. It accepts no
// new event. For at most stopDrainTimeout (2 s) the queued events still go
// out, so the last events of a shutdown reach healthy channels. Then it
// cancels the lookups and deliveries in progress, counts the rest as dropped
// (reason shutdown) and waits until all workers have returned. Safe to call
// multiple times and when no dispatcher is configured.
func (n *Notifier) Stop() {
	n.stopOnce.Do(func() {
		if n.done == nil {
			return
		}
		n.queueMu.Lock()
		close(n.done)
		n.queueMu.Unlock()

		drain := n.stopDrain
		if drain <= 0 {
			drain = stopDrainTimeout
		}
		deadline := time.Now().Add(drain)
		timer := time.NewTimer(drain)
		select {
		case <-n.workerExited:
		case <-timer.C:
		}
		timer.Stop()

		n.cancelDispatch()
		<-n.workerExited
		n.discardQueued()
		n.channelDispatcher.Shutdown(time.Until(deadline))
	})
}

// discardQueued counts the events left in the intake queue after the intake
// worker has returned.
func (n *Notifier) discardQueued() {
	for {
		select {
		case payload := <-n.dispatchQueue:
			n.channelDispatcher.RecordIntakeDrop(payload.EventType, channel.DropReasonShutdown)
		default:
			return
		}
	}
}

// enqueueDispatch queues payload for the intake worker. It never blocks: a
// full queue, or a stopped notifier, drops the event. The dispatcher counts
// the drop and logs at most one warning per minute and reason (#649).
func (n *Notifier) enqueueDispatch(payload channel.EventPayload) {
	if n.dispatchQueue == nil {
		return
	}
	n.queueMu.RLock()
	defer n.queueMu.RUnlock()
	select {
	case <-n.done:
		n.channelDispatcher.RecordIntakeDrop(payload.EventType, channel.DropReasonShutdown)
		return
	default:
	}
	select {
	case n.dispatchQueue <- payload:
	default:
		n.channelDispatcher.RecordIntakeDrop(payload.EventType, channel.DropReasonQueueFull)
	}
}

// Notify creates a notification for the given user and optionally broadcasts it
// via WebSocket. It returns an error only if the database insert fails.
// The channel dispatch has no instance data, so channels with filters skip
// it: use NotifyInstance for stack instance events.
func (n *Notifier) Notify(ctx context.Context, userID, notifType, title, message, entityType, entityID string) error {
	return n.notify(ctx, userID, notifType, title, message, entityType, entityID, true)
}

func (n *Notifier) notify(ctx context.Context, userID, notifType, title, message, entityType, entityID string, dispatchExternal bool) error {
	return n.notifyWithTrigger(ctx, userID, notifType, title, message, entityType, entityID, models.Trigger{}, dispatchExternal)
}

// notifyWithTrigger is notify that also stores the trigger of the action (#652).
func (n *Notifier) notifyWithTrigger(ctx context.Context, userID, notifType, title, message, entityType, entityID string, trigger models.Trigger, dispatchExternal bool) error {
	notification := &models.Notification{
		ID:         uuid.New().String(),
		UserID:     userID,
		Type:       notifType,
		Trigger:    trigger,
		Title:      title,
		Message:    message,
		EntityType: entityType,
		EntityID:   entityID,
		IsRead:     false,
		CreatedAt:  time.Now().UTC(),
	}

	if err := n.repo.Create(ctx, notification); err != nil {
		return err
	}

	// Send via WebSocket to the clients of the user only (with fan-out, also
	// on the other replicas).
	if n.hub != nil {
		msg, err := websocket.NewMessage(MessageTypeNotificationNew, notification)
		if err != nil {
			slog.Error("Failed to create WebSocket notification message", "error", err)
			return nil // DB write succeeded; WS failure is non-fatal
		}
		data, err := json.Marshal(msg)
		if err != nil {
			slog.Error("Failed to marshal WebSocket notification message", "error", err)
			return nil
		}
		n.hub.BroadcastToUser(userID, data)
	}

	// Dispatch to external channels (Teams, Slack, etc.) in background.
	if dispatchExternal && n.channelDispatcher != nil {
		n.enqueueDispatch(channel.EventPayload{
			EventType:       notifType,
			Timestamp:       notification.CreatedAt,
			Title:           title,
			Message:         message,
			UserDisplayName: n.displayName(ctx, userID),
			EntityType:      entityType,
			EntityID:        entityID,
		})
	}

	return nil
}

// EntityTypeStackInstance is the entity type of stack instance notifications.
const EntityTypeStackInstance = "stack_instance"

// FollowerIDs returns the user IDs that follow the instance. It returns nil
// when no follower repository is set or the lookup fails (the error is
// logged). Call it before an instance delete and put the result in
// NotificationTarget.FollowerIDs: the delete removes the follower rows.
func (n *Notifier) FollowerIDs(ctx context.Context, instanceID string) []string {
	if n.followerRepo == nil || instanceID == "" {
		return nil
	}
	ids, err := n.followerRepo.ListUserIDsByInstance(ctx, instanceID)
	if err != nil {
		slog.Error("failed to list instance followers", "instance_id", instanceID, "error", err)
		return nil
	}
	if ids == nil {
		ids = []string{}
	}
	return ids
}

// InstanceOptions is the data of an instance event that only the caller
// knows. NotifyInstance puts it into the channel payload; the in-app
// notification uses only the trigger (#652).
type InstanceOptions struct {
	// Trigger is what started the operation (payload field trigger). Nil:
	// no trigger.
	Trigger *hooks.Trigger
	// SiteURL is the primary URL of the stack (payload field site_url).
	SiteURL string
}

// InstanceOption sets one field of InstanceOptions.
type InstanceOption func(*InstanceOptions)

// WithTrigger sets the trigger of the event. A nil trigger sets none.
func WithTrigger(t *hooks.Trigger) InstanceOption {
	return func(o *InstanceOptions) {
		if t == nil {
			o.Trigger = nil
			return
		}
		cp := *t
		o.Trigger = &cp
	}
}

// WithSiteURL sets the primary URL of the stack (deployment.success,
// deployment.partial and rollback.completed only). Empty sets none.
func WithSiteURL(siteURL string) InstanceOption {
	return func(o *InstanceOptions) { o.SiteURL = siteURL }
}

// ResolveInstanceOptions applies opts in order and returns the result. Test
// doubles of NotifyInstance use it to read the options.
func ResolveInstanceOptions(opts ...InstanceOption) InstanceOptions {
	var o InstanceOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	return o
}

// NotifyInstance creates the in-app notification of an instance event for
// the owner and for each follower of the instance, and dispatches the event
// once to the external channels whose filters match the instance.
//
// Receivers: the owner and the followers, without duplicates (an owner who
// also follows gets one notification). A receiver who switched off the
// event type in the notification preferences gets no in-app notification.
// The user who started the operation (the actor) is not excluded: as for the
// owner before followers existed, an actor who is the owner or a follower
// gets the notification of the own operation.
//
// The channel payload also has the owner, the namespace, the branch, the
// cluster (ID, and the name when a resolver is set), the instance URL (with
// a public URL, not for instance.deleted) and the data of opts (trigger,
// site URL).
//
// The trigger is the WithTrigger option, else the trigger of ctx
// (hooks.WithTrigger). Each in-app notification and the payload get it.
//
// It returns the error of the owner's database insert; errors for followers
// are logged.
func (n *Notifier) NotifyInstance(ctx context.Context, target models.NotificationTarget, notifType, title, message string, opts ...InstanceOption) error {
	o := ResolveInstanceOptions(opts...)
	o.Trigger = n.instanceTrigger(ctx, o.Trigger)
	trigger := rowTrigger(o.Trigger)

	followers := target.FollowerIDs
	if followers == nil {
		followers = n.FollowerIDs(ctx, target.InstanceID)
	}
	receivers := make([]string, 0, 1+len(followers))
	seen := make(map[string]bool, 1+len(followers))
	for _, id := range append([]string{target.OwnerID}, followers...) {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		receivers = append(receivers, id)
	}

	disabled := n.disabledReceivers(ctx, notifType, receivers)
	var ownerErr error
	for _, userID := range receivers {
		if disabled[userID] {
			continue
		}
		if err := n.notifyWithTrigger(ctx, userID, notifType, title, message, EntityTypeStackInstance, target.InstanceID, trigger, false); err != nil {
			if userID == target.OwnerID {
				ownerErr = err
			}
			slog.Error("failed to create instance notification",
				"user_id", userID, "instance_id", target.InstanceID, "type", notifType, "error", err)
		}
	}

	if n.channelDispatcher != nil {
		n.enqueueDispatch(n.instancePayload(ctx, target, notifType, title, message, o))
	}
	return ownerErr
}

// instanceTrigger returns opt, else the trigger of ctx, with the username of
// a user trigger (resolveTrigger). It returns nil without a trigger.
func (n *Notifier) instanceTrigger(ctx context.Context, opt *hooks.Trigger) *hooks.Trigger {
	if opt == nil || opt.Type == "" {
		if t, ok := hooks.TriggerFromContext(ctx); ok {
			opt = &t
		}
	}
	return n.resolveTrigger(ctx, opt)
}

// rowTrigger returns the trigger columns of a notification row for t.
func rowTrigger(t *hooks.Trigger) models.Trigger {
	if t == nil {
		return models.Trigger{}
	}
	return models.Trigger{TriggerType: t.Type, TriggerID: t.ID, TriggerName: t.Name}
}

// instancePayload builds the channel payload of an instance event.
func (n *Notifier) instancePayload(ctx context.Context, target models.NotificationTarget, notifType, title, message string, o InstanceOptions) channel.EventPayload {
	owner := n.findUser(ctx, target.OwnerID)
	payload := channel.EventPayload{
		EventType:  notifType,
		Timestamp:  time.Now().UTC(),
		Title:      title,
		Message:    message,
		EntityType: EntityTypeStackInstance,
		EntityID:   target.InstanceID,
		Trigger:    n.resolveTrigger(ctx, o.Trigger),
		Namespace:  target.Namespace,
		Branch:     target.Branch,
		ClusterID:  target.ClusterID,
		SiteURL:    o.SiteURL,
		Instances:  []models.NotificationTarget{target},
	}
	if owner != nil {
		// user_display_name keeps its old value (the owner's display
		// name) for compatibility.
		payload.UserDisplayName = owner.DisplayName
		payload.OwnerDisplayName = owner.DisplayName
		if payload.OwnerDisplayName == "" {
			payload.OwnerDisplayName = owner.Username
		}
	}
	if n.publicURL != "" && target.InstanceID != "" && notifType != eventInstanceDeleted {
		payload.InstanceURL = n.publicURL + "/stack-instances/" + url.PathEscape(target.InstanceID)
	}
	return payload
}

// eventInstanceDeleted has no instance page, so its payload has no
// instance_url.
const eventInstanceDeleted = "instance.deleted"

// resolveTrigger returns a copy of t. A user trigger without a name gets the
// username of its ID, when the user exists.
func (n *Notifier) resolveTrigger(ctx context.Context, t *hooks.Trigger) *hooks.Trigger {
	if t == nil || t.Type == "" {
		return nil
	}
	cp := *t
	if cp.Type == hooks.TriggerUser && cp.Name == "" && cp.ID != "" {
		if u := n.findUser(ctx, cp.ID); u != nil {
			cp.Name = u.Username
		}
	}
	return &cp
}

// findUser returns the user, or nil without a user repository, for an empty
// ID or on a lookup error.
func (n *Notifier) findUser(ctx context.Context, userID string) *models.User {
	if n.userRepo == nil || userID == "" {
		return nil
	}
	user, err := n.userRepo.FindByID(ctx, userID)
	if err != nil {
		return nil
	}
	return user
}

// disabledReceivers returns the receivers that switched off notifType. On a
// lookup error it logs and returns no user (fail open: all get the event).
func (n *Notifier) disabledReceivers(ctx context.Context, notifType string, userIDs []string) map[string]bool {
	if len(userIDs) == 0 {
		return nil
	}
	disabled, err := n.repo.DisabledUserIDs(ctx, notifType, userIDs)
	if err != nil {
		slog.Error("failed to read notification preferences; notifying all receivers",
			"type", notifType, "error", err)
		return nil
	}
	return disabled
}

func (n *Notifier) displayName(ctx context.Context, userID string) string {
	if user := n.findUser(ctx, userID); user != nil {
		return user.DisplayName
	}
	return ""
}

// NotifySystem creates a notification for all admin and devops users that
// did not switch the event type off in their preferences, and dispatches the
// event once to the channels. Requires userRepo to have been provided at
// construction time.
func (n *Notifier) NotifySystem(ctx context.Context, notifType, title, message, entityType, entityID string) error {
	return n.NotifySystemForInstances(ctx, notifType, title, message, entityType, entityID, nil)
}

// NotifySystemForInstances is NotifySystem for an event about more than one
// instance (for example a cleanup policy run). A channel with filters gets
// the event when at least one of the instances matches; without instances,
// only channels without filters get it. When the admin and devops list
// fails, the channels still get the event and the error is returned.
func (n *Notifier) NotifySystemForInstances(ctx context.Context, notifType, title, message, entityType, entityID string, instances []models.NotificationTarget) error {
	if n.userRepo == nil {
		slog.Warn("NotifySystem called without userRepo configured")
		return nil
	}

	// A failed admin list must not stop the channel dispatch below.
	admins, listErr := n.userRepo.ListByRoles(ctx, []string{"admin", "devops"})
	if listErr != nil {
		slog.Error("failed to list admin and devops users for a system notification", "type", notifType, "error", listErr)
		admins = nil
	}

	// Each admin or devops user gets the event only when it is on in the
	// own notification preferences (one query; fail open).
	ids := make([]string, 0, len(admins))
	for _, u := range admins {
		ids = append(ids, u.ID)
	}
	disabled := n.disabledReceivers(ctx, notifType, ids)
	for _, u := range admins {
		if disabled[u.ID] {
			continue
		}
		if err := n.notify(ctx, u.ID, notifType, title, message, entityType, entityID, false); err != nil {
			slog.Error("failed to create system notification for user", "user_id", u.ID, "type", notifType, "error", err)
		}
	}

	// Dispatch once to external channels for system events.
	if n.channelDispatcher != nil {
		n.enqueueDispatch(channel.EventPayload{
			EventType:       notifType,
			Timestamp:       time.Now().UTC(),
			Title:           title,
			Message:         message,
			UserDisplayName: "System",
			EntityType:      entityType,
			EntityID:        entityID,
			Instances:       instances,
		})
	}

	return listErr
}

// MessageTypeNotificationNew is the WebSocket message type for new notifications.
const MessageTypeNotificationNew = "notification.new"

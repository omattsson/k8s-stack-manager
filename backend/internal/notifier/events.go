package notifier

// AllEventTypes returns the complete list of notification event types
// that can be subscribed to for channel routing: every type that the
// notifier sends (PreferenceEventTypes).
// TestSentEventTypesAreKnown fails when code sends a type that is not in
// PreferenceEventTypes.
func AllEventTypes() []string {
	return PreferenceEventTypes()
}

// InstanceEventTypes returns the in-app notification event types of a stack
// instance. The owner and the followers of the instance get them. A user can
// switch each type off in the notification preferences.
//
// The Profile page shows the same list. Keep the fixture
// frontend/src/utils/__tests__/notification-event-types.json in sync: run
// `go test ./internal/notifier -run TestPreferenceEventTypesFixture -update`.
func InstanceEventTypes() []string {
	return []string{
		"deployment.success",
		"deployment.error",
		"deployment.partial",
		"deployment.warning",
		"deploy.timeout",
		"deployment.stopped",
		"stop.error",
		"instance.created",
		"instance.deleted",
		"clean.completed",
		"clean.error",
		"rollback.completed",
		"rollback.error",
		"stack.expiring",
		"stack.expired",
		"cleanup.policy.stop",
		"cleanup.policy.clean",
	}
}

// SystemEventTypes returns the in-app notification event types that only
// admin and devops users get.
func SystemEventTypes() []string {
	return []string{
		"cleanup.policy.executed",
		"quota.warning",
		"secret.expiring",
	}
}

// PreferenceEventTypes returns all event types that a user can set in the
// notification preferences: the instance types, then the system types.
func PreferenceEventTypes() []string {
	return append(InstanceEventTypes(), SystemEventTypes()...)
}

// preferenceEventTypeSet is the set form of PreferenceEventTypes.
var preferenceEventTypeSet = func() map[string]struct{} {
	set := make(map[string]struct{})
	for _, t := range PreferenceEventTypes() {
		set[t] = struct{}{}
	}
	return set
}()

// IsPreferenceEventType reports whether eventType is a known event type for
// the notification preferences.
func IsPreferenceEventType(eventType string) bool {
	_, ok := preferenceEventTypeSet[eventType]
	return ok
}

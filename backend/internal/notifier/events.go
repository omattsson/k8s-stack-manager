package notifier

// AllEventTypes returns the complete list of notification event types
// that can be subscribed to for channel routing.
func AllEventTypes() []string {
	return []string{
		"deployment.success",
		"deployment.error",
		"deployment.partial",
		"deployment.stopped",
		"deploy.timeout",
		"instance.created",
		"instance.deleted",
		"clean.completed",
		"clean.error",
		"rollback.completed",
		"rollback.error",
		"stop.error",
		"stack.expiring",
		"stack.expired",
		"quota.warning",
		"secret.expiring",
		"cleanup.policy.executed",
	}
}

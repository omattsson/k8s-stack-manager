// Package hooks dispatches lifecycle events to user-configured outbound HTTP webhooks.
// It models the Kubernetes admission-webhook pattern: subscriptions register
// against named events and receive a versioned JSON envelope; their response
// can either allow or (for pre-* events with failurePolicy=fail) abort the
// operation. v1 is non-mutating — handlers cannot rewrite the payload.
package hooks

import "time"

// Event names use kebab-case (e.g. "deploy-timeout") for webhook payloads.
// In-app notification types in the notifier package use dot-separated names
// (e.g. "deploy.timeout") — keep the two conventions distinct.
const (
	EventPreDeploy           = "pre-deploy"
	EventPostDeploy          = "post-deploy"
	EventPreInstanceCreate   = "pre-instance-create"
	EventPostInstanceCreate  = "post-instance-create"
	EventPreInstanceDelete   = "pre-instance-delete"
	EventPostInstanceDelete  = "post-instance-delete"
	EventPreNamespaceCreate  = "pre-namespace-create"
	EventPostNamespaceCreate = "post-namespace-create"
	EventPreRollback         = "pre-rollback"
	EventPostRollback        = "post-rollback"
	EventDeployFinalized     = "deploy-finalized"
	EventStopCompleted       = "stop-completed"
	EventCleanCompleted      = "clean-completed"
	EventRollbackCompleted   = "rollback-completed"
	EventDeleteCompleted     = "delete-completed"
	EventInstanceCreated     = "instance-created"

	// Phase 2 notification events (#189–#193).
	EventDeployTimeout          = "deploy-timeout"
	EventCleanupPolicyExecuted  = "cleanup-policy-executed"
	EventStackExpired           = "stack-expired"
	EventStackExpiring          = "stack-expiring"
	EventQuotaWarning           = "quota-warning"
	EventSecretExpiring         = "secret-expiring"
)

// FailurePolicy controls how dispatch errors propagate to the caller.
//
//	FailurePolicyFail   — errors abort the operation (only meaningful for pre-* events).
//	FailurePolicyIgnore — errors are logged and swallowed; the operation continues.
type FailurePolicy string

const (
	FailurePolicyFail   FailurePolicy = "fail"
	FailurePolicyIgnore FailurePolicy = "ignore"
)

// envelopeAPIVersion is the contract version for EventEnvelope and
// ActionRequest payloads. Subscribers should ignore envelopes with an
// unknown apiVersion rather than erroring, since the dispatcher bumps
// this on additive changes too. Version negotiation is handler-side
// only in v1 — the dispatcher does not read it back.
const envelopeAPIVersion = "hooks.k8sstackmanager.io/v1"

// maxHookResponseBytes caps subscriber response body reads to keep a
// misbehaving handler from causing OOM on the dispatch path.
const maxHookResponseBytes = 1 << 20 // 1 MiB

// EventEnvelope is the JSON payload posted to subscriber URLs.
type EventEnvelope struct {
	APIVersion  string                 `json:"apiVersion"`
	Kind        string                 `json:"kind"`
	Event       string                 `json:"event"`
	Timestamp   time.Time              `json:"timestamp"`
	RequestID   string                 `json:"request_id"`
	InstanceRef *InstanceRef           `json:"instance,omitempty"`
	Deployment  *DeploymentRef         `json:"deployment,omitempty"`
	Charts      []ChartRef             `json:"charts,omitempty"`
	Values      map[string]any         `json:"values,omitempty"`
	Metadata    map[string]string      `json:"metadata,omitempty"`
	Extra       map[string]any         `json:"extra,omitempty"`
}

// InstanceRef identifies a stack instance without coupling the hooks package to models.StackInstance.
type InstanceRef struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Namespace         string `json:"namespace"`
	OwnerID           string `json:"owner_id"`
	StackDefinitionID string `json:"stack_definition_id"`
	Branch            string `json:"branch,omitempty"`
	ClusterID         string `json:"cluster_id,omitempty"`
	Status            string `json:"status,omitempty"`
}

// DeploymentRef identifies a deployment in progress.
type DeploymentRef struct {
	ID        string    `json:"id"`
	StartedAt time.Time `json:"started_at"`
}

// ChartRef describes a chart involved in the event.
type ChartRef struct {
	Name            string `json:"name"`
	ReleaseName     string `json:"release_name,omitempty"`
	Version         string `json:"version,omitempty"`
	SourceRepoURL   string `json:"source_repo_url,omitempty"`
	BuildPipelineID string `json:"build_pipeline_id,omitempty"`
	Branch          string `json:"branch,omitempty"`
}

// HookResponse is the JSON shape subscribers return.
//
// Allowed=false on a pre-* event with FailurePolicyFail aborts the operation.
// Message is surfaced to the operator (logs and, where appropriate, API responses).
type HookResponse struct {
	Allowed bool   `json:"allowed"`
	Message string `json:"message,omitempty"`
}

// Subscription registers a webhook for one or more events.
type Subscription struct {
	Name           string        `json:"name"`
	Events         []string      `json:"events"`
	URL            string        `json:"url"`
	TimeoutSeconds int           `json:"timeout_seconds,omitempty"`
	FailurePolicy  FailurePolicy `json:"failure_policy,omitempty"`
	Secret         string        `json:"-"`
}

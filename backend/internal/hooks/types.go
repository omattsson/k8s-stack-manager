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
	// Trigger tells what started the operation (a user, a cleanup policy
	// or the TTL reaper). Set for the deploy, stop, clean, rollback and
	// delete events, and for cleanup-policy-executed.
	Trigger *Trigger `json:"trigger,omitempty"`
	// CleanupPolicy is the run summary of cleanup-policy-executed.
	CleanupPolicy *CleanupPolicyRun `json:"cleanup_policy,omitempty"`
}

// Results of one instance in a cleanup policy run (CleanupPolicyInstance.Result).
const (
	CleanupResultSuccess = "success"
	CleanupResultError   = "error"
	CleanupResultDryRun  = "dry_run"
)

// CleanupPolicyRun is the payload of cleanup-policy-executed: one run of a
// cleanup policy with at least one matching instance.
type CleanupPolicyRun struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Action    string `json:"action"`
	ClusterID string `json:"cluster_id"`
	// ClusterName is the name of the cluster of ClusterID. Omitted for
	// "all" and when the cluster is unknown.
	ClusterName string `json:"cluster_name,omitempty"`
	Condition   string `json:"condition,omitempty"`
	DryRun      bool   `json:"dry_run"`
	// Run is "scheduled" (cron) or "manual" (POST .../run).
	Run string `json:"run"`
	// Matched, Succeeded and Failed count all matching instances, also when
	// Instances is cut (InstancesTruncated).
	Matched            int                     `json:"matched"`
	Succeeded          int                     `json:"succeeded"`
	Failed             int                     `json:"failed"`
	Instances          []CleanupPolicyInstance `json:"instances"`
	InstancesTruncated bool                    `json:"instances_truncated,omitempty"`
}

// CleanupPolicyInstance is one matching instance of a cleanup policy run.
// For stop and clean, "success" means the operation started; the result
// comes later with stop-completed or clean-completed.
type CleanupPolicyInstance struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	OwnerID   string `json:"owner_id"`
	Result    string `json:"result"`
	Error     string `json:"error,omitempty"`
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
	// ClusterName is the name of the cluster of ClusterID. Omitted when the
	// cluster is unknown (deleted, no cluster_id, or the lookup failed).
	ClusterName string `json:"cluster_name,omitempty"`
	Status      string `json:"status,omitempty"`
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
	// ImageTag is the Docker-safe tag of Branch, the same value as the
	// {{.ImageTag}} template variable in the chart values. Subscribers (for
	// example a CI trigger gate) use it so they check and build the tag that
	// Helm deploys.
	ImageTag string `json:"image_tag,omitempty"`
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
	// Blocking applies to post-deploy only: the deployer waits for the
	// subscriber (progress lines go to the deploy log) before the instance
	// becomes running. Fire and FireWithProgress skip a blocking
	// subscription for post-deploy; FireBlocking calls it.
	Blocking bool   `json:"blocking,omitempty"`
	Secret   string `json:"-"`
}

package models

import (
	"context"
	"errors"
	"time"
)

// DeploymentLog records the output and status of a deployment operation.
type DeploymentLog struct {
	StartedAt       time.Time  `json:"started_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	ID              string     `json:"id" gorm:"primaryKey;size:36"`
	StackInstanceID string     `json:"stack_instance_id" gorm:"size:36"`
	Action          string     `json:"action" gorm:"size:50"` // "deploy", "stop", "clean", "rollback"
	Status          string     `json:"status" gorm:"size:50"` // "running", "success", "error", "partial" (deploy only)
	Output          string     `json:"output,omitempty" gorm:"type:longtext"`
	ErrorMessage    string     `json:"error_message,omitempty" gorm:"type:text"`
	ValuesSnapshot  string     `json:"values_snapshot,omitempty" gorm:"type:longtext"`
	TargetLogID     string     `json:"target_log_id,omitempty" gorm:"size:36"`
	// Branch is the instance branch of a deploy, or the branch of the target
	// deploy for a rollback to a target. Empty for older logs and for stop
	// and clean logs.
	Branch string `json:"branch,omitempty" gorm:"size:255"`
	// ChartVersions is a JSON object chart name -> chart version that a
	// deploy used. A rollback to this deploy installs the same versions.
	// Empty for logs written before the column existed.
	ChartVersions string `json:"chart_versions,omitempty" gorm:"type:text"`
	// UserID is the user who started a deploy. Per-user analytics count
	// deploys by this field, so the history stays after the instance is
	// deleted. Empty for system deploys and for other actions. Migration 49
	// sets it for older deploy logs to the owner of the instance and adds
	// the composite index idx_deployment_logs_user_action (user_id, action,
	// started_at, completed_at, status) for SummarizeByUsers.
	UserID string `json:"user_id,omitempty" gorm:"size:36"`
	// StartedBy is the username of UserID. The deploy log handlers set it
	// with one batch lookup. Empty when the user is not known.
	StartedBy string `json:"started_by,omitempty" gorm:"-"`
	// ReplicaID is the process identity of the replica that runs the
	// operation (POD_NAME or host name plus a random suffix per process, the
	// same value as its heartbeat row). The leader uses it to find
	// operations whose replica stopped (see InterruptedOperationRepository).
	// NULL for logs written before migration 54. Not in the API.
	ReplicaID string `json:"-" gorm:"size:253"`
	// DeadlineAt is the latest time the operation can still run: the start
	// plus the time budget of the operation (hooks, Helm, readiness wait,
	// blocking post-deploy hooks) plus a margin. The deploy manager moves it
	// later when the operation gets its concurrency slot. The leader ends a
	// running operation only after this time and only when its replica has
	// no fresh heartbeat. NULL for logs written before migration 54 (the
	// recovery skips them). Written with the clock of the replica.
	DeadlineAt *time.Time `json:"-"`
}

// ErrDeployLogNotRunning is returned by DeploymentLogRepository.Update when
// the stored log is no longer running (for example the leader ended it as
// an interrupted operation). The update changes nothing.
var ErrDeployLogNotRunning = errors.New("deployment log is no longer running")

// DeploymentLogDeadlineExtender is implemented by the GORM deployment log
// repository. ExtendDeadline sets deadline_at to deadline when the log is
// still running and its deadline is earlier. It returns
// ErrDeployLogNotRunning when the log is no longer running.
type DeploymentLogDeadlineExtender interface {
	ExtendDeadline(ctx context.Context, id string, deadline time.Time) error
}

// DeploymentLogOutputWriter is implemented by the GORM deployment log
// repository. UpdateRunningOutput sets the output of a running log to the
// lines streamed so far, so a page that loads the log during the operation
// shows them. It returns ErrDeployLogNotRunning when the log is no longer
// running; the final write of the operation then has the full output.
type DeploymentLogOutputWriter interface {
	UpdateRunningOutput(ctx context.Context, id, output string) error
}

// Deployment log action constants.
const (
	DeployActionDeploy   = "deploy"
	DeployActionStop     = "stop"
	DeployActionClean    = "clean"
	DeployActionRollback = "rollback"
)

// Deployment log status constants.
const (
	DeployLogRunning = "running"
	DeployLogSuccess = "success"
	DeployLogError   = "error"
	// DeployLogPartial is the status of a deploy log when some charts
	// deployed and others failed: the instance status is "partial" too.
	// Only deploy logs use it; older deploy logs keep "error" for a partial
	// deploy. A partial deploy is not a rollback target, and the deploy
	// statistics count it as an error.
	DeployLogPartial = "partial"
)

// IsDeployLogAction reports whether action is a deployment log action.
func IsDeployLogAction(action string) bool {
	switch action {
	case DeployActionDeploy, DeployActionRollback, DeployActionStop, DeployActionClean:
		return true
	}
	return false
}

// IsDeployLogStatus reports whether status is a deployment log status.
func IsDeployLogStatus(status string) bool {
	switch status {
	case DeployLogRunning, DeployLogSuccess, DeployLogError, DeployLogPartial:
		return true
	}
	return false
}

// MaxDeployLogPageSize is the largest page of the deployment log list. A
// larger limit gives this size.
const MaxDeployLogPageSize = 100

// DeploymentLogFilters holds optional filters and pagination for querying deployment logs.
type DeploymentLogFilters struct {
	InstanceID string
	Cursor     string
	// Actions and Statuses limit the rows (and the total) to these values.
	// Empty means all.
	Actions  []string
	Statuses []string
	Limit    int
	Offset   int
	// Summary selects only the row columns: id, stack_instance_id, action,
	// status, branch, error_message, target_log_id, user_id, started_at and
	// completed_at. No output, values snapshot or chart versions.
	Summary bool
}

// DeploymentLogResult holds the result of a paginated deployment log query.
type DeploymentLogResult struct {
	// Highlights is set only in the summary view without a cursor.
	Highlights *DeployLogHighlights `json:"highlights,omitempty"`
	Data       []DeploymentLog      `json:"data"`
	Total      int64                `json:"total"`
	NextCursor string               `json:"next_cursor,omitempty"`
}

// DeployLogHighlights holds the deploy logs that the history view shows
// first. The fields are summary rows (see DeploymentLogFilters.Summary). A
// field is nil when no log matches. The action and status filters do not
// apply.
type DeployLogHighlights struct {
	// Running is the newest running log (any action).
	Running *DeploymentLog `json:"running,omitempty"`
	// Current is the deploy whose values run now: the newest successful or
	// partial deploy, or the target of a newer successful rollback. Nil when
	// that rollback has no target or its target is not a log of the
	// instance.
	Current *DeploymentLog `json:"current,omitempty"`
	// LatestFailure is the newest error log that started after the newest
	// successful or partial deploy or rollback (any error log when there is
	// none). A partial log is never a failure here.
	LatestFailure *DeploymentLog `json:"latest_failure,omitempty"`
}

// DeploymentLogHighlightReader is implemented by the GORM deployment log
// repository. HistoryHighlights returns the highlights of the deploy
// history of the instance. No matching log is not an error.
type DeploymentLogHighlightReader interface {
	HistoryHighlights(ctx context.Context, instanceID string) (*DeployLogHighlights, error)
}

// DeployLogSummary provides lightweight aggregate counts for an instance's
// deployment logs, avoiding the need to fetch full log entities with their
// potentially large Output and Details fields.
type DeployLogSummary struct {
	LastDeployAt *time.Time
	InstanceID   string
	// UserID is set by SummarizeByUsers; InstanceID stays empty there.
	UserID       string
	DeployCount  int
	SuccessCount int
	ErrorCount   int
}

// DeploymentLogWithContext extends DeploymentLog with denormalized instance
// and user fields, populated via JOIN to avoid N+1 queries.
type DeploymentLogWithContext struct {
	DeploymentLog
	InstanceName  string `json:"instance_name"`
	OwnerUsername string `json:"owner_username"`
}

// DeploymentLogRepository defines data access operations for deployment logs.
type DeploymentLogRepository interface {
	Create(ctx context.Context, log *DeploymentLog) error
	FindByID(ctx context.Context, id string) (*DeploymentLog, error)
	// Update writes all columns of the log, only while the stored log is
	// running. When the stored log is no longer running it changes nothing
	// and returns ErrDeployLogNotRunning (a late writer after an interrupted
	// operation recovery must not overwrite the recovered log).
	Update(ctx context.Context, log *DeploymentLog) error
	ListByInstance(ctx context.Context, instanceID string) ([]DeploymentLog, error)
	ListByInstancePaginated(ctx context.Context, filters DeploymentLogFilters) (*DeploymentLogResult, error)
	GetLatestByInstance(ctx context.Context, instanceID string) (*DeploymentLog, error)
	// ListLatestByActions returns up to limit logs of the instance whose
	// action is one of actions, newest first (by started_at). Only the small
	// columns are loaded (no output, values snapshot or chart versions).
	ListLatestByActions(ctx context.Context, instanceID string, actions []string, limit int) ([]DeploymentLog, error)
	SummarizeByInstance(ctx context.Context, instanceID string) (*DeployLogSummary, error)
	SummarizeBatch(ctx context.Context, instanceIDs []string) (map[string]*DeployLogSummary, error)
	// SummarizeByUsers returns deploy statistics per user (DeploymentLog.UserID)
	// over all deploy logs, also of deleted instances. Users without deploys
	// are not in the map.
	SummarizeByUsers(ctx context.Context, userIDs []string) (map[string]*DeployLogSummary, error)
	CountByAction(ctx context.Context, action string) (int, error)
	ListRecentGlobal(ctx context.Context, limit int) ([]DeploymentLogWithContext, error)
}

package models

import (
	"context"
	"errors"
	"time"
)

// ReplicaHeartbeat is the last sign of life of one backend process (table
// replica_heartbeats). Every replica writes its row every heartbeat
// interval, also when leader election is disabled. The leader reads the
// rows to tell a live operation from an operation whose replica stopped,
// and deletes old rows.
//
// The ID is the process identity: the replica identity (POD_NAME or host
// name) plus a random suffix per process. A restarted process gets a new
// ID, so the row of a stopped process never becomes fresh again. The table
// does not embed Base and has no Version: each process writes only its own
// row.
type ReplicaHeartbeat struct {
	// LastSeen is the time of the last heartbeat, from the database server
	// clock (UTC on MySQL).
	LastSeen time.Time `json:"last_seen" gorm:"not null;index:idx_replica_heartbeats_last_seen"`
	// ID is the process identity (max 253 characters).
	ID string `json:"id" gorm:"primaryKey;size:253"`
}

// TableName returns the table name of ReplicaHeartbeat.
func (ReplicaHeartbeat) TableName() string { return "replica_heartbeats" }

// ReplicaHeartbeatRepository stores the heartbeats of the backend processes.
// All times come from the database server clock (written and compared in
// SQL), so the clocks of the replicas do not matter for the heartbeat check.
type ReplicaHeartbeatRepository interface {
	// Beat inserts or updates the row of id with last_seen = the current
	// database time.
	Beat(ctx context.Context, id string) error
	// SeenWithin returns the IDs of ids whose last heartbeat is at most
	// maxAge old (database time). An ID without a row is not in the result.
	SeenWithin(ctx context.Context, ids []string, maxAge time.Duration) (map[string]bool, error)
	// Remove deletes the row of id (clean shutdown).
	Remove(ctx context.Context, id string) error
	// DeleteOlderThan deletes the rows whose last heartbeat is more than
	// maxAge old (database time) and returns the number of deleted rows.
	DeleteOlderThan(ctx context.Context, maxAge time.Duration) (int64, error)
}

// InterruptedOperationMessage returns the error message of an operation
// (deploy log action) whose replica stopped before the operation ended. The
// last sentence tells the user what to do next.
func InterruptedOperationMessage(action string) string {
	const prefix = "Interrupted: the server that ran this operation stopped. "
	switch action {
	case DeployActionStop:
		return prefix + "Stop again."
	case DeployActionClean:
		return prefix + "Clean again."
	default:
		return prefix + "Deploy again."
	}
}

// ErrInterruptConflict is returned by InterruptOperation when the instance
// changed after the caller read it (another status or another update time).
// Nothing is changed; the caller skips the operation and checks it again
// later.
var ErrInterruptConflict = errors.New("instance changed during the interrupt")

// InterruptRequest describes a running operation that the leader ends.
type InterruptRequest struct {
	// LogStartedAt is the start time of the deploy log. A log of the same
	// instance that started later is a newer operation.
	LogStartedAt time.Time
	// Now is the completion time of the log and the update time of the
	// instance.
	Now time.Time
	// InstanceUpdatedAt is the updated_at value that the caller read. With
	// InstanceStatus it is the optimistic lock of the instance update.
	InstanceUpdatedAt time.Time
	LogID             string
	InstanceID        string
	// ReplicaID is the process identity on the log. The log update requires
	// that this process has no heartbeat within StaleAfter (database time),
	// so a heartbeat that arrives after the listing stops the interrupt.
	ReplicaID  string
	StaleAfter time.Duration
	// InstanceStatus is the in-progress status that the caller read. Empty:
	// do not change the instance (it is not in progress, or it is gone).
	InstanceStatus string
	Message        string
}

// InterruptResult tells what InterruptOperation changed.
type InterruptResult struct {
	// LogClosed is true when the log was still running and is now error.
	LogClosed bool
	// InstanceUpdated is true when the instance is now error.
	InstanceUpdated bool
}

// InterruptedOperationRepository finds and ends the operations whose
// replica stopped (hard kill, OOM kill, node loss).
type InterruptedOperationRepository interface {
	// ListInterruptCandidates returns at most limit running deploy logs,
	// oldest first, whose deadline_at is before now, whose replica ID is set
	// and is not selfID, and whose replica has no heartbeat within
	// staleAfter (database time). Logs of live replicas are excluded in SQL,
	// so they cannot fill the batch. Only the small columns are loaded.
	ListInterruptCandidates(ctx context.Context, now time.Time, selfID string, staleAfter time.Duration, limit int) ([]DeploymentLog, error)
	// InterruptOperation ends the operation in one transaction: the log
	// gets error (only when it is still running and its replica still has
	// no fresh heartbeat), and the instance gets
	// error with req.Message and no post-deploy hook marker when
	// req.InstanceStatus is set and no newer log of the instance exists.
	// It returns ErrInterruptConflict (and changes nothing) when the
	// instance no longer has req.InstanceStatus and req.InstanceUpdatedAt.
	InterruptOperation(ctx context.Context, req InterruptRequest) (InterruptResult, error)
}

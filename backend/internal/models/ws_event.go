package models

import (
	"context"
	"time"
)

// WebSocket fan-out targets. A WSEvent target is one of these values, or a
// prefix followed by an ID.
const (
	// WSEventTargetAll sends the event to all clients.
	WSEventTargetAll = "all"
	// WSEventTargetInstancePrefix + instance ID sends the event to the
	// clients that subscribe to the instance.
	WSEventTargetInstancePrefix = "instance:"
	// WSEventTargetUserPrefix + user ID sends the event to the clients of
	// the user.
	WSEventTargetUserPrefix = "user:"
	// WSEventTargetRevokeUserPrefix + user ID closes the sockets of the user
	// on all replicas (no payload).
	WSEventTargetRevokeUserPrefix = "revoke:user:"
	// WSEventTargetRevokeTokenPrefix + token ID (jti) closes the sockets
	// opened with the token on all replicas (no payload).
	WSEventTargetRevokeTokenPrefix = "revoke:token:"
)

// WSEvent is one WebSocket message that a replica shares with the other
// replicas (table ws_events). Each replica delivers the message to its own
// clients at once and writes one row. The other replicas poll the table and
// deliver the rows of other origins to their clients.
//
// The table is append-only and short-lived: rows are never updated, and the
// leader deletes rows older than the retention. For this reason WSEvent does
// not embed Base (no UUID, no soft delete) and has no Version: the
// auto-increment ID is the read position of the pollers.
//
//nolint:govet // Struct field alignment optimized for readability over padding
type WSEvent struct {
	// ID is the auto-increment position. Pollers read rows with id > last id.
	ID int64 `json:"id" gorm:"primaryKey;autoIncrement"`
	// CreatedAt is the time of the broadcast on the origin replica. The
	// cleanup deletes rows by this column.
	CreatedAt time.Time `json:"created_at" gorm:"not null;index:idx_ws_events_created_at"`
	// Target is "all", "instance:<id>", "user:<id>", "revoke:user:<id>" or
	// "revoke:token:<jti>".
	Target string `json:"target" gorm:"size:128;not null"`
	// Origin identifies the process that wrote the row: the replica identity
	// (POD_NAME or host name) plus a random suffix per process.
	Origin string `json:"origin" gorm:"size:253;not null"`
	// Payload is the WebSocket message (JSON), sent to clients as is.
	Payload string `json:"payload" gorm:"type:mediumtext;not null"`
}

// TableName returns the table name of WSEvent.
func (WSEvent) TableName() string { return "ws_events" }

// WSEventRepository stores the WebSocket fan-out events.
type WSEventRepository interface {
	// Insert stores the events in one statement. It sets the IDs.
	Insert(ctx context.Context, events []*WSEvent) error
	// MaxID returns the highest ID, or 0 when the table is empty.
	MaxID(ctx context.Context) (int64, error)
	// ListAfter returns at most limit events with id > afterID, in ID order.
	// The payload of events from skipPayloadOrigin is left empty: the caller
	// skips its own rows, but still needs their IDs to find gaps.
	ListAfter(ctx context.Context, afterID int64, limit int, skipPayloadOrigin string) ([]WSEvent, error)
	// IDsAfter returns at most limit IDs greater than afterID, in ID order
	// (no payloads).
	IDsAfter(ctx context.Context, afterID int64, limit int) ([]int64, error)
	// ListByIDs returns the events with these IDs that exist, in ID order.
	// The payload of events from skipPayloadOrigin is left empty.
	ListByIDs(ctx context.Context, ids []int64, skipPayloadOrigin string) ([]WSEvent, error)
	// DeleteOlderThan deletes the events created before t in batches of at
	// most batchSize rows (each batch is one short statement) and returns
	// the number of deleted rows. It stops between batches when ctx is done.
	DeleteOlderThan(ctx context.Context, t time.Time, batchSize int) (int64, error)
}

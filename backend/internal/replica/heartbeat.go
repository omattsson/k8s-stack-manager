// Package replica identifies a backend process and writes its heartbeat.
//
// Every backend process (replica) has a process identity: the replica
// identity (POD_NAME, else the host name) plus a random suffix. The deploy
// manager stores it on each deploy log, and the heartbeat writes it to the
// replica_heartbeats table every HeartbeatInterval; the database server
// clock sets and compares the heartbeat time, so replica clocks do not
// matter. The leader treats a process without a heartbeat for StaleAfter as
// stopped and ends its running operations (see deployer.InterruptRecovery).
package replica

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"backend/internal/models"

	"github.com/google/uuid"
)

const (
	// HeartbeatInterval is how often a process writes its heartbeat.
	HeartbeatInterval = 30 * time.Second
	// StaleAfter is the age of the last heartbeat after which the leader
	// treats a process as stopped (four missed heartbeats).
	StaleAfter = 2 * time.Minute
	// maxProcessIDLen is the size of replica_heartbeats.id and
	// deployment_logs.replica_id.
	maxProcessIDLen = 253
	// beatTimeout limits one heartbeat write.
	beatTimeout = 10 * time.Second
	// fallbackIdentity is used when the process has no POD_NAME and no host
	// name.
	fallbackIdentity = "backend"
)

// ProcessID returns a new process identity: identity plus "-" and 8 random
// hex characters. Two processes with the same host name (docker-compose
// keeps the host name over a restart) get different IDs, so the row of a
// stopped process never becomes fresh again. The result fits 253
// characters.
func ProcessID(identity string) string {
	if identity == "" {
		identity = fallbackIdentity
	}
	const maxIdentity = maxProcessIDLen - 9 // "-" + 8 hex characters
	if len(identity) > maxIdentity {
		identity = identity[:maxIdentity]
	}
	return identity + "-" + uuid.New().String()[:8]
}

// Heartbeat writes the heartbeat row of one process. Every replica runs it,
// also the leader and a process with leader election disabled.
type Heartbeat struct {
	repo     models.ReplicaHeartbeatRepository
	cancel   context.CancelFunc
	done     chan struct{}
	id       string
	interval time.Duration
	mu       sync.Mutex
	// failing is true after a failed write; the next success logs once.
	failing bool
}

// NewHeartbeat creates a heartbeat for the process id. An interval <= 0
// uses HeartbeatInterval. A nil repo or an empty id returns nil; the methods
// of a nil *Heartbeat do nothing.
func NewHeartbeat(repo models.ReplicaHeartbeatRepository, id string, interval time.Duration) *Heartbeat {
	if repo == nil || id == "" {
		return nil
	}
	if interval <= 0 {
		interval = HeartbeatInterval
	}
	return &Heartbeat{repo: repo, id: id, interval: interval}
}

// ID returns the process identity, or "" for a nil heartbeat.
func (h *Heartbeat) ID() string {
	if h == nil {
		return ""
	}
	return h.id
}

// Start writes the first heartbeat at once and then every interval, in a
// goroutine. A second Start does nothing.
func (h *Heartbeat) Start() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.done = make(chan struct{})
	go h.run(ctx, h.done)
	slog.Info("Replica heartbeat started", "replica_id", h.id, "interval", h.interval)
}

func (h *Heartbeat) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	h.beat(ctx)
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.beat(ctx)
		}
	}
}

// beat writes one heartbeat. A failure is logged once until the next
// success.
func (h *Heartbeat) beat(ctx context.Context) {
	bctx, cancel := context.WithTimeout(ctx, beatTimeout)
	defer cancel()
	err := h.repo.Beat(bctx, h.id)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		if !h.failing {
			slog.Error("Replica heartbeat write failed", "replica_id", h.id, "error", err)
		}
		h.failing = true
		return
	}
	if h.failing {
		slog.Info("Replica heartbeat write works again", "replica_id", h.id)
		h.failing = false
	}
}

// Stop ends the heartbeat and deletes the row of the process, so the
// leader can end operations that this process left running at once (after
// the age limit) instead of after StaleAfter. Call it after the deploy
// manager stopped. timeout limits the delete.
func (h *Heartbeat) Stop(timeout time.Duration) {
	if h == nil {
		return
	}
	h.mu.Lock()
	cancel, done := h.cancel, h.done
	h.cancel, h.done = nil, nil
	h.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
	ctx, cancelRemove := context.WithTimeout(context.Background(), timeout)
	defer cancelRemove()
	if err := h.repo.Remove(ctx, h.id); err != nil {
		slog.Warn("Replica heartbeat row not deleted", "replica_id", h.id, "error", err)
	}
}

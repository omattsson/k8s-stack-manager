// Package ttl provides automatic expiry of stack instances via a background reaper.
package ttl

import (
	"log"
	"time"

	"backend/internal/models"
	"backend/internal/websocket"
)

// Reaper periodically checks for expired stack instances and marks them as stopped.
type Reaper struct {
	instanceRepo models.StackInstanceRepository
	auditRepo    models.AuditLogRepository
	hub          websocket.BroadcastSender
	interval     time.Duration
	stopCh       chan struct{}
	doneCh       chan struct{}
}

// NewReaper creates a new TTL reaper.
// auditRepo and hub are optional (may be nil).
func NewReaper(
	instanceRepo models.StackInstanceRepository,
	auditRepo models.AuditLogRepository,
	hub websocket.BroadcastSender,
	interval time.Duration,
) *Reaper {
	return &Reaper{
		instanceRepo: instanceRepo,
		auditRepo:    auditRepo,
		hub:          hub,
		interval:     interval,
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
	}
}

// Start begins the periodic expiry check loop. It blocks until Stop is called
// or the stop channel is closed. Call this in a goroutine.
func (r *Reaper) Start() {
	defer close(r.doneCh)

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	log.Printf("[TTL] Reaper started (interval=%s)", r.interval)

	for {
		select {
		case <-r.stopCh:
			log.Printf("[TTL] Reaper stopped")
			return
		case <-ticker.C:
			r.processExpired()
		}
	}
}

// Stop signals the reaper to shut down and waits for it to finish.
func (r *Reaper) Stop() {
	close(r.stopCh)
	<-r.doneCh
}

func (r *Reaper) processExpired() {
	expired, err := r.instanceRepo.ListExpired()
	if err != nil {
		log.Printf("[TTL] Error listing expired instances: %v", err)
		return
	}

	for _, inst := range expired {
		inst.Status = models.StackStatusStopped
		inst.ErrorMessage = "Expired (TTL)"
		inst.UpdatedAt = time.Now().UTC()
		if updateErr := r.instanceRepo.Update(inst); updateErr != nil {
			log.Printf("[TTL] Error updating instance %s: %v", inst.ID, updateErr)
			continue
		}

		if r.auditRepo != nil {
			auditEntry := &models.AuditLog{
				UserID:     "system",
				Username:   "system",
				Action:     "expired",
				EntityType: "stack_instance",
				EntityID:   inst.ID,
				Details:    "Instance expired after TTL",
				Timestamp:  time.Now().UTC(),
			}
			_ = r.auditRepo.Create(auditEntry)
		}

		if r.hub != nil {
			msg, msgErr := websocket.NewMessage("instance.expired", inst)
			if msgErr == nil {
				b, bErr := msg.Bytes()
				if bErr == nil {
					r.hub.Broadcast(b)
				}
			}
		}

		log.Printf("[TTL] Instance %s expired and stopped", inst.ID)
	}
}

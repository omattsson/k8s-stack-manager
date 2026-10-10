// Package ttl provides automatic expiry of stack instances via a background reaper.
package ttl

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"backend/internal/models"
	"backend/internal/websocket"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// InstanceStopper can stop a running stack instance (e.g. Helm uninstall).
type InstanceStopper interface {
	StopInstance(ctx context.Context, inst *models.StackInstance) error
}

// Reaper periodically checks for expired stack instances and marks them as stopped.
type Reaper struct {
	instanceRepo models.StackInstanceRepository
	auditRepo    models.AuditLogRepository
	hub          websocket.BroadcastSender
	stopper      InstanceStopper
	notifier     ExpiryNotifier
	interval     time.Duration
	stopCh       chan struct{}
	doneCh       chan struct{}
	once         sync.Once
}

// NewReaper creates a new TTL reaper.
// auditRepo, hub, and stopper are optional (may be nil).
func NewReaper(
	instanceRepo models.StackInstanceRepository,
	auditRepo models.AuditLogRepository,
	hub websocket.BroadcastSender,
	stopper InstanceStopper,
	interval time.Duration,
) *Reaper {
	return &Reaper{
		instanceRepo: instanceRepo,
		auditRepo:    auditRepo,
		hub:          hub,
		stopper:      stopper,
		interval:     interval,
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
	}
}

// WithNotifier sets the notifier that sends "stack.expired" to the owner and
// the followers of an expired instance. nil sends no notification.
func (r *Reaper) WithNotifier(n ExpiryNotifier) *Reaper {
	r.notifier = n
	return r
}

// Start begins the periodic expiry check loop. It blocks until Stop is called
// or the stop channel is closed. Call this in a goroutine. Start and Stop
// work once; use Run to start the reaper again after a stop.
func (r *Reaper) Start() {
	defer close(r.doneCh)
	ctx, cancel := contextUntilClosed(r.stopCh)
	defer cancel()
	r.Run(ctx)
}

// Stop signals the reaper to shut down and waits for it to finish.
func (r *Reaper) Stop() {
	r.once.Do(func() { close(r.stopCh) })
	<-r.doneCh
}

// Run runs the periodic expiry check loop until ctx is done. It blocks. Run
// can be called again after it returned (one call per leadership term).
func (r *Reaper) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	slog.Info("TTL reaper started", "interval", r.interval)

	// Perform an initial check immediately on startup.
	r.processExpired()

	for {
		select {
		case <-ctx.Done():
			slog.Info("TTL reaper stopped")
			return
		case <-ticker.C:
			r.processExpired()
		}
	}
}

// contextUntilClosed returns a context that is cancelled when stopCh is
// closed or when cancel is called.
func contextUntilClosed(stopCh <-chan struct{}) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

func (r *Reaper) processExpired() {
	start := time.Now()
	ctx, span := reaperTracer.Start(context.Background(), "ttl.reap_cycle")

	expired, err := r.instanceRepo.ListExpired()
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		span.End()
		rMetrics.reapDuration.Record(ctx, time.Since(start).Seconds())
		slog.Error("Failed to list expired instances", "error", err)
		return
	}

	span.SetAttributes(attribute.Int("ttl.expired_count", len(expired)))

	for _, inst := range expired {
		// Attempt Helm uninstall via stopper if available.
		if r.stopper != nil {
			if stopErr := r.stopper.StopInstance(context.Background(), inst); stopErr != nil {
				slog.Error("Failed to initiate stop for expired instance, marking as stopped", "instance_id", inst.ID, "error", stopErr)
				// Fall through to manual status update below.
			} else {
				// Stop initiated; async process handles status transitions.
				slog.Info("Expired instance stop initiated via deployer", "instance_id", inst.ID)
				r.logExpiry(inst)
				r.notifyExpired(inst, "Stack %q expired after its TTL and is being stopped")
				rMetrics.expiredTotal.Add(ctx, 1)
				continue
			}
		}

		// No stopper or stop failed — mark as stopped directly.
		now := time.Now().UTC()
		inst.Status = models.StackStatusStopped
		inst.ErrorMessage = "Expired (TTL)"
		inst.StoppedAt = &now
		inst.UpdatedAt = now
		if updateErr := r.instanceRepo.Update(inst); updateErr != nil {
			slog.Error("Failed to update expired instance", "instance_id", inst.ID, "error", updateErr)
			continue
		}

		r.logExpiry(inst)
		r.notifyExpired(inst, "Stack %q expired after its TTL and was stopped")
		rMetrics.expiredTotal.Add(ctx, 1)

		slog.Info("Instance expired and stopped", "instance_id", inst.ID)
	}

	span.SetStatus(codes.Ok, "")
	span.End()
	rMetrics.reapDuration.Record(ctx, time.Since(start).Seconds())
}

// notifyExpired sends "stack.expired" to the owner and the followers of the
// instance. format gets the instance name. The stop itself later sends
// "deployment.stopped" (or "stop.error"); a user can switch off each type in
// the notification preferences.
func (r *Reaper) notifyExpired(inst *models.StackInstance, format string) {
	if r.notifier == nil {
		return
	}
	if err := r.notifier.NotifyInstance(context.Background(), models.NewNotificationTarget(inst),
		"stack.expired", "Stack expired", fmt.Sprintf(format, inst.Name)); err != nil {
		slog.Error("Failed to send stack expired notification", "instance_id", inst.ID, "error", err)
	}
}

// logExpiry creates an audit log entry and broadcasts a WebSocket message for an expired instance.
func (r *Reaper) logExpiry(inst *models.StackInstance) {
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
		if auditErr := r.auditRepo.Create(auditEntry); auditErr != nil {
			slog.Error("Failed to create audit log for expired instance", "instance_id", inst.ID, "error", auditErr)
		}
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
}

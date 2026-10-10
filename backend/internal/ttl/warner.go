package ttl

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"backend/internal/models"
)

// ExpiryNotifier sends in-app notifications for TTL warnings to the owner
// and the followers of the instance.
type ExpiryNotifier interface {
	NotifyInstance(ctx context.Context, target models.NotificationTarget, notifType, title, message string) error
}

// Warner periodically checks for stack instances approaching TTL expiry and
// sends a one-time warning notification to the instance owner.
//
// The "already warned" state is stack_instances.expiry_warned_at in the
// database, not process memory. A conditional update marks the instance
// before the warning is sent, so a new leader does not warn again, and two
// warners that run at the same time (a short leader overlap) send one
// warning. A new expiry time (deploy, extend) clears the mark.
type Warner struct {
	instanceRepo models.StackInstanceRepository
	notifier     ExpiryNotifier
	threshold    time.Duration // warn this far before ExpiresAt
	interval     time.Duration // how often to check
	stopCh       chan struct{}
	doneCh       chan struct{}
	once         sync.Once
}

// NewWarner creates a TTL expiry warner. threshold is how far before expiry to
// warn (default 30m), interval is how often to scan (default 60s).
func NewWarner(instanceRepo models.StackInstanceRepository, notifier ExpiryNotifier, threshold, interval time.Duration) *Warner {
	if threshold <= 0 {
		threshold = 30 * time.Minute
	}
	if interval <= 0 {
		interval = 60 * time.Second
	}
	return &Warner{
		instanceRepo: instanceRepo,
		notifier:     notifier,
		threshold:    threshold,
		interval:     interval,
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
	}
}

// Start begins the periodic warning check loop. Blocks until Stop is called.
// Start and Stop work once; use Run to start the warner again after a stop.
func (w *Warner) Start() {
	defer close(w.doneCh)
	ctx, cancel := contextUntilClosed(w.stopCh)
	defer cancel()
	w.Run(ctx)
}

// Stop signals the warner to shut down and waits for it to finish.
func (w *Warner) Stop() {
	w.once.Do(func() { close(w.stopCh) })
	<-w.doneCh
}

// Run runs the periodic warning check loop until ctx is done. It blocks. Run
// can be called again after it returned (one call per leadership term).
func (w *Warner) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	slog.Info("TTL warner started", "threshold", w.threshold, "interval", w.interval)

	w.check()

	for {
		select {
		case <-ctx.Done():
			slog.Info("TTL warner stopped")
			return
		case <-ticker.C:
			w.check()
		}
	}
}

func (w *Warner) check() {
	// The repository returns only instances without a warning for the
	// current expiry time.
	instances, err := w.instanceRepo.ListExpiringSoon(w.threshold)
	if err != nil {
		slog.Error("TTL warner: failed to list expiring instances", "error", err)
		return
	}

	for _, inst := range instances {
		if inst.ExpiresAt == nil {
			continue
		}
		// Mark first, then notify: the mark succeeds for one warner only,
		// and it fails when the expiry time changed after the list.
		marked, markErr := w.instanceRepo.MarkExpiryWarned(inst.ID, *inst.ExpiresAt, time.Now().UTC())
		if markErr != nil {
			slog.Error("TTL warner: failed to mark expiry warning", "instance_id", inst.ID, "error", markErr)
			continue
		}
		if !marked {
			continue
		}

		remaining := time.Until(*inst.ExpiresAt).Truncate(time.Minute)
		_ = w.notifier.NotifyInstance(
			context.Background(),
			models.NewNotificationTarget(inst),
			"stack.expiring",
			"Stack expiring soon",
			fmt.Sprintf("Stack %q will expire in %s", inst.Name, remaining),
		)
		slog.Info("TTL warner: sent expiry warning", "instance_id", inst.ID, "expires_at", inst.ExpiresAt)
	}
}

package ttl

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"backend/internal/models"
)

// ExpiryNotifier sends in-app notifications for TTL warnings.
type ExpiryNotifier interface {
	Notify(ctx context.Context, userID, notifType, title, message, entityType, entityID string) error
}

// Warner periodically checks for stack instances approaching TTL expiry and
// sends a one-time warning notification to the instance owner.
type Warner struct {
	instanceRepo models.StackInstanceRepository
	notifier     ExpiryNotifier
	threshold    time.Duration // warn this far before ExpiresAt
	interval     time.Duration // how often to check
	stopCh       chan struct{}
	doneCh       chan struct{}
	once         sync.Once

	// warned tracks instances for which a warning has already been sent.
	// Key: instanceID + "|" + ExpiresAt (so a TTL extension resets the warning).
	// Value: the ExpiresAt time, used for pruning entries once they've passed.
	mu     sync.Mutex
	warned map[string]time.Time
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
		warned:       make(map[string]time.Time),
	}
}

// Start begins the periodic warning check loop. Blocks until Stop is called.
func (w *Warner) Start() {
	defer close(w.doneCh)

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	slog.Info("TTL warner started", "threshold", w.threshold, "interval", w.interval)

	w.check()

	for {
		select {
		case <-w.stopCh:
			slog.Info("TTL warner stopped")
			return
		case <-ticker.C:
			w.check()
		}
	}
}

// Stop signals the warner to shut down and waits for it to finish.
func (w *Warner) Stop() {
	w.once.Do(func() { close(w.stopCh) })
	<-w.doneCh
}

func (w *Warner) check() {
	w.pruneWarned()

	instances, err := w.instanceRepo.ListExpiringSoon(w.threshold)
	if err != nil {
		slog.Error("TTL warner: failed to list expiring instances", "error", err)
		return
	}

	for _, inst := range instances {
		key := inst.ID + "|" + inst.ExpiresAt.Format(time.RFC3339)

		w.mu.Lock()
		_, already := w.warned[key]
		if !already {
			w.warned[key] = *inst.ExpiresAt
		}
		w.mu.Unlock()

		if already {
			continue
		}

		remaining := time.Until(*inst.ExpiresAt).Truncate(time.Minute)
		_ = w.notifier.Notify(
			context.Background(),
			inst.OwnerID,
			"stack.expiring",
			"Stack expiring soon",
			fmt.Sprintf("Stack %q will expire in %s", inst.Name, remaining),
			"stack_instance",
			inst.ID,
		)
		slog.Info("TTL warner: sent expiry warning", "instance_id", inst.ID, "expires_at", inst.ExpiresAt)
	}
}

// pruneWarned removes entries whose ExpiresAt has passed.
func (w *Warner) pruneWarned() {
	now := time.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	for key, expiresAt := range w.warned {
		if expiresAt.Before(now) {
			delete(w.warned, key)
		}
	}
}

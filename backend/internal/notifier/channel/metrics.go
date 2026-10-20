package channel

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Reasons of a dropped event (label reason of notification.channel.dropped).
const (
	// DropReasonQueueFull: the intake queue of the notifier or the queue of
	// the channel was full.
	DropReasonQueueFull = "queue_full"
	// DropReasonShutdown: the event was still queued, or came in, when the
	// server stopped.
	DropReasonShutdown = "shutdown"
)

// dropWarnInterval is the shortest time between two drop warnings of one key.
const dropWarnInterval = time.Minute

// notifierMeter is the OTel meter scope of the notification channels.
var notifierMeter = otel.Meter("notifier")

// droppedEvents counts the events that no channel got, by event type,
// channel ID (empty for the intake queue) and reason.
var droppedEvents metric.Int64Counter

func init() {
	var err error
	droppedEvents, err = notifierMeter.Int64Counter(
		"notification.channel.dropped",
		metric.WithDescription("Notification channel events that were not delivered, by event type, channel and reason"),
		metric.WithUnit("{event}"),
	)
	if err != nil {
		otel.Handle(err)
	}
}

type dropKey struct {
	eventType string
	channelID string
	reason    string
}

type dropCount struct {
	lastWarn time.Time
	pending  int64
}

type dropWarning struct {
	key   dropKey
	count int64
}

// dropLog counts dropped events and logs at most one warning per key per
// dropWarnInterval, with the drops since the last warning (#649). The zero
// value is ready to use.
type dropLog struct {
	counter metric.Int64Counter // nil: droppedEvents
	logger  *slog.Logger        // nil: slog.Default()
	now     func() time.Time    // nil: time.Now
	keys    map[dropKey]*dropCount
	mu      sync.Mutex
}

// record counts one dropped event. It logs the drops of each key whose last
// warning is older than dropWarnInterval, so the count of a key that stops
// dropping comes with the next drop of any key (or with flush).
func (l *dropLog) record(eventType, channelID, reason string) {
	counter := l.counter
	if counter == nil {
		counter = droppedEvents
	}
	if counter != nil {
		counter.Add(context.Background(), 1, metric.WithAttributes(
			attribute.String("event_type", eventType),
			attribute.String("channel_id", channelID),
			attribute.String("reason", reason),
		))
	}

	now := time.Now
	if l.now != nil {
		now = l.now
	}
	t := now()

	l.mu.Lock()
	if l.keys == nil {
		l.keys = make(map[dropKey]*dropCount)
	}
	key := dropKey{eventType: eventType, channelID: channelID, reason: reason}
	c := l.keys[key]
	if c == nil {
		c = &dropCount{}
		l.keys[key] = c
	}
	c.pending++
	var due []dropWarning
	for k, v := range l.keys {
		if !v.lastWarn.IsZero() && t.Sub(v.lastWarn) < dropWarnInterval {
			continue
		}
		if v.pending == 0 {
			// Same effect as no entry: the next drop warns at once.
			delete(l.keys, k)
			continue
		}
		due = append(due, dropWarning{key: k, count: v.pending})
		v.pending = 0
		v.lastWarn = t
	}
	l.mu.Unlock()

	for _, w := range due {
		l.warn(w)
	}
}

// flush logs the drops that no warning has reported yet. Stop calls it.
func (l *dropLog) flush() {
	l.mu.Lock()
	var due []dropWarning
	for k, v := range l.keys {
		if v.pending > 0 {
			due = append(due, dropWarning{key: k, count: v.pending})
		}
	}
	l.keys = nil
	l.mu.Unlock()

	for _, w := range due {
		l.warn(w)
	}
}

func (l *dropLog) warn(w dropWarning) {
	logger := l.logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Warn("notification channel: events dropped",
		"event", w.key.eventType, "channel_id", w.key.channelID,
		"reason", w.key.reason, "count", w.count)
}

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
	// DropReasonChannelRemoved: the channel was deleted or disabled while
	// the event waited in its queue.
	DropReasonChannelRemoved = "channel_removed"
)

// warnInterval is the shortest time between two warnings of one key, and
// between two sweeps of all keys.
const warnInterval = time.Minute

const (
	msgEventsDropped  = "notification channel: events dropped"
	msgDeliveryFailed = "notification channel: delivery failed"
	msgLookupFailed   = "notification channel: channel lookup failed, sending with the queued copy"
)

// notifierMeter is the OTel meter scope of the notification channels.
var notifierMeter = otel.Meter("notifier")

// droppedEvents counts the events that a channel did not get, by event
// type, channel ID (empty for the intake queue) and reason.
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

// RecordIntakeDrop counts one event that the intake queue of the notifier
// dropped (channel_id empty). See recordDrop.
func (d *Dispatcher) RecordIntakeDrop(eventType, reason string) {
	d.recordDrop(eventType, "", "", reason)
}

// recordDrop adds one to notification.channel.dropped and logs at most one
// warning per channel and reason per minute, with the drops since the last
// warning (#649).
func (d *Dispatcher) recordDrop(eventType, channelID, channelName, reason string) {
	counter := d.dropCounter
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
	d.drops.add(channelID+"\x00"+reason, msgEventsDropped,
		"channel", channelName, "channel_id", channelID, "reason", reason)
}

// limitedLog writes at most one warning per key per warnInterval. Each
// warning has the attributes of the latest occurrence and the count since
// the last warning. The zero value is ready to use.
type limitedLog struct {
	nextSweep time.Time
	logger    *slog.Logger     // nil: slog.Default()
	now       func() time.Time // nil: time.Now
	entries   map[string]*limitedEntry
	mu        sync.Mutex
}

type limitedEntry struct {
	lastWarn time.Time
	msg      string
	attrs    []any
	pending  int64
}

type limitedLine struct {
	msg   string
	attrs []any
	count int64
}

// add counts one occurrence of key. It warns when the last warning of key is
// older than warnInterval. At most once per warnInterval it also warns for
// the other keys whose window has passed, so a key that stopped still
// reports its count, and it removes the keys without a pending count.
func (l *limitedLog) add(key, msg string, attrs ...any) {
	now := time.Now
	if l.now != nil {
		now = l.now
	}
	t := now()

	l.mu.Lock()
	if l.entries == nil {
		l.entries = make(map[string]*limitedEntry)
	}
	e := l.entries[key]
	if e == nil {
		e = &limitedEntry{}
		l.entries[key] = e
	}
	e.pending++
	e.msg, e.attrs = msg, attrs
	var due []limitedLine
	if e.lastWarn.IsZero() || t.Sub(e.lastWarn) >= warnInterval {
		due = append(due, e.take(t))
	}
	if !t.Before(l.nextSweep) {
		l.nextSweep = t.Add(warnInterval)
		for k, v := range l.entries {
			if v == e || t.Sub(v.lastWarn) < warnInterval {
				continue
			}
			if v.pending == 0 {
				delete(l.entries, k)
				continue
			}
			due = append(due, v.take(t))
		}
	}
	l.mu.Unlock()

	l.write(due)
}

func (e *limitedEntry) take(t time.Time) limitedLine {
	line := limitedLine{msg: e.msg, attrs: e.attrs, count: e.pending}
	e.pending = 0
	e.lastWarn = t
	return line
}

// flush warns for every key with a pending count. Stop calls it.
func (l *limitedLog) flush() {
	l.mu.Lock()
	var due []limitedLine
	for _, v := range l.entries {
		if v.pending > 0 {
			due = append(due, limitedLine{msg: v.msg, attrs: v.attrs, count: v.pending})
		}
	}
	l.entries = nil
	l.nextSweep = time.Time{}
	l.mu.Unlock()

	l.write(due)
}

func (l *limitedLog) write(lines []limitedLine) {
	if len(lines) == 0 {
		return
	}
	logger := l.logger
	if logger == nil {
		logger = slog.Default()
	}
	for _, line := range lines {
		args := make([]any, 0, len(line.attrs)+2)
		args = append(args, line.attrs...)
		args = append(args, "count", line.count)
		logger.Warn(line.msg, args...)
	}
}

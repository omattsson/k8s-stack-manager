package websocket

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// wsMeter is the OTel meter scope for WebSocket operations.
var wsMeter = otel.Meter("websocket")

// wsMetrics holds pre-created metric instruments for the WebSocket hub.
type wsMetrics struct {
	connectionsActive metric.Int64UpDownCounter
	messagesSentTotal metric.Int64Counter
}

var hubMetrics wsMetrics

func init() {
	var err error

	hubMetrics.connectionsActive, err = wsMeter.Int64UpDownCounter(
		"websocket.connections_active",
		metric.WithDescription("Number of currently active WebSocket connections"),
		metric.WithUnit("{connection}"),
	)
	if err != nil {
		otel.Handle(err)
	}

	hubMetrics.messagesSentTotal, err = wsMeter.Int64Counter(
		"websocket.messages_sent_total",
		metric.WithDescription("Total number of WebSocket messages sent to clients"),
		metric.WithUnit("{message}"),
	)
	if err != nil {
		otel.Handle(err)
	}
}

// fanoutInstruments holds the metric instruments of the WebSocket fan-out
// between replicas (ws_events).
type fanoutInstruments struct {
	written    metric.Int64Counter
	droppedCtr metric.Int64Counter
	delivered  metric.Int64Counter
	pollErrors metric.Int64Counter
	skippedCtr metric.Int64Counter
	lag        metric.Float64Histogram
}

var fanoutMetrics fanoutInstruments

// dropped counts n messages that were not written, by reason (buffer_full,
// too_large, write_error).
func (m fanoutInstruments) dropped(reason string, n int64) {
	m.droppedCtr.Add(context.Background(), n, metric.WithAttributes(attribute.String("reason", reason)))
}

// skipped counts one row of another replica that was not delivered, by
// reason (stale, unknown_target).
func (m fanoutInstruments) skipped(reason string) {
	m.skippedCtr.Add(context.Background(), 1, metric.WithAttributes(attribute.String("reason", reason)))
}

func init() {
	var err error

	fanoutMetrics.skippedCtr, err = wsMeter.Int64Counter(
		"websocket.fanout.events_skipped_total",
		metric.WithDescription("ws_events rows from other replicas not delivered, by reason"),
		metric.WithUnit("{event}"),
	)
	if err != nil {
		otel.Handle(err)
	}

	fanoutMetrics.written, err = wsMeter.Int64Counter(
		"websocket.fanout.events_written_total",
		metric.WithDescription("WebSocket messages written to ws_events for the other replicas"),
		metric.WithUnit("{event}"),
	)
	if err != nil {
		otel.Handle(err)
	}

	fanoutMetrics.droppedCtr, err = wsMeter.Int64Counter(
		"websocket.fanout.events_dropped_total",
		metric.WithDescription("WebSocket messages not shared with the other replicas, by reason"),
		metric.WithUnit("{event}"),
	)
	if err != nil {
		otel.Handle(err)
	}

	fanoutMetrics.delivered, err = wsMeter.Int64Counter(
		"websocket.fanout.events_delivered_total",
		metric.WithDescription("ws_events rows from other replicas delivered to local clients"),
		metric.WithUnit("{event}"),
	)
	if err != nil {
		otel.Handle(err)
	}

	fanoutMetrics.pollErrors, err = wsMeter.Int64Counter(
		"websocket.fanout.poll_errors_total",
		metric.WithDescription("Failed ws_events poll queries"),
		metric.WithUnit("{error}"),
	)
	if err != nil {
		otel.Handle(err)
	}

	fanoutMetrics.lag, err = wsMeter.Float64Histogram(
		"websocket.fanout.delivery_lag",
		metric.WithDescription("Time from the broadcast on the origin replica to the delivery on this replica"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(0.1, 0.25, 0.5, 0.75, 1, 2, 5, 10),
	)
	if err != nil {
		otel.Handle(err)
	}
}

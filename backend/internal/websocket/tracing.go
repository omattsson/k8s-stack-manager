package websocket

import (
	"go.opentelemetry.io/otel"
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

package podstream

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// KindPortForward is the kind label of the port-forward sessions.
const KindPortForward = "port_forward"

// Directions of stack_stream.bytes_total.
const (
	DirectionClientToPod = "client_to_pod"
	DirectionPodToClient = "pod_to_client"
)

var streamMeter = otel.Meter("podstream")

type streamInstruments struct {
	sessionsActive metric.Int64UpDownCounter
	bytesTotal     metric.Int64Counter
	sessionsClosed metric.Int64Counter
	rejected       metric.Int64Counter
}

var instruments streamInstruments

func init() {
	var err error
	instruments.sessionsActive, err = streamMeter.Int64UpDownCounter(
		"stack_stream.sessions_active",
		metric.WithDescription("Number of open pod stream sessions"),
		metric.WithUnit("{session}"))
	if err != nil {
		otel.Handle(err)
	}
	instruments.bytesTotal, err = streamMeter.Int64Counter(
		"stack_stream.bytes_total",
		metric.WithDescription("Bytes copied through pod stream sessions"),
		metric.WithUnit("By"))
	if err != nil {
		otel.Handle(err)
	}
	instruments.sessionsClosed, err = streamMeter.Int64Counter(
		"stack_stream.sessions_closed_total",
		metric.WithDescription("Closed pod stream sessions by reason"),
		metric.WithUnit("{session}"))
	if err != nil {
		otel.Handle(err)
	}
	instruments.rejected, err = streamMeter.Int64Counter(
		"stack_stream.rejected_total",
		metric.WithDescription("Pod stream requests refused before the upgrade, by reason"),
		metric.WithUnit("{request}"))
	if err != nil {
		otel.Handle(err)
	}
}

func kindAttr(kind string) attribute.KeyValue { return attribute.String("kind", kind) }

// RecordRejected counts a request that was refused before the upgrade.
func RecordRejected(kind, reason string) {
	if instruments.rejected == nil {
		return
	}
	instruments.rejected.Add(context.Background(), 1,
		metric.WithAttributes(kindAttr(kind), attribute.String("reason", reason)))
}

func recordActive(kind string, delta int64) {
	if instruments.sessionsActive == nil {
		return
	}
	instruments.sessionsActive.Add(context.Background(), delta, metric.WithAttributes(kindAttr(kind)))
}

func recordBytes(kind, direction string, n int64) {
	if instruments.bytesTotal == nil || n <= 0 {
		return
	}
	instruments.bytesTotal.Add(context.Background(), n,
		metric.WithAttributes(kindAttr(kind), attribute.String("direction", direction)))
}

func recordClosed(kind, reason string) {
	if instruments.sessionsClosed == nil {
		return
	}
	instruments.sessionsClosed.Add(context.Background(), 1,
		metric.WithAttributes(kindAttr(kind), attribute.String("reason", reason)))
}

// closeReasonLabel maps a close code to the reason label of
// stack_stream.sessions_closed_total.
func closeReasonLabel(code int) string {
	switch code {
	case 1000:
		return "normal"
	case 1001:
		return "shutdown"
	case 1003:
		return "bad_frame"
	case 1006:
		return "client_gone"
	case 1008:
		return "revoked"
	case CloseMaxDuration:
		return "max_duration"
	case CloseIdleTimeout:
		return "idle_timeout"
	case ClosePodError:
		return "pod_error"
	default:
		return "other"
	}
}

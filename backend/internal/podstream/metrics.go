package podstream

import (
	"context"
	"sync/atomic"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// KindPortForward is the kind label of the port-forward sessions.
const KindPortForward = "port_forward"

// KindExec is the kind label of the exec sessions.
const KindExec = "exec"

// Directions of stack_stream.bytes_total.
const (
	DirectionClientToPod = "client_to_pod"
	DirectionPodToClient = "pod_to_client"
)

type streamInstruments struct {
	sessionsActive metric.Int64UpDownCounter
	bytesTotal     metric.Int64Counter
	sessionsClosed metric.Int64Counter
	rejected       metric.Int64Counter
	execExit       metric.Int64Counter
}

// instruments holds the current instruments. It is replaced by
// UseMeterProvider (tests); the record functions read it atomically.
var instruments atomic.Pointer[streamInstruments]

func init() { UseMeterProvider(otel.GetMeterProvider()) }

// UseMeterProvider creates the stream instruments on mp. The package calls it
// with the global provider at start; tests call it with a provider that has a
// reader, because the global provider delegates only once per process.
func UseMeterProvider(mp metric.MeterProvider) {
	meter := mp.Meter("podstream")
	var in streamInstruments
	var err error
	in.sessionsActive, err = meter.Int64UpDownCounter(
		"stack_stream.sessions_active",
		metric.WithDescription("Number of open pod stream sessions"),
		metric.WithUnit("{session}"))
	if err != nil {
		otel.Handle(err)
	}
	in.bytesTotal, err = meter.Int64Counter(
		"stack_stream.bytes_total",
		metric.WithDescription("Bytes copied through pod stream sessions"),
		metric.WithUnit("By"))
	if err != nil {
		otel.Handle(err)
	}
	in.sessionsClosed, err = meter.Int64Counter(
		"stack_stream.sessions_closed_total",
		metric.WithDescription("Closed pod stream sessions by reason"),
		metric.WithUnit("{session}"))
	if err != nil {
		otel.Handle(err)
	}
	in.rejected, err = meter.Int64Counter(
		"stack_stream.rejected_total",
		metric.WithDescription("Pod stream requests refused before the upgrade, by reason"),
		metric.WithUnit("{request}"))
	if err != nil {
		otel.Handle(err)
	}
	in.execExit, err = meter.Int64Counter(
		"stack_stream.exec_exit_total",
		metric.WithDescription("Finished exec commands by result (zero, nonzero, error, timeout)"),
		metric.WithUnit("{command}"))
	if err != nil {
		otel.Handle(err)
	}
	instruments.Store(&in)
}

func kindAttr(kind string) attribute.KeyValue { return attribute.String("kind", kind) }

// RecordRejected counts a request that was refused before the upgrade.
func RecordRejected(kind, reason string) {
	in := instruments.Load()
	if in == nil || in.rejected == nil {
		return
	}
	in.rejected.Add(context.Background(), 1,
		metric.WithAttributes(kindAttr(kind), attribute.String("reason", reason)))
}

// recordExecExit counts a finished exec command.
func recordExecExit(result string) {
	in := instruments.Load()
	if in == nil || in.execExit == nil {
		return
	}
	in.execExit.Add(context.Background(), 1, metric.WithAttributes(attribute.String("result", result)))
}

func recordActive(kind string, delta int64) {
	in := instruments.Load()
	if in == nil || in.sessionsActive == nil {
		return
	}
	in.sessionsActive.Add(context.Background(), delta, metric.WithAttributes(kindAttr(kind)))
}

func recordBytes(kind, direction string, n int64) {
	in := instruments.Load()
	if in == nil || in.bytesTotal == nil || n <= 0 {
		return
	}
	in.bytesTotal.Add(context.Background(), n,
		metric.WithAttributes(kindAttr(kind), attribute.String("direction", direction)))
}

func recordClosed(kind, reason string) {
	in := instruments.Load()
	if in == nil || in.sessionsClosed == nil {
		return
	}
	in.sessionsClosed.Add(context.Background(), 1,
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
	case CloseMessageTooBig:
		return "message_too_big"
	case CloseInternalError:
		return "internal_error"
	case CloseMaxDuration:
		return "max_duration"
	case CloseIdleTimeout:
		return "idle_timeout"
	case ClosePodError:
		return "pod_error"
	case CloseBadStart:
		return "bad_start"
	case CloseStartTimeout:
		return "start_timeout"
	default:
		return "other"
	}
}

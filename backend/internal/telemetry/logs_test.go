package telemetry_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"backend/internal/telemetry"

	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memLogExporter keeps the exported OTel log records in memory.
type memLogExporter struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (e *memLogExporter) Export(_ context.Context, records []sdklog.Record) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range records {
		e.records = append(e.records, r.Clone())
	}
	return nil
}

func (e *memLogExporter) Shutdown(context.Context) error   { return nil }
func (e *memLogExporter) ForceFlush(context.Context) error { return nil }

func (e *memLogExporter) bodies() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.records))
	for _, r := range e.records {
		out = append(out, r.Body().AsString())
	}
	return out
}

func (e *memLogExporter) find(body string) (sdklog.Record, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range e.records {
		if r.Body().AsString() == body {
			return r.Clone(), true
		}
	}
	return sdklog.Record{}, false
}

// newLogTelemetry returns a Telemetry whose LoggerProvider exports
// synchronously to an in-memory exporter.
func newLogTelemetry(t *testing.T) (*telemetry.Telemetry, *memLogExporter) {
	t.Helper()
	exp := &memLogExporter{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exp)))
	t.Cleanup(func() { _ = lp.Shutdown(context.Background()) })
	return &telemetry.Telemetry{LoggerProvider: lp}, exp
}

func TestLogHandler_WithoutLoggerProviderReturnsBase(t *testing.T) {
	t.Parallel()

	base := slog.NewTextHandler(&bytes.Buffer{}, nil)
	tests := []struct {
		name string
		tel  *telemetry.Telemetry
	}{
		{name: "nil telemetry", tel: nil},
		{name: "no logger provider (OTEL_ENABLED=false)", tel: &telemetry.Telemetry{}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Same(t, base, tt.tel.LogHandler(base, slog.LevelInfo))
		})
	}
}

func TestLogHandler_SendsRecordsToBaseAndOTel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		level     slog.Level
		logLevel  slog.Level
		wantOTel  bool
		wantLocal bool
	}{
		{name: "info at level info", level: slog.LevelInfo, logLevel: slog.LevelInfo, wantOTel: true, wantLocal: true},
		{name: "error at level info", level: slog.LevelInfo, logLevel: slog.LevelError, wantOTel: true, wantLocal: true},
		{name: "debug below level info", level: slog.LevelInfo, logLevel: slog.LevelDebug, wantOTel: false, wantLocal: false},
		{name: "debug at level debug", level: slog.LevelDebug, logLevel: slog.LevelDebug, wantOTel: true, wantLocal: true},
		{name: "warn below level error", level: slog.LevelError, logLevel: slog.LevelWarn, wantOTel: false, wantLocal: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tel, exp := newLogTelemetry(t)
			var local bytes.Buffer
			base := slog.NewTextHandler(&local, &slog.HandlerOptions{Level: tt.level})
			logger := slog.New(tel.LogHandler(base, tt.level))

			logger.Log(context.Background(), tt.logLevel, "the message", "key", "value")

			_, gotOTel := exp.find("the message")
			assert.Equal(t, tt.wantOTel, gotOTel, "OTel record; exported: %v", exp.bodies())
			assert.Equal(t, tt.wantLocal, strings.Contains(local.String(), "the message"), "local output: %q", local.String())
		})
	}
}

func TestLogHandler_KeepsAttributesGroupsAndTraceContext(t *testing.T) {
	t.Parallel()

	tel, exp := newLogTelemetry(t)
	var local bytes.Buffer
	logger := slog.New(tel.LogHandler(slog.NewTextHandler(&local, nil), slog.LevelInfo)).
		With("component", "test").
		WithGroup("req")

	traceID, err := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("0102030405060708")
	require.NoError(t, err)
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled,
	}))

	logger.InfoContext(ctx, "with context", "id", "42")

	rec, ok := exp.find("with context")
	require.True(t, ok, "exported: %v", exp.bodies())
	assert.Equal(t, otellog.SeverityInfo, rec.Severity())
	assert.Equal(t, traceID, rec.TraceID(), "the record carries the trace of the context")
	assert.Equal(t, spanID, rec.SpanID())

	attrs := map[string]attribute.Value{}
	rec.WalkAttributes(func(kv attribute.KeyValue) bool {
		attrs[string(kv.Key)] = kv.Value
		return true
	})
	assert.Equal(t, "test", attrs["component"].AsString())
	req, ok := attrs["req"]
	require.True(t, ok, "the group is an attribute; got %v", attrs)
	require.Equal(t, attribute.MAP, req.Type())
	group := map[string]string{}
	for _, kv := range req.AsMap() {
		group[string(kv.Key)] = kv.Value.AsString()
	}
	assert.Equal(t, map[string]string{"id": "42"}, group)

	assert.Contains(t, local.String(), "component=test")
	assert.Contains(t, local.String(), "req.id=42")
}

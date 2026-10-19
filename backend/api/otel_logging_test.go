package main

import (
	"bytes"
	"context"
	"log"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/config"
	"backend/internal/telemetry"

	sdklog "go.opentelemetry.io/otel/sdk/log"

	"github.com/stretchr/testify/assert"
)

// otelLogSink keeps the bodies of the exported OTel log records.
type otelLogSink struct {
	mu     sync.Mutex
	bodies []string
}

func (s *otelLogSink) Export(_ context.Context, records []sdklog.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range records {
		s.bodies = append(s.bodies, r.Body().AsString())
	}
	return nil
}

func (s *otelLogSink) Shutdown(context.Context) error   { return nil }
func (s *otelLogSink) ForceFlush(context.Context) error { return nil }

func (s *otelLogSink) has(body string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.bodies {
		if strings.Contains(b, body) {
			return true
		}
	}
	return false
}

// TestSetupOTelLogging changes the process-wide default logger, so it does
// not run in parallel and restores the defaults afterwards.
func TestSetupOTelLogging(t *testing.T) {
	prevDefault := slog.Default()
	prevWriter := log.Writer()
	prevFlags := log.Flags()
	t.Cleanup(func() {
		slog.SetDefault(prevDefault)
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})

	t.Run("without an OTel log pipeline the default logger stays", func(t *testing.T) {
		before := slog.Default()
		setupOTelLogging(&telemetry.Telemetry{}, config.LogConfig{Level: "debug"})
		setupOTelLogging(nil, config.LogConfig{Level: "debug"})
		assert.Same(t, before, slog.Default())
	})

	t.Run("with an OTel log pipeline the logs go to both, with LOG_LEVEL", func(t *testing.T) {
		// Start from the package default handler, as main does.
		slog.SetDefault(prevDefault)

		sink := &otelLogSink{}
		lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(sink)))
		t.Cleanup(func() { _ = lp.Shutdown(context.Background()) })

		var local lockedBuffer
		setupOTelLoggingTo(&telemetry.Telemetry{LoggerProvider: lp}, config.LogConfig{Level: "warn"}, &local)

		// The log package writes through the new default logger. That must
		// not deadlock (it did when the package default handler was
		// wrapped): run the calls with a deadline.
		done := make(chan struct{})
		go func() {
			defer close(done)
			slog.Warn("warn record", "k", "v")
			slog.Info("info record below the level")
			log.Print("log package record")
			slog.Error("error record")
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("logging through the new default logger blocked")
		}

		out := local.String()
		assert.Contains(t, out, `msg="warn record" k=v`, "local output keeps the record")
		assert.Contains(t, out, "level=WARN")
		assert.True(t, sink.has("warn record"), "OTel gets the record; got %v", sink.bodies)
		assert.True(t, sink.has("error record"))
		assert.NotContains(t, out, "info record below the level")
		assert.False(t, sink.has("info record below the level"), "LOG_LEVEL applies to OTel")
		// log.Print goes through the slog default at level info, below warn.
		assert.NotContains(t, out, "log package record")
		assert.False(t, sink.has("log package record"))
	})

	t.Run("log package records reach both outputs at level info", func(t *testing.T) {
		slog.SetDefault(prevDefault)
		sink := &otelLogSink{}
		lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(sink)))
		t.Cleanup(func() { _ = lp.Shutdown(context.Background()) })

		var local lockedBuffer
		setupOTelLoggingTo(&telemetry.Telemetry{LoggerProvider: lp}, config.LogConfig{Level: "info"}, &local)

		done := make(chan struct{})
		go func() {
			defer close(done)
			log.Print("log package record")
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("log.Print through the new default logger blocked")
		}
		assert.Contains(t, local.String(), "log package record")
		assert.True(t, sink.has("log package record"), "got %v", sink.bodies)
	})
}

// lockedBuffer is a bytes.Buffer that is safe for concurrent writes.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

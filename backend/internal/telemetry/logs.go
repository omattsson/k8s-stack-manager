package telemetry

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/contrib/bridges/otelslog"
)

// logInstrumentationName is the instrumentation scope of the backend logs in
// the OTel log pipeline.
const logInstrumentationName = "backend"

// LogHandler returns the slog handler for the default logger.
//
// base must not be the package default handler of log/slog (what
// slog.Default().Handler() returns before any slog.SetDefault): it writes
// through the log package, which writes through the new default logger again
// and deadlocks. Use a handler with its own writer, for example
// slog.NewTextHandler(os.Stderr, ...).
//
// Without an OTel log pipeline (OTEL_ENABLED=false), it returns base
// unchanged, so the log output stays as it is. With the pipeline, it returns
// a handler that writes each record to base and also sends it to the OTel
// LoggerProvider (OTLP to the collector). The level applies to both: a record
// below it goes nowhere. Records logged with a context (slog.InfoContext and
// so on) carry the trace and span ID of that context in OTel.
func (t *Telemetry) LogHandler(base slog.Handler, level slog.Leveler) slog.Handler {
	if t == nil || t.LoggerProvider == nil {
		return base
	}
	bridge := otelslog.NewHandler(logInstrumentationName, otelslog.WithLoggerProvider(t.LoggerProvider))
	return slog.NewMultiHandler(base, &levelHandler{Handler: bridge, level: level})
}

// levelHandler drops records below level before they reach the wrapped
// handler. The otelslog bridge has no level option of its own.
type levelHandler struct {
	slog.Handler
	level slog.Leveler
}

// Enabled reports whether the level is enabled and the wrapped handler wants
// the record.
func (h *levelHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= h.level.Level() && h.Handler.Enabled(ctx, l)
}

// WithAttrs keeps the level filter on the derived handler.
func (h *levelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &levelHandler{Handler: h.Handler.WithAttrs(attrs), level: h.level}
}

// WithGroup keeps the level filter on the derived handler.
func (h *levelHandler) WithGroup(name string) slog.Handler {
	return &levelHandler{Handler: h.Handler.WithGroup(name), level: h.level}
}
